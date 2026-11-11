// Package fcm is a minimal Firebase Cloud Messaging HTTP v1 client built on
// the standard library only (ADR-0019 item 6).
//
// It signs a short JWT with the service account's RSA key (RS256 through
// crypto/rsa.SignPKCS1v15: the standard library's signature, not a scheme
// of our own; CLAUDE.md rule 4), exchanges it at Google's OAuth 2.0 token
// endpoint for an access token limited to the firebase.messaging scope,
// keeps that token in memory until shortly before it expires, and posts
// one message per call to messages:send.
//
// The endpoints are fixed here and never come from configuration;
// redirects are not followed. The service account key, the signed JWT, the
// access token, push tokens and response bodies never reach an error
// message or a log: errors carry HTTP status codes and FCM error codes
// only.
package fcm

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push"
)

// Fixed endpoints (ADR-0019 item 6).
const (
	TokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // an endpoint address, not a credential
	sendURL  = "https://fcm.googleapis.com/v1/projects/%s/messages:send"
	scope    = "https://www.googleapis.com/auth/firebase.messaging"

	assertionLifetime = time.Hour
	// refreshBefore: an access token is replaced this long before it
	// expires.
	refreshBefore  = 5 * time.Minute
	requestTimeout = 10 * time.Second
	maxBody        = 64 << 10
)

var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// ServiceAccount is the part of a Google service account key the client
// uses. Its fields are never printed.
type ServiceAccount struct {
	projectID string
	email     string
	keyID     string
	key       *rsa.PrivateKey
}

// ProjectID is the Firebase project (not secret).
func (a *ServiceAccount) ProjectID() string { return a.projectID }

// String hides the key from fmt and slog.
func (a *ServiceAccount) String() string { return "fcm.ServiceAccount{" + a.projectID + "}" }

// errAccount is returned for any problem in the service account JSON; it
// names the field only, never its value.
func errAccount(field string) error {
	return fmt.Errorf("fcm servis hesabı geçersiz: %s", field)
}

