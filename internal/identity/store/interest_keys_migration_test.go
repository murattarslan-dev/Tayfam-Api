package store

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// migrationParts returns the Up and the Down statements of a goose
// migration file, each as one script.
func migrationParts(t *testing.T, fsys fs.FS, name string) (up, down string) {
	t.Helper()
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "-- +goose Up")
	if !ok {
		t.Fatalf("%s: Up bölümü yok", name)
	}
	up, down, ok = strings.Cut(rest, "-- +goose Down")
	if !ok {
		t.Fatalf("%s: Down bölümü yok", name)
	}
	return up, down
}

// The copy of core's catalog starts with the six interests of core's seed
// at catalog version 1 (ADR-0025 §10), and identity_app can only read it.
func TestCatalogCopySeed(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	v, err := q.GetCatalogCopyVersion(ctx)
	if err != nil || v != 1 {
		t.Fatalf("kopya sürümü = %d, %v", v, err)
	}
	rows, err := q.ListCatalogInterests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range rows {
		if r.Status != "active" {
			t.Errorf("%s: durum %q", r.Key, r.Status)
		}
		keys = append(keys, r.Key)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"automotive", "games", "general", "nature", "sports", "water"}) {
		t.Fatalf("kopyadaki ilgi alanları = %v", keys)
	}
	for name, sql := range map[string]string{
		"ilgi alanı ekleme":     `INSERT INTO catalog_interests (key) VALUES ('dance')`,
		"ilgi alanı değiştirme": `UPDATE catalog_interests SET status = 'retired'`,
		"ilgi alanı silme":      `DELETE FROM catalog_interests`,
		"sürümü değiştirme":     `UPDATE catalog_copy SET catalog_version = 99`,
		"sürümü silme":          `DELETE FROM catalog_copy`,
		"ikinci sürüm satırı":   `INSERT INTO catalog_copy (id, catalog_version) VALUES (false, 2)`,
	} {
		t.Run(name, func(t *testing.T) { dbtest.WantDenied(t, pool, sql) })
	}
}

// The database refuses a profile interest the copy does not have, whatever
// the service checked (the second line of defence).
func TestProfileInterestsGuard(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	u := newUser(t, q)
	if _, err := pool.Exec(ctx, `INSERT INTO profile_details (user_id, interests) VALUES ($1, '{nature,water}')`, u.PG()); err != nil {
		t.Fatalf("katalogdaki ilgi alanları yazılamadı: %v", err)
	}
	for name, tc := range map[string]struct{ sql, constraint string }{
		"bilinmeyen ilgi alanı": {`UPDATE profile_details SET interests = '{nature,dance}' WHERE user_id = $1`, "profile_details_interests_guard"},
		// An event type is not an interest.
		"etkinlik türü": {`UPDATE profile_details SET interests = '{hike}' WHERE user_id = $1`, "profile_details_interests_guard"},
		"çok fazla":     {`UPDATE profile_details SET interests = array_fill('nature'::text, ARRAY[21]) WHERE user_id = $1`, "profile_details_interests"},
	} {
		if _, err := pool.Exec(ctx, tc.sql, u.PG()); constraintOf(err) != tc.constraint {
			t.Errorf("%s: %v", name, err)
		}
	}
	other := newUser(t, q)
	if _, err := pool.Exec(ctx, `INSERT INTO profile_details (user_id, interests) VALUES ($1, '{dance}')`, other.PG()); constraintOf(err) != "profile_details_interests_guard" {
		t.Fatalf("bilinmeyen ilgi alanıyla satır açıldı: %v", err)
	}
	// A change that leaves the interests alone is not checked again: a
	// profile keeps an interest however the copy changes.
	if _, err := pool.Exec(ctx, `UPDATE profile_details SET team = 'Bursaspor' WHERE user_id = $1`, u.PG()); err != nil {
		t.Fatal(err)
	}
}

// 00014 moves real values (ADR-0025 §10): an event type becomes its
// interest, two types of one interest become one value, and the way back
// maps the six Faz 1 interests to a type again. The test database is
// migrated while empty, so the move is replayed here on rows: back to the
// old shape, old rows in, forward. The scripts run as a superuser; only
// that connection is used after them.
func TestProfileInterestKeysMigrationMovesValues(t *testing.T) {
	d := dbtest.Open(t, "identity", identitydb.Migrations())
	ctx := context.Background()
	up, down := migrationParts(t, identitydb.Migrations(), "00014_profile_interest_keys.sql")
	q := New(d.App)
	mixed, none, rest := newUser(t, q), newUser(t, q), newUser(t, q)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Admin.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	interests := func(u id.ID) []string {
		t.Helper()
		var out []string
		if err := d.Admin.QueryRow(ctx, `SELECT interests FROM profile_details WHERE user_id = $1`, u.PG()).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	exec(down)
	exec(`INSERT INTO profile_details (user_id, interests, team) VALUES
		($1, '{camp,hike,convoy}', 'Bursaspor'), ($2, '{}', NULL), ($3, '{general,gaming,fishing,sports}', NULL)`,
		mixed.PG(), none.PG(), rest.PG())

	exec(up)
	if got := interests(mixed); !slices.Equal(got, []string{"automotive", "nature"}) {
		t.Fatalf("camp + hike + convoy = %v", got)
	}
	if got := interests(none); len(got) != 0 {
		t.Fatalf("boş ilgi alanları = %v", got)
	}
	if got := interests(rest); !slices.Equal(got, []string{"games", "general", "sports", "water"}) {
		t.Fatalf("diğer türler = %v", got)
	}
	// Nothing else of the row changed.
	var team string
	if err := d.Admin.QueryRow(ctx, `SELECT team FROM profile_details WHERE user_id = $1`, mixed.PG()).Scan(&team); err != nil || team != "Bursaspor" {
		t.Fatalf("takım = %q, %v", team, err)
	}
	var version int64
	if err := d.Admin.QueryRow(ctx, `SELECT catalog_version FROM catalog_copy`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("kopya sürümü = %d, %v", version, err)
	}

	// The way back: the six Faz 1 interests become a type again.
	exec(down)
	if got := interests(mixed); !slices.Equal(got, []string{"camp", "convoy"}) {
		t.Fatalf("geri alınan türler = %v", got)
	}
	if got := interests(rest); !slices.Equal(got, []string{"fishing", "gaming", "general", "sports"}) {
		t.Fatalf("geri alınan diğer türler = %v", got)
	}
}
