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
	// A checked session that did not pass is asked again after RetryFailed.
	if tok.AccessToken == "" || tok.IntegrityPassed || !tok.IntegrityCheckAfter.Equal(e.now.Add(integrity.RetryFailed)) {
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

// setupAppAttest is setupIntegrity with App Attest against a fake Apple.
func setupAppAttest(t *testing.T) (*env, *integritytest.Apple) {
	t.Helper()
	e := setup(t)
	clock := func() time.Time { return *e.clock }
	_, p := integritytest.New(t, clock)
	apple, aa := integritytest.NewApple(t, clock)
	c, err := integrity.NewChecker(integrity.Config{Mode: integrity.ModeReport, Play: p, AppAttest: aa, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.Integrity = c
	if e.svc, err = New(cfg); err != nil {
		t.Fatal(err)
	}
	return e, apple
}

func (e *env) signInIOS(t *testing.T, subject string, proof integrity.Proof) Tokens {
	t.Helper()
	tok, err := e.svc.SignIn(context.Background(), SignInInput{Provider: "google", IDToken: "ok:" + subject, Nonce: "n", Platform: "ios", Integrity: proof})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// deviceKey is the App Attest key of a session's device; counter is -1
// without one.
func (e *env) deviceKey(t *testing.T, session id.ID) ([]byte, int64) {
	t.Helper()
	var (
		key     []byte
		counter pgtype.Int8
	)
	if err := e.pool.QueryRow(context.Background(),
		`SELECT d.app_attest_key_id, d.app_attest_counter FROM sessions s JOIN devices d ON d.id = s.device_id WHERE s.id = $1`,
		session.PG()).Scan(&key, &counter); err != nil {
		t.Fatal(err)
	}
	if !counter.Valid {
		return key, -1
	}
	return key, counter.Int64
}

// TestIntegrityAppAttest walks an iOS session through ADR-0022 §4: the key
// attested at sign-in is bound to the device, a refresh proves itself with
// an assertion and its counter is kept; a replay and a key bound before do
// not pass. Report mode refuses nothing.
func TestIntegrityAppAttest(t *testing.T) {
	e, apple := setupAppAttest(t)
	ctx := context.Background()
	dev := apple.NewDevice()
	attest := func(d *integritytest.Device, b string) integrity.Proof {
		return integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: d.KeyID, Attestation: d.Attest(b)}}
	}
	assertion := func(as []byte) integrity.Proof {
		return integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: dev.KeyID, Assertion: as}}
	}

	tok := e.signInIOS(t, "sub-ios", attest(dev, integrity.SignInBinding("n")))
	if !tok.IntegrityPassed {
		t.Fatalf("giriş: %+v", tok)
	}
	if key, counter := e.deviceKey(t, tok.SessionID); string(key) != string(dev.KeyID) || counter != 0 {
		t.Fatalf("cihaz anahtarı: %x %d", key, counter)
	}

	// The same key on another sign-in, even of the same account, is not
	// bound twice.
	again := e.signInIOS(t, "sub-ios", attest(dev, integrity.SignInBinding("n")))
	if again.IntegrityPassed {
		t.Fatal("aynı anahtar ikinci kez geçti")
	}
	if s := e.integrityOf(t, again.SessionID); s.status != "failed" || s.reason.String != "binding" {
		t.Fatalf("ikinci bağlama kaydı = %+v", s)
	}
	if key, _ := e.deviceKey(t, again.SessionID); key != nil {
		t.Fatal("anahtar ikinci cihaza bağlandı")
	}

	// After 24 hours a refresh carries an assertion bound to it.
	e.now = e.now.Add(25 * time.Hour)
	tok, err := e.svc.RefreshWithProof(ctx, tok.RefreshToken, assertion(dev.Assert(integrity.RefreshBinding(tok.RefreshToken))))
	if err != nil || !tok.IntegrityPassed {
		t.Fatalf("assertion: %v %v", tok.IntegrityPassed, err)
	}
	if _, counter := e.deviceKey(t, tok.SessionID); counter != 1 {
		t.Fatalf("sayaç = %d", counter)
	}

	// A day later an assertion with an old counter is a replay: failed,
	// but the refresh goes through.
	e.now = e.now.Add(25 * time.Hour)
	tok, err = e.svc.RefreshWithProof(ctx, tok.RefreshToken, assertion(dev.AssertWith(integrity.RefreshBinding(tok.RefreshToken), 1)))
	if err != nil || tok.AccessToken == "" || tok.IntegrityPassed {
		t.Fatalf("tekrar: %v %v", tok.IntegrityPassed, err)
	}
	if s := e.integrityOf(t, tok.SessionID); s.status != "failed" || s.reason.String != "binding" {
		t.Fatalf("tekrar kaydı = %+v", s)
	}
	if _, counter := e.deviceKey(t, tok.SessionID); counter != 1 {
		t.Fatalf("tekrar sayacı değiştirdi: %d", counter)
	}

	// An hour later the next assertion passes again.
	e.now = e.now.Add(2 * time.Hour)
	tok, err = e.svc.RefreshWithProof(ctx, tok.RefreshToken, assertion(dev.Assert(integrity.RefreshBinding(tok.RefreshToken))))
	if err != nil || !tok.IntegrityPassed {
		t.Fatalf("sonraki assertion: %v %v", tok.IntegrityPassed, err)
	}
	if _, counter := e.deviceKey(t, tok.SessionID); counter != 2 {
		t.Fatalf("sayaç = %d", counter)
	}

	// A sign-in without a proof binds nothing.
	plain := e.signInIOS(t, "sub-ios-2", integrity.Proof{})
	if key, counter := e.deviceKey(t, plain.SessionID); key != nil || counter != -1 {
		t.Fatalf("kanıtsız girişte anahtar: %x %d", key, counter)
	}
}
