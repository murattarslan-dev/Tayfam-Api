package txn

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func TestWithActor(t *testing.T) {
	pool := dbtest.New(t, coredb.Migrations())
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
	pool := dbtest.New(t, coredb.Migrations())
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
