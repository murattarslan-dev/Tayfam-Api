package component_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// ---------------------------------------------------------------------------
// The test component: the pattern every component follows.
//
// "notes" keeps short notes per (event, component). Its table carries
// (event_id, component_key) and the framework's guard trigger; its service
// reads with View and writes with Change, names who may make each change,
// and marks the changes members see with Scope.Changed.

// Outbox and audit names of the test component.
const (
	noteAdded   = "event.test_note_added"
	notesClosed = "event.test_notes_cleared"
)

// notes is the test component's service for one component kind.
type notes struct {
	fw   *component.Service
	pool *pgxpool.Pool
	kind string
}

// List is a read: every member who may see the component.
func (n notes) List(ctx context.Context, ref component.Ref) ([]string, error) {
	out := []string{}
	err := n.fw.View(ctx, ref, n.kind, func(sc *component.Scope) error {
		rows, err := n.pool.Query(ctx, `SELECT note FROM test_component_notes WHERE event_id = $1 AND component_key = $2 ORDER BY id`, sc.Event.PG(), sc.Key)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// Add is a participant's change: the caller must be going.
func (n notes) Add(ctx context.Context, ref component.Ref, note string) error {
	return n.fw.Change(ctx, ref, n.kind, component.Participant(), func(sc *component.Scope) error {
		going, err := sc.Going(ctx)
		if err != nil {
			return err
		}
		if !going {
			return apperr.ErrEventNotGoing
		}
		tx, _ := sc.Q().Tx()
		nid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO test_component_notes (id, event_id, component_key, user_id, note) VALUES ($1, $2, $3, $4, $5)`,
			nid.PG(), sc.Event.PG(), sc.Key, sc.User.PG(), note); err != nil {
			return err
		}
		// IDs only: the note's text stays out of the audit record and the
		// outbox.
		if err := sc.Audit(ctx, noteAdded, map[string]any{"note_id": nid.String()}); err != nil {
			return err
		}
		sc.Emit(noteAdded, map[string]any{"note_id": nid.String()})
		sc.Changed()
		return nil
	})
}

// Clear is the organizer's change.
func (n notes) Clear(ctx context.Context, ref component.Ref) error {
	return n.fw.Change(ctx, ref, n.kind, component.Manager(), func(sc *component.Scope) error {
		tx, _ := sc.Q().Tx()
		tag, err := tx.Exec(ctx, `DELETE FROM test_component_notes WHERE event_id = $1 AND component_key = $2`, sc.Event.PG(), sc.Key)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			sc.Changed()
		}
		return sc.Audit(ctx, notesClosed, nil)
	})
}

// Judge is a referee's change in a scope (a table, a match); it reports
// the parameters as they hold for the event.
func (n notes) Judge(ctx context.Context, ref component.Ref, scope id.ID) (format string, bestOf int64, err error) {
	err = n.fw.Change(ctx, ref, n.kind, component.Referee(scope), func(sc *component.Scope) error {
		format = sc.String("format")
		bestOf, _ = sc.Int("best_of")
		return nil
	})
	return format, bestOf, err
}

// ---------------------------------------------------------------------------
// Fixtures

// testType is a type with every shape the framework handles: a plain
// component, an optional one that is off by default with a chosen
// parameter, an optional one that is on by default with two chosen
// parameters, and a keyed one.
const (
	testType       = "test_tournament"
	testDefinition = `{"components":[
		{"kind":"draw"},
		{"kind":"teams","optional":{"default":false},"params":{"mode":{"organizer_choice":["random","manual"],"default":"random"},"team_size":2}},
		{"kind":"bracket","optional":{"default":true},"params":{"format":{"organizer_choice":["elimination","league","swiss"],"default":"swiss"},"best_of":{"organizer_choice":[1,3,5],"default":3}}},
		{"kind":"checklist","key":"gear","labels":{"tr":"Malzeme"},"params":{"label":"gear","assignment":true}}
	]}`
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
	events *event.Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	admin  *pgxpool.Pool
	clock  *clock
	// gear, bracket and teams are the test component bound to three
	// kinds of the test type.
	gear, bracket, teams notes
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
	e := &env{fw: fw, events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	// The catalog is read-only for the service: the test type is written
	// as the platform would (migration, later the panel).
	e.exec(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
		VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test turnuvası"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, testType, testDefinition)
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, testType)
	// One notes table serves the three kinds in these tests; a real
	// component's table has one trigger with its own kind. The trigger's
	// kind check is tested on its own (TestGuardTrigger).
	for _, stmt := range []string{
		`CREATE TABLE test_component_notes (
			id uuid PRIMARY KEY,
			event_id uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
			component_key text NOT NULL,
			user_id uuid NOT NULL,
			note text NOT NULL)`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON test_component_notes TO core_app`,
	} {
		_, err := d.Admin.Exec(ctx, stmt)
		must(t, err)
	}
	e.gear = notes{fw: fw, pool: d.App, kind: definition.CompChecklist}
	e.bracket = notes{fw: fw, pool: d.App, kind: definition.CompBracket}
	e.teams = notes{fw: fw, pool: d.App, kind: definition.CompTeams}
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Turnuva Tayfası", ClientRequestID: id.New().String()})
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

// newEvent creates an event of the test type that starts in a day and
// lasts three hours.
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, sels ...component.Selection) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: testType, Title: "Kış turnuvası", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(), Components: sels,
	})
	must(t, err)
	return v
}

func (e *env) going(t *testing.T, tf, ev id.ID, users ...id.ID) {
	t.Helper()
	for _, u := range users {
		_, err := e.events.SetRSVP(ctx, u, tf, ev, event.RSVPGoing)
		must(t, err)
	}
}

func ref(user, tf, ev id.ID, key string) component.Ref {
	return component.Ref{User: user, Tayfa: tf, Event: ev, Key: key}
}

func on(b bool) *bool { return &b }

// changes returns the payloads of the event's component_changed events, in
// order.
func (e *env) changes(t *testing.T, ev id.ID) []map[string]any {
	t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT payload FROM domain_events WHERE type = $1 AND subject_id = $2 ORDER BY id`, component.EventChanged, ev.PG())
	must(t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		must(t, rows.Scan(&raw))
		var m map[string]any
		must(t, json.Unmarshal(raw, &m))
		out = append(out, m)
	}
	must(t, rows.Err())
	return out
}

func stateOf(t *testing.T, st component.States, key string) component.State {
	t.Helper()
	for _, c := range st.Components {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("bileşen %q yok: %+v", key, st)
	return component.State{}
}

func choiceOf(t *testing.T, c component.State, param string) component.Choice {
	t.Helper()
	for _, ch := range c.Choices {
		if ch.Param == param {
			return ch
		}
	}
	t.Fatalf("%s bileşeninde %q seçimi yok: %+v", c.Key, param, c)
	return component.Choice{}
}

// ---------------------------------------------------------------------------
// Tests

// The framework end to end with the test component (issue #238): reading,
// a participant's change, the organizer's change, the live update event
// and what stays out of it.
func TestComponentEndToEnd(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	goer, bystander := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin)
	e.going(t, tf, ev.ID, goer)

	// The setup follows the definition: order, defaults, choices.
	st, err := e.fw.List(ctx, bystander, tf, ev.ID)
	must(t, err)
	keys := make([]string, 0, len(st.Components))
	for _, c := range st.Components {
		keys = append(keys, c.Key+":"+c.Kind)
	}
	if !slices.Equal(keys, []string{"draw:draw", "teams:teams", "bracket:bracket", "gear:checklist"}) || st.Locked || st.CallerCanManage {
		t.Fatalf("bileşenler: %v kilit=%v yönetir=%v", keys, st.Locked, st.CallerCanManage)
	}
	if d := stateOf(t, st, "draw"); d.Optional || !d.Enabled || len(d.Choices) != 0 {
		t.Fatalf("kura: %+v", d)
	}
	if tm := stateOf(t, st, "teams"); !tm.Optional || tm.Enabled || choiceOf(t, tm, "mode").Value != "random" ||
		!slices.Equal(choiceOf(t, tm, "mode").Options, []string{"random", "manual"}) || len(tm.Choices) != 1 {
		t.Fatalf("takımlar: %+v", tm)
	}
	br := stateOf(t, st, "bracket")
	if !br.Optional || !br.Enabled || len(br.Choices) != 2 || br.Choices[0].Param != "best_of" || br.Choices[0].Value != "3" ||
		!slices.Equal(br.Choices[0].Options, []string{"1", "3", "5"}) || choiceOf(t, br, "format").Value != "swiss" {
		t.Fatalf("eşleşme ağacı: %+v", br)
	}
	if mine, err := e.fw.List(ctx, admin, tf, ev.ID); err != nil || !mine.CallerCanManage {
		t.Fatalf("organizatör: %+v %v", mine, err)
	}
	// Nothing is stored for defaults.
	if n := e.count(t, `SELECT count(*) FROM event_components WHERE event_id = $1`, ev.ID.PG()); n != 0 {
		t.Fatalf("varsayılan için satır yazıldı: %d", n)
	}

	// A participant adds; every member reads.
	must(t, e.gear.Add(ctx, ref(goer, tf, ev.ID, "gear"), "Çadır bende"))
	got, err := e.gear.List(ctx, ref(bystander, tf, ev.ID, "gear"))
	must(t, err)
	if !slices.Equal(got, []string{"Çadır bende"}) {
		t.Fatalf("notlar: %v", got)
	}
	// The component's own rule: only those who are going.
	wantErr(t, "gitmeyen", e.gear.Add(ctx, ref(bystander, tf, ev.ID, "gear"), "x"), apperr.ErrEventNotGoing)

	// The live update: one event with the IDs and nothing else.
	ch := e.changes(t, ev.ID)
	if len(ch) != 1 || len(ch[0]) != 3 || ch[0]["tayfa_id"] != tf.String() || ch[0]["event_id"] != ev.ID.String() || ch[0]["component_key"] != "gear" {
		t.Fatalf("bileşen olayı: %v", ch)
	}
	// The component's own outbox event and audit record carry the
	// component key; the note's text is nowhere.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND payload ->> 'component_key' = 'gear' AND payload ->> 'event_id' = $2`, noteAdded, ev.ID.String()); n != 1 {
		t.Fatalf("bileşenin kendi olayı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND metadata ->> 'component_key' = 'gear' AND actor_id = $3`, noteAdded, ev.ID.PG(), goer.PG()); n != 1 {
		t.Fatalf("denetim kaydı = %d", n)
	}
	for _, q := range []string{
		`SELECT count(*) FROM domain_events WHERE payload::text LIKE '%Çadır%'`,
		`SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%Çadır%'`,
	} {
		if n := e.count(t, q); n != 0 {
			t.Fatalf("içerik sızdı: %s", q)
		}
	}
	// The framework's event is the transaction's last write: after the
	// component's own.
	var last string
	must(t, e.pool.QueryRow(ctx, `SELECT type FROM domain_events WHERE subject_id = $1 ORDER BY id DESC LIMIT 1`, ev.ID.PG()).Scan(&last))
	if last != component.EventChanged {
		t.Fatalf("son olay = %s", last)
	}

	// The organizer's change; a change nobody sees writes no live update.
	wantErr(t, "üye temizleyemez", e.gear.Clear(ctx, ref(goer, tf, ev.ID, "gear")), apperr.ErrPermissionDenied)
	must(t, e.gear.Clear(ctx, ref(admin, tf, ev.ID, "gear")))
	if got, err := e.gear.List(ctx, ref(goer, tf, ev.ID, "gear")); err != nil || len(got) != 0 {
		t.Fatalf("temizlik sonrası: %v %v", got, err)
	}
	must(t, e.gear.Clear(ctx, ref(admin, tf, ev.ID, "gear"))) // nothing left to clear
	if ch := e.changes(t, ev.ID); len(ch) != 2 {
		t.Fatalf("görünür değişiklik olmadan olay yazıldı: %d", len(ch))
	}

	// A failed change leaves nothing behind.
	before := e.count(t, `SELECT count(*) FROM domain_events`) + e.count(t, `SELECT count(*) FROM audit_logs`)
	wantErr(t, "gitmeyen", e.gear.Add(ctx, ref(bystander, tf, ev.ID, "gear"), "x"), apperr.ErrEventNotGoing)
	if after := e.count(t, `SELECT count(*) FROM domain_events`) + e.count(t, `SELECT count(*) FROM audit_logs`); after != before {
		t.Fatalf("reddedilen değişiklik kayıt bıraktı: %d → %d", before, after)
	}
}

// Who reaches a component: active members of the tayfa only, the right
// event of the right tayfa, a component of the event's type version and of
// the endpoint's kind.
func TestComponentResolution(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	other, otherAdmin := e.newTayfa(t)
	goer := e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin)
	foreign := e.newEvent(t, other, otherAdmin)
	e.going(t, tf, ev.ID, goer)
	stranger := id.New()

	for name, tc := range map[string]struct {
		n    notes
		ref  component.Ref
		want error
	}{
		"üye değil":                 {e.gear, ref(stranger, tf, ev.ID, "gear"), apperr.ErrTayfaNotFound},
		"başka tayfanın yöneticisi": {e.gear, ref(otherAdmin, tf, ev.ID, "gear"), apperr.ErrTayfaNotFound},
		"bilinmeyen tayfa":          {e.gear, ref(goer, id.New(), ev.ID, "gear"), apperr.ErrTayfaNotFound},
		"bilinmeyen etkinlik":       {e.gear, ref(goer, tf, id.New(), "gear"), apperr.ErrEventNotFound},
		// IDOR: an event of another tayfa under the caller's own tayfa.
		"başka tayfanın etkinliği": {e.gear, ref(goer, tf, foreign.ID, "gear"), apperr.ErrEventNotFound},
		"türde olmayan bileşen":    {e.gear, ref(goer, tf, ev.ID, "tasks"), apperr.ErrComponentNotFound},
		// The key exists, but belongs to another kind's endpoint.
		"başka türün bileşeni": {e.gear, ref(goer, tf, ev.ID, "draw"), apperr.ErrComponentNotFound},
		"kind anahtar değil":   {e.gear, ref(goer, tf, ev.ID, "checklist"), apperr.ErrComponentNotFound},
		"kapalı bileşen":       {e.teams, ref(goer, tf, ev.ID, "teams"), apperr.ErrComponentDisabled},
	} {
		_, err := tc.n.List(ctx, tc.ref)
		wantErr(t, name+" (okuma)", err, tc.want)
		wantErr(t, name+" (yazma)", tc.n.Add(ctx, tc.ref, "x"), tc.want)
	}
	for _, key := range []string{"", "Gear", "g", "gear'; DROP TABLE events; --", "gear "} {
		_, err := e.gear.List(ctx, ref(goer, tf, ev.ID, key))
		wantField(t, "bozuk anahtar okuma "+key, err, "component_key")
		wantField(t, "bozuk anahtar yazma "+key, e.gear.Add(ctx, ref(goer, tf, ev.ID, key), "x"), "component_key")
	}
	// A type without components has none to reach.
	plain, err := e.events.Create(ctx, admin, tf, event.Input{
		Type: event.TypeGeneral, Title: "Buluşma", StartsAt: e.clock.now().Add(time.Hour), EndsAt: e.clock.now().Add(2 * time.Hour),
		Location: event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"}, ClientRequestID: id.New().String(),
	})
	must(t, err)
	_, err = e.gear.List(ctx, ref(admin, tf, plain.ID, "gear"))
	wantErr(t, "bileşensiz tür", err, apperr.ErrComponentNotFound)
	if st, err := e.fw.List(ctx, admin, tf, plain.ID); err != nil || len(st.Components) != 0 || st.Components == nil {
		t.Fatalf("bileşensiz türün listesi: %+v %v", st, err)
	}
	if n := e.count(t, `SELECT count(*) FROM test_component_notes`); n != 0 {
		t.Fatalf("reddedilen istek yazdı: %d", n)
	}

	// The events module off: nothing is reachable, reads included.
	must(t, e.tayfas.SetModule(ctx, admin, tf, access.ModuleEvents, false))
	_, err = e.gear.List(ctx, ref(goer, tf, ev.ID, "gear"))
	wantErr(t, "modül kapalı okuma", err, apperr.ErrModuleDisabled)
	wantErr(t, "modül kapalı yazma", e.gear.Add(ctx, ref(goer, tf, ev.ID, "gear"), "x"), apperr.ErrModuleDisabled)
	_, err = e.fw.List(ctx, goer, tf, ev.ID)
	wantErr(t, "modül kapalı liste", err, apperr.ErrModuleDisabled)
	wantErr(t, "modül kapalı erişim", e.fw.CheckAccess(ctx, goer, tf, ev.ID), apperr.ErrModuleDisabled)
}

