package vehicles_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/vehicles"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. The catalog's convoy has the component under its kind with
// plates and without seats. poolType has it under a key of its own
// ("cars") with plates and seats, next to another component; its second
// version, written by a test, has no vehicles. tripType has an optional
// vehicles component that is off by default, with seats and without
// plates.
const (
	poolType = "test_pool"
	poolV1   = `{"components":[{"kind":"vehicles","key":"cars","labels":{"tr":"Arabalar"},"params":{"plate":true,"seats":true}},{"kind":"draw"}]}`
	poolV2   = `{"components":[{"kind":"draw"}]}`
	tripType = "test_trip"
	tripV1   = `{"components":[{"kind":"vehicles","optional":{"default":false},"params":{"seats":true}}]}`
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type env struct {
	fw     *component.Service
	cars   *vehicles.Service
	events *event.Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	admin  *pgxpool.Pool
	clock  *clock
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: %v, %v bekleniyordu", what, err, want)
	}
}

func wantField(t *testing.T, what string, err error, field string) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != field {
		t.Errorf("%s: %v, %q alanında doğrulama hatası bekleniyordu", what, err, field)
	}
}

func constraintOf(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.ConstraintName
	}
	return ""
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := &clock{t: time.Now().UTC().Truncate(time.Microsecond)}
	fw, err := component.New(component.Config{Pool: d.App, Log: log, Now: c.now})
	must(t, err)
	events, err := event.New(event.Config{Pool: d.App, Log: log, Now: c.now})
	must(t, err)
	tayfas, err := tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
	e := &env{fw: fw, cars: vehicles.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	e.addType(t, poolType, poolV1)
	e.addType(t, tripType, tripV1)
	return e
}

// exec runs a fixture statement as a superuser with triggers off.
func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, sql, args...)
	must(t, err)
	must(t, tx.Commit(ctx))
}

// addType writes a type as the platform would (the catalog is read-only
// for the service).
func (e *env) addType(t *testing.T, key, def string) {
	t.Helper()
	e.exec(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
		VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, key, def)
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, key)
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

// newTayfa creates a tayfa (every module on) and returns it with its
// admin.
func (e *env) newTayfa(t *testing.T) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Araç Tayfası", ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID, admin
}

// member writes an active membership directly.
func (e *env) member(t *testing.T, tf id.ID, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	must(t, store.New(tx).CreateMembership(ctx, store.CreateMembershipParams{
		TayfaID: tf.PG(), UserID: u.PG(), Role: role.String(), Status: "active", JoinedVia: "code",
		JoinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}))
	must(t, tx.Commit(ctx))
	return u
}

func (e *env) mute(t *testing.T, tf, u id.ID) {
	t.Helper()
	e.exec(t, `UPDATE memberships SET muted_until = now() + interval '30 days', muted_by = $3 WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), u.PG(), id.New().PG())
}

// newEvent creates an event of typ that starts in a day and lasts three
// hours.
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string, sels ...component.Selection) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Pazar turu", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(), Components: sels,
	})
	must(t, err)
	return v
}

func ref(user, tf, ev id.ID, key string) component.Ref {
	return component.Ref{User: user, Tayfa: tf, Event: ev, Key: key}
}

func on(b bool) *bool { return &b }

// changes counts the event's component_changed events for key.
func (e *env) changes(t *testing.T, ev id.ID, key string) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND subject_id = $2 AND payload ->> 'component_key' = $3`,
		component.EventChanged, ev.PG(), key)
}

// going answers "going" for every user.
func (e *env) going(t *testing.T, tf, ev id.ID, users ...id.ID) {
	t.Helper()
	for _, u := range users {
		_, err := e.events.SetRSVP(ctx, u, tf, ev, event.RSVPGoing)
		must(t, err)
	}
}

// letMembersCreate lets plain members create events, so that an organizer
// who is not a moderator exists.
func (e *env) letMembersCreate(t *testing.T, tf, admin id.ID) {
	t.Helper()
	_, err := e.tayfas.Update(ctx, admin, tf, tayfa.UpdateInput{Fields: []string{tayfa.FieldMembersCanCreateEvents}, MembersCanCreateEvents: true})
	must(t, err)
}

// of returns user's vehicle in vs.
func of(t *testing.T, vs vehicles.Vehicles, user id.ID) vehicles.Vehicle {
	t.Helper()
	for _, v := range vs.Items {
		if v.UserID == user {
			return v
		}
	}
	t.Fatalf("%s kullanıcısının aracı yok: %+v", user, vs.Items)
	return vehicles.Vehicle{}
}

func sameUsers(got []id.ID, want ...id.ID) bool {
	if len(got) != len(want) {
		return false
	}
	for _, u := range want {
		if !slices.Contains(got, u) {
			return false
		}
	}
	return true
}

