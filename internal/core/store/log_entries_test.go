package store

import (
	"context"
	"testing"
	"time"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The log table (event_log_entries, 00038; the Faz 1 fishing catches moved
// there, issue #243) keeps its CHECKs, the component guard and core_app's
// column privileges.
func TestLogEntryConstraints(t *testing.T) {
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
	fishing, camp := event("fishing"), event("camp")
	const insert = `INSERT INTO event_log_entries (id, event_id, component_key, user_id, client_request_id, field_values, lat, lng, location_visibility)
		VALUES (gen_random_uuid(), $1, `
	for name, tc := range map[string]struct {
		sql        string
		on         id.ID
		constraint string
	}{
		"bileşensiz türde kayıt": {insert + `'catches', gen_random_uuid(), gen_random_uuid(), '{}', NULL, NULL, NULL)`, camp, "event_component_guard"},
		"türde olmayan anahtar":  {insert + `'sightings', gen_random_uuid(), gen_random_uuid(), '{}', NULL, NULL, NULL)`, fishing, "event_component_guard"},
		"nesne olmayan değerler": {insert + `'catches', gen_random_uuid(), gen_random_uuid(), '[]', NULL, NULL, NULL)`, fishing, "event_log_entries_values"},
		"yarım konum":            {insert + `'catches', gen_random_uuid(), gen_random_uuid(), '{}', 39.5, NULL, 'self')`, fishing, "event_log_entries_location"},
		"enlem 90'dan büyük":     {insert + `'catches', gen_random_uuid(), gen_random_uuid(), '{}', 91, 26.9, 'self')`, fishing, "event_log_entries_lat"},
		"görünürlüksüz konum":    {insert + `'catches', gen_random_uuid(), gen_random_uuid(), '{}', 39.5, 26.9, NULL)`, fishing, "event_log_entries_location_visibility"},
		"konumsuz görünürlük":    {insert + `'catches', gen_random_uuid(), gen_random_uuid(), '{}', NULL, NULL, 'self')`, fishing, "event_log_entries_location_visibility"},
		"bilinmeyen görünürlük":  {insert + `'catches', gen_random_uuid(), gen_random_uuid(), '{}', 39.5, 26.9, 'public')`, fishing, "event_log_entries_location_visibility"},
	} {
		_, err := pool.Exec(ctx, tc.sql, tc.on.PG())
		if got := constraintOf(err); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu (%v)", name, got, tc.constraint, err)
		}
	}

	user, crid := id.New(), id.New()
	if _, err := pool.Exec(ctx, insert+`'catches', $2, $3, '{"species": "Levrek"}', 39.5, 26.9, 'participants')`,
		fishing.PG(), user.PG(), crid.PG()); err != nil {
		t.Fatal(err)
	}
	// One client request ID is one entry of its author.
	_, err := pool.Exec(ctx, insert+`'catches', $2, $3, '{}', NULL, NULL, NULL)`, fishing.PG(), user.PG(), crid.PG())
	if got := constraintOf(err); got != "event_log_entries_client_request_key" {
		t.Errorf("tekrar gönderim: kısıt %q (%v)", got, err)
	}
	// Only the soft delete is written; nothing moves or changes.
	if _, err := pool.Exec(ctx, `UPDATE event_log_entries SET deleted_at = now(), deleted_by = $2 WHERE event_id = $1`, fishing.PG(), user.PG()); err != nil {
		t.Fatalf("silme: %v", err)
	}
	for _, sql := range []string{
		`UPDATE event_log_entries SET field_values = '{}' WHERE event_id = $1`,
		`UPDATE event_log_entries SET lat = 0, lng = 0 WHERE event_id = $1`,
		`UPDATE event_log_entries SET location_visibility = 'tayfa' WHERE event_id = $1`,
		`UPDATE event_log_entries SET user_id = gen_random_uuid() WHERE event_id = $1`,
		`UPDATE event_log_entries SET event_id = event_id WHERE event_id = $1`,
		`UPDATE event_log_entries SET component_key = 'x' WHERE event_id = $1`,
		`UPDATE event_log_entries SET photo_upload_id = NULL WHERE event_id = $1`,
	} {
		dbtest.WantDenied(t, pool, sql, fishing.PG())
	}

	// Deleting the event removes its entries.
	if _, err := pool.Exec(ctx, `DELETE FROM events WHERE id = $1`, fishing.PG()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_log_entries WHERE event_id = $1`, fishing.PG()).Scan(&n); err != nil || n != 0 {
		t.Fatalf("silinen etkinliğin kayıtları kaldı: %d %v", n, err)
	}
}

// 00038 moves real rows (ADR-0041 item 3): the Faz 1 fishing catches become
// entries of the fishing type's catches log, with the spot rule folded into
// each location's visibility; their photos and reports follow under the new
// names, and Down brings the old tables back. The test database is
// migrated while empty, so the move is replayed here on rows: back to the
// old shape, old rows in, forward.
func TestLogMigrationMovesRows(t *testing.T) {
	pool, q := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	up, down := migrationParts(t, coredb.Migrations(), "00038_component_log.sql")
	owner := id.New()
	tayfa := newTayfa(t, pool, owner, tayfaOpts{})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
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
	open, secret := event("fishing"), event("fishing")
	catalog := func() string {
		return scalar(`SELECT catalog_version::text FROM catalog_meta`)
	}
	before := catalog()

	// Entries written with the new shape: one Faz 1 can hold, one without a
	// length (dropped on the way back).
	kept, dropped := id.New(), id.New()
	exec(`INSERT INTO event_log_entries (id, event_id, component_key, user_id, field_values, lat, lng, location_visibility, client_request_id)
		VALUES ($1, $2, 'catches', $3, '{"species": "Çipura", "length_cm": 31.5, "weight": 0.6}', 39.5, 26.9, 'participants', gen_random_uuid())`,
		kept.PG(), secret.PG(), owner.PG())
	exec(`INSERT INTO event_log_entries (id, event_id, component_key, user_id, field_values, client_request_id)
		VALUES ($1, $2, 'catches', $3, '{"species": "Kefal", "count": 3}', gen_random_uuid())`, dropped.PG(), open.PG(), owner.PG())

	exec(down)
	if got := scalar(`SELECT string_agg(spot_visibility, ',' ORDER BY (event_id = $1)) FROM event_fishing WHERE event_id IN ($1, $2)`, open.PG(), secret.PG()); got != "participants,tayfa" {
		t.Fatalf("geri kurulan mera görünürlükleri: %q", got)
	}
	if got := scalar(`SELECT concat_ws('|', species, length_cm::text, visibility, lat::text) FROM fishing_catches WHERE id = $1`, kept.PG()); got != "Çipura|31.5|tayfa|39.5" {
		t.Fatalf("geri alınan av kaydı: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM fishing_catches WHERE id = $1`, dropped.PG()); got != "0" {
		t.Fatalf("boysuz kayıt geri alındı: %s", got)
	}
	if got := scalar(`SELECT count(*)::text FROM information_schema.tables WHERE table_name = 'event_log_entries'`); got != "0" {
		t.Fatalf("bileşen tablosu kaldırılmadı: %s", got)
	}
	if catalog() == before {
		t.Fatal("geri almada katalog sürümü artmadı")
	}

	// Faz 1 rows, as the old code wrote them: on the open spot a catch kept
	// to oneself with a photo and a shared one; on the secret spot a shared
	// one; a deleted one without a location.
	selfCatch, shared, secretCatch, gone := id.New(), id.New(), id.New(), id.New()
	exec(`UPDATE event_fishing SET spot_visibility = 'participants' WHERE event_id = $1`, secret.PG())
	exec(`UPDATE event_fishing SET spot_visibility = 'tayfa' WHERE event_id = $1`, open.PG())
	photo := readyUpload(t, q, owner, uploadOpts{purpose: "catch_photo", tayfa: tayfa})
	if n, err := q.AttachUpload(ctx, AttachUploadParams{
		ID: photo.PG(), UserID: owner.PG(), Purpose: "catch_photo", AttachedType: "fishing_catch", AttachedID: selfCatch.PG(),
		TayfaID: tayfa.PG(), Now: ts(time.Now()),
	}); err != nil || n != 1 {
		t.Fatalf("bağlama: %d %v", n, err)
	}
	const catch = `INSERT INTO fishing_catches (id, event_id, user_id, species, length_cm, lat, lng, visibility, client_request_id, photo_upload_id, deleted_at, deleted_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, gen_random_uuid(), $9, $10, $11)`
	var none *float64
	lat, lng := 39.58, 26.92
	exec(catch, selfCatch.PG(), open.PG(), owner.PG(), "Levrek", 42.5, lat, lng, "self", photo.PG(), nil, nil)
	exec(catch, shared.PG(), open.PG(), owner.PG(), "Lüfer", 28, lat, lng, "tayfa", nil, nil, nil)
	exec(catch, secretCatch.PG(), secret.PG(), owner.PG(), "Sinarit", 60, lat, lng, "tayfa", nil, nil, nil)
	exec(catch, gone.PG(), open.PG(), owner.PG(), "İstavrit", 15, none, none, "self", nil, time.Now(), owner.PG())
	exec(`INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, reason, subject_user_id)
		VALUES (gen_random_uuid(), $1, gen_random_uuid(), 'fishing_catch', $2, 'spam', $3)`, tayfa.PG(), shared.PG(), owner.PG())

	beforeUp := catalog()
	exec(up)
	for c, want := range map[id.ID]string{
		selfCatch:   `catches|{"species": "Levrek", "length_cm": 42.5}|self|t|f`,
		shared:      `catches|{"species": "Lüfer", "length_cm": 28}|tayfa|f|f`,
		secretCatch: `catches|{"species": "Sinarit", "length_cm": 60}|participants|f|f`,
		gone:        `catches|{"species": "İstavrit", "length_cm": 15}|-|f|t`,
	} {
		got := scalar(`SELECT concat_ws('|', component_key, field_values::text, coalesce(location_visibility, '-'),
				(photo_upload_id IS NOT NULL)::text::char, (deleted_at IS NOT NULL)::text::char)
			FROM event_log_entries WHERE id = $1`, c.PG())
		if got != want {
			t.Errorf("taşınan kayıt: %q, %q bekleniyordu", got, want)
		}
	}
	// The photo and the report follow under the new names.
	if got := scalar(`SELECT concat_ws('|', purpose, attached_type, status) FROM uploads WHERE id = $1`, photo.PG()); got != "log_photo|log_entry|ready" {
		t.Fatalf("taşınan fotoğraf: %q", got)
	}
	if got := scalar(`SELECT target_type FROM reports WHERE target_id = $1`, shared.PG()); got != "log_entry" {
		t.Fatalf("taşınan şikâyet: %q", got)
	}
	// The fishing definition has the Notion fields, and the catalog moved on.
	if got := scalar(`SELECT string_agg(f.e ->> 'key', ',' ORDER BY f.n) FROM catalog_event_type_versions v
		CROSS JOIN LATERAL jsonb_array_elements(v.definition -> 'components' -> 1 -> 'params' -> 'fields') WITH ORDINALITY AS f(e, n)
		WHERE v.type_key = 'fishing' AND v.version = 1`); got != "species,count,weight,length_cm" {
		t.Fatalf("balıkçılık günlüğü alanları: %q", got)
	}
	if got := scalar(`SELECT (t.definition = v.definition)::text FROM catalog_event_types t
		JOIN catalog_event_type_versions v ON v.type_key = t.key AND v.version = t.version WHERE t.key = 'fishing'`); got != "true" {
		t.Fatalf("güncel tanım ile sürüm kopyası farklı: %s", got)
	}
	if catalog() == beforeUp {
		t.Fatal("katalog sürümü artmadı")
	}
	// The old tables are gone.
	if got := scalar(`SELECT count(*)::text FROM information_schema.tables WHERE table_name IN ('fishing_catches', 'event_fishing')`); got != "0" {
		t.Fatalf("eski tablolar kaldırılmadı: %s", got)
	}
	// core_app keeps its privileges on the table the migration made.
	if _, err := pool.Exec(ctx, `UPDATE event_log_entries SET deleted_at = now(), deleted_by = $2 WHERE id = $1`, shared.PG(), owner.PG()); err != nil {
		t.Fatalf("taşınan kaydı silme: %v", err)
	}
}
