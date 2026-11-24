// Package appleauth talks to Apple's token endpoints for one purpose:
// revoking a user's Apple tokens when the account is deleted (ADR-0045 §4).
//
// Nothing here is stored: the authorization code, the tokens Apple returns
// for it and the client secret live in memory for the length of one call and
// are never logged. Errors carry an HTTP status and an OAuth error name at
// most.
package appleauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ExchangeURL exchanges an authorization code for tokens.
	ExchangeURL = "https://appleid.apple.com/auth/token"
	// RevokeURL revokes a refresh or access token.
	RevokeURL = "https://appleid.apple.com/auth/revoke"

	// MaxCode bounds the authorization code (ADR-0045 §3).
	MaxCode = 1024

	appleAudience = "https://appleid.apple.com"
	assertionTTL  = 5 * time.Minute
	maxBody       = 64 << 10
)

var (
	// ErrCodeRejected: the code is missing, malformed, used, expired or
	// belongs to another Apple user. The person signs in with Apple again.
	ErrCodeRejected = errors.New("apple yetkilendirme kodu reddedildi")
	// ErrUnavailable: Apple could not be asked or did not answer as
	// expected. Nothing was revoked; the caller may retry.
	ErrUnavailable = errors.New("apple'a ulaşılamıyor")
)

// SubjectVerifier verifies the identity token Apple returns for an exchanged
// code and gives its subject (oidc.Apple).
type SubjectVerifier interface {
	ExchangedSubject(ctx context.Context, idToken string) (string, error)
}

// Config wires a Client. None of the identifiers is secret; Key is.
type Config struct {
	// ClientID is the iOS bundle ID the codes are issued to.
	ClientID string
	TeamID   string
	KeyID    string
	// Key is the Sign in with Apple private key (P-256).
	Key    *ecdsa.PrivateKey
	HTTP   *http.Client
	Tokens SubjectVerifier
	Now    func() time.Time // optional, for tests
}

// Client revokes Apple tokens.
type Client struct {
	cfg         Config
	exchangeURL string
	revokeURL   string
}

// New returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.ClientID == "" || cfg.TeamID == "" || cfg.KeyID == "" || cfg.Key == nil || cfg.HTTP == nil || cfg.Tokens == nil {
		return nil, errors.New("appleauth: eksik yapılandırma")
	}
	if cfg.Key.Curve != elliptic.P256() {
		return nil, errors.New("appleauth: anahtar P-256 değil")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Client{cfg: cfg, exchangeURL: ExchangeURL, revokeURL: RevokeURL}, nil
}

// ParseKey reads the Sign in with Apple private key as Apple hands it out:
// a PEM file holding a PKCS #8 P-256 key.
func ParseKey(pemText string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("apple anahtarı: PEM PRIVATE KEY bloğu yok")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("apple anahtarı: PKCS #8 okunamadı")
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("apple anahtarı: P-256 ECDSA anahtarı değil")
	}
	return key, nil
}

// RevokeByCode exchanges an authorization code and revokes the tokens it
// stands for. subject is the Apple user the account belongs to: a code of
// any other Apple user is refused and nothing is revoked.
func (c *Client) RevokeByCode(ctx context.Context, code, subject string) error {
	if code == "" || len(code) > MaxCode || subject == "" {
		return ErrCodeRejected
	}
	secret, err := c.clientSecret()
	if err != nil {
		return fmt.Errorf("%w: istemci sırrı", ErrUnavailable)
	}
	tok, err := c.exchange(ctx, secret, code)
	if err != nil {
		return err
	}
	got, err := c.cfg.Tokens.ExchangedSubject(ctx, tok.IDToken)
	if err != nil {
		return fmt.Errorf("%w: takas edilen kimlik token'ı doğrulanamadı", ErrUnavailable)
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(subject)) != 1 {
		return fmt.Errorf("%w: başka kullanıcının kodu", ErrCodeRejected)
	}
	token, hint := tok.RefreshToken, "refresh_token"
	if token == "" {
		token, hint = tok.AccessToken, "access_token"
	}
	if token == "" {
		return fmt.Errorf("%w: takas cevabında token yok", ErrUnavailable)
	}
	return c.revoke(ctx, secret, token, hint)
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

func (c *Client) exchange(ctx context.Context, secret, code string) (tokenResponse, error) {
	status, body, err := c.post(ctx, c.exchangeURL, url.Values{
		"client_id":     {c.cfg.ClientID},
		"client_secret": {secret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		return tokenResponse{}, err
	}
	if status != http.StatusOK {
		name := oauthError(body)
		// invalid_grant is how Apple refuses a code. Every other answer
		// (invalid_client, a 5xx) is ours or Apple's to fix, not the
		// person's.
		if status == http.StatusBadRequest && name == "invalid_grant" {
			return tokenResponse{}, fmt.Errorf("%w: invalid_grant", ErrCodeRejected)
		}
		return tokenResponse{}, fmt.Errorf("%w: token ucu HTTP %d %s", ErrUnavailable, status, name)
	}
	var t tokenResponse
	if err := json.Unmarshal(body, &t); err != nil || t.IDToken == "" {
		return tokenResponse{}, fmt.Errorf("%w: token ucu cevabı okunamadı", ErrUnavailable)
	}
	return t, nil
}

func (c *Client) revoke(ctx context.Context, secret, token, hint string) error {
	status, body, err := c.post(ctx, c.revokeURL, url.Values{
		"client_id":       {c.cfg.ClientID},
		"client_secret":   {secret},
		"token":           {token},
		"token_type_hint": {hint},
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%w: iptal ucu HTTP %d %s", ErrUnavailable, status, oauthError(body))
	}
	return nil
}

func (c *Client) post(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: istek kurulamadı", ErrUnavailable)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		// The transport error can quote the request; only its class is kept.
		return 0, nil, fmt.Errorf("%w: istek gönderilemedi", ErrUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: cevap okunamadı", ErrUnavailable)
	}
	return resp.StatusCode, body, nil
}

// oauthError returns the OAuth error name of an error response if it looks
// like one (lower-case letters and underscores), otherwise "". Free text
// from the response never reaches a log.
func oauthError(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error == "" || len(e.Error) > 40 {
		return ""
	}
	for _, r := range e.Error {
		if (r < 'a' || r > 'z') && r != '_' {
			return ""
		}
	}
	return e.Error
}

var b64 = base64.RawURLEncoding

// clientSecret builds the short-lived ES256 JWT Apple asks for in place of a
// client secret: issued by the team, for the app, signed with the Sign in
// with Apple key.
func (c *Client) clientSecret() (string, error) {
	now := c.cfg.Now()
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": c.cfg.KeyID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss": c.cfg.TeamID,
		"iat": now.Unix(),
		"exp": now.Add(assertionTTL).Unix(),
		"aud": appleAudience,
		"sub": c.cfg.ClientID,
	})
	if err != nil {
		return "", err
	}
	signing := b64.EncodeToString(header) + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, c.cfg.Key, digest[:])
	if err != nil {
		return "", err
	}
	// JWS wants the fixed-width r || s, not ASN.1.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + b64.EncodeToString(sig), nil
}