// Vehicles end to end on the catalog's convoy (plates on, seats off): set,
// replace, list, remove; who sees a plate; nothing about the vehicle in
// the audit record or the outbox.
func TestVehiclesSetListRemove(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	e.letMembersCreate(t, tf, admin)
	org, driver := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	rider, outsider := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, org, event.TypeConvoy)
	const key = "vehicles"

	// Nothing yet: an empty list, not an error.
	got, err := e.cars.List(ctx, ref(outsider, tf, ev.ID, key))
	must(t, err)
	if got.Items == nil || len(got.Items) != 0 || !got.PlateEnabled || got.SeatsEnabled || got.CallerCanParticipate || got.CallerCanManage {
		t.Fatalf("araçsız etkinlik: %+v", got)
	}

	// Only someone who is going has a vehicle.
	_, err = e.cars.Set(ctx, ref(driver, tf, ev.ID, key), vehicles.Input{Make: "Ford", Plate: "35 ABC 123"})
	wantErr(t, "katılmadan araç", err, apperr.ErrEventNotGoing)
	e.going(t, tf, ev.ID, driver, rider)
	_, err = e.events.SetRSVP(ctx, outsider, tf, ev.ID, event.RSVPMaybe)
	must(t, err)
	_, err = e.cars.Set(ctx, ref(outsider, tf, ev.ID, key), vehicles.Input{Make: "Ford"})
	wantErr(t, "belki diyenin aracı", err, apperr.ErrEventNotGoing)

	for _, tc := range []struct {
		name  string
		in    vehicles.Input
		field string
	}{
		{"marka yok", vehicles.Input{}, "make"},
		{"boşluk marka", vehicles.Input{Make: "   "}, "make"},
		{"uzun marka", vehicles.Input{Make: strings.Repeat("m", vehicles.MakeMax+1)}, "make"},
		{"denetim karakterli marka", vehicles.Input{Make: "Fo\x00rd"}, "make"},
		{"uzun model", vehicles.Input{Make: "Ford", Model: strings.Repeat("m", vehicles.ModelMax+1)}, "model"},
		{"uzun renk", vehicles.Input{Make: "Ford", Color: strings.Repeat("r", vehicles.ColorMax+1)}, "color"},
		{"1899 model", vehicles.Input{Make: "Ford", ModelYear: 1899}, "model_year"},
		{"iki yıl sonrası", vehicles.Input{Make: "Ford", ModelYear: int32(e.clock.now().Year() + 2)}, "model_year"},
		{"tireli plaka", vehicles.Input{Make: "Ford", Plate: "35-ABC-123"}, "plate"},
		{"Türkçe harfli plaka", vehicles.Input{Make: "Ford", Plate: "35 ŞĞÜ 12"}, "plate"},
		{"17 karakter plaka", vehicles.Input{Make: "Ford", Plate: "1234567890ABCDEFG"}, "plate"},
		{"eksi koltuk", vehicles.Input{Make: "Ford", Seats: -1}, "seats"},
		{"dokuz koltuk", vehicles.Input{Make: "Ford", Seats: vehicles.MaxSeats + 1}, "seats"},
		// The convoy's component has no seats parameter.
		{"koltuk kapalıyken koltuk", vehicles.Input{Make: "Ford", Seats: 1}, "seats"},
	} {
		_, err := e.cars.Set(ctx, ref(driver, tf, ev.ID, key), tc.in)
		wantField(t, tc.name, err, tc.field)
	}
	if n := e.count(t, `SELECT count(*) FROM event_vehicles`); n != 0 {
		t.Fatalf("reddedilen araç yazıldı: %d", n)
	}

	set, err := e.cars.Set(ctx, ref(driver, tf, ev.ID, key), vehicles.Input{
		Make: "  Ford ", Model: "Mustang", ModelYear: int32(e.clock.now().Year() + 1), Color: "Kırmızı", Plate: " 35  abc 123 ",
	})
	must(t, err)
	first := of(t, set, driver)
	if len(set.Items) != 1 || first.Make != "Ford" || first.Model != "Mustang" || first.Color != "Kırmızı" || first.Plate != "35 ABC 123" ||
		first.Seats != 0 || first.Riders == nil || first.Pending == nil || !set.CallerCanParticipate || set.CallerCanManage {
		t.Fatalf("yazılan araç: %+v", set)
	}
	// Setting again replaces the caller's vehicle and keeps its ID.
	set, err = e.cars.Set(ctx, ref(driver, tf, ev.ID, key), vehicles.Input{Make: "Ford", Model: "Mustang", ModelYear: 1967, Plate: "35 ABC 123"})
	must(t, err)
	if v := of(t, set, driver); v.ID != first.ID || v.ModelYear != 1967 || v.Color != "" || e.count(t, `SELECT count(*) FROM event_vehicles`) != 1 {
		t.Fatalf("değişen araç: %+v", v)
	}
	if n := e.changes(t, ev.ID, key); n != 2 {
		t.Fatalf("canlı güncelleme olayı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND actor_id = $3 AND metadata ->> 'component_key' = $4 AND metadata ->> 'vehicle_id' = $5`,
		vehicles.AuditSet, ev.ID.PG(), driver.PG(), key, first.ID.String()); n != 2 {
		t.Fatalf("denetim kaydı = %d", n)
	}

	// Plates are personal data: the participants, the organizer and the
	// owner see them; an admin who does not take part does not.
	for name, tc := range map[string]struct {
		u    id.ID
		want string
	}{
		"sürücü":           {driver, "35 ABC 123"},
		"katılımcı":        {rider, "35 ABC 123"},
		"organizatör":      {org, "35 ABC 123"},
		"belki diyen":      {outsider, ""},
		"katılmayan admin": {admin, ""},
	} {
		got, err := e.cars.List(ctx, ref(tc.u, tf, ev.ID, key))
		must(t, err)
		if v := of(t, got, driver); v.Plate != tc.want || v.Make != "Ford" {
			t.Errorf("%s: plaka %q, %q bekleniyordu", name, v.Plate, tc.want)
		}
	}
	// The vehicle never reaches audit records or events.
	var audit, events string
	must(t, e.pool.QueryRow(ctx, `SELECT coalesce(string_agg(metadata::text, ' '), '') FROM audit_logs`).Scan(&audit))
	must(t, e.pool.QueryRow(ctx, `SELECT coalesce(string_agg(payload::text, ' '), '') FROM domain_events`).Scan(&events))
	if strings.Contains(audit+events, "ABC") || strings.Contains(audit+events, "Mustang") || strings.Contains(audit+events, "Ford") {
		t.Fatal("araç bilgisi kayda girdi")
	}

	// Seats are off on this component: nobody can ask for one.
	_, err = e.cars.RequestSeat(ctx, ref(rider, tf, ev.ID, key), first.ID)
	wantErr(t, "koltuk kapalıyken istek", err, apperr.ErrVehicleFull)

	// Removing: not someone else's (unless managing), always one's own.
	_, err = e.cars.Remove(ctx, ref(rider, tf, ev.ID, key), first.ID)
	wantErr(t, "başkasının aracı", err, apperr.ErrPermissionDenied)
	_, err = e.cars.Remove(ctx, ref(rider, tf, ev.ID, key), id.New())
	wantErr(t, "olmayan araç", err, apperr.ErrEventItemNotFound)
	left, err := e.cars.Remove(ctx, ref(org, tf, ev.ID, key), first.ID)
	must(t, err)
	if len(left.Items) != 0 || !left.CallerCanManage {
		t.Fatalf("organizatörün çıkardığı araç: %+v", left)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND actor_id = $2`, vehicles.AuditRemoved, org.PG()); n != 1 {
		t.Fatalf("çıkarma denetim kaydı = %d", n)
	}
	set, err = e.cars.Set(ctx, ref(driver, tf, ev.ID, key), vehicles.Input{Make: "Ford", Plate: "35 ABC 123"})
	must(t, err)
	if of(t, set, driver).ID == first.ID {
		t.Fatal("silinen aracın kimliği yeniden kullanıldı")
	}
	left, err = e.cars.Remove(ctx, ref(driver, tf, ev.ID, key), of(t, set, driver).ID)
	must(t, err)
	if len(left.Items) != 0 {
		t.Fatalf("kendi aracını çıkarma: %+v", left)
	}

	// Leaving the event takes the vehicle (and plate) with it.
	_, err = e.cars.Set(ctx, ref(driver, tf, ev.ID, key), vehicles.Input{Make: "Ford", Plate: "35 ABC 123"})
	must(t, err)
	_, err = e.events.SetRSVP(ctx, driver, tf, ev.ID, event.RSVPNotGoing)
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM event_vehicles`); n != 0 {
		t.Fatalf("ayrılan sürücünün aracı kaldı: %d", n)
	}
}

