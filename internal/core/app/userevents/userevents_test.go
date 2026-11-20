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
	// admin is a superuser pool for fixtures core_app cannot write
	// (seeding with triggers off, ADR-0015).
	admin *pgxpool.Pool
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	pool := d.App
	svc, err := New(Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, pool: pool, admin: d.Admin}
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
	tx, err := e.admin.Begin(ctx)
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
		"memberships.user_id":             `SELECT count(*) FROM memberships WHERE user_id = $1`,
		"memberships.invited_by_user_id":  `SELECT count(*) FROM memberships WHERE invited_by_user_id = $1`,
		"memberships.approved_by":         `SELECT count(*) FROM memberships WHERE approved_by = $1`,
		"memberships.muted_by":            `SELECT count(*) FROM memberships WHERE muted_by = $1`,
		"tayfas":                          `SELECT count(*) FROM tayfas WHERE $1 IN (created_by, deletion_requested_by, suspended_by, deleted_by)`,
		"invites.created_by":              `SELECT count(*) FROM invites WHERE created_by = $1`,
		"audit_logs.actor_id":             `SELECT count(*) FROM audit_logs WHERE actor_id = $1`,
		"domain_events.actor_id":          `SELECT count(*) FROM domain_events WHERE actor_id = $1`,
		"announcements":                   `SELECT count(*) FROM announcements WHERE $1 IN (author_id, deleted_by)`,
		"events":                          `SELECT count(*) FROM events WHERE $1 IN (created_by, cancelled_by, deleted_by)`,
		"event_series.created_by":         `SELECT count(*) FROM event_series WHERE created_by = $1`,
		"event_rsvps":                     `SELECT count(*) FROM event_rsvps WHERE $1 IN (user_id, attendance_marked_by)`,
		"event_vehicles.user_id":          `SELECT count(*) FROM event_vehicles WHERE user_id = $1`,
		"event_vehicle_seat_requests":     `SELECT count(*) FROM event_vehicle_seat_requests WHERE user_id = $1`,
		"event_referees.user_id":          `SELECT count(*) FROM event_referees WHERE user_id = $1`,
		"event_checklist_items":           `SELECT count(*) FROM event_checklist_items WHERE $1 IN (assigned_user_id, created_by)`,
		"event_roll_call_checkins":        `SELECT count(*) FROM event_roll_call_checkins WHERE user_id = $1`,
		"event_log_entries":               `SELECT count(*) FROM event_log_entries WHERE $1 IN (user_id, deleted_by)`,
		"tayfa_vocabulary.created_by":     `SELECT count(*) FROM tayfa_vocabulary WHERE created_by = $1`,
		"event_measurements":              `SELECT count(*) FROM event_measurements WHERE $1 IN (user_id, entered_by)`,
		"event_team_members.user_id":      `SELECT count(*) FROM event_team_members WHERE user_id = $1`,
		"event_draw_entries.user_id":      `SELECT count(*) FROM event_draw_entries WHERE user_id = $1`,
		"event_score_players.user_id":     `SELECT count(*) FROM event_score_players WHERE user_id = $1`,
		"event_ranking_entries":           `SELECT count(*) FROM event_ranking_entries WHERE $1 IN (user_id, entered_by)`,
		"event_celebrations.announced_by": `SELECT count(*) FROM event_celebrations WHERE announced_by = $1`,
		"event_celebration_winners":       `SELECT count(*) FROM event_celebration_winners WHERE user_id = $1`,
		"user_badges.user_id":             `SELECT count(*) FROM user_badges WHERE user_id = $1`,
		"event_questions":                 `SELECT count(*) FROM event_questions WHERE $1 IN (author_id, answered_by, deleted_by)`,
		"event_question_votes.user_id":    `SELECT count(*) FROM event_question_votes WHERE user_id = $1`,
		"event_photos":                    `SELECT count(*) FROM event_photos WHERE $1 IN (uploader_id, deleted_by)`,
		"user_activity_records.user_id":   `SELECT count(*) FROM user_activity_records WHERE user_id = $1`,
		"questions":                       `SELECT count(*) FROM questions WHERE $1 IN (author_id, deleted_by)`,
		"answers":                         `SELECT count(*) FROM answers WHERE $1 IN (author_id, deleted_by)`,
		"answer_votes.user_id":            `SELECT count(*) FROM answer_votes WHERE user_id = $1`,
		"polls":                           `SELECT count(*) FROM polls WHERE $1 IN (created_by, deleted_by)`,
		"poll_votes.user_id":              `SELECT count(*) FROM poll_votes WHERE user_id = $1`,
		"friendships":                     `SELECT count(*) FROM friendships WHERE $1 IN (user_low_id, user_high_id, requested_by)`,
		"user_blocks":                     `SELECT count(*) FROM user_blocks WHERE $1 IN (blocker_id, blocked_id)`,
		"friend_scores":                   `SELECT count(*) FROM friend_scores WHERE $1 IN (user_low_id, user_high_id)`,
		"friend_suggestions":              `SELECT count(*) FROM friend_suggestions WHERE $1 IN (user_id, suggested_user_id)`,
		"social_settings":                 `SELECT count(*) FROM social_settings WHERE user_id = $1`,
		"user_availability":               `SELECT count(*) FROM user_availability WHERE user_id = $1`,
		"reports":                         `SELECT count(*) FROM reports WHERE $1 IN (reporter_id, subject_user_id, handled_by, platform_handled_by)`,
		"user_profiles (kişisel)":         `SELECT count(*) FROM user_profiles WHERE user_id = $1 AND (display_name IS NOT NULL OR avatar_upload_id IS NOT NULL OR deleted_at IS NULL)`,
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

	// u's question and answer stay with an empty author; u's votes go and
	// the counts drop; a vote by someone else on u's answer stays.
	ownQ, otherQ, ownA, otherA := id.New(), id.New(), id.New(), id.New()
	e.seed(t, `INSERT INTO questions (id, tayfa_id, author_id, title, client_request_id) VALUES ($1, $3, $4, 'Kamp ocağı önerisi?', $5), ($2, $3, $6, 'Hangi çadırı alalım?', $7)`,
		ownQ.PG(), otherQ.PG(), t2.PG(), u.PG(), id.New().PG(), other.PG(), id.New().PG())
	e.seed(t, `INSERT INTO answers (id, question_id, tayfa_id, author_id, body, vote_count, client_request_id, deleted_at, deleted_by) VALUES ($1, $3, $4, $5, 'Benim cevabım', 1, $6, NULL, NULL), ($2, $3, $4, $7, 'Gizlenen cevap', 1, $8, now(), $5)`,
		ownA.PG(), otherA.PG(), otherQ.PG(), t2.PG(), u.PG(), id.New().PG(), other.PG(), id.New().PG())
	e.seed(t, `INSERT INTO answer_votes (answer_id, user_id) VALUES ($1, $2), ($3, $4)`, ownA.PG(), other.PG(), otherA.PG(), u.PG())

	// u's events stay with an empty organizer; u's RSVPs and vehicle (plate)
	// go; list items stay without u (issue #241).
	ownEvent, otherEvent, campEvent := id.New(), id.New(), id.New()
	for _, x := range []struct {
		ev, by id.ID
		typ    string
	}{{ownEvent, u, "convoy"}, {otherEvent, other, "convoy"}, {campEvent, other, "camp"}} {
		e.seed(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, created_by, status, cancelled_at, cancelled_by)
			VALUES ($1, $2, $3, 'online', 'Etkinlik', now() + interval '1 day', now() + interval '2 days', 'https://example.com', $4, 'cancelled', now(), $5)`,
			x.ev.PG(), t2.PG(), x.typ, x.by.PG(), u.PG())
	}
	e.seed(t, `INSERT INTO event_series (id, tayfa_id, rrule, timezone, dtstart, created_by) VALUES ($1, $2, 'FREQ=WEEKLY', 'Europe/Istanbul', now(), $3)`, id.New().PG(), t2.PG(), u.PG())
	e.seed(t, `INSERT INTO event_rsvps (event_id, user_id, status) VALUES ($1, $2, 'going')`, otherEvent.PG(), u.PG())
	e.seed(t, `INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method, attendance_marked_by, attendance_marked_at) VALUES ($1, $2, 'going', 'attended', 'organizer', $3, now())`, otherEvent.PG(), other.PG(), u.PG())
	// u's vehicle goes with the seat request another member made to it;
	// u's own request to another member's vehicle goes, the vehicle stays
	// (issue #240).
	ownVehicle, otherVehicle := id.New(), id.New()
	e.seed(t, `INSERT INTO event_vehicles (id, event_id, component_key, user_id, make, plate, seats) VALUES ($1, $3, 'vehicles', $4, 'Ford', '35 ABC 123', 2), ($2, $5, 'vehicles', $6, 'Fiat', NULL, 2)`,
		ownVehicle.PG(), otherVehicle.PG(), otherEvent.PG(), u.PG(), ownEvent.PG(), other.PG())
	e.seed(t, `INSERT INTO event_vehicle_seat_requests (id, event_id, component_key, user_id, event_vehicle_id) VALUES ($1, $3, 'vehicles', $4, $5), ($2, $6, 'vehicles', $7, $8)`,
		id.New().PG(), id.New().PG(), otherEvent.PG(), other.PG(), ownVehicle.PG(), ownEvent.PG(), u.PG(), otherVehicle.PG())
	// u's referee assignment goes; another member's stays (issue #238).
	e.seed(t, `INSERT INTO event_referees (id, event_id, user_id) VALUES ($1, $3, $4), ($2, $3, $5)`, id.New().PG(), id.New().PG(), otherEvent.PG(), u.PG(), other.PG())
	e.seed(t, `INSERT INTO event_checklist_items (id, event_id, component_key, name, assigned_user_id, created_by) VALUES ($1, $2, 'gear', 'Çadır', $3, $3)`, id.New().PG(), campEvent.PG(), u.PG())

	// u's poll stays with an empty creator; u's votes go, other votes stay.
	// One poll u deleted loses deleted_by.
	ownPoll, deletedPoll, optA, optB := id.New(), id.New(), id.New(), id.New()
	e.seed(t, `WITH p AS (INSERT INTO polls (id, tayfa_id, event_id, created_by, question, anonymous) VALUES ($1, $2, $3, $4, 'Hangi hafta sonu?', true) RETURNING id)
		INSERT INTO poll_options (id, poll_id, text, position) SELECT $5::uuid, id, 'Bu hafta', 0 FROM p UNION ALL SELECT $6::uuid, id, 'Gelecek hafta', 1 FROM p`,
		ownPoll.PG(), t2.PG(), ownEvent.PG(), u.PG(), optA.PG(), optB.PG())
	e.seed(t, `WITH p AS (INSERT INTO polls (id, tayfa_id, created_by, question, deleted_at, deleted_by) VALUES ($1, $2, $3, 'Silinen', now(), $4) RETURNING id)
		INSERT INTO poll_options (id, poll_id, text, position) SELECT $5::uuid, id, 'Evet', 0 FROM p UNION ALL SELECT $6::uuid, id, 'Hayır', 1 FROM p`,
		deletedPoll.PG(), t2.PG(), other.PG(), u.PG(), id.New().PG(), id.New().PG())
	e.seed(t, `INSERT INTO poll_votes (poll_id, option_id, user_id) VALUES ($1, $2, $3), ($1, $4, $5)`, ownPoll.PG(), optA.PG(), u.PG(), optB.PG(), other.PG())

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
	if n := e.count(t, `SELECT count(*) FROM event_referees WHERE event_id = $1 AND user_id = $2`, otherEvent.PG(), other.PG()); n != 1 {
		t.Fatalf("başkasının hakem ataması = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_vehicles WHERE id = $1 AND user_id = $2`, otherVehicle.PG(), other.PG()); n != 1 {
		t.Fatalf("başkasının aracı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_vehicles`) + e.count(t, `SELECT count(*) FROM event_vehicle_seat_requests`); n != 1 {
		t.Fatalf("silinen kullanıcının aracı ya da koltuk istekleri kaldı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM announcements WHERE (id = $1 AND author_id IS NULL AND title = 'Duyuru') OR (id = $2 AND author_id = $3 AND deleted_at IS NOT NULL AND deleted_by IS NULL)`, ownAnn.PG(), hiddenAnn.PG(), other.PG()); n != 2 {
		t.Fatalf("duyurular = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM questions WHERE (id = $1 AND author_id IS NULL AND title <> '') OR (id = $2 AND author_id = $3)`, ownQ.PG(), otherQ.PG(), other.PG()); n != 2 {
		t.Fatalf("sorular = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM answers WHERE (id = $1 AND author_id IS NULL AND vote_count = 1) OR (id = $2 AND deleted_by IS NULL AND deleted_at IS NOT NULL AND vote_count = 0)`, ownA.PG(), otherA.PG()); n != 2 {
		t.Fatalf("cevaplar = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM answers a WHERE a.vote_count <> (SELECT count(*) FROM answer_votes v WHERE v.answer_id = a.id)`); n != 0 {
		t.Fatalf("oy sayısı tutarsız: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM polls WHERE id = $1 AND created_by IS NULL AND anonymous`, ownPoll.PG()); n != 1 {
		t.Fatal("anket silindi ya da açan boşalmadı")
	}
	if n := e.count(t, `SELECT count(*) FROM poll_votes WHERE poll_id = $1 AND option_id = $2 AND user_id = $3`, ownPoll.PG(), optB.PG(), other.PG()); n != 1 {
		t.Fatal("başkasının oyu silindi")
	}
	if n := e.count(t, `SELECT count(*) FROM events WHERE id IN ($1, $2, $3)`, ownEvent.PG(), otherEvent.PG(), campEvent.PG()); n != 3 {
		t.Fatalf("etkinlikler silindi: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_rsvps WHERE event_id = $1 AND user_id = $2 AND attendance = 'attended'`, otherEvent.PG(), other.PG()); n != 1 {
		t.Fatal("başkasının katılım kaydı silindi")
	}
	if n := e.count(t, `SELECT count(*) FROM event_checklist_items WHERE event_id = $1 AND assigned_user_id IS NULL AND created_by IS NULL`, campEvent.PG()); n != 1 {
		t.Fatal("liste maddesi silindi ya da kişi alanları kaldı")
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

// eventAt seeds an in-person event of tf from start to end.
func (e *env) eventAt(t *testing.T, tf id.ID, typ string, start, end time.Time, capacity any, status string) id.ID {
	t.Helper()
	ev := id.New()
	cancelled := any(nil)
	if status == "cancelled" {
		cancelled = now
	}
	e.seed(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, meeting_lat, meeting_lng, city, district, capacity, status, cancelled_at)
		VALUES ($1, $2, $3, 'in_person', 'Etkinlik', $4, $5, 38.42, 27.14, 'İzmir', 'Konak', $6, $7, $8)`,
		ev.PG(), tf.PG(), typ, start, end, capacity, status, cancelled)
	return ev
}

func (e *env) rsvp(t *testing.T, ev, u id.ID, status string, at time.Time) {
	t.Helper()
	e.seed(t, `INSERT INTO event_rsvps (event_id, user_id, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $4)`, ev.PG(), u.PG(), status, at)
}

func (e *env) status(t *testing.T, ev, u id.ID) string {
	t.Helper()
	var s string
	must(t, e.pool.QueryRow(ctx, `SELECT status FROM event_rsvps WHERE event_id = $1 AND user_id = $2`, ev.PG(), u.PG()).Scan(&s))
	return s
}

// Deleting an account frees the user's seats: on every upcoming event they
// were going to, the oldest waitlisted answer becomes going in the same
// transaction, with an rsvp.promoted event and audit record by the system
// (issue #50). Ended, cancelled and not-going events are left alone. The
// user's roll calls, log entries (catches) and activity records go too.
func TestDeletedPromotesWaitlist(t *testing.T) {
	e := setup(t)
	u, admin, a, w1, w2 := id.New(), id.New(), id.New(), id.New(), id.New()
	tf := e.tayfa(t, admin)
	t0 := now.Add(-48 * time.Hour)
	for _, m := range []id.ID{admin, u, a, w1, w2} {
		role := "member"
		if m == admin {
			role = "admin"
		}
		e.member(t, tf, m, role, "active", t0)
	}
	soon, later := now.Add(24*time.Hour), now.Add(27*time.Hour)

	full := e.eventAt(t, tf, "general", soon, later, 2, "scheduled")
	e.rsvp(t, full, u, "going", t0)
	e.rsvp(t, full, a, "going", t0)
	e.rsvp(t, full, w2, "waitlist", t0.Add(2*time.Minute))
	e.rsvp(t, full, w1, "waitlist", t0.Add(time.Minute))

	ended := e.eventAt(t, tf, "general", now.Add(-3*time.Hour), now.Add(-time.Hour), 1, "scheduled")
	e.rsvp(t, ended, u, "going", t0)
	e.rsvp(t, ended, w1, "waitlist", t0)

	cancelled := e.eventAt(t, tf, "general", soon, later, 1, "cancelled")
	e.rsvp(t, cancelled, u, "going", t0)
	e.rsvp(t, cancelled, w1, "waitlist", t0)

	maybe := e.eventAt(t, tf, "general", soon, later, 1, "scheduled")
	e.rsvp(t, maybe, u, "maybe", t0)
	e.rsvp(t, maybe, a, "going", t0)
	e.rsvp(t, maybe, w1, "waitlist", t0)

	// Personal attendance data of u.
	hike := e.eventAt(t, tf, "hike", now.Add(-5*time.Hour), now.Add(-time.Hour), nil, "scheduled")
	e.seed(t, `INSERT INTO event_roll_call_checkins (event_id, component_key, user_id, started_at, started_received_at) VALUES ($1, 'roll_call', $2, $3, $3)`, hike.PG(), u.PG(), now.Add(-4*time.Hour))
	e.seed(t, `INSERT INTO user_activity_records (id, user_id, event_id, tayfa_id, event_type, mode, event_title, started_at, duration_minutes, attendance_method)
		VALUES ($1, $2, $3, $4, 'hike', 'in_person', 'Etkinlik', $5, 240, 'hike_start')`, id.New().PG(), u.PG(), hike.PG(), tf.PG(), now.Add(-4*time.Hour))
	fishing := e.eventAt(t, tf, "fishing", now.Add(-5*time.Hour), now.Add(-time.Hour), nil, "scheduled")
	e.seed(t, `INSERT INTO event_log_entries (id, event_id, component_key, user_id, field_values, lat, lng, location_visibility, client_request_id)
		VALUES ($1, $2, 'catches', $3, '{"species": "Levrek"}', 38.1, 26.9, 'self', $4)`,
		id.New().PG(), fishing.PG(), u.PG(), id.New().PG())
	e.seed(t, `INSERT INTO event_log_entries (id, event_id, component_key, user_id, field_values, client_request_id, deleted_at, deleted_by)
		VALUES ($1, $2, 'catches', $3, '{"species": "Çipura"}', $4, now(), $5)`,
		id.New().PG(), fishing.PG(), a.PG(), id.New().PG(), u.PG())
	// A tayfa vocabulary value u added stays, without its writer (issue
	// #244).
	e.seed(t, `INSERT INTO tayfa_vocabulary (id, tayfa_id, namespace, value, value_normalized, created_by) VALUES ($1, $2, 'fish_species', 'Levrek', 'levrek', $3)`,
		id.New().PG(), tf.PG(), u.PG())
	// Measurements (issue #245): u's personal value goes; the total u
	// entered and a's value u entered stay without their writer.
	e.seed(t, `INSERT INTO event_measurements (id, event_id, component_key, user_id, value, entered_by) VALUES ($1, $2, 'distance', NULL, 12.5, $3)`,
		id.New().PG(), hike.PG(), u.PG())
	e.seed(t, `INSERT INTO event_measurements (id, event_id, component_key, user_id, value, entered_by) VALUES ($1, $2, 'goals', $3, 2, $3)`,
		id.New().PG(), hike.PG(), u.PG())
	e.seed(t, `INSERT INTO event_measurements (id, event_id, component_key, user_id, value, entered_by) VALUES ($1, $2, 'goals', $3, 1, $4)`,
		id.New().PG(), hike.PG(), a.PG(), u.PG())
	// Rankings (issue #253): u's time goes; a's time u entered stays
	// without its writer.
	e.seed(t, `INSERT INTO event_ranking_entries (id, event_id, component_key, user_id, value, entered_by) VALUES ($1, $2, 'ranking', $3, 61.5, $4)`,
		id.New().PG(), hike.PG(), u.PG(), a.PG())
	e.seed(t, `INSERT INTO event_ranking_entries (id, event_id, component_key, user_id, value, entered_by) VALUES ($1, $2, 'ranking', $3, 63, $4)`,
		id.New().PG(), hike.PG(), a.PG(), u.PG())
	// Celebrations (issue #255): u's win goes; the celebration u announced
	// and a's win stay, without the announcer.
	e.seed(t, `INSERT INTO event_celebrations (event_id, component_key, announced_by, announced_at, updated_at) VALUES ($1, 'celebration', $2, now(), now())`,
		hike.PG(), u.PG())
	e.seed(t, `INSERT INTO event_celebration_winners (id, event_id, component_key, user_id, position) VALUES ($1, $2, 'celebration', $3, 1)`,
		id.New().PG(), hike.PG(), u.PG())
	e.seed(t, `INSERT INTO event_celebration_winners (id, event_id, component_key, user_id, position) VALUES ($1, $2, 'celebration', $3, 2)`,
		id.New().PG(), hike.PG(), a.PG())
	// Badges (issue #258; ADR-0039 §5): u's badges go, a's stay.
	e.seed(t, `INSERT INTO user_badges (id, user_id, badge_key, period_key, tayfa_id, event_id, earned_at) VALUES ($1, $3, 'test_first', 'all', NULL, NULL, now()), ($2, $4, 'test_first', 'all', NULL, NULL, now())`,
		id.New().PG(), id.New().PG(), u.PG(), a.PG())
	// Event questions (issue #256): u's questions (an anonymous one too)
	// and votes go; a's question that u marked answered and removed stays,
	// without them, with a's vote on it.
	uQuestion, aQuestion := id.New(), id.New()
	e.seed(t, `INSERT INTO event_questions (id, event_id, component_key, author_id, anonymous, body, client_request_id) VALUES ($1, $2, 'questions', $3, true, 'Anonim soru?', $4)`,
		uQuestion.PG(), hike.PG(), u.PG(), id.New().PG())
	e.seed(t, `INSERT INTO event_questions (id, event_id, component_key, author_id, anonymous, body, answered_at, answered_by, client_request_id, deleted_at, deleted_by)
		VALUES ($1, $2, 'questions', $3, false, 'Açık soru?', now(), $4, $5, now(), $4)`,
		aQuestion.PG(), hike.PG(), a.PG(), u.PG(), id.New().PG())
	e.seed(t, `INSERT INTO event_question_votes (question_id, event_id, component_key, user_id) VALUES ($1, $3, 'questions', $4), ($2, $3, 'questions', $4), ($2, $3, 'questions', $5)`,
		uQuestion.PG(), aQuestion.PG(), hike.PG(), u.PG(), a.PG())
	// Teams (issue #248): u's place goes; the team and a's place stay.
	team := id.New()
	e.seed(t, `INSERT INTO event_teams (id, event_id, component_key, position) VALUES ($1, $2, 'teams', 1)`, team.PG(), hike.PG())
	e.seed(t, `INSERT INTO event_team_members (event_id, component_key, team_id, user_id) VALUES ($1, 'teams', $2, $3), ($1, 'teams', $2, $4)`,
		hike.PG(), team.PG(), u.PG(), a.PG())
	// Draw (issue #249): the draw is fixed; u's place stays empty, a's
	// place keeps its position.
	e.seed(t, `INSERT INTO event_draws (event_id, component_key, subject, drawn_at) VALUES ($1, 'draw', 'participants', now())`, hike.PG())
	e.seed(t, `INSERT INTO event_draw_entries (id, event_id, component_key, position, user_id) VALUES ($1, $3, 'draw', 1, $4), ($2, $3, 'draw', 2, $5)`,
		id.New().PG(), id.New().PG(), hike.PG(), u.PG(), a.PG())
	// Scoreboard (issue #250): u's seat and scores stay as a deleted user;
	// a's seat is untouched.
	scoreTable, uSeat := id.New(), id.New()
	e.seed(t, `INSERT INTO event_score_tables (id, event_id, component_key, label, side, rounds, winner, result) VALUES ($1, $2, 'scoreboard', 'Masa 1', 'player', 'round', 'high', 'points')`,
		scoreTable.PG(), hike.PG())
	e.seed(t, `INSERT INTO event_score_players (id, table_id, event_id, component_key, position, user_id) VALUES ($1, $3, $4, 'scoreboard', 1, $5), ($2, $3, $4, 'scoreboard', 2, $6)`,
		uSeat.PG(), id.New().PG(), scoreTable.PG(), hike.PG(), u.PG(), a.PG())
	e.seed(t, `INSERT INTO event_scores (id, table_id, event_id, component_key, player_id, round, points) VALUES ($1, $2, $3, 'scoreboard', $4, 1, 12)`,
		id.New().PG(), scoreTable.PG(), hike.PG(), uSeat.PG())
	// Album photos (issue #206): u's own go; a's photo that u removed stays
	// without its remover.
	e.seed(t, `INSERT INTO event_photos (id, event_id, uploader_id, upload_id, caption, client_request_id) VALUES ($1, $2, $3, $4, 'Zirve', $5)`,
		id.New().PG(), hike.PG(), u.PG(), id.New().PG(), id.New().PG())
	e.seed(t, `INSERT INTO event_photos (id, event_id, uploader_id, upload_id, client_request_id, deleted_at, deleted_by) VALUES ($1, $2, $3, $4, $5, now(), $6)`,
		id.New().PG(), hike.PG(), a.PG(), id.New().PG(), id.New().PG(), u.PG())

	must(t, e.svc.Apply(ctx, Event{ID: id.New(), User: u, Deleted: true}))
	if n := e.count(t, `SELECT count(*) FROM event_photos WHERE uploader_id = $1 AND deleted_at IS NOT NULL AND deleted_by IS NULL`, a.PG()); n != 1 {
		t.Error("başkasının albüm fotoğrafı silindi ya da silen boşalmadı")
	}

	if refs := e.userRefs(t, u); len(refs) != 0 {
		t.Fatalf("core'da kullanıcıya ait kayıt kaldı: %v", refs)
	}
	if got := e.status(t, full, w1); got != "going" {
		t.Errorf("en eski bekleyen = %s, going bekleniyordu", got)
	}
	if got := e.status(t, full, w2); got != "waitlist" {
		t.Errorf("ikinci bekleyen = %s", got)
	}
	for name, ev := range map[string]id.ID{"bitmiş": ended, "iptal": cancelled, "belki": maybe} {
		if got := e.status(t, ev, w1); got != "waitlist" {
			t.Errorf("%s etkinlikte terfi oldu: %s", name, got)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM event_log_entries WHERE user_id = $1 AND deleted_at IS NOT NULL AND deleted_by IS NULL`, a.PG()); n != 1 {
		t.Error("başkasının av kaydı silindi ya da silen boşalmadı")
	}
	if n := e.count(t, `SELECT count(*) FROM tayfa_vocabulary WHERE tayfa_id = $1 AND value = 'Levrek' AND created_by IS NULL`, tf.PG()); n != 1 {
		t.Error("sözlük değeri silindi ya da ekleyen boşalmadı")
	}
	if n := e.count(t, `SELECT count(*) FROM event_measurements WHERE event_id = $1 AND entered_by IS NULL AND (user_id IS NULL OR user_id = $2)`, hike.PG(), a.PG()); n != 2 {
		t.Errorf("toplam ölçüm ya da başkasının ölçümü silindi ya da yazan boşalmadı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_ranking_entries WHERE event_id = $1 AND user_id = $2 AND entered_by IS NULL`, hike.PG(), a.PG()); n != 1 {
		t.Errorf("başkasının derecesi silindi ya da yazan boşalmadı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_celebration_winners AS w JOIN event_celebrations AS c USING (event_id, component_key)
		WHERE w.event_id = $1 AND w.user_id = $2 AND c.announced_by IS NULL`, hike.PG(), a.PG()); n != 1 {
		t.Errorf("başkasının kazananlığı ya da ilan silindi ya da ilan eden boşalmadı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM user_badges WHERE user_id = $1`, a.PG()); n != 1 {
		t.Errorf("başkasının rozeti silindi: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_questions q JOIN event_question_votes v ON v.question_id = q.id
		WHERE q.id = $1 AND q.answered_at IS NOT NULL AND q.answered_by IS NULL AND q.deleted_at IS NOT NULL AND q.deleted_by IS NULL AND v.user_id = $2`,
		aQuestion.PG(), a.PG()); n != 1 {
		t.Errorf("başkasının sorusu ya da oyu silindi ya da işaretleyen/silen boşalmadı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_team_members WHERE team_id = $1 AND user_id = $2`, team.PG(), a.PG()); n != 1 {
		t.Errorf("başkasının takımdaki yeri silindi: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_draw_entries WHERE event_id = $1 AND ((position = 1 AND user_id IS NULL) OR (position = 2 AND user_id = $2))`, hike.PG(), a.PG()); n != 2 {
		t.Errorf("kuradaki yer boşalmadı ya da kura değişti: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_score_players p JOIN event_scores s ON s.player_id = p.id WHERE p.id = $1 AND p.user_id IS NULL AND s.points = 12`, uSeat.PG()); n != 1 {
		t.Errorf("yazbozdaki yer ve skor silinmiş kullanıcı olarak kalmadı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_score_players WHERE table_id = $1 AND user_id = $2`, scoreTable.PG(), a.PG()); n != 1 {
		t.Errorf("başkasının yazbozdaki yeri değişti: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'rsvp.promoted' AND actor_id IS NULL AND tayfa_id = $1 AND subject_id = $2 AND payload->>'user_id' = $3`,
		tf.PG(), full.PG(), w1.String()); n != 1 {
		t.Fatalf("terfi olayı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'rsvp.promoted'`); n != 1 {
		t.Fatalf("terfi olayı sayısı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'rsvp.promoted' AND actor_id IS NULL AND target_id = $1`, full.PG()); n != 1 {
		t.Fatalf("terfi denetim kaydı = %d", n)
	}

	// Again: nothing changes.
	before := e.snapshot(t)
	statuses := e.status(t, full, w1) + e.status(t, full, w2)
	must(t, e.svc.Apply(ctx, Event{ID: id.New(), User: u, Deleted: true}))
	if e.snapshot(t) != before || e.status(t, full, w1)+e.status(t, full, w2) != statuses {
		t.Fatal("tekrar uygulama sonucu değiştirdi")
	}
}

// TestDeletedRemovesSocialRecords (issue #71): friendships, requests, blocks
// in both directions and the unanswered invitations the user sent go with
// the account; invitations sent to the user go with their membership rows.
func TestDeletedRemovesSocialRecords(t *testing.T) {
	e := setup(t)
	u, friend, requester, blocker, blocked, invitee, other := id.New(), id.New(), id.New(), id.New(), id.New(), id.New(), id.New()
	pair := func(a, b id.ID) (id.ID, id.ID) {
		if a.String() < b.String() {
			return a, b
		}
		return b, a
	}
	for _, f := range []struct {
		a, b   id.ID
		status string
	}{{u, friend, "accepted"}, {u, requester, "pending"}, {friend, other, "accepted"}} {
		low, high := pair(f.a, f.b)
		var acc any
		if f.status == "accepted" {
			acc = now
		}
		e.seed(t, `INSERT INTO friendships (user_low_id, user_high_id, status, requested_by, accepted_at) VALUES ($1, $2, $3, $4, $5)`,
			low.PG(), high.PG(), f.status, f.b.PG(), acc)
	}
	e.seed(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2), ($3, $4), ($5, $6)`,
		u.PG(), blocked.PG(), blocker.PG(), u.PG(), other.PG(), friend.PG())
	// Issue #208: scores, suggestions both ways (dismissed too) and settings.
	for _, p := range [][2]id.ID{{u, other}, {friend, other}} {
		low, high := pair(p[0], p[1])
		e.seed(t, `INSERT INTO friend_scores (user_low_id, user_high_id, score, shared_count, last_shared_at, computed_at) VALUES ($1, $2, 1.5, 3, $3, $3)`,
			low.PG(), high.PG(), now)
	}
	e.seed(t, `INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at, dismissed_at) VALUES ($1, $2, $5, NULL), ($3, $1, $5, $5), ($4, $3, $5, NULL)`,
		u.PG(), other.PG(), friend.PG(), requester.PG(), now)
	e.seed(t, `INSERT INTO social_settings (user_id, profiling_enabled) VALUES ($1, false), ($2, false)`, u.PG(), other.PG())
	// "Boşum" days (issue #207): the user's go, a friend's stay.
	e.seed(t, `INSERT INTO user_availability (id, user_id, day, slots, note) VALUES
		($1, $2, (now() AT TIME ZONE 'Europe/Istanbul')::date, '{evening}', 'not'),
		($3, $2, (now() AT TIME ZONE 'Europe/Istanbul')::date + 3, '{all_day}', NULL),
		($4, $5, (now() AT TIME ZONE 'Europe/Istanbul')::date, '{morning}', NULL)`,
		id.New().PG(), u.PG(), id.New().PG(), id.New().PG(), friend.PG())

	admin := id.New()
	tf := e.tayfa(t, admin)
	e.member(t, tf, admin, "admin", "active", now)
	e.member(t, tf, u, "member", "active", now)
	// u invited invitee and was invited elsewhere.
	e.seed(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, invited_by_user_id) VALUES ($1, $2, 'member', 'invited', 'direct_invite', $3)`,
		tf.PG(), invitee.PG(), u.PG())
	tf2 := e.tayfa(t, admin)
	e.member(t, tf2, admin, "admin", "active", now)
	e.seed(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, invited_by_user_id) VALUES ($1, $2, 'member', 'invited', 'direct_invite', $3)`,
		tf2.PG(), u.PG(), admin.PG())

	ev := Event{ID: id.New(), User: u, Deleted: true}
	must(t, e.svc.Apply(ctx, ev))
	if refs := e.userRefs(t, u); len(refs) != 0 {
		t.Fatalf("kalan kayıtlar: %v", refs)
	}
	if got := e.role(t, tf, invitee); got != "none" {
		t.Fatalf("gönderilen davet = %s", got)
	}
	// Other people's records are untouched.
	if n := e.count(t, `SELECT count(*) FROM friendships`); n != 1 {
		t.Fatalf("başkalarının arkadaşlığı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM user_blocks`); n != 1 {
		t.Fatalf("başkalarının engeli = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM friend_scores`) + e.count(t, `SELECT count(*) FROM friend_suggestions`) +
		e.count(t, `SELECT count(*) FROM social_settings`); n != 3 {
		t.Fatalf("başkalarının puan, öneri ve ayarları = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM user_availability WHERE user_id = $1`, friend.PG()); n != 1 {
		t.Fatalf("başkalarının boş vakti = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'tayfa.invitation_cancelled' AND payload->>'user_id' = $1 AND actor_id IS NULL`, invitee.String()); n != 1 {
		t.Fatalf("davet iptal olayı = %d", n)
	}
	before := e.snapshot(t)
	must(t, e.svc.Apply(ctx, ev))
	if e.snapshot(t) != before {
		t.Fatal("tekrar uygulama durumu değiştirdi")
	}
}

func TestProfileCarriesActivityVisibility(t *testing.T) {
	e := setup(t)
	u := id.New()
	ev := eventIDs(3)
	vis := func() string {
		t.Helper()
		var v string
		must(t, e.pool.QueryRow(ctx, `SELECT activity_visibility FROM user_profiles WHERE user_id = $1`, u.PG()).Scan(&v))
		return v
	}
	// A new copy without the field gets the default.
	must(t, e.svc.Apply(ctx, profile(ev[0], u, "Ece")))
	if v := vis(); v != VisibilityTayfa {
		t.Fatalf("varsayılan = %s", v)
	}
	must(t, e.svc.Apply(ctx, Event{ID: ev[1], User: u, Profile: &Profile{DisplayName: "Ece", ActivityVisibility: VisibilityPrivate}}))
	if v := vis(); v != VisibilityPrivate {
		t.Fatalf("görünürlük = %s", v)
	}
	// An event without the field keeps the setting.
	must(t, e.svc.Apply(ctx, profile(ev[2], u, "Ece Y")))
	if v := vis(); v != VisibilityPrivate {
		t.Fatalf("alansız olay ayarı değiştirdi: %s", v)
	}
	var verr *apperr.ValidationError
	if err := e.svc.Apply(ctx, Event{ID: id.New(), User: u, Profile: &Profile{DisplayName: "Ece", ActivityVisibility: "friends"}}); !errors.As(err, &verr) {
		t.Fatalf("geçersiz görünürlük: %v", err)
	}
}

// TestDeletedClearsReports: reports stay after the account's deletion, but
// the user's ID in them (reporter, reported, handler) goes (v14
// "Kullanıcı → yazdığı içerik: SET NULL").
func TestDeletedClearsReports(t *testing.T) {
	e := setup(t)
	u, other := id.New(), id.New()
	tf := e.tayfa(t, other)
	must(t, e.svc.Apply(ctx, profile(id.New(), u, "Deniz")))
	byU, aboutU, handledByU := id.New(), id.New(), id.New()
	e.seed(t, `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, subject_user_id, reason) VALUES
		($1, $3, $4, 'user', $5, $5, 'spam'), ($2, $3, $5, 'user', $4, $4, 'spam')`,
		byU.PG(), aboutU.PG(), tf.PG(), u.PG(), other.PG())
	e.seed(t, `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, reason, status, resolution, handled_by, handled_at)
		VALUES ($1, $2, $3, 'user', $5, 'other', 'resolved', 'dismissed', $4, now())`, handledByU.PG(), tf.PG(), other.PG(), u.PG(), id.New().PG())

	must(t, e.svc.Apply(ctx, Event{ID: id.New(), User: u, Deleted: true}))
	if refs := e.userRefs(t, u); len(refs) != 0 {
		t.Fatalf("kalan kayıtlar: %v", refs)
	}
	if n := e.count(t, `SELECT count(*) FROM reports`); n != 3 {
		t.Fatalf("şikâyetler silindi: %d", n)
	}
}