// The permission table of the framework, for every role and operation
// (ADR-0025 §5). "organizatör" is a plain member who created the event.
func TestComponentPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	_, err := e.tayfas.Update(ctx, admin, tf, tayfa.UpdateInput{Fields: []string{tayfa.FieldMembersCanCreateEvents}, MembersCanCreateEvents: true})
	must(t, err)
	organizer := e.member(t, tf, authz.Member)
	moderator, member, referee := e.member(t, tf, authz.Moderator), e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	mutedMember, mutedModerator, mutedOrganizer := e.member(t, tf, authz.Member), e.member(t, tf, authz.Moderator), e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, organizer)
	mutedEv := e.newEvent(t, tf, mutedOrganizer)
	everyone := []id.ID{admin, moderator, organizer, member, referee, mutedMember, mutedModerator}
	e.going(t, tf, ev.ID, everyone...)
	e.going(t, tf, mutedEv.ID, mutedOrganizer)
	_, err = e.fw.AssignReferee(ctx, organizer, tf, ev.ID, component.RefereeInput{User: referee})
	must(t, err)
	// A muted referee is still assigned, but may not act.
	_, err = e.fw.AssignReferee(ctx, organizer, tf, ev.ID, component.RefereeInput{User: mutedMember})
	must(t, err)
	for _, u := range []id.ID{mutedMember, mutedModerator} {
		e.mute(t, tf, u)
	}
	e.mute(t, tf, mutedOrganizer)
	stranger := id.New()

	denied, muted, gone := apperr.ErrPermissionDenied, apperr.ErrMuted, apperr.ErrTayfaNotFound
	type row struct {
		who                                      string
		user                                     id.ID
		view, participate, manage, judge, assign error
	}
	for _, r := range []row{
		{"yönetici", admin, nil, nil, nil, nil, nil},
		{"moderatör", moderator, nil, nil, nil, nil, nil},
		{"organizatör (üye)", organizer, nil, nil, nil, nil, nil},
		{"üye", member, nil, nil, denied, denied, denied},
		{"hakem (üye)", referee, nil, nil, denied, nil, denied},
		{"susturulmuş üye (hakem)", mutedMember, nil, muted, denied, muted, denied},
		{"susturulmuş moderatör", mutedModerator, nil, muted, muted, muted, muted},
		{"üye değil", stranger, gone, gone, gone, gone, gone},
	} {
		_, err := e.gear.List(ctx, ref(r.user, tf, ev.ID, "gear"))
		wantErr(t, r.who+" / okuma", err, r.view)
		_, err = e.fw.List(ctx, r.user, tf, ev.ID)
		wantErr(t, r.who+" / bileşen listesi", err, r.view)
		_, err = e.fw.ListReferees(ctx, r.user, tf, ev.ID)
		wantErr(t, r.who+" / hakem listesi", err, r.view)
		wantErr(t, r.who+" / erişim", e.fw.CheckAccess(ctx, r.user, tf, ev.ID), r.view)
		wantErr(t, r.who+" / katılımcı işlemi", e.gear.Add(ctx, ref(r.user, tf, ev.ID, "gear"), "not"), r.participate)
		wantErr(t, r.who+" / organizatör işlemi", e.gear.Clear(ctx, ref(r.user, tf, ev.ID, "gear")), r.manage)
		_, _, err = e.bracket.Judge(ctx, ref(r.user, tf, ev.ID, "bracket"), id.ID{})
		wantErr(t, r.who+" / hakem işlemi", err, r.judge)
		_, err = e.fw.Set(ctx, r.user, tf, ev.ID, component.Selection{Key: "teams", Enabled: on(true)})
		wantErr(t, r.who+" / seçim", err, r.manage)
		assigned, err := e.fw.AssignReferee(ctx, r.user, tf, ev.ID, component.RefereeInput{User: member, ComponentKey: "draw"})
		wantErr(t, r.who+" / hakem atama", err, r.assign)
		err = e.fw.RemoveReferee(ctx, r.user, tf, ev.ID, assigned.ID)
		wantErr(t, r.who+" / hakem kaldırma", err, r.assign)
	}
	// A muted organizer reads their own event but manages nothing.
	_, err = e.fw.List(ctx, mutedOrganizer, tf, mutedEv.ID)
	must(t, err)
	wantErr(t, "susturulmuş organizatör / organizatör işlemi", e.gear.Clear(ctx, ref(mutedOrganizer, tf, mutedEv.ID, "gear")), muted)
	_, err = e.fw.Set(ctx, mutedOrganizer, tf, mutedEv.ID, component.Selection{Key: "teams", Enabled: on(true)})
	wantErr(t, "susturulmuş organizatör / seçim", err, muted)
	_, err = e.fw.AssignReferee(ctx, mutedOrganizer, tf, mutedEv.ID, component.RefereeInput{User: member})
	wantErr(t, "susturulmuş organizatör / hakem atama", err, muted)
	if st, err := e.fw.List(ctx, mutedOrganizer, tf, mutedEv.ID); err != nil || st.CallerCanManage {
		t.Fatalf("susturulmuş organizatör yönetebilir görünüyor: %+v %v", st, err)
	}
	// Being the organizer of one event gives nothing on another.
	wantErr(t, "başka etkinliğin organizatörü", e.gear.Clear(ctx, ref(organizer, tf, mutedEv.ID, "gear")), denied)
	// The zero Who allows nobody, not even an admin.
	wantErr(t, "tanımsız rol", e.fw.Change(ctx, ref(admin, tf, ev.ID, "gear"), definition.CompChecklist, component.Who{}, func(*component.Scope) error { return nil }), denied)
	// A component's own action from the permission table.
	must(t, e.fw.Change(ctx, ref(member, tf, ev.ID, "gear"), definition.CompChecklist, component.Allowed(authz.EventRSVP), func(*component.Scope) error { return nil }))
	wantErr(t, "bileşenin kendi işlemi", e.fw.Change(ctx, ref(member, tf, ev.ID, "gear"), definition.CompChecklist, component.Allowed(authz.EventEditAny),
		func(*component.Scope) error { return nil }), denied)
}

