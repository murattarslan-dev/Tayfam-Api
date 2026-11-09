package platformadmin

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

const reason = "Tekrarlanan taciz şikâyetleri"

// signedIn returns the claims of a fresh panel sign-in.
func (e *env) signedIn(t *testing.T) auth.AdminClaims {
	t.Helper()
	ctx := context.Background()
	tok, err := e.svc.SignIn(ctx, SignInInput{IDToken: "ok:g-admin", Nonce: nonce, AccessEmail: adminEmail})
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.verifier.Verify(ctx, tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// session opens a session for user directly (device and session rows).
func (e *env) session(t *testing.T, user id.ID) id.ID {
	t.Helper()
	ctx := context.Background()
	dev, sid := id.New(), id.New()
	if err := e.q.CreateDevice(ctx, store.CreateDeviceParams{ID: dev.PG(), UserID: user.PG(), Platform: "ios"}); err != nil {
		t.Fatal(err)
	}
	if err := e.q.CreateSession(ctx, store.CreateSessionParams{ID: sid.PG(), UserID: user.PG(), DeviceID: dev.PG(),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	return sid
}

// events returns the outbox event types about user, oldest first.
func (e *env) events(t *testing.T, user id.ID) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT type FROM domain_events WHERE subject_id = $1 ORDER BY id`, user.PG())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestSearchUsers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := e.signedIn(t)
	target := e.user(t, "g-target")
	if _, err := e.pool.Exec(ctx, `UPDATE users SET display_name = 'Ayşe_Yılmaz', ref_code = 'TYF-ABCDEF' WHERE id = $1`, target.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE auth_identities SET email = 'Ayse@Example.com' WHERE user_id = $1`, target.PG()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"ayşe_y", "tyf-abcdef", "ayse@example.COM"} {
		got, err := e.svc.SearchUsers(ctx, c, q)
		if err != nil || len(got) != 1 || got[0].ID != target || got[0].RefCode != "TYF-ABCDEF" {
			t.Errorf("%q: %+v %v", q, got, err)
		}
	}
	// LIKE wildcards are matched literally.
	if got, _ := e.svc.SearchUsers(ctx, c, "%%"); len(got) != 0 {
		t.Fatalf("joker karakter: %+v", got)
	}
	if _, err := e.svc.SearchUsers(ctx, c, "a"); !errors.Is(err, ErrValidation) {
		t.Fatalf("kısa arama: %v", err)
	}
}

// TestPanelSeesNoPersonalDetail: the user detail the panel gets has no
// birth date, e-mail address, IP, device or token field (Notion "Panelin
// göremedikleri"; ADR-0012). A new field must be added here on purpose.
func TestPanelSeesNoPersonalDetail(t *testing.T) {
	var fields []string
	for _, typ := range []reflect.Type{reflect.TypeFor[UserDetail](), reflect.TypeFor[UserSummary](), reflect.TypeFor[Sanction]()} {
		for f := range typ.Fields() {
			fields = append(fields, typ.Name()+"."+f.Name)
		}
	}
	want := []string{
		"UserDetail.UserSummary", "UserDetail.CreatedAt", "UserDetail.ActiveSessions", "UserDetail.Sanction",
		"UserSummary.ID", "UserSummary.DisplayName", "UserSummary.RefCode", "UserSummary.Registered", "UserSummary.Deleted", "UserSummary.ActiveSanction",
		"Sanction.ID", "Sanction.Type", "Sanction.Reason", "Sanction.StartsAt", "Sanction.EndsAt",
	}
	if !slices.Equal(fields, want) {
		t.Fatalf("panelin kullanıcı alanları değişti: %v", fields)
	}
}

func TestRevokeUserSessions(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := e.signedIn(t)
	target := e.user(t, "g-target")
	e.session(t, target)
	e.session(t, target)

	if _, err := e.svc.RevokeUserSessions(ctx, c, target, "kısa"); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("gerekçesiz: %v", err)
	}
	n, err := e.svc.RevokeUserSessions(ctx, c, target, reason)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := e.events(t, target); !slices.Equal(got, []string{"session.revoked", "session.revoked"}) {
		t.Fatalf("olaylar = %v", got)
	}
	d, err := e.svc.GetUser(ctx, c, target)
	if err != nil || d.ActiveSessions != 0 {
		t.Fatalf("detay = %+v %v", d, err)
	}
	if _, err := e.svc.RevokeUserSessions(ctx, c, id.New(), reason); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("bilinmeyen kullanıcı: %v", err)
	}
	// More than 5 minutes after the Google sign-in: refused.
	e.now = e.now.Add(6 * time.Minute)
	if _, err := e.svc.RevokeUserSessions(ctx, c, target, reason); !errors.Is(err, ErrReauthenticationRequired) {
		t.Fatalf("eski giriş: %v", err)
	}
}

