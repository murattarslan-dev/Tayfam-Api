package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/announcement"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

const reason = "Topluluk kurallarına aykırı içerik"

// fakeChecker stands in for identity's GetAdmin.
type fakeChecker struct{ err error }

func (f *fakeChecker) Check(context.Context) error { return f.err }

type env struct {
	svc     *Service
	pool    *pgxpool.Pool
	checker *fakeChecker
	now     time.Time
	tayfas  *tayfa.Service
	ann     *announcement.Service
	claims  auth.AdminClaims
	admin   id.ID
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, coredb.Migrations())
	e := &env{pool: pool, checker: &fakeChecker{}, now: time.Now().UTC().Truncate(time.Second), admin: id.New()}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var err error
	e.svc, err = New(Config{Pool: pool, Log: log, Checker: e.checker, Now: func() time.Time { return e.now }})
	must(t, err)
	e.tayfas, err = tayfa.New(tayfa.Config{Pool: pool, Log: log})
	must(t, err)
	e.ann, err = announcement.New(announcement.Config{Pool: pool})
	must(t, err)
	e.claims = auth.AdminClaims{Subject: e.admin.String(), SessionID: id.New().String(), AuthTime: e.now.Add(-time.Minute), ExpiresAt: e.now.Add(time.Hour)}
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func is(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: %v, beklenen %v", what, err, want)
	}
}

type crew struct {
	tayfa, admin, mod, member id.ID
}

func (e *env) crew(t *testing.T) crew {
	t.Helper()
	c := crew{admin: id.New(), mod: id.New(), member: id.New()}
	v, err := e.tayfas.Create(ctx, c.admin, tayfa.CreateInput{Name: "Kamp Tayfası " + id.New().String()[:8], JoinPolicy: tayfa.JoinOpen, ClientRequestID: id.New().String()})
	must(t, err)
	c.tayfa = v.ID
	for _, u := range []id.ID{c.mod, c.member} {
		must(t, txn.WithActor(ctx, e.pool, u, func(_ pgx.Tx, q *store.Queries) error {
			return q.CreateMembership(ctx, store.CreateMembershipParams{TayfaID: c.tayfa.PG(), UserID: u.PG(), Role: "member", Status: "active",
				JoinedVia: "code", JoinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}})
		}))
	}
	must(t, txn.WithActor(ctx, e.pool, c.admin, func(_ pgx.Tx, q *store.Queries) error {
		_, err := q.SetMembershipRole(ctx, store.SetMembershipRoleParams{TayfaID: c.tayfa.PG(), UserID: c.mod.PG(), Role: "moderator"})
		return err
	}))
	return c
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

// audits returns the platform.audit events' payloads, oldest first.
func (e *env) audits(t *testing.T) []map[string]string {
	t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT actor_id, payload FROM domain_events WHERE type = 'platform.audit' ORDER BY id`)
	must(t, err)
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var actor pgtype.UUID
		var p []byte
		must(t, rows.Scan(&actor, &p))
		m := map[string]string{}
		must(t, json.Unmarshal(p, &m))
		if a, _ := id.FromPG(actor); a.String() != e.admin.String() {
			t.Fatalf("denetim olayının aktörü yönetici değil: %v", a)
		}
		out = append(out, m)
	}
	return out
}

// escalate files a platform report about target directly.
func (e *env) escalate(t *testing.T, tf pgtype.UUID, targetType string, target, subject, reporter id.ID, reasonCode, priority string) id.ID {
	t.Helper()
	r := id.New()
	var minorAt any
	if priority == "minor_involved" {
		minorAt = time.Now()
	}
	var evidence any
	escalation := "unresolved"
	if targetType == "dm_message" {
		evidence = "Kanıt metni"
		escalation = "direct_message"
	}
	_, err := e.pool.Exec(ctx, `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, subject_user_id, reason,
		priority, minor_involved_at, platform_only, escalated_to_platform_at, escalation_reason, platform_status, evidence_text)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now(), $11, 'open', $12)`,
		r.PG(), tf, reporter.PG(), targetType, target.PG(), subject.PG(), reasonCode, priority, minorAt,
		targetType == "tayfa" || targetType == "dm_message", escalation, evidence)
	must(t, err)
	return r
}

func TestEveryCallAsksIdentity(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	e.checker.err = apperr.ErrNotPlatformAdmin
	_, err := e.svc.SearchTayfas(ctx, e.claims, "")
	is(t, "arama", err, apperr.ErrNotPlatformAdmin)
	is(t, "askı", e.svc.SuspendTayfa(ctx, e.claims, c.tayfa, reason), apperr.ErrNotPlatformAdmin)
	if e.count(t, `SELECT count(*) FROM tayfas WHERE suspended_at IS NOT NULL`) != 0 {
		t.Fatal("yönetici olmayan askıya aldı")
	}
}

func TestDestructiveNeedsReasonAndRecentSignIn(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	is(t, "gerekçesiz", e.svc.SuspendTayfa(ctx, e.claims, c.tayfa, "kısa"), apperr.ErrReasonRequired)
	old := e.claims
	old.AuthTime = e.now.Add(-6 * time.Minute)
	is(t, "eski giriş", e.svc.SuspendTayfa(ctx, old, c.tayfa, reason), apperr.ErrReauthenticationRequired)
	is(t, "eski giriş, silme", e.svc.DeleteTayfa(ctx, old, c.tayfa, reason), apperr.ErrReauthenticationRequired)
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'platform.audit'`); n != 0 {
		t.Fatalf("reddedilen işlem kaydedildi: %d", n)
	}
}

