package store

import (
	"context"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestLibraryLoanStepNotificationsArePushed (issue #321; ADR-0040 §1,
// revised 2026-11-23): the loan step notifications are about a library
// item and, unlike library_due, open a push job.
func TestLibraryLoanStepNotificationsArePushed(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	for _, typ := range []string{
		"library_loan_requested", "library_queue_joined", "library_loan_approved", "library_loan_declined",
		"library_loan_handed_over",
	} {
		user, n, tayfa := id.New(), id.New(), id.New()
		if _, err := pool.Exec(ctx, `INSERT INTO notifications (id, user_id, tayfa_id, actor_id, type, target_type, target_id, source_event_id)
		    VALUES ($1, $2, $3, $4, $5, 'library_item', $6, $7)`, n.PG(), user.PG(), tayfa.PG(), id.New().PG(), typ, id.New().PG(), id.New().PG()); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		var c int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_queue WHERE notification_id = $1`, n.PG()).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 1 {
			t.Fatalf("%s push kuyruğuna girmedi", typ)
		}
	}
	// library_item is the target of these types only in the code; the
	// database refuses an unknown target type.
	if _, err := pool.Exec(ctx, `INSERT INTO notifications (id, user_id, tayfa_id, type, target_type, target_id, source_event_id)
	    VALUES ($1, $2, $3, 'library_loan_requested', 'library_loan', $4, $5)`, id.New().PG(), id.New().PG(), id.New().PG(), id.New().PG(), id.New().PG()); err == nil {
		t.Fatal("bilinmeyen hedef türü kabul edildi")
	}
}
