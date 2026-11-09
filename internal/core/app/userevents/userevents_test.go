package userevents

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var (
	ctx = context.Background()
	now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
)

type env struct {
	svc  *Service
	pool *pgxpool.Pool
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, coredb.Migrations())
	svc, err := New(Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, pool: pool}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seed runs setup SQL with triggers off, to build a state directly (the
// test connection is a superuser). Apply itself always runs with triggers.
func (e *env) seed(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, sql, args...)
	must(t, err)
	must(t, tx.Commit(ctx))
}

var codeSeq int

func (e *env) tayfa(t *testing.T, createdBy id.ID) id.ID {
	t.Helper()
	codeSeq++
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	code := []byte("TEST")
	for n := codeSeq; len(code) < 8; n /= len(alphabet) {
		code = append(code, alphabet[n%len(alphabet)])
	}
	tf := id.New()
	e.seed(t, `INSERT INTO tayfas (id, name, name_normalized, join_code, created_by) VALUES ($1, 'Kamp Tayfası', 'kamp tayfası', $2, $3)`,
		tf.PG(), string(code), createdBy.PG())
	return tf
}

// member adds a membership; joined orders active members by join time.
func (e *env) member(t *testing.T, tf, u id.ID, role, status string, joined time.Time) {
	t.Helper()
	var at any
	if status == "active" {
		at = joined
	}
	e.seed(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at) VALUES ($1, $2, $3, $4, 'code', $5)`,
		tf.PG(), u.PG(), role, status, at)
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) role(t *testing.T, tf, u id.ID) string {
	t.Helper()
	var r string
	if err := e.pool.QueryRow(ctx, `SELECT status || '/' || role FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), u.PG()).Scan(&r); err != nil {
		return "none"
	}
	return r
}

func profile(eid, user id.ID, name string) Event {
	return Event{ID: eid, User: user, Profile: &Profile{DisplayName: name}}
}

// eventIDs returns n increasing event IDs.
func eventIDs(n int) []id.ID {
	out := make([]id.ID, n)
	var prev id.ID
	for i := range out {
		prev = id.After(prev)
		out[i] = prev
	}
	return out
}

func TestProfileCopyIsIdempotentAndOrdered(t *testing.T) {
	e := setup(t)
	u := id.New()
	ev := eventIDs(3)
	get := func() store.GetUserProfileRow {
		t.Helper()
		p, err := store.New(e.pool).GetUserProfile(ctx, u.PG())
		must(t, err)
		return p
	}

	must(t, e.svc.Apply(ctx, profile(ev[1], u, "İkinci Ad")))
	must(t, e.svc.Apply(ctx, profile(ev[1], u, "İkinci Ad"))) // same event again
	must(t, e.svc.Apply(ctx, profile(ev[0], u, "Eski Ad")))   // older, arrives late
	if p := get(); p.DisplayName.String != "İkinci Ad" || id.ID(p.SourceEventID.Bytes) != ev[1] {
		t.Fatalf("profil = %+v", p)
	}
	avatar := id.New()
	must(t, e.svc.Apply(ctx, Event{ID: ev[2], User: u, Profile: &Profile{DisplayName: "Yeni Ad", AvatarUploadID: avatar}}))
	if p := get(); p.DisplayName.String != "Yeni Ad" || id.ID(p.AvatarUploadID.Bytes) != avatar {
		t.Fatalf("profil = %+v", p)
	}
}

