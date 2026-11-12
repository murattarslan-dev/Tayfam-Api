package integrity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/googleauth"
)

// Play Integrity (ADR-0022 §3). The endpoint is fixed here and never comes
// from configuration.
const (
	// PlayScope is the only scope the integrity service account's token
	// gets.
	PlayScope = "https://www.googleapis.com/auth/playintegrity"
	decodeURL = "https://playintegrity.googleapis.com/v1/%s:decodeIntegrityToken"

	// MaxPlayToken bounds a token before any work; real ones are a few KB.
	MaxPlayToken = 16 << 10
	// maxAge is how old a token may be (requestDetails.timestampMillis);
	// maxSkew tolerates a device clock slightly ahead of ours.
	maxAge  = 2 * time.Minute
	maxSkew = time.Minute
	maxBody = 64 << 10
)

// Device labels (deviceIntegrity.deviceRecognitionVerdict). The required one
// is configured; STRONG is the platform owner's choice (2026-11-12).
const (
	LabelBasic  = "MEETS_BASIC_INTEGRITY"
	LabelDevice = "MEETS_DEVICE_INTEGRITY"
	LabelStrong = "MEETS_STRONG_INTEGRITY"
)

var (
	packagePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)
	// A SHA-256 certificate digest as Play reports it: base64url, no padding.
	digestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// TokenSource gives access tokens for the Play Integrity scope
// (googleauth.TokenSource).
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	Forget(access string)
}

// PlayConfig wires a Play verifier.
type PlayConfig struct {
	Tokens TokenSource
	// Package is the Android application ID.
	Package string
	// CertDigests are the SHA-256 digests of the app signing certificate
	// (Play App Signing): base64url without padding, as Play reports them,
	// or hex as Play Console shows them ("AB:CD:…", colons optional).
	CertDigests []string
	// DeviceLabel is the device level a session needs.
	DeviceLabel string
	// Transport is for tests (a fake Google); nil uses the default.
	Transport http.RoundTripper
	Now       func() time.Time
}

// Play decodes Play Integrity tokens through Google and judges them.
type Play struct {
	cfg  PlayConfig
	http *http.Client
}

// NewPlay returns a Play verifier.
func NewPlay(cfg PlayConfig) (*Play, error) {
	switch {
	case cfg.Tokens == nil:
		return nil, errors.New("integrity: token kaynağı yok")
	case !packagePattern.MatchString(cfg.Package) || len(cfg.Package) > 255:
		return nil, errors.New("integrity: geçersiz paket adı")
	case len(cfg.CertDigests) == 0:
		return nil, errors.New("integrity: imza sertifikası özeti yok")
	case cfg.DeviceLabel != LabelBasic && cfg.DeviceLabel != LabelDevice && cfg.DeviceLabel != LabelStrong:
		return nil, errors.New("integrity: geçersiz cihaz seviyesi")
	}
	digests := make([]string, 0, len(cfg.CertDigests))
	for _, d := range cfg.CertDigests {
		n, ok := normalizeDigest(d)
		if !ok {
			return nil, errors.New("integrity: geçersiz imza sertifikası özeti")
		}
		digests = append(digests, n)
	}
	cfg.CertDigests = digests
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Play{cfg: cfg, http: googleauth.NewHTTPClient(cfg.Transport)}, nil
}

// payload is the part of tokenPayloadExternal the decision reads. Google
// writes int64 fields as JSON strings.
type payload struct {
	RequestDetails struct {
		RequestPackageName string          `json:"requestPackageName"`
		RequestHash        string          `json:"requestHash"`
		TimestampMillis    json.RawMessage `json:"timestampMillis"`
	} `json:"requestDetails"`
	AppIntegrity struct {
		AppRecognitionVerdict   string   `json:"appRecognitionVerdict"`
		PackageName             string   `json:"packageName"`
		CertificateSha256Digest []string `json:"certificateSha256Digest"`
	} `json:"appIntegrity"`
	DeviceIntegrity struct {
		DeviceRecognitionVerdict []string `json:"deviceRecognitionVerdict"`
	} `json:"deviceIntegrity"`
	AccountDetails struct {
		AppLicensingVerdict string `json:"appLicensingVerdict"`
	} `json:"accountDetails"`
}

