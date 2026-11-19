package rollcall_test

import (
	"context"
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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/rollcall"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. walkType has a keyed roll call (the key is not the kind)
// with its own grace period next to another component; tripType has an
// optional roll call without the alarm that is off by default.
const (
	walkType = "test_walk"
	walkV1   = `{"components":[{"kind":"roll_call","key":"party","params":{"alarm":true,"return_grace_minutes":45}},{"kind":"draw"}]}`
	tripType = "test_trip"
	tripV1   = `{"components":[{"kind":"roll_call","optional":{"default":false},"params":{"alarm":false}}]}`
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
	rc     *rollcall.Service
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
	e := &env{rc: rollcall.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	e.addType(t, walkType, walkV1)
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

// addType writes a type as the platform would.
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Yürüyüş Tayfası", ClientRequestID: id.New().String()})
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

// going adds a member who answered going.
func (e *env) going(t *testing.T, tf, ev id.ID, role authz.Role) id.ID {
	t.Helper()
	u := e.member(t, tf, role)
	_, err := e.events.SetRSVP(ctx, u, tf, ev, event.RSVPGoing)
	must(t, err)
	return u
}

// newEvent creates an event of typ that starts in a day and lasts three
// hours.
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string, sels ...component.Selection) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Zirve yürüyüşü", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(), Components: sels,
	})
	must(t, err)
	return v
}

func ref(user, tf, ev id.ID, key string) component.Ref {
	return component.Ref{User: user, Tayfa: tf, Event: ev, Key: key}
}

