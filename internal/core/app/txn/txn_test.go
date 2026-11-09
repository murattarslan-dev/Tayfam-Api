package txn

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func TestWithActor(t *testing.T) {
	pool := dbtest.New(t, "core", coredb.Migrations())
	ctx := context.Background()
	actor := id.New()

	var inside string
	err := WithActor(ctx, pool, actor, func(tx pgx.Tx, _ *store.Queries) error {
		return tx.QueryRow(ctx, "SELECT current_actor()::text").Scan(&inside)
	})
	if err != nil || inside != actor.String() {
		t.Fatalf("işlemi yapan = %q %v", inside, err)
	}

	// SET LOCAL: the value does not outlive the transaction on a pooled
	// connection.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var after *string
	if err := conn.QueryRow(ctx, "SELECT current_actor()::text").Scan(&after); err != nil || after != nil {
		t.Fatalf("transaction dışında işlemi yapan kaldı: %v %v", after, err)
	}

	// fn's error is returned unchanged and the transaction rolls back.
	sentinel := errors.New("dur")
	err = WithActor(ctx, pool, actor, func(tx pgx.Tx, _ *store.Queries) error {
		if _, err := tx.Exec(ctx, "INSERT INTO event_consumers (consumer_name) VALUES ('test')"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("hata = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM event_consumers").Scan(&n); err != nil || n != 0 {
		t.Fatalf("geri alınmadı: %d %v", n, err)
	}
}

func TestAuditAndEvent(t *testing.T) {
	pool := dbtest.New(t, "core", coredb.Migrations())
	ctx := context.Background()
	actor, tayfa, target := id.New(), id.New(), id.New()
	err := WithActor(ctx, pool, actor, func(tx pgx.Tx, q *store.Queries) error {
		if _, err := tx.Exec(ctx, "INSERT INTO tayfas (id, name, name_normalized, join_code, created_by) VALUES ($1, 'Tayfa', 'tayfa', 'ABCDEFGH', $2)", tayfa.PG(), actor.PG()); err != nil {
			return err
		}
		if err := Audit(ctx, q, tayfa, actor, "member.banned", Target{Type: "membership", ID: target}, nil); err != nil {
			return err
		}
		return Event(ctx, q, "tayfa.updated", tayfa, actor, Target{}, map[string]any{"fields": []string{"name"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	var meta, payload string
	var subject *string
	if err := pool.QueryRow(ctx, "SELECT metadata::text FROM audit_logs WHERE target_id = $1 AND target_type = 'membership'", target.PG()).Scan(&meta); err != nil || meta != "{}" {
		t.Fatalf("denetim = %q %v", meta, err)
	}
	if err := pool.QueryRow(ctx, "SELECT payload::text, subject_type FROM domain_events WHERE tayfa_id = $1", tayfa.PG()).Scan(&payload, &subject); err != nil || payload != `{"fields": ["name"]}` || subject != nil {
		t.Fatalf("olay = %q %v %v", payload, subject, err)
	}
}

// TestEventUsesOutbox: events go through outbox.Write (ordered IDs) and keep
// core's tayfa_id; outside a transaction nothing is written.
func TestEventUsesOutbox(t *testing.T) {
	pool := dbtest.New(t, coredb.Migrations())
	ctx := context.Background()
	actor, tayfa := id.New(), id.New()

	if err := Event(ctx, store.New(pool), "tayfa.updated", tayfa, actor, Target{}, nil); err == nil {
		t.Fatal("transaction dışında olay yazıldı")
	}

	for range 3 {
		if err := WithActor(ctx, pool, actor, func(_ pgx.Tx, q *store.Queries) error {
			return Event(ctx, q, "tayfa.updated", tayfa, actor, Target{Type: "tayfa", ID: tayfa}, nil)
		}); err != nil {
			t.Fatal(err)
		}
	}
	// System-wide events without a tayfa keep tayfa_id NULL.
	if err := WithActor(ctx, pool, actor, func(_ pgx.Tx, q *store.Queries) error {
		return Event(ctx, q, "tayfa.purged", id.ID{}, actor, Target{}, nil)
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(ctx, "SELECT id::text, tayfa_id::text, payload::text FROM domain_events ORDER BY occurred_at, id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var prev string
	n := 0
	for rows.Next() {
		var eid, payload string
		var tid *string
		if err := rows.Scan(&eid, &tid, &payload); err != nil {
			t.Fatal(err)
		}
		if eid <= prev {
			t.Fatalf("olay kimlikleri sıralı değil: %s <= %s", eid, prev)
		}
		prev = eid
		n++
		if payload != "{}" {
			t.Fatalf("payload = %s", payload)
		}
		if n <= 3 && (tid == nil || *tid != tayfa.String()) {
			t.Fatalf("tayfa_id = %v", tid)
		}
		if n == 4 && tid != nil {
			t.Fatalf("tayfasız olayda tayfa_id = %v", *tid)
		}
	}
	if n != 4 {
		t.Fatalf("%d olay", n)
	}
}

// TouchTayfa moves last_activity_at forward at most once per hour and
// leaves the tayfa's update time alone.
func TestTouchTayfa(t *testing.T) {
	pool := dbtest.New(t, coredb.Migrations())
	ctx := context.Background()
	tf := id.New()
	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO tayfas (id, name, name_normalized, join_code, last_activity_at, updated_at) VALUES ($1, 'Kamp Tayfası', 'kamp tayfası', 'TESTAAAA', $2, $2)`, tf.PG(), start); err != nil {
		t.Fatal(err)
	}
	read := func() (activity, updated time.Time) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT last_activity_at, updated_at FROM tayfas WHERE id = $1`, tf.PG()).Scan(&activity, &updated); err != nil {
			t.Fatal(err)
		}
		return activity, updated
	}
	touch := func(at time.Time) {
		t.Helper()
		if err := WithActor(ctx, pool, id.New(), func(_ pgx.Tx, q *store.Queries) error { return TouchTayfa(ctx, q, tf, at) }); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		at, want time.Time
	}{
		{start.Add(30 * time.Minute), start},                 // within the hour: no new row version
		{start.Add(time.Hour), start.Add(time.Hour)},         // an hour later: moves
		{start.Add(-time.Hour), start.Add(time.Hour)},        // never backwards
		{start.Add(3 * time.Hour), start.Add(3 * time.Hour)}, // moves again
		{start.Add(3*time.Hour + time.Minute), start.Add(3 * time.Hour)},
	} {
		touch(tc.at)
		if got, updated := read(); !got.Equal(tc.want) || !updated.Equal(start) {
			t.Errorf("TouchTayfa(%v): last_activity_at = %v, updated_at = %v", tc.at, got, updated)
		}
	}
	// Any other change still moves updated_at.
	if _, err := pool.Exec(ctx, `UPDATE tayfas SET description = 'x' WHERE id = $1`, tf.PG()); err != nil {
		t.Fatal(err)
	}
	if _, updated := read(); updated.Equal(start) {
		t.Error("başka bir değişiklik updated_at'i ilerletmedi")
	}
}
