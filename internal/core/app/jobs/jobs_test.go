package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

type env struct {
	pool *pgxpool.Pool
	// admin is a superuser pool for fixtures core_app cannot write
	// (seeding with triggers off, ADR-0014).
	admin *pgxpool.Pool
	log   *slog.Logger
	base  time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	return &env{
		pool:  d.App,
		admin: d.Admin,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		base:  time.Now().UTC().Truncate(time.Microsecond),
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// at returns a Service whose clock reads base + d.
func (e *env) at(t *testing.T, d time.Duration) *Service {
	t.Helper()
	now := e.base.Add(d)
	s, err := New(Config{Pool: e.pool, Log: e.log, Now: func() time.Time { return now }})
	must(t, err)
	return s
}

func (e *env) tayfas(t *testing.T, d time.Duration) *tayfa.Service {
	t.Helper()
	now := e.base.Add(d)
	s, err := tayfa.New(tayfa.Config{Pool: e.pool, Log: e.log, Now: func() time.Time { return now }})
	must(t, err)
	return s
}

// newTayfa creates a tayfa through the tayfa service (its admin is the
// creator) and returns it with its admin.
func (e *env) newTayfa(t *testing.T) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas(t, 0).Create(ctx, admin, tayfa.CreateInput{Name: "Pazar Yürüyüşçüleri", ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID, admin
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// seed writes an active membership with triggers off (superuser test
// connection).
func (e *env) seed(t *testing.T, tf id.ID, role string) id.ID {
	t.Helper()
	u := id.New()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	must(t, store.New(tx).CreateMembership(ctx, store.CreateMembershipParams{
		TayfaID: tf.PG(), UserID: u.PG(), Role: role, Status: "active", JoinedVia: "code",
		JoinedAt: pgtype.Timestamptz{Time: e.base, Valid: true},
	}))
	must(t, tx.Commit(ctx))
	return u
}

type tayfaState struct {
	status    string
	warned    bool
	archived  bool
	deleted   bool
	lastActed time.Time
}

func (e *env) state(t *testing.T, tf id.ID) tayfaState {
	t.Helper()
	var s tayfaState
	var w, a, d pgtype.Timestamptz
	must(t, e.pool.QueryRow(ctx, `SELECT status, warned_at, archived_at, deleted_at, last_activity_at FROM tayfas WHERE id = $1`, tf.PG()).
		Scan(&s.status, &w, &a, &d, &s.lastActed))
	s.warned, s.archived, s.deleted = w.Valid, a.Valid, d.Valid
	return s
}

type event struct {
	tayfa   id.ID
	subject id.ID
	payload map[string]any
}

// events returns the outbox events of type typ in order.
func (e *env) events(t *testing.T, typ string) []event {
	t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT tayfa_id, subject_id, payload FROM domain_events WHERE type = $1 ORDER BY id`, typ)
	must(t, err)
	defer rows.Close()
	var out []event
	for rows.Next() {
		var tf, sub pgtype.UUID
		var raw []byte
		must(t, rows.Scan(&tf, &sub, &raw))
		ev := event{tayfa: id.ID(tf.Bytes), subject: id.ID(sub.Bytes)}
		must(t, json.Unmarshal(raw, &ev.payload))
		out = append(out, ev)
	}
	must(t, rows.Err())
	return out
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func run(t *testing.T, what string, pass func(context.Context) (int, error), want int) {
	t.Helper()
	n, err := pass(ctx)
	must(t, err)
	if n != want {
		t.Fatalf("%s: %d satır, %d bekleniyordu", what, n, want)
	}
}

// notices returns the notification.requested events of type typ, in order.
func (e *env) notices(t *testing.T, typ string) []event {
	t.Helper()
	var out []event
	for _, ev := range e.events(t, notify.EventType) {
		if ev.payload["type"] == typ {
			out = append(out, ev)
		}
	}
	return out
}

// noticeFor returns the recipients of the typ notification about target.
func (e *env) noticeFor(t *testing.T, typ string, target id.ID, extra ...string) []string {
	t.Helper()
	for _, ev := range e.notices(t, typ) {
		if ev.subject != target {
			continue
		}
		if len(extra) == 2 && ev.payload[extra[0]] != extra[1] {
			continue
		}
		return strs(ev.payload["recipient_ids"])
	}
	return nil
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

const day = 24 * time.Hour

// ---------------------------------------------------------------------------
// Pasif tayfa temizliği

func TestCleanupTayfas(t *testing.T) {
	e := setup(t)
	// Created 8 days ago with one member: few_members.
	few, fewAdmin := e.newTayfa(t)
	e.exec(t, `UPDATE tayfas SET created_at = $2, last_activity_at = $3 WHERE id = $1`, few.PG(), e.base.Add(-8*day), e.base.Add(-day))
	// Three members, last activity 31 days ago: inactive.
	idle, idleAdmin := e.newTayfa(t)
	idleMod := e.seed(t, idle, "moderator")
	e.seed(t, idle, "member")
	e.exec(t, `UPDATE tayfas SET created_at = $2, last_activity_at = $2 WHERE id = $1`, idle.PG(), e.base.Add(-31*day))
	// Below both thresholds.
	busy, _ := e.newTayfa(t)
	e.seed(t, busy, "member")
	e.seed(t, busy, "member")
	e.exec(t, `UPDATE tayfas SET created_at = $2, last_activity_at = $3 WHERE id = $1`, busy.PG(), e.base.Add(-60*day), e.base.Add(-29*day))
	young, _ := e.newTayfa(t)
	e.exec(t, `UPDATE tayfas SET created_at = $2, last_activity_at = $2 WHERE id = $1`, young.PG(), e.base.Add(-6*day))
	// Suspended and deletion-pending tayfas are left alone.
	suspended, _ := e.newTayfa(t)
	e.exec(t, `UPDATE tayfas SET created_at = $2, last_activity_at = $2, suspended_at = $2, suspended_reason = 'test' WHERE id = $1`, suspended.PG(), e.base.Add(-40*day))
	pending, _ := e.newTayfa(t)
	e.exec(t, `UPDATE tayfas SET created_at = $2, last_activity_at = $2, deletion_requested_at = $3, deletion_scheduled_for = $4 WHERE id = $1`,
		pending.PG(), e.base.Add(-40*day), e.base, e.base.Add(3*day))

	run(t, "uyarı", e.at(t, 0).CleanupTayfas, 2)
	for tf, want := range map[id.ID]string{few: "warned", idle: "warned", busy: "active", young: "active", suspended: "active", pending: "active"} {
		if got := e.state(t, tf).status; got != want {
			t.Errorf("tayfa %s durumu %q, %q bekleniyordu", tf, got, want)
		}
	}
	warned := e.events(t, EventTayfaWarned)
	if len(warned) != 2 {
		t.Fatalf("tayfa.warned = %d", len(warned))
	}
	byTayfa := map[id.ID]event{}
	for _, ev := range warned {
		byTayfa[ev.tayfa] = ev
		if ev.subject != ev.tayfa || ev.payload["tayfa_id"] != ev.tayfa.String() {
			t.Errorf("olay = %+v", ev)
		}
	}
	if r := byTayfa[few].payload["reason"]; r != ReasonFewMembers {
		t.Errorf("few sebebi %v", r)
	}
	if r := byTayfa[idle].payload["reason"]; r != ReasonInactive {
		t.Errorf("idle sebebi %v", r)
	}
	// Recipients travel in notification.requested, not in the tayfa event.
	if _, ok := byTayfa[few].payload["recipient_ids"]; ok {
		t.Error("tayfa.warned alıcı taşıyor")
	}
	if got := e.noticeFor(t, notify.TypeTayfaWarned, few, "reason", ReasonFewMembers); !slices.Equal(got, []string{fewAdmin.String()}) {
		t.Errorf("few alıcıları %v", got)
	}
	// Admins first, then moderators; plain members are not recipients.
	if got := e.noticeFor(t, notify.TypeTayfaWarned, idle, "reason", ReasonInactive); !slices.Equal(got, []string{idleAdmin.String(), idleMod.String()}) {
		t.Errorf("idle alıcıları %v", got)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'tayfa.warned' AND actor_id IS NULL`); n != 2 {
		t.Errorf("denetim kaydı %d", n)
	}

	// busy and young are done; out of the way of the steps below.
	e.exec(t, `UPDATE tayfas SET deleted_at = $2 WHERE id = ANY($1)`, []pgtype.UUID{busy.PG(), young.PG()}, e.base)

	// Running again changes nothing.
	run(t, "tekrar", e.at(t, time.Minute).CleanupTayfas, 0)
	if n := len(e.events(t, EventTayfaWarned)); n != 2 {
		t.Errorf("tekrar çalışmada tayfa.warned = %d", n)
	}

	// Activity after the warning: few becomes active again.
	e.exec(t, `UPDATE tayfas SET last_activity_at = $2 WHERE id = $1`, few.PG(), e.base.Add(time.Hour))
	run(t, "yeniden etkinleştirme", e.at(t, 2*time.Hour).CleanupTayfas, 1)
	if s := e.state(t, few); s.status != "active" || s.warned {
		t.Errorf("few = %+v", s)
	}
	if ev := e.events(t, EventTayfaReactivated); len(ev) != 1 || ev[0].tayfa != few {
		t.Errorf("tayfa.reactivated = %+v", ev)
	}

	// Just under seven days after the warning nothing is archived.
	run(t, "eşik altı", e.at(t, WarningPeriod-time.Minute).CleanupTayfas, 0)
	if s := e.state(t, idle); s.status != "warned" {
		t.Errorf("idle = %+v", s)
	}
	// Seven days: idle is archived. few still has fewer than three members
	// but was warned for that once; it is not warned again.
	run(t, "arşiv", e.at(t, WarningPeriod).CleanupTayfas, 1)
	if s := e.state(t, idle); s.status != "archived" || !s.archived {
		t.Errorf("idle = %+v", s)
	}
	if s := e.state(t, few); s.status != "active" {
		t.Errorf("few = %+v", s)
	}
	archived := e.events(t, EventTayfaArchived)
	if len(archived) != 1 || archived[0].tayfa != idle || len(e.noticeFor(t, notify.TypeTayfaArchived, idle)) != 2 {
		t.Errorf("tayfa.archived = %+v", archived)
	}
	// An archived tayfa stays archived.
	run(t, "arşiv tekrar", e.at(t, WarningPeriod+day).CleanupTayfas, 0)

	// few goes quiet for 30 days: warned again, now as inactive. Suspended
	// and deletion-pending tayfas are still left alone.
	run(t, "hareketsizlik", e.at(t, InactivityPeriod+2*time.Hour).CleanupTayfas, 1)
	if ev := e.events(t, EventTayfaWarned); len(ev) != 3 || ev[2].tayfa != few || ev[2].payload["reason"] != ReasonInactive {
		t.Errorf("tayfa.warned = %+v", ev)
	}
}

// ---------------------------------------------------------------------------
// 72 saatlik silme

func TestDeleteDueTayfas(t *testing.T) {
	e := setup(t)
	due, admin := e.newTayfa(t)
	_, err := e.tayfas(t, 0).RequestDeletion(ctx, admin, due)
	must(t, err)
	cancelled, admin2 := e.newTayfa(t)
	_, err = e.tayfas(t, 0).RequestDeletion(ctx, admin2, cancelled)
	must(t, err)
	must(t, e.tayfas(t, time.Hour).CancelDeletion(ctx, admin2, cancelled))
	live, _ := e.newTayfa(t)

	run(t, "süre dolmadan", e.at(t, tayfa.DeletionDelay-time.Minute).DeleteDueTayfas, 0)
	if e.state(t, due).deleted {
		t.Fatal("süre dolmadan silindi")
	}
	run(t, "süre dolunca", e.at(t, tayfa.DeletionDelay).DeleteDueTayfas, 1)
	if !e.state(t, due).deleted || e.state(t, cancelled).deleted || e.state(t, live).deleted {
		t.Errorf("silinme: due %v, cancelled %v, live %v", e.state(t, due).deleted, e.state(t, cancelled).deleted, e.state(t, live).deleted)
	}
	if ev := e.events(t, EventTayfaDeleted); len(ev) != 1 || ev[0].tayfa != due || ev[0].payload["tayfa_id"] != due.String() {
		t.Errorf("tayfa.deleted = %+v", ev)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1 AND action = 'tayfa.deleted' AND actor_id IS NULL`, due.PG()); n != 1 {
		t.Errorf("denetim kaydı %d", n)
	}
	run(t, "tekrar", e.at(t, tayfa.DeletionDelay+day).DeleteDueTayfas, 0)
}

// ---------------------------------------------------------------------------
// 30 günlük kalıcı silme

// content writes one row of each content type in tf, soft deleted at
// deletedAt when it is valid, and returns their IDs by table.
func (e *env) content(t *testing.T, tf id.ID, deletedAt pgtype.Timestamptz) map[string]id.ID {
	t.Helper()
	ids := map[string]id.ID{
		"announcements": id.New(), "questions": id.New(), "answers": id.New(),
		"polls": id.New(), "events": id.New(), "fishing_catches": id.New(),
	}
	author := id.New()
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	ex := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	ex(`INSERT INTO announcements (id, tayfa_id, author_id, title, client_request_id, deleted_at) VALUES ($1, $2, $3, 'Duyuru', $4, $5)`,
		ids["announcements"].PG(), tf.PG(), author.PG(), id.New().PG(), deletedAt)
	// The question stays live; its answer carries the deletion.
	q := id.New()
	ex(`INSERT INTO questions (id, tayfa_id, author_id, title, client_request_id) VALUES ($1, $2, $3, 'Kamp yeri önerisi var mı?', $4)`,
		q.PG(), tf.PG(), author.PG(), id.New().PG())
	ex(`INSERT INTO questions (id, tayfa_id, author_id, title, client_request_id, deleted_at) VALUES ($1, $2, $3, 'Silinen soru başlığı', $4, $5)`,
		ids["questions"].PG(), tf.PG(), author.PG(), id.New().PG(), deletedAt)
	ex(`INSERT INTO answers (id, question_id, tayfa_id, author_id, body, client_request_id, deleted_at) VALUES ($1, $2, $3, $4, 'Cevap', $5, $6)`,
		ids["answers"].PG(), q.PG(), tf.PG(), author.PG(), id.New().PG(), deletedAt)
	ex(`INSERT INTO polls (id, tayfa_id, created_by, question, deleted_at) VALUES ($1, $2, $3, 'Hangi hafta?', $4)`,
		ids["polls"].PG(), tf.PG(), author.PG(), deletedAt)
	ex(`INSERT INTO poll_options (id, poll_id, text, position) VALUES ($1, $2, 'Bu', 0), ($3, $2, 'Sonraki', 1)`,
		id.New().PG(), ids["polls"].PG(), id.New().PG())
	ex(`INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, created_by, deleted_at)
	    VALUES ($1, $2, 'general', 'online', 'Toplantı', $3, $4, 'https://example.test/t', $5, $6)`,
		ids["events"].PG(), tf.PG(), e.base.Add(-60*day), e.base.Add(-60*day+time.Hour), author.PG(), deletedAt)
	// A live fishing event holds the soft deleted catch.
	fe := id.New()
	ex(`INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, created_by)
	    VALUES ($1, $2, 'fishing', 'online', 'Av', $3, $4, 'https://example.test/f', $5)`,
		fe.PG(), tf.PG(), e.base.Add(-60*day), e.base.Add(-60*day+time.Hour), author.PG())
	ex(`INSERT INTO event_fishing (event_id) VALUES ($1)`, fe.PG())
	ex(`INSERT INTO fishing_catches (id, event_id, user_id, species, length_cm, client_request_id, deleted_at) VALUES ($1, $2, $3, 'Levrek', 42, $4, $5)`,
		ids["fishing_catches"].PG(), fe.PG(), author.PG(), id.New().PG(), deletedAt)
	must(t, tx.Commit(ctx))
	return ids
}

// Fixed queries per table; no SQL is assembled from strings.
var (
	existsSQL = map[string]string{
		"announcements":   `SELECT count(*) FROM announcements WHERE id = $1`,
		"questions":       `SELECT count(*) FROM questions WHERE id = $1`,
		"answers":         `SELECT count(*) FROM answers WHERE id = $1`,
		"polls":           `SELECT count(*) FROM polls WHERE id = $1`,
		"events":          `SELECT count(*) FROM events WHERE id = $1`,
		"fishing_catches": `SELECT count(*) FROM fishing_catches WHERE id = $1`,
	}
	byTayfaSQL = map[string]string{
		"memberships":     `SELECT count(*) FROM memberships WHERE tayfa_id = $1`,
		"audit_logs":      `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1`,
		"announcements":   `SELECT count(*) FROM announcements WHERE tayfa_id = $1`,
		"questions":       `SELECT count(*) FROM questions WHERE tayfa_id = $1`,
		"answers":         `SELECT count(*) FROM answers WHERE tayfa_id = $1`,
		"polls":           `SELECT count(*) FROM polls WHERE tayfa_id = $1`,
		"events":          `SELECT count(*) FROM events WHERE tayfa_id = $1`,
		"tayfa_modules":   `SELECT count(*) FROM tayfa_modules WHERE tayfa_id = $1`,
		"tayfa_interests": `SELECT count(*) FROM tayfa_interests WHERE tayfa_id = $1`,
		"invites":         `SELECT count(*) FROM invites WHERE tayfa_id = $1`,
	}
)

func (e *env) exists(t *testing.T, table string, row id.ID) bool {
	t.Helper()
	return e.count(t, existsSQL[table], row.PG()) == 1
}

func TestPurge(t *testing.T) {
	e := setup(t)
	ts := func(d time.Duration) pgtype.Timestamptz { return pgtype.Timestamptz{Time: e.base.Add(d), Valid: true} }

	// gone: a tayfa soft deleted 30 days ago, with members, content and audit
	// records.
	gone, _ := e.newTayfa(t)
	e.seed(t, gone, "moderator")
	e.content(t, gone, pgtype.Timestamptz{})
	// A series whose template is a hike with an RSVP, a check-in and an
	// activity record: the cascade crosses the series ⇄ event foreign keys.
	member := e.seed(t, gone, "member")
	hikeID := e.hike(t, gone, member, -2*time.Hour)
	e.checkin(t, hikeID, member, true)
	series := id.New()
	e.exec(t, `INSERT INTO event_series (id, tayfa_id, template_event_id, rrule, timezone, dtstart) VALUES ($1, $2, $3, 'FREQ=WEEKLY', 'Europe/Istanbul', $4)`,
		series.PG(), gone.PG(), hikeID.PG(), e.base.Add(-6*time.Hour))
	e.exec(t, `UPDATE events SET series_id = $2 WHERE id = $1`, hikeID.PG(), series.PG())
	e.exec(t, `INSERT INTO event_rsvps (event_id, user_id, status) VALUES ($1, $2, 'going')`, hikeID.PG(), member.PG())
	record := id.New()
	e.exec(t, `INSERT INTO user_activity_records (id, user_id, event_id, tayfa_id, event_type, mode, event_title, started_at, duration_minutes, attendance_method)
	    VALUES ($1, $2, $3, $4, 'hike', 'in_person', 'Zirve', $5, 240, 'hike_start')`, record.PG(), member.PG(), hikeID.PG(), gone.PG(), e.base.Add(-5*time.Hour))
	e.exec(t, `UPDATE tayfas SET deleted_at = $2 WHERE id = $1`, gone.PG(), e.base.Add(-RetentionPeriod))
	// recent: soft deleted 29 days ago, stays.
	recent, _ := e.newTayfa(t)
	e.exec(t, `UPDATE tayfas SET deleted_at = $2 WHERE id = $1`, recent.PG(), e.base.Add(-RetentionPeriod+day))
	// live: content deleted 30 days ago goes, newer and live content stays.
	live, _ := e.newTayfa(t)
	e.seed(t, live, "member")
	old := e.content(t, live, ts(-RetentionPeriod))
	newer := e.content(t, live, ts(-RetentionPeriod+time.Minute))
	kept := e.content(t, live, pgtype.Timestamptz{})
	auditBefore := e.count(t, `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1`, live.PG())
	membersBefore := e.count(t, `SELECT count(*) FROM memberships WHERE tayfa_id = $1`, live.PG())
	eventsBefore := e.count(t, `SELECT count(*) FROM events WHERE tayfa_id = $1`, live.PG())

	// 1 tayfa + 6 content rows.
	run(t, "kalıcı silme", e.at(t, 0).Purge, 7)

	if n := e.count(t, `SELECT count(*) FROM tayfas WHERE id = $1`, gone.PG()); n != 0 {
		t.Error("silinen tayfa duruyor")
	}
	for table, sql := range byTayfaSQL {
		if n := e.count(t, sql, gone.PG()); n != 0 {
			t.Errorf("%s: silinen tayfanın %d satırı kaldı", table, n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM event_series WHERE id = $1`, series.PG()) +
		e.count(t, `SELECT count(*) FROM event_rsvps WHERE event_id = $1`, hikeID.PG()) +
		e.count(t, `SELECT count(*) FROM event_hike_checkins WHERE event_id = $1`, hikeID.PG()); n != 0 {
		t.Errorf("seri, katılım ya da yoklama kaldı: %d", n)
	}
	// The activity record stays without its event and tayfa (SET NULL).
	if n := e.count(t, `SELECT count(*) FROM user_activity_records WHERE id = $1 AND event_id IS NULL AND tayfa_id IS NULL`, record.PG()); n != 1 {
		t.Error("katılım geçmişi kaydı korunmadı")
	}
	if ev := e.events(t, EventTayfaPurged); len(ev) != 1 || ev[0].tayfa != gone || ev[0].payload["tayfa_id"] != gone.String() {
		t.Errorf("tayfa.purged = %+v", ev)
	}
	if !e.state(t, recent).deleted {
		t.Error("29 günlük tayfa gitti")
	}
	for table, row := range old {
		if e.exists(t, table, row) {
			t.Errorf("%s: 30 günlük satır duruyor", table)
		}
	}
	for _, set := range []map[string]id.ID{newer, kept} {
		for table, row := range set {
			if !e.exists(t, table, row) {
				t.Errorf("%s: silinmemesi gereken satır gitti", table)
			}
		}
	}
	// The other tayfa's records are untouched.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1`, live.PG()); n != auditBefore {
		t.Errorf("canlı tayfanın denetim kaydı %d → %d", auditBefore, n)
	}
	if n := e.count(t, `SELECT count(*) FROM memberships WHERE tayfa_id = $1`, live.PG()); n != membersBefore {
		t.Errorf("canlı tayfanın üyelikleri %d → %d", membersBefore, n)
	}
	if n := e.count(t, `SELECT count(*) FROM events WHERE tayfa_id = $1`, live.PG()); n != eventsBefore-1 {
		t.Errorf("canlı tayfanın etkinlikleri %d → %d", eventsBefore, n)
	}

	run(t, "tekrar", e.at(t, 0).Purge, 0)
	// A day later, recent's turn.
	run(t, "ertesi gün", e.at(t, day+time.Minute).Purge, 1+6)
}

