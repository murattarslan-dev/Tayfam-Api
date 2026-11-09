package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Service tokens (ADR-0013) are Kubernetes projected ServiceAccount tokens.
// They are signed by the cluster, not by identity, so they have their own
// verifier. The format is again narrow: only RS256 and ES256 (the two
// algorithms k3s may use), a fixed header, one audience. Signatures are
// checked with crypto/rsa and crypto/ecdsa from the standard library.

const (
	// MaxServiceTokenTTL is the longest lifetime a service token may have,
	// whatever it says (ADR-0013). Projected tokens are requested for 600 s.
	MaxServiceTokenTTL = time.Hour

	algRS256 = "RS256"
	algES256 = "ES256"

	// maxServiceTokenBytes bounds the work done before the signature is
	// checked. Kubernetes tokens are around 1–1.5 KB.
	maxServiceTokenBytes = 8192
	// maxJWKSBytes bounds the JWKS response read from the API server.
	maxJWKSBytes = 64 << 10
	// jwksTTL is how long the cluster's keys are cached. The API server
	// rotates them rarely; an unknown key ID refetches sooner (minRefetch).
	jwksTTL = 5 * time.Minute
	// minRSABits rejects weak cluster keys.
	minRSABits = 2048
)

// serviceKidPattern: Kubernetes key IDs are base64url SHA-256 digests.
var serviceKidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// dnsLabel is a Kubernetes namespace or ServiceAccount name (RFC 1123).
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

const saPrefix = "system:serviceaccount:"

// ServiceAccount is a Kubernetes ServiceAccount, the identity of a service
// in service-to-service calls (ADR-0013).
type ServiceAccount struct {
	Namespace string
	Name      string
}

// Subject returns the token subject, system:serviceaccount:<ns>:<name>.
func (s ServiceAccount) Subject() string { return saPrefix + s.Namespace + ":" + s.Name }

// Valid reports whether both parts are valid Kubernetes names.
func (s ServiceAccount) Valid() bool {
	return dnsLabel.MatchString(s.Namespace) && dnsLabel.MatchString(s.Name)
}

// ParseServiceAccount reads a token subject.
func ParseServiceAccount(sub string) (ServiceAccount, bool) {
	rest, ok := strings.CutPrefix(sub, saPrefix)
	if !ok {
		return ServiceAccount{}, false
	}
	ns, name, ok := strings.Cut(rest, ":")
	sa := ServiceAccount{Namespace: ns, Name: name}
	if !ok || !sa.Valid() {
		return ServiceAccount{}, false
	}
	return sa, true
}

// ServiceClaims are the verified contents of a service token.
type ServiceClaims struct {
	Account   ServiceAccount
	Issuer    string
	Audience  string
	IssuedAt  time.Time
	ExpiresAt time.Time
	KeyID     string
}

// ServiceVerifierConfig configures a ServiceVerifier.
type ServiceVerifierConfig struct {
	// Issuer is the cluster's service account issuer, e.g.
	// https://kubernetes.default.svc.cluster.local on k3s.
	Issuer string
	// Audience is this service's own audience, e.g. core.tayfam.internal.
	Audience string
	// JWKSURL is the API server's key endpoint, e.g.
	// https://kubernetes.default.svc/openid/v1/jwks. Must be https.
	JWKSURL string
	// Client fetches the keys. It must trust the API server's CA. Redirects
	// are never followed.
	Client *http.Client
	// BearerToken, when set, returns the token sent with the JWKS request:
	// the pod's own short-lived token for the API server, needed when the
	// endpoint is not open to anonymous requests.
	BearerToken func() (string, error)
	Now         func() time.Time // optional, for tests
}

// ServiceVerifier checks service tokens against the cluster's public keys.
type ServiceVerifier struct {
	cfg   ServiceVerifierConfig
	cache keyCache[crypto.PublicKey]
}

// NewServiceVerifier validates cfg and returns a ServiceVerifier. Keys are
// fetched on first use.
func NewServiceVerifier(cfg ServiceVerifierConfig) (*ServiceVerifier, error) {
	if cfg.Issuer == "" || cfg.Audience == "" || cfg.Client == nil {
		return nil, errors.New("servis token'ı: issuer, audience ve HTTP istemcisi zorunlu")
	}
	u, err := url.Parse(cfg.JWKSURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, errors.New("servis token'ı: JWKS adresi https olmalı")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	// A copy, so the caller's client is not changed.
	client := *cfg.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg.Client = &client
	v := &ServiceVerifier{cfg: cfg}
	v.cache.now = cfg.Now
	v.cache.fetch = v.fetchKeys
	return v, nil
}

type serviceHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ,omitempty"`
}

