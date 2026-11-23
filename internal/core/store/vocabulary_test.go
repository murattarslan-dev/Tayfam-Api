package store

import (
	"context"
	"strings"
	"testing"
	"time"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The tayfa vocabulary (00039, issue #244; ADR-0037 §4): a value's CHECKs,
// one spelling per normalized form, merges one level deep in the same
// tayfa and namespace, and a log entry's link only to its own tayfa's
// value of the field's namespace.
func TestVocabularyConstraints(t *testing.T) {
	pool, _ := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	tayfa, other := newTayfa(t, pool, id.New(), tayfaOpts{}), newTayfa(t, pool, id.New(), tayfaOpts{})
	value := func(tf id.ID, ns, v, norm string) (id.ID, error) {
		t.Helper()
		vid := id.New()
		_, err := pool.Exec(ctx, `INSERT INTO tayfa_vocabulary (id, tayfa_id, namespace, value, value_normalized, created_by)
			VALUES ($1, $2, $3, $4, $5, gen_random_uuid())`, vid.PG(), tf.PG(), ns, v, norm)
		return vid, err
	}
	must := func(v id.ID, err error) id.ID {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	levrek := must(value(tayfa, "fish_species", "Levrek", "levrek"))
	lavrak := must(value(tayfa, "fish_species", "lavrak", "lavrak"))
	cipura := must(value(tayfa, "fish_species", "Çipura", "çipura"))
	book := must(value(tayfa, "book", "Levrek", "levrek"))
	theirs := must(value(other, "fish_species", "Levrek", "levrek"))

	for name, tc := range map[string]struct {
		ns, v, norm, constraint string
	}{
		"boş değer":            {"fish_species", "", "x", "tayfa_vocabulary_value"},
		"kırpılmamış değer":    {"fish_species", " Lüfer", "lüfer", "tayfa_vocabulary_value"},
		"uzun değer":           {"fish_species", strings.Repeat("a", 61), "x", "tayfa_vocabulary_value"},
		"bozuk ad alanı":       {"Fish", "Lüfer", "lüfer", "tayfa_vocabulary_namespace"},
		"aynı normalize biçim": {"fish_species", "LEVREK", "levrek", "tayfa_vocabulary_key"},
	} {
		_, err := value(tayfa, tc.ns, tc.v, tc.norm)
		if got := constraintOf(err); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu (%v)", name, got, tc.constraint, err)
		}
	}

	merge := func(src, dst id.ID) error {
		_, err := pool.Exec(ctx, `UPDATE tayfa_vocabulary SET merged_into = $2 WHERE id = $1`, src.PG(), dst.PG())
		return err
	}
	for name, tc := range map[string]struct {
		src, dst   id.ID
		constraint string
	}{
		"kendisine":        {levrek, levrek, "tayfa_vocabulary_not_self"},
		"başka ad alanı":   {levrek, book, "tayfa_vocabulary_merged_into_fkey"},
		"başka tayfa":      {levrek, theirs, "tayfa_vocabulary_merged_into_fkey"},
		"bilinmeyen hedef": {levrek, id.New(), "tayfa_vocabulary_merged_into_fkey"},
	} {
		if got := constraintOf(merge(tc.src, tc.dst)); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu", name, got, tc.constraint)
		}
	}
	if err := merge(lavrak, levrek); err != nil {
		t.Fatal(err)
	}
	// No chains: a merged value is no target, and a value others are merged
	// into cannot be merged before they move.
	if got := constraintOf(merge(cipura, lavrak)); got != "tayfa_vocabulary_guard" {
		t.Errorf("birleştirilmiş hedef: %q", got)
	}
	if got := constraintOf(merge(levrek, cipura)); got != "tayfa_vocabulary_guard" {
		t.Errorf("bağlı değerleri olan değer: %q", got)
	}
	// A value never changes its spelling or goes away; only a merge and
	// account deletion write to it.
	for _, sql := range []string{
		`UPDATE tayfa_vocabulary SET value = 'x' WHERE id = $1`,
		`UPDATE tayfa_vocabulary SET value_normalized = 'x' WHERE id = $1`,
		`UPDATE tayfa_vocabulary SET tayfa_id = gen_random_uuid() WHERE id = $1`,
		`UPDATE tayfa_vocabulary SET namespace = 'book' WHERE id = $1`,
		`DELETE FROM tayfa_vocabulary WHERE id = $1`,
	} {
		dbtest.WantDenied(t, pool, sql, cipura.PG())
	}
	if _, err := pool.Exec(ctx, `UPDATE tayfa_vocabulary SET created_by = NULL WHERE id = $1`, cipura.PG()); err != nil {
		t.Fatalf("ekleyeni boşaltma: %v", err)
	}

	// Links: the entry's tayfa, the field's namespace in the event's type
	// version.
	ev, err := insertEvent(ctx, pool, tayfa, "fishing", "in_person", nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := id.New()
	if _, err := pool.Exec(ctx, `INSERT INTO event_log_entries (id, event_id, component_key, user_id, client_request_id)
		VALUES ($1, $2, 'catches', gen_random_uuid(), gen_random_uuid())`, entry.PG(), ev.PG()); err != nil {
		t.Fatal(err)
	}
	link := func(field string, v id.ID) error {
		_, err := pool.Exec(ctx, `INSERT INTO event_log_entry_vocabulary (entry_id, field_key, vocabulary_id) VALUES ($1, $2, $3)`,
			entry.PG(), field, v.PG())
		return err
	}
	for name, tc := range map[string]struct {
		field string
		v     id.ID
	}{
		"başka tayfanın değeri":    {"species", theirs},
		"başka ad alanının değeri": {"species", book},
		"sözlüğe bağlı olmayan":    {"weight", levrek},
		"tanımda olmayan alan":     {"price", levrek},
	} {
		if got := constraintOf(link(tc.field, tc.v)); got != "event_log_entry_vocabulary_guard" {
			t.Errorf("%s: kısıt %q", name, got)
		}
	}
	if err := link("species", lavrak); err != nil {
		t.Fatalf("bağ: %v", err)
	}
	dbtest.WantDenied(t, pool, `UPDATE event_log_entry_vocabulary SET vocabulary_id = $1`, levrek.PG())
	dbtest.WantDenied(t, pool, `DELETE FROM event_log_entry_vocabulary WHERE vocabulary_id = $1`, lavrak.PG())
	// The link goes with the entry.
	if _, err := admin.Exec(ctx, `DELETE FROM event_log_entries WHERE id = $1`, entry.PG()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_log_entry_vocabulary`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("kayıtla bağ silinmedi: %d %v", n, err)
	}
}

// 00039 moves the text values of vocabulary fields out of field_values. The
// test database is migrated while empty, so the move is replayed on rows:
// back to the old shape, old rows in, forward, and back again.
func TestVocabularyMigrationMovesRows(t *testing.T) {
	pool, _ := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	up, down := migrationParts(t, coredb.Migrations(), "00039_tayfa_vocabulary.sql")
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	ev, err := insertEvent(ctx, pool, tayfa, "fishing", "in_person", nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := admin.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	exec(down)
	first, second, third, deleted := id.New(), id.New(), id.New(), id.New()
	const insert = `INSERT INTO event_log_entries (id, event_id, component_key, user_id, client_request_id, field_values, created_at, deleted_at)
		VALUES ($1, $2, 'catches', gen_random_uuid(), gen_random_uuid(), $3::text::jsonb, $4, $5)`
	at := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	var live *time.Time
	gone := at.Add(24 * time.Hour)
	exec(insert, first.PG(), ev.PG(), `{"species": "Levrek", "count": 2}`, at, live)
	exec(insert, second.PG(), ev.PG(), `{"species": "  LEVREK  "}`, at.Add(time.Hour), live)
	exec(insert, third.PG(), ev.PG(), `{"species": "Kırlangıç   balığı"}`, at.Add(2*time.Hour), live)
	exec(insert, deleted.PG(), ev.PG(), `{"species": "İstavrit"}`, at.Add(3*time.Hour), &gone)

	exec(up)
	if got := scalar(`SELECT string_agg(value || '=' || value_normalized, ',' ORDER BY value_normalized) FROM tayfa_vocabulary WHERE tayfa_id = $1`, tayfa.PG()); got != "İstavrit=istavrit,Kırlangıç balığı=kırlangıç balığı,Levrek=levrek" {
		t.Fatalf("sözlük: %s", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_log_entry_vocabulary x JOIN tayfa_vocabulary v ON v.id = x.vocabulary_id
		WHERE x.field_key = 'species' AND v.value = 'Levrek' AND x.entry_id IN ($1, $2)`, first.PG(), second.PG()); got != "2" {
		t.Fatalf("aynı değerin bağları: %s", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_log_entry_vocabulary`); got != "4" {
		t.Fatalf("bağ sayısı: %s", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_log_entries WHERE field_values ? 'species'`); got != "0" {
		t.Fatalf("metin değer kaldı: %s", got)
	}
	if got := scalar(`SELECT field_values::text FROM event_log_entries WHERE id = $1`, first.PG()); got != `{"count": 2}` {
		t.Fatalf("diğer değerler: %s", got)
	}

	// Back: the chosen value's spelling returns to the entry.
	exec(down)
	if got := scalar(`SELECT field_values ->> 'species' FROM event_log_entries WHERE id = $1`, second.PG()); got != "Levrek" {
		t.Fatalf("geri alınan değer: %s", got)
	}
	if got := scalar(`SELECT count(*)::text FROM information_schema.tables WHERE table_name IN ('tayfa_vocabulary', 'event_log_entry_vocabulary')`); got != "0" {
		t.Fatalf("tablolar kalmış: %s", got)
	}
	exec(up)
}