// The organizer's selections (ADR-0037 §2): optional components and chosen
// parameters, checked against the definition, locked at the start.
func TestSelections(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	goer := e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin)
	e.going(t, tf, ev.ID, goer, admin)

	// teams is off by default: its endpoints answer component-disabled.
	_, err := e.teams.List(ctx, ref(goer, tf, ev.ID, "teams"))
	wantErr(t, "varsayılan kapalı", err, apperr.ErrComponentDisabled)
	st, err := e.fw.Set(ctx, admin, tf, ev.ID, component.Selection{Key: "teams", Enabled: on(true), Choices: map[string]string{"mode": "manual"}})
	must(t, err)
	if tm := stateOf(t, st, "teams"); !tm.Enabled || choiceOf(t, tm, "mode").Value != "manual" {
		t.Fatalf("açılan takımlar: %+v", tm)
	}
	must(t, e.teams.Add(ctx, ref(goer, tf, ev.ID, "teams"), "A takımı"))

	// bracket is on by default; the parameters hold as chosen.
	format, bestOf, err := e.bracket.Judge(ctx, ref(admin, tf, ev.ID, "bracket"), id.ID{})
	if err != nil || format != "swiss" || bestOf != 3 {
		t.Fatalf("varsayılan parametreler: %q %d %v", format, bestOf, err)
	}
	_, err = e.fw.Set(ctx, admin, tf, ev.ID, component.Selection{Key: "bracket", Choices: map[string]string{"best_of": "5"}})
	must(t, err)
	st, err = e.fw.Set(ctx, admin, tf, ev.ID, component.Selection{Key: "bracket", Choices: map[string]string{"format": "league"}})
	must(t, err)
	// A choice left out keeps its value; the switch too.
	if br := stateOf(t, st, "bracket"); !br.Enabled || choiceOf(t, br, "best_of").Value != "5" || choiceOf(t, br, "format").Value != "league" {
		t.Fatalf("seçimler: %+v", br)
	}
	format, bestOf, err = e.bracket.Judge(ctx, ref(admin, tf, ev.ID, "bracket"), id.ID{})
	if err != nil || format != "league" || bestOf != 5 {
		t.Fatalf("seçilen parametreler: %q %d %v", format, bestOf, err)
	}
	// Stored with their JSON types, the chosen parameters only.
	var params string
	must(t, e.pool.QueryRow(ctx, `SELECT params::text FROM event_components WHERE event_id = $1 AND component_key = 'bracket'`, ev.ID.PG()).Scan(&params))
	if params != `{"format": "league", "best_of": 5}` && params != `{"best_of": 5, "format": "league"}` {
		t.Fatalf("saklanan seçim: %s", params)
	}

	// Switching off: reads and writes are refused, the data stays, and the
	// database refuses a row for the component too.
	must(t, e.bracket.Add(ctx, ref(goer, tf, ev.ID, "bracket"), "1. tur"))
	st, err = e.fw.Set(ctx, admin, tf, ev.ID, component.Selection{Key: "bracket", Enabled: on(false)})
	must(t, err)
	if br := stateOf(t, st, "bracket"); br.Enabled || choiceOf(t, br, "format").Value != "league" {
		t.Fatalf("kapatılan bileşen: %+v", br)
	}
	_, err = e.bracket.List(ctx, ref(goer, tf, ev.ID, "bracket"))
	wantErr(t, "kapalı okuma", err, apperr.ErrComponentDisabled)
	wantErr(t, "kapalı yazma", e.bracket.Add(ctx, ref(goer, tf, ev.ID, "bracket"), "x"), apperr.ErrComponentDisabled)
	_, _, err = e.bracket.Judge(ctx, ref(admin, tf, ev.ID, "bracket"), id.ID{})
	wantErr(t, "kapalı organizatör", err, apperr.ErrComponentDisabled)
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: goer, ComponentKey: "bracket"})
	wantErr(t, "kapalı bileşene hakem", err, apperr.ErrComponentDisabled)
	if n := e.count(t, `SELECT count(*) FROM test_component_notes WHERE component_key = 'bracket'`); n != 1 {
		t.Fatalf("kapatınca veri silindi: %d", n)
	}
	_, err = e.fw.Set(ctx, admin, tf, ev.ID, component.Selection{Key: "bracket", Enabled: on(true)})
	must(t, err)
	if got, err := e.bracket.List(ctx, ref(goer, tf, ev.ID, "bracket")); err != nil || len(got) != 1 {
		t.Fatalf("yeniden açılan bileşen: %v %v", got, err)
	}

	// What the definition does not offer is refused, and nothing changes.
	for name, tc := range map[string]struct {
		sel   component.Selection
		field string
	}{
		"isteğe bağlı olmayanı kapatma": {component.Selection{Key: "draw", Enabled: on(false)}, "enabled"},
		"seçeneklerde olmayan değer":    {component.Selection{Key: "bracket", Choices: map[string]string{"format": "double_elimination"}}, "choices"},
		"sayı yerine metin":             {component.Selection{Key: "bracket", Choices: map[string]string{"best_of": "three"}}, "choices"},
		"listede olmayan sayı":          {component.Selection{Key: "bracket", Choices: map[string]string{"best_of": "7"}}, "choices"},
		"seçilemeyen parametre":         {component.Selection{Key: "teams", Choices: map[string]string{"team_size": "5"}}, "choices"},
		"bilinmeyen parametre":          {component.Selection{Key: "teams", Choices: map[string]string{"stake": "100"}}, "choices"},
		"parametresiz bileşende seçim":  {component.Selection{Key: "draw", Choices: map[string]string{"mode": "random"}}, "choices"},
		"bozuk anahtar":                 {component.Selection{Key: "Draw", Enabled: on(true)}, "component_key"},
	} {
		_, err := e.fw.Set(ctx, admin, tf, ev.ID, tc.sel)
		wantField(t, name, err, tc.field)
	}
	_, err = e.fw.Set(ctx, admin, tf, ev.ID, component.Selection{Key: "tasks", Enabled: on(true)})
	wantErr(t, "türde olmayan bileşen", err, apperr.ErrComponentNotFound)
	// A component that is not optional accepts "on" and stays on.
	st, err = e.fw.Set(ctx, admin, tf, ev.ID, component.Selection{Key: "draw", Enabled: on(true)})
	if err != nil || !stateOf(t, st, "draw").Enabled {
		t.Fatalf("isteğe bağlı olmayanı açma: %+v %v", st, err)
	}
	final, err := e.fw.List(ctx, goer, tf, ev.ID)
	must(t, err)
	if br := stateOf(t, final, "bracket"); !br.Enabled || choiceOf(t, br, "format").Value != "league" || choiceOf(t, br, "best_of").Value != "5" {
		t.Fatalf("reddedilen seçim iz bıraktı: %+v", br)
	}

	// Every accepted change is audited (which parts, never more) and
	// announced with the component key.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, component.AuditConfigured, ev.ID.PG()); n != 6 {
		t.Fatalf("seçim denetim kaydı = %d", n)
	}
	var meta string
	must(t, e.pool.QueryRow(ctx, `SELECT metadata::text FROM audit_logs WHERE action = $1 AND target_id = $2 ORDER BY id LIMIT 1`, component.AuditConfigured, ev.ID.PG()).Scan(&meta))
	var m struct {
		Key    string   `json:"component_key"`
		Fields []string `json:"fields"`
	}
	must(t, json.Unmarshal([]byte(meta), &m))
	if m.Key != "teams" || !slices.Equal(m.Fields, []string{"enabled", "choices"}) {
		t.Fatalf("denetim kaydı: %s", meta)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND metadata::text LIKE '%manual%'`, component.AuditConfigured); n != 0 {
		t.Fatal("seçilen değer denetim kaydına yazıldı")
	}
}

// Selections are locked once the event starts, and with a cancelled event
// or a read-only tayfa.
func TestSelectionLock(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin)
	sel := component.Selection{Key: "teams", Enabled: on(true)}

	e.clock.set(ev.StartsAt.Add(-time.Microsecond))
	st, err := e.fw.Set(ctx, admin, tf, ev.ID, sel)
	if err != nil || st.Locked {
		t.Fatalf("başlamadan hemen önce: %+v %v", st, err)
	}
	e.clock.set(ev.StartsAt)
	_, err = e.fw.Set(ctx, admin, tf, ev.ID, component.Selection{Key: "teams", Enabled: on(false)})
	wantErr(t, "başlayınca", err, apperr.ErrComponentLocked)
	st, err = e.fw.List(ctx, admin, tf, ev.ID)
	if err != nil || !st.Locked || !st.CallerCanManage || !stateOf(t, st, "teams").Enabled {
		t.Fatalf("başlamış etkinlik: %+v %v", st, err)
	}
	// The component itself keeps working after the start.
	e.exec(t, `INSERT INTO event_rsvps (event_id, user_id, status) VALUES ($1, $2, 'going')`, ev.ID.PG(), admin.PG())
	must(t, e.teams.Add(ctx, ref(admin, tf, ev.ID, "teams"), "A"))

	// A cancelled event.
	e.clock.set(ev.StartsAt.Add(-48 * time.Hour))
	cancelled := e.newEvent(t, tf, admin)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.fw.Set(ctx, admin, tf, cancelled.ID, sel)
	wantErr(t, "iptal edilmiş etkinlikte seçim", err, apperr.ErrEventCancelled)
	wantErr(t, "iptal edilmiş etkinlikte yazma", e.gear.Clear(ctx, ref(admin, tf, cancelled.ID, "gear")), apperr.ErrEventCancelled)
	_, err = e.fw.AssignReferee(ctx, admin, tf, cancelled.ID, component.RefereeInput{User: admin})
	wantErr(t, "iptal edilmiş etkinlikte hakem", err, apperr.ErrEventCancelled)
	if st, err := e.fw.List(ctx, admin, tf, cancelled.ID); err != nil || !st.Locked {
		t.Fatalf("iptal edilmiş etkinlik: %+v %v", st, err)
	}
	// Reading a cancelled event's component still works.
	if _, err := e.gear.List(ctx, ref(admin, tf, cancelled.ID, "gear")); err != nil {
		t.Fatalf("iptal edilmiş etkinlikte okuma: %v", err)
	}

	// A read-only tayfa.
	open := e.newEvent(t, tf, admin)
	e.exec(t, `UPDATE tayfas SET suspended_at = now(), suspended_reason = 'test', suspended_by = $2 WHERE id = $1`, tf.PG(), id.New().PG())
	_, err = e.fw.Set(ctx, admin, tf, open.ID, sel)
	wantErr(t, "salt okunur tayfada seçim", err, apperr.ErrTayfaReadOnly)
	wantErr(t, "salt okunur tayfada yazma", e.gear.Clear(ctx, ref(admin, tf, open.ID, "gear")), apperr.ErrTayfaReadOnly)
	_, err = e.fw.AssignReferee(ctx, admin, tf, open.ID, component.RefereeInput{User: admin})
	wantErr(t, "salt okunur tayfada hakem", err, apperr.ErrTayfaReadOnly)
	if st, err := e.fw.List(ctx, admin, tf, open.ID); err != nil || !st.Locked {
		t.Fatalf("salt okunur tayfa: %+v %v", st, err)
	}
}

// Component data freezes seven days after the event's end (ADR-0025 §5).
func TestWriteWindow(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	goer := e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin)
	e.going(t, tf, ev.ID, goer)

	// During and after the event, until the window closes.
	for _, at := range []time.Time{ev.StartsAt.Add(time.Hour), ev.EndsAt.Add(time.Hour), ev.EndsAt.Add(component.FreezeAfter - time.Microsecond)} {
		e.clock.set(at)
		must(t, e.gear.Add(ctx, ref(goer, tf, ev.ID, "gear"), "not"))
	}
	_, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: goer})
	must(t, err)

	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	wantErr(t, "donmuş katılımcı", e.gear.Add(ctx, ref(goer, tf, ev.ID, "gear"), "geç"), apperr.ErrComponentFrozen)
	wantErr(t, "donmuş organizatör", e.gear.Clear(ctx, ref(admin, tf, ev.ID, "gear")), apperr.ErrComponentFrozen)
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: admin})
	wantErr(t, "donmuş hakem atama", err, apperr.ErrComponentFrozen)
	refs, err := e.fw.ListReferees(ctx, admin, tf, ev.ID)
	must(t, err)
	wantErr(t, "donmuş hakem kaldırma", e.fw.RemoveReferee(ctx, admin, tf, ev.ID, refs[0].ID), apperr.ErrComponentFrozen)
	// The frozen data stays readable: it is the source of the statistics.
	got, err := e.gear.List(ctx, ref(goer, tf, ev.ID, "gear"))
	if err != nil || len(got) != 3 {
		t.Fatalf("donmuş veri: %v %v", got, err)
	}
	// Whoever may not make the change learns nothing about the window.
	wantErr(t, "donmuş, yetkisiz", e.gear.Clear(ctx, ref(goer, tf, ev.ID, "gear")), apperr.ErrPermissionDenied)

	// The phase is the component's to judge.
	phases := func(at time.Time) (started, ended, frozen bool) {
		e.clock.set(at)
		_ = e.fw.View(ctx, ref(goer, tf, ev.ID, "gear"), definition.CompChecklist, func(sc *component.Scope) error {
			started, ended, frozen = sc.Started(), sc.Ended(), sc.Frozen()
			return nil
		})
		return
	}
	for name, tc := range map[string]struct {
		at   time.Time
		want [3]bool
	}{
		"önce":       {ev.StartsAt.Add(-time.Microsecond), [3]bool{false, false, false}},
		"başlangıç":  {ev.StartsAt, [3]bool{true, false, false}},
		"bitiş":      {ev.EndsAt, [3]bool{true, true, false}},
		"donma anı":  {ev.EndsAt.Add(component.FreezeAfter), [3]bool{true, true, true}},
		"donmadan 1": {ev.EndsAt.Add(component.FreezeAfter - time.Microsecond), [3]bool{true, true, false}},
	} {
		s, en, f := phases(tc.at)
		if got := [3]bool{s, en, f}; got != tc.want {
			t.Errorf("%s: %v, %v bekleniyordu", name, got, tc.want)
		}
	}
}

// Referees (ADR-0025 §5): the organizer assigns active members to the
// event, a component or a scope of it; an assignment allows exactly that.
func TestReferees(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	wide, comp, scoped, plain := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member), e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	ev := e.newEvent(t, tf, admin)
	table, otherTable := id.New(), id.New()

	// The list is in assignment order: each assignment a second later.
	tick := func() { e.clock.set(e.clock.now().Add(time.Second)) }
	rw, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: wide})
	must(t, err)
	tick()
	rc, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: comp, ComponentKey: "bracket"})
	must(t, err)
	tick()
	rs, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: scoped, ComponentKey: "bracket", Scope: table})
	must(t, err)
	if rs.User != scoped || rs.ComponentKey != "bracket" || rs.Scope != table || rs.ID == (id.ID{}) || !rs.CreatedAt.Equal(e.clock.now()) {
		t.Fatalf("atama: %+v", rs)
	}
	// The same assignment again is the first one; another scope is new.
	again, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: scoped, ComponentKey: "bracket", Scope: table})
	if err != nil || again.ID != rs.ID {
		t.Fatalf("tekrar atama: %+v %v", again, err)
	}
	againWide, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: wide})
	if err != nil || againWide.ID != rw.ID {
		t.Fatalf("tekrar etkinlik ataması: %+v %v", againWide, err)
	}
	list, err := e.fw.ListReferees(ctx, plain, tf, ev.ID)
	must(t, err)
	if len(list) != 3 || list[0].ID != rw.ID || list[0].ComponentKey != "" || list[0].Scope != (id.ID{}) || list[1].ID != rc.ID ||
		list[2].ID != rs.ID || list[2].User != scoped || list[2].ComponentKey != "bracket" || list[2].Scope != table || !list[2].CreatedAt.Equal(rs.CreatedAt) {
		t.Fatalf("hakemler: %+v", list)
	}

	judge := func(n notes, user id.ID, key string, scope id.ID) error {
		_, _, err := n.Judge(ctx, ref(user, tf, ev.ID, key), scope)
		return err
	}
	denied := apperr.ErrPermissionDenied
	for name, tc := range map[string]struct {
		n     notes
		user  id.ID
		key   string
		scope id.ID
		want  error
	}{
		"etkinlik hakemi, bileşen":            {e.bracket, wide, "bracket", id.ID{}, nil},
		"etkinlik hakemi, kapsam":             {e.bracket, wide, "bracket", table, nil},
		"etkinlik hakemi, başka bileşen":      {e.gear, wide, "gear", id.ID{}, nil},
		"bileşen hakemi, bileşen":             {e.bracket, comp, "bracket", id.ID{}, nil},
		"bileşen hakemi, kapsam":              {e.bracket, comp, "bracket", otherTable, nil},
		"bileşen hakemi, başka bileşen":       {e.gear, comp, "gear", id.ID{}, denied},
		"kapsam hakemi, kendi kapsamı":        {e.bracket, scoped, "bracket", table, nil},
		"kapsam hakemi, başka kapsam":         {e.bracket, scoped, "bracket", otherTable, denied},
		"kapsam hakemi, bileşenin tamamı":     {e.bracket, scoped, "bracket", id.ID{}, denied},
		"kapsam hakemi, başka bileşen":        {e.gear, scoped, "gear", table, denied},
		"hakem olmayan":                       {e.bracket, plain, "bracket", table, denied},
		"organizatör hakem atanmadan da":      {e.bracket, admin, "bracket", otherTable, nil},
		"etkinlik hakemi kapalı bileşende de": {e.teams, wide, "teams", id.ID{}, apperr.ErrComponentDisabled},
	} {
		wantErr(t, name, judge(tc.n, tc.user, tc.key, tc.scope), tc.want)
	}
	// A referee is not a manager, and an assignment holds for its event.
	wantErr(t, "hakem organizatör işlemi yapamaz", e.gear.Clear(ctx, ref(wide, tf, ev.ID, "gear")), denied)
	_, err = e.fw.Set(ctx, wide, tf, ev.ID, component.Selection{Key: "teams", Enabled: on(true)})
	wantErr(t, "hakem seçim yapamaz", err, denied)
	second := e.newEvent(t, tf, admin)
	_, _, err = e.bracket.Judge(ctx, ref(wide, tf, second.ID, "bracket"), id.ID{})
	wantErr(t, "başka etkinlikte hakem", err, denied)

	// Refusals.
	outsider := id.New()
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: outsider})
	wantErr(t, "üye olmayan hakem", err, apperr.ErrMemberNotFound)
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: plain, ComponentKey: "tasks"})
	wantErr(t, "olmayan bileşen", err, apperr.ErrComponentNotFound)
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: plain, ComponentKey: "teams"})
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: plain, Scope: table})
	wantField(t, "bileşensiz kapsam", err, "scope_id")
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{})
	wantField(t, "kullanıcısız", err, "user_id")
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: plain, ComponentKey: "Bracket"})
	wantField(t, "bozuk bileşen anahtarı", err, "component_key")
	_, err = e.fw.AssignReferee(ctx, admin, tf, id.New(), component.RefereeInput{User: plain})
	wantErr(t, "bilinmeyen etkinlik", err, apperr.ErrEventNotFound)

	// Removing: the scope's referee loses the scope; removing again and
	// removing another event's assignment change nothing.
	must(t, e.fw.RemoveReferee(ctx, admin, tf, ev.ID, rs.ID))
	wantErr(t, "kaldırılan hakem", judge(e.bracket, scoped, "bracket", table), denied)
	must(t, e.fw.RemoveReferee(ctx, admin, tf, ev.ID, rs.ID))
	must(t, e.fw.RemoveReferee(ctx, admin, tf, second.ID, rw.ID))
	must(t, judge(e.bracket, wide, "bracket", id.ID{}))
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, component.AuditRefereeRemoved, ev.ID.PG()); n != 1 {
		t.Fatalf("kaldırma denetim kaydı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, component.AuditRefereeAssigned, ev.ID.PG()); n != 3 {
		t.Fatalf("atama denetim kaydı = %d", n)
	}
	// Referee changes are announced: with the component key on assignment,
	// without one on removal.
	ch := e.changes(t, ev.ID)
	keys := make([]any, 0, len(ch))
	for _, c := range ch {
		keys = append(keys, c["component_key"])
	}
	if !slices.Equal(keys, []any{"", "bracket", "bracket", ""}) {
		t.Fatalf("hakem olayları: %v", keys)
	}

	// A component that deletes a scope removes the scope's referees.
	tick()
	other, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: scoped, ComponentKey: "bracket", Scope: otherTable})
	must(t, err)
	tick()
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: plain, ComponentKey: "bracket", Scope: table})
	must(t, err)
	must(t, e.fw.Change(ctx, ref(admin, tf, ev.ID, "bracket"), definition.CompBracket, component.Manager(), func(sc *component.Scope) error {
		return sc.RemoveScopeReferees(ctx, table)
	}))
	list, err = e.fw.ListReferees(ctx, admin, tf, ev.ID)
	must(t, err)
	if len(list) != 3 || list[2].ID != other.ID {
		t.Fatalf("kapsam silinince hakemler: %+v", list)
	}

	// A member who leaves the tayfa is no referee any more, whatever the
	// row says.
	e.exec(t, `DELETE FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), wide.PG())
	wantErr(t, "ayrılan hakem", judge(e.bracket, wide, "bracket", id.ID{}), apperr.ErrTayfaNotFound)
}

