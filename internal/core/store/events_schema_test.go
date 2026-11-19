package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Schema tests for 00004_events.sql. The app layer validates the same rules;
// these pin the database's second line of defence.

// insertEvent writes an event row directly; extra is appended to the column
// and value lists ("col = value" pairs are not used so that a test can omit
// a required column).
func insertEvent(ctx context.Context, pool *pgxpool.Pool, tayfa id.ID, typ, mode string, extra map[string]string) (id.ID, error) {
	eid := id.New()
	cols := []string{"id", "tayfa_id", "type", "mode", "title", "starts_at", "ends_at"}
	vals := []string{"$1", "$2", "$3", "$4", "'Pazar sürüşü'", "now() + interval '1 day'", "now() + interval '1 day 3 hours'"}
	switch mode {
	case "in_person":
		cols = append(cols, "meeting_lat", "meeting_lng", "city", "district")
		vals = append(vals, "38.42", "27.14", "'İzmir'", "'Konak'")
	case "online":
		cols = append(cols, "online_url")
		vals = append(vals, "'https://example.com/oda'")
	}
	for c, v := range extra {
		replaced := false
		for i := range cols {
			if cols[i] == c {
				vals[i], replaced = v, true
			}
		}
		if !replaced {
			cols = append(cols, c)
			vals = append(vals, v)
		}
	}
	// Test-only SQL: column names and values are fixed literals of this file.
	sql := fmt.Sprintf("INSERT INTO events (%s) VALUES (%s)", strings.Join(cols, ", "), strings.Join(vals, ", "))
	_, err := pool.Exec(ctx, sql, eid.PG(), tayfa.PG(), typ, mode)
	return eid, err
}