func TestSuspendAndUnsuspend(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	must(t, e.svc.SuspendTayfa(ctx, e.claims, c.tayfa, reason))
	is(t, "ikinci askı", e.svc.SuspendTayfa(ctx, e.claims, c.tayfa, reason), apperr.ErrTayfaStateConflict)

	// Read-only for its members.
	_, err := e.ann.Create(ctx, c.admin, c.tayfa, announcement.CreateInput{Title: "Duyuru", ClientRequestID: id.New().String()})
	is(t, "askıda duyuru", err, apperr.ErrTayfaReadOnly)

	d, err := e.svc.GetTayfa(ctx, e.claims, c.tayfa)
	must(t, err)
	if d.SuspendedAt.IsZero() || d.SuspendReason != reason || len(d.Staff) != 2 || d.MemberCount != 3 {
		t.Fatalf("detay = %+v", d)
	}
	// Staff notified, chat told, the tayfa's own log without the admin.
	var payload string
	must(t, e.pool.QueryRow(ctx, `SELECT payload::text FROM domain_events WHERE type = 'notification.requested'
		AND payload->>'type' = 'tayfa_suspended'`).Scan(&payload))
	if !strings.Contains(payload, c.admin.String()) || !strings.Contains(payload, c.mod.String()) || strings.Contains(payload, c.member.String()) {
		t.Fatalf("bildirim alıcıları: %s", payload)
	}
	if e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'tayfa.suspended'`) != 1 {
		t.Fatal("tayfa.suspended yok")
	}
	if e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'platform.suspended' AND actor_id IS NULL`) != 1 {
		t.Fatal("tayfanın denetim kaydı yok ya da yöneticiyi gösteriyor")
	}

	must(t, e.svc.UnsuspendTayfa(ctx, e.claims, c.tayfa, reason))
	_, err = e.ann.Create(ctx, c.admin, c.tayfa, announcement.CreateInput{Title: "Duyuru", ClientRequestID: id.New().String()})
	must(t, err)
	a := e.audits(t)
	if len(a) != 2 || a[0]["action"] != "tayfa.suspend" || a[1]["action"] != "tayfa.unsuspend" || a[0]["reason"] != reason || a[0]["admin_session_id"] != e.claims.SessionID {
		t.Fatalf("denetim olayları = %v", a)
	}
}