// Verify decodes token through Google and judges it against binding.
// Licensing is the app licensing verdict, for counting only (ADR-0022 §3).
func (p *Play) Verify(ctx context.Context, token, binding string) (r Result, licensing string) {
	if token == "" || len(token) > MaxPlayToken {
		return Result{Status: StatusFailed, Reason: ReasonMalformed}, ""
	}
	pl, res, ok := p.decode(ctx, token)
	if !ok {
		return res, ""
	}
	return p.judge(pl, binding), licensingOf(pl)
}

// decode asks Google. ok is false with the result to use when there is no
// payload to judge.
func (p *Play) decode(ctx context.Context, token string) (payload, Result, bool) {
	unavailable := Result{Reason: ReasonUnavailable}
	access, err := p.cfg.Tokens.Token(ctx)
	if err != nil {
		return payload{}, unavailable, false
	}
	body, err := json.Marshal(map[string]string{"integrity_token": token})
	if err != nil {
		return payload{}, Result{Status: StatusFailed, Reason: ReasonMalformed}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf(decodeURL, url.PathEscape(p.cfg.Package)), bytes.NewReader(body))
	if err != nil {
		return payload{}, unavailable, false
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return payload{}, unavailable, false
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest:
		// Google could not read the token: not a real one, or garbled.
		return payload{}, Result{Status: StatusFailed, Reason: ReasonMalformed}, false
	case http.StatusUnauthorized:
		p.cfg.Tokens.Forget(access)
		return payload{}, unavailable, false
	default:
		return payload{}, unavailable, false
	}
	var out struct {
		TokenPayloadExternal *payload `json:"tokenPayloadExternal"`
	}
	if json.Unmarshal(raw, &out) != nil || out.TokenPayloadExternal == nil {
		return payload{}, Result{Status: StatusFailed, Reason: ReasonMalformed}, false
	}
	return *out.TokenPayloadExternal, Result{}, true
}

// judge applies ADR-0022 §3 in order: the request first (a token for
// another request says nothing about this one), then the app, then the
// device.
func (p *Play) judge(pl payload, binding string) Result {
	rd := pl.RequestDetails
	ts, ok := millis(rd.TimestampMillis)
	now := p.cfg.Now()
	if rd.RequestPackageName != p.cfg.Package || rd.RequestHash != binding || !ok {
		return Result{Status: StatusFailed, Reason: ReasonBinding}
	}
	at := time.UnixMilli(ts)
	if now.Sub(at) > maxAge || at.Sub(now) > maxSkew {
		return Result{Status: StatusFailed, Reason: ReasonBinding}
	}
	ai := pl.AppIntegrity
	if ai.AppRecognitionVerdict != "PLAY_RECOGNIZED" || ai.PackageName != p.cfg.Package || !slices.ContainsFunc(ai.CertificateSha256Digest, func(d string) bool {
		return slices.Contains(p.cfg.CertDigests, d)
	}) {
		return Result{Status: StatusFailed, Reason: ReasonAppUnrecognized}
	}
	if !slices.Contains(pl.DeviceIntegrity.DeviceRecognitionVerdict, p.cfg.DeviceLabel) {
		return Result{Status: StatusFailed, Reason: ReasonDevice}
	}
	return Result{Status: StatusPassed}
}

// normalizeDigest returns a SHA-256 digest in Play's form (base64url, no
// padding) from that form or from hex with optional colons.
func normalizeDigest(d string) (string, bool) {
	if digestPattern.MatchString(d) {
		return d, true
	}
	h := strings.ReplaceAll(d, ":", "")
	if len(h) != 2*sha256Size {
		return "", false
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return "", false
	}
	return base64.RawURLEncoding.EncodeToString(b), true
}

// sha256Size is the length of a SHA-256 digest in bytes.
const sha256Size = 32

// millis reads an int64 that Google may write as a string or a number.
func millis(raw json.RawMessage) (int64, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil && n > 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		return n, true
	}
	return 0, false
}

// licensingOf keeps the licensing verdict only if it is one of Google's
// values, so a log line never carries anything else.
func licensingOf(pl payload) string {
	switch v := pl.AccountDetails.AppLicensingVerdict; v {
	case "LICENSED", "UNLICENSED", "UNEVALUATED":
		return v
	}
	return ""
}