// Seat sharing (ADR-0037 §3): the owner writes the free seats, a
// participant asks, the owner approves; one request per person; never
// more approved riders than seats.
func TestVehicleSeats(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	driver, other := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	a, b, c, bystander := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member), e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin, poolType)
	const key = "cars"
	e.going(t, tf, ev.ID, driver, other, a, b, c, bystander)
	r := func(u id.ID) component.Ref { return ref(u, tf, ev.ID, key) }

	set, err := e.cars.Set(ctx, r(driver), vehicles.Input{Make: "Ford", Plate: "35 ABC 123", Seats: 2})
	must(t, err)
	car := of(t, set, driver)
	if car.Seats != 2 || !set.SeatsEnabled || !set.PlateEnabled {
		t.Fatalf("koltuklu araç: %+v", set)
	}
	set, err = e.cars.Set(ctx, r(other), vehicles.Input{Make: "Fiat", Seats: 1})
	must(t, err)
	second := of(t, set, other)

	// Asking: the request waits; the owner and the requester see it,
	// nobody else does.
	_, err = e.cars.RequestSeat(ctx, r(a), id.New())
	wantErr(t, "olmayan araca istek", err, apperr.ErrEventItemNotFound)
	_, err = e.cars.RequestSeat(ctx, r(driver), second.ID)
	wantErr(t, "sürücünün koltuk isteği", err, apperr.ErrSeatRequestDriver)
	_, err = e.cars.RequestSeat(ctx, r(driver), car.ID)
	wantErr(t, "kendi aracına istek", err, apperr.ErrSeatRequestDriver)
	asked, err := e.cars.RequestSeat(ctx, r(a), car.ID)
	must(t, err)
	if v := of(t, asked, driver); !sameUsers(v.Pending, a) || len(v.Riders) != 0 {
		t.Fatalf("isteyenin gördüğü: %+v", v)
	}
	before := e.changes(t, ev.ID, key)
	if _, err := e.cars.RequestSeat(ctx, r(a), car.ID); err != nil || e.changes(t, ev.ID, key) != before {
		t.Fatalf("yinelenen istek: %v, %d olay", err, e.changes(t, ev.ID, key)-before)
	}
	_, err = e.cars.RequestSeat(ctx, r(b), car.ID)
	must(t, err)
	for name, tc := range map[string]struct {
		u    id.ID
		want []id.ID
	}{
		"araç sahibi":  {driver, []id.ID{a, b}},
		"isteyen":      {b, []id.ID{b}},
		"başka üye":    {bystander, nil},
		"yönetici":     {admin, nil},
		"başka sürücü": {other, nil},
	} {
		got, err := e.cars.List(ctx, r(tc.u))
		must(t, err)
		if v := of(t, got, driver); !sameUsers(v.Pending, tc.want...) || len(v.Riders) != 0 {
			t.Errorf("%s: bekleyenler %v", name, v.Pending)
		}
	}

	// Approving is the owner's alone.
	for name, u := range map[string]id.ID{"isteyen": a, "başka üye": bystander, "yönetici": admin, "başka sürücü": other} {
		_, err := e.cars.ApproveSeat(ctx, r(u), car.ID, a)
		wantErr(t, name+" / onay", err, apperr.ErrPermissionDenied)
	}
	_, err = e.cars.ApproveSeat(ctx, r(driver), car.ID, bystander)
	wantErr(t, "isteği olmayanı onaylama", err, apperr.ErrEventItemNotFound)
	_, err = e.cars.ApproveSeat(ctx, r(other), second.ID, a)
	wantErr(t, "başka araca yazılmış isteği onaylama", err, apperr.ErrEventItemNotFound)
	_, err = e.cars.ApproveSeat(ctx, r(driver), id.New(), a)
	wantErr(t, "olmayan araçta onay", err, apperr.ErrEventItemNotFound)
	approved, err := e.cars.ApproveSeat(ctx, r(driver), car.ID, a)
	must(t, err)
	if v := of(t, approved, driver); !sameUsers(v.Riders, a) || !sameUsers(v.Pending, b) {
		t.Fatalf("onay sonrası: %+v", v)
	}
	before = e.changes(t, ev.ID, key)
	if _, err := e.cars.ApproveSeat(ctx, r(driver), car.ID, a); err != nil || e.changes(t, ev.ID, key) != before {
		t.Fatalf("yinelenen onay: %v", err)
	}
	// Everyone sees who rides along.
	got, err := e.cars.List(ctx, r(bystander))
	must(t, err)
	if v := of(t, got, driver); !sameUsers(v.Riders, a) || len(v.Pending) != 0 {
		t.Fatalf("başka üyenin gördüğü yolcular: %+v", v)
	}

	// Never more riders than seats.
	_, err = e.cars.ApproveSeat(ctx, r(driver), car.ID, b)
	must(t, err)
	_, err = e.cars.RequestSeat(ctx, r(c), car.ID)
	wantErr(t, "dolu araca istek", err, apperr.ErrVehicleFull)
	_, err = e.cars.Set(ctx, r(driver), vehicles.Input{Make: "Ford", Seats: 1})
	wantErr(t, "yolcudan az koltuk", err, apperr.ErrVehicleSeatsTaken)
	_, err = e.cars.Set(ctx, r(driver), vehicles.Input{Make: "Ford", Seats: 3})
	must(t, err)
	_, err = e.cars.RequestSeat(ctx, r(c), car.ID)
	must(t, err)
	set, err = e.cars.Set(ctx, r(driver), vehicles.Input{Make: "Ford", Seats: 2})
	must(t, err)
	if v := of(t, set, driver); v.ID != car.ID || v.Seats != 2 || !sameUsers(v.Riders, a, b) || !sameUsers(v.Pending, c) {
		t.Fatalf("koltuk sayısı değişen araç: %+v", v)
	}
	_, err = e.cars.ApproveSeat(ctx, r(driver), car.ID, c)
	wantErr(t, "dolu araçta onay", err, apperr.ErrVehicleFull)
	if n := e.count(t, `SELECT count(*) FROM event_vehicle_seat_requests WHERE event_vehicle_id = $1 AND status = 'approved'`, car.ID.PG()); n != 2 {
		t.Fatalf("onaylı yolcu = %d", n)
	}

	// One request per person: asking another vehicle moves the request,
	// an approved seat included.
	moved, err := e.cars.RequestSeat(ctx, r(a), second.ID)
	must(t, err)
	if v := of(t, moved, driver); !sameUsers(v.Riders, b) {
		t.Fatalf("taşınan isteğin eski aracı: %+v", v)
	}
	if v := of(t, moved, other); !sameUsers(v.Pending, a) || len(v.Riders) != 0 {
		t.Fatalf("taşınan isteğin yeni aracı: %+v", v)
	}
	if n := e.count(t, `SELECT count(*) FROM event_vehicle_seat_requests WHERE user_id = $1`, a.PG()); n != 1 {
		t.Fatalf("kişinin istekleri = %d", n)
	}

	// Releasing: the requester, the owner and managers; nobody else, and
	// nobody else learns whether a request exists.
	_, err = e.cars.ReleaseSeat(ctx, r(bystander), car.ID, c)
	wantErr(t, "başkasının isteğini kaldırma", err, apperr.ErrPermissionDenied)
	_, err = e.cars.ReleaseSeat(ctx, r(bystander), car.ID, id.New())
	wantErr(t, "olmayan isteği yoklama", err, apperr.ErrPermissionDenied)
	_, err = e.cars.ReleaseSeat(ctx, r(other), car.ID, c)
	wantErr(t, "başka aracın sahibi", err, apperr.ErrPermissionDenied)
	_, err = e.cars.ReleaseSeat(ctx, r(driver), car.ID, bystander)
	wantErr(t, "olmayan isteği kaldırma", err, apperr.ErrEventItemNotFound)
	_, err = e.cars.ReleaseSeat(ctx, r(driver), second.ID, a)
	wantErr(t, "başka aracın isteğini kaldırma", err, apperr.ErrPermissionDenied)
	_, err = e.cars.ReleaseSeat(ctx, r(a), car.ID, a)
	wantErr(t, "yanlış araçtan çekilme", err, apperr.ErrEventItemNotFound)
	_, err = e.cars.ReleaseSeat(ctx, r(c), id.New(), c)
	wantErr(t, "olmayan araçtan çekilme", err, apperr.ErrEventItemNotFound)
	_, err = e.cars.ReleaseSeat(ctx, r(c), car.ID, c) // the requester withdraws
	must(t, err)
	_, err = e.cars.ReleaseSeat(ctx, r(driver), car.ID, b) // the owner removes a rider
	must(t, err)
	released, err := e.cars.ReleaseSeat(ctx, r(admin), second.ID, a) // a manager removes any
	must(t, err)
	if len(of(t, released, driver).Riders)+len(of(t, released, other).Pending) != 0 || e.count(t, `SELECT count(*) FROM event_vehicle_seat_requests`) != 0 {
		t.Fatalf("kaldırılan istekler: %+v", released)
	}

	// A driver rides in their own vehicle: setting a vehicle removes the
	// caller's request.
	_, err = e.cars.RequestSeat(ctx, r(a), car.ID)
	must(t, err)
	_, err = e.cars.Set(ctx, r(a), vehicles.Input{Make: "Renault", Seats: 4})
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM event_vehicle_seat_requests`); n != 0 {
		t.Fatalf("araç yazanın isteği kaldı: %d", n)
	}

	// Leaving the event: the rider's request goes; the driver's vehicle
	// goes with the requests made to it.
	_, err = e.cars.RequestSeat(ctx, r(b), car.ID)
	must(t, err)
	_, err = e.cars.RequestSeat(ctx, r(c), car.ID)
	must(t, err)
	_, err = e.cars.ApproveSeat(ctx, r(driver), car.ID, c)
	must(t, err)
	_, err = e.events.SetRSVP(ctx, b, tf, ev.ID, event.RSVPMaybe)
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM event_vehicle_seat_requests WHERE user_id = $1`, b.PG()); n != 0 {
		t.Fatalf("ayrılanın isteği kaldı: %d", n)
	}
	_, err = e.cars.RequestSeat(ctx, r(b), car.ID)
	wantErr(t, "ayrılanın isteği", err, apperr.ErrEventNotGoing)
	_, err = e.events.SetRSVP(ctx, driver, tf, ev.ID, event.RSVPNotGoing)
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM event_vehicles WHERE user_id = $1`, driver.PG()) + e.count(t, `SELECT count(*) FROM event_vehicle_seat_requests`); n != 0 {
		t.Fatalf("ayrılan sürücünün aracı ya da yolcuları kaldı: %d", n)
	}

	// Nothing about who rides with whom reaches the outbox, and the audit
	// record names the vehicle only.
	for _, u := range []id.ID{a, b, c} {
		if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND payload::text LIKE '%' || $2 || '%'`, component.EventChanged, u.String()); n != 0 {
			t.Fatalf("canlı güncelleme olayında kişi: %d", n)
		}
		if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action LIKE 'event.vehicle_seat_%' AND metadata::text LIKE '%' || $1 || '%'`, u.String()); n != 0 {
			t.Fatalf("koltuk denetim kaydında kişi: %d", n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = ANY($1)`,
		[]string{vehicles.AuditSeatRequested, vehicles.AuditSeatApproved, vehicles.AuditSeatReleased}); n == 0 {
		t.Fatal("koltuk denetim kaydı yok")
	}
}

