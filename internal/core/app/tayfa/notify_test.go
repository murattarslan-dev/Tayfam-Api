package tayfa

import (
	"context"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
)

// A deletion request notifies the other admins and the moderators (v14
// "Tayfa silmede bekleme"), not plain or pending members.
func TestDeletionRequestNotifiesStaff(t *testing.T) {
	e := setup(t)
	tf, admin := e.create(t, "open")
	admin2 := e.member(t, tf, admin, authz.Admin)
	mod := e.member(t, tf, admin, authz.Moderator)
	e.member(t, tf, admin, authz.Member)
	if _, err := e.svc.RequestDeletion(context.Background(), admin, tf); err != nil {
		t.Fatal(err)
	}
	got := noticesOf(t, e.pool, notify.TypeTayfaDeletionRequested)
	if len(got) != 1 || got[0].actor != admin || got[0].target != tf || !sameUsers(got[0].recipients, admin2, mod) {
		t.Fatalf("istekler = %+v", got)
	}
}
