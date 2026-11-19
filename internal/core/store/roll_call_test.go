package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The roll call tables (event_roll_calls, event_roll_call_checkins, 00037;
// the Faz 1 hike roll call moved there, issue #242) keep their CHECKs, the
// component guard and core_app's column privileges.
func TestRollCallConstraints(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	event := func(typ string, extra map[string]string) id.ID {
		t.Helper()
		eid, err := insertEvent(ctx, pool, tayfa, typ, "in_person", extra)
		if err != nil {
			t.Fatal(err)
		}
		return eid
	}
	hike, camp := event("hike", nil), event("camp", nil)
	const checkin = `INSERT INTO event_roll_call_checkins (event_id, component_key, user_id, started_at, started_received_at`
	for name, tc := range map[string]struct {
		sql        string
		on         id.ID
		constraint string
	}{
		"bileşensiz türde plan":    {`INSERT INTO event_roll_calls (event_id, component_key, expected_return_at) VALUES ($1, 'roll_call', now())`, camp, "event_component_guard"},
		"türde olmayan anahtar":    {`INSERT INTO event_roll_calls (event_id, component_key, expected_return_at) VALUES ($1, 'roll_call_2', now())`, hike, "event_component_guard"},
		"bileşensiz türde yoklama": {checkin + `) VALUES ($1, 'roll_call', gen_random_uuid(), now(), now())`, camp, "event_component_guard"},
		"gelecekte çıkış":          {checkin + `) VALUES ($1, 'roll_call', gen_random_uuid(), now() + interval '6 minutes', now())`, hike, "event_roll_call_checkins_started_at"},
		"çok eski çıkış":           {checkin + `) VALUES ($1, 'roll_call', gen_random_uuid(), now() - interval '49 hours', now())`, hike, "event_roll_call_checkins_started_at"},
		"alınma anı olmayan dönüş": {checkin + `, returned_at) VALUES ($1, 'roll_call', gen_random_uuid(), now(), now(), now())`, hike, "event_roll_call_checkins_returned"},
		"çıkıştan önce dönüş": {checkin + `, returned_at, returned_received_at)
			VALUES ($1, 'roll_call', gen_random_uuid(), now(), now(), now() - interval '1 minute', now())`, hike, "event_roll_call_checkins_order"},
	} {
		_, err := pool.Exec(ctx, tc.sql, tc.on.PG())
		if got := constraintOf(err); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu (%v)", name, got, tc.constraint, err)
		}
	}

	user := id.New()
	if _, err := pool.Exec(ctx, checkin+`) VALUES ($1, 'roll_call', $2, now(), now())`, hike.PG(), user.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO event_roll_calls (event_id, component_key, expected_return_at) VALUES ($1, 'roll_call', now() + interval '2 days')`, hike.PG()); err != nil {
		t.Fatal(err)
	}
	// Only the return and the alarm are written to a record; the plan's
	// time changes; nothing moves to another event, component or person.
	if _, err := pool.Exec(ctx, `UPDATE event_roll_call_checkins SET returned_at = now(), returned_received_at = now(), alert_sent_at = now() WHERE event_id = $1`, hike.PG()); err != nil {
		t.Fatalf("dönüş: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE event_roll_calls SET expected_return_at = expected_return_at + interval '1 hour', updated_at = now() WHERE event_id = $1`, hike.PG()); err != nil {
		t.Fatalf("plan: %v", err)
	}
	dbtest.WantDenied(t, pool, `UPDATE event_roll_call_checkins SET started_at = started_at WHERE event_id = $1`, hike.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_roll_call_checkins SET user_id = gen_random_uuid() WHERE event_id = $1`, hike.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_roll_call_checkins SET event_id = event_id WHERE event_id = $1`, hike.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_roll_call_checkins SET component_key = 'x' WHERE event_id = $1`, hike.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_roll_calls SET event_id = event_id WHERE event_id = $1`, hike.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_roll_calls SET component_key = 'x' WHERE event_id = $1`, hike.PG())

	// A series occurrence gets the template's plan, moved with its start.
	start := time.Date(2026, 12, 6, 8, 0, 0, 0, time.UTC)
	lit := func(at time.Time) string { return "'" + at.Format(time.RFC3339) + "'" }
	tmpl := event("hike", map[string]string{"starts_at": lit(start), "ends_at": lit(start.Add(6 * time.Hour))})
	next := event("hike", map[string]string{"starts_at": lit(start.AddDate(0, 0, 7)), "ends_at": lit(start.AddDate(0, 0, 7).Add(6 * time.Hour))})
	if _, err := pool.Exec(ctx, `INSERT INTO event_roll_calls (event_id, component_key, expected_return_at) VALUES ($1, 'roll_call', $2)`,
		tmpl.PG(), start.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := q.CopyRollCallPlans(ctx, CopyRollCallPlansParams{ToEventID: next.PG(), FromEventID: tmpl.PG()}); err != nil {
		t.Fatal(err)
	}
	p, err := q.GetRollCallPlan(ctx, GetRollCallPlanParams{EventID: next.PG(), ComponentKey: "roll_call"})
	if err != nil {
		t.Fatal(err)
	}
	if want := start.AddDate(0, 0, 7).Add(5 * time.Hour); !p.ExpectedReturnAt.Time.Equal(want) {
		t.Fatalf("kopyalanan beklenen dönüş = %v, %v bekleniyordu", p.ExpectedReturnAt.Time, want)
	}

	// Deleting the event removes its plan and records.
	if _, err := pool.Exec(ctx, `DELETE FROM events WHERE id = $1`, hike.PG()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM event_roll_call_checkins WHERE event_id = $1) + (SELECT count(*) FROM event_roll_calls WHERE event_id = $1)`, hike.PG()).Scan(&n); err != nil || n != 0 {
		t.Fatalf("silinen etkinliğin yoklaması kaldı: %d %v", n, err)
	}
}

