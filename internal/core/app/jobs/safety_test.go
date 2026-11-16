package jobs

import (
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// safetyEvent creates an in-person event of tf that ended base + end.
func (e *env) safetyEvent(t *testing.T, tf id.ID, end time.Duration, status string) id.ID {
	t.Helper()
	ev := id.New()
	e.exec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, meeting_point_name, meeting_lat, meeting_lng, city, district, status, cancelled_at, cancel_reason)
	    VALUES ($1, $2, 'general', 'in_person', 'Buluşma', $3, $4, 'Meydan', 38.4, 27.1, 'İzmir', 'Konak', $5,
	            CASE WHEN $5 = 'cancelled' THEN $3::timestamptz END, CASE WHEN $5 = 'cancelled' THEN 'iptal' END)`,
		ev.PG(), tf.PG(), e.base.Add(end-2*time.Hour), e.base.Add(end), status)
	return ev
}

func (e *env) befriend(t *testing.T, a, b id.ID) {
	t.Helper()
	lo, hi := a, b
	if strings.Compare(hi.String(), lo.String()) < 0 {
		lo, hi = hi, lo
	}
	e.exec(t, `INSERT INTO friendships (user_low_id, user_high_id, status, requested_by, accepted_at) VALUES ($1, $2, 'accepted', $1, now())`, lo.PG(), hi.PG())
}

func (e *env) safetyContact(t *testing.T, ev, user, contact id.ID, grace int) {
	t.Helper()
	e.exec(t, `INSERT INTO event_safety_contacts (event_id, user_id, contact_user_id, grace_minutes) VALUES ($1, $2, $3, $4)`,
		ev.PG(), user.PG(), contact.PG(), grace)
}

// TestSafetyAlert (issue #210, ADR-0033 §10): the alert goes to the chosen
// contact alone, once, after the end plus the grace time, and only while
// they are still friends; "I'm back" and a cancellation before the start
// stop it. The notification carries the participant's ID only.
func TestSafetyAlert(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	contactOf := func(u id.ID) id.ID {
		c := id.New()
		e.befriend(t, u, c)
		return c
	}

	due := e.seed(t, tf, "member")
	dueContact := contactOf(due)
	e.safetyContact(t, e.safetyEvent(t, tf, -2*time.Hour, "scheduled"), due, dueContact, 60)

	inGrace := e.seed(t, tf, "member")
	e.safetyContact(t, e.safetyEvent(t, tf, -30*time.Minute, "scheduled"), inGrace, contactOf(inGrace), 60)

	back := e.seed(t, tf, "member")
	backEv := e.safetyEvent(t, tf, -2*time.Hour, "scheduled")
	e.safetyContact(t, backEv, back, contactOf(back), 15)
	e.exec(t, `UPDATE event_safety_contacts SET returned_at = now() WHERE event_id = $1`, backEv.PG())

	unfriended := e.seed(t, tf, "member")
	ufContact := contactOf(unfriended)
	e.safetyContact(t, e.safetyEvent(t, tf, -2*time.Hour, "scheduled"), unfriended, ufContact, 15)
	e.exec(t, `DELETE FROM friendships WHERE $1 IN (user_low_id, user_high_id)`, unfriended.PG())

	cancelled := e.seed(t, tf, "member")
	e.safetyContact(t, e.safetyEvent(t, tf, -2*time.Hour, "cancelled"), cancelled, contactOf(cancelled), 15)

	cancelledThere := e.seed(t, tf, "member")
	ctContact := contactOf(cancelledThere)
	ctEv := e.safetyEvent(t, tf, -2*time.Hour, "cancelled")
	e.safetyContact(t, ctEv, cancelledThere, ctContact, 15)
	e.exec(t, `INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method, checked_in_at) VALUES ($1, $2, 'going', 'attended', 'location', now())`,
		ctEv.PG(), cancelledThere.PG())

	n, err := e.at(t, 0).AlertMissedReturns(ctx)
	must(t, err)
	if n != 2 {
		t.Fatalf("alarm sayısı %d, beklenen 2", n)
	}
	if got := e.noticeFor(t, notify.TypeSafetyAlert, due); len(got) != 1 || got[0] != dueContact.String() {
		t.Fatalf("alıcılar: %v", got)
	}
	if got := e.noticeFor(t, notify.TypeSafetyAlert, cancelledThere); len(got) != 1 || got[0] != ctContact.String() {
		t.Fatalf("check-in yapılmış iptal etkinlik: %v", got)
	}
	for _, u := range []id.ID{inGrace, back, unfriended, cancelled} {
		if got := e.noticeFor(t, notify.TypeSafetyAlert, u); got != nil {
			t.Errorf("%s için alarm gitti: %v", u, got)
		}
	}
	for _, ev := range e.notices(t, notify.TypeSafetyAlert) {
		for _, k := range []string{"tayfa_id", "event_id", "lat", "lng", "title"} {
			if _, ok := ev.payload[k]; ok {
				t.Errorf("bildirimde %s var: %v", k, ev.payload)
			}
		}
	}
	// Once only.
	n, err = e.at(t, time.Hour).AlertMissedReturns(ctx)
	must(t, err)
	if n != 1 { // inGrace is due now
		t.Fatalf("ikinci çalışma: %d", n)
	}
	if got := len(e.notices(t, notify.TypeSafetyAlert)); got != 3 {
		t.Fatalf("toplam bildirim %d", got)
	}
}

// TestSafetyRetention: safety contacts go 30 days after the event's end,
// feedback after a year.
func TestSafetyRetention(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	u := e.seed(t, tf, "member")
	old := e.safetyEvent(t, tf, -31*day, "scheduled")
	recent := e.safetyEvent(t, tf, -29*day, "scheduled")
	for _, ev := range []id.ID{old, recent} {
		e.safetyContact(t, ev, u, id.New(), 60)
	}
	e.exec(t, `INSERT INTO event_feedback (event_id, user_id, answer, created_at) VALUES ($1, $2, 'ok', now() - interval '366 days'), ($3, $2, 'ok', now())`,
		old.PG(), u.PG(), recent.PG())
	_, err := e.at(t, 0).ExpireRecords(ctx)
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM event_safety_contacts WHERE event_id = $1`, recent.PG()); n != 1 {
		t.Fatalf("yeni güvenlik kişisi silindi: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_safety_contacts WHERE event_id = $1`, old.PG()); n != 0 {
		t.Fatalf("eski güvenlik kişisi kaldı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_feedback`); n != 1 {
		t.Fatalf("geri bildirim: %d", n)
	}
}
