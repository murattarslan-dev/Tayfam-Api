package event

import (
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
)

// A tayfa's now event tells its members with now_started instead of
// event_created: never the organizer, never anyone blocked with the
// organizer either way, at most notify.NowPerTayfaDay a day per tayfa
// (issue #265; ADR-0038 §4).
func TestNowNotification(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	a := e.seed(t, tf, authz.Member)
	b := e.seed(t, tf, authz.Member)
	blocker := e.seed(t, tf, authz.Member)
	blocked := e.seed(t, tf, authz.Member)
	e.exec(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2), ($3, $4)`,
		blocker.PG(), admin.PG(), admin.PG(), blocked.PG())
	c := nowClock()
	svc := e.clocked(t, c, nil)

	ev, err := svc.Create(ctx, admin, tf, nowInput())
	must(t, err)
	got := noticesOf(t, e.pool, notify.TypeNowStarted)
	if len(got) != 1 || got[0].target != ev.ID || got[0].actor != admin || !sameUsers(got[0].recipients, a, b) {
		t.Fatalf("şimdi bildirimi = %+v", got)
	}
	// Only IDs: no title, type or place in the request.
	for k := range got[0].payload {
		switch k {
		case "type", "target_type", "target_id", "recipient_ids", "tayfa_id":
		default:
			t.Errorf("bildirimde %q alanı", k)
		}
	}
	if n := len(noticesOf(t, e.pool, notify.TypeEventCreated)); n != 0 {
		t.Fatalf("event_created = %d", n)
	}
	if n := e.count(t, `SELECT sent FROM now_notification_counters WHERE scope = 'tayfa' AND subject_id = $1`, tf.PG()); n != 1 {
		t.Fatalf("sayaç = %d", n)
	}

	// Two more notify; the fourth of the day opens without a notification.
	for i := 2; i <= notify.NowPerTayfaDay+1; i++ {
		c.t = c.t.Add(time.Minute)
		_, err := svc.Finish(ctx, admin, tf, ev.ID)
		must(t, err)
		ev, err = svc.Create(ctx, admin, tf, nowInput())
		must(t, err)
		want := min(i, notify.NowPerTayfaDay)
		if n := len(noticesOf(t, e.pool, notify.TypeNowStarted)); n != want {
			t.Fatalf("%d. şimdi etkinliği: bildirim = %d, beklenen %d", i, n, want)
		}
	}
	if n := e.count(t, `SELECT sent FROM now_notification_counters WHERE scope = 'tayfa' AND subject_id = $1`, tf.PG()); n != notify.NowPerTayfaDay {
		t.Fatalf("sayaç = %d", n)
	}
	// The counter is the database's second line too.
	if _, err := e.pool.Exec(ctx, `UPDATE now_notification_counters SET sent = 4 WHERE subject_id = $1`, tf.PG()); err == nil {
		t.Fatal("sayaç sınırı aşıldı")
	}

	// A planned event still says event_created.
	planned := e.create(t, tf, admin, input())
	if created := noticesOf(t, e.pool, notify.TypeEventCreated); len(created) != 1 || created[0].target != planned.ID {
		t.Fatalf("planlı = %+v", created)
	}

	// A tayfa nobody else is in counts nothing.
	alone, owner := e.newTayfa(t)
	_, err = e.clocked(t, c, nil).Create(ctx, owner, alone, nowInput())
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM now_notification_counters WHERE subject_id = $1`, alone.PG()); n != 0 {
		t.Fatalf("boş tayfa sayacı = %d", n)
	}
}
