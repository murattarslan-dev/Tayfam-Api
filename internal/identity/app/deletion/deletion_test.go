package deletion

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/appleauth"
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

// fakeApple is fakeGoogle for Apple sign-in (no name, ADR-0045 §2).
type fakeApple struct{}

func (fakeApple) Verify(_ context.Context, tok, nonce string) (oidc.Identity, error) {
	if len(tok) > 3 && tok[:3] == "ok:" && nonce == "n" {
		return oidc.Identity{Provider: "apple", Subject: tok[3:]}, nil
	}
	return oidc.Identity{}, oidc.ErrInvalidToken
}

// fakeRevoker records what deletion asks Apple to revoke and answers err.
type fakeRevoker struct {
	mu    sync.Mutex
	err   error
	calls [][2]string // code, subject
}

func (f *fakeRevoker) RevokeByCode(_ context.Context, code, subject string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [2]string{code, subject})
	return f.err
}

func (f *fakeRevoker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

var docs = consent.Documents{PrivacyNotice: "2026-11-08", Terms: "2026-11-08"}

type env struct {
	pool     *pgxpool.Pool
	sessions *session.Service
	svc      *Service
	verifier *auth.Verifier
	apple    *fakeRevoker
	log      *slog.Logger
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
		Verifiers: map[string]oidc.Verifier{"google": fakeGoogle{}, "apple": fakeApple{}},
		API:       signer(session.AudienceAPI), Registration: signer(session.AudienceRegistration),
		Documents: docs,
	})
	if err != nil {
		t.Fatal(err)
	}
	apple := &fakeRevoker{}
	svc, err := New(Config{Pool: pool, Log: log, Apple: apple})
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: session.AudienceAPI})
	if err != nil {
		t.Fatal(err)
	}
	return &env{pool: pool, sessions: sessions, svc: svc, verifier: v, apple: apple, log: log}
}

// signIn signs subject in with Google and, the first time, registers the
// account.
func (e *env) signIn(t *testing.T, subject string) session.Tokens {
	t.Helper()
	return e.signInWith(t, "google", "android", subject)
}

// signInApple is signIn with Apple, from the iOS app (ADR-0045 §1).
func (e *env) signInApple(t *testing.T, subject string) session.Tokens {
	t.Helper()
	return e.signInWith(t, "apple", "ios", subject)
}

