package deletion

import (
	"bytes"
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
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/consent"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/oidc"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/refcode"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/session"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeGoogle struct{}

func (fakeGoogle) Verify(_ context.Context, tok, nonce string) (oidc.Identity, error) {
	if len(tok) > 3 && tok[:3] == "ok:" && nonce == "n" {
		return oidc.Identity{Provider: "google", Subject: tok[3:], Email: "ornek@example.com", Name: "Ayşe"}, nil
	}
	return oidc.Identity{}, oidc.ErrInvalidToken
}

var docs = consent.Documents{PrivacyNotice: "2026-11-08", Terms: "2026-11-08"}

type env struct {
	pool     *pgxpool.Pool
	sessions *session.Service
	svc      *Service
	verifier *auth.Verifier
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, "identity", identitydb.Migrations())
	seed := make([]byte, ed25519.SeedSize)
	copy(seed, "deletion-test")
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	signer := func(aud string) *auth.Signer {
		s, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-identity", Audience: aud, TTL: 15 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := session.New(session.Config{
		Pool: pool, Log: log,
		Verifiers: map[string]oidc.Verifier{"google": fakeGoogle{}},
		API:       signer(session.AudienceAPI), Registration: signer(session.AudienceRegistration),
		Documents: docs,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{Pool: pool, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: session.AudienceAPI})
	if err != nil {
		t.Fatal(err)
	}
	return &env{pool: pool, sessions: sessions, svc: svc, verifier: v}
}

