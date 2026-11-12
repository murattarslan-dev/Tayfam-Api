// Package googleauth gets OAuth 2.0 access tokens for a Google service
// account with the standard library only: it signs a short JWT with the
// account's RSA key (RS256 through crypto/rsa.SignPKCS1v15, the standard
// library's signature, not a scheme of our own; CLAUDE.md rule 4) and
// exchanges it at Google's token endpoint (RFC 7523) for an access token
// limited to one scope. The token is kept in memory until shortly before it
// expires.
//
// It is infrastructure only: it knows no Google API beyond the token
// endpoint. The key, the signed JWT and the access token never reach an
// error message or a log; errors carry HTTP status codes only.
package googleauth

import (
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
	"strings"
	"sync"
	"time"
)

// TokenURL is Google's OAuth 2.0 token endpoint. It is fixed here and never
// comes from configuration.
const TokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // an endpoint address, not a credential

const (
	assertionLifetime = time.Hour
	// refreshBefore: a cached access token is replaced this long before it
	// expires.
	refreshBefore  = 5 * time.Minute
	requestTimeout = 10 * time.Second
	maxBody        = 64 << 10
)

var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// ErrUnavailable means Google's token endpoint could not give a token: a
// network error or a non-200 answer. It is temporary from the caller's
// point of view.
var ErrUnavailable = errors.New("google erişim token'ı alınamadı")

// ServiceAccount is the part of a Google service account key the client
// uses. Its fields are never printed.
type ServiceAccount struct {
	projectID string
	email     string
	keyID     string
	key       *rsa.PrivateKey
}

// ProjectID is the Cloud project of the account (not secret).
func (a *ServiceAccount) ProjectID() string { return a.projectID }

// String hides the key from fmt and slog.
func (a *ServiceAccount) String() string { return "googleauth.ServiceAccount{" + a.projectID + "}" }

// errAccount is returned for any problem in the service account JSON; it
// names the field only, never its value.
func errAccount(field string) error {
	return fmt.Errorf("google servis hesabı geçersiz: %s", field)
}

// ParseServiceAccount reads a service account key file (JSON). Its
// token_uri, when present, must be Google's token endpoint.
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

// Config wires a TokenSource.
type Config struct {
	Account *ServiceAccount
	// Scope is the single OAuth scope the token is limited to.
	Scope string
	// Transport is for tests (a fake token server); nil uses the default.
	Transport http.RoundTripper
	Now       func() time.Time // optional, for tests
}

// TokenSource hands out access tokens for one account and scope. It is safe
// for concurrent use.
type TokenSource struct {
	acct  *ServiceAccount
	scope string
	http  *http.Client
	now   func() time.Time

	mu      sync.Mutex
	access  string
	expires time.Time
}

// New returns a TokenSource.
func New(cfg Config) (*TokenSource, error) {
	if cfg.Account == nil {
		return nil, errors.New("googleauth: servis hesabı yok")
	}
	if cfg.Scope == "" || !strings.HasPrefix(cfg.Scope, "https://www.googleapis.com/auth/") {
		return nil, errors.New("googleauth: geçersiz kapsam")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &TokenSource{acct: cfg.Account, scope: cfg.Scope, now: cfg.Now, http: NewHTTPClient(cfg.Transport)}, nil
}

// NewHTTPClient returns a client with a timeout that follows no redirects;
// nil t uses the default transport. Callers of Google APIs use it so their
// requests behave like the token request.
func NewHTTPClient(t http.RoundTripper) *http.Client {
	if t == nil {
		t = http.DefaultTransport
	}
	return &http.Client{
		Transport: t, Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Token returns a valid access token, fetching a new one when needed. Errors
// wrap ErrUnavailable or are the context's error.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.access != "" && now.Before(s.expires.Add(-refreshBefore)) {
		return s.access, nil
	}
	assertion, err := s.assertion(now)
	if err != nil {
		return "", fmt.Errorf("%w: assertion", ErrUnavailable)
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: request", ErrUnavailable)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: network", ErrUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if json.Unmarshal(raw, &t) != nil || t.AccessToken == "" || !strings.EqualFold(t.TokenType, "Bearer") || t.ExpiresIn <= 0 {
		return "", fmt.Errorf("%w: response", ErrUnavailable)
	}
	s.access, s.expires = t.AccessToken, now.Add(time.Duration(t.ExpiresIn)*time.Second)
	return s.access, nil
}

// Forget drops access if it is still the cached token, so the next call
// fetches a new one (after the API refused it with 401).
func (s *TokenSource) Forget(access string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.access == access {
		s.access, s.expires = "", time.Time{}
	}
}

// assertion is the signed JWT of the OAuth 2.0 JWT bearer flow (RFC 7523).
func (s *TokenSource) assertion(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	h, err := json.Marshal(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid,omitempty"`
	}{"RS256", "JWT", s.acct.keyID})
	if err != nil {
		return "", err
	}
	cl, err := json.Marshal(struct {
		Iss   string `json:"iss"`
		Scope string `json:"scope"`
		Aud   string `json:"aud"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}{s.acct.email, s.scope, TokenURL, now.Unix(), now.Add(assertionLifetime).Unix()})
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(cl)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.acct.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
