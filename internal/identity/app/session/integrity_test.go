package session

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/integrity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/integrity/integritytest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// setupIntegrity is setup with integrity checks in report mode against a
// fake Google.
func setupIntegrity(t *testing.T) (*env, *integritytest.Fake) {
	t.Helper()
	e := setup(t)
	f, p := integritytest.New(t, func() time.Time { return *e.clock })
	c, err := integrity.NewChecker(integrity.Config{Mode: integrity.ModeReport, Play: p, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.Integrity = c
	if e.svc, err = New(cfg); err != nil {
		t.Fatal(err)
	}
	return e, f
}

type storedIntegrity struct {
	status   string
	reason   pgtype.Text
	checked  pgtype.Timestamptz
	passedAt pgtype.Timestamptz
}

func (e *env) integrityOf(t *testing.T, session id.ID) storedIntegrity {
	t.Helper()
	var s storedIntegrity
	if err := e.pool.QueryRow(context.Background(),
		`SELECT integrity_status, integrity_reason, integrity_checked_at, integrity_passed_at FROM sessions WHERE id = $1`,
		session.PG()).Scan(&s.status, &s.reason, &s.checked, &s.passedAt); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *env) signInWith(t *testing.T, subject string, proof integrity.Proof) Tokens {
	t.Helper()
	tok, err := e.svc.SignIn(context.Background(), SignInInput{Provider: "google", IDToken: "ok:" + subject, Nonce: "n", Platform: "android", Integrity: proof})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestIntegrityLifecycle walks one session through ADR-0022 §1 and §5: a
// pass at sign-in, refreshes that need no proof for 24 hours, a stale
// session without a proof, a pass again, and Google's outage.
func TestIntegrityLifecycle(t *testing.T) {
	e, f := setupIntegrity(t)
	ctx := context.Background()

	tok := e.signInWith(t, "sub-1", integrity.Proof{Android: f.Token(integritytest.Verdict{Binding: integrity.SignInBinding("n")})})
	if !tok.IntegrityPassed || !tok.IntegrityCheckAfter.Equal(e.now.Add(integrity.Recheck)) {
		t.Fatalf("giriş: geçti=%v sonraki=%v", tok.IntegrityPassed, tok.IntegrityCheckAfter)
	}
	if s := e.integrityOf(t, tok.SessionID); s.status != "passed" || s.reason.Valid || !s.passedAt.Valid {
		t.Fatalf("giriş kaydı = %+v", s)
	}

	// Within 24 hours a refresh carries no proof and Google is not asked.
	e.now = e.now.Add(time.Hour)
	decodes := f.Decodes()
	tok, err := e.svc.Refresh(ctx, tok.RefreshToken)
	if err != nil || !tok.IntegrityPassed || f.Decodes() != decodes {
		t.Fatalf("erken yenileme: %+v %v (%d istek)", tok.IntegrityPassed, err, f.Decodes()-decodes)
	}

	// After 24 hours a refresh without a proof makes the session stale.
	e.now = e.now.Add(24 * time.Hour)
	tok, err = e.svc.Refresh(ctx, tok.RefreshToken)
	if err != nil || tok.IntegrityPassed || !tok.IntegrityCheckAfter.Equal(e.now.Add(integrity.RetryFailed)) {
		t.Fatalf("kanıtsız yenileme: %v %v %v", tok.IntegrityPassed, tok.IntegrityCheckAfter, err)
	}
	if s := e.integrityOf(t, tok.SessionID); s.status != "failed" || s.reason.String != "stale" {
		t.Fatalf("bayat kayıt = %+v", s)
	}

	// A proof bound to another refresh token does not count.
	e.now = e.now.Add(2 * time.Hour)
	wrong := integrity.Proof{Android: f.Token(integritytest.Verdict{Binding: integrity.RefreshBinding("başka-token")})}
	tok, err = e.svc.RefreshWithProof(ctx, tok.RefreshToken, wrong)
	if err != nil || tok.IntegrityPassed {
		t.Fatalf("yanlış bağlama: %v %v", tok.IntegrityPassed, err)
	}
	if s := e.integrityOf(t, tok.SessionID); s.status != "failed" || s.reason.String != "binding" {
		t.Fatalf("yanlış bağlama kaydı = %+v", s)
	}

	// The right proof passes again.
	e.now = e.now.Add(2 * time.Hour)
	good := integrity.Proof{Android: f.Token(integritytest.Verdict{Binding: integrity.RefreshBinding(tok.RefreshToken)})}
	tok, err = e.svc.RefreshWithProof(ctx, tok.RefreshToken, good)
	if err != nil || !tok.IntegrityPassed {
		t.Fatalf("doğru kanıt: %v %v", tok.IntegrityPassed, err)
	}

	// Google is down a day later: the recent pass stands.
	e.now = e.now.Add(25 * time.Hour)
	f.Down(true)
	proof := integrity.Proof{Android: f.Token(integritytest.Verdict{Binding: integrity.RefreshBinding(tok.RefreshToken)})}
	tok, err = e.svc.RefreshWithProof(ctx, tok.RefreshToken, proof)
	if err != nil || !tok.IntegrityPassed {
		t.Fatalf("Google kapalı: %v %v", tok.IntegrityPassed, err)
	}
	if s := e.integrityOf(t, tok.SessionID); s.status != "passed" || s.checked.Time.Sub(e.now).Abs() > time.Millisecond {
		t.Fatalf("Google kapalı kaydı = %+v", s)
	}
}

// TestIntegrityReportNeverRefuses: in report mode a session without a proof
// or with a failing one still signs in and refreshes.
func TestIntegrityReportNeverRefuses(t *testing.T) {
	e, f := setupIntegrity(t)
	tok := e.signInWith(t, "sub-2", integrity.Proof{})
	if tok.AccessToken == "" || tok.IntegrityPassed || !tok.IntegrityCheckAfter.Equal(e.now) {
		t.Fatalf("kanıtsız giriş: %+v", tok)
	}
	if s := e.integrityOf(t, tok.SessionID); s.status != "unverified" || s.reason.String != "missing" || !s.checked.Valid {
		t.Fatalf("kanıtsız kayıt = %+v", s)
	}
	weak := integrity.Proof{Android: f.Token(integritytest.Verdict{Binding: integrity.SignInBinding("n"), Labels: []string{integrity.LabelBasic}})}
	tok = e.signInWith(t, "sub-2", weak)
	if tok.AccessToken == "" || tok.IntegrityPassed {
		t.Fatalf("zayıf cihaz girişi: %+v", tok)
	}
	if s := e.integrityOf(t, tok.SessionID); s.status != "failed" || s.reason.String != "device" {
		t.Fatalf("zayıf cihaz kaydı = %+v", s)
	}
	if _, err := e.svc.Refresh(context.Background(), tok.RefreshToken); err != nil {
		t.Fatalf("yenileme reddedildi: %v", err)
	}
}

// TestIntegrityOffTouchesNothing: without a checker nothing is written and
// no next check time is given.
func TestIntegrityOffTouchesNothing(t *testing.T) {
	e := setup(t)
	tok := e.signIn(t, "sub-3")
	if tok.IntegrityPassed || !tok.IntegrityCheckAfter.IsZero() {
		t.Fatalf("kapalıyken: %+v", tok)
	}
	if s := e.integrityOf(t, tok.SessionID); s.status != "unverified" || s.reason.Valid || s.checked.Valid {
		t.Fatalf("kapalıyken kayıt = %+v", s)
	}
}