// Who may read and write, for every role (ADR-0025 §5), and that an event
// and a vehicle are reachable only through their own tayfa and event.
func TestVehiclesPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	e.letMembersCreate(t, tf, admin)
	org, moderator := e.member(t, tf, authz.Member), e.member(t, tf, authz.Moderator)
	driver, member := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	mutedDriver, mutedRider, mutedModerator := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member), e.member(t, tf, authz.Moderator)
	stranger := id.New()
	ev := e.newEvent(t, tf, org, poolType)
	const key = "cars"
	r := func(u id.ID) component.Ref { return ref(u, tf, ev.ID, key) }
	e.going(t, tf, ev.ID, driver, member, mutedDriver, mutedRider, mutedModerator)
	set, err := e.cars.Set(ctx, r(driver), vehicles.Input{Make: "Ford", Seats: 4})
	must(t, err)
	car := of(t, set, driver)
	set, err = e.cars.Set(ctx, r(mutedDriver), vehicles.Input{Make: "Fiat", Plate: "06 XYZ 06", Seats: 1})
	must(t, err)
	mutedCar := of(t, set, mutedDriver)
	_, err = e.cars.RequestSeat(ctx, r(mutedRider), car.ID)
	must(t, err)
	for _, u := range []id.ID{mutedDriver, mutedRider, mutedModerator} {
		e.mute(t, tf, u)
	}

	denied, muted, gone := apperr.ErrPermissionDenied, apperr.ErrMuted, apperr.ErrTayfaNotFound
	for _, row := range []struct {
		who                       string
		user                      id.ID
		read, set, removeOther    error
		canParticipate, canManage bool
	}{
		{"yönetici (katılmıyor)", admin, nil, apperr.ErrEventNotGoing, nil, false, true},
		{"moderatör (katılmıyor)", moderator, nil, apperr.ErrEventNotGoing, nil, false, true},
		{"organizatör (üye, katılmıyor)", org, nil, apperr.ErrEventNotGoing, nil, false, true},
		{"üye (katılıyor)", member, nil, nil, denied, true, false},
		{"susturulmuş üye (katılıyor)", mutedRider, nil, muted, denied, false, false},
		{"susturulmuş moderatör (katılıyor)", mutedModerator, nil, muted, muted, false, false},
		{"üye değil", stranger, gone, gone, gone, false, false},
	} {
		got, err := e.cars.List(ctx, r(row.user))
		wantErr(t, row.who+" / okuma", err, row.read)
		if row.read == nil && (len(got.Items) == 0 || got.CallerCanParticipate != row.canParticipate || got.CallerCanManage != row.canManage) {
			t.Errorf("%s / okunan: %d araç, katılabilir=%v, yönetebilir=%v", row.who, len(got.Items), got.CallerCanParticipate, got.CallerCanManage)
		}
		_, err = e.cars.Set(ctx, r(row.user), vehicles.Input{Make: "Opel"})
		wantErr(t, row.who+" / araç yazma", err, row.set)
		// Someone else's vehicle: a fresh one each time.
		_, err = e.cars.Set(ctx, r(driver), vehicles.Input{Make: "Ford", Seats: 4})
		must(t, err)
		cur, err := e.cars.List(ctx, r(driver))
		must(t, err)
		_, err = e.cars.Remove(ctx, r(row.user), of(t, cur, driver).ID)
		wantErr(t, row.who+" / başkasının aracını çıkarma", err, row.removeOther)
		// The member's own vehicle, written above, goes again.
		if row.set == nil {
			mine, err := e.cars.List(ctx, r(row.user))
			must(t, err)
			_, err = e.cars.Remove(ctx, r(row.user), of(t, mine, row.user).ID)
			must(t, err)
		}
	}

	// A mute stops new entries, not taking one's own data back.
	_, err = e.cars.RequestSeat(ctx, r(mutedModerator), mutedCar.ID)
	wantErr(t, "susturulmuşun koltuk isteği", err, muted)
	_, err = e.cars.ApproveSeat(ctx, r(mutedDriver), mutedCar.ID, member)
	wantErr(t, "susturulmuş sürücünün onayı", err, muted)
	// The muted rider's request went with the driver's first vehicle
	// above; write one directly to withdraw.
	cur, err := e.cars.List(ctx, r(driver))
	must(t, err)
	e.exec(t, `INSERT INTO event_vehicle_seat_requests (id, event_id, component_key, user_id, event_vehicle_id) VALUES ($1, $2, $3, $4, $5)`,
		id.New().PG(), ev.ID.PG(), key, mutedRider.PG(), of(t, cur, driver).ID.PG())
	if _, err := e.cars.ReleaseSeat(ctx, r(mutedRider), of(t, cur, driver).ID, mutedRider); err != nil {
		t.Fatalf("susturulmuşun isteğini geri çekmesi: %v", err)
	}
	left, err := e.cars.Remove(ctx, r(mutedDriver), mutedCar.ID)
	if err != nil || len(left.Items) != 1 {
		t.Fatalf("susturulmuşun kendi aracını çıkarması: %+v %v", left, err)
	}

	// IDOR: an event of another tayfa under the caller's own tayfa, and a
	// vehicle of another event under this one.
	otherTayfa, otherAdmin := e.newTayfa(t)
	foreign := e.newEvent(t, otherTayfa, otherAdmin, poolType)
	e.going(t, otherTayfa, foreign.ID, otherAdmin)
	fset, err := e.cars.Set(ctx, ref(otherAdmin, otherTayfa, foreign.ID, key), vehicles.Input{Make: "Tofaş", Seats: 2})
	must(t, err)
	foreignCar := of(t, fset, otherAdmin)
	_, err = e.cars.List(ctx, ref(admin, tf, foreign.ID, key))
	wantErr(t, "başka tayfanın etkinliği / okuma", err, apperr.ErrEventNotFound)
	_, err = e.cars.Set(ctx, ref(admin, tf, foreign.ID, key), vehicles.Input{Make: "Opel"})
	wantErr(t, "başka tayfanın etkinliği / yazma", err, apperr.ErrEventNotFound)
	_, err = e.cars.Remove(ctx, ref(admin, otherTayfa, foreign.ID, key), foreignCar.ID)
	wantErr(t, "başka tayfanın yöneticisi / çıkarma", err, gone)
	_, err = e.cars.Remove(ctx, r(admin), foreignCar.ID)
	wantErr(t, "başka etkinliğin aracı / çıkarma", err, apperr.ErrEventItemNotFound)
	_, err = e.cars.RequestSeat(ctx, r(member), foreignCar.ID)
	wantErr(t, "başka etkinliğin aracı / koltuk isteği", err, apperr.ErrEventItemNotFound)
	if n := e.count(t, `SELECT count(*) FROM event_vehicles WHERE event_id = $1`, foreign.ID.PG()); n != 1 {
		t.Fatalf("reddedilen istek başka tayfanın aracına dokundu: %d", n)
	}
}

