package store

import (
	"context"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestBadgeNotificationHasNoPush (issue #258; ADR-0039 §4): the badge
// notice is stored for the in-app list, without a tayfa for a personal
// badge, but the enqueue_push trigger opens no push job for it.
func TestBadgeNotificationHasNoPush(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	for _, tayfa := range []any{nil, id.New().PG()} {
		user, n := id.New(), id.New()
		target := user.PG()
		targetType := "user"
		if tayfa != nil {
			target, targetType = id.New().PG(), "tayfa"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO notifications (id, user_id, tayfa_id, type, target_type, target_id, source_event_id)
		    VALUES ($1, $2, $3, 'badge_earned', $4, $5, $6)`, n.PG(), user.PG(), tayfa, targetType, target, id.New().PG()); err != nil {
			t.Fatal(err)
		}
		var c int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_queue WHERE notification_id = $1`, n.PG()).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Fatalf("rozet bildirimi push kuyruğuna girdi (%s)", targetType)
		}
	}
}