// audience is "aud" as either a string or an array of strings (RFC 7519).
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("aud biçimi")
	}
	*a = many
	return nil
}

// servicePayload holds the claims we check; Kubernetes adds others (jti,
// kubernetes.io) that are ignored.
type servicePayload struct {
	Iss string   `json:"iss"`
	Aud audience `json:"aud"`
	Sub string   `json:"sub"`
	Iat int64    `json:"iat"`
	Nbf int64    `json:"nbf"`
	Exp int64    `json:"exp"`
}

// Verify checks token's structure, signature and claims and returns the
// claims. Every failure wraps ErrInvalidToken, except a failing key fetch,
// which is an infrastructure error and wraps neither.
func (v *ServiceVerifier) Verify(ctx context.Context, token string) (ServiceClaims, error) {
	if len(token) == 0 || len(token) > maxServiceTokenBytes {
		return ServiceClaims{}, fmt.Errorf("%w: uzunluk", ErrInvalidToken)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ServiceClaims{}, fmt.Errorf("%w: biçim", ErrInvalidToken)
	}
	var h serviceHeader
	// Unknown header fields (crit, jku, x5u, jwk …) are refused: nothing in
	// the token may say where its key comes from.
	if err := decodeStrict(parts[0], &h); err != nil {
		return ServiceClaims{}, fmt.Errorf("%w: başlık: %w", ErrInvalidToken, err)
	}
	if (h.Alg != algRS256 && h.Alg != algES256) || !serviceKidPattern.MatchString(h.Kid) || (h.Typ != "" && h.Typ != "JWT") {
		return ServiceClaims{}, fmt.Errorf("%w: başlık değerleri", ErrInvalidToken)
	}
	sig, err := b64.Strict().DecodeString(parts[2])
	if err != nil {
		return ServiceClaims{}, fmt.Errorf("%w: imza biçimi", ErrInvalidToken)
	}

	key, err := v.cache.get(ctx, h.Kid)
	if errors.Is(err, ErrUnknownKey) {
		return ServiceClaims{}, fmt.Errorf("%w: anahtar", ErrInvalidToken)
	}
	if err != nil {
		return ServiceClaims{}, fmt.Errorf("cluster açık anahtarı alınamadı: %w", err)
	}
	digest := sha256.Sum256([]byte(token[:len(parts[0])+1+len(parts[1])]))
	if !verifySignature(h.Alg, key, digest[:], sig) {
		return ServiceClaims{}, fmt.Errorf("%w: imza", ErrInvalidToken)
	}

	// Claims are only read after the signature is known to be good.
	var p servicePayload
	raw, err := b64.Strict().DecodeString(parts[1])
	if err != nil {
		return ServiceClaims{}, fmt.Errorf("%w: içerik", ErrInvalidToken)
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ServiceClaims{}, fmt.Errorf("%w: içerik", ErrInvalidToken)
	}
	sa, saOK := ParseServiceAccount(p.Sub)
	now := v.cfg.Now()
	iat, exp := time.Unix(p.Iat, 0).UTC(), time.Unix(p.Exp, 0).UTC()
	switch {
	case p.Iss != v.cfg.Issuer:
		return ServiceClaims{}, fmt.Errorf("%w: issuer", ErrInvalidToken)
	// Exactly one audience: a token valid for several targets is refused.
	case len(p.Aud) != 1 || p.Aud[0] != v.cfg.Audience:
		return ServiceClaims{}, fmt.Errorf("%w: audience", ErrInvalidToken)
	case !saOK:
		return ServiceClaims{}, fmt.Errorf("%w: kimlik", ErrInvalidToken)
	case p.Iat == 0 || p.Exp == 0 || !exp.After(iat) || exp.Sub(iat) > MaxServiceTokenTTL:
		return ServiceClaims{}, fmt.Errorf("%w: ömür", ErrInvalidToken)
	case iat.After(now.Add(clockSkew)) || (p.Nbf != 0 && time.Unix(p.Nbf, 0).After(now.Add(clockSkew))):
		return ServiceClaims{}, fmt.Errorf("%w: gelecekte verilmiş", ErrInvalidToken)
	case !now.Before(exp.Add(clockSkew)):
		return ServiceClaims{}, fmt.Errorf("%w: süresi dolmuş", ErrInvalidToken)
	}
	return ServiceClaims{
		Account: sa, Issuer: p.Iss, Audience: p.Aud[0],
		IssuedAt: iat, ExpiresAt: exp, KeyID: h.Kid,
	}, nil
}