// ---------------------------------------------------------------------------
// Yürüyüş alarmı

// hike creates a hike event organized by organizer whose expected return
// is base + ret (grace 30 minutes).
func (e *env) hike(t *testing.T, tf, organizer id.ID, ret time.Duration) id.ID {
	t.Helper()
	ev := id.New()
	e.exec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, meeting_point_name, meeting_lat, meeting_lng, city, district, created_by)
	    VALUES ($1, $2, 'hike', 'in_person', 'Zirve', $3, $4, 'Otopark', 38.4, 27.1, 'İzmir', 'Bornova', $5)`,
		ev.PG(), tf.PG(), e.base.Add(-6*time.Hour), e.base.Add(ret), organizer.PG())
	e.exec(t, `INSERT INTO event_hike (event_id, expected_return_at, return_grace_minutes) VALUES ($1, $2, 30)`, ev.PG(), e.base.Add(ret))
	return ev
}

func (e *env) checkin(t *testing.T, ev, user id.ID, returned bool) {
	t.Helper()
	start := e.base.Add(-5 * time.Hour)
	var back pgtype.Timestamptz
	if returned {
		back = pgtype.Timestamptz{Time: e.base.Add(-time.Hour), Valid: true}
	}
	e.exec(t, `INSERT INTO event_hike_checkins (event_id, user_id, started_at, started_received_at, returned_at, returned_received_at)
	    VALUES ($1, $2, $3, $3, $4, $4)`, ev.PG(), user.PG(), start, back)
}

func (e *env) alertSent(t *testing.T, ev, user id.ID) bool {
	t.Helper()
	var at pgtype.Timestamptz
	must(t, e.pool.QueryRow(ctx, `SELECT alert_sent_at FROM event_hike_checkins WHERE event_id = $1 AND user_id = $2`, ev.PG(), user.PG()).Scan(&at))
	return at.Valid
}

func TestAlertOverdueHikes(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	mod := e.seed(t, tf, "moderator")
	organizer := e.seed(t, tf, "member")
	hiker := e.seed(t, tf, "member")
	back := e.seed(t, tf, "member")
	modHiker := mod

	// Expected back 2 hours ago (+30 minutes grace): overdue.
	late := e.hike(t, tf, organizer, -2*time.Hour)
	e.checkin(t, late, hiker, false)
	e.checkin(t, late, back, true)
	e.checkin(t, late, modHiker, false)
	// Expected back 10 minutes ago: still within the grace period.
	grace := e.hike(t, tf, organizer, -10*time.Minute)
	e.checkin(t, grace, hiker, false)

	run(t, "alarm", e.at(t, 0).AlertOverdueHikes, 2)
	if !e.alertSent(t, late, hiker) || !e.alertSent(t, late, modHiker) || e.alertSent(t, late, back) || e.alertSent(t, grace, hiker) {
		t.Error("alert_sent_at yanlış satırlarda")
	}
	got := e.events(t, EventHikeOverdue)
	if len(got) != 2 {
		t.Fatalf("hike.overdue = %d", len(got))
	}
	for _, ev := range got {
		user := ev.payload["user_id"].(string)
		want := []string{organizer.String(), admin.String(), mod.String()}
		if user == mod.String() {
			// The hiker is never a recipient of their own alarm.
			want = []string{organizer.String(), admin.String()}
		}
		if ev.tayfa != tf || ev.subject != late || ev.payload["event_id"] != late.String() || ev.payload["organizer_id"] != organizer.String() {
			t.Errorf("olay = %+v", ev)
		}
		if r := e.noticeFor(t, notify.TypeHikeOverdue, late, "user_id", user); !slices.Equal(r, want) {
			t.Errorf("%s alıcıları %v, %v bekleniyordu", user, r, want)
		}
	}

	// Sent once: running again changes nothing.
	run(t, "tekrar", e.at(t, time.Minute).AlertOverdueHikes, 0)
	// The grace period of the second hike ends at -10m + 30m.
	run(t, "tolerans eşiği altı", e.at(t, 20*time.Minute-time.Microsecond).AlertOverdueHikes, 0)
	run(t, "tolerans dolunca", e.at(t, 20*time.Minute).AlertOverdueHikes, 1)
	if !e.alertSent(t, grace, hiker) {
		t.Error("ikinci alarm yok")
	}
}

func TestAlertOverdueHikesOrganizerLeft(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	hiker := e.seed(t, tf, "member")
	ev := e.hike(t, tf, id.New(), -2*time.Hour) // organizer is not a member
	e.checkin(t, ev, hiker, false)
	run(t, "alarm", e.at(t, 0).AlertOverdueHikes, 1)
	if r := e.noticeFor(t, notify.TypeHikeOverdue, ev, "user_id", hiker.String()); !slices.Equal(r, []string{admin.String()}) {
		t.Errorf("alıcılar %v", r)
	}
}

// ---------------------------------------------------------------------------
// Plaka temizliği

func (e *env) convoy(t *testing.T, tf id.ID, ended time.Duration) id.ID {
	t.Helper()
	ev, v := id.New(), id.New()
	e.exec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, meeting_point_name, meeting_lat, meeting_lng, city, district)
	    VALUES ($1, $2, 'convoy', 'in_person', 'Konvoy', $3, $4, 'Meydan', 38.4, 27.1, 'İzmir', 'Konak')`,
		ev.PG(), tf.PG(), e.base.Add(ended-3*time.Hour), e.base.Add(ended))
	e.exec(t, `INSERT INTO event_convoy (event_id) VALUES ($1)`, ev.PG())
	e.exec(t, `INSERT INTO event_convoy_vehicles (id, event_id, user_id, make, plate) VALUES ($1, $2, $3, 'Murat', '35 ABC 123')`, v.PG(), ev.PG(), id.New().PG())
	return v
}

