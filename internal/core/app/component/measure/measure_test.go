package measure_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/measure"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. matchType has a personal measure the organizer and referees
// enter (goals); examType a sensitive personal measure each participant
// enters for themselves (a mock exam's net); optType an optional total
// measure that is off by default and has no bounds of its own, and a
// second version without it.
const (
	matchType = "test_match"
	matchV1   = `{"components":[{"kind":"measure","key":"goals","labels":{"tr":"Gol"},"params":{"unit":"goal","scope":"personal","entered_by":"organizer","min":0,"max":50,"decimals":0,"showcase":true}}]}`
	examType  = "test_exam"
	examV1    = `{"components":[{"kind":"measure","key":"net","labels":{"tr":"Net"},"params":{"unit":"net","scope":"personal","entered_by":"self","min":-40,"max":120,"decimals":2,"sensitive":true,"showcase":false}}]}`
	optType   = "test_opt"
	optV1     = `{"components":[{"kind":"measure","key":"bags","labels":{"tr":"Torba"},"optional":{"default":false},"params":{"unit":"bag","scope":"total","entered_by":"organizer"}}]}`
	optV2     = `{"components":[{"kind":"draw"}]}`
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
	fw       *component.Service
	measures *measure.Service
	events   *event.Service
	tayfas   *tayfa.Service
	pool     *pgxpool.Pool
	admin    *pgxpool.Pool
	clock    *clock
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
	e := &env{fw: fw, measures: measure.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	e.addType(t, matchType, matchV1)
	e.addType(t, examType, examV1)
	e.addType(t, optType, optV1)
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Ölçüm Tayfası", ClientRequestID: id.New().String()})
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

// going adds a member who answered "going".
func (e *env) going(t *testing.T, tf, ev id.ID, role authz.Role) id.ID {
	t.Helper()
	u := e.member(t, tf, role)
	_, err := e.events.SetRSVP(ctx, u, tf, ev, event.RSVPGoing)
	must(t, err)
	return u
}

// attended adds a member whose attendance was verified.
func (e *env) attended(t *testing.T, tf, ev id.ID, role authz.Role) id.ID {
	t.Helper()
	u := e.going(t, tf, ev, role)
	e.exec(t, `UPDATE event_rsvps SET attendance = 'attended', attendance_method = 'location', checked_in_at = now()
		WHERE event_id = $1 AND user_id = $2`, ev.PG(), u.PG())
	return u
}

// newEvent creates an event of typ that starts in a day and lasts three
// hours.
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string, fields map[string]json.RawMessage, sels ...component.Selection) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Pazar buluşması", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(), Fields: fields, Components: sels,
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

func value(p *float64) string {
	if p == nil {
		return "yok"
	}
	b, _ := json.Marshal(*p)
	return string(b)
}

// byUser returns the entries by person.
func byUser(m measure.Measure) map[id.ID]float64 {
	out := map[id.ID]float64{}
	for _, x := range m.Entries {
		out[x.User] = x.Value
	}
	return out
}

// The hike's total measures (the catalog's hike, ADR-0037 §2): only
// managers write, from the start; values are rounded and bounded; the
// planned value is suggested after the end until someone confirms it; the
// component freezes seven days after the end.
func TestMeasureTotalAndSuggestion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeHike, map[string]json.RawMessage{
		"planned_distance": json.RawMessage(`12.5`), "planned_elevation": json.RawMessage(`800`),
	})
	hiker := e.attended(t, tf, ev.ID, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	const key = "distance"

	got, err := e.measures.Get(ctx, ref(hiker, tf, ev.ID, key))
	must(t, err)
	if got.Unit != "km" || got.Scope != measure.ScopeTotal || got.EnteredBy != measure.EnteredByOrganizer || got.Min != 0 || got.Max != 1000 ||
		got.Decimals != 2 || got.Total != nil || got.Suggested != nil || got.CallerCanEdit || got.Entries == nil || len(got.Entries) != 0 {
		t.Fatalf("başlangıç: %+v", got)
	}
	// Nothing is measured before the start.
	_, err = e.measures.SetTotal(ctx, ref(admin, tf, ev.ID, key), 10)
	wantErr(t, "başlamadan", err, apperr.ErrMeasurementNotStarted)
	if got, err := e.measures.Get(ctx, ref(admin, tf, ev.ID, key)); err != nil || got.CallerCanEdit {
		t.Fatalf("başlamadan yazma izni: %+v %v", got, err)
	}

	e.clock.set(ev.StartsAt.Add(time.Hour))
	if got, err := e.measures.Get(ctx, ref(admin, tf, ev.ID, key)); err != nil || !got.CallerCanEdit || got.Suggested != nil {
		t.Fatalf("başladıktan sonra yönetici: %+v %v", got, err)
	}
	_, err = e.measures.SetTotal(ctx, ref(hiker, tf, ev.ID, key), 10)
	wantErr(t, "katılımcı toplam giremez", err, apperr.ErrPermissionDenied)
	_, err = e.measures.SetTotal(ctx, ref(admin, tf, ev.ID, key), 1000.01)
	wantField(t, "üst sınır", err, "value")
	_, err = e.measures.SetTotal(ctx, ref(admin, tf, ev.ID, key), -1)
	wantField(t, "alt sınır", err, "value")
	_, err = e.measures.SetEntry(ctx, ref(admin, tf, ev.ID, key), hiker, 3)
	wantErr(t, "toplam ölçümde kişisel değer", err, apperr.ErrMeasurementScope)

	got, err = e.measures.SetTotal(ctx, ref(admin, tf, ev.ID, key), 12.3456)
	must(t, err)
	if got.Total == nil || *got.Total != 12.35 || got.TotalUpdatedAt.IsZero() || !got.CallerCanEdit {
		t.Fatalf("yazılan toplam: %s %+v", value(got.Total), got)
	}
	if n := e.changes(t, ev.ID, key); n != 1 {
		t.Fatalf("canlı güncelleme = %d", n)
	}
	// The same value again changes nothing.
	_, err = e.measures.SetTotal(ctx, ref(moderator, tf, ev.ID, key), 12.35)
	must(t, err)
	if n := e.changes(t, ev.ID, key); n != 1 {
		t.Fatalf("aynı değer canlı güncelleme yazdı: %d", n)
	}
	if got, err := e.measures.Get(ctx, ref(hiker, tf, ev.ID, key)); err != nil || got.Total == nil || *got.Total != 12.35 || got.CallerCanEdit {
		t.Fatalf("katılımcının gördüğü: %+v %v", got, err)
	}
	// The elevation has no decimals.
	if got, err := e.measures.SetTotal(ctx, ref(admin, tf, ev.ID, "elevation_gain"), 812.6); err != nil || got.Total == nil || *got.Total != 813 || got.Unit != "m" {
		t.Fatalf("irtifa: %+v %v", got, err)
	}

	// After the end the planned value is suggested while there is no total;
	// writing it confirms it.
	e.clock.set(ev.EndsAt.Add(time.Hour))
	got, err = e.measures.RemoveTotal(ctx, ref(admin, tf, ev.ID, key))
	must(t, err)
	if got.Total != nil || got.Suggested == nil || *got.Suggested != 12.5 {
		t.Fatalf("öneri: toplam %s, öneri %s", value(got.Total), value(got.Suggested))
	}
	if n := e.count(t, `SELECT count(*) FROM event_measurements WHERE event_id = $1 AND component_key = $2`, ev.ID.PG(), key); n != 0 {
		t.Fatal("öneri tabloya yazıldı")
	}
	// Removing what is not there is not an error.
	_, err = e.measures.RemoveTotal(ctx, ref(admin, tf, ev.ID, key))
	must(t, err)
	got, err = e.measures.SetTotal(ctx, ref(admin, tf, ev.ID, key), *got.Suggested)
	must(t, err)
	if got.Total == nil || *got.Total != 12.5 || got.Suggested != nil {
		t.Fatalf("onaylanan öneri: toplam %s, öneri %s", value(got.Total), value(got.Suggested))
	}
	if got, err := e.measures.Get(ctx, ref(hiker, tf, ev.ID, "elevation_gain")); err != nil || got.Suggested != nil || got.Total == nil {
		t.Fatalf("değeri olan ölçümde öneri: %+v %v", got, err)
	}

	// Seven days after the end the measure freezes; reading still works.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.measures.SetTotal(ctx, ref(admin, tf, ev.ID, key), 13)
	wantErr(t, "donmuş ölçüm", err, apperr.ErrComponentFrozen)
	_, err = e.measures.RemoveTotal(ctx, ref(admin, tf, ev.ID, key))
	wantErr(t, "donmuş ölçümü kaldırma", err, apperr.ErrComponentFrozen)
	if got, err := e.measures.Get(ctx, ref(admin, tf, ev.ID, key)); err != nil || got.CallerCanEdit || got.Total == nil {
		t.Fatalf("donmuş ölçüm okuma: %+v %v", got, err)
	}

	// The audit records name the component, never the value.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND metadata ->> 'component_key' = $3`,
		measure.AuditSet, ev.ID.PG(), key); n != 2 {
		t.Fatalf("denetim kaydı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action LIKE 'event.measurement_%'
		AND EXISTS (SELECT 1 FROM jsonb_object_keys(metadata) AS k WHERE k NOT IN ('component_key', 'scope', 'user_id'))`, ev.ID.PG()); n != 0 {
		t.Fatal("değer denetim kaydına yazıldı")
	}
}

// A personal measure the organizer enters (goals, ADR-0037 §3): managers
// and assigned referees write any participant's value; nobody else, not
// the player either.
func TestMeasurePersonalByOrganizer(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	ev := e.newEvent(t, tf, organizer, matchType, nil)
	moderator := e.member(t, tf, authz.Moderator)
	const key = "goals"
	player := e.going(t, tf, ev.ID, authz.Member)
	other := e.going(t, tf, ev.ID, authz.Member)
	referee := e.member(t, tf, authz.Member)
	bystander := e.member(t, tf, authz.Member)
	_, err := e.fw.AssignReferee(ctx, organizer, tf, ev.ID, component.RefereeInput{User: referee, ComponentKey: key})
	must(t, err)

	_, err = e.measures.SetEntry(ctx, ref(referee, tf, ev.ID, key), player, 1)
	wantErr(t, "başlamadan", err, apperr.ErrMeasurementNotStarted)

	e.clock.set(ev.StartsAt.Add(30 * time.Minute))
	for name, u := range map[string]id.ID{"hakem": referee, "organizatör": organizer, "moderatör": moderator} {
		if got, err := e.measures.Get(ctx, ref(u, tf, ev.ID, key)); err != nil || !got.CallerCanEdit {
			t.Errorf("%s yazabilmeli: %+v %v", name, got, err)
		}
	}
	for name, u := range map[string]id.ID{"oyuncu": player, "izleyici": bystander} {
		if got, err := e.measures.Get(ctx, ref(u, tf, ev.ID, key)); err != nil || got.CallerCanEdit {
			t.Errorf("%s yazamamalı: %+v %v", name, got, err)
		}
	}

	got, err := e.measures.SetEntry(ctx, ref(referee, tf, ev.ID, key), player, 2.4)
	must(t, err)
	if v := byUser(got); len(v) != 1 || v[player] != 2 {
		t.Fatalf("hakemin girdiği: %+v", got.Entries)
	}
	_, err = e.measures.SetEntry(ctx, ref(organizer, tf, ev.ID, key), other, 1)
	must(t, err)
	_, err = e.measures.SetEntry(ctx, ref(referee, tf, ev.ID, key), player, 51)
	wantField(t, "üst sınır", err, "value")
	_, err = e.measures.SetEntry(ctx, ref(player, tf, ev.ID, key), player, 5)
	wantErr(t, "oyuncu kendi golünü giremez", err, apperr.ErrPermissionDenied)
	_, err = e.measures.SetEntry(ctx, ref(bystander, tf, ev.ID, key), player, 5)
	wantErr(t, "atanmamış üye", err, apperr.ErrPermissionDenied)
	_, err = e.measures.SetEntry(ctx, ref(referee, tf, ev.ID, key), bystander, 1)
	wantErr(t, "katılımcı olmayan kişi", err, apperr.ErrMeasurementNotParticipant)
	_, err = e.measures.SetEntry(ctx, ref(referee, tf, ev.ID, key), id.New(), 1)
	wantErr(t, "üye olmayan kişi", err, apperr.ErrMeasurementNotParticipant)
	_, err = e.measures.SetEntry(ctx, ref(referee, tf, ev.ID, key), id.ID{}, 1)
	wantField(t, "boş kişi", err, "user_id")
	_, err = e.measures.SetTotal(ctx, ref(organizer, tf, ev.ID, key), 3)
	wantErr(t, "kişisel ölçümde toplam", err, apperr.ErrMeasurementScope)

	// Everyone sees every goal (the measure is not sensitive).
	got, err = e.measures.Get(ctx, ref(bystander, tf, ev.ID, key))
	must(t, err)
	if v := byUser(got); len(v) != 2 || v[player] != 2 || v[other] != 1 || got.Total != nil || got.Scope != measure.ScopePersonal {
		t.Fatalf("izleyicinin gördüğü: %+v", got)
	}
	// A value for someone else is audited with their ID only.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND metadata ->> 'user_id' = $3`,
		measure.AuditSet, ev.ID.PG(), player.String()); n != 1 {
		t.Fatalf("denetim kaydı = %d", n)
	}

	got, err = e.measures.RemoveEntry(ctx, ref(referee, tf, ev.ID, key), other)
	must(t, err)
	if v := byUser(got); len(v) != 1 {
		t.Fatalf("kaldırılan değer: %+v", got.Entries)
	}
	_, err = e.measures.RemoveEntry(ctx, ref(player, tf, ev.ID, key), player)
	wantErr(t, "oyuncu kendi golünü kaldıramaz", err, apperr.ErrPermissionDenied)
	// A muted referee enters nothing.
	e.mute(t, tf, referee)
	_, err = e.measures.SetEntry(ctx, ref(referee, tf, ev.ID, key), player, 3)
	wantErr(t, "susturulmuş hakem", err, apperr.ErrMuted)
}

