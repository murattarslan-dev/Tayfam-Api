package session

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/oidc"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeGoogle accepts tokens of the form "ok:<subject>" with nonce "n".
type fakeGoogle struct{}

func (fakeGoogle) Verify(_ context.Context, tok, nonce string) (oidc.Identity, error) {
	if len(tok) > 3 && tok[:3] == "ok:" && nonce == "n" {
		return oidc.Identity{Provider: "google", Subject: tok[3:], Email: "ornek@example.com", Name: "Ayşe"}, nil
	}
	if tok == "down" {
		return oidc.Identity{}, errors.New("google erişilemez")
	}
	return oidc.Identity{}, oidc.ErrInvalidToken
}

type env struct {
	svc  *Service
	pool *pgxpool.Pool
	// admin is a superuser pool for fixtures identity_app cannot write
	// (consent withdrawal has no query yet).
	admin *pgxpool.Pool
	now   time.Time
	api   *auth.Verifier
	reg   *auth.Verifier
	clock *time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "identity", identitydb.Migrations())
	pool := d.App
	seed := make([]byte, ed25519.SeedSize)
	copy(seed, "session-test")
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, admin: d.Admin, now: time.Now()}
	e.clock = &e.now
	clock := func() time.Time { return *e.clock }
	signer := func(aud string) *auth.Signer {
		s, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-identity", Audience: aud, TTL: 15 * time.Minute, Now: clock})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	verifier := func(aud string) *auth.Verifier {
		v, _ := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: aud, Now: clock})
		return v
	}
	e.api, e.reg = verifier(AudienceAPI), verifier(AudienceRegistration)
	e.svc, err = New(Config{
		Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifiers:    map[string]oidc.Verifier{"google": fakeGoogle{}},
		API:          signer(AudienceAPI),
		Registration: signer(AudienceRegistration),
		Documents:    testDocs,
		Now:          clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) signIn(t *testing.T, subject string) Tokens {
	t.Helper()
	tok, err := e.svc.SignIn(context.Background(), SignInInput{Provider: "google", IDToken: "ok:" + subject, Nonce: "n", Platform: "android"})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *env) register(t *testing.T, user id.ID) {
	t.Helper()
	ct := make([]byte, 40)
	if _, err := store.New(e.pool).CompleteRegistration(context.Background(), store.CompleteRegistrationParams{ID: user.PG(), BirthDateEnc: ct}); err != nil {
		t.Fatal(err)
	}
}

func TestNewUserGetsRegistrationOnlyToken(t *testing.T) {
	e := setup(t)
	tok := e.signIn(t, "sub-1")
	if !tok.RegistrationPending {
		t.Fatal("yeni kullanıcı kayıt bekliyor olmalı")
	}
	ctx := context.Background()
	if _, err := e.api.Verify(ctx, tok.AccessToken); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("kayıt bekleyen token API'de geçti: %v", err)
	}
	c, err := e.reg.Verify(ctx, tok.AccessToken)
	if err != nil || c.Subject != tok.UserID.String() || c.SessionID != tok.SessionID.String() {
		t.Fatalf("kayıt token'ı: %+v %v", c, err)
	}
	if tok.RefreshExpiresAt.Sub(e.now) != sessionTTL {
		t.Fatalf("oturum süresi = %v", tok.RefreshExpiresAt.Sub(e.now))
	}
}

func TestSameAccountSameUserNewSession(t *testing.T) {
	e := setup(t)
	a, b := e.signIn(t, "sub-1"), e.signIn(t, "sub-1")
	if a.UserID != b.UserID || a.SessionID == b.SessionID || a.RefreshToken == b.RefreshToken {
		t.Fatalf("a=%v/%v b=%v/%v", a.UserID, a.SessionID, b.UserID, b.SessionID)
	}
	if c := e.signIn(t, "sub-2"); c.UserID == a.UserID {
		t.Fatal("farklı Google hesabı aynı kullanıcıya bağlandı")
	}
}

func TestRegisteredUserGetsAPIToken(t *testing.T) {
	e := setup(t)
	first := e.signIn(t, "sub-1")
	e.register(t, first.UserID)
	tok := e.signIn(t, "sub-1")
	if tok.RegistrationPending {
		t.Fatal("kayıtlı kullanıcı bekliyor görünüyor")
	}
	if _, err := e.api.Verify(context.Background(), tok.AccessToken); err != nil {
		t.Fatalf("API token'ı: %v", err)
	}
	// Kayıt bekleyen oturumun refresh'i, kayıt tamamlanınca API token'ı alır.
	r, err := e.svc.Refresh(context.Background(), first.RefreshToken)
	if err != nil || r.RegistrationPending {
		t.Fatalf("kayıttan sonra yenileme: %+v %v", r, err)
	}
}

