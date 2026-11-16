package jobs

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// instantTayfa writes an instant tayfa (ADR-0032) created at base +
// created, with its organizer as admin and one event ending at base + ends;
// cancelled, when not zero, cancels the event at base + cancelled.
func (e *env) instantTayfa(t *testing.T, created, ends, cancelled time.Duration) (tf, ev id.ID) {
	t.Helper()
	tf, ev, organizer := id.New(), id.New(), id.New()
	code := make([]byte, 8)
	_, _ = rand.Read(code)
	for i := range code {
		code[i] = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"[code[i]&31]
	}
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO tayfas (id, name, name_normalized, join_code, kind, created_by, created_at, last_activity_at)
		VALUES ($1, 'Akşam yemeği', 'akşam yemeği', $2, 'instant', $3, $4, $4)`, tf.PG(), string(code), organizer.PG(), e.base.Add(created))
	must(t, err)
	must(t, store.New(tx).CreateMembership(ctx, store.CreateMembershipParams{
		TayfaID: tf.PG(), UserID: organizer.PG(), Role: "admin", Status: "active", JoinedVia: "creator",
		JoinedAt: pgtype.Timestamptz{Time: e.base.Add(created), Valid: true},
	}))
	must(t, tx.Commit(ctx))
	e.exec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, created_by)
		VALUES ($1, $2, 'general', 'online', 'Akşam yemeği', $3, $4, 'https://example.org/x', $5)`,
		ev.PG(), tf.PG(), e.base.Add(ends-2*time.Hour), e.base.Add(ends), organizer.PG())
	if cancelled != 0 {
		e.exec(t, `UPDATE events SET status = 'cancelled', cancelled_at = $2 WHERE id = $1`, ev.PG(), e.base.Add(cancelled))
	}
	return tf, ev
}

func TestArchiveInstantTayfas(t *testing.T) {
	e := setup(t)
	// Ended 8 days ago: archived.
	ended, _ := e.instantTayfa(t, -10*day, -8*day, 0)
	// Ended 6 days ago: waits.
	recent, _ := e.instantTayfa(t, -10*day, -6*day, 0)
	// Cancelled 8 days ago, though it would have ended tomorrow: archived
	// (the cancellation counts as the end).
	cancelled, _ := e.instantTayfa(t, -10*day, day, -8*day)
	// Cancelled yesterday: waits.
	cancelledRecent, _ := e.instantTayfa(t, -10*day, 5*day, -day)
	// Not yet happened.
	future, _ := e.instantTayfa(t, -day, 2*day, 0)
	// Its event deleted: nothing left to wait for.
	empty, emptyEv := e.instantTayfa(t, -10*day, 2*day, 0)
	e.exec(t, `UPDATE events SET deleted_at = $2 WHERE id = $1`, emptyEv.PG(), e.base)
	// Suspended: left alone.
	suspended, _ := e.instantTayfa(t, -20*day, -9*day, 0)
	e.exec(t, `UPDATE tayfas SET suspended_at = $2, suspended_reason = 'test' WHERE id = $1`, suspended.PG(), e.base)
	// A standard tayfa with an old event is not this job's.
	standard, admin := e.newTayfa(t)
	e.exec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, created_by)
		VALUES ($1, $2, 'general', 'online', 'Eski', $3, $4, 'https://example.org/x', $5)`,
		id.New().PG(), standard.PG(), e.base.Add(-10*day), e.base.Add(-9*day), admin.PG())

	run(t, "anlık arşiv", e.at(t, 0).ArchiveInstantTayfas, 3)
	for tf, want := range map[id.ID]string{
		ended: "archived", recent: "active", cancelled: "archived", cancelledRecent: "active",
		future: "active", empty: "archived", suspended: "active", standard: "active",
	} {
		if got := e.state(t, tf).status; got != want {
			t.Errorf("tayfa %s durumu %q, %q bekleniyordu", tf, got, want)
		}
	}
	// tayfa.archived with the kind, a system audit record, no notification.
	archived := e.events(t, EventTayfaArchived)
	if len(archived) != 3 {
		t.Fatalf("tayfa.archived = %d", len(archived))
	}
	for _, ev := range archived {
		if ev.payload["kind"] != "instant" || ev.payload["tayfa_id"] != ev.tayfa.String() {
			t.Errorf("olay = %+v", ev)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'tayfa.archived' AND actor_id IS NULL AND metadata ->> 'kind' = 'instant'`); n != 3 {
		t.Errorf("denetim kaydı %d", n)
	}
	if n := len(e.notices(t, "tayfa_archived")); n != 0 {
		t.Errorf("arşiv bildirimi %d", n)
	}

	// Running again changes nothing; a day later the recent one goes too.
	run(t, "tekrar", e.at(t, time.Minute).ArchiveInstantTayfas, 0)
	run(t, "bir gün sonra", e.at(t, day+time.Minute).ArchiveInstantTayfas, 1)
	if got := e.state(t, recent).status; got != "archived" {
		t.Errorf("recent = %q", got)
	}
}

// TestCleanupSkipsInstantTayfas: the weekly cleanup's "3 members in 7 days"
// and "30 days inactive" rules do not apply to instant tayfas (ADR-0032 §6),
// and a converted tayfa counts its 7 days from the conversion.
func TestCleanupSkipsInstantTayfas(t *testing.T) {
	e := setup(t)
	// One member, created 20 days ago, inactive for 40: would be warned on
	// both counts if it were standard.
	inst, _ := e.instantTayfa(t, -40*day, 5*day, 0)
	e.exec(t, `UPDATE tayfas SET last_activity_at = $2 WHERE id = $1`, inst.PG(), e.base.Add(-40*day))
	// Converted 3 days ago (created 20 days ago): not yet a few_members case.
	young, _ := e.instantTayfa(t, -20*day, 5*day, 0)
	e.exec(t, `UPDATE tayfas SET kind = 'standard', converted_at = $2, name = 'Yemek Tayfası', name_normalized = 'yemek tayfası', last_activity_at = $2 WHERE id = $1`,
		young.PG(), e.base.Add(-3*day))
	// Converted 8 days ago with one member: few_members.
	old, _ := e.instantTayfa(t, -20*day, 5*day, 0)
	e.exec(t, `UPDATE tayfas SET kind = 'standard', converted_at = $2, name = 'Yemek Tayfası', name_normalized = 'yemek tayfası', last_activity_at = $3 WHERE id = $1`,
		old.PG(), e.base.Add(-8*day), e.base.Add(-day))

	run(t, "temizlik", e.at(t, 0).CleanupTayfas, 1)
	for tf, want := range map[id.ID]string{inst: "active", young: "active", old: "warned"} {
		if got := e.state(t, tf).status; got != want {
			t.Errorf("tayfa %s durumu %q, %q bekleniyordu", tf, got, want)
		}
	}
}