// changes counts the event's component_changed events for key.
func (e *env) changes(t *testing.T, ev id.ID, key string) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND subject_id = $2 AND payload ->> 'component_key' = $3`,
		component.EventChanged, ev.PG(), key)
}

func (e *env) attendance(t *testing.T, ev, u id.ID) (status, method string) {
	t.Helper()
	var m *string
	must(t, e.pool.QueryRow(ctx, `SELECT attendance, attendance_method FROM event_rsvps WHERE event_id = $1 AND user_id = $2`, ev.PG(), u.PG()).Scan(&status, &m))
	if m != nil {
		method = *m
	}
	return status, method
}

func (e *env) activityMinutes(t *testing.T, u id.ID) int {
	t.Helper()
	return e.count(t, `SELECT duration_minutes FROM user_activity_records WHERE user_id = $1`, u.PG())
}

// The roll call end to end on the hike type: the plan, setting out (it
// counts as attendance), coming back (the real duration), repeats and the
// client time rules (Faz 1 hike roll call, issue #242).
func TestRollCallEndToEnd(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeHike)
	u := e.going(t, tf, ev.ID, authz.Member)
	// loc checks in online before setting out (it answers going before the
	// start, as an online check-in needs).
	loc := e.going(t, tf, ev.ID, authz.Member)
	mine, plan := ref(u, tf, ev.ID, "roll_call"), ref(admin, tf, ev.ID, "roll_call")

	// Nothing yet: the definition's parameters, no plan, no record.
	got, err := e.rc.Get(ctx, mine)
	must(t, err)
	if !got.Alarm || got.ReturnGraceMinutes != 30 || !got.ExpectedReturnAt.IsZero() || got.Mine != nil || got.Entries != nil ||
		got.CallerCanManage || got.CallerCanEdit {
		t.Fatalf("boş yoklama: %+v", got)
	}

	// The plan: after the start, managers only.
	_, err = e.rc.SetExpectedReturn(ctx, plan, ev.StartsAt)
	wantField(t, "başlangıçta dönüş", err, "expected_return_time")
	_, err = e.rc.SetExpectedReturn(ctx, mine, ev.StartsAt.Add(time.Hour))
	wantErr(t, "üyenin planı", err, apperr.ErrPermissionDenied)
	back := ev.StartsAt.Add(5 * time.Hour)
	got, err = e.rc.SetExpectedReturn(ctx, plan, back)
	must(t, err)
	if !got.ExpectedReturnAt.Equal(back) || !got.CallerCanManage || !got.CallerCanEdit || got.Entries == nil {
		t.Fatalf("plan: %+v", got)
	}
	// The same plan again writes nothing.
	_, err = e.rc.SetExpectedReturn(ctx, plan, back)
	must(t, err)
	if n := e.changes(t, ev.ID, "roll_call"); n != 1 {
		t.Fatalf("plan olayları = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, rollcall.AuditPlanned, ev.ID.PG()); n != 1 {
		t.Fatalf("plan denetim kaydı = %d", n)
	}

	// Setting out: only in the check-in window, only going.
	_, err = e.rc.Start(ctx, mine, time.Time{})
	wantErr(t, "erken çıkış", err, apperr.ErrCheckInClosed)
	e.clock.set(ev.StartsAt.Add(-10 * time.Minute))
	now := e.clock.now()
	_, err = e.rc.Return(ctx, mine, time.Time{})
	wantErr(t, "çıkmadan dönüş", err, apperr.ErrHikeNotStarted)
	_, err = e.rc.Start(ctx, mine, now.Add(3*time.Minute))
	wantField(t, "gelecekte çıkış", err, "start_time")
	_, err = e.rc.Start(ctx, mine, now.Add(-49*time.Hour))
	wantField(t, "48 saatten eski çıkış", err, "start_time")
	_, err = e.rc.Start(ctx, ref(e.member(t, tf, authz.Member), tf, ev.ID, "roll_call"), time.Time{})
	wantErr(t, "gelmeyen üye", err, apperr.ErrEventNotGoing)

	start := now.Add(-time.Minute)
	got, err = e.rc.Start(ctx, mine, start)
	must(t, err)
	if got.Mine == nil || got.Mine.User != u || !got.Mine.StartedAt.Equal(start) || !got.Mine.ReturnedAt.IsZero() || got.Entries != nil {
		t.Fatalf("çıkış: %+v", got)
	}
	if st, m := e.attendance(t, ev.ID, u); st != event.AttendanceAttended || m != event.MethodHikeStart {
		t.Fatalf("çıkış katılım sayılmadı: %s %s", st, m)
	}
	var started time.Time
	must(t, e.pool.QueryRow(ctx, `SELECT started_at FROM user_activity_records WHERE user_id = $1`, u.PG()).Scan(&started))
	if !started.Equal(start) {
		t.Errorf("geçmiş başlangıcı = %v", started)
	}
	// A repeat returns the first start.
	again, err := e.rc.Start(ctx, mine, time.Time{})
	must(t, err)
	if !again.Mine.StartedAt.Equal(start) {
		t.Fatalf("tekrar çıkış: %+v", again.Mine)
	}
	// Managers see everyone's records.
	all, err := e.rc.Get(ctx, plan)
	must(t, err)
	if all.Mine != nil || len(all.Entries) != 1 || all.Entries[0].User != u || !all.Entries[0].StartedAt.Equal(start) {
		t.Fatalf("yöneticinin gördüğü: %+v", all)
	}

	// Back 150 minutes later: the real duration replaces the planned one.
	e.clock.set(start.Add(150 * time.Minute))
	_, err = e.rc.Return(ctx, mine, start.Add(-time.Second))
	wantField(t, "çıkıştan önce dönüş", err, "return_time")
	got, err = e.rc.Return(ctx, mine, time.Time{})
	must(t, err)
	if !got.Mine.ReturnedAt.Equal(e.clock.now()) {
		t.Fatalf("dönüş: %+v", got.Mine)
	}
	if n := e.activityMinutes(t, u); n != 150 {
		t.Errorf("süre = %d", n)
	}
	again, err = e.rc.Return(ctx, mine, time.Time{})
	must(t, err)
	if !again.Mine.ReturnedAt.Equal(got.Mine.ReturnedAt) {
		t.Error("tekrar dönüş değişti")
	}

	// Someone who checked in already keeps the method; the record starts
	// with the roll call.
	e.clock.set(ev.StartsAt)
	_, err = e.events.CheckIn(ctx, loc, tf, ev.ID, nil)
	must(t, err)
	_, err = e.rc.Start(ctx, ref(loc, tf, ev.ID, "roll_call"), time.Time{})
	must(t, err)
	if _, m := e.attendance(t, ev.ID, loc); m != event.MethodOnline {
		t.Errorf("yöntem = %s", m)
	}

	// Outbox events and audit records carry IDs only.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action IN ($1, $2) AND target_id = $3 AND metadata ->> 'user_id' IS NOT NULL
		AND metadata ->> 'component_key' = 'roll_call'`, rollcall.AuditStarted, rollcall.AuditReturned, ev.ID.PG()); n != 3 {
		t.Fatalf("çıkış ve dönüş denetim kayıtları = %d", n)
	}
	for _, q := range []string{
		`SELECT count(*) FROM domain_events WHERE payload::text LIKE '%started_at%' OR payload::text LIKE '%returned_at%' OR payload::text LIKE '%expected_return%'`,
		`SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%started_at%' OR metadata::text LIKE '%returned_at%' OR metadata::text LIKE '%expected_return%'`,
	} {
		if n := e.count(t, q); n != 0 {
			t.Fatalf("yoklama zamanı sızdı: %s", q)
		}
	}
}

