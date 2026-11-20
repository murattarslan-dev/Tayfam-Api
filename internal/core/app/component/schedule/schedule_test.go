package schedule_test

import (
	"context"
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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/schedule"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. talkType has a programme of at most three items (Notion
// Faz 3: söyleşi, program); tourType one without max_items and an optional
// programme that is off by default, and a second version without them.
const (
	talkType = "test_talk"
	talkV1   = `{"components":[{"kind":"schedule","params":{"max_items":3}}]}`
	tourType = "test_tour"
	tourV1   = `{"components":[{"kind":"schedule","key":"plan"},
		{"kind":"schedule","key":"extra","optional":{"default":false}}]}`
	tourV2 = `{"components":[{"kind":"draw"}]}`
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
	schedules *schedule.Service
	events    *event.Service
	tayfas    *tayfa.Service
	pool      *pgxpool.Pool
	admin     *pgxpool.Pool
	clock     *clock
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
	e := &env{schedules: schedule.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	for key, def := range map[string]string{talkType: talkV1, tourType: tourV1} {
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Program Tayfası", ClientRequestID: id.New().String()})
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
		Type: typ, Title: "Pazar buluşması", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
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

func titles(s schedule.Schedule) string {
	var out []string
	for _, it := range s.Items {
		out = append(out, it.Title)
	}
	return strings.Join(out, ",")
}

// Items are added, ordered by their start, edited and removed; every
// change is a live update and an audit record without the item's text.
func TestScheduleItems(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, talkType)
	member := e.member(t, tf, authz.Member)
	const key = "schedule"
	r := ref(admin, tf, ev.ID, key)

	got, err := e.schedules.Get(ctx, ref(member, tf, ev.ID, key))
	must(t, err)
	if len(got.Items) != 0 || got.Items == nil || got.MaxItems != 3 || got.CallerCanEdit {
		t.Fatalf("boş program: %+v", got)
	}

	// Before the event starts: the programme is the plan.
	lunch := schedule.Input{StartsAt: ev.StartsAt.Add(2 * time.Hour), Title: " Öğle yemeği "}
	_, err = e.schedules.Add(ctx, r, lunch)
	must(t, err)
	talk := schedule.Input{StartsAt: ev.StartsAt.Add(30 * time.Minute), EndsAt: ev.StartsAt.Add(90 * time.Minute), Title: "Söyleşi", Note: "Salon B"}
	_, err = e.schedules.Add(ctx, r, talk)
	must(t, err)
	// The same start as the talk: after it, in the order added.
	e.clock.set(e.clock.now().Add(time.Second))
	got, err = e.schedules.Add(ctx, r, schedule.Input{StartsAt: talk.StartsAt, Title: "Soru-cevap"})
	must(t, err)
	if titles(got) != "Söyleşi,Soru-cevap,Öğle yemeği" || !got.CallerCanEdit {
		t.Fatalf("sıra: %s %+v", titles(got), got)
	}
	first := got.Items[0]
	if !first.StartsAt.Equal(talk.StartsAt) || !first.EndsAt.Equal(talk.EndsAt) || first.Note != "Salon B" ||
		!got.Items[2].EndsAt.IsZero() || got.Items[2].Note != "" || got.Items[2].Title != "Öğle yemeği" {
		t.Fatalf("maddeler: %+v", got.Items)
	}
	if n := e.changes(t, ev.ID, key); n != 3 {
		t.Fatalf("canlı güncelleme = %d", n)
	}

	// max_items: the fourth is refused.
	_, err = e.schedules.Add(ctx, r, schedule.Input{StartsAt: ev.StartsAt, Title: "Fazla"})
	wantErr(t, "dolu program", err, apperr.ErrEventLimit)

	// Edit moves the talk after lunch and drops its end and note.
	got, err = e.schedules.Update(ctx, r, first.ID, schedule.Input{StartsAt: ev.StartsAt.Add(150 * time.Minute), Title: "Söyleşi (ertelendi)"})
	must(t, err)
	if titles(got) != "Soru-cevap,Öğle yemeği,Söyleşi (ertelendi)" || !got.Items[2].EndsAt.IsZero() || got.Items[2].Note != "" || got.Items[2].ID != first.ID {
		t.Fatalf("düzenleme: %+v", got.Items)
	}
	_, err = e.schedules.Update(ctx, r, id.New(), lunch)
	wantErr(t, "bilinmeyen madde düzenleme", err, apperr.ErrEventItemNotFound)

	got, err = e.schedules.Remove(ctx, r, first.ID)
	must(t, err)
	if titles(got) != "Soru-cevap,Öğle yemeği" {
		t.Fatalf("silme: %s", titles(got))
	}
	_, err = e.schedules.Remove(ctx, r, first.ID)
	wantErr(t, "bilinmeyen madde silme", err, apperr.ErrEventItemNotFound)
	// Room again after a removal.
	_, err = e.schedules.Add(ctx, r, schedule.Input{StartsAt: ev.EndsAt, Title: "Kapanış"})
	must(t, err)

	if n := e.changes(t, ev.ID, key); n != 6 {
		t.Fatalf("canlı güncelleme = %d", n)
	}
	for _, action := range []string{schedule.AuditAdded, schedule.AuditUpdated, schedule.AuditRemoved} {
		if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND metadata ->> 'component_key' = $3`,
			action, ev.ID.PG(), key); n == 0 {
			t.Errorf("%s denetim kaydı yok", action)
		}
	}
	// User content never reaches the audit record or the outbox.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND metadata::text ~ '(Söyleşi|Salon|yemeği)'`, ev.ID.PG()); n != 0 {
		t.Fatalf("denetim kaydında içerik: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE subject_id = $1 AND payload::text ~ '(Söyleşi|Salon|yemeği)'`, ev.ID.PG()); n != 0 {
		t.Fatalf("olayda içerik: %d", n)
	}
}

// Bad values are refused with their field; times lie within a day of the
// event.
func TestScheduleValidation(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, tourType)
	r := ref(admin, tf, ev.ID, "plan")
	at := ev.StartsAt

	for name, tc := range map[string]struct {
		in    schedule.Input
		field string
	}{
		"başlıksız":            {schedule.Input{StartsAt: at, Title: "  "}, "title"},
		"uzun başlık":          {schedule.Input{StartsAt: at, Title: strings.Repeat("ç", schedule.TitleMax+1)}, "title"},
		"denetim karakteri":    {schedule.Input{StartsAt: at, Title: "Varış\x00"}, "title"},
		"bozuk UTF-8":          {schedule.Input{StartsAt: at, Title: "Var\xffış"}, "title"},
		"uzun not":             {schedule.Input{StartsAt: at, Title: "Varış", Note: strings.Repeat("a", schedule.NoteMax+1)}, "note"},
		"saatsiz":              {schedule.Input{Title: "Varış"}, "start_time"},
		"bitiş başlangıçta":    {schedule.Input{StartsAt: at, EndsAt: at, Title: "Varış"}, "end_time"},
		"bitiş önce":           {schedule.Input{StartsAt: at, EndsAt: at.Add(-time.Minute), Title: "Varış"}, "end_time"},
		"bir günden çok önce":  {schedule.Input{StartsAt: at.Add(-schedule.Margin - time.Minute), Title: "Varış"}, "start_time"},
		"bir günden çok sonra": {schedule.Input{StartsAt: ev.EndsAt.Add(schedule.Margin + time.Minute), Title: "Varış"}, "start_time"},
		"bitiş çok sonra":      {schedule.Input{StartsAt: ev.EndsAt, EndsAt: ev.EndsAt.Add(schedule.Margin + time.Minute), Title: "Varış"}, "end_time"},
	} {
		_, err := e.schedules.Add(ctx, r, tc.in)
		wantField(t, name, err, tc.field)
	}
	// The edges are allowed; the longest title and note too.
	_, err := e.schedules.Add(ctx, r, schedule.Input{StartsAt: at.Add(-schedule.Margin), Title: "Önceki akşam buluşma"})
	must(t, err)
	got, err := e.schedules.Add(ctx, r, schedule.Input{
		StartsAt: ev.EndsAt, EndsAt: ev.EndsAt.Add(schedule.Margin),
		Title: strings.Repeat("ç", schedule.TitleMax), Note: strings.Repeat("ş", schedule.NoteMax),
	})
	must(t, err)
	if len(got.Items) != 2 || got.MaxItems != schedule.DefaultMaxItems {
		t.Fatalf("sınırlar: %+v", got)
	}
	// Edits are checked the same way.
	_, err = e.schedules.Update(ctx, r, got.Items[0].ID, schedule.Input{StartsAt: at, Title: ""})
	wantField(t, "düzenlemede başlıksız", err, "title")

	// The database bounds the values too.
	for name, tc := range map[string]struct{ sql, constraint string }{
		"boş başlık": {`INSERT INTO event_schedule_items (id, event_id, component_key, starts_at, title) VALUES (gen_random_uuid(), $1, 'plan', now(), '')`, "event_schedule_items_title_len"},
		"boş not":    {`INSERT INTO event_schedule_items (id, event_id, component_key, starts_at, title, note) VALUES (gen_random_uuid(), $1, 'plan', now(), 'a', '')`, "event_schedule_items_note_len"},
		"ters saat":  {`INSERT INTO event_schedule_items (id, event_id, component_key, starts_at, ends_at, title) VALUES (gen_random_uuid(), $1, 'plan', now(), now() - interval '1 hour', 'a')`, "event_schedule_items_ends"},
	} {
		_, err := e.pool.Exec(ctx, tc.sql, ev.ID.PG())
		if c := constraintOf(err); c != tc.constraint {
			t.Errorf("%s: %q (%v)", name, c, err)
		}
	}
	// core_app cannot move an item to another event.
	_, err = e.pool.Exec(ctx, `UPDATE event_schedule_items SET event_id = $1`, id.New().PG())
	if err == nil {
		t.Fatal("madde başka etkinliğe taşındı")
	}
}

// The component exists only where the event's type version has it, under
// its key; an optional one is off until the organizer switches it on.
// Writes outside the type version are refused by the service and the
// trigger.
func TestScheduleOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral)
	tour := e.newEvent(t, tf, admin, tourType)
	extra := e.newEvent(t, tf, admin, tourType, component.Selection{Key: "extra", Enabled: on(true)})
	in := schedule.Input{StartsAt: tour.StartsAt, Title: "Varış"}

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":          ref(admin, tf, general.ID, "schedule"),
		"tanımda olmayan anahtar": ref(admin, tf, tour.ID, "schedule"),
	} {
		_, err := e.schedules.Get(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.schedules.Add(ctx, r, in)
		wantErr(t, name+" ekleme", err, apperr.ErrComponentNotFound)
		_, err = e.schedules.Update(ctx, r, id.New(), in)
		wantErr(t, name+" düzenleme", err, apperr.ErrComponentNotFound)
		_, err = e.schedules.Remove(ctx, r, id.New())
		wantErr(t, name+" silme", err, apperr.ErrComponentNotFound)
	}
	_, err := e.schedules.Get(ctx, ref(admin, tf, tour.ID, "extra"))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.schedules.Add(ctx, ref(admin, tf, tour.ID, "extra"), in)
	wantErr(t, "kapalı bileşene yazma", err, apperr.ErrComponentDisabled)
	if _, err := e.schedules.Add(ctx, ref(admin, tf, extra.ID, "extra"), schedule.Input{StartsAt: extra.StartsAt, Title: "Varış"}); err != nil {
		t.Fatalf("açılan bileşen: %v", err)
	}

	// The type's next version drops the programme: the old event keeps it,
	// an event of the new version has none.
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, tourType, tourV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, tourType, tourV2)
	newer := e.newEvent(t, tf, admin, tourType)
	if newer.TypeVersion != 2 || tour.TypeVersion != 1 {
		t.Fatalf("tür sürümleri: eski %d, yeni %d", tour.TypeVersion, newer.TypeVersion)
	}
	_, err = e.schedules.Add(ctx, ref(admin, tf, newer.ID, "plan"), schedule.Input{StartsAt: newer.StartsAt, Title: "Varış"})
	wantErr(t, "yeni sürümde olmayan program", err, apperr.ErrComponentNotFound)
	if got, err := e.schedules.Add(ctx, ref(admin, tf, tour.ID, "plan"), in); err != nil || len(got.Items) != 1 {
		t.Fatalf("eski sürümün etkinliği: %+v %v", got, err)
	}

	// The trigger refuses writes that go around the service.
	for name, tc := range map[string]struct {
		ev  id.ID
		key string
	}{
		"bileşensiz tür": {general.ID, "schedule"},
		"yeni sürüm":     {newer.ID, "plan"},
		"kapalı bileşen": {tour.ID, "extra"},
	} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_schedule_items (id, event_id, component_key, starts_at, title)
			VALUES (gen_random_uuid(), $1, $2, now(), 'Varış')`, tc.ev.PG(), tc.key)
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
}

// Who may do what for every role; IDOR; a cancelled or frozen event takes
// nothing, and reads stay open.
func TestSchedulePermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, tourType)
	const key = "plan"
	organizer := e.member(t, tf, authz.Member)
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	muted := e.member(t, tf, authz.Member)
	e.mute(t, tf, muted)
	// A member's own event (members may not create events by default: the
	// organizer is written directly).
	own := e.newEvent(t, tf, admin, tourType)
	e.exec(t, `UPDATE events SET created_by = $2 WHERE id = $1`, own.ID.PG(), organizer.PG())

	for _, tc := range []struct {
		name string
		user id.ID
		ev   event.Event
		want error
	}{
		{"yönetici", admin, ev, nil},
		{"moderatör", moderator, ev, nil},
		{"kendi etkinliğinin organizatörü", organizer, own, nil},
		{"başkasının etkinliğinde organizatör", organizer, ev, apperr.ErrPermissionDenied},
		{"üye", member, ev, apperr.ErrPermissionDenied},
		{"susturulmuş üye", muted, ev, apperr.ErrPermissionDenied},
		{"üye değil", id.New(), ev, apperr.ErrTayfaNotFound},
	} {
		denied := func(what string, err error) {
			t.Helper()
			if tc.want == nil {
				must(t, err)
			} else if ok := errors.Is(err, tc.want) || (tc.user == muted && errors.Is(err, apperr.ErrMuted)); !ok {
				t.Errorf("%s %s: %v, %v bekleniyordu", tc.name, what, err, tc.want)
			}
		}
		r := ref(tc.user, tf, tc.ev.ID, key)
		got, err := e.schedules.Add(ctx, r, schedule.Input{StartsAt: tc.ev.StartsAt, Title: "Varış"})
		denied("ekleme", err)
		// The item to edit and remove: the caller's own, or the admin's.
		var item id.ID
		if err == nil {
			item = got.Items[len(got.Items)-1].ID
		} else {
			byAdmin, err := e.schedules.Add(ctx, ref(admin, tf, tc.ev.ID, key), schedule.Input{StartsAt: tc.ev.StartsAt, Title: "Varış"})
			must(t, err)
			item = byAdmin.Items[len(byAdmin.Items)-1].ID
		}
		_, err = e.schedules.Update(ctx, r, item, schedule.Input{StartsAt: tc.ev.StartsAt, Title: "Toplanma"})
		denied("düzenleme", err)
		_, err = e.schedules.Remove(ctx, r, item)
		denied("silme", err)

		got, err = e.schedules.Get(ctx, r)
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			wantErr(t, tc.name+" okuma", err, tc.want)
			continue
		}
		// A mute does not hide the programme.
		must(t, err)
		if got.CallerCanEdit != (tc.want == nil) {
			t.Errorf("%s: caller_can_edit = %v", tc.name, got.CallerCanEdit)
		}
	}

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err := e.schedules.Get(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.schedules.Add(ctx, ref(admin2, tf2, ev.ID, key), schedule.Input{StartsAt: ev.StartsAt, Title: "Varış"})
	wantErr(t, "başka tayfa ekleme", err, apperr.ErrEventNotFound)
	// Another event's item is unknown here.
	other := e.newEvent(t, tf, admin, tourType)
	got, err := e.schedules.Add(ctx, ref(admin, tf, other.ID, key), schedule.Input{StartsAt: other.StartsAt, Title: "Varış"})
	must(t, err)
	_, err = e.schedules.Remove(ctx, ref(admin, tf, ev.ID, key), got.Items[0].ID)
	wantErr(t, "başka etkinliğin maddesi", err, apperr.ErrEventItemNotFound)

	// During and after the event the programme can still be corrected,
	// until the write window closes.
	e.clock.set(ev.EndsAt.Add(time.Hour))
	got, err = e.schedules.Add(ctx, ref(admin, tf, ev.ID, key), schedule.Input{StartsAt: ev.EndsAt, Title: "Dönüş"})
	must(t, err)
	item := got.Items[len(got.Items)-1].ID
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.schedules.Add(ctx, ref(admin, tf, ev.ID, key), schedule.Input{StartsAt: ev.EndsAt, Title: "Dönüş"})
	wantErr(t, "donmuş ekleme", err, apperr.ErrComponentFrozen)
	_, err = e.schedules.Update(ctx, ref(admin, tf, ev.ID, key), item, schedule.Input{StartsAt: ev.EndsAt, Title: "Dönüş"})
	wantErr(t, "donmuş düzenleme", err, apperr.ErrComponentFrozen)
	_, err = e.schedules.Remove(ctx, ref(admin, tf, ev.ID, key), item)
	wantErr(t, "donmuş silme", err, apperr.ErrComponentFrozen)
	if got, err := e.schedules.Get(ctx, ref(member, tf, ev.ID, key)); err != nil || got.CallerCanEdit {
		t.Fatalf("donmuş okuma: %+v %v", got, err)
	}

	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	cancelled := e.newEvent(t, tf, admin, tourType)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.schedules.Add(ctx, ref(admin, tf, cancelled.ID, key), schedule.Input{StartsAt: cancelled.StartsAt, Title: "Varış"})
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
	if got, err := e.schedules.Get(ctx, ref(admin, tf, cancelled.ID, key)); err != nil || got.CallerCanEdit {
		t.Fatalf("iptal edilmiş etkinlikte yazma izni: %+v %v", got, err)
	}
}

// A series occurrence takes the template's programme (CopyScheduleItems),
// moved with its start, after the template's selections: the copy passes
// the same trigger.
func TestScheduleCopyForSeries(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	tmpl := e.newEvent(t, tf, admin, tourType, component.Selection{Key: "extra", Enabled: on(true)})
	for _, in := range []schedule.Input{
		{StartsAt: tmpl.StartsAt, EndsAt: tmpl.StartsAt.Add(time.Hour), Title: "Varış", Note: "Kapıda"},
		{StartsAt: tmpl.StartsAt.Add(2 * time.Hour), Title: "Dönüş"},
	} {
		_, err := e.schedules.Add(ctx, ref(admin, tf, tmpl.ID, "plan"), in)
		must(t, err)
	}
	_, err := e.schedules.Add(ctx, ref(admin, tf, tmpl.ID, "extra"), schedule.Input{StartsAt: tmpl.StartsAt, Title: "Ek"})
	must(t, err)
	want, err := e.schedules.Get(ctx, ref(admin, tf, tmpl.ID, "plan"))
	must(t, err)
	q := store.New(e.pool)

	// A week later, with the optional programme switched on as the
	// template.
	e.clock.set(e.clock.now().Add(7 * 24 * time.Hour))
	occ := e.newEvent(t, tf, admin, tourType, component.Selection{Key: "extra", Enabled: on(true)})
	shift := occ.StartsAt.Sub(tmpl.StartsAt)
	must(t, q.CopyScheduleItems(ctx, store.CopyScheduleItemsParams{ToEventID: occ.ID.PG(), FromEventID: tmpl.ID.PG()}))
	got, err := e.schedules.Get(ctx, ref(admin, tf, occ.ID, "plan"))
	must(t, err)
	if len(got.Items) != len(want.Items) {
		t.Fatalf("kopya: %+v", got.Items)
	}
	for i, it := range got.Items {
		w := want.Items[i]
		if it.ID == w.ID || it.Title != w.Title || it.Note != w.Note || !it.StartsAt.Equal(w.StartsAt.Add(shift)) ||
			it.EndsAt.IsZero() != w.EndsAt.IsZero() || (!w.EndsAt.IsZero() && !it.EndsAt.Equal(w.EndsAt.Add(shift))) {
			t.Fatalf("kopya madde %d: %+v, şablon %+v", i, it, w)
		}
	}
	if extra, err := e.schedules.Get(ctx, ref(admin, tf, occ.ID, "extra")); err != nil || titles(extra) != "Ek" {
		t.Fatalf("ek program: %+v %v", extra, err)
	}
	// Without the selection the trigger refuses the copy.
	off := e.newEvent(t, tf, admin, tourType)
	if err := q.CopyScheduleItems(ctx, store.CopyScheduleItemsParams{ToEventID: off.ID.PG(), FromEventID: tmpl.ID.PG()}); constraintOf(err) != "event_component_guard" {
		t.Fatalf("kapalı bileşene kopya: %v", err)
	}
	// A template without a programme copies nothing.
	bare := e.newEvent(t, tf, admin, talkType)
	target := e.newEvent(t, tf, admin, talkType)
	must(t, q.CopyScheduleItems(ctx, store.CopyScheduleItemsParams{ToEventID: target.ID.PG(), FromEventID: bare.ID.PG()}))
	if n := e.count(t, `SELECT count(*) FROM event_schedule_items WHERE event_id = $1`, target.ID.PG()); n != 0 {
		t.Fatalf("programsız şablondan kopya: %d", n)
	}
}
