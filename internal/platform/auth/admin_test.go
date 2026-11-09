package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newAdminPair(t testing.TB, key SigningKey, now time.Time) (*AdminSigner, *AdminVerifier) {
	t.Helper()
	clock := func() time.Time { return now }
	s, err := NewAdminSigner(AdminSignerConfig{Key: key, Issuer: testIssuer, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewAdminVerifier(AdminVerifierConfig{Keys: StaticKeys{key.ID: key.Public().Key}, Issuer: testIssuer, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	return s, v
}

func TestAdminIssueAndVerify(t *testing.T) {
	key := testKey(t, "k1")
	s, v := newAdminPair(t, key, t0)
	authTime := t0.Add(-time.Minute)
	tok, exp, err := s.Issue(testUser, testSession, authTime)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(t0.Add(8 * time.Hour)) {
		t.Fatalf("exp = %v", exp)
	}
	c, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != testUser || c.SessionID != testSession || c.Audience != AdminAudience ||
		!c.AuthTime.Equal(authTime) || !c.ExpiresAt.Equal(exp) {
		t.Fatalf("claims = %+v", c)
	}
}

// TestAdminAndAccessTokensDoNotCross: an app token never passes in the panel
// and a panel token never passes in the app (ADR-0008, layer 5), whatever
// audience the app is configured with.
func TestAdminAndAccessTokensDoNotCross(t *testing.T) {
	key := testKey(t, "k1")
	ctx := context.Background()
	appSigner, appVerifier := newPair(t, key, t0, nil)
	adminSigner, adminVerifier := newAdminPair(t, key, t0)

	appTok, _, err := appSigner.Issue(testUser, testSession)
	if err != nil {
		t.Fatal(err)
	}
	adminTok, _, err := adminSigner.Issue(testUser, testSession, t0)
	if err != nil {
		t.Fatal(err)
	}
	// An access token signer configured with the admin audience by mistake
	// still issues an access token, not an admin token.
	sameAud, err := NewSigner(SignerConfig{Key: key, Issuer: testIssuer, Audience: AdminAudience, TTL: 15 * time.Minute, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	sameAudTok, _, err := sameAud.Issue(testUser, testSession)
	if err != nil {
		t.Fatal(err)
	}
	// And an app verifier configured to accept the admin audience still
	// refuses admin tokens.
	lenient, err := NewVerifier(VerifierConfig{Keys: StaticKeys{key.ID: key.Public().Key}, Issuer: testIssuer, Audience: testAudience,
		AlsoAccept: []string{AdminAudience}, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		verify func(string) error
		token  string
		ok     bool
	}{
		{"uygulama token'ı uygulamada", func(s string) error { _, err := appVerifier.Verify(ctx, s); return err }, appTok, true},
		{"admin token'ı panelde", func(s string) error { _, err := adminVerifier.Verify(ctx, s); return err }, adminTok, true},
		{"uygulama token'ı panelde", func(s string) error { _, err := adminVerifier.Verify(ctx, s); return err }, appTok, false},
		{"admin audience'lı erişim token'ı panelde", func(s string) error { _, err := adminVerifier.Verify(ctx, s); return err }, sameAudTok, false},
		{"admin token'ı uygulamada", func(s string) error { _, err := appVerifier.Verify(ctx, s); return err }, adminTok, false},
		{"admin token'ı admin audience'ını da kabul eden uygulama doğrulayıcısında", func(s string) error { _, err := lenient.Verify(ctx, s); return err }, adminTok, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.verify(tc.token)
			if tc.ok && err != nil {
				t.Fatalf("reddedildi: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("kabul edildi ya da yanlış hata: %v", err)
			}
		})
	}
}

func TestAdminVerifyRejects(t *testing.T) {
	key := testKey(t, "k1")
	_, v := newAdminPair(t, key, t0)
	sign := func(p adminPayload) string {
		tok, err := signJWS(key.private, key.ID, typAdmin, p)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	valid := adminPayload{Iss: testIssuer, Aud: AdminAudience, Sub: testUser, Sid: testSession,
		Iat: t0.Unix(), Exp: t0.Add(8 * time.Hour).Unix(), AuthTime: t0.Unix()}
	if _, err := v.Verify(context.Background(), sign(valid)); err != nil {
		t.Fatalf("geçerli token reddedildi: %v", err)
	}
	with := func(f func(*adminPayload)) string { p := valid; f(&p); return sign(p) }
	other := testKey(t, "k2")
	foreign, err := signJWS(other.private, "k1", typAdmin, valid)
	if err != nil {
		t.Fatal(err)
	}
	wrongTyp, err := signJWS(key.private, key.ID, typAccess, valid)
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{
		"boş":                 "",
		"çok uzun":            strings.Repeat("a", 3000),
		"başka anahtar":       foreign,
		"erişim token türü":   wrongTyp,
		"yanlış issuer":       with(func(p *adminPayload) { p.Iss = "x" }),
		"uygulama audience'ı": with(func(p *adminPayload) { p.Aud = testAudience }),
		"kullanıcı yok":       with(func(p *adminPayload) { p.Sub = "" }),
		"oturum yok":          with(func(p *adminPayload) { p.Sid = "" }),
		"8 saatten uzun":      with(func(p *adminPayload) { p.Exp = t0.Add(8*time.Hour + time.Second).Unix() }),
		"süresi dolmuş": with(func(p *adminPayload) {
			p.Iat = t0.Add(-9 * time.Hour).Unix()
			p.Exp = t0.Add(-time.Hour).Unix()
			p.AuthTime = p.Iat
		}),
		"gelecekte verilmiş": with(func(p *adminPayload) {
			p.Iat = t0.Add(time.Hour).Unix()
			p.Exp = t0.Add(2 * time.Hour).Unix()
			p.AuthTime = p.Iat
		}),
		"auth_time yok":           with(func(p *adminPayload) { p.AuthTime = 0 }),
		"auth_time iat'ten sonra": with(func(p *adminPayload) { p.AuthTime = t0.Add(time.Minute).Unix() }),
		"auth_time çok eski":      with(func(p *adminPayload) { p.AuthTime = t0.Add(-6 * time.Minute).Unix() }),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("kabul edildi ya da yanlış hata: %v", err)
			}
		})
	}
}

func TestAdminSignerRefusesStaleSignIn(t *testing.T) {
	s, _ := newAdminPair(t, testKey(t, "k1"), t0)
	for name, at := range map[string]time.Time{
		"5 dakikadan eski": t0.Add(-AdminReauthWindow - time.Second),
		"gelecekte":        t0.Add(time.Hour),
		"sıfır":            {},
	} {
		if _, _, err := s.Issue(testUser, testSession, at); err == nil {
			t.Errorf("%s: token verildi", name)
		}
	}
	if _, err := NewAdminSigner(AdminSignerConfig{Key: testKey(t, "k1"), Issuer: testIssuer, TTL: 9 * time.Hour}); err == nil {
		t.Fatal("8 saatten uzun ömür kabul edildi")
	}
}

func TestRequireRecentAuth(t *testing.T) {
	c := AdminClaims{AuthTime: t0}
	for _, tc := range []struct {
		now  time.Time
		want error
	}{
		{t0, nil},
		{t0.Add(AdminReauthWindow), nil},
		{t0.Add(AdminReauthWindow + time.Second), ErrReauthenticationRequired},
		{t0.Add(time.Hour), ErrReauthenticationRequired},
	} {
		if got := c.RequireRecentAuth(tc.now); !errors.Is(got, tc.want) && got != tc.want {
			t.Errorf("%v sonra: %v, beklenen %v", tc.now.Sub(t0), got, tc.want)
		}
	}
	if (AdminClaims{}).RequireRecentAuth(t0) == nil {
		t.Fatal("auth_time'sız claims kabul edildi")
	}
}
