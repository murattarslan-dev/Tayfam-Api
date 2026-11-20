package store

import (
	"context"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestCelebrationNotificationHasNoPush (issue #255): the celebration notice
// is stored for the in-app list, but the enqueue_push trigger opens no push
// job for it; an event notice still gets one.
func TestCelebrationNotificationHasNoPush(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	user, tayfa, event := id.New(), id.New(), id.New()
	insert := func(typ string) id.ID {
		t.Helper()
		n := id.New()
		if _, err := pool.Exec(ctx, `INSERT INTO notifications (id, user_id, tayfa_id, type, target_type, target_id, source_event_id)
		    VALUES ($1, $2, $3, $4, 'event', $5, $6)`, n.PG(), user.PG(), tayfa.PG(), typ, event.PG(), id.New().PG()); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		return n
	}
	queued := func(n id.ID) bool {
		t.Helper()
		var c int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_queue WHERE notification_id = $1`, n.PG()).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c == 1
	}
	if c := insert("celebration"); queued(c) {
		t.Fatal("kutlama push kuyruğuna girdi")
	}
	if e := insert("event_created"); !queued(e) {
		t.Fatal("etkinlik bildirimi push kuyruğuna girmedi")
	}
}