func TestEventTypeIntegrity(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})

	ids := map[string]id.ID{}
	for _, typ := range []string{"convoy", "camp", "hike", "fishing", "gaming", "sports", "general"} {
		eid, err := insertEvent(ctx, pool, tayfa, typ, "in_person", nil)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		ids[typ] = eid
	}

	details := map[string]string{
		"fishing": `INSERT INTO event_fishing (event_id) VALUES ($1)`,
	}
	for typ, sql := range details {
		// Every other event type is refused by the composite foreign key.
		for other, eid := range ids {
			if other == typ {
				continue
			}
			_, err := pool.Exec(ctx, sql, eid.PG())
			if c := constraintOf(err); c != "event_"+typ+"_event_fkey" {
				t.Errorf("%s detayı %s etkinliğine eklendi ya da başka hata: %v", typ, other, err)
			}
		}
		if _, err := pool.Exec(ctx, sql, ids[typ].PG()); err != nil {
			t.Errorf("%s detayı kendi türüne eklenemedi: %v", typ, err)
		}
	}

	// The fixed type column cannot be changed to point elsewhere.
	_, err := pool.Exec(ctx, `UPDATE event_fishing SET type = 'hike' WHERE event_id = $1`, ids["fishing"].PG())
	if c := constraintOf(err); c != "event_fishing_type" {
		t.Errorf("event_fishing.type değişti: %v", err)
	}
	// The event's type cannot change while a detail row points at it.
	_, err = pool.Exec(ctx, `UPDATE events SET type = 'hike' WHERE id = $1`, ids["fishing"].PG())
	if c := constraintOf(err); c != "event_fishing_event_fkey" {
		t.Errorf("detaylı etkinliğin türü değişti: %v", err)
	}

	// Vehicles and list items are components' (event_vehicles, 00035;
	// event_checklist_items, 00036): the component guard refuses a type
	// without the component.
	_, err = pool.Exec(ctx, `INSERT INTO event_vehicles (id, event_id, component_key, user_id, make) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), 'Murat')`, ids["camp"].PG())
	if c := constraintOf(err); c != "event_component_guard" {
		t.Errorf("kamp etkinliğine araç eklendi: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO event_checklist_items (id, event_id, component_key, name) VALUES (gen_random_uuid(), $1, 'gear', 'Çadır')`, ids["convoy"].PG())
	if c := constraintOf(err); c != "event_component_guard" {
		t.Errorf("konvoy etkinliğine liste maddesi eklendi: %v", err)
	}

	// Deleting the event removes its details, vehicles (with their seat
	// requests), items and RSVPs.
	vehicle := id.New()
	if _, err := pool.Exec(ctx, `INSERT INTO event_vehicles (id, event_id, component_key, user_id, make, plate, seats) VALUES ($2, $1, 'vehicles', gen_random_uuid(), 'Murat', '35 ABC 123', 1)`, ids["convoy"].PG(), vehicle.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO event_vehicle_seat_requests (id, event_id, component_key, user_id, event_vehicle_id) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), $2)`, ids["convoy"].PG(), vehicle.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO event_rsvps (event_id, user_id, status) VALUES ($1, gen_random_uuid(), 'going')`, ids["convoy"].PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM events WHERE id = $1`, ids["convoy"].PG()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"event_vehicles", "event_vehicle_seat_requests", "event_rsvps"} {
		var n int
		// Test-only SQL: table names are fixed literals of this file.
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE event_id = $1", ids["convoy"].PG()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s: etkinlik silinince %d satır kaldı", table, n)
		}
	}
}

func TestEventConstraints(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})

	for name, tc := range map[string]struct {
		mode       string
		extra      map[string]string
		constraint string
	}{
		"boş başlık":              {"in_person", map[string]string{"title": "''"}, "events_title_len"},
		"121 karakter başlık":     {"in_person", map[string]string{"title": "repeat('ğ', 121)"}, "events_title_len"},
		"5001 karakter açıklama":  {"in_person", map[string]string{"description": "repeat('ş', 5001)"}, "events_description_len"},
		"bitiş başlangıçtan önce": {"in_person", map[string]string{"ends_at": "now()"}, "events_time"},
		"enlem 90'dan büyük":      {"in_person", map[string]string{"meeting_lat": "90.0001"}, "events_meeting_lat"},
		"boylam -180'den küçük":   {"in_person", map[string]string{"meeting_lng": "-180.5"}, "events_meeting_lng"},
		"yalnız enlem":            {"in_person", map[string]string{"meeting_lng": "NULL"}, "events_meeting_point"},
		"yüz yüzede il yok":       {"in_person", map[string]string{"city": "NULL"}, "events_mode_fields"},
		"yüz yüzede ilçe yok":     {"in_person", map[string]string{"district": "NULL"}, "events_mode_fields"},
		"yüz yüzede konum yok":    {"in_person", map[string]string{"meeting_lat": "NULL", "meeting_lng": "NULL"}, "events_mode_fields"},
		"yüz yüzede bağlantı":     {"in_person", map[string]string{"online_url": "'https://example.com'"}, "events_mode_fields"},
		"online'da bağlantı yok":  {"online", map[string]string{"online_url": "NULL"}, "events_mode_fields"},
		"online'da il":            {"online", map[string]string{"city": "'İzmir'"}, "events_mode_fields"},
		"online'da konum":         {"online", map[string]string{"meeting_lat": "1", "meeting_lng": "1"}, "events_mode_fields"},
		"http bağlantı":           {"online", map[string]string{"online_url": "'http://example.com'"}, "events_online_url"},
		"javascript bağlantı":     {"online", map[string]string{"online_url": "'javascript:alert(1)'"}, "events_online_url"},
		"yalnız şema":             {"online", map[string]string{"online_url": "'https://'"}, "events_online_url"},
		"sıfır kapasite":          {"in_person", map[string]string{"capacity": "0"}, "events_capacity"},
		// CHECKs run in name order; events_cancellation comes first.
		"bilinmeyen durum":          {"in_person", map[string]string{"status": "'deleted'"}, "events_cancellation"},
		"zamansız iptal":            {"in_person", map[string]string{"status": "'cancelled'"}, "events_cancellation"},
		"planlıda iptal izi":        {"in_person", map[string]string{"cancel_reason": "'yağmur'"}, "events_cancellation"},
		"501 karakter iptal nedeni": {"in_person", map[string]string{"status": "'cancelled'", "cancelled_at": "now()", "cancel_reason": "repeat('a', 501)"}, "events_cancel_reason_len"},
		"serisiz seri istisnası":    {"in_person", map[string]string{"series_override": "true"}, "events_series_override"},
		"silinmeden silen":          {"in_person", map[string]string{"deleted_by": "gen_random_uuid()"}, "events_deleted_by"},
		"başka tayfanın serisi":     {"in_person", map[string]string{"series_id": "gen_random_uuid()"}, "events_series_fkey"},
	} {
		_, err := insertEvent(ctx, pool, tayfa, "general", tc.mode, tc.extra)
		if got := constraintOf(err); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu (%v)", name, got, tc.constraint, err)
		}
	}

	// The type is a catalog key at a version the catalog has (issue #237).
	if _, err := insertEvent(ctx, pool, tayfa, "cycling", "in_person", nil); constraintOf(err) != "events_type_version_fkey" {
		t.Errorf("bilinmeyen tür: %v", err)
	}
	if _, err := insertEvent(ctx, pool, tayfa, "general", "in_person", map[string]string{"type_version": "2"}); constraintOf(err) != "events_type_version_fkey" {
		t.Errorf("bilinmeyen tür sürümü: %v", err)
	}
	if _, err := insertEvent(ctx, pool, tayfa, "general", "hybrid", nil); constraintOf(err) != "events_mode" {
		t.Errorf("bilinmeyen biçim: %v", err)
	}

	// A cancelled event keeps its row and reason.
	if _, err := insertEvent(ctx, pool, tayfa, "general", "in_person", map[string]string{"status": "'cancelled'", "cancelled_at": "now()", "cancel_reason": "'yağmur'"}); err != nil {
		t.Errorf("iptal edilmiş etkinlik yazılamadı: %v", err)
	}
	if _, err := insertEvent(ctx, pool, tayfa, "general", "online", nil); err != nil {
		t.Errorf("online etkinlik yazılamadı: %v", err)
	}

	eid, err := insertEvent(ctx, pool, tayfa, "general", "in_person", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		sql, constraint string
	}{
		"bilinmeyen katılım":  {`INSERT INTO event_rsvps (event_id, user_id, status) VALUES ($1, gen_random_uuid(), 'interested')`, "event_rsvps_status"},
		"bilinmeyen yoklama":  {`INSERT INTO event_rsvps (event_id, user_id, status, attendance) VALUES ($1, gen_random_uuid(), 'going', 'late')`, "event_rsvps_attendance"},
		"bilinmeyen yöntem":   {`INSERT INTO event_rsvps (event_id, user_id, status, attendance_method) VALUES ($1, gen_random_uuid(), 'going', 'qr')`, "event_rsvps_attendance_method"},
		"zamansız işaretleme": {`INSERT INTO event_rsvps (event_id, user_id, status, attendance_marked_by) VALUES ($1, gen_random_uuid(), 'going', gen_random_uuid())`, "event_rsvps_attendance_marked"},
		"yanlış tür sütunu":   {`INSERT INTO event_fishing (event_id, type) VALUES ($1, 'general')`, "event_fishing_type"},
	} {
		_, err := pool.Exec(ctx, tc.sql, eid.PG())
		if got := constraintOf(err); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu (%v)", name, got, tc.constraint, err)
		}
	}
}

func TestEventDetailConstraints(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	event := func(typ string) id.ID {
		eid, err := insertEvent(ctx, pool, tayfa, typ, "in_person", nil)
		if err != nil {
			t.Fatal(err)
		}
		return eid
	}
	for name, tc := range map[string]struct {
		sql        string
		on         id.ID
		constraint string
	}{
		"bilinmeyen görünürlük": {`INSERT INTO event_fishing (event_id, spot_visibility) VALUES ($1, 'public')`, event("fishing"), "event_fishing_spot_visibility"},
		"türde olmayan alan":    {`INSERT INTO event_field_values (event_id, field_key, value) VALUES ($1, 'stake', '100')`, event("gaming"), "event_field_values_guard"},
		"alansız türde alan":    {`INSERT INTO event_field_values (event_id, field_key, value) VALUES ($1, 'game', '"CS2"')`, event("general"), "event_field_values_guard"},
		"nesne alan değeri":     {`INSERT INTO event_field_values (event_id, field_key, value) VALUES ($1, 'game', '{}')`, event("gaming"), "event_field_values_value"},
	} {
		_, err := pool.Exec(ctx, tc.sql, tc.on.PG())
		if got := constraintOf(err); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu (%v)", name, got, tc.constraint, err)
		}
	}
}

// The points of a route (event_routes, 00034; the Faz 1 convoy route moved
// there, issue #239) keep the rules of is_valid_route.
func TestRoutePoints(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})

	points := func(n int) string {
		var b strings.Builder
		b.WriteByte('[')
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"lat": %d.5, "lng": %d.25}`, i%90, i%180)
		}
		b.WriteByte(']')
		return b.String()
	}
	for name, tc := range map[string]struct {
		route string
		ok    bool
	}{
		"iki nokta":           {points(2), true},
		"500 nokta":           {points(500), true},
		"sınır değerler":      {`[{"lat": -90, "lng": -180}, {"lat": 90, "lng": 180}]`, true},
		"tek nokta":           {points(1), false},
		"501 nokta":           {points(501), false},
		"dizi değil":          {`{"lat": 1, "lng": 2}`, false},
		"boş dizi":            {`[]`, false},
		"nesne olmayan nokta": {`[[1, 2], [3, 4]]`, false},
		"metin enlem":         {`[{"lat": "1", "lng": 2}, {"lat": 1, "lng": 2}]`, false},
		"eksik boylam":        {`[{"lat": 1}, {"lat": 1, "lng": 2}]`, false},
		"fazla alan":          {`[{"lat": 1, "lng": 2, "name": "ev"}, {"lat": 1, "lng": 2}]`, false},
		"enlem aralık dışı":   {`[{"lat": 90.1, "lng": 2}, {"lat": 1, "lng": 2}]`, false},
		"boylam aralık dışı":  {`[{"lat": 1, "lng": -180.1}, {"lat": 1, "lng": 2}]`, false},
		"çok büyük sayı":      {`[{"lat": 1e400, "lng": 2}, {"lat": 1, "lng": 2}]`, false},
		"null nokta":          {`[null, {"lat": 1, "lng": 2}]`, false},
	} {
		eid, err := insertEvent(ctx, pool, tayfa, "convoy", "in_person", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO event_routes (event_id, component_key, points) VALUES ($1, 'route', $2::jsonb)`, eid.PG(), tc.route)
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: reddedildi: %v", name, err)
		case !tc.ok && constraintOf(err) != "event_routes_points":
			t.Errorf("%s: kısıt event_routes_points bekleniyordu: %v", name, err)
		}
	}
	// No route is no row: the points cannot be NULL.
	eid, err := insertEvent(ctx, pool, tayfa, "convoy", "in_person", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO event_routes (event_id, component_key, points) VALUES ($1, 'route', NULL)`, eid.PG()); err == nil {
		t.Error("NULL rota yazıldı")
	}
	// One route per component; the row cannot move to another component
	// or event (core_app has no such privilege).
	ins := `INSERT INTO event_routes (event_id, component_key, points) VALUES ($1, 'route', '[{"lat": 1, "lng": 2}, {"lat": 3, "lng": 4}]')`
	if _, err := pool.Exec(ctx, ins, eid.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, ins, eid.PG()); constraintOf(err) != "event_routes_pkey" {
		t.Errorf("ikinci rota: %v", err)
	}
	dbtest.WantDenied(t, pool, `UPDATE event_routes SET component_key = 'track' WHERE event_id = $1`, eid.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_routes SET event_id = event_id WHERE event_id = $1`, eid.PG())
}

// 00034 moves real rows (ADR-0041 item 3): the Faz 1 convoy routes become
// rows of the route component and the old column goes; Down brings both
// back. The test database is migrated while empty, so the move is replayed
// here on rows: back to the old shape, old rows in, forward.
func TestRouteMigrationMovesRows(t *testing.T) {
	pool, _ := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	up, down := migrationParts(t, coredb.Migrations(), "00034_component_route.sql")
	// 00035 removed event_convoy (issue #240): its Down comes first, as it
	// would on the real way back.
	_, laterDown := migrationParts(t, coredb.Migrations(), "00035_component_vehicles.sql")
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(laterDown)
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
	convoy := func() id.ID {
		t.Helper()
		eid, err := insertEvent(ctx, pool, tayfa, "convoy", "in_person", nil)
		if err != nil {
			t.Fatal(err)
		}
		return eid
	}
	with, without, later := convoy(), convoy(), convoy()
	all := []pgtype.UUID{with.PG(), without.PG(), later.PG()}
	const route = `[{"lat": 38.5, "lng": 27.25}, {"lat": 38.75, "lng": 27.5}, {"lat": 39, "lng": 28}]`
	exec(`INSERT INTO event_convoy (event_id) SELECT unnest($1::uuid[])`, all)
	// A route written with the new shape survives the way back.
	exec(`INSERT INTO event_routes (event_id, component_key, points) VALUES ($1, 'route', '[{"lat": 1, "lng": 2}, {"lat": 3, "lng": 4}]')`, later.PG())

	exec(down)
	if got := scalar(`SELECT count(*)::text FROM event_convoy WHERE route IS NOT NULL AND event_id = ANY($1)`, all); got != "1" {
		t.Fatalf("geri alınan rota sayısı = %s", got)
	}
	// A Faz 1 row, as the old code wrote it.
	exec(`UPDATE event_convoy SET route = $2::jsonb WHERE event_id = $1`, with.PG(), route)

	exec(up)
	if got := scalar(`SELECT (points = $2::jsonb)::text FROM event_routes WHERE event_id = $1 AND component_key = 'route'`, with.PG(), route); got != "true" {
		t.Fatalf("taşınan rota aynı değil: %q", got)
	}
	if got := scalar(`SELECT (r.created_at = e.created_at AND r.updated_at = e.updated_at)::text FROM event_routes r JOIN events e ON e.id = r.event_id WHERE r.event_id = $1`, with.PG()); got != "true" {
		t.Fatalf("taşınan rotanın zamanları etkinliğinkiler değil: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_routes WHERE event_id = ANY($1)`, all); got != "2" {
		t.Fatalf("taşınan rota sayısı = %s (rotasız konvoy satır almaz)", got)
	}
	// The old column is gone; the detail rows the vehicles hang on stay.
	if got := scalar(`SELECT count(*)::text FROM information_schema.columns WHERE table_name = 'event_convoy' AND column_name = 'route'`); got != "0" {
		t.Fatal("event_convoy.route kaldırılmadı")
	}
	if got := scalar(`SELECT count(*)::text FROM event_convoy WHERE event_id = ANY($1)`, all); got != "3" {
		t.Fatalf("konvoy detay satırları = %s", got)
	}
	// The way back: the routes return to the column.
	exec(down)
	if got := scalar(`SELECT (route = $2::jsonb)::text FROM event_convoy WHERE event_id = $1`, with.PG(), route); got != "true" {
		t.Fatalf("geri alınan rota aynı değil: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_convoy WHERE route IS NOT NULL AND event_id = ANY($1)`, all); got != "2" {
		t.Fatalf("ikinci kez geri alınan rota sayısı = %s", got)
	}
}

// The vehicles component's tables (00035, issue #240): the Faz 1 checks on
// a vehicle, seats 0–8, one vehicle and one seat request per person in a
// component, a request only to a vehicle of the same component, and
// core_app's column privileges. The component guard runs before the CHECKs.
func TestVehicleConstraints(t *testing.T) {
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
	convoy, other := event("convoy"), event("convoy")
	const vehicle = `INSERT INTO event_vehicles (id, event_id, component_key, user_id, make`
	for name, tc := range map[string]struct {
		sql        string
		constraint string
	}{
		"1899 model":            {vehicle + `, model_year) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), 'Ford', 1899)`, "event_vehicles_model_year"},
		"boş marka":             {vehicle + `) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), '')`, "event_vehicles_make_len"},
		"41 karakter model":     {vehicle + `, model) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), 'Ford', repeat('m', 41))`, "event_vehicles_model_len"},
		"31 karakter renk":      {vehicle + `, color) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), 'Ford', repeat('r', 31))`, "event_vehicles_color_len"},
		"küçük harf plaka":      {vehicle + `, plate) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), 'Ford', '35 abc 123')`, "event_vehicles_plate"},
		"boşlukla biten plaka":  {vehicle + `, plate) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), 'Ford', '35ABC ')`, "event_vehicles_plate"},
		"17 karakter plaka":     {vehicle + `, plate) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), 'Ford', '12345678901234567')`, "event_vehicles_plate"},
		"eksi koltuk":           {vehicle + `, seats) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), 'Ford', -1)`, "event_vehicles_seats"},
		"dokuz koltuk":          {vehicle + `, seats) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), 'Ford', 9)`, "event_vehicles_seats"},
		"türde olmayan anahtar": {vehicle + `) VALUES (gen_random_uuid(), $1, 'cars', gen_random_uuid(), 'Ford')`, "event_component_guard"},
	} {
		_, err := pool.Exec(ctx, tc.sql, convoy.PG())
		if got := constraintOf(err); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu (%v)", name, got, tc.constraint, err)
		}
	}

	// One vehicle per person in a component; eight seats are fine.
	driver, rider := id.New(), id.New()
	mine, theirs, elsewhere := id.New(), id.New(), id.New()
	ins := vehicle + `, seats) VALUES ($1, $2, 'vehicles', $3, 'Ford', 8)`
	if _, err := pool.Exec(ctx, ins, mine.PG(), convoy.PG(), driver.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, ins, id.New().PG(), convoy.PG(), driver.PG()); constraintOf(err) != "event_vehicles_event_component_user_key" {
		t.Errorf("ikinci araç eklendi: %v", err)
	}
	if _, err := pool.Exec(ctx, ins, theirs.PG(), convoy.PG(), id.New().PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, ins, elsewhere.PG(), other.PG(), driver.PG()); err != nil {
		t.Fatalf("aynı kişinin başka etkinlikteki aracı: %v", err)
	}

	// One seat request per person in a component, to a vehicle of that
	// component; approved exactly when it has a decision time.
	const request = `INSERT INTO event_vehicle_seat_requests (id, event_id, component_key, user_id, event_vehicle_id`
	if _, err := pool.Exec(ctx, request+`) VALUES (gen_random_uuid(), $1, 'vehicles', $2, $3)`, convoy.PG(), rider.PG(), mine.PG()); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		sql        string
		args       []any
		constraint string
	}{
		"ikinci istek":           {request + `) VALUES (gen_random_uuid(), $1, 'vehicles', $2, $3)`, []any{convoy.PG(), rider.PG(), theirs.PG()}, "event_vehicle_seat_requests_event_component_user_key"},
		"başka etkinliğin aracı": {request + `) VALUES (gen_random_uuid(), $1, 'vehicles', $2, $3)`, []any{convoy.PG(), id.New().PG(), elsewhere.PG()}, "event_vehicle_seat_requests_vehicle_fkey"},
		"olmayan araç":           {request + `) VALUES (gen_random_uuid(), $1, 'vehicles', $2, $3)`, []any{convoy.PG(), id.New().PG(), id.New().PG()}, "event_vehicle_seat_requests_vehicle_fkey"},
		"bilinmeyen durum":       {request + `, status) VALUES (gen_random_uuid(), $1, 'vehicles', $2, $3, 'declined')`, []any{convoy.PG(), id.New().PG(), mine.PG()}, "event_vehicle_seat_requests_status"},
		"zamansız onay":          {request + `, status) VALUES (gen_random_uuid(), $1, 'vehicles', $2, $3, 'approved')`, []any{convoy.PG(), id.New().PG(), mine.PG()}, "event_vehicle_seat_requests_decided"},
		"onaysız zaman":          {request + `, decided_at) VALUES (gen_random_uuid(), $1, 'vehicles', $2, $3, now())`, []any{convoy.PG(), id.New().PG(), mine.PG()}, "event_vehicle_seat_requests_decided"},
	} {
		_, err := pool.Exec(ctx, tc.sql, tc.args...)
		if got := constraintOf(err); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu (%v)", name, got, tc.constraint, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE event_vehicle_seat_requests SET status = 'approved', decided_at = now() WHERE user_id = $1`, rider.PG()); err != nil {
		t.Fatalf("onay: %v", err)
	}

	// A vehicle cannot move to another event, component or person, and a
	// request cannot move to another vehicle or person (core_app has no
	// such privilege).
	dbtest.WantDenied(t, pool, `UPDATE event_vehicles SET event_id = event_id WHERE id = $1`, mine.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_vehicles SET component_key = 'cars' WHERE id = $1`, mine.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_vehicles SET user_id = user_id WHERE id = $1`, mine.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_vehicle_seat_requests SET event_vehicle_id = event_vehicle_id WHERE user_id = $1`, rider.PG())
	dbtest.WantDenied(t, pool, `UPDATE event_vehicle_seat_requests SET user_id = user_id WHERE user_id = $1`, rider.PG())

	// Removing a vehicle removes the requests made to it.
	if _, err := pool.Exec(ctx, `DELETE FROM event_vehicles WHERE id = $1`, mine.PG()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_vehicle_seat_requests WHERE event_id = $1`, convoy.PG()).Scan(&n); err != nil || n != 0 {
		t.Fatalf("silinen aracın istekleri kaldı: %d %v", n, err)
	}
}

