package store

import (
	"context"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestFriendSuggestionNotificationHasNoPush (issue #208, ADR-0028 §8): the
// friend_suggestion notice is stored for the in-app list, but the
// enqueue_push trigger opens no push job for it; other types still get one.
func TestFriendSuggestionNotificationHasNoPush(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	user := id.New()
	insert := func(typ string) id.ID {
		t.Helper()
		n := id.New()
		if _, err := pool.Exec(ctx, `INSERT INTO notifications (id, user_id, type, target_type, target_id, source_event_id)
		    VALUES ($1, $2, $3, 'user', $4, $5)`, n.PG(), user.PG(), typ, id.New().PG(), id.New().PG()); err != nil {
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
	if s := insert("friend_suggestion"); queued(s) {
		t.Fatal("arkadaş önerisi push kuyruğuna girdi")
	}
	if r := insert("friend_request"); !queued(r) {
		t.Fatal("arkadaşlık isteği push kuyruğuna girmedi")
	}
}