// roll_call_alarm_minutes reads the alarm of a roll call definition: the
// grace period when the alarm is on, NULL otherwise (the overdue job skips
// those).
func TestRollCallAlarmMinutes(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	for def, want := range map[string]string{
		`{"kind": "roll_call", "params": {"alarm": true, "return_grace_minutes": 45}}`:  "45",
		`{"kind": "roll_call", "params": {"alarm": true}}`:                              "30",
		`{"kind": "roll_call", "params": {"alarm": false, "return_grace_minutes": 45}}`: "-",
		`{"kind": "roll_call"}`:                        "-",
		`{"kind": "route", "params": {"alarm": true}}`: "-",
		`null`: "-",
	} {
		var got string
		if err := pool.QueryRow(ctx, `SELECT coalesce(roll_call_alarm_minutes($1::jsonb)::text, '-')`, def).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: %s, %s bekleniyordu", def, got, want)
		}
	}
}

// 00037 moves real rows (ADR-0041 item 3): the Faz 1 hike details become
// field values of the hike type, its expected return the roll call's plan
// and its roll call rows the component's; the two hike tables go and Down
// brings them back. The test database is migrated while empty, so the move
// is replayed here on rows: back to the old shape, old rows in, forward.
func TestRollCallMigrationMovesRows(t *testing.T) {
	pool, _ := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	up, down := migrationParts(t, coredb.Migrations(), "00037_component_roll_call.sql")
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
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
	full, bare, later := event("hike"), event("hike"), event("hike")
	all := []pgtype.UUID{full.PG(), bare.PG(), later.PG()}
	catalog := func() string {
		return scalar(`SELECT catalog_version::text FROM catalog_meta`)
	}
	before := catalog()
	back := time.Date(2026, 12, 6, 15, 0, 0, 0, time.UTC)

	// Rows written with the new shape: a field value, a plan and a record.
	// They survive the way back.
	walker := id.New()
	exec(`INSERT INTO event_field_values (event_id, field_key, value) VALUES ($1, 'difficulty', '2')`, later.PG())
	exec(`INSERT INTO event_roll_calls (event_id, component_key, expected_return_at) VALUES ($1, 'roll_call', $2)`, later.PG(), back)
	exec(`INSERT INTO event_roll_call_checkins (event_id, component_key, user_id, started_at, started_received_at) VALUES ($1, 'roll_call', $2, now(), now())`,
		later.PG(), walker.PG())

	exec(down)
	// Every hike has its detail row again, with its fields and its plan.
	if got := scalar(`SELECT count(*)::text FROM event_hike WHERE event_id = ANY($1)`, all); got != "3" {
		t.Fatalf("geri kurulan yürüyüş detay satırları = %s", got)
	}
	if got := scalar(`SELECT concat_ws('|', difficulty::text, coalesce(distance_km::text, '-'), (expected_return_at = $2)::text, return_grace_minutes::text)
		FROM event_hike WHERE event_id = $1`, later.PG(), back); got != "2|-|true|30" {
		t.Fatalf("geri alınan yürüyüş ayrıntısı: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_hike_checkins WHERE event_id = $1 AND user_id = $2 AND returned_at IS NULL`, later.PG(), walker.PG()); got != "1" {
		t.Fatalf("geri alınan yoklama: %s", got)
	}
	// The fields left the hike's definition and their values went.
	if got := scalar(`SELECT count(*)::text FROM event_field_values WHERE event_id = ANY($1)`, all); got != "0" {
		t.Fatalf("geri almada kalan alan değerleri: %s", got)
	}
	if got := scalar(`SELECT (definition ? 'fields')::text FROM catalog_event_type_versions WHERE type_key = 'hike' AND version = 1`); got != "false" {
		t.Fatalf("geri alınan yürüyüş tanımında alan: %s", got)
	}
	if got := scalar(`SELECT count(*)::text FROM information_schema.tables WHERE table_name IN ('event_roll_calls', 'event_roll_call_checkins')`); got != "0" {
		t.Fatalf("bileşen tabloları kaldırılmadı: %s", got)
	}
	if catalog() == before {
		t.Fatal("geri almada katalog sürümü artmadı")
	}

	// Faz 1 rows, as the old code wrote them: a hike with every detail and
	// two roll calls (one out with the alarm already sent, one back), one
	// with none.
	out, home := id.New(), id.New()
	exec(`UPDATE event_hike SET difficulty = 4, distance_km = 12.5, elevation_m = 800, expected_return_at = $2, return_grace_minutes = 30 WHERE event_id = $1`,
		full.PG(), back)
	exec(`INSERT INTO event_hike_checkins (event_id, user_id, started_at, started_received_at, alert_sent_at)
		VALUES ($1, $2, '2026-12-06T08:00:00Z', '2026-12-06T08:01:00Z', '2026-12-06T15:30:00Z')`, full.PG(), out.PG())
	exec(`INSERT INTO event_hike_checkins (event_id, user_id, started_at, started_received_at, returned_at, returned_received_at)
		VALUES ($1, $2, '2026-12-06T08:00:00Z', '2026-12-06T08:00:00Z', '2026-12-06T14:00:00Z', '2026-12-06T14:00:00Z')`, full.PG(), home.PG())

	beforeUp := catalog()
	exec(up)
	if got := scalar(`SELECT string_agg(field_key || '=' || value::text, ',' ORDER BY field_key) FROM event_field_values WHERE event_id = $1`, full.PG()); got != `difficulty=4,planned_distance=12.5,planned_elevation=800` {
		t.Fatalf("taşınan alanlar: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_field_values WHERE event_id = $1`, bare.PG()); got != "0" {
		t.Fatalf("boş yürüyüşe alan yazıldı: %s", got)
	}
	if got := scalar(`SELECT string_agg(field_key, ',' ORDER BY field_key) FROM event_field_values WHERE event_id = $1`, later.PG()); got != "difficulty" {
		t.Fatalf("ikinci kez taşınan alanlar: %q", got)
	}
	// Plans: the two hikes with an expected return; none for the bare one.
	if got := scalar(`SELECT string_agg(component_key || '=' || (expected_return_at = $2)::text, ',') FROM event_roll_calls WHERE event_id = $1`, full.PG(), back); got != "roll_call=true" {
		t.Fatalf("taşınan plan: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_roll_calls WHERE event_id = ANY($1)`, all); got != "2" {
		t.Fatalf("taşınan plan sayısı = %s", got)
	}
	// Records keep their times and the alarm already sent.
	if got := scalar(`SELECT concat_ws('|', component_key, (started_received_at = '2026-12-06T08:01:00Z')::text, coalesce(returned_at::text, '-'),
			(alert_sent_at = '2026-12-06T15:30:00Z')::text)
		FROM event_roll_call_checkins WHERE event_id = $1 AND user_id = $2`, full.PG(), out.PG()); got != "roll_call|true|-|true" {
		t.Fatalf("taşınan yoklama: %q", got)
	}
	if got := scalar(`SELECT (returned_at = '2026-12-06T14:00:00Z')::text FROM event_roll_call_checkins WHERE event_id = $1 AND user_id = $2`, full.PG(), home.PG()); got != "true" {
		t.Fatalf("taşınan dönüş: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_roll_call_checkins WHERE event_id = ANY($1)`, all); got != "3" {
		t.Fatalf("taşınan yoklama sayısı = %s", got)
	}
	// The hike's definition has the three fields, and the catalog moved on.
	if got := scalar(`SELECT string_agg(f ->> 'key', ',' ORDER BY f ->> 'key') FROM catalog_event_type_versions v
		CROSS JOIN LATERAL jsonb_array_elements(v.definition -> 'fields') AS f WHERE v.type_key = 'hike' AND v.version = 1`); got != "difficulty,planned_distance,planned_elevation" {
		t.Fatalf("yürüyüş tanımı alanları: %q", got)
	}
	if got := scalar(`SELECT (t.definition = v.definition)::text FROM catalog_event_types t
		JOIN catalog_event_type_versions v ON v.type_key = t.key AND v.version = t.version WHERE t.key = 'hike'`); got != "true" {
		t.Fatalf("güncel tanım ile sürüm kopyası farklı: %s", got)
	}
	if catalog() == beforeUp {
		t.Fatal("katalog sürümü artmadı")
	}
	// The old tables are gone.
	if got := scalar(`SELECT count(*)::text FROM information_schema.tables WHERE table_name IN ('event_hike', 'event_hike_checkins')`); got != "0" {
		t.Fatalf("eski tablolar kaldırılmadı: %s", got)
	}
	// core_app keeps its privileges on the tables the migration made.
	if _, err := pool.Exec(ctx, `UPDATE event_roll_call_checkins SET returned_at = '2026-12-06T16:00:00Z', returned_received_at = '2026-12-06T16:00:00Z'
		WHERE event_id = $1 AND user_id = $2`, full.PG(), out.PG()); err != nil {
		t.Fatalf("taşınan yoklamayı güncelleme: %v", err)
	}
}