// The return is a safety signal: it works for a muted member, after the
// end, after the component froze and on a cancelled event; only a
// read-only tayfa refuses it. Setting out keeps the usual window.
func TestRollCallReturnIsSafetySignal(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeHike)
	late, muted, notOut := e.going(t, tf, ev.ID, authz.Member), e.going(t, tf, ev.ID, authz.Member), e.going(t, tf, ev.ID, authz.Member)
	e.mute(t, tf, muted)
	e.clock.set(ev.StartsAt)
	for _, u := range []id.ID{late, muted} {
		_, err := e.rc.Start(ctx, ref(u, tf, ev.ID, "roll_call"), time.Time{})
		must(t, err)
	}
	_, err := e.rc.Return(ctx, ref(muted, tf, ev.ID, "roll_call"), time.Time{})
	must(t, err)

	_, err = e.events.Cancel(ctx, admin, tf, ev.ID, "")
	must(t, err)
	_, err = e.rc.Start(ctx, ref(notOut, tf, ev.ID, "roll_call"), time.Time{})
	wantErr(t, "iptal sonrası çıkış", err, apperr.ErrEventCancelled)
	_, err = e.rc.SetExpectedReturn(ctx, ref(admin, tf, ev.ID, "roll_call"), ev.EndsAt)
	wantErr(t, "iptal sonrası plan", err, apperr.ErrEventCancelled)

	// Frozen and cancelled: the return still gets through; the activity
	// record is frozen.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter + time.Hour))
	got, err := e.rc.Return(ctx, ref(late, tf, ev.ID, "roll_call"), time.Time{})
	must(t, err)
	if got.Mine == nil || got.Mine.ReturnedAt.IsZero() {
		t.Fatalf("geç dönüş: %+v", got)
	}
	if n := e.activityMinutes(t, late); n != 180 {
		t.Errorf("donmuş kayıt değişti: %d", n)
	}
	if n := e.changes(t, ev.ID, "roll_call"); n != 4 {
		t.Errorf("canlı güncelleme olayları = %d", n)
	}

	// A read-only tayfa refuses even the return.
	ev2 := e.newEvent(t, tf, admin, event.TypeHike)
	u := e.going(t, tf, ev2.ID, authz.Member)
	e.clock.set(ev2.StartsAt)
	_, err = e.rc.Start(ctx, ref(u, tf, ev2.ID, "roll_call"), time.Time{})
	must(t, err)
	e.exec(t, `UPDATE tayfas SET suspended_at = now(), suspended_reason = 'test', suspended_by = $2 WHERE id = $1`, tf.PG(), id.New().PG())
	_, err = e.rc.Return(ctx, ref(u, tf, ev2.ID, "roll_call"), time.Time{})
	wantErr(t, "salt okunur tayfada dönüş", err, apperr.ErrTayfaReadOnly)
	if _, err := e.rc.Get(ctx, ref(u, tf, ev2.ID, "roll_call")); err != nil {
		t.Fatalf("salt okunur tayfada okuma: %v", err)
	}
}