func TestBanAndLift(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := e.signedIn(t)
	target := e.user(t, "g-target")
	e.session(t, target)

	report := id.New()
	sn, err := e.svc.SanctionUser(ctx, c, SanctionInput{User: target, Type: SanctionBan, Reason: reason, Report: report})
	if err != nil {
		t.Fatal(err)
	}
	if sn.Type != SanctionBan || !sn.EndsAt.IsZero() {
		t.Fatalf("yaptırım = %+v", sn)
	}
	// Sessions closed, chat and core told, nothing about the reason sent.
	if got := e.events(t, target); !slices.Equal(got, []string{"session.revoked", "user.sanctioned", "user.banned"}) {
		t.Fatalf("olaylar = %v", got)
	}
	var payloads []string
	rows, _ := e.pool.Query(ctx, `SELECT payload::text FROM domain_events WHERE subject_id = $1`, target.PG())
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		payloads = append(payloads, p)
	}
	rows.Close()
	if strings.Contains(strings.Join(payloads, ""), "taciz") {
		t.Fatal("gerekçe olaya yazıldı")
	}
	d, err := e.svc.GetUser(ctx, c, target)
	if err != nil || d.Sanction == nil || d.ActiveSanction != SanctionBan || d.ActiveSessions != 0 {
		t.Fatalf("detay = %+v %v", d, err)
	}
	var meta string
	if err := e.pool.QueryRow(ctx, `SELECT metadata::text FROM platform_audit_logs WHERE action = 'user.ban'`).Scan(&meta); err != nil || !strings.Contains(meta, report.String()) {
		t.Fatalf("denetim kaydı: %s %v", meta, err)
	}

	if _, err := e.svc.SanctionUser(ctx, c, SanctionInput{User: target, Type: SanctionBan, Reason: reason}); !errors.Is(err, ErrSanctionConflict) {
		t.Fatalf("ikinci ban: %v", err)
	}
	if err := e.svc.LiftSanction(ctx, c, target, reason); err != nil {
		t.Fatal(err)
	}
	if got := e.events(t, target); got[len(got)-1] != "user.unbanned" {
		t.Fatalf("olaylar = %v", got)
	}
	if err := e.svc.LiftSanction(ctx, c, target, reason); !errors.Is(err, ErrSanctionConflict) {
		t.Fatalf("yaptırımsız kaldırma: %v", err)
	}
}

func TestSuspension(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := e.signedIn(t)
	target := e.user(t, "g-target")
	for name, in := range map[string]SanctionInput{
		"bitişsiz askı":  {User: target, Type: SanctionSuspend, Reason: reason},
		"çok kısa":       {User: target, Type: SanctionSuspend, Reason: reason, EndsAt: e.now.Add(time.Minute)},
		"çok uzun":       {User: target, Type: SanctionSuspend, Reason: reason, EndsAt: e.now.Add(400 * 24 * time.Hour)},
		"bitişli ban":    {User: target, Type: SanctionBan, Reason: reason, EndsAt: e.now.Add(time.Hour * 24)},
		"bilinmeyen tür": {User: target, Type: "warn", Reason: reason},
	} {
		if _, err := e.svc.SanctionUser(ctx, c, in); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.svc.SanctionUser(ctx, c, SanctionInput{User: target, Type: SanctionSuspend, Reason: reason, EndsAt: e.now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// No user.banned for a suspension: content stays visible.
	if got := e.events(t, target); slices.Contains(got, "user.banned") || !slices.Contains(got, "user.sanctioned") {
		t.Fatalf("olaylar = %v", got)
	}
	// Once the suspension is over (not lifted), a new sanction replaces it.
	if _, err := e.owner.Exec(ctx, `UPDATE platform_sanctions SET starts_at = now() - interval '3 hours', ends_at = now() - interval '1 hour' WHERE user_id = $1`, target.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SanctionUser(ctx, c, SanctionInput{User: target, Type: SanctionBan, Reason: reason}); err != nil {
		t.Fatalf("süresi dolmuş askıdan sonra ban: %v", err)
	}
}

func TestRecordFromCoreIsIdempotent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ev := id.New()
	en := CoreEntry{EventID: ev, OccurredAt: e.now, Actor: e.admin, AdminSession: id.New(),
		Entry: Entry{Action: "tayfa.suspend", TargetType: "tayfa", TargetID: id.New(), Reason: reason}}
	for range 2 {
		if err := e.svc.RecordFromCore(ctx, en); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM platform_audit_logs WHERE id = $1`, ev.PG()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("kayıt sayısı = %d %v", n, err)
	}
	bad := en
	bad.EventID = id.New()
	bad.Reason = ""
	if err := e.svc.RecordFromCore(ctx, bad); !errors.Is(err, ErrInvalidAudit) {
		t.Fatalf("gerekçesiz: %v", err)
	}
}
