package announcement

import (
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
)

// A new announcement notifies every active member but the author; pending,
// banned and non-members hear nothing (issue #64).
func TestCreateRequestsNotifications(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	v := e.publish(t, c, c.mod, "Pazar buluşması")
	got := noticesOf(t, e.pool, notify.TypeAnnouncement)
	if len(got) != 1 || got[0].actor != c.mod || got[0].target != v.ID || got[0].payload["target_type"] != notify.TargetAnnouncement {
		t.Fatalf("istekler = %+v", got)
	}
	if !sameUsers(got[0].recipients, c.admin, c.admin2, c.mod2, c.member, c.mutedMod) {
		t.Fatalf("alıcılar = %v", got[0].recipients)
	}
	for _, k := range []string{"title", "body"} {
		if _, ok := got[0].payload[k]; ok {
			t.Fatalf("içerik %q bildirime girdi", k)
		}
	}
}