// The write window (ADR-0025 §5): vehicles and seats are added until the
// event ends, removed until the component freezes seven days later; never
// on a cancelled event or in a read-only tayfa. Reading always works.
func TestVehiclesWriteWindow(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	driver, rider, late := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin, poolType)
	cancelled := e.newEvent(t, tf, admin, poolType)
	open := e.newEvent(t, tf, admin, poolType)
	const key = "cars"
	r := func(u id.ID) component.Ref { return ref(u, tf, ev.ID, key) }
	for _, x := range []id.ID{ev.ID, cancelled.ID, open.ID} {
		e.going(t, tf, x, driver, rider, late)
	}
	set, err := e.cars.Set(ctx, r(driver), vehicles.Input{Make: "Ford", Seats: 2})
	must(t, err)
	car := of(t, set, driver)
	_, err = e.cars.RequestSeat(ctx, r(rider), car.ID)
	must(t, err)

	// Started: everything still works.
	e.clock.set(ev.StartsAt.Add(time.Minute))
	if _, err := e.cars.Set(ctx, r(driver), vehicles.Input{Make: "Ford", Seats: 3}); err != nil {
		t.Fatalf("başlamış etkinlikte araç: %v", err)
	}
	_, err = e.cars.RequestSeat(ctx, r(late), car.ID)
	must(t, err)
	_, err = e.cars.ApproveSeat(ctx, r(driver), car.ID, rider)
	must(t, err)

	// Ended: nothing is added, things may still be taken back.
	e.clock.set(ev.EndsAt)
	_, err = e.cars.Set(ctx, r(driver), vehicles.Input{Make: "Ford", Seats: 4})
	wantErr(t, "bitmiş etkinlikte araç", err, apperr.ErrEventEnded)
	_, err = e.cars.Set(ctx, r(late), vehicles.Input{Make: "Opel"})
	wantErr(t, "bitmiş etkinlikte yeni araç", err, apperr.ErrEventEnded)
	_, err = e.cars.RequestSeat(ctx, r(rider), car.ID)
	wantErr(t, "bitmiş etkinlikte istek", err, apperr.ErrEventEnded)
	_, err = e.cars.ApproveSeat(ctx, r(driver), car.ID, late)
	wantErr(t, "bitmiş etkinlikte onay", err, apperr.ErrEventEnded)
	got, err := e.cars.List(ctx, r(driver))
	must(t, err)
	if got.CallerCanParticipate || of(t, got, driver).Seats != 3 {
		t.Fatalf("bitmiş etkinlikte okuma: %+v", got)
	}
	if _, err := e.cars.ReleaseSeat(ctx, r(late), car.ID, late); err != nil {
		t.Fatalf("bitmiş etkinlikte isteği geri çekme: %v", err)
	}
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter - time.Microsecond))
	if _, err := e.cars.ReleaseSeat(ctx, r(admin), car.ID, rider); err != nil {
		t.Fatalf("pencerenin son anında yolcuyu çıkarma: %v", err)
	}

	// Frozen: seven days after the end.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.cars.Remove(ctx, r(driver), car.ID)
	wantErr(t, "donmuş etkinlikte çıkarma", err, apperr.ErrComponentFrozen)
	_, err = e.cars.Remove(ctx, r(admin), car.ID)
	wantErr(t, "donmuş etkinlikte yöneticinin çıkarması", err, apperr.ErrComponentFrozen)
	_, err = e.cars.Set(ctx, r(driver), vehicles.Input{Make: "Ford"})
	wantErr(t, "donmuş etkinlikte araç", err, apperr.ErrComponentFrozen)
	got, err = e.cars.List(ctx, r(admin))
	must(t, err)
	if len(got.Items) != 1 || got.CallerCanManage || got.CallerCanParticipate {
		t.Fatalf("donmuş etkinlikte okuma: %+v", got)
	}

	// A cancelled event.
	e.clock.set(ev.StartsAt.Add(-time.Hour))
	cr := func(u id.ID) component.Ref { return ref(u, tf, cancelled.ID, key) }
	cset, err := e.cars.Set(ctx, cr(driver), vehicles.Input{Make: "Ford", Seats: 2})
	must(t, err)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.cars.Set(ctx, cr(driver), vehicles.Input{Make: "Ford", Seats: 3})
	wantErr(t, "iptal edilmiş etkinlikte araç", err, apperr.ErrEventCancelled)
	_, err = e.cars.RequestSeat(ctx, cr(rider), of(t, cset, driver).ID)
	wantErr(t, "iptal edilmiş etkinlikte istek", err, apperr.ErrEventCancelled)
	_, err = e.cars.Remove(ctx, cr(driver), of(t, cset, driver).ID)
	wantErr(t, "iptal edilmiş etkinlikte çıkarma", err, apperr.ErrEventCancelled)
	if got, err := e.cars.List(ctx, cr(driver)); err != nil || len(got.Items) != 1 || got.CallerCanParticipate || got.CallerCanManage {
		t.Fatalf("iptal edilmiş etkinlikte okuma: %+v %v", got, err)
	}

	// A read-only tayfa.
	or := func(u id.ID) component.Ref { return ref(u, tf, open.ID, key) }
	e.exec(t, `UPDATE tayfas SET suspended_at = now(), suspended_reason = 'test', suspended_by = $2 WHERE id = $1`, tf.PG(), id.New().PG())
	_, err = e.cars.Set(ctx, or(driver), vehicles.Input{Make: "Ford"})
	wantErr(t, "salt okunur tayfada araç", err, apperr.ErrTayfaReadOnly)
	if got, err := e.cars.List(ctx, or(admin)); err != nil || got.CallerCanManage || got.CallerCanParticipate {
		t.Fatalf("salt okunur tayfada okuma: %+v %v", got, err)
	}
}

