package store

import (
	"context"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Schema tests for 00054_starts_now.sql (issue #263, ADR-0038 §1, §2): the
// database's second line of defence behind the app's checks.

func TestOpenEndedConstraints(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})

	for name, extra := range map[string]map[string]string{
		"12 saatten uzun": {"open_ended": "true", "ends_at": "now() + interval '1 day 12 hours 1 second'"},
		"seride":          {"open_ended": "true", "series_id": "gen_random_uuid()"},
		"keşfette":        {"open_ended": "true", "discoverable": "true"},
	} {
		_, err := insertEvent(ctx, pool, tayfa, "general", "in_person", extra)
		if got := constraintOf(err); got != "events_open_ended" {
			t.Errorf("%s: kısıt %q, events_open_ended bekleniyordu (%v)", name, got, err)
		}
	}
	// Exactly 12 hours is allowed; a planned event may last longer.
	if _, err := insertEvent(ctx, pool, tayfa, "general", "in_person", map[string]string{
		"open_ended": "true", "starts_at": "now()", "ends_at": "now() + interval '12 hours'",
	}); err != nil {
		t.Errorf("12 saatlik açık uçlu etkinlik yazılamadı: %v", err)
	}
	if _, err := insertEvent(ctx, pool, tayfa, "general", "in_person", map[string]string{"ends_at": "now() + interval '3 days'"}); err != nil {
		t.Errorf("uzun planlı etkinlik yazılamadı: %v", err)
	}
	// A new event is not open-ended.
	eid, err := insertEvent(ctx, pool, tayfa, "general", "in_person", nil)
	if err != nil {
		t.Fatal(err)
	}
	var open bool
	if err := pool.QueryRow(ctx, `SELECT open_ended FROM events WHERE id = $1`, eid.PG()).Scan(&open); err != nil || open {
		t.Errorf("varsayılan open_ended = %v (%v)", open, err)
	}
}

func TestNowCreateMinRole(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	row, err := q.GetTayfa(ctx, tayfa.PG())
	if err != nil {
		t.Fatal(err)
	}
	if row.NowCreateMinRole != "member" {
		t.Errorf("varsayılan now_create_min_role = %q, member bekleniyordu", row.NowCreateMinRole)
	}
	for _, r := range []string{"moderator", "admin", "member"} {
		if _, err := pool.Exec(ctx, `UPDATE tayfas SET now_create_min_role = $2 WHERE id = $1`, tayfa.PG(), r); err != nil {
			t.Errorf("%s yazılamadı: %v", r, err)
		}
	}
	for _, r := range []string{"owner", "", "Member"} {
		_, err := pool.Exec(ctx, `UPDATE tayfas SET now_create_min_role = $2 WHERE id = $1`, tayfa.PG(), r)
		if got := constraintOf(err); got != "tayfas_now_create_min_role" {
			t.Errorf("%q: kısıt %q (%v)", r, got, err)
		}
	}
}
