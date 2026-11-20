package ranking_test

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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/measure"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/ranking"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. raceType has a ranking by time entered by hand (Notion Faz 3:
// koşu, derece); placeType one by place, descending; lapsType a personal
// measure the organizer enters and a ranking computed from it; examType a
// sensitive personal measure each participant enters and a ranking on it;
// scoreType a sensitive personal measure the organizer enters and a
// ranking on it; optType an optional measure that is off by default, a
// ranking on it and an optional ranking that is off, and a second version
// without them.
const (
	raceType  = "test_race"
	raceV1    = `{"components":[{"kind":"ranking","params":{"metric":"duration"}}]}`
	placeType = "test_place"
	placeV1   = `{"components":[{"kind":"ranking","key":"places","params":{"metric":"rank","direction":"desc"}}]}`
	lapsType  = "test_laps"
	lapsV1    = `{"components":[{"kind":"measure","key":"laps","labels":{"tr":"Tur"},"params":{"unit":"piece","scope":"personal","entered_by":"organizer","max":500}},
		{"kind":"ranking","params":{"source":"laps"}}]}`
	examType = "test_exam"
	examV1   = `{"components":[{"kind":"measure","key":"net","labels":{"tr":"Net"},"params":{"unit":"net","scope":"personal","entered_by":"self","min":-40,"max":120,"decimals":2,"sensitive":true}},
		{"kind":"ranking","params":{"source":"net"}}]}`
	scoreType = "test_score"
	scoreV1   = `{"components":[{"kind":"measure","key":"score","labels":{"tr":"Puan"},"params":{"unit":"point","scope":"personal","entered_by":"organizer","max":100,"sensitive":true}},
		{"kind":"ranking","params":{"source":"score"}}]}`
	optType = "test_opt"
	optV1   = `{"components":[{"kind":"measure","key":"bags","labels":{"tr":"Torba"},"optional":{"default":false},"params":{"unit":"bag","scope":"personal","entered_by":"organizer"}},
		{"kind":"ranking","params":{"source":"bags","direction":"asc"}},
		{"kind":"ranking","key":"ranking_off","optional":{"default":false},"params":{"metric":"rank"}}]}`
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
	fw       *component.Service
	rankings *ranking.Service
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
	e := &env{fw: fw, rankings: ranking.New(fw), measures: measure.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	for key, def := range map[string]string{raceType: raceV1, placeType: placeV1, lapsType: lapsV1, examType: examV1, scoreType: scoreV1, optType: optV1} {
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Derece Tayfası", ClientRequestID: id.New().String()})
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

// places returns the ranking as "user:rank" in order.
func places(entries []ranking.Entry) map[id.ID]int32 {
	out := make(map[id.ID]int32, len(entries))
	for _, en := range entries {
		out[en.User] = en.Rank
	}
	return out
}

// values returns the values in order.
func values(entries []ranking.Entry) []float64 {
	out := make([]float64, 0, len(entries))
	for _, en := range entries {
		out = append(out, en.Value)
	}
	return out
}

func equal(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A ranking by time entered by hand: managers and assigned referees write
// a participant's time from the event's start; times are rounded to
// hundredths and ascending; equal times share a place; the ranking freezes
// with the event's components.
func TestRankingByHand(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	ev := e.newEvent(t, tf, organizer, raceType, nil)
	moderator := e.member(t, tf, authz.Moderator)
	const key = "ranking"
	a := e.going(t, tf, ev.ID, authz.Member)
	b := e.attended(t, tf, ev.ID, authz.Member)
	c := e.going(t, tf, ev.ID, authz.Member)
	referee := e.member(t, tf, authz.Member)
	bystander := e.member(t, tf, authz.Member)
	_, err := e.fw.AssignReferee(ctx, organizer, tf, ev.ID, component.RefereeInput{User: referee, ComponentKey: key})
	must(t, err)
	// The assignment is a change of its own.
	base := e.changes(t, ev.ID, key)

	got, err := e.rankings.Get(ctx, ref(bystander, tf, ev.ID, key))
	must(t, err)
	if got.Source != "" || got.Metric != ranking.MetricDuration || got.Direction != ranking.DirectionAsc || got.Unit != "" ||
		got.Decimals != ranking.DurationDecimals || len(got.Entries) != 0 || got.Hidden || !got.MeasureEnabled || got.CallerCanEdit {
		t.Fatalf("başlangıç: %+v", got)
	}
	_, err = e.rankings.SetEntry(ctx, ref(referee, tf, ev.ID, key), a, 61)
	wantErr(t, "başlamadan", err, apperr.ErrRankingNotStarted)
	if got, err := e.rankings.Get(ctx, ref(organizer, tf, ev.ID, key)); err != nil || got.CallerCanEdit {
		t.Fatalf("başlamadan yazma izni: %+v %v", got, err)
	}

	e.clock.set(ev.StartsAt.Add(30 * time.Minute))
	for name, u := range map[string]id.ID{"hakem": referee, "organizatör": organizer, "moderatör": moderator} {
		if got, err := e.rankings.Get(ctx, ref(u, tf, ev.ID, key)); err != nil || !got.CallerCanEdit {
			t.Errorf("%s yazabilmeli: %+v %v", name, got, err)
		}
	}
	for name, u := range map[string]id.ID{"koşucu": a, "izleyici": bystander} {
		if got, err := e.rankings.Get(ctx, ref(u, tf, ev.ID, key)); err != nil || got.CallerCanEdit {
			t.Errorf("%s yazamamalı: %+v %v", name, got, err)
		}
	}

	got, err = e.rankings.SetEntry(ctx, ref(referee, tf, ev.ID, key), a, 61.456)
	must(t, err)
	if len(got.Entries) != 1 || got.Entries[0].User != a || got.Entries[0].Value != 61.46 || got.Entries[0].Rank != 1 || got.Entries[0].UpdatedAt.IsZero() {
		t.Fatalf("hakemin girdiği: %+v", got.Entries)
	}
	_, err = e.rankings.SetEntry(ctx, ref(organizer, tf, ev.ID, key), b, 59.5)
	must(t, err)
	got, err = e.rankings.SetEntry(ctx, ref(moderator, tf, ev.ID, key), c, 59.5)
	must(t, err)
	// Ascending; the equal times share the first place, the next is third.
	if !equal(values(got.Entries), []float64{59.5, 59.5, 61.46}) {
		t.Fatalf("sıra: %v", values(got.Entries))
	}
	if p := places(got.Entries); p[b] != 1 || p[c] != 1 || p[a] != 3 {
		t.Fatalf("yerler: %v", p)
	}
	if n := e.changes(t, ev.ID, key) - base; n != 3 {
		t.Fatalf("canlı güncelleme = %d", n)
	}
	// The same value again changes nothing.
	_, err = e.rankings.SetEntry(ctx, ref(referee, tf, ev.ID, key), c, 59.5)
	must(t, err)
	if n := e.changes(t, ev.ID, key) - base; n != 3 {
		t.Fatalf("aynı değer canlı güncelleme yazdı: %d", n)
	}

	for name, v := range map[string]float64{"sıfır": 0, "eksi": -1, "yuvarlanınca sıfır": 0.004, "üst sınır üstü": ranking.MaxDuration + 1} {
		_, err = e.rankings.SetEntry(ctx, ref(referee, tf, ev.ID, key), a, v)
		wantField(t, name, err, "value")
	}
	_, err = e.rankings.SetEntry(ctx, ref(a, tf, ev.ID, key), a, 50)
	wantErr(t, "koşucu kendi derecesini giremez", err, apperr.ErrPermissionDenied)
	_, err = e.rankings.SetEntry(ctx, ref(bystander, tf, ev.ID, key), a, 50)
	wantErr(t, "atanmamış üye", err, apperr.ErrPermissionDenied)
	_, err = e.rankings.SetEntry(ctx, ref(referee, tf, ev.ID, key), bystander, 50)
	wantErr(t, "katılımcı olmayan kişi", err, apperr.ErrRankingNotParticipant)
	_, err = e.rankings.SetEntry(ctx, ref(referee, tf, ev.ID, key), id.New(), 50)
	wantErr(t, "üye olmayan kişi", err, apperr.ErrRankingNotParticipant)
	_, err = e.rankings.SetEntry(ctx, ref(referee, tf, ev.ID, key), id.ID{}, 50)
	wantField(t, "boş kişi", err, "user_id")

	// Everyone sees the ranking.
	got, err = e.rankings.Get(ctx, ref(bystander, tf, ev.ID, key))
	must(t, err)
	if len(got.Entries) != 3 || got.Hidden {
		t.Fatalf("izleyicinin gördüğü: %+v", got)
	}

	got, err = e.rankings.RemoveEntry(ctx, ref(referee, tf, ev.ID, key), b)
	must(t, err)
	if p := places(got.Entries); len(p) != 2 || p[c] != 1 || p[a] != 2 {
		t.Fatalf("kaldırılan derece: %v", p)
	}
	// Removing what is not there is not an error and changes nothing.
	_, err = e.rankings.RemoveEntry(ctx, ref(referee, tf, ev.ID, key), b)
	must(t, err)
	if n := e.changes(t, ev.ID, key) - base; n != 4 {
		t.Fatalf("canlı güncelleme = %d", n)
	}
	_, err = e.rankings.RemoveEntry(ctx, ref(a, tf, ev.ID, key), a)
	wantErr(t, "koşucu kendi derecesini kaldıramaz", err, apperr.ErrPermissionDenied)

	// Seven days after the end the ranking freezes; reading still works.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.rankings.SetEntry(ctx, ref(organizer, tf, ev.ID, key), a, 60)
	wantErr(t, "donmuş derece", err, apperr.ErrComponentFrozen)
	_, err = e.rankings.RemoveEntry(ctx, ref(organizer, tf, ev.ID, key), a)
	wantErr(t, "donmuş dereceyi kaldırma", err, apperr.ErrComponentFrozen)
	if got, err := e.rankings.Get(ctx, ref(organizer, tf, ev.ID, key)); err != nil || got.CallerCanEdit || len(got.Entries) != 2 {
		t.Fatalf("donmuş derece okuma: %+v %v", got, err)
	}

	// The audit records name the component and the person, never the value.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND metadata ->> 'user_id' = $3`,
		ranking.AuditSet, ev.ID.PG(), a.String()); n != 1 {
		t.Fatalf("denetim kaydı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action LIKE 'event.ranking_%'
		AND EXISTS (SELECT 1 FROM jsonb_object_keys(metadata) AS k WHERE k NOT IN ('component_key', 'user_id'))`, ev.ID.PG()); n != 0 {
		t.Fatal("derece denetim kaydına yazıldı")
	}
	// A muted referee enters nothing (before the freeze).
	e.clock.set(ev.EndsAt)
	e.mute(t, tf, referee)
	_, err = e.rankings.SetEntry(ctx, ref(referee, tf, ev.ID, key), a, 3)
	wantErr(t, "susturulmuş hakem", err, apperr.ErrMuted)
}

// A ranking by place: whole places from 1 to MaxRank, in the definition's
// direction.
func TestRankingByPlace(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, placeType, nil)
	a := e.going(t, tf, ev.ID, authz.Member)
	b := e.going(t, tf, ev.ID, authz.Member)
	const key = "places"
	e.clock.set(ev.StartsAt)

	for name, v := range map[string]float64{"sıfır": 0, "kesirli": 1.5, "üst sınır üstü": ranking.MaxRank + 1} {
		_, err := e.rankings.SetEntry(ctx, ref(admin, tf, ev.ID, key), a, v)
		wantField(t, name, err, "value")
	}
	_, err := e.rankings.SetEntry(ctx, ref(admin, tf, ev.ID, key), a, 1)
	must(t, err)
	got, err := e.rankings.SetEntry(ctx, ref(admin, tf, ev.ID, key), b, 2)
	must(t, err)
	if got.Metric != ranking.MetricRank || got.Direction != ranking.DirectionDesc || got.Decimals != 0 ||
		!equal(values(got.Entries), []float64{2, 1}) || got.Entries[0].User != b || got.Entries[0].Rank != 1 || got.Entries[1].Rank != 2 {
		t.Fatalf("sıra ile derece: %+v", got)
	}
}

// A ranking computed from a personal measure: the measure's stored values,
// descending by default, no entry of its own; Load gives the same order
// and the winners.
func TestRankingFromMeasure(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, lapsType, nil)
	a := e.going(t, tf, ev.ID, authz.Member)
	b := e.going(t, tf, ev.ID, authz.Member)
	c := e.going(t, tf, ev.ID, authz.Member)
	const key = "ranking"
	e.clock.set(ev.StartsAt.Add(time.Hour))

	for u, v := range map[id.ID]float64{a: 12, b: 15, c: 15} {
		_, err := e.measures.SetEntry(ctx, ref(admin, tf, ev.ID, "laps"), u, v)
		must(t, err)
	}
	got, err := e.rankings.Get(ctx, ref(a, tf, ev.ID, key))
	must(t, err)
	if got.Source != "laps" || got.Metric != "" || got.Direction != ranking.DirectionDesc || got.Unit != "piece" || got.Decimals != 0 ||
		got.Hidden || !got.MeasureEnabled || got.CallerCanEdit || !equal(values(got.Entries), []float64{15, 15, 12}) {
		t.Fatalf("ölçümden derece: %+v", got)
	}
	if p := places(got.Entries); p[b] != 1 || p[c] != 1 || p[a] != 3 {
		t.Fatalf("yerler: %v", p)
	}
	// No entry of its own, for anyone; the organizer gets no write right.
	_, err = e.rankings.SetEntry(ctx, ref(admin, tf, ev.ID, key), a, 20)
	wantErr(t, "ölçümden dereceye yazma", err, apperr.ErrRankingFromMeasure)
	_, err = e.rankings.RemoveEntry(ctx, ref(admin, tf, ev.ID, key), a)
	wantErr(t, "ölçümden dereceyi kaldırma", err, apperr.ErrRankingFromMeasure)
	if got, err := e.rankings.Get(ctx, ref(admin, tf, ev.ID, key)); err != nil || got.CallerCanEdit {
		t.Fatalf("ölçümden derecede yazma izni: %+v %v", got, err)
	}
	if n := e.count(t, `SELECT count(*) FROM event_ranking_entries WHERE event_id = $1`, ev.ID.PG()); n != 0 {
		t.Fatalf("ölçümden derece tabloya yazıldı: %d", n)
	}

	// The measure changes, the ranking follows.
	_, err = e.measures.SetEntry(ctx, ref(admin, tf, ev.ID, "laps"), a, 16)
	must(t, err)
	def, comp := e.component(t, lapsType, key)
	res, err := ranking.Load(ctx, store.New(e.pool), ev.ID, def, comp)
	must(t, err)
	if res.Hidden || !equal(values(res.Entries), []float64{16, 15, 15}) {
		t.Fatalf("Load: %+v", res)
	}
	if w := res.Winners(); len(w) != 1 || w[0].User != a {
		t.Fatalf("birinci: %+v", w)
	}
	// A tie shares the first place.
	_, err = e.measures.SetEntry(ctx, ref(admin, tf, ev.ID, "laps"), b, 16)
	must(t, err)
	res, err = ranking.Load(ctx, store.New(e.pool), ev.ID, def, comp)
	must(t, err)
	if w := res.Winners(); len(w) != 2 {
		t.Fatalf("berabere birinciler: %+v", w)
	}
	if w := (ranking.Result{}).Winners(); len(w) != 0 {
		t.Fatalf("boş derecenin birincisi: %+v", w)
	}
}

// The ranking shows no more than its measure: the order of a sensitive
// measure would tell the others' values, so only those who see every value
// see it, and Load hides it from everyone.
func TestRankingSensitiveMeasure(t *testing.T) {
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
		got, err := e.rankings.Get(ctx, ref(u, tf, exam.ID, "ranking"))
		must(t, err)
		if !got.Hidden || len(got.Entries) != 0 || !got.MeasureEnabled {
			t.Errorf("%s hassas dereceyi gördü: %+v", name, got)
		}
	}

	// The organizer enters the scores: managers see the ranking; the
	// player does not.
	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	match := e.newEvent(t, tf, admin, scoreType, nil)
	player := e.going(t, tf, match.ID, authz.Member)
	other := e.going(t, tf, match.ID, authz.Member)
	e.clock.set(match.StartsAt.Add(time.Hour))
	_, err = e.measures.SetEntry(ctx, ref(admin, tf, match.ID, "score"), player, 12)
	must(t, err)
	_, err = e.measures.SetEntry(ctx, ref(admin, tf, match.ID, "score"), other, 7)
	must(t, err)
	for name, u := range map[string]id.ID{"yönetici": admin, "moderatör": moderator} {
		if got, err := e.rankings.Get(ctx, ref(u, tf, match.ID, "ranking")); err != nil || got.Hidden || !equal(values(got.Entries), []float64{12, 7}) {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	if got, err := e.rankings.Get(ctx, ref(player, tf, match.ID, "ranking")); err != nil || !got.Hidden || len(got.Entries) != 0 {
		t.Fatalf("oyuncu hassas dereceyi gördü: %+v %v", got, err)
	}
	// The celebration never announces it.
	def, comp := e.component(t, scoreType, "ranking")
	res, err := ranking.Load(ctx, store.New(e.pool), match.ID, def, comp)
	must(t, err)
	if !res.Hidden || len(res.Entries) != 0 || len(res.Winners()) != 0 {
		t.Fatalf("Load hassas dereceyi verdi: %+v", res)
	}
}

// The component exists only where the event's type version has it, under
// its key; an optional one is off until the organizer switches it on, and
// a ranking on a measure that is off ranks nobody. Writes outside the type
// version are refused by the service and the trigger.
func TestRankingOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral, nil)
	race := e.newEvent(t, tf, admin, raceType, nil)
	off := e.newEvent(t, tf, admin, optType, nil)
	on1 := e.newEvent(t, tf, admin, optType, nil,
		component.Selection{Key: "bags", Enabled: on(true)}, component.Selection{Key: "ranking_off", Enabled: on(true)})
	runner := e.going(t, tf, on1.ID, authz.Member)

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":           ref(admin, tf, general.ID, "ranking"),
		"tanımda olmayan anahtar":  ref(admin, tf, race.ID, "places"),
		"başka türün derecesi yok": ref(admin, tf, race.ID, "ranking_off"),
	} {
		_, err := e.rankings.Get(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.rankings.SetEntry(ctx, r, admin, 1)
		wantErr(t, name+" yazma", err, apperr.ErrComponentNotFound)
		_, err = e.rankings.RemoveEntry(ctx, r, admin)
		wantErr(t, name+" kaldırma", err, apperr.ErrComponentNotFound)
	}
	_, err := e.rankings.Get(ctx, ref(admin, tf, off.ID, "ranking_off"))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.rankings.SetEntry(ctx, ref(admin, tf, off.ID, "ranking_off"), admin, 1)
	wantErr(t, "kapalı bileşene yazma", err, apperr.ErrComponentDisabled)

	// The measure is off: the ranking is empty.
	got, err := e.rankings.Get(ctx, ref(admin, tf, off.ID, "ranking"))
	must(t, err)
	if got.MeasureEnabled || len(got.Entries) != 0 || got.Hidden || got.Source != "bags" || got.Direction != ranking.DirectionAsc {
		t.Fatalf("kapalı ölçümün derecesi: %+v", got)
	}
	// Switched on, it counts; so does the optional ranking switched on.
	e.clock.set(on1.StartsAt.Add(time.Hour))
	_, err = e.measures.SetEntry(ctx, ref(admin, tf, on1.ID, "bags"), runner, 3)
	must(t, err)
	if got, err := e.rankings.Get(ctx, ref(admin, tf, on1.ID, "ranking")); err != nil || !got.MeasureEnabled || !equal(values(got.Entries), []float64{3}) {
		t.Fatalf("açık ölçümün derecesi: %+v %v", got, err)
	}
	if got, err := e.rankings.SetEntry(ctx, ref(admin, tf, on1.ID, "ranking_off"), runner, 1); err != nil || len(got.Entries) != 1 {
		t.Fatalf("açılan derece: %+v %v", got, err)
	}

	// The type's next version drops the ranking: the old event keeps it,
	// an event of the new version has none.
	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, optType, optV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, optType, optV2)
	newer := e.newEvent(t, tf, admin, optType, nil)
	if newer.TypeVersion != 2 || on1.TypeVersion != 1 {
		t.Fatalf("tür sürümleri: eski %d, yeni %d", on1.TypeVersion, newer.TypeVersion)
	}
	newRunner := e.going(t, tf, newer.ID, authz.Member)
	e.clock.set(newer.StartsAt.Add(time.Hour))
	_, err = e.rankings.SetEntry(ctx, ref(admin, tf, newer.ID, "ranking_off"), newRunner, 1)
	wantErr(t, "yeni sürümde olmayan derece", err, apperr.ErrComponentNotFound)

	// The trigger refuses writes that go around the service.
	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "yeni sürüm": newer.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_ranking_entries (id, event_id, component_key, user_id, value) VALUES ($1, $2, 'ranking_off', $3, 1)`,
			id.New().PG(), ev.PG(), admin.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
	_, err = e.pool.Exec(ctx, `INSERT INTO event_ranking_entries (id, event_id, component_key, user_id, value) VALUES ($1, $2, 'ranking_off', $3, 1)`,
		id.New().PG(), off.ID.PG(), admin.PG())
	if c := constraintOf(err); c != "event_component_guard" {
		t.Errorf("kapalı bileşen: tetikleyici %q (%v)", c, err)
	}
	// The database bounds the value too.
	_, err = e.pool.Exec(ctx, `INSERT INTO event_ranking_entries (id, event_id, component_key, user_id, value) VALUES ($1, $2, 'ranking', $3, 0)`,
		id.New().PG(), race.ID.PG(), admin.PG())
	if c := constraintOf(err); c != "event_ranking_entries_value" {
		t.Errorf("sıfır değer: %q (%v)", c, err)
	}
}

// Who may enter values for every role; IDOR; a cancelled event takes
// nothing.
func TestRankingPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, raceType, nil)
	cancelled := e.newEvent(t, tf, admin, raceType, nil)
	_, err := e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	const key = "ranking"
	organizer := e.member(t, tf, authz.Member)
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	muted := e.member(t, tf, authz.Member)
	e.mute(t, tf, muted)
	referee := e.member(t, tf, authz.Member)
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: referee, ComponentKey: key})
	must(t, err)
	// A member's own event (members may not create events by default: the
	// organizer is written directly).
	own := e.newEvent(t, tf, admin, raceType, nil)
	e.exec(t, `UPDATE events SET created_by = $2 WHERE id = $1`, own.ID.PG(), organizer.PG())
	runner := e.going(t, tf, ev.ID, authz.Member)
	_, err = e.events.SetRSVP(ctx, runner, tf, own.ID, event.RSVPGoing)
	must(t, err)
	e.clock.set(ev.StartsAt.Add(time.Minute))

	for _, tc := range []struct {
		name string
		user id.ID
		ev   id.ID
		want error
	}{
		{"yönetici", admin, ev.ID, nil},
		{"moderatör", moderator, ev.ID, nil},
		{"atanmış hakem", referee, ev.ID, nil},
		{"kendi etkinliğinin organizatörü", organizer, own.ID, nil},
		{"başkasının etkinliğinde organizatör", organizer, ev.ID, apperr.ErrPermissionDenied},
		{"hakemin atanmadığı etkinlik", referee, own.ID, apperr.ErrPermissionDenied},
		{"üye", member, ev.ID, apperr.ErrPermissionDenied},
		{"susturulmuş üye", muted, ev.ID, apperr.ErrPermissionDenied},
		{"üye değil", id.New(), ev.ID, apperr.ErrTayfaNotFound},
	} {
		_, err := e.rankings.SetEntry(ctx, ref(tc.user, tf, tc.ev, key), runner, 60)
		if tc.want == nil {
			must(t, err)
		} else if ok := errors.Is(err, tc.want) || (tc.user == muted && errors.Is(err, apperr.ErrMuted)); !ok {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
		_, err = e.rankings.RemoveEntry(ctx, ref(tc.user, tf, tc.ev, key), runner)
		if tc.want == nil {
			must(t, err)
		} else if ok := errors.Is(err, tc.want) || (tc.user == muted && errors.Is(err, apperr.ErrMuted)); !ok {
			t.Errorf("%s kaldırma: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
		got, err := e.rankings.Get(ctx, ref(tc.user, tf, tc.ev, key))
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			wantErr(t, tc.name+" okuma", err, tc.want)
			continue
		}
		// A mute does not hide the ranking.
		must(t, err)
		if got.CallerCanEdit != (tc.want == nil) {
			t.Errorf("%s: caller_can_edit = %v", tc.name, got.CallerCanEdit)
		}
	}

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err = e.rankings.Get(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.rankings.SetEntry(ctx, ref(admin2, tf2, ev.ID, key), runner, 1)
	wantErr(t, "başka tayfa yazma", err, apperr.ErrEventNotFound)
	_, err = e.rankings.RemoveEntry(ctx, ref(admin2, tf2, ev.ID, key), runner)
	wantErr(t, "başka tayfa kaldırma", err, apperr.ErrEventNotFound)

	_, err = e.rankings.SetEntry(ctx, ref(admin, tf, cancelled.ID, key), runner, 1)
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
	if got, err := e.rankings.Get(ctx, ref(admin, tf, cancelled.ID, key)); err != nil || got.CallerCanEdit {
		t.Fatalf("iptal edilmiş etkinlikte yazma izni: %+v %v", got, err)
	}
}

// Order ranks by value in the direction, equal values sharing a place.
func TestOrder(t *testing.T) {
	a, b, c, d := id.New(), id.New(), id.New(), id.New()
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	entries := []ranking.Entry{
		{User: a, Value: 3, UpdatedAt: at}, {User: b, Value: 1, UpdatedAt: at.Add(time.Second)},
		{User: c, Value: 1, UpdatedAt: at}, {User: d, Value: 2, UpdatedAt: at},
	}
	ranking.Order(entries, ranking.DirectionAsc)
	// Among equal values the earlier entry comes first.
	if entries[0].User != c || entries[1].User != b || entries[2].User != d || entries[3].User != a {
		t.Fatalf("artan: %+v", entries)
	}
	if p := places(entries); p[c] != 1 || p[b] != 1 || p[d] != 3 || p[a] != 4 {
		t.Fatalf("artan yerler: %v", p)
	}
	ranking.Order(entries, ranking.DirectionDesc)
	if !equal(values(entries), []float64{3, 2, 1, 1}) {
		t.Fatalf("azalan: %v", values(entries))
	}
	if p := places(entries); p[a] != 1 || p[d] != 2 || p[c] != 3 || p[b] != 3 {
		t.Fatalf("azalan yerler: %v", p)
	}
	ranking.Order(nil, ranking.DirectionAsc)
}

// component reads a test type's definition and its component key, as the
// celebration would.
func (e *env) component(t *testing.T, typ, key string) (*definition.Definition, definition.Component) {
	t.Helper()
	var raw []byte
	must(t, e.pool.QueryRow(ctx, `SELECT definition FROM catalog_event_type_versions WHERE type_key = $1 AND version = 1`, typ).Scan(&raw))
	def, err := definition.Parse(raw)
	must(t, err)
	c, ok := def.Component(key)
	if !ok {
		t.Fatalf("%s türünde %s yok", typ, key)
	}
	return def, c
}
