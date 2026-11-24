package catalogcopy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/profilefields"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

var ctx = context.Background()

func setup(t *testing.T) (*pgxpool.Pool, *Service) {
	t.Helper()
	pool := dbtest.New(t, "identity", identitydb.Migrations())
	s, err := New(Config{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	return pool, s
}

// copyState reads the copy: its version and every key's status.
func copyState(t *testing.T, q *store.Queries) (int64, map[string]string) {
	t.Helper()
	v, err := q.GetCatalogCopyVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := q.ListCatalogInterests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.Key] = r.Status
	}
	return v, out
}

func TestNewNeedsPool(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("havuzsuz servis kuruldu")
	}
}

// TestApply: a newer version replaces the copy in one step under
// identity_app's own privileges: new keys are added, statuses follow the
// catalog, and a key the catalog lacks is retired, not deleted. Profiles
// can choose the new interest at once.
func TestApply(t *testing.T) {
	pool, s := setup(t)
	q := store.New(pool)
	change := Change{Version: 7, Interests: []Interest{
		{"automotive", StatusActive}, {"nature", StatusActive}, {"water", StatusRetired},
		{"sports", StatusActive}, {"general", StatusActive}, {"dance", StatusActive},
	}}
	applied, err := s.Apply(ctx, change)
	if err != nil || !applied {
		t.Fatalf("uygulandı = %v, %v", applied, err)
	}
	v, got := copyState(t, q)
	want := map[string]string{
		"automotive": StatusActive, "nature": StatusActive, "water": StatusRetired, "sports": StatusActive,
		"general": StatusActive, "dance": StatusActive,
		// Not in the catalog's list: retired, kept for old profiles.
		"games": StatusRetired,
	}
	if v != 7 || !maps.Equal(got, want) {
		t.Fatalf("kopya = %d %v", v, got)
	}
	if err := profilefields.CheckInterests(ctx, q, nil, []string{"dance"}); err != nil {
		t.Fatalf("yeni ilgi alanı seçilemedi: %v", err)
	}
	if err := profilefields.CheckInterests(ctx, q, nil, []string{"water"}); !errors.Is(err, profilefields.ErrInvalid) {
		t.Fatalf("emekli ilgi alanı seçildi: %v", err)
	}

	// The same event again, or an older one: nothing changes (the relay
	// delivers at least once; events may come late).
	for _, old := range []Change{
		change,
		{Version: 6, Interests: []Interest{{"automotive", StatusRetired}}},
		{Version: 1, Interests: []Interest{{"games", StatusActive}}},
	} {
		applied, err := s.Apply(ctx, old)
		if err != nil || applied {
			t.Fatalf("sürüm %d: uygulandı = %v, %v", old.Version, applied, err)
		}
	}
	if v, got := copyState(t, q); v != 7 || !maps.Equal(got, want) {
		t.Fatalf("eski olay kopyayı değiştirdi: %d %v", v, got)
	}

	// A retired interest comes back when the catalog activates it.
	applied, err = s.Apply(ctx, Change{Version: 8, Interests: []Interest{
		{"automotive", StatusActive}, {"nature", StatusActive}, {"water", StatusActive},
		{"sports", StatusActive}, {"general", StatusActive}, {"dance", StatusActive}, {"games", StatusActive},
	}})
	if err != nil || !applied {
		t.Fatalf("sürüm 8: uygulandı = %v, %v", applied, err)
	}
	if v, got := copyState(t, q); v != 8 || got["water"] != StatusActive || got["games"] != StatusActive || len(got) != 7 {
		t.Fatalf("sürüm 8 kopyası = %d %v", v, got)
	}
}

// TestApplyInvalid: an invalid change is ErrInvalid and changes nothing.
func TestApplyInvalid(t *testing.T) {
	pool, s := setup(t)
	q := store.New(pool)
	many := make([]Interest, MaxInterests+1)
	for i := range many {
		many[i] = Interest{fmt.Sprintf("k%03d", i), StatusActive}
	}
	for name, c := range map[string]Change{
		"sürüm yok":         {Interests: []Interest{{"nature", StatusActive}}},
		"eksi sürüm":        {Version: -1, Interests: []Interest{{"nature", StatusActive}}},
		"ilgi alanı yok":    {Version: 2},
		"çok fazla":         {Version: 2, Interests: many},
		"büyük harf":        {Version: 2, Interests: []Interest{{"Nature", StatusActive}}},
		"tek harf":          {Version: 2, Interests: []Interest{{"n", StatusActive}}},
		"çok uzun anahtar":  {Version: 2, Interests: []Interest{{strings.Repeat("a", 41), StatusActive}}},
		"tekrar":            {Version: 2, Interests: []Interest{{"nature", StatusActive}, {"nature", StatusRetired}}},
		"bilinmeyen durum":  {Version: 2, Interests: []Interest{{"nature", "deleted"}}},
		"boş durum":         {Version: 2, Interests: []Interest{{"nature", ""}}},
		"türkçe karakterli": {Version: 2, Interests: []Interest{{"doğa", StatusActive}}},
	} {
		t.Run(name, func(t *testing.T) {
			if applied, err := s.Apply(ctx, c); !errors.Is(err, ErrInvalid) || applied {
				t.Fatalf("uygulandı = %v, %v", applied, err)
			}
		})
	}
	if v, got := copyState(t, q); v != 1 || len(got) != 6 {
		t.Fatalf("geçersiz değişiklik kopyayı değiştirdi: %d %v", v, got)
	}
}

// TestCopyGuards: identity_app may advance the version and write statuses,
// but not move the version back, delete an interest or change a key
// (ADR-0042 §4).
func TestCopyGuards(t *testing.T) {
	pool, s := setup(t)
	if _, err := s.Apply(ctx, Change{Version: 5, Interests: []Interest{{"nature", StatusActive}}}); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `UPDATE catalog_copy SET catalog_version = 4`)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "catalog_copy_forward_only" {
		t.Fatalf("sürüm geri gitti: %v", err)
	}
	for name, sql := range map[string]string{
		"ilgi alanı silme":    `DELETE FROM catalog_interests`,
		"anahtar değiştirme":  `UPDATE catalog_interests SET key = 'dance' WHERE key = 'nature'`,
		"sürümü silme":        `DELETE FROM catalog_copy`,
		"ikinci sürüm satırı": `INSERT INTO catalog_copy (id, catalog_version) VALUES (false, 9)`,
		"boşaltma":            `TRUNCATE catalog_interests`,
	} {
		t.Run(name, func(t *testing.T) { dbtest.WantDenied(t, pool, sql) })
	}
}