// A sensitive personal measure each participant enters (a mock exam's
// net): only one's own value, only with a verified attendance; each
// person sees only their own value, managers included.
func TestMeasurePersonalSelfSensitive(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, examType, nil)
	const key = "net"
	a := e.attended(t, tf, ev.ID, authz.Member)
	b := e.attended(t, tf, ev.ID, authz.Member)
	c := e.going(t, tf, ev.ID, authz.Member)
	muted := e.attended(t, tf, ev.ID, authz.Member)
	e.mute(t, tf, muted)

	e.clock.set(ev.StartsAt.Add(2 * time.Hour))
	got, err := e.measures.SetEntry(ctx, ref(a, tf, ev.ID, key), a, 31.5)
	must(t, err)
	if v := byUser(got); len(v) != 1 || v[a] != 31.5 || !got.CallerCanEdit {
		t.Fatalf("kendi değeri: %+v", got)
	}
	if _, err := e.measures.SetEntry(ctx, ref(b, tf, ev.ID, key), b, -2.5); err != nil {
		t.Fatalf("eksi net: %v", err)
	}
	_, err = e.measures.SetEntry(ctx, ref(a, tf, ev.ID, key), b, 10)
	wantErr(t, "başkasının değeri", err, apperr.ErrPermissionDenied)
	_, err = e.measures.SetEntry(ctx, ref(admin, tf, ev.ID, key), a, 10)
	wantErr(t, "yönetici başkası adına", err, apperr.ErrPermissionDenied)
	_, err = e.measures.SetEntry(ctx, ref(c, tf, ev.ID, key), c, 10)
	wantErr(t, "katılımı doğrulanmamış", err, apperr.ErrNotAttended)
	_, err = e.measures.SetEntry(ctx, ref(muted, tf, ev.ID, key), muted, 10)
	wantErr(t, "susturulmuş", err, apperr.ErrMuted)
	_, err = e.measures.SetEntry(ctx, ref(a, tf, ev.ID, key), a, -40.5)
	wantField(t, "alt sınır", err, "value")
	if got, err := e.measures.Get(ctx, ref(c, tf, ev.ID, key)); err != nil || got.CallerCanEdit {
		t.Fatalf("doğrulanmamış katılımcı yazabilir görünüyor: %+v %v", got, err)
	}

	for name, tc := range map[string]struct {
		user id.ID
		want map[id.ID]float64
	}{
		"a":        {a, map[id.ID]float64{a: 31.5}},
		"b":        {b, map[id.ID]float64{b: -2.5}},
		"yönetici": {admin, map[id.ID]float64{}},
	} {
		got, err := e.measures.Get(ctx, ref(tc.user, tf, ev.ID, key))
		must(t, err)
		v := byUser(got)
		if len(v) != len(tc.want) {
			t.Errorf("%s gördüğü: %+v", name, got.Entries)
		}
		for u, x := range tc.want {
			if v[u] != x {
				t.Errorf("%s gördüğü: %+v", name, got.Entries)
			}
		}
	}

	_, err = e.measures.RemoveEntry(ctx, ref(a, tf, ev.ID, key), b)
	wantErr(t, "başkasının değerini kaldırma", err, apperr.ErrPermissionDenied)
	got, err = e.measures.RemoveEntry(ctx, ref(a, tf, ev.ID, key), a)
	must(t, err)
	if len(got.Entries) != 0 {
		t.Fatalf("kaldırılan kendi değeri: %+v", got.Entries)
	}
	// One's own entries are not audited.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action LIKE 'event.measurement_%'`, ev.ID.PG()); n != 0 {
		t.Fatalf("kendi değeri denetim kaydı yazdı: %d", n)
	}
}