// Who may do what, for every role (ADR-0025 §5); IDOR.
func TestRollCallPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeHike)
	moderator, member := e.member(t, tf, authz.Moderator), e.member(t, tf, authz.Member)
	mutedModerator := e.member(t, tf, authz.Moderator)
	e.mute(t, tf, mutedModerator)
	back := ev.StartsAt.Add(4 * time.Hour)
	for _, tc := range []struct {
		name   string
		user   id.ID
		want   error
		manage bool
	}{
		{"yönetici", admin, nil, true},
		{"moderatör", moderator, nil, true},
		{"üye", member, apperr.ErrPermissionDenied, false},
		{"susturulmuş moderatör", mutedModerator, apperr.ErrMuted, false},
		{"üye değil", id.New(), apperr.ErrTayfaNotFound, false},
	} {
		_, err := e.rc.SetExpectedReturn(ctx, ref(tc.user, tf, ev.ID, "roll_call"), back)
		if tc.want == nil {
			must(t, err)
		} else {
			wantErr(t, tc.name+" plan", err, tc.want)
		}
		got, err := e.rc.Get(ctx, ref(tc.user, tf, ev.ID, "roll_call"))
		if tc.want == apperr.ErrTayfaNotFound {
			wantErr(t, tc.name+" okuma", err, tc.want)
			continue
		}
		must(t, err)
		if got.CallerCanManage != tc.manage || (got.Entries != nil) != tc.manage {
			t.Errorf("%s: yönetim %v, kayıtlar %v", tc.name, got.CallerCanManage, got.Entries)
		}
	}

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err := e.rc.Get(ctx, ref(admin2, tf2, ev.ID, "roll_call"))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.rc.Return(ctx, ref(admin2, tf2, ev.ID, "roll_call"), time.Time{})
	wantErr(t, "başka tayfa dönüş", err, apperr.ErrEventNotFound)
}

// The component exists only where the event's type version has it, under
// its key; an optional one is off until the organizer switches it on. The
// parameters come from the definition.
func TestRollCallOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral)
	walk := e.newEvent(t, tf, admin, walkType)
	trip := e.newEvent(t, tf, admin, tripType)
	tripOn := e.newEvent(t, tf, admin, tripType, component.Selection{Key: "roll_call", Enabled: on(true)})

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":      ref(admin, tf, general.ID, "roll_call"),
		"anahtar yerine kind": ref(admin, tf, walk.ID, "roll_call"),
		"başka bileşen":       ref(admin, tf, walk.ID, "draw"),
	} {
		_, err := e.rc.Get(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.rc.Return(ctx, r, time.Time{})
		wantErr(t, name+" dönüş", err, apperr.ErrComponentNotFound)
	}
	_, err := e.rc.Get(ctx, ref(admin, tf, trip.ID, "roll_call"))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.rc.SetExpectedReturn(ctx, ref(admin, tf, trip.ID, "roll_call"), trip.StartsAt.Add(time.Hour))
	wantErr(t, "kapalı bileşene plan", err, apperr.ErrComponentDisabled)

	got, err := e.rc.Get(ctx, ref(admin, tf, walk.ID, "party"))
	must(t, err)
	if !got.Alarm || got.ReturnGraceMinutes != 45 {
		t.Fatalf("anahtarlı yoklama: %+v", got)
	}
	got, err = e.rc.Get(ctx, ref(admin, tf, tripOn.ID, "roll_call"))
	must(t, err)
	if got.Alarm || got.ReturnGraceMinutes != rollcall.DefaultGraceMinutes {
		t.Fatalf("alarmsız yoklama: %+v", got)
	}

	// The trigger refuses writes that go around the service.
	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "kapalı bileşen": trip.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_roll_call_checkins (event_id, component_key, user_id, started_at, started_received_at)
			VALUES ($1, 'roll_call', gen_random_uuid(), now(), now())`, ev.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
}

func on(b bool) *bool { return &b }

func constraintOf(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.ConstraintName
	}
	return ""
}