func (e *env) plate(t *testing.T, v id.ID) bool {
	t.Helper()
	var p pgtype.Text
	must(t, e.pool.QueryRow(ctx, `SELECT plate FROM event_convoy_vehicles WHERE id = $1`, v.PG()).Scan(&p))
	return p.Valid
}

func TestClearPlates(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	old := e.convoy(t, tf, -PlateRetention)
	recent := e.convoy(t, tf, -PlateRetention+time.Minute)
	run(t, "plaka", e.at(t, 0).ClearPlates, 1)
	if e.plate(t, old) || !e.plate(t, recent) {
		t.Errorf("plaka: eski %v, yeni %v", e.plate(t, old), e.plate(t, recent))
	}
	run(t, "tekrar", e.at(t, 0).ClearPlates, 0)
	run(t, "dakika sonra", e.at(t, time.Minute).ClearPlates, 1)
}

// ---------------------------------------------------------------------------
// Eşzamanlılık ve kilit

// TestConcurrentWorkers runs every job from two Services at once (two pods)
// and checks each change happened once.
func TestConcurrentWorkers(t *testing.T) {
	e := setup(t)
	const n = 12
	var hikers []id.ID
	for range n {
		tf, admin := e.newTayfa(t)
		e.exec(t, `UPDATE tayfas SET created_at = $2, last_activity_at = $2 WHERE id = $1`, tf.PG(), e.base.Add(-40*day))
		h := e.seed(t, tf, "member")
		ev := e.hike(t, tf, admin, -2*time.Hour)
		e.checkin(t, ev, h, false)
		hikers = append(hikers, h)
		del, delAdmin := e.newTayfa(t)
		_, err := e.tayfas(t, -4*day).RequestDeletion(ctx, delAdmin, del)
		must(t, err)
	}

	a, b := e.at(t, 0), e.at(t, 0)
	var wg sync.WaitGroup
	for _, s := range []*Service{a, b, a, b} {
		for _, pass := range []func(context.Context) (int, error){s.CleanupTayfas, s.DeleteDueTayfas, s.AlertOverdueHikes} {
			wg.Go(func() {
				if _, err := pass(ctx); err != nil {
					t.Error(err)
				}
			})
		}
	}
	wg.Wait()
	// Whatever a locked-out pass skipped, a later run finishes.
	for _, pass := range []func(context.Context) (int, error){a.CleanupTayfas, a.DeleteDueTayfas, a.AlertOverdueHikes} {
		_, err := pass(ctx)
		must(t, err)
	}

	// Only the n old tayfas are warned; the ones waiting for deletion are
	// left alone.
	if got := len(e.events(t, EventTayfaWarned)); got != n {
		t.Errorf("tayfa.warned = %d, %d bekleniyordu", got, n)
	}
	if got := len(e.events(t, EventTayfaDeleted)); got != n {
		t.Errorf("tayfa.deleted = %d, %d bekleniyordu", got, n)
	}
	got := e.events(t, EventHikeOverdue)
	if len(got) != n {
		t.Fatalf("hike.overdue = %d, %d bekleniyordu", len(got), n)
	}
	seen := map[string]bool{}
	for _, ev := range got {
		seen[ev.payload["user_id"].(string)] = true
	}
	for _, h := range hikers {
		if !seen[h.String()] {
			t.Errorf("%s için alarm yok", h)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'tayfa.warned'`); n != len(hikers) {
		t.Errorf("tayfa.warned denetim kaydı %d", n)
	}
}

// TestLockedJobSkips: while another worker holds a job's lock, a run does
// nothing; the next run does the work.
func TestLockedJobSkips(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	_, err := e.tayfas(t, -4*day).RequestDeletion(ctx, admin, tf)
	must(t, err)

	tx, err := e.pool.Begin(ctx)
	must(t, err)
	ok, err := store.New(tx).TryJobLock(ctx, "tayfam.core.job."+JobTayfaDeletion)
	must(t, err)
	if !ok {
		t.Fatal("kilit alınamadı")
	}
	run(t, "kilitliyken", e.at(t, 0).DeleteDueTayfas, 0)
	if e.state(t, tf).deleted {
		t.Fatal("kilitliyken silindi")
	}
	must(t, tx.Rollback(ctx))
	run(t, "kilit bırakılınca", e.at(t, 0).DeleteDueTayfas, 1)
}

// TestWorkersStop: the workers run on start and return nil when the context
// is cancelled.
func TestWorkersStop(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	hiker := e.seed(t, tf, "member")
	ev := e.hike(t, tf, id.New(), -2*time.Hour)
	e.checkin(t, ev, hiker, false)

	now := e.base
	s, err := New(Config{Pool: e.pool, Log: e.log, Now: func() time.Time { return now },
		Intervals: Intervals{TayfaCleanup: time.Millisecond, TayfaDeletion: time.Millisecond, Purge: time.Millisecond, HikeAlert: time.Millisecond, Plates: time.Millisecond,
			ReportEscalation: time.Millisecond, Retention: time.Millisecond}})
	must(t, err)
	wctx, cancel := context.WithCancel(ctx)
	workers := s.Workers()
	if len(workers) != 7 {
		t.Fatalf("worker sayısı %d", len(workers))
	}
	errs := make(chan error, len(workers))
	for _, w := range workers {
		go func() { errs <- w.Run(wctx) }()
	}
	deadline := time.Now().Add(10 * time.Second)
	for !e.alertSent(t, ev, hiker) {
		if time.Now().After(deadline) {
			t.Fatal("alarm worker'ı çalışmadı")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	for range workers {
		if err := <-errs; err != nil {
			t.Errorf("worker: %v", err)
		}
	}
	if n := len(e.events(t, EventHikeOverdue)); n != 1 {
		t.Errorf("hike.overdue = %d", n)
	}
}

// TestRecordRun: every pass leaves its finish time, success and count in
// job_runs (issue #110). An older finish (a slower pod) does not overwrite a
// newer one, and a failed run keeps the last success.
func TestRecordRun(t *testing.T) {
	e := setup(t)
	type row struct {
		finished, lastOK time.Time
		ok               bool
		changed          int64
	}
	get := func() row {
		t.Helper()
		var r row
		var lastOK pgtype.Timestamptz
		must(t, e.pool.QueryRow(ctx, `SELECT finished_at, succeeded, changed, last_succeeded_at FROM job_runs WHERE job = $1`, JobTayfaCleanup).
			Scan(&r.finished, &r.ok, &r.changed, &lastOK))
		r.finished, r.lastOK = r.finished.UTC(), lastOK.Time.UTC()
		return r
	}
	t0 := e.base.Truncate(time.Microsecond)

	e.at(t, 0).recordRun(ctx, JobTayfaCleanup, 3, nil)
	if r := get(); !r.finished.Equal(t0) || !r.ok || r.changed != 3 || !r.lastOK.Equal(t0) {
		t.Fatalf("ilk çalışma = %+v", r)
	}
	e.at(t, time.Hour).recordRun(ctx, JobTayfaCleanup, 0, errors.New("bağlantı koptu"))
	if r := get(); !r.finished.Equal(t0.Add(time.Hour)) || r.ok || r.changed != 0 || !r.lastOK.Equal(t0) {
		t.Fatalf("başarısız çalışma = %+v", r)
	}
	e.at(t, 30*time.Minute).recordRun(ctx, JobTayfaCleanup, 9, nil)
	if r := get(); !r.finished.Equal(t0.Add(time.Hour)) || r.changed != 0 {
		t.Fatalf("eski bitiş yenisini ezdi: %+v", r)
	}
	if n := e.count(t, `SELECT count(*) FROM job_runs`); n != 1 {
		t.Fatalf("satır sayısı %d", n)
	}
}