// Vehicles are written only where the event's type version has the
// component (ADR-0025 §4): the service refuses, and so do the tables'
// triggers for a write that goes around the service.
func TestVehiclesOnlyWhereTheTypeVersionHasIt(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	in := vehicles.Input{Make: "Ford"}
	refused := func(what string, ev id.ID, key string) {
		t.Helper()
		_, err := e.pool.Exec(ctx, `INSERT INTO event_vehicles (id, event_id, component_key, user_id, make) VALUES ($1, $2, $3, $4, 'Ford')`,
			id.New().PG(), ev.PG(), key, id.New().PG())
		if constraintOf(err) != "event_component_guard" {
			t.Errorf("%s: doğrudan yazma: %v, event_component_guard bekleniyordu", what, err)
		}
	}
	all := func(what string, r component.Ref, want error) {
		t.Helper()
		_, err := e.cars.List(ctx, r)
		wantErr(t, what+" / okuma", err, want)
		_, err = e.cars.Set(ctx, r, in)
		wantErr(t, what+" / araç yazma", err, want)
		_, err = e.cars.Remove(ctx, r, id.New())
		wantErr(t, what+" / çıkarma", err, want)
		_, err = e.cars.RequestSeat(ctx, r, id.New())
		wantErr(t, what+" / koltuk isteği", err, want)
		_, err = e.cars.ApproveSeat(ctx, r, id.New(), id.New())
		wantErr(t, what+" / onay", err, want)
		_, err = e.cars.ReleaseSeat(ctx, r, id.New(), id.New())
		wantErr(t, what+" / bırakma", err, want)
	}

	// A type without the component.
	plain := e.newEvent(t, tf, admin, event.TypeGeneral)
	e.going(t, tf, plain.ID, admin)
	all("bileşensiz tür", ref(admin, tf, plain.ID, "vehicles"), apperr.ErrComponentNotFound)
	refused("bileşensiz tür", plain.ID, "vehicles")

	// A keyed component: the key, not the kind; a component of another
	// kind is not vehicles.
	pool := e.newEvent(t, tf, admin, poolType)
	e.going(t, tf, pool.ID, admin)
	set, err := e.cars.Set(ctx, ref(admin, tf, pool.ID, "cars"), in)
	must(t, err)
	all("anahtarlı bileşende kind", ref(admin, tf, pool.ID, "vehicles"), apperr.ErrComponentNotFound)
	all("başka türün bileşeni", ref(admin, tf, pool.ID, "draw"), apperr.ErrComponentNotFound)
	refused("anahtarlı bileşende kind", pool.ID, "vehicles")
	refused("başka türün bileşeni", pool.ID, "draw")
	// A seat request goes through the same guard.
	_, err = e.pool.Exec(ctx, `INSERT INTO event_vehicle_seat_requests (id, event_id, component_key, user_id, event_vehicle_id) VALUES ($1, $2, 'draw', $3, $4)`,
		id.New().PG(), pool.ID.PG(), id.New().PG(), of(t, set, admin).ID.PG())
	if constraintOf(err) != "event_component_guard" {
		t.Errorf("başka bileşene koltuk isteği: %v", err)
	}

	// The type's next version drops the component: the old event keeps
	// it, an event of the new version has none.
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, poolType, poolV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, poolType, poolV2)
	newer := e.newEvent(t, tf, admin, poolType)
	e.going(t, tf, newer.ID, admin)
	if newer.TypeVersion != 2 || pool.TypeVersion != 1 {
		t.Fatalf("tür sürümleri: eski %d, yeni %d", pool.TypeVersion, newer.TypeVersion)
	}
	all("yeni sürümde olmayan bileşen", ref(admin, tf, newer.ID, "cars"), apperr.ErrComponentNotFound)
	refused("yeni sürümde olmayan bileşen", newer.ID, "cars")
	if got, err := e.cars.Set(ctx, ref(admin, tf, pool.ID, "cars"), vehicles.Input{Make: "Fiat", Seats: 2}); err != nil || of(t, got, admin).Seats != 2 {
		t.Fatalf("eski sürümün etkinliği: %+v %v", got, err)
	}

	// An optional component that is off: every method answers
	// component-disabled until the organizer switches it on. This one has
	// seats and no plates.
	trip := e.newEvent(t, tf, admin, tripType)
	e.going(t, tf, trip.ID, admin)
	all("kapalı bileşen", ref(admin, tf, trip.ID, "vehicles"), apperr.ErrComponentDisabled)
	refused("kapalı bileşen", trip.ID, "vehicles")
	_, err = e.fw.Set(ctx, admin, tf, trip.ID, component.Selection{Key: "vehicles", Enabled: on(true)})
	must(t, err)
	_, err = e.cars.Set(ctx, ref(admin, tf, trip.ID, "vehicles"), vehicles.Input{Make: "Ford", Plate: "35 ABC 123"})
	wantField(t, "plaka kapalıyken plaka", err, "plate")
	got, err := e.cars.Set(ctx, ref(admin, tf, trip.ID, "vehicles"), vehicles.Input{Make: "Ford", Seats: vehicles.MaxSeats})
	if err != nil || got.PlateEnabled || !got.SeatsEnabled || of(t, got, admin).Seats != vehicles.MaxSeats {
		t.Fatalf("açılan bileşen: %+v %v", got, err)
	}

	if n := e.count(t, `SELECT count(*) FROM event_vehicles`); n != 2 {
		t.Fatalf("araç satırları = %d (havuz ve açılan gezi)", n)
	}

	// The events module off: nothing is reachable, reads included.
	must(t, e.tayfas.SetModule(ctx, admin, tf, "events", false))
	all("modül kapalı", ref(admin, tf, pool.ID, "cars"), apperr.ErrModuleDisabled)
}
