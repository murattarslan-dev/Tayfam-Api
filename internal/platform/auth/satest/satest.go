// Package satest is a stand-in Kubernetes cluster for tests: it signs
// ServiceAccount tokens and serves its public keys as JWKS over TLS, so the
// real ServiceVerifier path is exercised without a cluster (ADR-0013).
//
// It works only inside `go test`: New fails the test otherwise, and nothing
// in it is reachable from service configuration. Production verifiers only
// trust the cluster's own keys fetched from the API server.
package satest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
)

// Issuer is the stand-in cluster's issuer.
const Issuer = "https://kubernetes.default.svc.cluster.local"

// Key IDs of the two keys the cluster signs with.
const (
	KidRSA = "rsa-test-key"
	KidEC  = "ec-test-key"
)

// Cluster signs tokens and serves their keys.
type Cluster struct {
	rsaKey *rsa.PrivateKey
	ecKey  *ecdsa.PrivateKey
	srv    *httptest.Server

	mu         sync.Mutex
	jwksHits   int
	lastBearer string
}

// New starts a cluster for the duration of t.
func New(t testing.TB) *Cluster {
	t.Helper()
	if !testing.Testing() {
		panic("satest yalnızca testlerde kullanılabilir")
	}
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := &Cluster{rsaKey: rk, ecKey: ek}
	c.srv = httptest.NewTLSServer(http.HandlerFunc(c.serveJWKS))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *Cluster) serveJWKS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/openid/v1/jwks" {
		http.NotFound(w, r)
		return
	}
	c.mu.Lock()
	c.jwksHits++
	c.lastBearer = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	c.mu.Unlock()
	enc := base64.RawURLEncoding
	ecPub, err := c.ecKey.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		http.Error(w, "anahtar", http.StatusInternalServerError)
		return
	}
	keys := []map[string]string{
		{"kty": "RSA", "kid": KidRSA, "alg": "RS256", "use": "sig",
			"n": enc.EncodeToString(c.rsaKey.N.Bytes()),
			"e": enc.EncodeToString(big.NewInt(int64(c.rsaKey.E)).Bytes())},
		{"kty": "EC", "kid": KidEC, "alg": "ES256", "use": "sig", "crv": "P-256",
			"x": enc.EncodeToString(ecPub[1:33]), "y": enc.EncodeToString(ecPub[33:])},
	}
	w.Header().Set("Content-Type", "application/jwk-set+json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}

// JWKSURL is the cluster's key endpoint.
func (c *Cluster) JWKSURL() string { return c.srv.URL + "/openid/v1/jwks" }

// Client trusts the cluster's TLS certificate.
func (c *Cluster) Client() *http.Client { return c.srv.Client() }

// JWKSHits is how many times the keys were fetched.
func (c *Cluster) JWKSHits() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.jwksHits
}

// LastBearer is the bearer token sent with the last JWKS request.
func (c *Cluster) LastBearer() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastBearer
}

// Verifier returns a ServiceVerifier for audience that trusts this cluster.
func (c *Cluster) Verifier(t testing.TB, audience string, now func() time.Time) *auth.ServiceVerifier {
	t.Helper()
	v, err := auth.NewServiceVerifier(auth.ServiceVerifierConfig{
		Issuer: Issuer, Audience: audience, JWKSURL: c.JWKSURL(), Client: c.Client(), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Spec describes a token. Zero fields get the values of a valid projected
// token: ES256, this cluster's issuer, issued now, valid for 10 minutes.
type Spec struct {
	Alg      string // "RS256" or "ES256"
	Kid      string
	Issuer   string
	Audience []string
	Subject  string
	IssuedAt time.Time
	TTL      time.Duration
	// Header and Claims, when set, replace the generated ones entirely.
	Header map[string]any
	Claims map[string]any
}

// Token signs a token for s.
func (c *Cluster) Token(t testing.TB, s Spec) string {
	t.Helper()
	if s.Alg == "" {
		s.Alg = "ES256"
	}
	if s.Kid == "" {
		s.Kid = KidEC
		if s.Alg == "RS256" {
			s.Kid = KidRSA
		}
	}
	if s.Issuer == "" {
		s.Issuer = Issuer
	}
	if s.IssuedAt.IsZero() {
		s.IssuedAt = time.Now()
	}
	if s.TTL == 0 {
		s.TTL = 10 * time.Minute
	}
	header := s.Header
	if header == nil {
		header = map[string]any{"alg": s.Alg, "kid": s.Kid}
	}
	claims := s.Claims
	if claims == nil {
		claims = map[string]any{
			"iss": s.Issuer, "aud": s.Audience, "sub": s.Subject,
			"iat": s.IssuedAt.Unix(), "nbf": s.IssuedAt.Unix(), "exp": s.IssuedAt.Add(s.TTL).Unix(),
			"jti":           "test",
			"kubernetes.io": map[string]any{"namespace": "tayfam-test"},
		}
	}
	enc := base64.RawURLEncoding
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	input := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	digest := sha256.Sum256([]byte(input))
	var sig []byte
	switch s.Alg {
	case "RS256":
		b, err := rsa.SignPKCS1v15(rand.Reader, c.rsaKey, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = b
	default:
		r, ss, err := ecdsa.Sign(rand.Reader, c.ecKey, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = make([]byte, 64)
		r.FillBytes(sig[:32])
		ss.FillBytes(sig[32:])
	}
	return input + "." + enc.EncodeToString(sig)
}

// ServiceToken is a valid ES256 token of sa for audience.
func (c *Cluster) ServiceToken(t testing.TB, sa auth.ServiceAccount, audience string) string {
	t.Helper()
	return c.Token(t, Spec{Audience: []string{audience}, Subject: sa.Subject()})
}
