package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The items of a list (event_checklist_items, 00036; the Faz 1 camp items
// moved there, issue #241) keep their CHECKs, the component guard and
// core_app's column privileges.
func TestChecklistConstraints(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	event := func(typ string) id.ID {
		t.Helper()
		eid, err := insertEvent(ctx, pool, tayfa, typ, "in_person", nil)
		if err != nil {
			t.Fatal(err)
		}
		return eid
	}
	camp, convoy := event("camp"), event("convoy")
	const item = `INSERT INTO event_checklist_items (id, event_id, component_key, name`
	for name, tc := range map[string]struct {
		sql        string
		on         id.ID
		constraint string
	}{
		"boş ad":                {item + `) VALUES (gen_random_uuid(), $1, 'gear', '')`, camp, "event_checklist_items_name_len"},
		"61 karakter ad":        {item + `) VALUES (gen_random_uuid(), $1, 'gear', repeat('ç', 61))`, camp, "event_checklist_items_name_len"},
		"sıfır adet":            {item + `, quantity) VALUES (gen_random_uuid(), $1, 'gear', 'Çadır', 0)`, camp, "event_checklist_items_quantity"},
		"1000 adet":             {item + `, quantity) VALUES (gen_random_uuid(), $1, 'gear', 'Çadır', 1000)`, camp, "event_checklist_items_quantity"},
		"türde olmayan anahtar": {item + `) VALUES (gen_random_uuid(), $1, 'duty', 'Nöbet')`, camp, "event_component_guard"},
		"bileşensiz tür":        {item + `) VALUES (gen_random_uuid(), $1, 'gear', 'Çadır')`, convoy, "event_component_guard"},
		"başka bileşen türü":    {item + `) VALUES (gen_random_uuid(), $1, 'vehicles', 'Çadır')`, convoy, "event_component_guard"},
	} {
		_, err := pool.Exec(ctx, tc.sql, tc.on.PG())
		if got := constraintOf(err); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu (%v)", name, got, tc.constraint, err)
		}
	}

	mine := id.New()
	if _, err := pool.Exec(ctx, item+`, quantity, assigned_user_id) VALUES ($1, $2, 'gear', 'Çadır', 999, gen_random_uuid())`, mine.PG(), camp.PG()); err != nil {
		t.Fatal(err)
	}
	// Only the assignee changes (and account deletion clears the people).
	if _, err := pool.Exec(ctx, `UPDATE event_checklist_items SET assigned_user_id = NULL, created_by = NULL, updated_at = now() WHERE id = $1`, mine.PG()); err != nil {
		t.Fatalf("atama: %v", err)
	}
	dbtest.WantDenied(t, pool, `UPDATE event_checklist_items SET event_id = event_id WHERE id = $1`, mine.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_checklist_items SET component_key = 'duty' WHERE id = $1`, mine.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_checklist_items SET name = 'x' WHERE id = $1`, mine.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_checklist_items SET quantity = 2 WHERE id = $1`, mine.PG())

	// Deleting the event removes its items.
	if _, err := pool.Exec(ctx, `DELETE FROM events WHERE id = $1`, camp.PG()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_checklist_items WHERE event_id = $1`, camp.PG()).Scan(&n); err != nil || n != 0 {
		t.Fatalf("silinen etkinliğin maddeleri kaldı: %d %v", n, err)
	}
}