func TestDeleteTayfa(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	must(t, e.svc.DeleteTayfa(ctx, e.claims, c.tayfa, reason))
	if e.count(t, `SELECT count(*) FROM tayfas WHERE id = $1 AND deleted_at IS NOT NULL AND deleted_by = $2`, c.tayfa.PG(), e.admin.PG()) != 1 {
		t.Fatal("silinmedi")
	}
	if e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'tayfa.deleted'`) != 1 {
		t.Fatal("tayfa.deleted yok")
	}
	is(t, "ikinci silme", e.svc.DeleteTayfa(ctx, e.claims, c.tayfa, reason), apperr.ErrTayfaStateConflict)
	is(t, "bilinmeyen", e.svc.DeleteTayfa(ctx, e.claims, id.New(), reason), apperr.ErrTayfaNotFound)
}

// TestSetMemberRole: the narrow 'platform_admin' system job changes the
// role of an active member and nothing else; the last-admin rule holds.
func TestSetMemberRole(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	// Make the tayfa ownerless: the trigger lets nobody remove the last
	// admin, so the test does it with the trigger off.
	_, err := e.pool.Exec(ctx, `ALTER TABLE memberships DISABLE TRIGGER memberships_guard;
		UPDATE memberships SET role = 'member' WHERE tayfa_id = '`+c.tayfa.String()+`' AND user_id = '`+c.admin.String()+`';
		ALTER TABLE memberships ENABLE TRIGGER memberships_guard;`)
	must(t, err)
	must(t, e.svc.SetMemberRole(ctx, e.claims, c.tayfa, c.mod, "admin", reason))
	if err := e.svc.SetMemberRole(ctx, e.claims, c.tayfa, c.mod, "member", reason); store.SQLState(err) != store.CodeLastAdmin {
		t.Fatalf("son yöneticiyi düşürme: %v", err)
	}
	if e.count(t, `SELECT count(*) FROM memberships WHERE tayfa_id = $1 AND role = 'admin'`, c.tayfa.PG()) != 1 {
		t.Fatal("tayfada tek yönetici kalmalıydı")
	}
	is(t, "üye olmayan", e.svc.SetMemberRole(ctx, e.claims, c.tayfa, id.New(), "admin", reason), apperr.ErrMemberNotFound)
	is(t, "aynı rol", e.svc.SetMemberRole(ctx, e.claims, c.tayfa, c.member, "member", reason), apperr.ErrTayfaStateConflict)
}

// TestSetMemberRoleKeepsLastAdmin: demoting the only admin is refused.
func TestSetMemberRoleKeepsLastAdmin(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	err := e.svc.SetMemberRole(ctx, e.claims, c.tayfa, c.admin, "member", reason)
	if store.SQLState(err) != store.CodeLastAdmin {
		t.Fatalf("son yönetici düşürüldü: %v", err)
	}
}

// TestPlatformAdminJobIsNarrow: under app.system_job = 'platform_admin' the
// trigger refuses everything but an active member's role.
func TestPlatformAdminJobIsNarrow(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	for name, sql := range map[string]string{
		"ekleme":          `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via) VALUES ('` + c.tayfa.String() + `', '` + id.New().String() + `', 'member', 'invited', 'direct_invite')`,
		"silme":           `DELETE FROM memberships WHERE tayfa_id = '` + c.tayfa.String() + `' AND user_id = '` + c.member.String() + `'`,
		"durum":           `UPDATE memberships SET status = 'banned' WHERE tayfa_id = '` + c.tayfa.String() + `' AND user_id = '` + c.member.String() + `'`,
		"rol ve susturma": `UPDATE memberships SET role = 'moderator', muted_until = now() + interval '1 hour' WHERE tayfa_id = '` + c.tayfa.String() + `' AND user_id = '` + c.member.String() + `'`,
	} {
		err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.system_job', 'platform_admin', true)`); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, sql)
			return err
		})
		if store.SQLState(err) != "TF001" {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An unknown job is still refused.
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT set_config('app.system_job', 'baska', true); UPDATE memberships SET role = 'moderator' WHERE user_id = '`+c.member.String()+`'`)
		return err
	})
	if store.SQLState(err) != "TF001" {
		t.Fatalf("bilinmeyen sistem işi: %v", err)
	}
}

func TestPlatformQueueOrder(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	tf := c.tayfa.PG()
	normal := e.escalate(t, tf, "user", c.member, c.member, c.mod, "spam", "normal")
	child := e.escalate(t, tf, "user", c.mod, c.mod, c.member, "child_safety", "normal")
	minor := e.escalate(t, tf, "tayfa", c.tayfa, id.ID{}, c.member, "other", "minor_involved")
	rs, err := e.svc.ListPlatformReports(ctx, e.claims, false)
	must(t, err)
	var got []id.ID
	for _, r := range rs {
		got = append(got, r.ID)
	}
	if len(got) != 3 || got[2] != normal || !slices.Contains(got[:2], child) || !slices.Contains(got[:2], minor) {
		t.Fatalf("sıra = %v (normal %v)", got, normal)
	}
}

func TestReportedItemAndDecision(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	shown, err := e.ann.Create(ctx, c.member, c.tayfa, announcement.CreateInput{Title: "Şikâyet edilen", Body: "Gövde", ClientRequestID: id.New().String()})
	if errors.Is(err, apperr.ErrPermissionDenied) {
		shown, err = e.ann.Create(ctx, c.admin, c.tayfa, announcement.CreateInput{Title: "Şikâyet edilen", Body: "Gövde", ClientRequestID: id.New().String()})
	}
	must(t, err)
	other, err := e.ann.Create(ctx, c.admin, c.tayfa, announcement.CreateInput{Title: "Başka duyuru", Body: "Gizli kalmalı", ClientRequestID: id.New().String()})
	must(t, err)
	_ = other
	rep := e.escalate(t, c.tayfa.PG(), "announcement", shown.ID, c.admin, c.member, "spam", "normal")

	r, item, err := e.svc.GetReportedItem(ctx, e.claims, rep)
	must(t, err)
	if item == nil || item.Title != "Şikâyet edilen" || item.Body != "Gövde" || r.Reporter != c.member {
		t.Fatalf("öğe = %+v rapor = %+v", item, r)
	}
	if a := e.audits(t); len(a) != 1 || a[0]["action"] != "report.view_item" {
		t.Fatalf("görüntüleme kaydı = %v", a)
	}

	is(t, "kullanıcıda içerik kaldırma", e.svc.DecideReport(ctx, e.claims,
		e.escalate(t, c.tayfa.PG(), "user", c.member, c.member, c.mod, "spam", "normal"), DecisionContentRemoved, reason), apperr.ErrDecisionNotApplicable)

	must(t, e.svc.DecideReport(ctx, e.claims, rep, DecisionContentRemoved, reason))
	if e.count(t, `SELECT count(*) FROM announcements WHERE id = $1 AND deleted_at IS NOT NULL AND deleted_by = $2`, shown.ID.PG(), e.admin.PG()) != 1 {
		t.Fatal("içerik kaldırılmadı")
	}
	if e.count(t, `SELECT count(*) FROM announcements WHERE deleted_at IS NOT NULL`) != 1 {
		t.Fatal("başka içerik de kaldırıldı")
	}
	if e.count(t, `SELECT count(*) FROM reports WHERE id = $1 AND platform_status = 'resolved' AND platform_decision = 'content_removed'
		AND status = 'resolved' AND resolution = 'content_removed'`, rep.PG()) != 1 {
		t.Fatal("karar yazılmadı")
	}
	is(t, "ikinci karar", e.svc.DecideReport(ctx, e.claims, rep, DecisionDismissed, reason), apperr.ErrReportAlreadyResolved)
	// The decision cannot be rewritten even in SQL.
	_, err = e.pool.Exec(ctx, `UPDATE reports SET platform_decision = 'dismissed' WHERE id = $1`, rep.PG())
	if store.SQLState(err) != "TF100" {
		t.Fatalf("karar değiştirildi: %v", err)
	}
}

func TestDecisionSuspendsTayfa(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	rep := e.escalate(t, c.tayfa.PG(), "tayfa", c.tayfa, id.ID{}, c.member, "other", "normal")
	must(t, e.svc.DecideReport(ctx, e.claims, rep, DecisionTayfaSuspended, reason))
	if e.count(t, `SELECT count(*) FROM tayfas WHERE id = $1 AND suspended_at IS NOT NULL`, c.tayfa.PG()) != 1 {
		t.Fatal("tayfa askıya alınmadı")
	}
	a := e.audits(t)
	if len(a) != 1 || a[0]["action"] != "report.decide" || a[0]["decision"] != DecisionTayfaSuspended || a[0]["report_id"] != rep.String() {
		t.Fatalf("denetim = %v", a)
	}
}

// TestDirectMessageReportShowsEvidenceOnly: a DM report shows the
// reporter's own evidence text; core has no message content to show.
func TestDirectMessageReportShowsEvidenceOnly(t *testing.T) {
	e := setup(t)
	sender, reporter, msg := id.New(), id.New(), id.New()
	rep := e.escalate(t, pgtype.UUID{}, "dm_message", msg, sender, reporter, "harassment", "normal")
	_, item, err := e.svc.GetReportedItem(ctx, e.claims, rep)
	must(t, err)
	if item == nil || item.Body != "Kanıt metni" || item.Author != sender {
		t.Fatalf("öğe = %+v", item)
	}
	is(t, "DM'de içerik kaldırma", e.svc.DecideReport(ctx, e.claims, rep, DecisionContentRemoved, reason), apperr.ErrDecisionNotApplicable)
	is(t, "DM'de tayfa askısı", e.svc.DecideReport(ctx, e.claims, rep, DecisionTayfaSuspended, reason), apperr.ErrDecisionNotApplicable)
	must(t, e.svc.DecideReport(ctx, e.claims, rep, DecisionUserBanned, reason))
}

// TestPanelCannotSee pins what the panel's views carry (Notion "Panelin
// göremedikleri"): no location, no poll votes, no content beyond the
// reported item. A new field must be added here on purpose.
func TestPanelCannotSee(t *testing.T) {
	var fields []string
	for _, typ := range []reflect.Type{reflect.TypeFor[Item](), reflect.TypeFor[TayfaDetail](), reflect.TypeFor[TayfaSummary](), reflect.TypeFor[Member](), reflect.TypeFor[Report]()} {
		for f := range typ.Fields() {
			fields = append(fields, typ.Name()+"."+f.Name)
		}
	}
	want := []string{
		"Item.TargetType", "Item.Target", "Item.Author", "Item.Title", "Item.Body", "Item.Options", "Item.CreatedAt", "Item.DeletedAt",
		"TayfaDetail.TayfaSummary", "TayfaDetail.Description", "TayfaDetail.City", "TayfaDetail.District", "TayfaDetail.SuspendReason", "TayfaDetail.Staff", "TayfaDetail.OpenPlatformReports",
		"TayfaSummary.ID", "TayfaSummary.Name", "TayfaSummary.Status", "TayfaSummary.Listed", "TayfaSummary.MemberCount", "TayfaSummary.CreatedAt", "TayfaSummary.SuspendedAt", "TayfaSummary.DeletedAt", "TayfaSummary.DeletionScheduledAt",
		"Member.User", "Member.DisplayName", "Member.Role", "Member.Status", "Member.JoinedAt",
		"Report.ID", "Report.Tayfa", "Report.TargetType", "Report.Target", "Report.Subject", "Report.Reporter", "Report.Reason", "Report.Description", "Report.Priority", "Report.CreatedAt", "Report.EscalatedAt", "Report.Resolved", "Report.Decision", "Report.DecidedAt", "Report.evidenceText", "Report.tayfaResolved",
	}
	if !slices.Equal(fields, want) {
		t.Fatalf("panelin gördüğü alanlar değişti:\n%v", fields)
	}
	// The queries never select a location or a vote.
	src, err := readQueries()
	must(t, err)
	if m := regexp.MustCompile(`\b(lat|lng|meeting_[a-z_]+|poll_votes|check_ins?|dm_[a-z_]+|channel_messages)\b`).FindString(src); m != "" {
		t.Errorf("panel sorgusu %q okuyor", m)
	}
}

// readQueries returns the panel's SQL without comments.
func readQueries() (string, error) {
	b, err := os.ReadFile("../../../../db/core/queries/admin.sql")
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for line := range strings.SplitSeq(string(b), "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		out.WriteString(line + "\n")
	}
	return out.String(), nil
}
