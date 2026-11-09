package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
)

const coreAud = "core.tayfam.internal"

var identitySA = auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-identity"}

func TestServiceVerifierAccepts(t *testing.T) {
	c := satest.New(t)
	v := c.Verifier(t, coreAud, nil)
	for _, alg := range []string{"RS256", "ES256"} {
		t.Run(alg, func(t *testing.T) {
			tok := c.Token(t, satest.Spec{Alg: alg, Audience: []string{coreAud}, Subject: identitySA.Subject()})
			got, err := v.Verify(context.Background(), tok)
			if err != nil {
				t.Fatal(err)
			}
			if got.Account != identitySA || got.Audience != coreAud || got.Issuer != satest.Issuer {
				t.Fatalf("claims = %+v", got)
			}
		})
	}
	if n := c.JWKSHits(); n != 1 {
		t.Fatalf("anahtarlar %d kez alındı, önbellek bekleniyordu", n)
	}
}

func TestServiceVerifierRejects(t *testing.T) {
	c := satest.New(t)
	now := time.Now()
	v := c.Verifier(t, coreAud, func() time.Time { return now })
	sub := identitySA.Subject()
	aud := []string{coreAud}
	valid := c.Token(t, satest.Spec{Audience: aud, Subject: sub, IssuedAt: now})
	parts := strings.Split(valid, ".")
	enc := base64.RawURLEncoding
	seg := func(v any) string { b, _ := json.Marshal(v); return enc.EncodeToString(b) }

	cases := map[string]string{
		"boş":                    "",
		"biçim":                  "a.b",
		"çok uzun":               strings.Repeat("a", 9000),
		"yanlış audience":        c.Token(t, satest.Spec{Audience: []string{"chat.tayfam.internal"}, Subject: sub, IssuedAt: now}),
		"iki audience":           c.Token(t, satest.Spec{Audience: []string{coreAud, "chat.tayfam.internal"}, Subject: sub, IssuedAt: now}),
		"audience yok":           c.Token(t, satest.Spec{Subject: sub, IssuedAt: now}),
		"yanlış issuer":          c.Token(t, satest.Spec{Issuer: "https://baska.cluster", Audience: aud, Subject: sub, IssuedAt: now}),
		"süresi dolmuş":          c.Token(t, satest.Spec{Audience: aud, Subject: sub, IssuedAt: now.Add(-20 * time.Minute)}),
		"ömür bir saatten uzun":  c.Token(t, satest.Spec{Audience: aud, Subject: sub, IssuedAt: now, TTL: 2 * time.Hour}),
		"gelecekte verilmiş":     c.Token(t, satest.Spec{Audience: aud, Subject: sub, IssuedAt: now.Add(5 * time.Minute)}),
		"ServiceAccount değil":   c.Token(t, satest.Spec{Audience: aud, Subject: "admin", IssuedAt: now}),
		"bozuk ServiceAccount":   c.Token(t, satest.Spec{Audience: aud, Subject: "system:serviceaccount:a:b:c", IssuedAt: now}),
		"kullanıcı token'ı gibi": c.Token(t, satest.Spec{Header: map[string]any{"alg": "EdDSA", "typ": "at+jwt", "kid": "k1"}, Audience: aud, Subject: sub, IssuedAt: now}),
		"alg none":               seg(map[string]any{"alg": "none", "kid": satest.KidEC}) + "." + parts[1] + ".",
		"HS256":                  seg(map[string]any{"alg": "HS256", "kid": satest.KidEC}) + "." + parts[1] + "." + parts[2],
		"jku başlığı":            c.Token(t, satest.Spec{Header: map[string]any{"alg": "ES256", "kid": satest.KidEC, "jku": "https://x"}, Audience: aud, Subject: sub, IssuedAt: now}),
		"bilinmeyen kid":         c.Token(t, satest.Spec{Kid: "baska-anahtar", Audience: aud, Subject: sub, IssuedAt: now}),
		// The RSA key with an ES256 header and the reverse: key type must
		// match the algorithm.
		"ES256 başlık RSA anahtar": seg(map[string]any{"alg": "ES256", "kid": satest.KidRSA}) + "." + parts[1] + "." + parts[2],
		"RS256 başlık EC anahtar":  c.Token(t, satest.Spec{Header: map[string]any{"alg": "RS256", "kid": satest.KidEC}, Audience: aud, Subject: sub, IssuedAt: now}),
		"değiştirilmiş içerik": parts[0] + "." + seg(map[string]any{
			"iss": satest.Issuer, "aud": aud, "sub": "system:serviceaccount:tayfam-test:tayfam-core",
			"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}) + "." + parts[2],
		"exp yok": c.Token(t, satest.Spec{Claims: map[string]any{"iss": satest.Issuer, "aud": aud, "sub": sub, "iat": now.Unix()}}),
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := v.Verify(context.Background(), tok)
			if !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if _, err := v.Verify(context.Background(), valid); err != nil {
		t.Fatalf("geçerli token reddedildi: %v", err)
	}
}

func TestServiceVerifierFetchFailureIsNotInvalidToken(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "yok", http.StatusForbidden)
	}))
	defer srv.Close()
	v, err := auth.NewServiceVerifier(auth.ServiceVerifierConfig{
		Issuer: satest.Issuer, Audience: coreAud, JWKSURL: srv.URL + "/openid/v1/jwks", Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := satest.New(t)
	_, err = v.Verify(context.Background(), c.ServiceToken(t, identitySA, coreAud))
	if err == nil || errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceVerifierDoesNotFollowRedirects(t *testing.T) {
	c := satest.New(t)
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, c.JWKSURL(), http.StatusFound)
	}))
	defer redirect.Close()
	// A client that trusts both servers.
	client := c.Client()
	v, err := auth.NewServiceVerifier(auth.ServiceVerifierConfig{
		Issuer: satest.Issuer, Audience: coreAud, JWKSURL: redirect.URL + "/openid/v1/jwks", Client: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), c.ServiceToken(t, identitySA, coreAud)); err == nil {
		t.Fatal("yönlendirme izlendi")
	}
	if c.JWKSHits() != 0 {
		t.Fatal("yönlendirilen adrese istek gitti")
	}
}