func TestSignInRejects(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		in   SignInInput
		want error
	}{
		"geçersiz token":       {SignInInput{"google", "bozuk", "n", "android"}, ErrInvalidCredentials},
		"yanlış nonce":         {SignInInput{"google", "ok:x", "baska", "android"}, ErrInvalidCredentials},
		"bilinmeyen sağlayıcı": {SignInInput{"facebook", "ok:x", "n", "android"}, ErrUnsupportedProvider},
		"bilinmeyen platform":  {SignInInput{"google", "ok:x", "n", "symbian"}, ErrInvalidCredentials},
		"sağlayıcı erişilemez": {SignInInput{"google", "down", "n", "android"}, ErrUnavailable},
	} {
		if _, err := e.svc.SignIn(ctx, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, %v bekleniyordu", name, err, tc.want)
		}
	}
}

func TestAgeBlockedCannotSignIn(t *testing.T) {
	e := setup(t)
	tok := e.signIn(t, "sub-genc")
	if _, err := store.New(e.pool).BlockRegistrationForAge(context.Background(), tok.UserID.PG()); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.SignIn(context.Background(), SignInInput{"google", "ok:sub-genc", "n", "android"})
	if !errors.Is(err, ErrAgeBlocked) {
		t.Fatalf("err = %v", err)
	}
}

func TestRefreshRotates(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	t0 := e.signIn(t, "sub-1")
	*e.clock = e.now.Add(time.Hour)
	t1, err := e.svc.Refresh(ctx, t0.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if t1.RefreshToken == t0.RefreshToken || t1.SessionID != t0.SessionID || t1.UserID != t0.UserID {
		t.Fatal("yenileme yeni bir refresh token vermedi veya oturum değişti")
	}
	if t1.RefreshExpiresAt.Sub(*e.clock) != sessionTTL {
		t.Fatal("oturum süresi yenilemede uzamadı")
	}
	if _, err := e.svc.Refresh(ctx, t1.RefreshToken); err != nil {
		t.Fatalf("yeni token kullanılamadı: %v", err)
	}
}

func TestRefreshReuseRevokesSession(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	t0 := e.signIn(t, "sub-1")
	t1, err := e.svc.Refresh(ctx, t0.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	// Aynı anda gelen ikinci istek: oturum korunur, istek reddedilir.
	*e.clock = e.now.Add(reuseGrace / 2)
	if _, err := e.svc.Refresh(ctx, t0.RefreshToken); !errors.Is(err, ErrRefreshConflict) {
		t.Fatalf("tolerans içinde err = %v", err)
	}
	if _, err := store.New(e.pool).GetActiveSession(ctx, t0.SessionID.PG()); err != nil {
		t.Fatal("tolerans içinde oturum iptal edildi")
	}

	// Tolerans geçtikten sonra eski token: çalınmış sayılır.
	*e.clock = e.now.Add(reuseGrace + time.Second)
	if _, err := e.svc.Refresh(ctx, t0.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("yeniden kullanım err = %v", err)
	}
	// Meşru kullanıcının yeni token'ı da artık geçersiz: oturum bitti.
	if _, err := e.svc.Refresh(ctx, t1.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("iptal edilen oturumun token'ı err = %v", err)
	}
	var reason string
	if err := e.pool.QueryRow(ctx, `SELECT revoked_reason FROM sessions WHERE id = $1`, t0.SessionID.PG()).Scan(&reason); err != nil || reason != "token_reuse" {
		t.Fatalf("iptal nedeni = %q, %v", reason, err)
	}
}

func TestRefreshRejects(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok := e.signIn(t, "sub-1")
	for name, rt := range map[string]string{
		"boş":            "",
		"kısa":           "abc",
		"bilinmeyen":     base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		"dolgulu base64": base64.URLEncoding.EncodeToString(make([]byte, 32)),
	} {
		if _, err := e.svc.Refresh(ctx, rt); !errors.Is(err, ErrInvalidRefresh) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// 90 gün kullanılmayan oturum biter.
	*e.clock = e.now.Add(sessionTTL + time.Second)
	if _, err := e.svc.Refresh(ctx, tok.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("süresi dolan oturum: %v", err)
	}
}

func TestSignOutEndsSession(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok := e.signIn(t, "sub-1")
	if err := e.svc.SignOut(ctx, tok.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SignOut(ctx, tok.SessionID); err != nil {
		t.Fatalf("ikinci çıkış hata verdi: %v", err)
	}
	if _, err := e.svc.Refresh(ctx, tok.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("çıkıştan sonra yenileme: %v", err)
	}
}

func TestConcurrentFirstSignInCreatesOneUser(t *testing.T) {
	e := setup(t)
	var wg sync.WaitGroup
	ids := make([]id.ID, 8)
	errs := make([]error, 8)
	for i := range ids {
		wg.Go(func() {
			tok, err := e.svc.SignIn(context.Background(), SignInInput{"google", "ok:yaris", "n", "web"})
			ids[i], errs[i] = tok.UserID, err
		})
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("istek %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatal("aynı Google hesabı için birden fazla kullanıcı oluştu")
		}
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&n)
	if n != 1 {
		t.Fatalf("kullanıcı sayısı = %d", n)
	}
}
