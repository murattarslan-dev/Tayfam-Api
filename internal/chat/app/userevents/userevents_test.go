package userevents

import (
	"context"
	"errors"
	"testing"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestProfileAndDeletion(t *testing.T) {
	pool := dbtest.New(t, chatdb.Migrations())
	q := store.New(pool)
	var cleaned []id.ID
	svc, err := New(Config{Pool: pool, Cleanups: []Cleanup{func(_ context.Context, _ *store.Queries, u id.ID) error {
		cleaned = append(cleaned, u)
		return nil
	}}})
	must(t, err)

	u := id.New()
	first := id.New()
	must(t, svc.Apply(ctx, Event{ID: first, User: u, Profile: &Profile{DisplayName: "Deniz"}}))
	del := Event{ID: id.After(first), User: u, Deleted: true}
	for range 2 { // the same deletion twice
		must(t, svc.Apply(ctx, del))
	}
	if len(cleaned) != 2 || cleaned[0] != u {
		t.Fatalf("temizlik = %v", cleaned)
	}
	p, err := q.GetUserProfile(ctx, u.PG())
	must(t, err)
	if !p.DeletedAt.Valid || p.DisplayName.Valid {
		t.Fatal("profil mezar taşı değil")
	}
	// A late profile event does not bring the name back.
	must(t, svc.Apply(ctx, Event{ID: id.After(del.ID), User: u, Profile: &Profile{DisplayName: "Geri"}}))
	if p, _ := q.GetUserProfile(ctx, u.PG()); p.DisplayName.Valid {
		t.Fatal("silinen profil geri geldi")
	}
}

func TestValidation(t *testing.T) {
	pool := dbtest.New(t, chatdb.Migrations())
	svc, err := New(Config{Pool: pool})
	must(t, err)
	for name, e := range map[string]Event{
		"kimlik":    {User: id.New(), Deleted: true},
		"kullanıcı": {ID: id.New(), Deleted: true},
		"ikisi":     {ID: id.New(), User: id.New(), Deleted: true, Profile: &Profile{DisplayName: "A"}},
		"ad boş":    {ID: id.New(), User: id.New(), Profile: &Profile{}},
		"ad uzun":   {ID: id.New(), User: id.New(), Profile: &Profile{DisplayName: "abcdefghijabcdefghijabcdefghijabcdefghijX"}},
		"kontrol":   {ID: id.New(), User: id.New(), Profile: &Profile{DisplayName: "a\x07b"}},
	} {
		var v *apperr.ValidationError
		if err := svc.Apply(ctx, e); !errors.As(err, &v) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
