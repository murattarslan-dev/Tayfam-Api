package auth

import (
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Cloudflare Access (ADR-0008, layers 2 and 3). Cloudflare Zero Trust puts
// a login in front of admin.tayfam.app and adds a signed JWT to every
// request it lets through, in the Cf-Access-Jwt-Assertion header. gateway
// checks that JWT itself on every admin request instead of trusting that
// the request came through Cloudflare: a request that reaches gateway some
// other way (a leaked tunnel token, the cluster network) has no valid JWT.
//
// The token is RS256-signed with the team's keys, published at
// https://<team>.cloudflareaccess.com/cdn-cgi/access/certs; the issuer is
// the team domain and the audience is the Access application's AUD tag.
// Keys go through keyCache, with the same rules as the other key sets.

// AccessHeader carries Cloudflare Access's JWT to the origin.
const AccessHeader = "Cf-Access-Jwt-Assertion"

const (
	// MaxAccessTokenTTL bounds the Access application's session duration
	// that gateway accepts; the runbook sets it below this.
	MaxAccessTokenTTL = 24 * time.Hour
	// maxAccessTokenBytes bounds the work done before the signature is
	// checked. Access tokens are around 1 KB; custom claims make them grow.
	maxAccessTokenBytes = 8192
	maxEmailLength      = 320
)

var (
	teamDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.cloudflareaccess\.com$`)
	accessAUDPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// AccessVerifierConfig configures an AccessVerifier.
type AccessVerifierConfig struct {
	// TeamDomain is the Zero Trust team domain, e.g.
	// tayfam.cloudflareaccess.com. The issuer is https://<TeamDomain>.
	TeamDomain string
	// Audience is the Access application's AUD tag (64 hex characters).
	Audience string
	// Client fetches the keys. Redirects are never followed.
	Client *http.Client
	// CertsURL overrides the key endpoint derived from TeamDomain. Only
	// tests set it; it must be https.
	CertsURL string
	Now      func() time.Time // optional, for tests
}

// AccessClaims are the verified contents of a Cloudflare Access token.
type AccessClaims struct {
	// Email is the address the person signed in to Access with, lower case.
	// Personal data: never logged.
	Email     string
	Subject   string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// AccessVerifier checks Cloudflare Access tokens of one application.
type AccessVerifier struct {
	cfg    AccessVerifierConfig
	issuer string
	cache  keyCache[crypto.PublicKey]
}

// NewAccessVerifier validates cfg and returns an AccessVerifier. Keys are
// fetched on first use.
func NewAccessVerifier(cfg AccessVerifierConfig) (*AccessVerifier, error) {
	if !teamDomainPattern.MatchString(cfg.TeamDomain) {
		return nil, errors.New("cloudflare access: takım alan adı <takım>.cloudflareaccess.com biçiminde olmalı")
	}
	if !accessAUDPattern.MatchString(cfg.Audience) {
		return nil, errors.New("cloudflare access: AUD etiketi 64 onaltılık karakter olmalı")
	}
	if cfg.Client == nil {
		return nil, errors.New("cloudflare access: HTTP istemcisi zorunlu")
	}
	if cfg.CertsURL == "" {
		cfg.CertsURL = "https://" + cfg.TeamDomain + "/cdn-cgi/access/certs"
	}
	u, err := url.Parse(cfg.CertsURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, errors.New("cloudflare access: anahtar adresi https olmalı")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	client := *cfg.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg.Client = &client
	v := &AccessVerifier{cfg: cfg, issuer: "https://" + cfg.TeamDomain}
	v.cache.now = cfg.Now
	v.cache.fetch = func(ctx context.Context) (map[string]crypto.PublicKey, time.Duration, error) {
		return fetchJWKS(ctx, v.cfg.Client, v.cfg.CertsURL, nil)
	}
	return v, nil
}

// accessPayload holds the claims we check; Cloudflare adds others
// (identity_nonce, country, custom) that are ignored.
type accessPayload struct {
	Iss   string   `json:"iss"`
	Aud   audience `json:"aud"`
	Sub   string   `json:"sub"`
	Email string   `json:"email"`
	Type  string   `json:"type"`
	Iat   int64    `json:"iat"`
	Nbf   int64    `json:"nbf"`
	Exp   int64    `json:"exp"`
}

// Verify checks token's structure, signature and claims and returns the
// claims. Every failure wraps ErrInvalidToken, except a failing key fetch,
// which is an infrastructure error and wraps neither.
func (v *AccessVerifier) Verify(ctx context.Context, token string) (AccessClaims, error) {
	var zero AccessClaims
	if len(token) == 0 || len(token) > maxAccessTokenBytes {
		return zero, fmt.Errorf("%w: uzunluk", ErrInvalidToken)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return zero, fmt.Errorf("%w: biçim", ErrInvalidToken)
	}
	var h serviceHeader
	// Unknown header fields (jku, x5u, jwk …) are refused: nothing in the
	// token may say where its key comes from.
	if err := decodeStrict(parts[0], &h); err != nil {
		return zero, fmt.Errorf("%w: başlık: %w", ErrInvalidToken, err)
	}
	if h.Alg != algRS256 || !serviceKidPattern.MatchString(h.Kid) || (h.Typ != "" && h.Typ != "JWT") {
		return zero, fmt.Errorf("%w: başlık değerleri", ErrInvalidToken)
	}
	sig, err := b64.Strict().DecodeString(parts[2])
	if err != nil {
		return zero, fmt.Errorf("%w: imza biçimi", ErrInvalidToken)
	}
	key, err := v.cache.get(ctx, h.Kid)
	if errors.Is(err, ErrUnknownKey) {
		return zero, fmt.Errorf("%w: anahtar", ErrInvalidToken)
	}
	if err != nil {
		return zero, fmt.Errorf("cloudflare access açık anahtarı alınamadı: %w", err)
	}
	digest := sha256.Sum256([]byte(token[:len(parts[0])+1+len(parts[1])]))
	if !verifySignature(algRS256, key, digest[:], sig) {
		return zero, fmt.Errorf("%w: imza", ErrInvalidToken)
	}

	// Claims are only read after the signature is known to be good.
	raw, err := b64.Strict().DecodeString(parts[1])
	if err != nil {
		return zero, fmt.Errorf("%w: içerik", ErrInvalidToken)
	}
	var p accessPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return zero, fmt.Errorf("%w: içerik", ErrInvalidToken)
	}
	now := v.cfg.Now()
	iat, exp := time.Unix(p.Iat, 0).UTC(), time.Unix(p.Exp, 0).UTC()
	email := strings.ToLower(strings.TrimSpace(p.Email))
	switch {
	case p.Iss != v.issuer:
		return zero, fmt.Errorf("%w: issuer", ErrInvalidToken)
	// Exactly our application: a token of another Access application of the
	// same team is refused.
	case len(p.Aud) != 1 || p.Aud[0] != v.cfg.Audience:
		return zero, fmt.Errorf("%w: audience", ErrInvalidToken)
	// "app" is an application token; a service token or the team's own
	// token is refused.
	case p.Type != "app":
		return zero, fmt.Errorf("%w: tür", ErrInvalidToken)
	case p.Sub == "" || len(p.Sub) > 255:
		return zero, fmt.Errorf("%w: kimlik", ErrInvalidToken)
	case len(email) < 3 || len(email) > maxEmailLength || !strings.Contains(email, "@"):
		return zero, fmt.Errorf("%w: e-posta", ErrInvalidToken)
	case p.Iat == 0 || p.Exp == 0 || !exp.After(iat) || exp.Sub(iat) > MaxAccessTokenTTL:
		return zero, fmt.Errorf("%w: ömür", ErrInvalidToken)
	case iat.After(now.Add(clockSkew)) || (p.Nbf != 0 && time.Unix(p.Nbf, 0).After(now.Add(clockSkew))):
		return zero, fmt.Errorf("%w: gelecekte verilmiş", ErrInvalidToken)
	case !now.Before(exp.Add(clockSkew)):
		return zero, fmt.Errorf("%w: süresi dolmuş", ErrInvalidToken)
	}
	return AccessClaims{Email: email, Subject: p.Sub, IssuedAt: iat, ExpiresAt: exp}, nil
}