func TestServiceVerifierSendsBearer(t *testing.T) {
	c := satest.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("api-sunucusu-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewServiceVerifier(auth.ServiceVerifierConfig{
		Issuer: satest.Issuer, Audience: coreAud, JWKSURL: c.JWKSURL(), Client: c.Client(),
		BearerToken: auth.TokenFile(path).Token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), c.ServiceToken(t, identitySA, coreAud)); err != nil {
		t.Fatal(err)
	}
	if got := c.LastBearer(); got != "api-sunucusu-token" {
		t.Fatalf("bearer = %q", got)
	}
}

func TestNewServiceVerifierConfig(t *testing.T) {
	client := http.DefaultClient
	for name, cfg := range map[string]auth.ServiceVerifierConfig{
		"issuer yok":   {Audience: coreAud, JWKSURL: "https://k/openid/v1/jwks", Client: client},
		"audience yok": {Issuer: satest.Issuer, JWKSURL: "https://k/openid/v1/jwks", Client: client},
		"istemci yok":  {Issuer: satest.Issuer, Audience: coreAud, JWKSURL: "https://k/openid/v1/jwks"},
		"http adres":   {Issuer: satest.Issuer, Audience: coreAud, JWKSURL: "http://k/openid/v1/jwks", Client: client},
		"adres yok":    {Issuer: satest.Issuer, Audience: coreAud, Client: client},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := auth.NewServiceVerifier(cfg); err == nil {
				t.Fatal("hata bekleniyordu")
			}
		})
	}
}

func TestParseServiceAccount(t *testing.T) {
	for sub, ok := range map[string]bool{
		"system:serviceaccount:tayfam-dev:tayfam-identity": true,
		"system:serviceaccount:tayfam-dev":                 false,
		"system:serviceaccount::tayfam-identity":           false,
		"system:serviceaccount:Tayfam:x":                   false,
		"system:serviceaccount:a:b:c":                      false,
		"system:node:x":                                    false,
		"":                                                 false,
	} {
		sa, got := auth.ParseServiceAccount(sub)
		if got != ok || (ok && sa.Subject() != sub) {
			t.Errorf("%q: %v %+v", sub, got, sa)
		}
	}
}

func TestTokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	f := auth.TokenFile(path)
	if _, err := f.Token(); err == nil {
		t.Fatal("olmayan dosya")
	}
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Token(); err == nil {
		t.Fatal("boş dosya")
	}
	// kubelet renews the file; the new token is read on the next use.
	for _, want := range []string{"birinci", "ikinci"} {
		if err := os.WriteFile(path, []byte(want+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := f.Token(); err != nil || got != want {
			t.Fatalf("token = %q, %v", got, err)
		}
	}
	if err := os.WriteFile(path, make([]byte, 17<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Token(); err == nil {
		t.Fatal("çok büyük dosya")
	}
	if _, err := auth.TokenFile("").Token(); err == nil {
		t.Fatal("boş yol")
	}
}