// The component exists only where the event's type version has it, under
// its key; an optional one is off until the organizer switches it on.
// Writes outside the type version are refused by the service and the
// trigger.
func TestMeasureOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral, nil)
	hike := e.newEvent(t, tf, admin, event.TypeHike, nil)
	off := e.newEvent(t, tf, admin, optType, nil)
	bags := e.newEvent(t, tf, admin, optType, nil, component.Selection{Key: "bags", Enabled: on(true)})
	e.clock.set(general.StartsAt.Add(time.Hour))

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":      ref(admin, tf, general.ID, "distance"),
		"anahtar yerine kind": ref(admin, tf, hike.ID, "measure"),
		"başka bileşen":       ref(admin, tf, hike.ID, "roll_call"),
		"başka türün ölçümü":  ref(admin, tf, hike.ID, "goals"),
	} {
		_, err := e.measures.Get(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.measures.SetTotal(ctx, r, 1)
		wantErr(t, name+" yazma", err, apperr.ErrComponentNotFound)
		_, err = e.measures.SetEntry(ctx, r, admin, 1)
		wantErr(t, name+" kişisel yazma", err, apperr.ErrComponentNotFound)
	}
	_, err := e.measures.Get(ctx, ref(admin, tf, off.ID, "bags"))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.measures.SetTotal(ctx, ref(admin, tf, off.ID, "bags"), 3)
	wantErr(t, "kapalı bileşene yazma", err, apperr.ErrComponentDisabled)

	// Without bounds in the definition: 0 … MaxValue, no decimals.
	got, err := e.measures.SetTotal(ctx, ref(admin, tf, bags.ID, "bags"), 7.4)
	must(t, err)
	if got.Total == nil || *got.Total != 7 || got.Min != 0 || got.Max != measure.MaxValue || got.Decimals != 0 || got.Unit != "bag" {
		t.Fatalf("sınırsız tanım: %+v", got)
	}

	// The type's next version drops the measure: the old event keeps it,
	// an event of the new version has none.
	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, optType, optV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, optType, optV2)
	newer := e.newEvent(t, tf, admin, optType, nil)
	if newer.TypeVersion != 2 || bags.TypeVersion != 1 {
		t.Fatalf("tür sürümleri: eski %d, yeni %d", bags.TypeVersion, newer.TypeVersion)
	}
	e.clock.set(newer.StartsAt.Add(time.Hour))
	_, err = e.measures.SetTotal(ctx, ref(admin, tf, newer.ID, "bags"), 1)
	wantErr(t, "yeni sürümde olmayan ölçüm", err, apperr.ErrComponentNotFound)
	if got, err := e.measures.SetTotal(ctx, ref(admin, tf, bags.ID, "bags"), 9); err != nil || got.Total == nil || *got.Total != 9 {
		t.Fatalf("eski sürümün etkinliği: %+v %v", got, err)
	}

	// The trigger refuses writes that go around the service.
	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "kapalı bileşen": off.ID, "yeni sürüm": newer.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_measurements (id, event_id, component_key, value) VALUES (gen_random_uuid(), $1, 'bags', 1)`, ev.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
	// One total per component; one value per person.
	_, err = e.pool.Exec(ctx, `INSERT INTO event_measurements (id, event_id, component_key, value) VALUES (gen_random_uuid(), $1, 'bags', 2)`, bags.ID.PG())
	if err == nil || !strings.Contains(err.Error(), "event_measurements_total_idx") {
		t.Fatalf("ikinci toplam: %v", err)
	}
}

// Who may do what for every role; IDOR; a cancelled event takes nothing.
func TestMeasurePermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeHike, nil)
	cancelled := e.newEvent(t, tf, admin, event.TypeHike, nil)
	_, err := e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	const key = "distance"
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	muted := e.member(t, tf, authz.Member)
	e.mute(t, tf, muted)
	e.clock.set(ev.StartsAt.Add(time.Hour))

	for _, tc := range []struct {
		name string
		user id.ID
		want error
	}{
		{"yönetici", admin, nil},
		{"moderatör", moderator, nil},
		{"üye", member, apperr.ErrPermissionDenied},
		{"susturulmuş üye", muted, apperr.ErrPermissionDenied},
		{"üye değil", id.New(), apperr.ErrTayfaNotFound},
	} {
		_, err := e.measures.SetTotal(ctx, ref(tc.user, tf, ev.ID, key), 5)
		if tc.want == nil {
			must(t, err)
		} else if ok := errors.Is(err, tc.want) || (tc.user == muted && errors.Is(err, apperr.ErrMuted)); !ok {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
		_, err = e.measures.Get(ctx, ref(tc.user, tf, ev.ID, key))
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			wantErr(t, tc.name+" okuma", err, tc.want)
		} else {
			// A mute does not hide the measure.
			must(t, err)
		}
	}

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err = e.measures.Get(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.measures.SetTotal(ctx, ref(admin2, tf2, ev.ID, key), 1)
	wantErr(t, "başka tayfa yazma", err, apperr.ErrEventNotFound)
	_, err = e.measures.RemoveTotal(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa kaldırma", err, apperr.ErrEventNotFound)

	_, err = e.measures.SetTotal(ctx, ref(admin, tf, cancelled.ID, key), 1)
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
	if got, err := e.measures.Get(ctx, ref(admin, tf, cancelled.ID, key)); err != nil || got.CallerCanEdit {
		t.Fatalf("iptal edilmiş etkinlikte yazma izni: %+v %v", got, err)
	}
}
