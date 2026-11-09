package userevents

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestBanHidesProfileInOrder: user.banned hides the user's profile from
// other users, user.unbanned shows it again, an older event arriving late
// changes nothing, and nothing is deleted (issue #104).
func TestBanHidesProfileInOrder(t *testing.T) {
	e := setup(t)
	u := id.New()
	ev := eventIDs(4)
	must(t, e.svc.Apply(ctx, profile(ev[0], u, "Ayşe")))
	q := store.New(e.pool)
	visible := func() bool {
		t.Helper()
		_, err := q.GetLiveUserProfile(ctx, u.PG())
		if errors.Is(err, pgx.ErrNoRows) {
			return false
		}
		must(t, err)
		return true
	}
	must(t, e.svc.Apply(ctx, Event{ID: ev[1], User: u, OccurredAt: now, Ban: BanBanned}))
	if visible() {
		t.Fatal("banlı kullanıcının profili görünüyor")
	}
	if p, err := q.GetUserProfile(ctx, u.PG()); err != nil || p.DisplayName.String != "Ayşe" {
		t.Fatalf("profil silindi: %+v %v", p, err)
	}
	must(t, e.svc.Apply(ctx, Event{ID: ev[3], User: u, OccurredAt: now, Ban: BanLifted}))
	must(t, e.svc.Apply(ctx, Event{ID: ev[2], User: u, OccurredAt: now, Ban: BanBanned})) // older, late
	if !visible() {
		t.Fatal("ban kaldırıldıktan sonra profil gizli")
	}
	if err := e.svc.Apply(ctx, Event{ID: id.New(), User: u, Deleted: true, Ban: BanBanned}); err == nil {
		t.Fatal("iki tür birden kabul edildi")
	}
}