// An event has at most MaxReferees assignments.
func TestRefereeLimit(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin)
	target := e.member(t, tf, authz.Member)
	for range component.MaxReferees {
		_, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: target, ComponentKey: "bracket", Scope: id.New()})
		must(t, err)
	}
	_, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: target})
	wantErr(t, "sınır", err, apperr.ErrEventLimit)
	// The limit does not refuse an assignment that already exists.
	list, err := e.fw.ListReferees(ctx, admin, tf, ev.ID)
	must(t, err)
	if _, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: target, ComponentKey: "bracket", Scope: list[0].Scope}); err != nil {
		t.Fatalf("var olan atama: %v", err)
	}
}

// A new event takes the organizer's selections with it, checked against
// the type version; a series' occurrences get the template's.
func TestSelectionsOnCreate(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin,
		component.Selection{Key: "teams", Enabled: on(true), Choices: map[string]string{"mode": "manual"}},
		component.Selection{Key: "bracket", Enabled: on(false), Choices: map[string]string{"best_of": "1"}},
	)
	st, err := e.fw.List(ctx, admin, tf, ev.ID)
	must(t, err)
	if tm, br := stateOf(t, st, "teams"), stateOf(t, st, "bracket"); !tm.Enabled || choiceOf(t, tm, "mode").Value != "manual" ||
		br.Enabled || choiceOf(t, br, "best_of").Value != "1" || choiceOf(t, br, "format").Value != "swiss" {
		t.Fatalf("oluşturmadaki seçimler: %+v", st)
	}
	// Creating announces the event, not a component change.
	if ch := e.changes(t, ev.ID); len(ch) != 0 {
		t.Fatalf("oluşturmada bileşen olayı: %v", ch)
	}

	start := e.clock.now().Add(24 * time.Hour)
	create := func(sels ...component.Selection) error {
		_, err := e.events.Create(ctx, admin, tf, event.Input{
			Type: testType, Title: "Turnuva", StartsAt: start, EndsAt: start.Add(time.Hour),
			Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
			ClientRequestID: id.New().String(), Components: sels,
		})
		return err
	}
	before := e.count(t, `SELECT count(*) FROM events WHERE tayfa_id = $1`, tf.PG())
	many := make([]component.Selection, definition.MaxComponents+1)
	for i := range many {
		many[i] = component.Selection{Key: "draw"}
	}
	for name, sels := range map[string][]component.Selection{
		"türde olmayan bileşen":       {{Key: "tasks", Enabled: on(true)}},
		"tekrar eden bileşen":         {{Key: "teams", Enabled: on(true)}, {Key: "teams", Enabled: on(false)}},
		"isteğe bağlı olmayanı kapat": {{Key: "draw", Enabled: on(false)}},
		"geçersiz seçim":              {{Key: "bracket", Choices: map[string]string{"format": "ladder"}}},
		"seçilemeyen parametre":       {{Key: "teams", Choices: map[string]string{"team_size": "9"}}},
		"çok fazla":                   many,
	} {
		wantField(t, name, create(sels...), "components")
	}
	if after := e.count(t, `SELECT count(*) FROM events WHERE tayfa_id = $1`, tf.PG()); after != before {
		t.Fatalf("reddedilen seçimle etkinlik açıldı: %d → %d", before, after)
	}
	// A type without components takes no selections.
	_, err = e.events.Create(ctx, admin, tf, event.Input{
		Type: event.TypeGeneral, Title: "Buluşma", StartsAt: start, EndsAt: start.Add(time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(), Components: []component.Selection{{Key: "draw", Enabled: on(true)}},
	})
	wantField(t, "bileşensiz türde seçim", err, "components")

	// A weekly series: every occurrence starts with the template's
	// selections.
	first, err := e.events.Create(ctx, admin, tf, event.Input{
		Type: testType, Title: "Haftalık turnuva", StartsAt: start, EndsAt: start.Add(time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(), Recurrence: &event.Recurrence{RRule: "FREQ=WEEKLY;COUNT=3"},
		Components: []component.Selection{{Key: "teams", Enabled: on(true)}, {Key: "bracket", Choices: map[string]string{"format": "elimination"}}},
	})
	must(t, err)
	if first.SeriesID == (id.ID{}) {
		t.Fatal("seri açılmadı")
	}
	rows, err := e.pool.Query(ctx, `SELECT id FROM events WHERE series_id = $1 ORDER BY starts_at`, first.SeriesID.PG())
	must(t, err)
	var occurrences []id.ID
	for rows.Next() {
		var u pgtype.UUID
		must(t, rows.Scan(&u))
		occurrences = append(occurrences, id.ID(u.Bytes))
	}
	rows.Close()
	must(t, rows.Err())
	if len(occurrences) != 3 {
		t.Fatalf("seri örnekleri = %d", len(occurrences))
	}
	for _, o := range occurrences {
		st, err := e.fw.List(ctx, admin, tf, o)
		must(t, err)
		if !stateOf(t, st, "teams").Enabled || choiceOf(t, stateOf(t, st, "bracket"), "format").Value != "elimination" {
			t.Fatalf("seri örneğinin seçimleri (%s): %+v", o, st)
		}
	}
	// A later change applies to the one event.
	_, err = e.fw.Set(ctx, admin, tf, occurrences[1], component.Selection{Key: "teams", Enabled: on(false)})
	must(t, err)
	for i, want := range []bool{true, false, true} {
		st, err := e.fw.List(ctx, admin, tf, occurrences[i])
		must(t, err)
		if stateOf(t, st, "teams").Enabled != want {
			t.Fatalf("seri örneği %d: takımlar %v", i, !want)
		}
	}
}

// What chat asks before a connection may watch an event (ADR-0025 §6).
func TestCheckAccess(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	other, otherAdmin := e.newTayfa(t)
	member, muted := e.member(t, tf, authz.Member), e.member(t, tf, authz.Member)
	e.mute(t, tf, muted)
	ev := e.newEvent(t, tf, admin)
	foreign := e.newEvent(t, other, otherAdmin)

	for _, u := range []id.ID{admin, member, muted} {
		must(t, e.fw.CheckAccess(ctx, u, tf, ev.ID))
	}
	wantErr(t, "üye değil", e.fw.CheckAccess(ctx, otherAdmin, tf, ev.ID), apperr.ErrTayfaNotFound)
	wantErr(t, "bilinmeyen etkinlik", e.fw.CheckAccess(ctx, member, tf, id.New()), apperr.ErrEventNotFound)
	wantErr(t, "başka tayfanın etkinliği", e.fw.CheckAccess(ctx, member, tf, foreign.ID), apperr.ErrEventNotFound)
	// Pending and former members are no members.
	waiting := id.New()
	e.exec(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via) VALUES ($1, $2, 'member', 'pending', 'code')`, tf.PG(), waiting.PG())
	wantErr(t, "onay bekleyen", e.fw.CheckAccess(ctx, waiting, tf, ev.ID), apperr.ErrTayfaNotFound)
	e.exec(t, `DELETE FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), member.PG())
	wantErr(t, "ayrılan üye", e.fw.CheckAccess(ctx, member, tf, ev.ID), apperr.ErrTayfaNotFound)
	// A cancelled event can still be watched (and read).
	_, err := e.events.Cancel(ctx, admin, tf, ev.ID, "")
	must(t, err)
	must(t, e.fw.CheckAccess(ctx, admin, tf, ev.ID))
}

