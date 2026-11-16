package store

import (
	"context"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestSafetyAlertNotificationIsPushed (issue #210, ADR-0033 §10): the
// safety contact alarm is a stored notification without a tayfa and, like
// the hike alarm, gets a push job.
func TestSafetyAlertNotificationIsPushed(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	n := id.New()
	if _, err := pool.Exec(ctx, `INSERT INTO notifications (id, user_id, type, target_type, target_id, source_event_id)
	    VALUES ($1, $2, 'safety_alert', 'user', $3, $4)`, n.PG(), id.New().PG(), id.New().PG(), id.New().PG()); err != nil {
		t.Fatal(err)
	}
	var c int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_queue WHERE notification_id = $1`, n.PG()).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Fatal("güvenlik alarmı push kuyruğuna girmedi")
	}
}
