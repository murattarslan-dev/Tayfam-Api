package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func setup(t *testing.T) (*pgxpool.Pool, *Queries) {
	t.Helper()
	pool := dbtest.New(t, chatdb.Migrations())
	return pool, New(pool)
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func TestMigrationsCreateBaseTables(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	for _, table := range []string{"domain_events", "event_consumers", "user_profiles"} {
		var ok bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("%s tablosu yok", table)
		}
	}
}

func TestUserProfileUpsertIsOrderedAndIdempotent(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	user := id.New()
	older := id.New()
	newer := id.After(older)

	upsert := func(name string, ev id.ID) int64 {
		t.Helper()
		n, err := q.UpsertUserProfile(ctx, UpsertUserProfileParams{
			UserID: user.PG(), DisplayName: text(name), SourceEventID: ev.PG(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := upsert("Yeni", newer); n != 1 {
		t.Fatalf("ilk yazım: %d satır", n)
	}
	// The same event again and an older event change nothing.
	if n := upsert("Yeni", newer); n != 0 {
		t.Errorf("aynı olay tekrar uygulandı: %d", n)
	}
	if n := upsert("Eski", older); n != 0 {
		t.Errorf("eski olay uygulandı: %d", n)
	}
	p, err := q.GetUserProfile(ctx, user.PG())
	if err != nil {
		t.Fatal(err)
	}
	if p.DisplayName.String != "Yeni" {
		t.Errorf("ad = %q", p.DisplayName.String)
	}
}

func TestUserProfileTombstoneCannotBeRevived(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	user := id.New()
	first := id.New()
	deleted := id.After(first)
	late := id.After(deleted)

	if _, err := q.UpsertUserProfile(ctx, UpsertUserProfileParams{UserID: user.PG(), DisplayName: text("Ad"), SourceEventID: first.PG()}); err != nil {
		t.Fatal(err)
	}
	for range 2 { // applying the deletion twice gives the same result
		if err := q.TombstoneUserProfile(ctx, TombstoneUserProfileParams{UserID: user.PG(), SourceEventID: deleted.PG()}); err != nil {
			t.Fatal(err)
		}
	}
	// A profile event that arrives after the deletion, even with a greater
	// ID, does not bring the name back.
	n, err := q.UpsertUserProfile(ctx, UpsertUserProfileParams{UserID: user.PG(), DisplayName: text("Geri"), SourceEventID: late.PG()})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("silinen profil geri geldi")
	}
	p, err := q.GetUserProfile(ctx, user.PG())
	if err != nil {
		t.Fatal(err)
	}
	if !p.DeletedAt.Valid || p.DisplayName.Valid || p.AvatarUploadID.Valid {
		t.Errorf("mezar taşı kişisel veri taşıyor: %+v", p)
	}
}
