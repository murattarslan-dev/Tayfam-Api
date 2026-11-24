package store

import (
	"context"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestLibraryNotificationsHaveNoPush (issue #270; ADR-0040 §1): the
// library reminder and the left-side warning are stored for the in-app
// list, about the loan's tayfa, but the enqueue_push trigger opens no push
// job for them.
func TestLibraryNotificationsHaveNoPush(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	for _, typ := range []string{"library_due", "library_loan_orphaned"} {
		user, n, tayfa := id.New(), id.New(), id.New()
		if _, err := pool.Exec(ctx, `INSERT INTO notifications (id, user_id, tayfa_id, type, target_type, target_id, source_event_id)
		    VALUES ($1, $2, $3, $4, 'tayfa', $3, $5)`, n.PG(), user.PG(), tayfa.PG(), typ, id.New().PG()); err != nil {
			t.Fatal(err)
		}
		var c int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_queue WHERE notification_id = $1`, n.PG()).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Fatalf("%s push kuyruğuna girdi", typ)
		}
	}
}