// 00036 moves real rows (ADR-0041 item 3): the Faz 1 camp details become
// field values of the camp type and its items rows of the "gear" list; the
// two camp tables go and Down brings them back. The test database is
// migrated while empty, so the move is replayed here on rows: back to the
// old shape, old rows in, forward.
func TestChecklistMigrationMovesRows(t *testing.T) {
	pool, _ := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	up, down := migrationParts(t, coredb.Migrations(), "00036_component_checklist.sql")
	// 00066 moved the type to its second version (issue #260): its Down
	// comes first, as it would on the real way back.
	_, catalogDown := migrationParts(t, coredb.Migrations(), "00066_catalog_data.sql")
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(catalogDown)
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s *string
		if err := admin.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatal(err)
		}
		if s == nil {
			return ""
		}
		return *s
	}
	event := func(typ string) id.ID {
		t.Helper()
		eid, err := insertEvent(ctx, pool, tayfa, typ, "in_person", nil)
		if err != nil {
			t.Fatal(err)
		}
		return eid
	}
	full, bare, later := event("camp"), event("camp"), event("camp")
	all := []pgtype.UUID{full.PG(), bare.PG(), later.PG()}
	catalog := func() string {
		return scalar(`SELECT catalog_version::text FROM catalog_meta`)
	}
	before := catalog()

	// Rows written with the new shape: a field value and an item. Both
	// survive the way back.
	newItem, newUser := id.New(), id.New()
	exec(`INSERT INTO event_field_values (event_id, field_key, value) VALUES ($1, 'site_name', '"Assos"'), ($1, 'has_water', 'false')`, later.PG())
	exec(`INSERT INTO event_checklist_items (id, event_id, component_key, name, quantity, assigned_user_id) VALUES ($1, $2, 'gear', 'Ocak', 2, $3)`, newItem.PG(), later.PG(), newUser.PG())

	exec(down)
	// Every camp has its detail row again, with the values of its fields.
	if got := scalar(`SELECT count(*)::text FROM event_camp WHERE event_id = ANY($1)`, all); got != "3" {
		t.Fatalf("geri kurulan kamp detay satırları = %s", got)
	}
	if got := scalar(`SELECT concat_ws('|', site_name, has_water::text, coalesce(has_toilet::text, '-'), coalesce(notes, '-')) FROM event_camp WHERE event_id = $1`, later.PG()); got != "Assos|false|-|-" {
		t.Fatalf("geri alınan kamp ayrıntısı: %q", got)
	}
	if got := scalar(`SELECT concat_ws('|', name, quantity::text, assigned_user_id::text) FROM event_camp_items WHERE id = $1 AND event_id = $2`, newItem.PG(), later.PG()); got != "Ocak|2|"+newUser.String() {
		t.Fatalf("geri alınan madde: %q", got)
	}
	// The fields left the camp's definition and their values went.
	if got := scalar(`SELECT count(*)::text FROM event_field_values WHERE event_id = ANY($1)`, all); got != "0" {
		t.Fatalf("geri almada kalan alan değerleri: %s", got)
	}
	if got := scalar(`SELECT (definition ? 'fields')::text FROM catalog_event_type_versions WHERE type_key = 'camp' AND version = 1`); got != "false" {
		t.Fatalf("geri alınan kamp tanımında alan: %s", got)
	}
	if got := scalar(`SELECT count(*)::text FROM information_schema.tables WHERE table_name = 'event_checklist_items'`); got != "0" {
		t.Fatalf("bileşen tablosu kaldırılmadı: %s", got)
	}
	if catalog() == before {
		t.Fatal("geri almada katalog sürümü artmadı")
	}

	// Faz 1 rows, as the old code wrote them: a camp with every detail and
	// two items, one with none.
	driver := id.New()
	exec(`UPDATE event_camp SET site_name = 'Kazdağları', has_water = true, has_toilet = false, notes = 'Odun getirin' WHERE event_id = $1`, full.PG())
	tent, stove := id.New(), id.New()
	exec(`INSERT INTO event_camp_items (id, event_id, name, quantity, assigned_user_id, created_by, created_at)
		VALUES ($1, $3, 'Çadır', 2, $4, $4, '2026-10-01T08:00:00Z'), ($2, $3, 'Ocak', 1, NULL, NULL, '2026-10-01T09:00:00Z')`,
		tent.PG(), stove.PG(), full.PG(), driver.PG())

	beforeUp := catalog()
	exec(up)
	if got := scalar(`SELECT string_agg(field_key || '=' || value::text, ',' ORDER BY field_key) FROM event_field_values WHERE event_id = $1`, full.PG()); got != `has_toilet=false,has_water=true,notes="Odun getirin",site_name="Kazdağları"` {
		t.Fatalf("taşınan alanlar: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_field_values WHERE event_id = $1`, bare.PG()); got != "0" {
		t.Fatalf("boş kampa alan yazıldı: %s", got)
	}
	if got := scalar(`SELECT string_agg(field_key, ',' ORDER BY field_key) FROM event_field_values WHERE event_id = $1`, later.PG()); got != "has_water,site_name" {
		t.Fatalf("ikinci kez taşınan alanlar: %q", got)
	}
	if got := scalar(`SELECT concat_ws('|', component_key, name, quantity::text, assigned_user_id::text, created_by::text,
			(created_at = '2026-10-01T08:00:00Z')::text, (updated_at = created_at)::text)
		FROM event_checklist_items WHERE id = $1 AND event_id = $2`, tent.PG(), full.PG()); got != "gear|Çadır|2|"+driver.String()+"|"+driver.String()+"|true|true" {
		t.Fatalf("taşınan madde: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_checklist_items WHERE event_id = ANY($1)`, all); got != "3" {
		t.Fatalf("taşınan madde sayısı = %s", got)
	}
	// The camp's definition has the four fields, and the catalog moved on.
	if got := scalar(`SELECT string_agg(f ->> 'key', ',' ORDER BY f ->> 'key') FROM catalog_event_type_versions v
		CROSS JOIN LATERAL jsonb_array_elements(v.definition -> 'fields') AS f WHERE v.type_key = 'camp' AND v.version = 1`); got != "has_toilet,has_water,notes,site_name" {
		t.Fatalf("kamp tanımı alanları: %q", got)
	}
	if got := scalar(`SELECT (t.definition = v.definition)::text FROM catalog_event_types t
		JOIN catalog_event_type_versions v ON v.type_key = t.key AND v.version = t.version WHERE t.key = 'camp'`); got != "true" {
		t.Fatalf("güncel tanım ile sürüm kopyası farklı: %s", got)
	}
	if catalog() == beforeUp {
		t.Fatal("katalog sürümü artmadı")
	}
	// The old tables are gone.
	if got := scalar(`SELECT count(*)::text FROM information_schema.tables WHERE table_name IN ('event_camp', 'event_camp_items')`); got != "0" {
		t.Fatalf("eski tablolar kaldırılmadı: %s", got)
	}
	// core_app keeps its privileges on the table the migration made.
	if _, err := pool.Exec(ctx, `UPDATE event_checklist_items SET assigned_user_id = NULL WHERE id = $1`, tent.PG()); err != nil {
		t.Fatalf("taşınan maddeyi güncelleme: %v", err)
	}
}