// The database's second line (ADR-0025 §4): a component table takes no row
// for a component the event's type version does not have, of another
// kind, or switched off; selections and referees are checked against the
// definition too. core_app cannot get around it.
func TestGuardTrigger(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin)
	_, err := e.admin.Exec(ctx, `CREATE TRIGGER test_component_notes_component_guard
		BEFORE INSERT OR UPDATE OF event_id, component_key ON test_component_notes
		FOR EACH ROW EXECUTE FUNCTION event_component_guard('checklist')`)
	must(t, err)
	insert := func(event id.ID, key string) error {
		_, err := e.pool.Exec(ctx, `INSERT INTO test_component_notes (id, event_id, component_key, user_id, note) VALUES ($1, $2, $3, $4, 'x')`,
			id.New().PG(), event.PG(), key, admin.PG())
		return err
	}
	guarded := func(what string, err error, constraint string) {
		t.Helper()
		if store.SQLState(err) != store.CodeCheckViolation || store.Constraint(err) != constraint {
			t.Errorf("%s: %v, %s bekleniyordu", what, err, constraint)
		}
	}
	must(t, insert(ev.ID, "gear"))
	guarded("türde olmayan bileşen", insert(ev.ID, "tasks"), "event_component_guard")
	guarded("başka türün bileşeni", insert(ev.ID, "draw"), "event_component_guard")
	guarded("kind anahtar değil", insert(ev.ID, "checklist"), "event_component_guard")
	// Moving a row to another component is checked like a new row.
	_, err = e.pool.Exec(ctx, `UPDATE test_component_notes SET component_key = 'draw' WHERE event_id = $1`, ev.ID.PG())
	guarded("satırı taşıma", err, "event_component_guard")
	// Other columns stay writable (account deletion clears user columns).
	_, err = e.pool.Exec(ctx, `UPDATE test_component_notes SET note = 'y' WHERE event_id = $1`, ev.ID.PG())
	must(t, err)

	// An event of a type without the component.
	plain, err := e.events.Create(ctx, admin, tf, event.Input{
		Type: event.TypeGeneral, Title: "Buluşma", StartsAt: e.clock.now().Add(time.Hour), EndsAt: e.clock.now().Add(2 * time.Hour),
		Location: event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"}, ClientRequestID: id.New().String(),
	})
	must(t, err)
	guarded("bileşensiz tür", insert(plain.ID, "gear"), "event_component_guard")

	// Switched off: default off without a row, and off by selection.
	_, err = e.admin.Exec(ctx, `DROP TRIGGER test_component_notes_component_guard ON test_component_notes`)
	must(t, err)
	_, err = e.admin.Exec(ctx, `CREATE TRIGGER test_component_notes_component_guard
		BEFORE INSERT OR UPDATE OF event_id, component_key ON test_component_notes
		FOR EACH ROW EXECUTE FUNCTION event_component_guard('teams')`)
	must(t, err)
	guarded("varsayılan kapalı", insert(ev.ID, "teams"), "event_component_guard")
	_, err = e.fw.Set(ctx, admin, tf, ev.ID, component.Selection{Key: "teams", Enabled: on(true)})
	must(t, err)
	must(t, insert(ev.ID, "teams"))
	_, err = e.fw.Set(ctx, admin, tf, ev.ID, component.Selection{Key: "teams", Enabled: on(false)})
	must(t, err)
	guarded("seçimle kapalı", insert(ev.ID, "teams"), "event_component_guard")

	// Selections written around the app layer.
	selection := func(key string, enabled bool, params string) error {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_components (event_id, component_key, enabled, params) VALUES ($1, $2, $3, $4::jsonb)
			ON CONFLICT (event_id, component_key) DO UPDATE SET enabled = EXCLUDED.enabled, params = EXCLUDED.params`, ev.ID.PG(), key, enabled, params)
		return err
	}
	must(t, selection("bracket", true, `{"format": "league", "best_of": 5}`))
	guarded("türde olmayan bileşenin seçimi", selection("tasks", true, `{}`), "event_components_guard")
	guarded("isteğe bağlı olmayanı kapatma", selection("draw", false, `{}`), "event_components_guard")
	guarded("seçeneklerde olmayan değer", selection("bracket", true, `{"format": "ladder"}`), "event_components_guard")
	guarded("yanlış türde değer", selection("bracket", true, `{"best_of": "5"}`), "event_components_guard")
	guarded("seçilemeyen parametre", selection("teams", true, `{"team_size": 4}`), "event_components_guard")
	guarded("bilinmeyen parametre", selection("draw", true, `{"stake": 100}`), "event_components_guard")
	if err := selection("bracket", true, `[]`); store.Constraint(err) != "event_components_params" {
		t.Errorf("nesne olmayan seçim: %v", err)
	}
	// A selection cannot move to another event or component, and is not
	// deleted by the service.
	dbtest.WantDenied(t, e.pool, `UPDATE event_components SET component_key = 'teams'`)
	dbtest.WantDenied(t, e.pool, `UPDATE event_components SET event_id = event_id`)
	dbtest.WantDenied(t, e.pool, `DELETE FROM event_components`)
	dbtest.WantDenied(t, e.pool, `ALTER TABLE event_components DISABLE TRIGGER event_components_guard`)

	// Referees: the component must be the event's; a scope needs one; an
	// assignment is never rewritten.
	referee := func(key *string, scope *id.ID) error {
		var k pgtype.Text
		if key != nil {
			k = pgtype.Text{String: *key, Valid: true}
		}
		var s pgtype.UUID
		if scope != nil {
			s = scope.PG()
		}
		_, err := e.pool.Exec(ctx, `INSERT INTO event_referees (id, event_id, user_id, component_key, scope_id) VALUES ($1, $2, $3, $4, $5)`,
			id.New().PG(), ev.ID.PG(), admin.PG(), k, s)
		return err
	}
	bracket, tasks, scope := "bracket", "tasks", id.New()
	must(t, referee(nil, nil))
	must(t, referee(&bracket, &scope))
	guarded("türde olmayan bileşene hakem", referee(&tasks, nil), "event_referees_guard")
	if err := referee(nil, &scope); store.Constraint(err) != "event_referees_scope" {
		t.Errorf("bileşensiz kapsam: %v", err)
	}
	if err := referee(nil, nil); store.SQLState(err) != store.CodeUniqueViolation {
		t.Errorf("aynı atama iki kez: %v", err)
	}
	if err := referee(&bracket, &scope); store.SQLState(err) != store.CodeUniqueViolation {
		t.Errorf("aynı kapsam ataması iki kez: %v", err)
	}
	dbtest.WantDenied(t, e.pool, `UPDATE event_referees SET user_id = user_id`)

	// The event goes, and its selections and referees with it.
	// (Not e.exec: with triggers off the cascade would not run.)
	_, err = e.admin.Exec(ctx, `DELETE FROM events WHERE id = $1`, ev.ID.PG())
	must(t, err)
	if n := e.count(t, `SELECT (SELECT count(*) FROM event_components) + (SELECT count(*) FROM event_referees)`); n != 0 {
		t.Fatalf("etkinlikle silinmeyen satır: %d", n)
	}
}