// ParseServiceAccount reads a service account key file (JSON). Its
// token_uri must be Google's token endpoint.
func ParseServiceAccount(raw []byte) (*ServiceAccount, error) {
	var f struct {
		Type         string `json:"type"`
		ProjectID    string `json:"project_id"`
		PrivateKeyID string `json:"private_key_id"`
		PrivateKey   string `json:"private_key"`
		ClientEmail  string `json:"client_email"`
		TokenURI     string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, errAccount("json")
	}
	switch {
	case f.Type != "service_account":
		return nil, errAccount("type")
	case !projectPattern.MatchString(f.ProjectID):
		return nil, errAccount("project_id")
	case f.ClientEmail == "" || len(f.ClientEmail) > 256 || !strings.Contains(f.ClientEmail, "@"):
		return nil, errAccount("client_email")
	case f.TokenURI != "" && f.TokenURI != TokenURL:
		return nil, errAccount("token_uri")
	case len(f.PrivateKeyID) > 128:
		return nil, errAccount("private_key_id")
	}
	block, _ := pem.Decode([]byte(f.PrivateKey))
	if block == nil {
		return nil, errAccount("private_key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errAccount("private_key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok || rk.N.BitLen() < 2048 {
		return nil, errAccount("private_key")
	}
	return &ServiceAccount{projectID: f.ProjectID, email: f.ClientEmail, keyID: f.PrivateKeyID, key: rk}, nil
}

// Config wires a Client.
type Config struct {
	Account *ServiceAccount
	// Transport is for tests (a fake FCM server); nil uses the default.
	Transport http.RoundTripper
	Now       func() time.Time // optional, for tests
}

// Client sends messages to FCM. It is safe for concurrent use.
type Client struct {
	acct *ServiceAccount
	http *http.Client
	now  func() time.Time

	mu      sync.Mutex
	access  string
	expires time.Time
}

// New returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Account == nil {
		return nil, errors.New("fcm: servis hesabı yok")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	t := cfg.Transport
	if t == nil {
		t = http.DefaultTransport
	}
	return &Client{
		acct: cfg.Account, now: cfg.Now,
		http: &http.Client{
			Transport: t, Timeout: requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Send sends m. It returns nil or a *push.SendError.
func (c *Client) Send(ctx context.Context, m push.Message) error {
	body, err := json.Marshal(request{Message: message(m, c.now())})
	if err != nil {
		return &push.SendError{Kind: push.Rejected, Code: "encode"}
	}
	access, err := c.token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf(sendURL, url.PathEscape(c.acct.projectID)), bytes.NewReader(body))
	if err != nil {
		return &push.SendError{Kind: push.Rejected, Code: "request"}
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The error of Do names the URL (the project ID only); it is not
		// passed on anyway.
		return &push.SendError{Kind: push.Transient, Code: "network"}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	e := classify(resp.StatusCode, raw)
	e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"), c.now())
	if resp.StatusCode == http.StatusUnauthorized && e.Code != "THIRD_PARTY_AUTH_ERROR" {
		// Our access token was refused: get a new one next time.
		c.forget(access)
	}
	return e
}

// classify turns an FCM error response into a SendError (ADR-0019 item 5).
func classify(status int, raw []byte) *push.SendError {
	var r struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				Type            string `json:"@type"`
				ErrorCode       string `json:"errorCode"`
				FieldViolations []struct {
					Field string `json:"field"`
				} `json:"fieldViolations"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &r)
	code, tokenField := r.Error.Status, false
	for _, d := range r.Error.Details {
		if strings.HasSuffix(d.Type, "google.firebase.fcm.v1.FcmError") && d.ErrorCode != "" {
			code = d.ErrorCode
		}
		for _, v := range d.FieldViolations {
			if v.Field == "message.token" {
				tokenField = true
			}
		}
	}
	code = safeCode(code)
	e := &push.SendError{Status: status, Code: code}
	switch {
	case code == "UNREGISTERED", code == "SENDER_ID_MISMATCH", status == http.StatusNotFound:
		e.Kind = push.InvalidToken
	case code == "INVALID_ARGUMENT" && tokenField:
		e.Kind = push.InvalidToken
	case code == "THIRD_PARTY_AUTH_ERROR":
		e.Kind = push.Rejected
	case status == http.StatusUnauthorized, status == http.StatusTooManyRequests, status >= 500,
		code == "QUOTA_EXCEEDED", code == "UNAVAILABLE", code == "INTERNAL":
		e.Kind = push.Transient
	default:
		e.Kind = push.Rejected
	}
	return e
}

var codePattern = regexp.MustCompile(`^[A-Z_]{1,40}$`)

// safeCode keeps an error code only if it looks like one (never a message
// that could echo input).
func safeCode(s string) string {
	if codePattern.MatchString(s) {
		return s
	}
	return ""
}

func retryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// token returns a valid access token, fetching a new one when needed.
func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.access != "" && now.Before(c.expires.Add(-refreshBefore)) {
		return c.access, nil
	}
	assertion, err := c.assertion(now)
	if err != nil {
		return "", &push.SendError{Kind: push.Rejected, Code: "assertion"}
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", &push.SendError{Kind: push.Rejected, Code: "request"}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &push.SendError{Kind: push.Transient, Code: "oauth_network"}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK {
		return "", &push.SendError{Kind: push.Transient, Status: resp.StatusCode, Code: "oauth"}
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if json.Unmarshal(raw, &t) != nil || t.AccessToken == "" || !strings.EqualFold(t.TokenType, "Bearer") || t.ExpiresIn <= 0 {
		return "", &push.SendError{Kind: push.Transient, Status: resp.StatusCode, Code: "oauth_response"}
	}
	c.access, c.expires = t.AccessToken, now.Add(time.Duration(t.ExpiresIn)*time.Second)
	return c.access, nil
}

// forget drops access if it is still the cached token.
func (c *Client) forget(access string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.access == access {
		c.access, c.expires = "", time.Time{}
	}
}

// assertion is the signed JWT of the OAuth 2.0 JWT bearer flow (RFC 7523).
func (c *Client) assertion(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	h, err := json.Marshal(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid,omitempty"`
	}{"RS256", "JWT", c.acct.keyID})
	if err != nil {
		return "", err
	}
	cl, err := json.Marshal(struct {
		Iss   string `json:"iss"`
		Scope string `json:"scope"`
		Aud   string `json:"aud"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}{c.acct.email, scope, TokenURL, now.Unix(), now.Add(assertionLifetime).Unix()})
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(cl)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.acct.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// The messages:send request body (FCM HTTP v1).
type request struct {
	Message wireMessage `json:"message"`
}

type wireMessage struct {
	Token   string            `json:"token"`
	Data    map[string]string `json:"data,omitempty"`
	Android *android          `json:"android,omitempty"`
	APNS    *apns             `json:"apns,omitempty"`
}

type android struct {
	Priority     string              `json:"priority"`
	TTL          string              `json:"ttl"`
	Notification androidNotification `json:"notification"`
}

type androidNotification struct {
	TitleLocKey string `json:"title_loc_key"`
	BodyLocKey  string `json:"body_loc_key"`
	Tag         string `json:"tag,omitempty"`
	ChannelID   string `json:"channel_id,omitempty"`
}

type apns struct {
	Headers map[string]string `json:"headers"`
	Payload apnsPayload       `json:"payload"`
}

type apnsPayload struct {
	APS aps `json:"aps"`
}

type aps struct {
	Alert             apsAlert `json:"alert"`
	Sound             string   `json:"sound,omitempty"`
	ThreadID          string   `json:"thread-id,omitempty"`
	InterruptionLevel string   `json:"interruption-level,omitempty"`
}

type apsAlert struct {
	TitleLocKey string `json:"title-loc-key"`
	LocKey      string `json:"loc-key"`
}

// message is the wire form of m; only m's platform gets its block.
func message(m push.Message, now time.Time) wireMessage {
	w := wireMessage{Token: m.Token, Data: m.Data}
	ttl := m.TTL
	if ttl <= 0 {
		ttl = push.TTL
	}
	if m.Platform == push.PlatformIOS {
		prio := "5"
		if m.High {
			prio = "10"
		}
		w.APNS = &apns{
			Headers: map[string]string{
				"apns-priority": prio, "apns-push-type": "alert",
				"apns-expiration": strconv.FormatInt(now.Add(ttl).Unix(), 10),
			},
			Payload: apnsPayload{APS: aps{
				Alert: apsAlert{TitleLocKey: m.TitleKey, LocKey: m.BodyKey}, Sound: "default", ThreadID: m.Group,
			}},
		}
		if m.Group != "" && len(m.Group) <= 64 {
			w.APNS.Headers["apns-collapse-id"] = m.Group
		}
		if m.TimeSensitive {
			w.APNS.Payload.APS.InterruptionLevel = "time-sensitive"
		}
		return w
	}
	prio := "NORMAL"
	if m.High {
		prio = "HIGH"
	}
	w.Android = &android{
		Priority: prio, TTL: strconv.FormatInt(int64(ttl/time.Second), 10) + "s",
		Notification: androidNotification{TitleLocKey: m.TitleKey, BodyLocKey: m.BodyKey, Tag: m.Group, ChannelID: m.Channel},
	}
	return w
}
