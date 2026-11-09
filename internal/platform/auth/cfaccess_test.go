package auth_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
)

const (
	testTeam = "tayfam-test.cloudflareaccess.com"
	testAUD  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func accessVerifier(t *testing.T, c *satest.Cluster, now func() time.Time) *auth.AccessVerifier {
	t.Helper()
	v, err := auth.NewAccessVerifier(auth.AccessVerifierConfig{
		TeamDomain: testTeam, Audience: testAUD, Client: c.Client(), CertsURL: c.JWKSURL(), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func accessClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss": "https://" + testTeam, "aud": []string{testAUD}, "sub": "cf-user-1",
		"email": "Yonetici@Example.com", "type": "app", "identity_nonce": "x", "country": "TR",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(8 * time.Hour).Unix(),
	}
}

func TestAccessVerifierAccepts(t *testing.T) {
	c := satest.New(t)
	now := time.Now()
	v := accessVerifier(t, c, func() time.Time { return now })
	tok := c.Token(t, satest.Spec{Alg: "RS256", Claims: accessClaims(now)})
	got, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "yonetici@example.com" || got.Subject != "cf-user-1" {
		t.Fatalf("claims = %+v", got)
	}
}

func TestAccessVerifierRejects(t *testing.T) {
	c := satest.New(t)
	now := time.Now()
	v := accessVerifier(t, c, func() time.Time { return now })
	with := func(f func(map[string]any)) string {
		cl := accessClaims(now)
		f(cl)
		return c.Token(t, satest.Spec{Alg: "RS256", Claims: cl})
	}
	for name, tok := range map[string]string{
		"boş":                      "",
		"biçim":                    "a.b",
		"çok uzun":                 strings.Repeat("a", 9000),
		"ES256":                    c.Token(t, satest.Spec{Alg: "ES256", Claims: accessClaims(now)}),
		"jku başlığı":              c.Token(t, satest.Spec{Header: map[string]any{"alg": "RS256", "kid": satest.KidRSA, "jku": "https://x"}, Claims: accessClaims(now)}),
		"bilinmeyen anahtar":       c.Token(t, satest.Spec{Alg: "RS256", Kid: "baska", Claims: accessClaims(now)}),
		"başka takım":              with(func(m map[string]any) { m["iss"] = "https://baska.cloudflareaccess.com" }),
		"başka uygulama":           with(func(m map[string]any) { m["aud"] = []string{strings.Repeat("f", 64)} }),
		"iki uygulama":             with(func(m map[string]any) { m["aud"] = []string{testAUD, strings.Repeat("f", 64)} }),
		"servis token'ı (tür yok)": with(func(m map[string]any) { delete(m, "type") }),
		"takım token'ı":            with(func(m map[string]any) { m["type"] = "org" }),
		"e-posta yok":              with(func(m map[string]any) { delete(m, "email") }),
		"geçersiz e-posta":         with(func(m map[string]any) { m["email"] = "yonetici" }),
		"sub yok":                  with(func(m map[string]any) { delete(m, "sub") }),
		"süresi dolmuş": with(func(m map[string]any) {
			m["iat"] = now.Add(-9 * time.Hour).Unix()
			m["exp"] = now.Add(-time.Hour).Unix()
		}),
		"24 saatten uzun": with(func(m map[string]any) { m["exp"] = now.Add(25 * time.Hour).Unix() }),
		"gelecekte":       with(func(m map[string]any) { m["iat"] = now.Add(time.Hour).Unix(); m["nbf"] = m["iat"] }),
		"nbf gelecekte":   with(func(m map[string]any) { m["nbf"] = now.Add(time.Hour).Unix() }),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), tok); !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("kabul edildi ya da yanlış hata: %v", err)
			}
		})
	}
	// A forged signature: valid claims, signature of another token.
	good := c.Token(t, satest.Spec{Alg: "RS256", Claims: accessClaims(now)})
	other := with(func(m map[string]any) { m["email"] = "saldirgan@example.com" })
	forged := other[:strings.LastIndex(other, ".")] + good[strings.LastIndex(good, "."):]
	if _, err := v.Verify(context.Background(), forged); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("sahte imza kabul edildi: %v", err)
	}
}

func TestAccessVerifierConfig(t *testing.T) {
	client := &http.Client{}
	for name, cfg := range map[string]auth.AccessVerifierConfig{
		"takım alan adı yok":  {Audience: testAUD, Client: client},
		"başka alan adı":      {TeamDomain: "tayfam.example.com", Audience: testAUD, Client: client},
		"AUD biçimi":          {TeamDomain: testTeam, Audience: "kisa", Client: client},
		"istemci yok":         {TeamDomain: testTeam, Audience: testAUD},
		"http anahtar adresi": {TeamDomain: testTeam, Audience: testAUD, Client: client, CertsURL: "http://x/certs"},
	} {
		if _, err := auth.NewAccessVerifier(cfg); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
}