// signIn signs subject in and, the first time, registers the account.
func (e *env) signIn(t *testing.T, subject string) session.Tokens {
	t.Helper()
	ctx := context.Background()
	in := session.SignInInput{Provider: "google", IDToken: "ok:" + subject, Nonce: "n", Platform: "android"}
	tok, err := e.sessions.SignIn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !tok.RegistrationPending {
		return tok
	}
	q := store.New(e.pool)
	if _, err := q.CompleteRegistration(ctx, store.CompleteRegistrationParams{ID: tok.UserID.PG(), BirthDateEnc: bytes.Repeat([]byte{1}, 40)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET ref_code = $2 WHERE id = $1`, tok.UserID.PG(), refcode.New()); err != nil {
		t.Fatal(err)
	}
	if err := consent.Record(ctx, q, tok.UserID, docs); err != nil {
		t.Fatal(err)
	}
	if tok, err = e.sessions.SignIn(ctx, in); err != nil {
		t.Fatal(err)
	}
	return tok
}

// caller is what the gRPC layer passes after verifying the access token.
func (e *env) caller(t *testing.T, tok session.Tokens) Caller {
	t.Helper()
	c, err := e.verifier.Verify(context.Background(), tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return Caller{User: tok.UserID, Session: tok.SessionID, IssuedAt: c.IssuedAt}
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeleteAccount(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok := e.signIn(t, "sub-a")
	other := e.signIn(t, "sub-a") // second device of the same user
	bystander := e.signIn(t, "sub-b")
	c := e.caller(t, tok)
	// A display name change (issue #184) is counted until the account goes.
	if _, err := e.pool.Exec(ctx, `INSERT INTO display_name_changes (id, user_id) VALUES ($1, $2)`, id.New().PG(), tok.UserID.PG()); err != nil {
		t.Fatal(err)
	}
	// So are the profile completion fields (issue #186).
	if _, err := e.pool.Exec(ctx, `INSERT INTO profile_details (user_id, team) VALUES ($1, 'Bursaspor')`, tok.UserID.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO user_social_accounts (user_id, platform, username) VALUES ($1, 'x', 'ece')`, tok.UserID.PG()); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.Delete(ctx, c); err != nil {
		t.Fatal(err)
	}

	u := tok.UserID.PG()
	if n := e.count(t, `SELECT count(*) FROM users WHERE id = $1 AND deleted_at IS NOT NULL
		AND display_name IS NULL AND avatar_upload_id IS NULL AND birth_date_enc IS NULL
		AND ref_code IS NULL AND referred_by_user_id IS NULL`, u); n != 1 {
		t.Fatal("kişisel veriler temizlenmedi")
	}
	for name, sql := range map[string]string{
		"giriş kimliği":  `SELECT count(*) FROM auth_identities WHERE user_id = $1`,
		"cihaz":          `SELECT count(*) FROM devices WHERE user_id = $1`,
		"oturum":         `SELECT count(*) FROM sessions WHERE user_id = $1`,
		"refresh token":  `SELECT count(*) FROM refresh_tokens rt JOIN sessions s ON s.id = rt.session_id WHERE s.user_id = $1`,
		"ad değişikliği": `SELECT count(*) FROM display_name_changes WHERE user_id = $1`,
		"profil alanı":   `SELECT count(*) FROM profile_details WHERE user_id = $1`,
		"sosyal hesap":   `SELECT count(*) FROM user_social_accounts WHERE user_id = $1`,
	} {
		if n := e.count(t, sql, u); n != 0 {
			t.Errorf("%s kaldı: %d", name, n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'user.deleted' AND subject_id = $1 AND actor_id IS NULL`, u); n != 1 {
		t.Fatalf("user.deleted olayı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM consent_records WHERE user_id = $1`, u); n != 2 {
		t.Fatalf("onay kayıtları = %d; hukukçu kararına kadar kalmalı", n)
	}

	// Every session of the account ended: no refresh, on any device.
	for _, rt := range []string{tok.RefreshToken, other.RefreshToken} {
		if _, err := e.sessions.Refresh(ctx, rt); !errors.Is(err, session.ErrInvalidRefresh) {
			t.Fatalf("silinen hesabın oturumu yenilendi: %v", err)
		}
	}
	// The access token is still signed and unexpired (ADR-0004: up to 15
	// minutes), but identity refuses it: its session and account are gone.
	if err := e.svc.Delete(ctx, c); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("aynı token'la ikinci silme: %v", err)
	}
	if _, err := e.sessions.List(ctx, session.Caller{User: c.User, Session: c.Session}); !errors.Is(err, session.ErrCallerSession) {
		t.Fatalf("aynı token'la oturum listesi: %v", err)
	}

	// Another user is untouched.
	if _, err := e.sessions.Refresh(ctx, bystander.RefreshToken); err != nil {
		t.Fatalf("başka kullanıcının oturumu etkilendi: %v", err)
	}

	// The same Google account opens a new, unregistered account.
	again, err := e.sessions.SignIn(ctx, session.SignInInput{Provider: "google", IDToken: "ok:sub-a", Nonce: "n", Platform: "ios"})
	if err != nil {
		t.Fatal(err)
	}
	if again.UserID == tok.UserID || !again.RegistrationPending {
		t.Fatalf("yeniden giriş: user=%v pending=%v", again.UserID, again.RegistrationPending)
	}
}

// TestDeleteNeedsRecentSignIn fixes the reauthentication rule: both the
// access token and its session (the provider sign-in) must be at most
// ReauthWindow old.
func TestDeleteNeedsRecentSignIn(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		tokenAge   time.Duration
		sessionAge time.Duration
		want       error
	}{
		"yeni giriş":                   {0, 0, nil},
		"sınırın içinde":               {ReauthWindow - time.Minute, ReauthWindow - time.Minute, nil},
		"eski erişim token'ı":          {ReauthWindow + time.Second, 0, ErrReauthenticationRequired},
		"yenilenmiş token, eski giriş": {0, ReauthWindow + time.Minute, ErrReauthenticationRequired},
		"eski token ve eski giriş":     {time.Hour, time.Hour, ErrReauthenticationRequired},
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			tok := e.signIn(t, "sub-a")
			c := e.caller(t, tok)
			c.IssuedAt = time.Now().Add(-tc.tokenAge)
			if _, err := e.pool.Exec(ctx, `UPDATE sessions SET created_at = now() - make_interval(secs => $2) WHERE id = $1`,
				tok.SessionID.PG(), tc.sessionAge.Seconds()); err != nil {
				t.Fatal(err)
			}
			err := e.svc.Delete(ctx, c)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, %v bekleniyordu", err, tc.want)
			}
			deleted := e.count(t, `SELECT count(*) FROM users WHERE id = $1 AND deleted_at IS NOT NULL`, tok.UserID.PG())
			events := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'user.deleted'`)
			sessions := e.count(t, `SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`, tok.UserID.PG())
			if tc.want != nil && (deleted != 0 || events != 0 || sessions == 0) {
				t.Fatalf("reddedilen silme iz bıraktı: silindi=%d olay=%d oturum=%d", deleted, events, sessions)
			}
			if tc.want == nil && (deleted != 1 || events != 1 || sessions != 0) {
				t.Fatalf("silme eksik: silindi=%d olay=%d oturum=%d", deleted, events, sessions)
			}
		})
	}
}

func TestDeleteRefusesEndedSession(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok := e.signIn(t, "sub-a")
	c := e.caller(t, tok)
	if err := e.sessions.SignOut(ctx, tok.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, c); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("kapalı oturumla silme: %v", err)
	}
	// Another user's session ID with one's own user ID is refused as well.
	other := e.signIn(t, "sub-b")
	c2 := e.caller(t, e.signIn(t, "sub-c"))
	c2.Session = other.SessionID
	if err := e.svc.Delete(ctx, c2); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("başkasının oturumuyla silme: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM users WHERE deleted_at IS NOT NULL`); n != 0 {
		t.Fatalf("silinen hesap = %d", n)
	}
}

func TestConcurrentDeleteWritesOneEvent(t *testing.T) {
	e := setup(t)
	tok := e.signIn(t, "sub-a")
	c := e.caller(t, tok)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { errs[i] = e.svc.Delete(context.Background(), c) })
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrUnknownUser):
			t.Fatalf("beklenmeyen hata: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("başarılı silme = %d", ok)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'user.deleted' AND subject_id = $1`, tok.UserID.PG()); n != 1 {
		t.Fatalf("user.deleted = %d", n)
	}
}