// verifySignature checks sig over digest. The key type must match the
// algorithm, so an RSA key can never verify an ES256 token or the reverse.
func verifySignature(alg string, key crypto.PublicKey, digest, sig []byte) bool {
	switch alg {
	case algRS256:
		k, ok := key.(*rsa.PublicKey)
		return ok && rsa.VerifyPKCS1v15(k, crypto.SHA256, digest, sig) == nil
	case algES256:
		k, ok := key.(*ecdsa.PublicKey)
		if !ok || k.Curve != elliptic.P256() || len(sig) != 64 {
			return false
		}
		r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
		return ecdsa.Verify(k, digest, r, s)
	}
	return false
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (v *ServiceVerifier) fetchKeys(ctx context.Context) (map[string]crypto.PublicKey, time.Duration, error) {
	return fetchJWKS(ctx, v.cfg.Client, v.cfg.JWKSURL, v.cfg.BearerToken)
}

// fetchJWKS reads the key set at jwksURL with client, sending the token
// bearer returns when it is set. Service tokens (API server) and Cloudflare
// Access (team domain) both use it.
func fetchJWKS(ctx context.Context, client *http.Client, jwksURL string, bearer func() (string, error)) (map[string]crypto.PublicKey, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, http.NoBody)
	if err != nil {
		return nil, 0, fmt.Errorf("JWKS isteği: %w", err)
	}
	req.Header.Set("Accept", "application/jwk-set+json, application/json")
	if bearer != nil {
		tok, err := bearer()
		if err != nil {
			return nil, 0, fmt.Errorf("API sunucusu token'ı: %w", err)
		}
		req.Header.Set("Authorization", bearerPrefix+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		// The error carries only the URL, which is configuration, not a secret.
		return nil, 0, fmt.Errorf("JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("JWKS: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("JWKS okunamadı: %w", err)
	}
	if len(body) > maxJWKSBytes {
		return nil, 0, errors.New("JWKS çok büyük")
	}
	return parseJWKS(body)
}

// parseJWKS returns the usable signing keys in a JWKS document. Keys of
// other types, curves or uses, and malformed or weak keys, are skipped.
func parseJWKS(body []byte) (map[string]crypto.PublicKey, time.Duration, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, 0, errors.New("JWKS çözümlenemedi")
	}
	keys := make(map[string]crypto.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if !serviceKidPattern.MatchString(k.Kid) || (k.Use != "" && k.Use != "sig") {
			continue
		}
		if pub, ok := k.publicKey(); ok {
			keys[k.Kid] = pub
		}
	}
	return keys, jwksTTL, nil
}

func (k jwk) publicKey() (crypto.PublicKey, bool) {
	switch {
	case k.Kty == "RSA" && (k.Alg == "" || k.Alg == algRS256):
		n, err1 := b64.Strict().DecodeString(k.N)
		e, err2 := b64.Strict().DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			return nil, false
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < minRSABits || pub.E < 3 || pub.E%2 == 0 {
			return nil, false
		}
		return pub, true
	case k.Kty == "EC" && k.Crv == "P-256" && (k.Alg == "" || k.Alg == algES256):
		x, err1 := b64.Strict().DecodeString(k.X)
		y, err2 := b64.Strict().DecodeString(k.Y)
		if err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
			return nil, false
		}
		// The parser rejects points that are not on the curve.
		raw := append(append([]byte{4}, x...), y...)
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
		if err != nil {
			return nil, false
		}
		return pub, true
	}
	return nil, false
}

// ServiceAudience is the audience of service tokens for target, e.g.
// "core" → "core.tayfam.internal" (ADR-0013).
func ServiceAudience(target string) string { return target + ".tayfam.internal" }
