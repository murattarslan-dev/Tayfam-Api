package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

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
		"convoy":  `INSERT INTO event_convoy (event_id) VALUES ($1)`,
		"camp":    `INSERT INTO event_camp (event_id) VALUES ($1)`,
		"hike":    `INSERT INTO event_hike (event_id) VALUES ($1)`,
		"fishing": `INSERT INTO event_fishing (event_id) VALUES ($1)`,
		"gaming":  `INSERT INTO event_gaming (event_id, game, platform) VALUES ($1, 'CS2', 'pc')`,
		"sports":  `INSERT INTO event_sports (event_id, sport) VALUES ($1, 'football')`,
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
	_, err := pool.Exec(ctx, `UPDATE event_camp SET type = 'hike' WHERE event_id = $1`, ids["camp"].PG())
	if c := constraintOf(err); c != "event_camp_type" {
		t.Errorf("event_camp.type değişti: %v", err)
	}
	// The event's type cannot change while a detail row points at it.
	_, err = pool.Exec(ctx, `UPDATE events SET type = 'hike' WHERE id = $1`, ids["camp"].PG())
	if c := constraintOf(err); c != "event_camp_event_fkey" {
		t.Errorf("detaylı etkinliğin türü değişti: %v", err)
	}

	// Vehicles and camp items hang off the type tables, so they too need an
	// event of the right type.
	_, err = pool.Exec(ctx, `INSERT INTO event_convoy_vehicles (id, event_id, user_id, make) VALUES (gen_random_uuid(), $1, gen_random_uuid(), 'Murat')`, ids["camp"].PG())
	if c := constraintOf(err); c != "event_convoy_vehicles_event_id_fkey" {
		t.Errorf("kamp etkinliğine araç eklendi: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO event_camp_items (id, event_id, name) VALUES (gen_random_uuid(), $1, 'Çadır')`, ids["convoy"].PG())
	if c := constraintOf(err); c != "event_camp_items_event_id_fkey" {
		t.Errorf("konvoy etkinliğine kamp malzemesi eklendi: %v", err)
	}

	// Deleting the event removes its details, vehicles, items and RSVPs.
	if _, err := pool.Exec(ctx, `INSERT INTO event_convoy_vehicles (id, event_id, user_id, make, plate) VALUES (gen_random_uuid(), $1, gen_random_uuid(), 'Murat', '35 ABC 123')`, ids["convoy"].PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO event_rsvps (event_id, user_id, status) VALUES ($1, gen_random_uuid(), 'going')`, ids["convoy"].PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM events WHERE id = $1`, ids["convoy"].PG()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"event_convoy", "event_convoy_vehicles", "event_rsvps"} {
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

	if _, err := insertEvent(ctx, pool, tayfa, "cycling", "in_person", nil); constraintOf(err) != "events_type" {
		t.Errorf("bilinmeyen tür: %v", err)
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
		"yanlış tür sütunu":   {`INSERT INTO event_hike (event_id, type) VALUES ($1, 'general')`, "event_hike_type"},
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
	convoy, camp := event("convoy"), event("camp")
	for _, sql := range []string{`INSERT INTO event_convoy (event_id) VALUES ($1)`} {
		if _, err := pool.Exec(ctx, sql, convoy.PG()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO event_camp (event_id) VALUES ($1)`, camp.PG()); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		sql        string
		on         id.ID
		constraint string
	}{
		"zorluk 0":              {`INSERT INTO event_hike (event_id, difficulty) VALUES ($1, 0)`, event("hike"), "event_hike_difficulty"},
		"zorluk 6":              {`INSERT INTO event_hike (event_id, difficulty) VALUES ($1, 6)`, event("hike"), "event_hike_difficulty"},
		"negatif mesafe":        {`INSERT INTO event_hike (event_id, distance_km) VALUES ($1, -1)`, event("hike"), "event_hike_distance_km"},
		"negatif yükseklik":     {`INSERT INTO event_hike (event_id, elevation_m) VALUES ($1, -5)`, event("hike"), "event_hike_elevation_m"},
		"uzun bekleme payı":     {`INSERT INTO event_hike (event_id, return_grace_minutes) VALUES ($1, 2000)`, event("hike"), "event_hike_return_grace_minutes"},
		"bilinmeyen görünürlük": {`INSERT INTO event_fishing (event_id, spot_visibility) VALUES ($1, 'public')`, event("fishing"), "event_fishing_spot_visibility"},
		"bilinmeyen platform":   {`INSERT INTO event_gaming (event_id, game, platform) VALUES ($1, 'CS2', 'amiga')`, event("gaming"), "event_gaming_platform"},
		"boş oyun":              {`INSERT INTO event_gaming (event_id, game, platform) VALUES ($1, '', 'pc')`, event("gaming"), "event_gaming_game_len"},
		"bilinmeyen spor":       {`INSERT INTO event_sports (event_id, sport) VALUES ($1, 'golf')`, event("sports"), "event_sports_sport"},
		"1001 karakter not":     {`INSERT INTO event_camp (event_id, notes) VALUES ($1, repeat('n', 1001))`, event("camp"), "event_camp_notes_len"},
		"sıfır adet":            {`INSERT INTO event_camp_items (id, event_id, name, quantity) VALUES (gen_random_uuid(), $1, 'Çadır', 0)`, camp, "event_camp_items_quantity"},
		"boş malzeme":           {`INSERT INTO event_camp_items (id, event_id, name) VALUES (gen_random_uuid(), $1, '')`, camp, "event_camp_items_name_len"},
		"1899 model":            {`INSERT INTO event_convoy_vehicles (id, event_id, user_id, make, model_year) VALUES (gen_random_uuid(), $1, gen_random_uuid(), 'Ford', 1899)`, convoy, "event_convoy_vehicles_model_year"},
		"boş marka":             {`INSERT INTO event_convoy_vehicles (id, event_id, user_id, make) VALUES (gen_random_uuid(), $1, gen_random_uuid(), '')`, convoy, "event_convoy_vehicles_make_len"},
		"küçük harf plaka":      {`INSERT INTO event_convoy_vehicles (id, event_id, user_id, make, plate) VALUES (gen_random_uuid(), $1, gen_random_uuid(), 'Ford', '35 abc 123')`, convoy, "event_convoy_vehicles_plate"},
		"boşlukla biten plaka":  {`INSERT INTO event_convoy_vehicles (id, event_id, user_id, make, plate) VALUES (gen_random_uuid(), $1, gen_random_uuid(), 'Ford', '35ABC ')`, convoy, "event_convoy_vehicles_plate"},
		"17 karakter plaka":     {`INSERT INTO event_convoy_vehicles (id, event_id, user_id, make, plate) VALUES (gen_random_uuid(), $1, gen_random_uuid(), 'Ford', '12345678901234567')`, convoy, "event_convoy_vehicles_plate"},
	} {
		_, err := pool.Exec(ctx, tc.sql, tc.on.PG())
		if got := constraintOf(err); got != tc.constraint {
			t.Errorf("%s: kısıt %q, %q bekleniyordu (%v)", name, got, tc.constraint, err)
		}
	}

	// One vehicle per person per event.
	user := id.New()
	ins := `INSERT INTO event_convoy_vehicles (id, event_id, user_id, make) VALUES (gen_random_uuid(), $1, $2, 'Ford')`
	if _, err := pool.Exec(ctx, ins, convoy.PG(), user.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, ins, convoy.PG(), user.PG()); constraintOf(err) != "event_convoy_vehicles_event_user_key" {
		t.Errorf("ikinci araç eklendi: %v", err)
	}
}

func TestConvoyRoute(t *testing.T) {
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
		"rota yok":            {"", true},
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
		var route any
		if tc.route != "" {
			route = tc.route
		}
		_, err = pool.Exec(ctx, `INSERT INTO event_convoy (event_id, route) VALUES ($1, $2::jsonb)`, eid.PG(), route)
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: reddedildi: %v", name, err)
		case !tc.ok && constraintOf(err) != "event_convoy_route":
			t.Errorf("%s: kısıt event_convoy_route bekleniyordu: %v", name, err)
		}
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
