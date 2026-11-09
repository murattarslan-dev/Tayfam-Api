package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	clientAndroid = "android-client.apps.googleusercontent.com"
	clientWeb     = "web-client.apps.googleusercontent.com"
	testNonce     = "n-0S6_WzA2Mj"
)

type provider struct {
	key   *rsa.PrivateKey
	kid   string
	srv   *httptest.Server
	hits  atomic.Int32
	extra []map[string]string // extra JWKS entries
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &provider{key: k, kid: "kid-1"}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.hits.Add(1)
		keys := []map[string]string{{
			"kty": "RSA", "kid": p.kid, "use": "sig", "alg": "RS256",
			"n": b64.EncodeToString(p.key.N.Bytes()),
			"e": b64.EncodeToString(big.NewInt(int64(p.key.E)).Bytes()),
		}}
		w.Header().Set("Cache-Control", "public, max-age=21600")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": append(keys, p.extra...)})
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *provider) verifier(t *testing.T, now time.Time) *Google {
	t.Helper()
	g, err := newGoogle([]string{clientAndroid, clientWeb}, p.srv.URL, p.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return now }
	g.keys.now = g.now
	return g
}

func sign(t *testing.T, key *rsa.PrivateKey, hdr, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(hdr)
	c, _ := json.Marshal(claims)
	in := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	d := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + b64.EncodeToString(sig)
}

var now0 = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

func goodClaims() map[string]any {
	return map[string]any{
		"iss": "https://accounts.google.com", "aud": clientAndroid, "azp": clientAndroid,
		"sub": "110169484474386276334", "email": "ornek@example.com", "email_verified": true,
		"name": "  Ayşe   Işık  ", "picture": "https://example.com/a.png",
		"iat": now0.Add(-time.Minute).Unix(), "exp": now0.Add(59 * time.Minute).Unix(),
		"nonce": testNonce,
	}
}

func TestGoogleVerify(t *testing.T) {
	p := newProvider(t)
	g := p.verifier(t, now0)
	tok := sign(t, p.key, map[string]any{"alg": "RS256", "kid": p.kid, "typ": "JWT"}, goodClaims())
	id, err := g.Verify(context.Background(), tok, testNonce)
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{Provider: "google", Subject: "110169484474386276334", Email: "ornek@example.com", Name: "Ayşe Işık", IssuedAt: time.Unix(now0.Add(-time.Minute).Unix(), 0).UTC()}
	if id != want {
		t.Fatalf("kimlik = %+v", id)
	}
}

func TestGoogleRejects(t *testing.T) {
	p := newProvider(t)
	g := p.verifier(t, now0)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	hdr := map[string]any{"alg": "RS256", "kid": p.kid}
	mod := func(f func(map[string]any)) map[string]any { c := goodClaims(); f(c); return c }
	good := sign(t, p.key, hdr, goodClaims())
	parts := strings.Split(good, ".")

	tests := map[string]struct {
		token, nonce string
	}{
		"boş":                     {"", testNonce},
		"iki parça":               {parts[0] + "." + parts[1], testNonce},
		"alg none":                {b64.EncodeToString([]byte(`{"alg":"none","kid":"kid-1"}`)) + "." + parts[1] + ".", testNonce},
		"HS256":                   {sign(t, p.key, map[string]any{"alg": "HS256", "kid": p.kid}, goodClaims()), testNonce},
		"kid yok":                 {sign(t, p.key, map[string]any{"alg": "RS256"}, goodClaims()), testNonce},
		"başka anahtarla imzalı":  {sign(t, other, hdr, goodClaims()), testNonce},
		"içerik değiştirilmiş":    {parts[0] + "." + b64.EncodeToString([]byte(`{"sub":"saldirgan"}`)) + "." + parts[2], testNonce},
		"yanlış issuer":           {sign(t, p.key, hdr, mod(func(c map[string]any) { c["iss"] = "https://evil.example" })), testNonce},
		"başka uygulamanın aud'u": {sign(t, p.key, hdr, mod(func(c map[string]any) { c["aud"] = "baska.apps.googleusercontent.com" })), testNonce},
		"süresi dolmuş": {sign(t, p.key, hdr, mod(func(c map[string]any) {
			c["iat"] = now0.Add(-2 * time.Hour).Unix()
			c["exp"] = now0.Add(-time.Hour).Unix()
		})), testNonce},
		"gelecekte verilmiş": {sign(t, p.key, hdr, mod(func(c map[string]any) {
			c["iat"] = now0.Add(time.Hour).Unix()
			c["exp"] = now0.Add(2 * time.Hour).Unix()
		})), testNonce},
		"ömür çok uzun":      {sign(t, p.key, hdr, mod(func(c map[string]any) { c["exp"] = now0.Add(48 * time.Hour).Unix() })), testNonce},
		"sub yok":            {sign(t, p.key, hdr, mod(func(c map[string]any) { delete(c, "sub") })), testNonce},
		"yanlış nonce":       {good, "baska-nonce"},
		"boş nonce":          {good, ""},
		"token'da nonce yok": {sign(t, p.key, hdr, mod(func(c map[string]any) { delete(c, "nonce") })), ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := g.Verify(context.Background(), tc.token, tc.nonce); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, ErrInvalidToken bekleniyordu", err)
			}
		})
	}
}

