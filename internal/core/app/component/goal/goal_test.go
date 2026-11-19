package goal_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/goal"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/measure"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. pickupType has a total measure suggested from a field and a
// goal with a default target (Notion Faz 3: çöp toplama, hedef weight);
// readingType a personal measure participants enter and a goal without a
// default; examType a sensitive personal measure each participant enters
// and a goal on it; scoreType a sensitive personal measure the organizer
// enters; optType an optional measure that is off by default, a goal on
// it and an optional goal that is off, and a second version without them.
const (
	pickupType = "test_pickup"
	pickupV1   = `{"fields":[{"key":"planned_weight","kind":"decimal","min":0.1,"max":5000,"decimals":1,"unit":"kg","labels":{"tr":"Planlanan"}}],
		"components":[{"kind":"measure","key":"weight","labels":{"tr":"Ağırlık"},"params":{"unit":"kg","scope":"total","entered_by":"organizer","min":0,"max":5000,"decimals":1,"showcase":true,"prefill_from":"planned_weight"}},
		{"kind":"goal","params":{"measure":"weight","default_target":100}}]}`
	readingType = "test_reading"
	readingV1   = `{"components":[{"kind":"measure","key":"pages","labels":{"tr":"Sayfa"},"params":{"unit":"page","scope":"personal","entered_by":"self","max":5000}},
		{"kind":"goal","params":{"measure":"pages"}}]}`
	examType = "test_exam"
	examV1   = `{"components":[{"kind":"measure","key":"net","labels":{"tr":"Net"},"params":{"unit":"net","scope":"personal","entered_by":"self","min":-40,"max":120,"decimals":2,"sensitive":true}},
		{"kind":"goal","params":{"measure":"net","default_target":300}}]}`
	scoreType = "test_score"
	scoreV1   = `{"components":[{"kind":"measure","key":"score","labels":{"tr":"Puan"},"params":{"unit":"point","scope":"personal","entered_by":"organizer","max":100,"sensitive":true}},
		{"kind":"goal","params":{"measure":"score","default_target":10}}]}`
	optType = "test_opt"
	optV1   = `{"components":[{"kind":"measure","key":"bags","labels":{"tr":"Torba"},"optional":{"default":false},"params":{"unit":"bag","scope":"total","entered_by":"organizer"}},
		{"kind":"goal","params":{"measure":"bags","default_target":20}},
		{"kind":"goal","key":"bag_goal_off","optional":{"default":false},"params":{"measure":"bags"}}]}`
	optV2 = `{"components":[{"kind":"draw"}]}`
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
	goals    *goal.Service
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
	e := &env{goals: goal.New(fw), measures: measure.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	for key, def := range map[string]string{pickupType: pickupV1, readingType: readingV1, examType: examV1, scoreType: scoreV1, optType: optV1} {
		e.addType(t, key, def)
	}
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Hedef Tayfası", ClientRequestID: id.New().String()})
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

func is(p *float64, v float64) bool { return p != nil && *p == v }

// A goal on a total measure: the default target until the organizer sets
// one; managers set it from the event's creation; the progress is the
// stored value only, never the suggestion; the goal freezes with the
// event's components.
func TestGoalTargetAndProgress(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, pickupType, map[string]json.RawMessage{"planned_weight": json.RawMessage(`120`)})
	member := e.attended(t, tf, ev.ID, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	const key = "goal"

	got, err := e.goals.Get(ctx, ref(member, tf, ev.ID, key))
	must(t, err)
	if got.Measure != "weight" || got.Unit != "kg" || got.Decimals != 1 || !is(got.Target, 100) || got.TargetSet || !got.TargetUpdatedAt.IsZero() ||
		!is(got.DefaultTarget, 100) || !is(got.Current, 0) || got.Reached || got.ProgressHidden || !got.MeasureEnabled || got.CallerCanEdit {
		t.Fatalf("başlangıç: %+v", got)
	}
	// The target is part of the plan: managers set it before the start.
	if got, err := e.goals.Get(ctx, ref(admin, tf, ev.ID, key)); err != nil || !got.CallerCanEdit {
		t.Fatalf("yönetici başlamadan: %+v %v", got, err)
	}
	_, err = e.goals.SetTarget(ctx, ref(member, tf, ev.ID, key), 150)
	wantErr(t, "üye hedef koyamaz", err, apperr.ErrPermissionDenied)
	for name, v := range map[string]float64{"sıfır": 0, "eksi": -5, "yuvarlanınca sıfır": 0.04, "ölçümün üst sınırı üstü": 5000.1} {
		_, err = e.goals.SetTarget(ctx, ref(admin, tf, ev.ID, key), v)
		wantField(t, name, err, "target")
	}
	got, err = e.goals.SetTarget(ctx, ref(admin, tf, ev.ID, key), 150.06)
	must(t, err)
	if !is(got.Target, 150.1) || !got.TargetSet || got.TargetUpdatedAt.IsZero() || !is(got.DefaultTarget, 100) || !got.CallerCanEdit {
		t.Fatalf("konan hedef: %s %+v", value(got.Target), got)
	}
	if n := e.changes(t, ev.ID, key); n != 1 {
		t.Fatalf("canlı güncelleme = %d", n)
	}
	// The same target again changes nothing.
	_, err = e.goals.SetTarget(ctx, ref(moderator, tf, ev.ID, key), 150.1)
	must(t, err)
	if n := e.changes(t, ev.ID, key); n != 1 {
		t.Fatalf("aynı hedef canlı güncelleme yazdı: %d", n)
	}

	// Progress follows the measure's stored value.
	e.clock.set(ev.StartsAt.Add(time.Hour))
	_, err = e.measures.SetTotal(ctx, ref(admin, tf, ev.ID, "weight"), 90)
	must(t, err)
	if got, err := e.goals.Get(ctx, ref(member, tf, ev.ID, key)); err != nil || !is(got.Current, 90) || got.Reached {
		t.Fatalf("ilerleme: %+v %v", got, err)
	}

	// After the end the measure suggests the planned value while it has no
	// value; the suggestion is not progress.
	e.clock.set(ev.EndsAt.Add(time.Hour))
	m, err := e.measures.RemoveTotal(ctx, ref(admin, tf, ev.ID, "weight"))
	must(t, err)
	if !is(m.Suggested, 120) {
		t.Fatalf("ölçümün önerisi: %s", value(m.Suggested))
	}
	if got, err := e.goals.Get(ctx, ref(member, tf, ev.ID, key)); err != nil || !is(got.Current, 0) || got.Reached {
		t.Fatalf("öneri ilerlemeye sayıldı: %+v %v", got, err)
	}
	_, err = e.measures.SetTotal(ctx, ref(admin, tf, ev.ID, "weight"), 160)
	must(t, err)
	if got, err := e.goals.Get(ctx, ref(member, tf, ev.ID, key)); err != nil || !is(got.Current, 160) || !got.Reached {
		t.Fatalf("ulaşılan hedef: %+v %v", got, err)
	}

	// Removing the target brings the default back.
	got, err = e.goals.RemoveTarget(ctx, ref(admin, tf, ev.ID, key))
	must(t, err)
	if !is(got.Target, 100) || got.TargetSet || !got.TargetUpdatedAt.IsZero() || !got.Reached {
		t.Fatalf("kaldırılan hedef: %+v", got)
	}
	// Removing what is not there is not an error and changes nothing.
	_, err = e.goals.RemoveTarget(ctx, ref(admin, tf, ev.ID, key))
	must(t, err)
	if n := e.changes(t, ev.ID, key); n != 2 {
		t.Fatalf("canlı güncelleme = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_goals WHERE event_id = $1`, ev.ID.PG()); n != 0 {
		t.Fatalf("kaldırılan hedefin satırı: %d", n)
	}

	// Seven days after the end the goal freezes; reading still works.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.goals.SetTarget(ctx, ref(admin, tf, ev.ID, key), 200)
	wantErr(t, "donmuş hedef", err, apperr.ErrComponentFrozen)
	_, err = e.goals.RemoveTarget(ctx, ref(admin, tf, ev.ID, key))
	wantErr(t, "donmuş hedefi kaldırma", err, apperr.ErrComponentFrozen)
	if got, err := e.goals.Get(ctx, ref(admin, tf, ev.ID, key)); err != nil || got.CallerCanEdit || !is(got.Current, 160) {
		t.Fatalf("donmuş hedef okuma: %+v %v", got, err)
	}

	// The audit records name the component, never the target.
	for action, want := range map[string]int{goal.AuditSet: 1, goal.AuditRemoved: 1} {
		if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND metadata ->> 'component_key' = $3`,
			action, ev.ID.PG(), key); n != want {
			t.Errorf("%s denetim kaydı = %d", action, n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action LIKE 'event.goal_%'
		AND EXISTS (SELECT 1 FROM jsonb_object_keys(metadata) AS k WHERE k <> 'component_key')`, ev.ID.PG()); n != 0 {
		t.Fatal("hedef denetim kaydına yazıldı")
	}
}

// A goal on a personal measure sums the participants' stored values; a
// goal without a default has no target until the organizer sets one.
func TestGoalPersonalSum(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, readingType, nil)
	a := e.attended(t, tf, ev.ID, authz.Member)
	b := e.attended(t, tf, ev.ID, authz.Member)
	const key = "goal"

	got, err := e.goals.Get(ctx, ref(a, tf, ev.ID, key))
	must(t, err)
	if got.Target != nil || got.DefaultTarget != nil || got.TargetSet || !is(got.Current, 0) || got.Reached || got.Unit != "page" {
		t.Fatalf("başlangıç: %+v", got)
	}
	e.clock.set(ev.StartsAt.Add(time.Hour))
	_, err = e.measures.SetEntry(ctx, ref(a, tf, ev.ID, "pages"), a, 30)
	must(t, err)
	_, err = e.measures.SetEntry(ctx, ref(b, tf, ev.ID, "pages"), b, 45)
	must(t, err)
	if got, err := e.goals.Get(ctx, ref(a, tf, ev.ID, key)); err != nil || !is(got.Current, 75) || got.Reached || got.ProgressHidden {
		t.Fatalf("toplam: %+v %v", got, err)
	}
	// The measure's own maximum bounds a person's value, not the sum: the
	// target may be above it.
	got, err = e.goals.SetTarget(ctx, ref(admin, tf, ev.ID, key), 6000)
	must(t, err)
	if !is(got.Target, 6000) || got.Reached {
		t.Fatalf("üst sınırın üstünde kişisel hedef: %+v", got)
	}
	got, err = e.goals.SetTarget(ctx, ref(admin, tf, ev.ID, key), 70.4)
	must(t, err)
	if !is(got.Target, 70) || !got.Reached || !is(got.Current, 75) {
		t.Fatalf("ulaşılan kişisel hedef: %+v", got)
	}
}

// The goal shows no more than the measure: the sum of a sensitive measure
// would tell one person's value, so only those who see every value see
// the progress.
func TestGoalSensitiveMeasure(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	moderator := e.member(t, tf, authz.Moderator)

	// Each participant enters their own net; nobody sees everyone's.
	exam := e.newEvent(t, tf, admin, examType, nil)
	a := e.attended(t, tf, exam.ID, authz.Member)
	b := e.attended(t, tf, exam.ID, authz.Member)
	e.clock.set(exam.StartsAt.Add(time.Hour))
	_, err := e.measures.SetEntry(ctx, ref(a, tf, exam.ID, "net"), a, 80)
	must(t, err)
	_, err = e.measures.SetEntry(ctx, ref(b, tf, exam.ID, "net"), b, 90)
	must(t, err)
	for name, u := range map[string]id.ID{"katılımcı": a, "yönetici": admin, "moderatör": moderator} {
		got, err := e.goals.Get(ctx, ref(u, tf, exam.ID, "goal"))
		must(t, err)
		if !got.ProgressHidden || got.Current != nil || got.Reached || !is(got.Target, 300) || !got.MeasureEnabled {
			t.Errorf("%s hassas ilerlemeyi gördü: %+v", name, got)
		}
	}
	// The target is not sensitive: managers still set it.
	if got, err := e.goals.SetTarget(ctx, ref(admin, tf, exam.ID, "goal"), 150); err != nil || !is(got.Target, 150) || !got.ProgressHidden {
		t.Fatalf("hassas ölçümde hedef: %+v %v", got, err)
	}

	// The organizer enters the scores: managers see every value and the
	// progress; the player sees only their own value and no progress.
	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	match := e.newEvent(t, tf, admin, scoreType, nil)
	player := e.going(t, tf, match.ID, authz.Member)
	e.clock.set(match.StartsAt.Add(time.Hour))
	_, err = e.measures.SetEntry(ctx, ref(admin, tf, match.ID, "score"), player, 12)
	must(t, err)
	for name, u := range map[string]id.ID{"yönetici": admin, "moderatör": moderator} {
		if got, err := e.goals.Get(ctx, ref(u, tf, match.ID, "goal")); err != nil || got.ProgressHidden || !is(got.Current, 12) || !got.Reached {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	if got, err := e.goals.Get(ctx, ref(player, tf, match.ID, "goal")); err != nil || !got.ProgressHidden || got.Current != nil || got.Reached {
		t.Fatalf("oyuncu hassas ilerlemeyi gördü: %+v %v", got, err)
	}
}

// The component exists only where the event's type version has it, under
// its key; an optional one is off until the organizer switches it on, and
// a goal on a measure that is off has no progress. Writes outside the type
// version are refused by the service and the trigger.
func TestGoalOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral, nil)
	pickup := e.newEvent(t, tf, admin, pickupType, nil)
	off := e.newEvent(t, tf, admin, optType, nil)
	bags := e.newEvent(t, tf, admin, optType, nil, component.Selection{Key: "bags", Enabled: on(true)})

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":        ref(admin, tf, general.ID, "goal"),
		"başka bileşen":         ref(admin, tf, pickup.ID, "weight"),
		"tanımda olmayan hedef": ref(admin, tf, pickup.ID, "bag_goal_off"),
	} {
		_, err := e.goals.Get(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.goals.SetTarget(ctx, r, 1)
		wantErr(t, name+" yazma", err, apperr.ErrComponentNotFound)
		_, err = e.goals.RemoveTarget(ctx, r)
		wantErr(t, name+" kaldırma", err, apperr.ErrComponentNotFound)
	}
	_, err := e.goals.Get(ctx, ref(admin, tf, off.ID, "bag_goal_off"))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.goals.SetTarget(ctx, ref(admin, tf, off.ID, "bag_goal_off"), 3)
	wantErr(t, "kapalı bileşene yazma", err, apperr.ErrComponentDisabled)

	// The measure is off: the goal and its target, without progress.
	got, err := e.goals.SetTarget(ctx, ref(admin, tf, off.ID, "goal"), 25)
	must(t, err)
	if got.MeasureEnabled || got.Current != nil || got.Reached || got.ProgressHidden || !is(got.Target, 25) {
		t.Fatalf("kapalı ölçümün hedefi: %+v", got)
	}
	// Switched on, it counts; no bounds of its own: no decimals.
	e.clock.set(bags.StartsAt.Add(time.Hour))
	_, err = e.measures.SetTotal(ctx, ref(admin, tf, bags.ID, "bags"), 21)
	must(t, err)
	got, err = e.goals.SetTarget(ctx, ref(admin, tf, bags.ID, "goal"), 20.4)
	must(t, err)
	if !got.MeasureEnabled || !is(got.Current, 21) || !is(got.Target, 20) || !got.Reached || got.Decimals != 0 {
		t.Fatalf("açık ölçümün hedefi: %+v", got)
	}

	// The type's next version drops the goal: the old event keeps it, an
	// event of the new version has none.
	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, optType, optV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, optType, optV2)
	newer := e.newEvent(t, tf, admin, optType, nil)
	if newer.TypeVersion != 2 || bags.TypeVersion != 1 {
		t.Fatalf("tür sürümleri: eski %d, yeni %d", bags.TypeVersion, newer.TypeVersion)
	}
	_, err = e.goals.SetTarget(ctx, ref(admin, tf, newer.ID, "goal"), 1)
	wantErr(t, "yeni sürümde olmayan hedef", err, apperr.ErrComponentNotFound)
	if got, err := e.goals.SetTarget(ctx, ref(admin, tf, bags.ID, "goal"), 30); err != nil || !is(got.Target, 30) {
		t.Fatalf("eski sürümün etkinliği: %+v %v", got, err)
	}

	// The trigger refuses writes that go around the service.
	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "yeni sürüm": newer.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_goals (event_id, component_key, target) VALUES ($1, 'goal', 1)`, ev.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
	_, err = e.pool.Exec(ctx, `INSERT INTO event_goals (event_id, component_key, target) VALUES ($1, 'bag_goal_off', 1)`, off.ID.PG())
	if c := constraintOf(err); c != "event_component_guard" {
		t.Errorf("kapalı bileşen: tetikleyici %q (%v)", c, err)
	}
	// The database bounds the target too.
	_, err = e.pool.Exec(ctx, `INSERT INTO event_goals (event_id, component_key, target) VALUES ($1, 'goal', 0)`, pickup.ID.PG())
	if c := constraintOf(err); c != "event_goals_target" {
		t.Errorf("sıfır hedef: %q (%v)", c, err)
	}
}

// Who may do what for every role; IDOR; a cancelled event takes nothing.
func TestGoalPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, pickupType, nil)
	cancelled := e.newEvent(t, tf, admin, pickupType, nil)
	_, err := e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	const key = "goal"
	organizer := e.member(t, tf, authz.Member)
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	muted := e.member(t, tf, authz.Member)
	e.mute(t, tf, muted)
	// A member's own event (members may not create events by default: the
	// organizer is written directly).
	own := e.newEvent(t, tf, admin, pickupType, nil)
	e.exec(t, `UPDATE events SET created_by = $2 WHERE id = $1`, own.ID.PG(), organizer.PG())

	for _, tc := range []struct {
		name string
		user id.ID
		ev   id.ID
		want error
	}{
		{"yönetici", admin, ev.ID, nil},
		{"moderatör", moderator, ev.ID, nil},
		{"kendi etkinliğinin organizatörü", organizer, own.ID, nil},
		{"başkasının etkinliğinde organizatör", organizer, ev.ID, apperr.ErrPermissionDenied},
		{"üye", member, ev.ID, apperr.ErrPermissionDenied},
		{"susturulmuş üye", muted, ev.ID, apperr.ErrPermissionDenied},
		{"üye değil", id.New(), ev.ID, apperr.ErrTayfaNotFound},
	} {
		_, err := e.goals.SetTarget(ctx, ref(tc.user, tf, tc.ev, key), 50)
		if tc.want == nil {
			must(t, err)
		} else if ok := errors.Is(err, tc.want) || (tc.user == muted && errors.Is(err, apperr.ErrMuted)); !ok {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
		_, err = e.goals.RemoveTarget(ctx, ref(tc.user, tf, tc.ev, key))
		if tc.want == nil {
			must(t, err)
		} else if ok := errors.Is(err, tc.want) || (tc.user == muted && errors.Is(err, apperr.ErrMuted)); !ok {
			t.Errorf("%s kaldırma: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
		got, err := e.goals.Get(ctx, ref(tc.user, tf, tc.ev, key))
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			wantErr(t, tc.name+" okuma", err, tc.want)
			continue
		}
		// A mute does not hide the goal.
		must(t, err)
		if got.CallerCanEdit != (tc.want == nil) {
			t.Errorf("%s: caller_can_edit = %v", tc.name, got.CallerCanEdit)
		}
	}

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err = e.goals.Get(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.goals.SetTarget(ctx, ref(admin2, tf2, ev.ID, key), 1)
	wantErr(t, "başka tayfa yazma", err, apperr.ErrEventNotFound)
	_, err = e.goals.RemoveTarget(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa kaldırma", err, apperr.ErrEventNotFound)

	_, err = e.goals.SetTarget(ctx, ref(admin, tf, cancelled.ID, key), 1)
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
	if got, err := e.goals.Get(ctx, ref(admin, tf, cancelled.ID, key)); err != nil || got.CallerCanEdit {
		t.Fatalf("iptal edilmiş etkinlikte yazma izni: %+v %v", got, err)
	}
}
