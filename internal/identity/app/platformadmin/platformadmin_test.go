package platformadmin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/oidc"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

const (
	adminEmail = "yonetici@example.com"
	nonce      = "nonce-0123456789abcdef"
)

// fakeGoogle accepts "ok:<subject>" with the test nonce; the ID token was
// issued at *issued with the verified address email.
type fakeGoogle struct {
	issued *time.Time
	email  string
}

func (f fakeGoogle) Verify(_ context.Context, tok, n string) (oidc.Identity, error) {
	if tok == "down" {
		return oidc.Identity{}, errors.New("google erişilemez")
	}
	if !strings.HasPrefix(tok, "ok:") || n != nonce {
		return oidc.Identity{}, oidc.ErrInvalidToken
	}
	return oidc.Identity{Provider: "google", Subject: tok[3:], Email: f.email, Name: "Yönetici", IssuedAt: *f.issued}, nil
}

type env struct {
	svc  *Service
	pool *pgxpool.Pool
	// owner is a superuser pool for what only the platform owner does
	// (adding and removing administrators) and for moving time in fixtures.
	owner    *pgxpool.Pool
	q        *store.Queries
	now      time.Time
	issued   time.Time
	verifier *auth.AdminVerifier
	logs     *bytes.Buffer
	admin    id.ID
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "identity", identitydb.Migrations())
	pool := d.App
	seed := make([]byte, ed25519.SeedSize)
	copy(seed, "platform-admin-test")
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, owner: d.Admin, q: store.New(pool), now: time.Now().UTC().Truncate(time.Second), logs: &bytes.Buffer{}}
	e.issued = e.now.Add(-30 * time.Second)
	clock := func() time.Time { return e.now }
	signer, err := auth.NewAdminSigner(auth.AdminSignerConfig{Key: key, Issuer: "tayfam-identity", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	e.verifier, err = auth.NewAdminVerifier(auth.AdminVerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	e.svc, err = New(Config{
		Pool: pool, Log: slog.New(slog.NewJSONHandler(e.logs, nil)),
		Google: fakeGoogle{issued: &e.issued, email: "Yonetici@Example.com"},
		Signer: signer, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.admin = e.user(t, "g-admin")
	if _, err := e.owner.Exec(context.Background(), `INSERT INTO platform_admins (user_id) VALUES ($1)`, e.admin.PG()); err != nil {
		t.Fatal(err)
	}
	e.user(t, "g-plain")
	return e
}

// user creates a registered user with a Google identity.
func (e *env) user(t *testing.T, subject string) id.ID {
	t.Helper()
	ctx := context.Background()
	u := id.New()
	if err := e.q.CreateUser(ctx, store.CreateUserParams{ID: u.PG(), DisplayName: pgtype.Text{String: "Yönetici", Valid: true}, Locale: "tr"}); err != nil {
		t.Fatal(err)
	}
	if n, err := e.q.CompleteRegistration(ctx, store.CompleteRegistrationParams{ID: u.PG(), BirthDateEnc: bytes.Repeat([]byte{1}, 40)}); err != nil || n != 1 {
		t.Fatalf("kayıt: %d %v", n, err)
	}
	if err := e.q.CreateAuthIdentity(ctx, store.CreateAuthIdentityParams{ID: id.New().PG(), UserID: u.PG(), Provider: "google", Subject: subject}); err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *env) auditCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform_audit_logs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSignIn(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok, err := e.svc.SignIn(ctx, SignInInput{IDToken: "ok:g-admin", Nonce: nonce, AccessEmail: adminEmail})
	if err != nil {
		t.Fatal(err)
	}
	if tok.UserID != e.admin || !tok.ExpiresAt.Equal(e.now.Add(8*time.Hour)) || !tok.AuthTime.Equal(e.issued) {
		t.Fatalf("token = %+v", tok)
	}
	c, err := e.verifier.Verify(ctx, tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != e.admin.String() || c.SessionID != tok.SessionID.String() || !c.AuthTime.Equal(e.issued) {
		t.Fatalf("claims = %+v", c)
	}
	a, err := e.svc.Get(ctx, c)
	if err != nil || a.UserID != e.admin || a.SessionID != tok.SessionID {
		t.Fatalf("Get = %+v, %v", a, err)
	}

	var action, reason, meta string
	if err := e.pool.QueryRow(ctx, `SELECT action, reason, metadata::text FROM platform_audit_logs WHERE actor_id = $1`, e.admin.PG()).Scan(&action, &reason, &meta); err != nil {
		t.Fatal(err)
	}
	if action != ActionSignIn || reason == "" || !strings.Contains(meta, tok.SessionID.String()) {
		t.Fatalf("denetim kaydı = %s %q %s", action, reason, meta)
	}
	if strings.Contains(e.logs.String(), "example.com") || strings.Contains(e.logs.String(), tok.Token) {
		t.Fatal("log'a e-posta ya da token yazıldı")
	}
}

func TestSignInRefusals(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	valid := SignInInput{IDToken: "ok:g-admin", Nonce: nonce, AccessEmail: adminEmail}
	for name, tc := range map[string]struct {
		in     func(SignInInput) SignInInput
		issued time.Duration // ID token age
	}{
		"yönetici değil":          {in: func(s SignInInput) SignInInput { s.IDToken = "ok:g-plain"; return s }},
		"bilinmeyen hesap":        {in: func(s SignInInput) SignInInput { s.IDToken = "ok:g-yok"; return s }},
		"geçersiz kimlik token'ı": {in: func(s SignInInput) SignInInput { s.IDToken = "bozuk"; return s }},
		"yanlış nonce":            {in: func(s SignInInput) SignInInput { s.Nonce = "baska"; return s }},
		"başka Access kimliği":    {in: func(s SignInInput) SignInInput { s.AccessEmail = "baska@example.com"; return s }},
		"Access kimliği yok":      {in: func(s SignInInput) SignInInput { s.AccessEmail = ""; return s }},
		"eski kimlik token'ı":     {in: func(s SignInInput) SignInInput { return s }, issued: 6 * time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			e.issued = e.now.Add(-30*time.Second - tc.issued)
			_, err := e.svc.SignIn(ctx, tc.in(valid))
			if !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("hata = %v", err)
			}
		})
	}
	if n := e.auditCount(t); n != 0 {
		t.Fatalf("reddedilen girişler denetim kaydına yazıldı: %d", n)
	}

	e.issued = e.now
	if _, err := e.svc.SignIn(ctx, SignInInput{IDToken: "down", Nonce: nonce, AccessEmail: adminEmail}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("sağlayıcı erişilemezken: %v", err)
	}
}

// TestRemovedAdminIsShutOut: removing the platform_admins row (or deleting
// the account) ends every open panel session at its next request, though
// the token is valid for 8 hours.
func TestRemovedAdminIsShutOut(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok, err := e.svc.SignIn(ctx, SignInInput{IDToken: "ok:g-admin", Nonce: nonce, AccessEmail: adminEmail})
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.verifier.Verify(ctx, tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(ctx, `DELETE FROM platform_admins WHERE user_id = $1`, e.admin.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, c); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("Get = %v", err)
	}
	if _, err := e.svc.SignIn(ctx, SignInInput{IDToken: "ok:g-admin", Nonce: nonce, AccessEmail: adminEmail}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("SignIn = %v", err)
	}
}

func TestDestructive(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok, err := e.svc.SignIn(ctx, SignInInput{IDToken: "ok:g-admin", Nonce: nonce, AccessEmail: adminEmail})
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.verifier.Verify(ctx, tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	target := id.New()
	entry := Entry{Action: "user.ban", TargetType: "user", TargetID: target, Reason: "Tekrarlanan taciz şikâyetleri"}
	ran := 0
	do := Action{Do: func(context.Context, *store.Queries, *Entry) error { ran++; return nil }}
	before := e.auditCount(t)

	// No reason, or a too short one: refused before anything runs.
	for _, r := range []string{"", "   ", "kısa"} {
		bad := entry
		bad.Reason = r
		if err := e.svc.Destructive(ctx, c, bad, do); !errors.Is(err, ErrReasonRequired) {
			t.Fatalf("gerekçe %q: %v", r, err)
		}
	}
	// The action fails: nothing is written.
	if err := e.svc.Destructive(ctx, c, entry, Action{Do: func(context.Context, *store.Queries, *Entry) error { return errors.New("olmadı") }}); err == nil {
		t.Fatal("başarısız işlem hata vermedi")
	}
	if err := e.svc.Destructive(ctx, c, entry, do); err != nil {
		t.Fatal(err)
	}
	if ran != 1 || e.auditCount(t) != before+1 {
		t.Fatalf("çalıştı=%d kayıt=%d", ran, e.auditCount(t)-before)
	}

	// More than 5 minutes after the provider sign-in: sign in again.
	e.now = e.now.Add(auth.AdminReauthWindow + time.Second)
	if err := e.svc.Destructive(ctx, c, entry, do); !errors.Is(err, ErrReauthenticationRequired) {
		t.Fatalf("eski giriş: %v", err)
	}
	if ran != 1 {
		t.Fatal("yeniden giriş istenen işlem çalıştı")
	}
}

func TestRecordValidates(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	target := id.New()
	for name, en := range map[string]Entry{
		"işlem biçimi":      {Action: "BAN", Reason: "yeterince uzun gerekçe"},
		"gerekçe yok":       {Action: "user.ban", Reason: ""},
		"hedef türü yok":    {Action: "user.ban", TargetID: target, Reason: "yeterince uzun gerekçe"},
		"hedef kimliği yok": {Action: "user.ban", TargetType: "user", Reason: "yeterince uzun gerekçe"},
		"bilinmeyen hedef":  {Action: "user.ban", TargetType: "dm", TargetID: target, Reason: "yeterince uzun gerekçe"},
		"büyük metadata":    {Action: "user.ban", Reason: "yeterince uzun gerekçe", Metadata: map[string]string{"x": strings.Repeat("a", 5000)}},
	} {
		if err := Record(ctx, e.q, e.admin, en); !errors.Is(err, ErrInvalidAudit) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := Record(ctx, e.q, e.admin, Entry{Action: "tayfa.suspend", TargetType: "tayfa", TargetID: target, Reason: "Kurallara aykırı içerik"}); err != nil {
		t.Fatal(err)
	}
}

func TestNewRequiresConfig(t *testing.T) {
	if _, err := New(Config{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}); err == nil {
		t.Fatal("eksik yapılandırma kabul edildi")
	}
}
