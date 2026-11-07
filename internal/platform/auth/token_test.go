package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testIssuer   = "https://identity.tayfam.test"
	testAudience = "tayfam-api"
	testUser     = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	testSession  = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"
)

var t0 = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

// testKey derives a deterministic key from a label; tests never use real
// keys.
func testKey(t testing.TB, id string) SigningKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	copy(seed, id)
	k, err := ParseSigningKey(id, base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newPair(t testing.TB, key SigningKey, now time.Time, keys StaticKeys) (*Signer, *Verifier) {
	t.Helper()
	s, err := NewSigner(SignerConfig{Key: key, Issuer: testIssuer, Audience: testAudience, TTL: 15 * time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if keys == nil {
		keys = StaticKeys{key.ID: key.Public().Key}
	}
	v, err := NewVerifier(VerifierConfig{Keys: keys, Issuer: testIssuer, Audience: testAudience, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return s, v
}

func TestIssueAndVerify(t *testing.T) {
	key := testKey(t, "k1")
	s, v := newPair(t, key, t0, nil)
	tok, exp, err := s.Issue(testUser, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(t0.Add(15 * time.Minute)) {
		t.Fatalf("exp = %v", exp)
	}
	c, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != testUser || c.SessionID != testSession || c.KeyID != "k1" || !c.ExpiresAt.Equal(exp) {
		t.Fatalf("claims = %+v", c)
	}
}

// forge builds a token with arbitrary header and payload, signed by key.
func forge(key ed25519.PrivateKey, hdr, pl any) string {
	h, _ := json.Marshal(hdr)
	p, _ := json.Marshal(pl)
	in := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	return in + "." + b64.EncodeToString(ed25519.Sign(key, []byte(in)))
}

func TestVerifyRejects(t *testing.T) {
	key := testKey(t, "k1")
	other := testKey(t, "k2")
	s, v := newPair(t, key, t0, nil)
	good, _, err := s.Issue(testUser, testSession)
	if err != nil {
		t.Fatal(err)
	}
	okHeader := map[string]string{"alg": "EdDSA", "typ": "at+jwt", "kid": "k1"}
	okPayload := func(mod func(map[string]any)) map[string]any {
		p := map[string]any{"iss": testIssuer, "aud": testAudience, "sub": testUser, "sid": testSession, "iat": t0.Unix(), "exp": t0.Add(15 * time.Minute).Unix()}
		if mod != nil {
			mod(p)
		}
		return p
	}
	parts := strings.Split(good, ".")

	tests := []struct {
		name  string
		token string
	}{
		{"boş", ""},
		{"iki parça", parts[0] + "." + parts[1]},
		{"dört parça", good + ".x"},
		{"çok uzun", strings.Repeat("a", maxTokenBytes+1)},
		{"imzasız (alg none)", forge(key.private, map[string]string{"alg": "none", "typ": "at+jwt", "kid": "k1"}, okPayload(nil))},
		{"başka algoritma", forge(key.private, map[string]string{"alg": "HS256", "typ": "at+jwt", "kid": "k1"}, okPayload(nil))},
		{"yanlış tür", forge(key.private, map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "k1"}, okPayload(nil))},
		{"kid yok", forge(key.private, map[string]string{"alg": "EdDSA", "typ": "at+jwt"}, okPayload(nil))},
		{"geçersiz kid", forge(key.private, map[string]string{"alg": "EdDSA", "typ": "at+jwt", "kid": "../k1"}, okPayload(nil))},
		{"başlıkta fazla alan", forge(key.private, map[string]string{"alg": "EdDSA", "typ": "at+jwt", "kid": "k1", "jku": "https://kötü.example"}, okPayload(nil))},
		{"bilinmeyen anahtar", forge(other.private, map[string]string{"alg": "EdDSA", "typ": "at+jwt", "kid": "k2"}, okPayload(nil))},
		{"başka anahtarla imzalı", forge(other.private, okHeader, okPayload(nil))},
		{"içerik değiştirilmiş", parts[0] + "." + b64.EncodeToString([]byte(`{"iss":"`+testIssuer+`","aud":"`+testAudience+`","sub":"baska","sid":"`+testSession+`","iat":1,"exp":2}`)) + "." + parts[2]},
		{"imza değiştirilmiş", parts[0] + "." + parts[1] + "." + b64.EncodeToString(make([]byte, ed25519.SignatureSize))},
		{"imza kısa", parts[0] + "." + parts[1] + "." + parts[2][:20]},
		{"base64 dolgu", parts[0] + "=." + parts[1] + "." + parts[2]},
		{"içerikte fazla alan", forge(key.private, okHeader, okPayload(func(p map[string]any) { p["role"] = "admin" }))},
		{"yanlış issuer", forge(key.private, okHeader, okPayload(func(p map[string]any) { p["iss"] = "https://kötü.example" }))},
		{"yanlış audience", forge(key.private, okHeader, okPayload(func(p map[string]any) { p["aud"] = "tayfam-admin" }))},
		{"audience dizi", forge(key.private, okHeader, okPayload(func(p map[string]any) { p["aud"] = []string{testAudience} }))},
		{"sub boş", forge(key.private, okHeader, okPayload(func(p map[string]any) { p["sub"] = "" }))},
		{"sid yok", forge(key.private, okHeader, okPayload(func(p map[string]any) { delete(p, "sid") }))},
		{"süresi dolmuş", forge(key.private, okHeader, okPayload(func(p map[string]any) {
			p["iat"] = t0.Add(-time.Hour).Unix()
			p["exp"] = t0.Add(-45 * time.Minute).Unix()
		}))},
		{"gelecekte verilmiş", forge(key.private, okHeader, okPayload(func(p map[string]any) {
			p["iat"] = t0.Add(time.Hour).Unix()
			p["exp"] = t0.Add(time.Hour + 10*time.Minute).Unix()
		}))},
		{"ömür çok uzun", forge(key.private, okHeader, okPayload(func(p map[string]any) { p["exp"] = t0.Add(24 * time.Hour).Unix() }))},
		{"exp iat'ten önce", forge(key.private, okHeader, okPayload(func(p map[string]any) { p["exp"] = t0.Add(-time.Minute).Unix() }))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := v.Verify(context.Background(), tt.token)
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, ErrInvalidToken bekleniyordu", err)
			}
		})
	}
}

func TestVerifyClockSkew(t *testing.T) {
	key := testKey(t, "k1")
	s, _ := newPair(t, key, t0, nil)
	tok, exp, err := s.Issue(testUser, testSession)
	if err != nil {
		t.Fatal(err)
	}
	keys := StaticKeys{"k1": key.Public().Key}
	for _, tc := range []struct {
		now time.Time
		ok  bool
	}{
		{t0.Add(-clockSkew + time.Second), true}, // verifier's clock slightly behind
		{exp.Add(clockSkew - time.Second), true},
		{exp.Add(clockSkew), false},
	} {
		v, _ := NewVerifier(VerifierConfig{Keys: keys, Issuer: testIssuer, Audience: testAudience, Now: func() time.Time { return tc.now }})
		_, err := v.Verify(context.Background(), tok)
		if (err == nil) != tc.ok {
			t.Errorf("now=%v: err=%v, ok=%v bekleniyordu", tc.now, err, tc.ok)
		}
	}
}

type failingKeys struct{}

func (failingKeys) PublicKey(context.Context, string) (ed25519.PublicKey, error) {
	return nil, errors.New("identity erişilemez")
}

func TestVerifyKeySourceFailureIsNotInvalidToken(t *testing.T) {
	key := testKey(t, "k1")
	s, _ := newPair(t, key, t0, nil)
	tok, _, _ := s.Issue(testUser, testSession)
	v, _ := NewVerifier(VerifierConfig{Keys: failingKeys{}, Issuer: testIssuer, Audience: testAudience, Now: func() time.Time { return t0 }})
	_, err := v.Verify(context.Background(), tok)
	if err == nil || errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v; altyapı hatası bekleniyordu", err)
	}
}

func TestParseSigningKey(t *testing.T) {
	seed := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	for _, tc := range []struct {
		id, seed string
		ok       bool
	}{
		{"2026-11", seed, true},
		{"", seed, false},
		{"Büyük", seed, false},
		{strings.Repeat("a", 33), seed, false},
		{"k1", "***", false},
		{"k1", base64.StdEncoding.EncodeToString(make([]byte, 16)), false},
	} {
		_, err := ParseSigningKey(tc.id, tc.seed)
		if (err == nil) != tc.ok {
			t.Errorf("ParseSigningKey(%q): err=%v", tc.id, err)
		}
	}
}

func TestParseSigningKeyErrorDoesNotEchoInput(t *testing.T) {
	secret := "c2VjcmV0LXZhbHVl!!"
	_, err := ParseSigningKey("k1", secret)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "c2VjcmV0") {
		t.Fatalf("hata girdiyi içeriyor: %v", err)
	}
}

func TestNewSignerLimits(t *testing.T) {
	key := testKey(t, "k1")
	for _, ttl := range []time.Duration{0, MaxTokenTTL + time.Second} {
		if _, err := NewSigner(SignerConfig{Key: key, Issuer: testIssuer, Audience: testAudience, TTL: ttl}); err == nil {
			t.Errorf("TTL %v kabul edildi", ttl)
		}
	}
	if _, err := NewSigner(SignerConfig{Issuer: testIssuer, Audience: testAudience, TTL: time.Minute}); err == nil {
		t.Error("anahtarsız imzalayıcı kabul edildi")
	}
}

func FuzzVerify(f *testing.F) {
	key := testKey(f, "k1")
	s, v := newPair(f, key, t0, nil)
	good, _, _ := s.Issue(testUser, testSession)
	f.Add(good)
	f.Add("a.b.c")
	f.Add("")
	f.Fuzz(func(t *testing.T, tok string) {
		c, err := v.Verify(context.Background(), tok)
		if err == nil && tok != good {
			// Any other accepted token would be a forgery.
			t.Fatalf("beklenmeyen token kabul edildi: %+v", c)
		}
	})
}
