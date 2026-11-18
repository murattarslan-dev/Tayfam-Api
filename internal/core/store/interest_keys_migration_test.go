package store

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
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

// 00032 moves real rows (ADR-0025 §10): an event type becomes its
// interest, two types of one interest become one row that keeps the older
// created_at, and the way back maps the six Faz 1 interests to a type
// again. The test database is migrated while empty, so the move is
// replayed here on rows: back to the old shape, old rows in, forward.
func TestTayfaInterestKeysMigrationMovesRows(t *testing.T) {
	pool, _ := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	up, down := migrationParts(t, coredb.Migrations(), "00032_tayfa_interest_keys.sql")
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	list := func(sql string) []string {
		t.Helper()
		rows, err := admin.Query(ctx, sql, tayfa.PG())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	exec(down)
	older := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	newer := older.Add(time.Hour)
	exec(`INSERT INTO tayfa_interests (tayfa_id, event_type, created_at) VALUES
		($1, 'camp', $3), ($1, 'hike', $2), ($1, 'convoy', $3), ($1, 'fishing', $3),
		($1, 'gaming', $3), ($1, 'sports', $3), ($1, 'general', $3)`, tayfa.PG(), older, newer)

	exec(up)
	if got := list(`SELECT interest_key FROM tayfa_interests WHERE tayfa_id = $1 ORDER BY 1`); !slices.Equal(got, []string{"automotive", "games", "general", "nature", "sports", "water"}) {
		t.Fatalf("ilgi alanları = %v", got)
	}
	var kept time.Time
	if err := admin.QueryRow(ctx, `SELECT created_at FROM tayfa_interests WHERE tayfa_id = $1 AND interest_key = 'nature'`, tayfa.PG()).Scan(&kept); err != nil || !kept.Equal(older) {
		t.Fatalf("birleşen satırın zamanı = %v, %v; en eskisi kalmalıydı", kept, err)
	}
	// The new shape holds: one row per interest, catalog keys only.
	if _, err := admin.Exec(ctx, `INSERT INTO tayfa_interests (tayfa_id, interest_key) VALUES ($1, 'nature')`, tayfa.PG()); constraintOf(err) != "tayfa_interests_pkey" {
		t.Fatalf("tekrar eden ilgi alanı: %v", err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO tayfa_interests (tayfa_id, interest_key) VALUES ($1, 'camp')`, tayfa.PG()); constraintOf(err) != "tayfa_interests_interest_key_fkey" {
		t.Fatalf("etkinlik türü ilgi alanı olarak yazıldı: %v", err)
	}

	// The way back: the six Faz 1 interests become a type again.
	exec(down)
	if got := list(`SELECT event_type FROM tayfa_interests WHERE tayfa_id = $1 ORDER BY 1`); !slices.Equal(got, []string{"camp", "convoy", "fishing", "gaming", "general", "sports"}) {
		t.Fatalf("geri alınan türler = %v", got)
	}
}