// 00035 moves real rows (ADR-0041 item 3): the Faz 1 convoy vehicles become
// rows of the vehicles component and the two convoy tables go; Down brings
// both back. The test database is migrated while empty, so the move is
// replayed here on rows: back to the old shape, old rows in, forward.
func TestVehiclesMigrationMovesRows(t *testing.T) {
	pool, _ := setup(t)
	admin := adminOf(t, pool)
	ctx := context.Background()
	up, down := migrationParts(t, coredb.Migrations(), "00035_component_vehicles.sql")
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
	with, empty, later := event("convoy"), event("convoy"), event("convoy")
	all := []pgtype.UUID{with.PG(), empty.PG(), later.PG()}
	// Rows written with the new shape: a vehicle with seats and a seat
	// request. The vehicle survives the way back; seats and requests have
	// no place in the Faz 1 schema.
	newVehicle, newDriver := id.New(), id.New()
	exec(`INSERT INTO event_vehicles (id, event_id, component_key, user_id, make, seats) VALUES ($1, $2, 'vehicles', $3, 'Fiat', 3)`, newVehicle.PG(), later.PG(), newDriver.PG())
	exec(`INSERT INTO event_vehicle_seat_requests (id, event_id, component_key, user_id, event_vehicle_id) VALUES (gen_random_uuid(), $1, 'vehicles', gen_random_uuid(), $2)`, later.PG(), newVehicle.PG())

	exec(down)
	// Every convoy has its detail row again and the new vehicle is back in
	// the old table.
	if got := scalar(`SELECT count(*)::text FROM event_convoy WHERE event_id = ANY($1)`, all); got != "3" {
		t.Fatalf("geri kurulan konvoy detay satırları = %s", got)
	}
	if got := scalar(`SELECT make FROM event_convoy_vehicles WHERE id = $1 AND event_id = $2 AND user_id = $3`, newVehicle.PG(), later.PG(), newDriver.PG()); got != "Fiat" {
		t.Fatalf("geri alınan araç: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM information_schema.tables WHERE table_name IN ('event_vehicles', 'event_vehicle_seat_requests')`); got != "0" {
		t.Fatalf("bileşen tabloları kaldırılmadı: %s", got)
	}
	// Faz 1 rows, as the old code wrote them: one with every field, one
	// with the make only.
	full, bare, driver := id.New(), id.New(), id.New()
	exec(`INSERT INTO event_convoy_vehicles (id, event_id, user_id, make, model, model_year, color, plate, created_at)
		VALUES ($1, $2, $3, 'Ford', 'Mustang', 1967, 'Kırmızı', '35 ABC 123', '2026-10-01T08:00:00Z')`, full.PG(), with.PG(), driver.PG())
	exec(`INSERT INTO event_convoy_vehicles (id, event_id, user_id, make) VALUES ($1, $2, gen_random_uuid(), 'Murat')`, bare.PG(), with.PG())

	exec(up)
	if got := scalar(`SELECT concat_ws('|', component_key, user_id::text, make, model, model_year::text, color, plate, seats::text,
			(created_at = '2026-10-01T08:00:00Z')::text, (updated_at = created_at)::text)
		FROM event_vehicles WHERE id = $1 AND event_id = $2`, full.PG(), with.PG()); got != "vehicles|"+driver.String()+"|Ford|Mustang|1967|Kırmızı|35 ABC 123|0|true|true" {
		t.Fatalf("taşınan araç: %q", got)
	}
	if got := scalar(`SELECT concat_ws('|', make, coalesce(model, '-'), coalesce(model_year::text, '-'), coalesce(color, '-'), coalesce(plate, '-'), seats::text)
		FROM event_vehicles WHERE id = $1`, bare.PG()); got != "Murat|-|-|-|-|0" {
		t.Fatalf("taşınan yalın araç: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_vehicles WHERE event_id = ANY($1)`, all); got != "3" {
		t.Fatalf("taşınan araç sayısı = %s", got)
	}
	// The old tables are gone.
	if got := scalar(`SELECT count(*)::text FROM information_schema.tables WHERE table_name IN ('event_convoy', 'event_convoy_vehicles')`); got != "0" {
		t.Fatalf("eski tablolar kaldırılmadı: %s", got)
	}
	// core_app keeps its privileges on the tables the migration made.
	if _, err := pool.Exec(ctx, `UPDATE event_vehicles SET seats = 2 WHERE id = $1`, full.PG()); err != nil {
		t.Fatalf("taşınan aracı güncelleme: %v", err)
	}
	// The way back: the vehicles return to the old table, with the detail
	// rows they hang on.
	exec(down)
	if got := scalar(`SELECT concat_ws('|', make, model, model_year::text, color, plate) FROM event_convoy_vehicles WHERE id = $1 AND user_id = $2`, full.PG(), driver.PG()); got != "Ford|Mustang|1967|Kırmızı|35 ABC 123" {
		t.Fatalf("ikinci kez geri alınan araç: %q", got)
	}
	if got := scalar(`SELECT count(*)::text FROM event_convoy_vehicles WHERE event_id = ANY($1)`, all); got != "3" {
		t.Fatalf("ikinci kez geri alınan araç sayısı = %s", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE event_convoy_vehicles SET plate = NULL WHERE id = $1`, full.PG()); err != nil {
		t.Fatalf("geri kurulan tabloda core_app yetkisi: %v", err)
	}
}

func TestMembersCanCreateEventsDefault(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	row, err := q.GetTayfa(ctx, tayfa.PG())
	if err != nil {
		t.Fatal(err)
	}
	if row.MembersCanCreateEvents {
		t.Error("yeni tayfada üyeler etkinlik açabiliyor; varsayılan kapalı olmalı")
	}
}