func TestGoogleEmailOnlyWhenVerified(t *testing.T) {
	p := newProvider(t)
	g := p.verifier(t, now0)
	hdr := map[string]any{"alg": "RS256", "kid": p.kid}
	for name, v := range map[string]any{"false": false, "\"false\"": "false", "yok": nil} {
		c := goodClaims()
		if v == nil {
			delete(c, "email_verified")
		} else {
			c["email_verified"] = v
		}
		id, err := g.Verify(context.Background(), sign(t, p.key, hdr, c), testNonce)
		if err != nil || id.Email != "" {
			t.Errorf("email_verified=%s: e-posta = %q, err=%v", name, id.Email, err)
		}
	}
	c := goodClaims()
	c["email_verified"] = "true"
	c["aud"] = []string{"baska", clientWeb}
	id, err := g.Verify(context.Background(), sign(t, p.key, hdr, c), testNonce)
	if err != nil || id.Email == "" {
		t.Fatalf("dizi aud ve \"true\" kabul edilmeli: %+v %v", id, err)
	}
}

func TestJWKSCachingAndRotation(t *testing.T) {
	p := newProvider(t)
	now := now0
	g := p.verifier(t, now)
	g.now = func() time.Time { return now }
	g.keys.now = g.now
	hdr := map[string]any{"alg": "RS256", "kid": p.kid}
	ctx := context.Background()

	for range 3 {
		if _, err := g.Verify(ctx, sign(t, p.key, hdr, goodClaims()), testNonce); err != nil {
			t.Fatal(err)
		}
	}
	if p.hits.Load() != 1 {
		t.Fatalf("JWKS %d kez çekildi, önbellek çalışmıyor", p.hits.Load())
	}

	// Rastgele kid'ler sağlayıcıyı çekmeye zorlayamaz.
	for _, kid := range []string{"x1", "x2", "x3"} {
		_, err := g.Verify(ctx, sign(t, p.key, map[string]any{"alg": "RS256", "kid": kid}, goodClaims()), testNonce)
		if !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("bilinmeyen kid: %v", err)
		}
	}
	if p.hits.Load() != 1 {
		t.Fatalf("bilinmeyen kid'ler %d ek çekime yol açtı", p.hits.Load()-1)
	}

	// Google anahtarı döndürür; yeni kid aralık dolunca alınır.
	newKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	p.key, p.kid = newKey, "kid-2"
	now = now.Add(jwksRefetchGap)
	if _, err := g.Verify(ctx, sign(t, newKey, map[string]any{"alg": "RS256", "kid": "kid-2"}, goodClaims()), testNonce); err != nil {
		t.Fatalf("döndürülen anahtar kabul edilmedi: %v", err)
	}
}

func TestJWKSRejectsWeakKeys(t *testing.T) {
	p := newProvider(t)
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	p.extra = []map[string]string{{
		"kty": "RSA", "kid": "zayif", "alg": "RS256",
		"n": b64.EncodeToString(weak.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(weak.E)).Bytes()),
	}}
	g := p.verifier(t, now0)
	_, err := g.Verify(context.Background(), sign(t, weak, map[string]any{"alg": "RS256", "kid": "zayif"}, goodClaims()), testNonce)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("1024 bit anahtar kabul edildi: %v", err)
	}
}

func TestProviderDownIsNotInvalidToken(t *testing.T) {
	p := newProvider(t)
	g := p.verifier(t, now0)
	p.srv.Close()
	_, err := g.Verify(context.Background(), sign(t, p.key, map[string]any{"alg": "RS256", "kid": p.kid}, goodClaims()), testNonce)
	if err == nil || errors.Is(err, ErrInvalidToken) || !errors.Is(err, errKeysUnavailable) {
		t.Fatalf("sağlayıcı erişilemezken err = %v", err)
	}
}

func TestCleanNameAndCache(t *testing.T) {
	if got := cleanName(strings.Repeat("ğ", 50)); len([]rune(got)) != 40 {
		t.Fatalf("ad kısaltılmadı: %d", len([]rune(got)))
	}
	for cc, want := range map[string]time.Duration{
		"public, max-age=21600": 6 * time.Hour,
		"max-age=1":             minJWKSCache,
		"max-age=9999999":       maxJWKSCache,
		"":                      minJWKSCache,
	} {
		if got := cacheFor(cc); got != want {
			t.Errorf("cacheFor(%q) = %v", cc, got)
		}
	}
	if _, err := NewGoogle(nil, http.DefaultClient); err == nil {
		t.Fatal("client ID'siz doğrulayıcı kabul edildi")
	}
}
