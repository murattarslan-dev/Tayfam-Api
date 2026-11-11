package userevents

import (
	"context"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// identity's profile events decide which avatar stays (issue #135): the
// newest event's avatar stays, earlier attached avatars go; a stale event
// deletes nothing. Account deletion deletes the user's files.
func TestAvatarLifecycle(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u := id.New()
	avatar := func() id.ID {
		a := mediatest.Ready(t, e.pool, u, media.PurposeAvatar, id.ID{})
		must(t, media.Attach(ctx, store.New(e.pool), u, a, media.PurposeAvatar, id.ID{}, media.Target{Type: media.TargetUser, ID: u}, now))
		return a
	}
	profile := func(ev, a id.ID) {
		must(t, e.svc.Apply(ctx, Event{ID: ev, User: u, Profile: &Profile{DisplayName: "Ece", AvatarUploadID: a}}))
	}
	first, second := avatar(), avatar()
	older := id.New()
	newer := id.After(older)
	profile(newer, second)
	if mediatest.Status(t, e.pool, first) != "deleting" || mediatest.Status(t, e.pool, second) != "ready" {
		t.Fatal("güncel avatar kalmalı, eskisi silinmeli")
	}
	// An event older than the applied one (reordered delivery): ignored.
	third := avatar()
	profile(older, first)
	if mediatest.Status(t, e.pool, third) != "ready" || mediatest.Status(t, e.pool, second) != "ready" {
		t.Fatalf("eski olay avatar silmemeli: %s %s", mediatest.Status(t, e.pool, third), mediatest.Status(t, e.pool, second))
	}
	tf := e.tayfa(t, u)
	cover := mediatest.Ready(t, e.pool, u, media.PurposeCover, tf)
	must(t, media.Attach(ctx, store.New(e.pool), u, cover, media.PurposeCover, tf, media.Target{Type: media.TargetTayfa, ID: tf}, now))
	must(t, e.svc.Apply(ctx, Event{ID: id.New(), User: u, Deleted: true}))
	for name, a := range map[string]id.ID{"avatar": second, "bağlanmamış": third} {
		if s := mediatest.Status(t, e.pool, a); s != "deleting" {
			t.Errorf("hesap silmede %s silinmeli: %s", name, s)
		}
	}
	var owner *string
	must(t, e.pool.QueryRow(ctx, "SELECT user_id::text FROM uploads WHERE id = $1", cover.PG()).Scan(&owner))
	if mediatest.Status(t, e.pool, cover) != "ready" || owner != nil {
		t.Fatal("tayfa kapağı sahipsiz kalmalı")
	}
}
