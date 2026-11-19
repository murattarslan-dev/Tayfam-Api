package route_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"slices"
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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/route"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. tourType has a keyed route (the key is not the kind) next to
// another component; rideType has an optional route that is off by
// default. tourType's second version, written by a test, has no route.
const (
	tourType = "test_tour"
	tourV1   = `{"components":[{"kind":"route","key":"track","labels":{"tr":"Parkur"}},{"kind":"draw"}]}`
	tourV2   = `{"components":[{"kind":"draw"}]}`
	rideType = "test_ride"
	rideV1   = `{"components":[{"kind":"route","optional":{"default":false}}]}`
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
	routes *route.Service
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
	e := &env{fw: fw, routes: route.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	e.addType(t, tourType, tourV1)
	e.addType(t, rideType, rideV1)
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Rota Tayfası", ClientRequestID: id.New().String()})
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

// line returns n valid points; the digits make them easy to search for in
// places they must not reach.
func line(n int) []route.Point {
	out := make([]route.Point, n)
	for i := range out {
		out[i] = route.Point{Lat: 38.4321 + float64(i%50)/100, Lng: 27.1234 + float64(i%100)/100}
	}
	return out
}

// changes counts the event's component_changed events for key.
func (e *env) changes(t *testing.T, ev id.ID, key string) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND subject_id = $2 AND payload ->> 'component_key' = $3`,
		component.EventChanged, ev.PG(), key)
}

// The route end to end: set, read, replace, remove; the live update and
// the audit record carry no point.
func TestRouteSetGetRemove(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin, event.TypeConvoy)
	r := ref(admin, tf, ev.ID, "route")

	// No route yet: empty points, not an error; nothing is stored.
	got, err := e.routes.Get(ctx, ref(member, tf, ev.ID, "route"))
	must(t, err)
	if got.Points == nil || len(got.Points) != 0 || !got.UpdatedAt.IsZero() || got.CallerCanEdit {
		t.Fatalf("rotasız etkinlik: %+v", got)
	}

	pts := line(3)
	set, err := e.routes.Set(ctx, r, pts)
	must(t, err)
	if !slices.Equal(set.Points, pts) || !set.UpdatedAt.Equal(e.clock.now()) || !set.CallerCanEdit {
		t.Fatalf("yazılan rota: %+v", set)
	}
	// Every member reads it, in order; only a manager may edit.
	got, err = e.routes.Get(ctx, ref(member, tf, ev.ID, "route"))
	must(t, err)
	if !slices.Equal(got.Points, pts) || !got.UpdatedAt.Equal(e.clock.now()) || got.CallerCanEdit {
		t.Fatalf("üyenin okuduğu rota: %+v", got)
	}
	if mine, err := e.routes.Get(ctx, r); err != nil || !mine.CallerCanEdit {
		t.Fatalf("organizatörün okuduğu rota: %+v %v", mine, err)
	}
	if n := e.changes(t, ev.ID, "route"); n != 1 {
		t.Fatalf("canlı güncelleme olayı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND actor_id = $3 AND metadata ->> 'component_key' = 'route'`,
		route.AuditSet, ev.ID.PG(), admin.PG()); n != 1 {
		t.Fatalf("denetim kaydı = %d", n)
	}

	// Replacing keeps one row and moves the time.
	later := e.clock.now().Add(time.Hour)
	e.clock.set(later)
	longer := line(500)
	set, err = e.routes.Set(ctx, r, longer)
	must(t, err)
	if len(set.Points) != 500 || !set.UpdatedAt.Equal(later) {
		t.Fatalf("değişen rota: %d nokta, %v", len(set.Points), set.UpdatedAt)
	}
	got, err = e.routes.Get(ctx, r)
	must(t, err)
	if !slices.Equal(got.Points, longer) || !got.UpdatedAt.Equal(later) {
		t.Fatalf("değişen rotanın okunması: %d nokta, %v", len(got.Points), got.UpdatedAt)
	}
	if n := e.count(t, `SELECT count(*) FROM event_routes WHERE event_id = $1`, ev.ID.PG()); n != 1 {
		t.Fatalf("rota satırı = %d", n)
	}

	// The same route again writes nothing: no event, no new time.
	e.clock.set(later.Add(time.Hour))
	set, err = e.routes.Set(ctx, r, longer)
	must(t, err)
	if len(set.Points) != 500 || !set.UpdatedAt.Equal(later) || e.changes(t, ev.ID, "route") != 2 {
		t.Fatalf("aynı rota yeniden yazıldı: %v, %d olay", set.UpdatedAt, e.changes(t, ev.ID, "route"))
	}

	// The points are content: they reach neither the outbox nor the audit
	// record.
	for _, q := range []string{
		`SELECT count(*) FROM domain_events WHERE payload::text LIKE '%"lat"%' OR payload::text LIKE '%"lng"%' OR payload::text LIKE '%points%'`,
		`SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%"lat"%' OR metadata::text LIKE '%"lng"%' OR metadata::text LIKE '%points%'`,
	} {
		if n := e.count(t, q); n != 0 {
			t.Fatalf("rota noktası sızdı: %s", q)
		}
	}
	var keys int
	must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM domain_events d, jsonb_object_keys(d.payload) k WHERE d.type = $1 AND d.subject_id = $2`,
		component.EventChanged, ev.ID.PG()).Scan(&keys))
	if keys != 2*3 {
		t.Fatalf("canlı güncelleme olaylarının alanları = %d (olay başına tayfa, etkinlik, bileşen)", keys)
	}

	// Removing: no points. A second removal changes nothing.
	set, err = e.routes.Set(ctx, r, nil)
	must(t, err)
	if set.Points == nil || len(set.Points) != 0 || !set.UpdatedAt.IsZero() {
		t.Fatalf("kaldırılan rota: %+v", set)
	}
	if got, err := e.routes.Get(ctx, r); err != nil || len(got.Points) != 0 {
		t.Fatalf("kaldırma sonrası: %+v %v", got, err)
	}
	if n := e.count(t, `SELECT count(*) FROM event_routes WHERE event_id = $1`, ev.ID.PG()); n != 0 {
		t.Fatalf("kaldırılan rotanın satırı kaldı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, route.AuditRemoved, ev.ID.PG()); n != 1 {
		t.Fatalf("kaldırma denetim kaydı = %d", n)
	}
	before := e.changes(t, ev.ID, "route")
	_, err = e.routes.Set(ctx, r, []route.Point{})
	must(t, err)
	if after := e.changes(t, ev.ID, "route"); after != before || before != 3 {
		t.Fatalf("olmayan rotayı kaldırmak olay yazdı: %d → %d", before, after)
	}
}

// Who may read and write, for every role (ADR-0025 §5). "organizatör" is a
// plain member who created the event.
func TestRoutePermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	_, err := e.tayfas.Update(ctx, admin, tf, tayfa.UpdateInput{Fields: []string{tayfa.FieldMembersCanCreateEvents}, MembersCanCreateEvents: true})
	must(t, err)
	organizer, mutedOrganizer := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	moderator, member := e.member(t, tf, authz.Moderator), e.member(t, tf, authz.Member)
	mutedMember, mutedModerator := e.member(t, tf, authz.Member), e.member(t, tf, authz.Moderator)
	ev := e.newEvent(t, tf, organizer, event.TypeConvoy)
	mutedEv := e.newEvent(t, tf, mutedOrganizer, event.TypeConvoy)
	// Going gives no right to change the route.
	_, err = e.events.SetRSVP(ctx, member, tf, ev.ID, event.RSVPGoing)
	must(t, err)
	for _, u := range []id.ID{mutedMember, mutedModerator, mutedOrganizer} {
		e.mute(t, tf, u)
	}
	stranger := id.New()
	_, err = e.routes.Set(ctx, ref(admin, tf, ev.ID, "route"), line(2))
	must(t, err)

	denied, muted, gone := apperr.ErrPermissionDenied, apperr.ErrMuted, apperr.ErrTayfaNotFound
	for _, r := range []struct {
		who         string
		user        id.ID
		read, write error
	}{
		{"yönetici", admin, nil, nil},
		{"moderatör", moderator, nil, nil},
		{"organizatör (üye)", organizer, nil, nil},
		{"üye (katılıyor)", member, nil, denied},
		{"susturulmuş üye", mutedMember, nil, denied},
		{"susturulmuş moderatör", mutedModerator, nil, muted},
		{"üye değil", stranger, gone, gone},
	} {
		got, err := e.routes.Get(ctx, ref(r.user, tf, ev.ID, "route"))
		wantErr(t, r.who+" / okuma", err, r.read)
		if r.read == nil && (len(got.Points) < 2 || got.CallerCanEdit != (r.write == nil)) {
			t.Errorf("%s / okunan rota: %d nokta, düzenleyebilir=%v", r.who, len(got.Points), got.CallerCanEdit)
		}
		_, err = e.routes.Set(ctx, ref(r.user, tf, ev.ID, "route"), line(4))
		wantErr(t, r.who+" / yazma", err, r.write)
		_, err = e.routes.Set(ctx, ref(r.user, tf, ev.ID, "route"), nil)
		wantErr(t, r.who+" / kaldırma", err, r.write)
		// Leave a route for the next row.
		_, err = e.routes.Set(ctx, ref(admin, tf, ev.ID, "route"), line(2))
		must(t, err)
	}
	// A muted organizer reads their own event's route but may not change
	// it; the organizer of one event has no right on another.
	if got, err := e.routes.Get(ctx, ref(mutedOrganizer, tf, mutedEv.ID, "route")); err != nil || got.CallerCanEdit {
		t.Fatalf("susturulmuş organizatör: %+v %v", got, err)
	}
	_, err = e.routes.Set(ctx, ref(mutedOrganizer, tf, mutedEv.ID, "route"), line(2))
	wantErr(t, "susturulmuş organizatör / yazma", err, muted)
	_, err = e.routes.Set(ctx, ref(organizer, tf, mutedEv.ID, "route"), line(2))
	wantErr(t, "başka etkinliğin organizatörü / yazma", err, denied)

	// IDOR: an event of another tayfa under the caller's own tayfa.
	other, otherAdmin := e.newTayfa(t)
	foreign := e.newEvent(t, other, otherAdmin, event.TypeConvoy)
	_, err = e.routes.Get(ctx, ref(admin, tf, foreign.ID, "route"))
	wantErr(t, "başka tayfanın etkinliği / okuma", err, apperr.ErrEventNotFound)
	_, err = e.routes.Set(ctx, ref(admin, tf, foreign.ID, "route"), line(2))
	wantErr(t, "başka tayfanın etkinliği / yazma", err, apperr.ErrEventNotFound)
	_, err = e.routes.Set(ctx, ref(admin, other, foreign.ID, "route"), line(2))
	wantErr(t, "başka tayfanın yöneticisi / yazma", err, gone)
	if n := e.count(t, `SELECT count(*) FROM event_routes WHERE event_id = $1`, foreign.ID.PG()); n != 0 {
		t.Fatalf("reddedilen istek yazdı: %d", n)
	}
}

// The points are checked before anything is read; a refused route leaves
// the old one in place.
func TestRouteValidation(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeConvoy)
	r := ref(admin, tf, ev.ID, "route")
	_, err := e.routes.Set(ctx, r, line(2))
	must(t, err)

	with := func(p route.Point) []route.Point { return append(line(2), p) }
	for name, pts := range map[string][]route.Point{
		"tek nokta":          line(1),
		"501 nokta":          line(501),
		"enlem aralık dışı":  with(route.Point{Lat: 90.1, Lng: 0}),
		"boylam aralık dışı": with(route.Point{Lat: 0, Lng: -180.1}),
		"NaN enlem":          with(route.Point{Lat: math.NaN(), Lng: 0}),
		"NaN boylam":         with(route.Point{Lat: 0, Lng: math.NaN()}),
		"sonsuz enlem":       with(route.Point{Lat: math.Inf(1), Lng: 0}),
	} {
		_, err := e.routes.Set(ctx, r, pts)
		wantField(t, name, err, "points")
	}
	// Bad points are refused for a caller who could not write anyway, too:
	// nothing about the event is told.
	_, err = e.routes.Set(ctx, ref(id.New(), tf, ev.ID, "route"), line(1))
	wantField(t, "üye olmayanın bozuk rotası", err, "points")
	got, err := e.routes.Get(ctx, r)
	must(t, err)
	if !slices.Equal(got.Points, line(2)) {
		t.Fatalf("reddedilen rota eskisini değiştirdi: %+v", got.Points)
	}
	// The limits themselves are valid.
	for name, pts := range map[string][]route.Point{
		"iki nokta":      {{Lat: -90, Lng: -180}, {Lat: 90, Lng: 180}},
		"beş yüz nokta":  line(500),
		"aynı iki nokta": {{Lat: 1, Lng: 1}, {Lat: 1, Lng: 1}},
	} {
		if _, err := e.routes.Set(ctx, r, pts); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, key := range []string{"", "Route", "r", "route "} {
		_, err := e.routes.Get(ctx, ref(admin, tf, ev.ID, key))
		wantField(t, "bozuk anahtar okuma "+key, err, "component_key")
		_, err = e.routes.Set(ctx, ref(admin, tf, ev.ID, key), line(2))
		wantField(t, "bozuk anahtar yazma "+key, err, "component_key")
	}
}

// The write window (ADR-0025 §5): the route may change before, during and
// up to seven days after the event; never on a cancelled event or in a
// read-only tayfa. Reading always works.
func TestRouteWriteWindow(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeConvoy)
	r := ref(admin, tf, ev.ID, "route")
	_, err := e.routes.Set(ctx, r, line(2))
	must(t, err)

	// Started, ended, the last moment of the window: still open.
	for name, at := range map[string]time.Time{
		"başladı":            ev.StartsAt.Add(time.Minute),
		"bitti":              ev.EndsAt.Add(time.Hour),
		"pencerenin son anı": ev.EndsAt.Add(component.FreezeAfter - time.Microsecond),
	} {
		e.clock.set(at)
		got, err := e.routes.Set(ctx, r, line(3))
		if err != nil || !got.CallerCanEdit {
			t.Errorf("%s: %+v %v", name, got.CallerCanEdit, err)
		}
	}
	// Frozen: seven days after the end.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.routes.Set(ctx, r, line(4))
	wantErr(t, "donmuş yazma", err, apperr.ErrComponentFrozen)
	_, err = e.routes.Set(ctx, r, nil)
	wantErr(t, "donmuş kaldırma", err, apperr.ErrComponentFrozen)
	got, err := e.routes.Get(ctx, r)
	must(t, err)
	if len(got.Points) != 3 || got.CallerCanEdit {
		t.Fatalf("donmuş rota: %d nokta, düzenleyebilir=%v", len(got.Points), got.CallerCanEdit)
	}

	// A cancelled event.
	e.clock.set(ev.StartsAt.Add(-48 * time.Hour))
	cancelled := e.newEvent(t, tf, admin, event.TypeConvoy)
	cr := ref(admin, tf, cancelled.ID, "route")
	_, err = e.routes.Set(ctx, cr, line(2))
	must(t, err)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.routes.Set(ctx, cr, line(3))
	wantErr(t, "iptal edilmiş etkinlikte yazma", err, apperr.ErrEventCancelled)
	if got, err := e.routes.Get(ctx, cr); err != nil || len(got.Points) != 2 || got.CallerCanEdit {
		t.Fatalf("iptal edilmiş etkinliğin rotası: %+v %v", got, err)
	}

	// A read-only tayfa.
	open := e.newEvent(t, tf, admin, event.TypeConvoy)
	e.exec(t, `UPDATE tayfas SET suspended_at = now(), suspended_reason = 'test', suspended_by = $2 WHERE id = $1`, tf.PG(), id.New().PG())
	_, err = e.routes.Set(ctx, ref(admin, tf, open.ID, "route"), line(2))
	wantErr(t, "salt okunur tayfada yazma", err, apperr.ErrTayfaReadOnly)
	if got, err := e.routes.Get(ctx, ref(admin, tf, open.ID, "route")); err != nil || got.CallerCanEdit {
		t.Fatalf("salt okunur tayfada okuma: %+v %v", got, err)
	}
}

// A route is written only where the event's type version has the
// component (ADR-0025 §4): the service refuses, and so does the table's
// trigger for a write that goes around the service.
func TestRouteOnlyWhereTheTypeVersionHasIt(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	const insert = `INSERT INTO event_routes (event_id, component_key, points) VALUES ($1, $2, '[{"lat": 1, "lng": 2}, {"lat": 3, "lng": 4}]')`
	refused := func(what string, ev id.ID, key string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, insert, ev.PG(), key); constraintOf(err) != "event_component_guard" {
			t.Errorf("%s: doğrudan yazma: %v, event_component_guard bekleniyordu", what, err)
		}
	}

	// A type without the component.
	plain := e.newEvent(t, tf, admin, event.TypeGeneral)
	_, err := e.routes.Get(ctx, ref(admin, tf, plain.ID, "route"))
	wantErr(t, "bileşensiz tür / okuma", err, apperr.ErrComponentNotFound)
	_, err = e.routes.Set(ctx, ref(admin, tf, plain.ID, "route"), line(2))
	wantErr(t, "bileşensiz tür / yazma", err, apperr.ErrComponentNotFound)
	refused("bileşensiz tür", plain.ID, "route")

	// A keyed route: the key, not the kind; a component of another kind
	// is not a route.
	tour := e.newEvent(t, tf, admin, tourType)
	_, err = e.routes.Set(ctx, ref(admin, tf, tour.ID, "track"), line(2))
	must(t, err)
	_, err = e.routes.Set(ctx, ref(admin, tf, tour.ID, "route"), line(2))
	wantErr(t, "anahtarlı bileşende kind / yazma", err, apperr.ErrComponentNotFound)
	_, err = e.routes.Set(ctx, ref(admin, tf, tour.ID, "draw"), line(2))
	wantErr(t, "başka türün bileşeni / yazma", err, apperr.ErrComponentNotFound)
	_, err = e.routes.Get(ctx, ref(admin, tf, tour.ID, "draw"))
	wantErr(t, "başka türün bileşeni / okuma", err, apperr.ErrComponentNotFound)
	refused("anahtarlı bileşende kind", tour.ID, "route")
	refused("başka türün bileşeni", tour.ID, "draw")

	// The type's next version drops the route: the old event keeps its
	// component, an event of the new version has none.
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, tourType, tourV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, tourType, tourV2)
	newer := e.newEvent(t, tf, admin, tourType)
	if newer.TypeVersion != 2 || tour.TypeVersion != 1 {
		t.Fatalf("tür sürümleri: eski %d, yeni %d", tour.TypeVersion, newer.TypeVersion)
	}
	_, err = e.routes.Set(ctx, ref(admin, tf, newer.ID, "track"), line(2))
	wantErr(t, "yeni sürümde olmayan bileşen / yazma", err, apperr.ErrComponentNotFound)
	_, err = e.routes.Get(ctx, ref(admin, tf, newer.ID, "track"))
	wantErr(t, "yeni sürümde olmayan bileşen / okuma", err, apperr.ErrComponentNotFound)
	refused("yeni sürümde olmayan bileşen", newer.ID, "track")
	if got, err := e.routes.Set(ctx, ref(admin, tf, tour.ID, "track"), line(5)); err != nil || len(got.Points) != 5 {
		t.Fatalf("eski sürümün etkinliği: %+v %v", got, err)
	}

	// An optional route that is off: every endpoint answers
	// component-disabled until the organizer switches it on.
	ride := e.newEvent(t, tf, admin, rideType)
	_, err = e.routes.Get(ctx, ref(admin, tf, ride.ID, "route"))
	wantErr(t, "kapalı bileşen / okuma", err, apperr.ErrComponentDisabled)
	_, err = e.routes.Set(ctx, ref(admin, tf, ride.ID, "route"), line(2))
	wantErr(t, "kapalı bileşen / yazma", err, apperr.ErrComponentDisabled)
	refused("kapalı bileşen", ride.ID, "route")
	_, err = e.fw.Set(ctx, admin, tf, ride.ID, component.Selection{Key: "route", Enabled: on(true)})
	must(t, err)
	if got, err := e.routes.Set(ctx, ref(admin, tf, ride.ID, "route"), line(2)); err != nil || len(got.Points) != 2 {
		t.Fatalf("açılan bileşen: %+v %v", got, err)
	}

	if n := e.count(t, `SELECT count(*) FROM event_routes`); n != 2 {
		t.Fatalf("rota satırları = %d (tur ve açılan gezi)", n)
	}

	// The events module off: nothing is reachable, reads included.
	must(t, e.tayfas.SetModule(ctx, admin, tf, "events", false))
	_, err = e.routes.Get(ctx, ref(admin, tf, tour.ID, "track"))
	wantErr(t, "modül kapalı / okuma", err, apperr.ErrModuleDisabled)
	_, err = e.routes.Set(ctx, ref(admin, tf, tour.ID, "track"), line(2))
	wantErr(t, "modül kapalı / yazma", err, apperr.ErrModuleDisabled)
}

// A series occurrence takes the template's routes (CopyEventRoutes), after
// the template's selections: the copy passes the same trigger.
func TestRouteCopyForSeries(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	tmpl := e.newEvent(t, tf, admin, rideType, component.Selection{Key: "route", Enabled: on(true)})
	_, err := e.routes.Set(ctx, ref(admin, tf, tmpl.ID, "route"), line(7))
	must(t, err)
	q := store.New(e.pool)

	// The occurrence has the component switched on, as the template.
	occ := e.newEvent(t, tf, admin, rideType, component.Selection{Key: "route", Enabled: on(true)})
	must(t, q.CopyEventRoutes(ctx, store.CopyEventRoutesParams{ToEventID: occ.ID.PG(), FromEventID: tmpl.ID.PG()}))
	got, err := e.routes.Get(ctx, ref(admin, tf, occ.ID, "route"))
	must(t, err)
	if !slices.Equal(got.Points, line(7)) {
		t.Fatalf("kopyalanan rota: %+v", got.Points)
	}
	// Without the selection the trigger refuses the copy.
	off := e.newEvent(t, tf, admin, rideType)
	if err := q.CopyEventRoutes(ctx, store.CopyEventRoutesParams{ToEventID: off.ID.PG(), FromEventID: tmpl.ID.PG()}); constraintOf(err) != "event_component_guard" {
		t.Fatalf("kapalı bileşene kopya: %v", err)
	}
	// A template without a route copies nothing.
	bare := e.newEvent(t, tf, admin, event.TypeConvoy)
	target := e.newEvent(t, tf, admin, event.TypeConvoy)
	must(t, q.CopyEventRoutes(ctx, store.CopyEventRoutesParams{ToEventID: target.ID.PG(), FromEventID: bare.ID.PG()}))
	if n := e.count(t, `SELECT count(*) FROM event_routes WHERE event_id = $1`, target.ID.PG()); n != 0 {
		t.Fatalf("rotasız şablondan kopya: %d", n)
	}
}