func (e *env) signInWith(t *testing.T, provider, platform, subject string) session.Tokens {
	t.Helper()
	ctx := context.Background()
	in := session.SignInInput{Provider: provider, IDToken: "ok:" + subject, Nonce: "n", Platform: platform}
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

// appleCaller is caller with the authorization code of the Apple sign-in.
func (e *env) appleCaller(t *testing.T, tok session.Tokens, code string) Caller {
	t.Helper()
	c := e.caller(t, tok)
	c.AppleCode = code
	return c
}

// TestDeleteAppleAccountRevokesFirst: ADR-0045 §4. An Apple account is
// deleted only after its Apple tokens are revoked, with the code of the
// re-authentication and for the account's own Apple user.
func TestDeleteAppleAccountRevokesFirst(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok := e.signInApple(t, "apple-sub-1")

	if err := e.svc.Delete(ctx, e.appleCaller(t, tok, "kod-1")); err != nil {
		t.Fatal(err)
	}
	if len(e.apple.calls) != 1 || e.apple.calls[0] != [2]string{"kod-1", "apple-sub-1"} {
		t.Fatalf("iptal çağrıları = %v", e.apple.calls)
	}
	if n := e.count(t, `SELECT count(*) FROM auth_identities WHERE user_id = $1`, tok.UserID.PG()); n != 0 {
		t.Fatalf("giriş kimliği kaldı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM users WHERE id = $1 AND deleted_at IS NOT NULL`, tok.UserID.PG()); n != 1 {
		t.Fatal("hesap silinmedi")
	}
}

// TestDeleteAppleAccountNotDeletedWithoutRevocation: when the tokens cannot
// be revoked nothing is deleted, whatever the reason.
func TestDeleteAppleAccountNotDeletedWithoutRevocation(t *testing.T) {
	for name, tc := range map[string]struct {
		code      string
		revokeErr error
		want      error
		wantCalls int
	}{
		"kod yok":              {"", nil, ErrReauthenticationRequired, 0},
		"kod reddedildi":       {"kod-1", fmt.Errorf("%w: invalid_grant", appleauth.ErrCodeRejected), ErrReauthenticationRequired, 1},
		"apple'a ulaşılamıyor": {"kod-1", fmt.Errorf("%w: token ucu HTTP 503", appleauth.ErrUnavailable), ErrProviderUnavailable, 1},
		"beklenmeyen hata":     {"kod-1", errors.New("bilinmeyen"), ErrProviderUnavailable, 1},
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			tok := e.signInApple(t, "apple-sub-1")
			e.apple.err = tc.revokeErr

			err := e.svc.Delete(ctx, e.appleCaller(t, tok, tc.code))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, %v bekleniyordu", err, tc.want)
			}
			if e.apple.count() != tc.wantCalls {
				t.Fatalf("iptal çağrıları = %d", e.apple.count())
			}
			if n := e.count(t, `SELECT count(*) FROM auth_identities WHERE user_id = $1`, tok.UserID.PG()); n != 1 {
				t.Fatal("iptal olmadan giriş kimliği silindi")
			}
			if n := e.count(t, `SELECT count(*) FROM users WHERE id = $1 AND deleted_at IS NULL`, tok.UserID.PG()); n != 1 {
				t.Fatal("iptal olmadan hesap silindi")
			}
			// The account is intact: with a good code the same request
			// goes through.
			e.apple.err = nil
			if err := e.svc.Delete(ctx, e.appleCaller(t, tok, "kod-2")); err != nil {
				t.Fatalf("yeniden deneme: %v", err)
			}
		})
	}
}

// TestDeleteAppleAccountStaleSessionNeverReachesApple: the re-authentication
// rule comes first; Apple is not asked for a session that could not delete
// the account anyway.
func TestDeleteAppleAccountStaleSessionNeverReachesApple(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok := e.signInApple(t, "apple-sub-1")
	if _, err := e.pool.Exec(ctx, `UPDATE sessions SET created_at = now() - interval '11 minutes' WHERE id = $1`, tok.SessionID.PG()); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, e.appleCaller(t, tok, "kod-1")); !errors.Is(err, ErrReauthenticationRequired) {
		t.Fatalf("err = %v", err)
	}
	if e.apple.count() != 0 {
		t.Fatal("eski oturumla Apple'a gidildi")
	}
}

// TestDeleteGoogleAccountIgnoresAppleCode: a Google account needs no code
// and a code sent with it goes nowhere.
func TestDeleteGoogleAccountIgnoresAppleCode(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok := e.signIn(t, "google-sub-1")
	e.apple.err = errors.New("çağrılmamalı")
	if err := e.svc.Delete(ctx, e.appleCaller(t, tok, "kod-1")); err != nil {
		t.Fatal(err)
	}
	if e.apple.count() != 0 {
		t.Fatal("Google hesabı için Apple'a gidildi")
	}
}

// TestDeleteAppleAccountWithoutRevoker: Apple sign-in is never on without
// revocation (cmd/identity), but if an Apple account exists while it is off
// the account is not deleted behind Apple's back.
func TestDeleteAppleAccountWithoutRevoker(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok := e.signInApple(t, "apple-sub-1")
	svc, err := New(Config{Pool: e.pool, Log: e.log})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, e.appleCaller(t, tok, "kod-1")); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM users WHERE id = $1 AND deleted_at IS NULL`, tok.UserID.PG()); n != 1 {
		t.Fatal("hesap silindi")
	}
}
