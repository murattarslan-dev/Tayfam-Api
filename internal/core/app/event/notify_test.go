package event

import (
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
)

// Event notifications go to the right audience and never to the actor
// (issue #64, v14 "Etkinlik iptali").
func TestEventNotifications(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	a := e.seed(t, tf, authz.Member)
	b := e.seed(t, tf, authz.Member)
	c := e.seed(t, tf, authz.Member)
	left := e.seed(t, tf, authz.Member)

	ev := e.create(t, tf, admin, withCapacity(1))
	created := noticesOf(t, e.pool, notify.TypeEventCreated)
	if len(created) != 1 || created[0].target != ev.ID || !sameUsers(created[0].recipients, a, b, c, left) {
		t.Fatalf("oluşturma = %+v", created)
	}

	if _, err := e.svc.SetRSVP(ctx, a, tf, ev.ID, RSVPGoing); err != nil {
		t.Fatal(err)
	}
	if st, err := e.svc.SetRSVP(ctx, b, tf, ev.ID, RSVPGoing); err != nil || st != RSVPWaitlist {
		t.Fatalf("b = %s %v", st, err)
	}
	if _, err := e.svc.SetRSVP(ctx, left, tf, ev.ID, RSVPMaybe); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetRSVP(ctx, c, tf, ev.ID, RSVPNotGoing); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `DELETE FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), left.PG())

	// a gives up the seat: b is promoted and told; a is not.
	if _, err := e.svc.SetRSVP(ctx, a, tf, ev.ID, RSVPNotGoing); err != nil {
		t.Fatal(err)
	}
	promoted := noticesOf(t, e.pool, notify.TypeRSVPPromoted)
	if len(promoted) != 1 || promoted[0].actor != a || !sameUsers(promoted[0].recipients, b) {
		t.Fatalf("terfi = %+v", promoted)
	}

	in := input()
	in.Title = "Yeni başlık"
	if _, err := e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldTitle}, in); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Cancel(ctx, admin, tf, ev.ID, ""); err != nil {
		t.Fatal(err)
	}
	// Participants (going, maybe, waitlist) who are still active members:
	// b only. c said not going, the one who left is gone.
	for _, typ := range []string{notify.TypeEventUpdated, notify.TypeEventCancelled} {
		got := noticesOf(t, e.pool, typ)
		if len(got) != 1 || got[0].target != ev.ID || got[0].actor != admin || !sameUsers(got[0].recipients, b) {
			t.Errorf("%s = %+v", typ, got)
		}
	}
}

func TestListsOnlyUpdatesNotifyNobody(t *testing.T) {
	for _, tc := range []struct {
		fields []string
		want   bool
	}{
		{[]string{fieldCampItems}, true},
		{[]string{fieldVehicles, fieldCampItems}, true},
		{[]string{fieldCampItems, FieldTitle}, false},
		{[]string{}, false},
	} {
		if got := listsOnly(tc.fields); got != tc.want {
			t.Errorf("%v: %v", tc.fields, got)
		}
	}
	if listsOnly(nil) {
		t.Error("alansız güncelleme liste sayıldı")
	}
}
