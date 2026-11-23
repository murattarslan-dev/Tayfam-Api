package jobs

import (
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/stats"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// statEvent writes a "general" event of tf that ended at base + ended,
// attended by who.
func (e *env) statEvent(t *testing.T, tf id.ID, ended time.Duration, who ...id.ID) id.ID {
	t.Helper()
	ev := id.New()
	end := e.base.Add(ended)
	e.seedAdmin(t, `INSERT INTO events (id, tayfa_id, type, type_version, mode, title, starts_at, ends_at, online_url)
		VALUES ($1, $2, 'general', 1, 'online', 'Buluşma', $3, $4, 'https://example.test/oda')`, ev.PG(), tf.PG(), end.Add(-time.Hour), end)
	for _, u := range who {
		e.seedAdmin(t, `INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method, checked_in_at)
			VALUES ($1, $2, 'going', 'attended', 'location', $3)`, ev.PG(), u.PG(), end)
	}
	return ev
}

func (e *env) allTime(t *testing.T, tf id.ID, metric string) int {
	t.Helper()
	return e.count(t, `SELECT COALESCE((SELECT value::int FROM tayfa_stats WHERE tayfa_id = $1 AND year = 0 AND metric_key = $2), -1)`, tf.PG(), metric)
}

// TestComputeStats (issue #266; ADR-0026 §1–§2): an event counts once it
// froze; a deleted event drops out; a queued tayfa is recomputed; a run
// with nothing to do changes nothing.
func TestComputeStats(t *testing.T) {
	day := 24 * time.Hour
	e := setup(t)
	tf, _ := e.newTayfa(t)
	u := e.seed(t, tf, "member")
	old := e.statEvent(t, tf, -8*day, u)
	e.statEvent(t, tf, -2*day, u)

	run(t, "ilk", e.at(t, 0).ComputeStats, 2) // one event marked, one tayfa computed
	if got := e.allTime(t, tf, "events"); got != 1 {
		t.Fatalf("donmuş etkinlik: %d", got)
	}
	if got := e.count(t, `SELECT value::int FROM user_stats WHERE user_id = $1 AND year = 0 AND metric_key = 'attended'`, u.PG()); got != 1 {
		t.Fatalf("kişinin katılımı: %d", got)
	}
	run(t, "tekrar", e.at(t, 0).ComputeStats, 0)

	run(t, "beş gün sonra", e.at(t, 5*day+time.Minute).ComputeStats, 2)
	if got := e.allTime(t, tf, "events"); got != 2 {
		t.Fatalf("yeni donan: %d", got)
	}

	e.seedAdmin(t, `UPDATE events SET deleted_at = $2 WHERE id = $1`, old.PG(), e.base)
	run(t, "silinen", e.at(t, 5*day+time.Minute).ComputeStats, 2)
	if got := e.allTime(t, tf, "events"); got != 1 {
		t.Fatalf("silinen etkinlik düşmedi: %d", got)
	}
	run(t, "silinen tekrar", e.at(t, 5*day+time.Minute).ComputeStats, 0)

	// A vocabulary merge queues the tayfa (the outbox consumer).
	must(t, stats.Queue(ctx, store.New(e.pool), tf, e.base))
	run(t, "kuyruk", e.at(t, 5*day+time.Minute).ComputeStats, 1)
	if n := e.count(t, `SELECT count(*) FROM tayfa_stat_queue`); n != 0 {
		t.Fatalf("kuyruk boşalmadı: %d", n)
	}
}
