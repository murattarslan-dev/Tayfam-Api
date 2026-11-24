package oidc

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	appleBundle = "com.example.tayfam"
	appleIssuer = "https://appleid.apple.com"
)

func (p *provider) appleVerifier(t *testing.T, now time.Time) *Apple {
	t.Helper()
	a, err := newApple([]string{appleBundle}, p.srv.URL, p.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	a.keys.now = a.now
	return a
}

// appleClaims mirrors a native Sign in with Apple identity token: the
// audience is the bundle ID, booleans come as strings and there is no name.
func appleClaims() map[string]any {
	return map[string]any{
		"iss": appleIssuer, "aud": appleBundle,
		"sub":   "001234.abcdef0123456789abcdef0123456789.1234",
		"email": "abc123@privaterelay.appleid.com", "email_verified": "true", "is_private_email": "true",
		"iat": now0.Add(-time.Minute).Unix(), "exp": now0.Add(9 * time.Minute).Unix(),
		"nonce": testNonce, "nonce_supported": true,
	}
}

func TestAppleVerify(t *testing.T) {
	p := newProvider(t)
	a := p.appleVerifier(t, now0)
	hdr := map[string]any{"alg": "RS256", "kid": p.kid}
	id, err := a.Verify(context.Background(), sign(t, p.key, hdr, appleClaims()), testNonce)
	if err != nil {
		t.Fatal(err)
	}
	if id.Provider != "apple" || id.Subject != "001234.abcdef0123456789abcdef0123456789.1234" {
		t.Fatalf("kimlik = %+v", id)
	}
	// A private relay address is an ordinary verified email (ADR-0004).
	if id.Email != "abc123@privaterelay.appleid.com" {
		t.Fatalf("e-posta = %q", id.Email)
	}
	// Apple puts no name in the token (ADR-0045 §2).
	if id.Name != "" {
		t.Fatalf("ad = %q", id.Name)
	}
	if !id.IssuedAt.Equal(now0.Add(-time.Minute)) {
		t.Fatalf("IssuedAt = %v", id.IssuedAt)
	}
}

func TestAppleRejects(t *testing.T) {
	p := newProvider(t)
	a := p.appleVerifier(t, now0)
	hdr := map[string]any{"alg": "RS256", "kid": p.kid}
	with := func(k string, v any) map[string]any {
		c := appleClaims()
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
		return c
	}
	for name, tc := range map[string]struct {
		claims map[string]any
		nonce  string
	}{
		"Google'ın issuer'ı":           {with("iss", "https://accounts.google.com"), testNonce},
		"başka uygulamanın token'ı":    {with("aud", "com.example.baska"), testNonce},
		"Google client'ına verilmiş":   {with("aud", clientAndroid), testNonce},
		"yanlış nonce":                 {appleClaims(), "baska-nonce"},
		"token'da nonce yok":           {with("nonce", nil), testNonce},
		"nonce desteklenmiyor":         {with("nonce_supported", false), testNonce},
		"nonce desteklenmiyor (metin)": {with("nonce_supported", "false"), testNonce},
		"süresi dolmuş":                {with("exp", now0.Add(-2*time.Minute).Unix()), testNonce},
		"subject yok":                  {with("sub", nil), testNonce},
	} {
		_, err := a.Verify(context.Background(), sign(t, p.key, hdr, tc.claims), tc.nonce)
		if !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v, ErrInvalidToken bekleniyordu", name, err)
		}
	}
}

// TestAppleNonceSupportedAbsent: older tokens carry no nonce_supported claim;
// the nonce itself is still checked.
func TestAppleNonceSupportedAbsent(t *testing.T) {
	p := newProvider(t)
	a := p.appleVerifier(t, now0)
	c := appleClaims()
	delete(c, "nonce_supported")
	if _, err := a.Verify(context.Background(), sign(t, p.key, map[string]any{"alg": "RS256", "kid": p.kid}, c), testNonce); err != nil {
		t.Fatal(err)
	}
}

func TestAppleEmailOnlyWhenVerified(t *testing.T) {
	p := newProvider(t)
	a := p.appleVerifier(t, now0)
	hdr := map[string]any{"alg": "RS256", "kid": p.kid}
	for name, v := range map[string]any{"false": false, "\"false\"": "false", "yok": nil} {
		c := appleClaims()
		if v == nil {
			delete(c, "email_verified")
		} else {
			c["email_verified"] = v
		}
		id, err := a.Verify(context.Background(), sign(t, p.key, hdr, c), testNonce)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if id.Email != "" {
			t.Errorf("%s: doğrulanmamış e-posta alındı: %q", name, id.Email)
		}
	}
}

// TestGoogleRejectsAppleToken: the two verifiers never accept each other's
// tokens even when signed by a key they trust.
func TestGoogleRejectsAppleToken(t *testing.T) {
	p := newProvider(t)
	g := p.verifier(t, now0)
	tok := sign(t, p.key, map[string]any{"alg": "RS256", "kid": p.kid}, appleClaims())
	if _, err := g.Verify(context.Background(), tok, testNonce); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewAppleNeedsClientID(t *testing.T) {
	if _, err := newApple([]string{"", ""}, "http://127.0.0.1:0", nil); err == nil {
		t.Fatal("client ID olmadan doğrulayıcı kuruldu")
	}
}