func TestApplyRejectsMalformed(t *testing.T) {
	e := setup(t)
	u, ev := id.New(), id.New()
	for name, in := range map[string]Event{
		"olay kimliği yok":  {User: u, Deleted: true},
		"kullanıcı yok":     {ID: ev, Deleted: true},
		"tür yok":           {ID: ev, User: u},
		"iki tür":           {ID: ev, User: u, Deleted: true, Profile: &Profile{DisplayName: "Ad"}},
		"boş ad":            profile(ev, u, ""),
		"41 karakter":       profile(ev, u, strings.Repeat("a", 41)),
		"kontrol karakteri": profile(ev, u, "Ali\u0007"),
		"fazla boşluk":      profile(ev, u, " Ali  Veli"),
	} {
		t.Run(name, func(t *testing.T) {
			var v *apperr.ValidationError
			if err := e.svc.Apply(ctx, in); !errors.As(err, &v) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if n := e.count(t, `SELECT count(*) FROM user_profiles`); n != 0 {
		t.Fatal("geçersiz olay kopyaya yazıldı")
	}
}

// userRefs counts every place core still refers to user, apart from the
// tombstone and IDs of past audit targets and event subjects.
func (e *env) userRefs(t *testing.T, u id.ID) map[string]int {
	t.Helper()
	refs := map[string]int{}
	for name, sql := range map[string]string{
		"memberships.user_id":            `SELECT count(*) FROM memberships WHERE user_id = $1`,
		"memberships.invited_by_user_id": `SELECT count(*) FROM memberships WHERE invited_by_user_id = $1`,
		"memberships.approved_by":        `SELECT count(*) FROM memberships WHERE approved_by = $1`,
		"memberships.muted_by":           `SELECT count(*) FROM memberships WHERE muted_by = $1`,
		"tayfas":                         `SELECT count(*) FROM tayfas WHERE $1 IN (created_by, deletion_requested_by, suspended_by, deleted_by)`,
		"invites.created_by":             `SELECT count(*) FROM invites WHERE created_by = $1`,
		"audit_logs.actor_id":            `SELECT count(*) FROM audit_logs WHERE actor_id = $1`,
		"domain_events.actor_id":         `SELECT count(*) FROM domain_events WHERE actor_id = $1`,
		"announcements":                  `SELECT count(*) FROM announcements WHERE $1 IN (author_id, deleted_by)`,
		"events":                         `SELECT count(*) FROM events WHERE $1 IN (created_by, cancelled_by, deleted_by)`,
		"event_series.created_by":        `SELECT count(*) FROM event_series WHERE created_by = $1`,
		"event_rsvps":                    `SELECT count(*) FROM event_rsvps WHERE $1 IN (user_id, attendance_marked_by)`,
		"event_convoy_vehicles.user_id":  `SELECT count(*) FROM event_convoy_vehicles WHERE user_id = $1`,
		"event_camp_items":               `SELECT count(*) FROM event_camp_items WHERE $1 IN (assigned_user_id, created_by)`,
		"user_profiles (kişisel)":        `SELECT count(*) FROM user_profiles WHERE user_id = $1 AND (display_name IS NOT NULL OR avatar_upload_id IS NOT NULL OR deleted_at IS NULL)`,
	} {
		if n := e.count(t, sql, u.PG()); n != 0 {
			refs[name] = n
		}
	}
	return refs
}

// snapshot is the state Apply changes, to compare a repeated apply.
func (e *env) snapshot(t *testing.T) string {
	t.Helper()
	var s string
	must(t, e.pool.QueryRow(ctx, `SELECT concat_ws('|',
		(SELECT string_agg(concat_ws(':', tayfa_id, user_id, role, status, invited_by_user_id, approved_by, muted_by, muted_until, updated_at), ',' ORDER BY tayfa_id, user_id) FROM memberships),
		(SELECT string_agg(concat_ws(':', id, created_by, deletion_requested_at, deletion_requested_by, deletion_scheduled_for, updated_at), ',' ORDER BY id) FROM tayfas),
		(SELECT count(*) FROM audit_logs), (SELECT count(*) FROM domain_events),
		(SELECT string_agg(concat_ws(':', user_id, display_name, avatar_upload_id, deleted_at), ',' ORDER BY user_id) FROM user_profiles))`).Scan(&s))
	return s
}

// The acceptance test of issue #38: after user.deleted no personal record of
// the user is left in core, and the same event twice changes nothing.
func TestDeletedRemovesPersonalRecords(t *testing.T) {
	e := setup(t)
	u := id.New()
	ev := eventIDs(3)
	must(t, e.svc.Apply(ctx, profile(ev[0], u, "Silinecek Kişi")))
	t0 := now.Add(-time.Hour)

	// T1: u is the only admin. The oldest moderator succeeds, even though a
	// member joined earlier.
	t1 := e.tayfa(t, u)
	early, modOld, modNew := id.New(), id.New(), id.New()
	e.member(t, t1, u, "admin", "active", t0)
	e.member(t, t1, early, "member", "active", t0.Add(time.Minute))
	e.member(t, t1, modOld, "moderator", "active", t0.Add(2*time.Minute))
	e.member(t, t1, modNew, "moderator", "active", t0.Add(3*time.Minute))

	// T2: u is a member; another admin; u invited, approved and muted others.
	other := id.New()
	t2 := e.tayfa(t, other)
	e.member(t, t2, other, "admin", "active", t0)
	e.member(t, t2, u, "moderator", "active", t0)
	invited, approved, muted := id.New(), id.New(), id.New()
	e.seed(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, invited_by_user_id) VALUES ($1, $2, 'member', 'invited', 'direct_invite', $3)`, t2.PG(), invited.PG(), u.PG())
	e.seed(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at, approved_by) VALUES ($1, $2, 'member', 'active', 'code', now(), $3)`, t2.PG(), approved.PG(), u.PG())
	e.seed(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at, muted_until, muted_by) VALUES ($1, $2, 'member', 'active', 'code', now(), now() + interval '1 day', $3)`, t2.PG(), muted.PG(), u.PG())
	e.seed(t, `INSERT INTO invites (id, tayfa_id, token_hash, created_by, expires_at) VALUES ($1, $2, sha256('x'::bytea), $3, now() + interval '1 day')`, id.New().PG(), t2.PG(), u.PG())
	e.seed(t, `INSERT INTO audit_logs (id, tayfa_id, actor_id, action) VALUES ($1, $2, $3, 'member.muted')`, id.New().PG(), t2.PG(), u.PG())
	e.seed(t, `INSERT INTO domain_events (id, type, actor_id, tayfa_id) VALUES ($1, 'membership.changed', $2, $3)`, id.New().PG(), u.PG(), t2.PG())
	// u's announcement stays with an empty author; one u hid keeps its
	// content and loses deleted_by.
	ownAnn, hiddenAnn := id.New(), id.New()
	e.seed(t, `INSERT INTO announcements (id, tayfa_id, author_id, title, client_request_id) VALUES ($1, $2, $3, 'Duyuru', $4)`, ownAnn.PG(), t2.PG(), u.PG(), id.New().PG())
	e.seed(t, `INSERT INTO announcements (id, tayfa_id, author_id, title, client_request_id, deleted_at, deleted_by) VALUES ($1, $2, $3, 'Gizlenen', $4, now(), $5)`, hiddenAnn.PG(), t2.PG(), other.PG(), id.New().PG(), u.PG())

	// u's events stay with an empty organizer; u's RSVPs and vehicle (plate)
	// go; camp items stay without u.
	ownEvent, otherEvent, campEvent := id.New(), id.New(), id.New()
	for _, x := range []struct {
		ev, by id.ID
		typ    string
	}{{ownEvent, u, "convoy"}, {otherEvent, other, "convoy"}, {campEvent, other, "camp"}} {
		e.seed(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, created_by, status, cancelled_at, cancelled_by)
			VALUES ($1, $2, $3, 'online', 'Etkinlik', now() + interval '1 day', now() + interval '2 days', 'https://example.com', $4, 'cancelled', now(), $5)`,
			x.ev.PG(), t2.PG(), x.typ, x.by.PG(), u.PG())
	}
	e.seed(t, `INSERT INTO event_series (id, tayfa_id, rrule, timezone, created_by) VALUES ($1, $2, 'FREQ=WEEKLY', 'Europe/Istanbul', $3)`, id.New().PG(), t2.PG(), u.PG())
	e.seed(t, `INSERT INTO event_convoy (event_id) VALUES ($1), ($2)`, ownEvent.PG(), otherEvent.PG())
	e.seed(t, `INSERT INTO event_camp (event_id) VALUES ($1)`, campEvent.PG())
	e.seed(t, `INSERT INTO event_rsvps (event_id, user_id, status) VALUES ($1, $2, 'going')`, otherEvent.PG(), u.PG())
	e.seed(t, `INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_marked_by, attendance_marked_at) VALUES ($1, $2, 'going', 'attended', $3, now())`, otherEvent.PG(), other.PG(), u.PG())
	e.seed(t, `INSERT INTO event_convoy_vehicles (id, event_id, user_id, make, plate) VALUES ($1, $2, $3, 'Ford', '35 ABC 123')`, id.New().PG(), otherEvent.PG(), u.PG())
	e.seed(t, `INSERT INTO event_camp_items (id, event_id, name, assigned_user_id, created_by) VALUES ($1, $2, 'Çadır', $3, $3)`, id.New().PG(), campEvent.PG(), u.PG())

	// T3: u's pending application. T4: u is the only member left.
	t3 := e.tayfa(t, other)
	e.member(t, t3, other, "admin", "active", t0)
	e.member(t, t3, u, "member", "pending", t0)
	t4 := e.tayfa(t, u)
	e.member(t, t4, u, "admin", "active", t0)
	e.member(t, t4, id.New(), "member", "pending", t0)

	must(t, e.svc.Apply(ctx, Event{ID: ev[1], User: u, Deleted: true}))

	if refs := e.userRefs(t, u); len(refs) != 0 {
		t.Fatalf("core'da kullanıcıya ait kayıt kaldı: %v", refs)
	}
	if n := e.count(t, `SELECT count(*) FROM announcements WHERE (id = $1 AND author_id IS NULL AND title = 'Duyuru') OR (id = $2 AND author_id = $3 AND deleted_at IS NOT NULL AND deleted_by IS NULL)`, ownAnn.PG(), hiddenAnn.PG(), other.PG()); n != 2 {
		t.Fatalf("duyurular = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM events WHERE id IN ($1, $2, $3)`, ownEvent.PG(), otherEvent.PG(), campEvent.PG()); n != 3 {
		t.Fatalf("etkinlikler silindi: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_rsvps WHERE event_id = $1 AND user_id = $2 AND attendance = 'attended'`, otherEvent.PG(), other.PG()); n != 1 {
		t.Fatal("başkasının katılım kaydı silindi")
	}
	if n := e.count(t, `SELECT count(*) FROM event_camp_items WHERE event_id = $1`, campEvent.PG()); n != 1 {
		t.Fatal("kamp malzemesi silindi")
	}
	if got := e.role(t, t1, modOld); got != "active/admin" {
		t.Fatalf("halef = %s", got)
	}
	for _, x := range []id.ID{early, modNew} {
		if got := e.role(t, t1, x); got == "active/admin" {
			t.Fatal("yanlış halef")
		}
	}
	if got := e.role(t, t2, muted); got != "active/member" {
		t.Fatalf("susturulan üye = %s", got)
	}
	if n := e.count(t, `SELECT count(*) FROM memberships WHERE tayfa_id = $1 AND user_id = $2 AND muted_until IS NOT NULL AND muted_by IS NULL`, t2.PG(), muted.PG()); n != 1 {
		t.Fatal("susturma süresi korunmalı, susturan boşalmalı")
	}
	// T4 waits 72 hours for deletion, requested by nobody.
	var reqAt, schedFor time.Time
	var reqBy *string
	must(t, e.pool.QueryRow(ctx, `SELECT deletion_requested_at, deletion_requested_by::text, deletion_scheduled_for FROM tayfas WHERE id = $1`, t4.PG()).Scan(&reqAt, &reqBy, &schedFor))
	if !reqAt.Equal(now) || reqBy != nil || !schedFor.Equal(now.Add(tayfa.DeletionDelay)) {
		t.Fatalf("silme beklemesi = %v %v %v", reqAt, reqBy, schedFor)
	}
	// Tayfas with members left are not scheduled.
	if n := e.count(t, `SELECT count(*) FROM tayfas WHERE id IN ($1, $2, $3) AND deletion_requested_at IS NOT NULL`, t1.PG(), t2.PG(), t3.PG()); n != 0 {
		t.Fatal("üyesi olan tayfa silme beklemesine alındı")
	}
	// Events for chat: the removals, the succession and the deletion wait,
	// all without an actor.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'membership.changed' AND actor_id IS NULL AND subject_id = $1 AND payload->>'change' = 'account_deleted'`, u.PG()); n != 4 {
		t.Fatalf("üyelik olayları = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'membership.changed' AND subject_id = $1 AND payload->>'change' = 'role_changed' AND payload->>'role' = 'admin'`, modOld.PG()); n != 1 {
		t.Fatal("halef olayı yok")
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'tayfa.deletion_requested' AND tayfa_id = $1 AND actor_id IS NULL`, t4.PG()); n != 1 {
		t.Fatal("silme beklemesi olayı yok")
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE payload::text LIKE '%Silinecek%'`); n != 0 {
		t.Fatal("ad olaya girdi")
	}

	// The same event again, and a newer duplicate: nothing changes.
	before := e.snapshot(t)
	must(t, e.svc.Apply(ctx, Event{ID: ev[1], User: u, Deleted: true}))
	must(t, e.svc.Apply(ctx, Event{ID: ev[2], User: u, Deleted: true}))
	if after := e.snapshot(t); after != before {
		t.Fatalf("tekrar uygulama sonucu değiştirdi:\n%s\n%s", before, after)
	}
}

// A profile event that arrives after the deletion, older or newer, cannot
// bring the profile back.
func TestProfileAfterDeletionStaysDeleted(t *testing.T) {
	e := setup(t)
	u := id.New()
	ev := eventIDs(3)
	must(t, e.svc.Apply(ctx, Event{ID: ev[1], User: u, Deleted: true}))
	must(t, e.svc.Apply(ctx, profile(ev[0], u, "Geç Gelen")))
	must(t, e.svc.Apply(ctx, profile(ev[2], u, "Sonraki")))
	if refs := e.userRefs(t, u); len(refs) != 0 {
		t.Fatalf("silinen profil geri geldi: %v", refs)
	}
	if n := e.count(t, `SELECT count(*) FROM user_profiles WHERE user_id = $1 AND deleted_at IS NOT NULL`, u.PG()); n != 1 {
		t.Fatal("mezar taşı yok")
	}
}

// A tayfa already waiting for deletion keeps its request; a deleted tayfa is
// left alone.
func TestDeletedKeepsExistingDeletionRequest(t *testing.T) {
	e := setup(t)
	u := id.New()
	tf := e.tayfa(t, u)
	e.member(t, tf, u, "admin", "active", now.Add(-time.Hour))
	requested := now.Add(-time.Hour)
	e.seed(t, `UPDATE tayfas SET deletion_requested_at = $2, deletion_requested_by = $3, deletion_scheduled_for = $4 WHERE id = $1`,
		tf.PG(), requested, u.PG(), requested.Add(tayfa.DeletionDelay))
	gone := e.tayfa(t, u)
	e.member(t, gone, u, "admin", "active", now.Add(-time.Hour))
	e.seed(t, `UPDATE tayfas SET deleted_at = now() WHERE id = $1`, gone.PG())

	must(t, e.svc.Apply(ctx, Event{ID: id.New(), User: u, Deleted: true}))
	var sched time.Time
	must(t, e.pool.QueryRow(ctx, `SELECT deletion_scheduled_for FROM tayfas WHERE id = $1`, tf.PG()).Scan(&sched))
	if !sched.Equal(requested.Add(tayfa.DeletionDelay)) {
		t.Fatalf("mevcut silme talebi değişti: %v", sched)
	}
	if n := e.count(t, `SELECT count(*) FROM tayfas WHERE id = $1 AND deletion_requested_at IS NOT NULL`, gone.PG()); n != 0 {
		t.Fatal("silinmiş tayfa silme beklemesine alındı")
	}
	if refs := e.userRefs(t, u); len(refs) != 0 {
		t.Fatalf("kayıt kaldı: %v", refs)
	}
}
