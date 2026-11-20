package celebration_test

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

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/celebration"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/guest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/measure"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/ranking"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types. raceType celebrates a ranking by time entered by hand
// (Notion Faz 3: koşu, derece); gameType a single match scoreboard with
// guests (masa oyunu gecesi, kutlama); plainType has a celebration without a
// source; examType a ranking of a sensitive measure and a celebration of
// it; bloodType a celebration in a type whose attendance is sensitive;
// optType an optional ranking that is off by default with a celebration of
// it and an optional celebration that is off, and a second version without
// them.
const (
	raceType  = "test_cel_race"
	raceV1    = `{"components":[{"kind":"ranking","params":{"metric":"duration"}},{"kind":"celebration","params":{"source":"ranking"}}]}`
	gameType  = "test_cel_game"
	gameV1    = `{"components":[{"kind":"scoreboard","key":"games","params":{"rounds":"single","winner":"high","guests":true}},{"kind":"celebration","params":{"source":"games"}}]}`
	plainType = "test_cel_plain"
	plainV1   = `{"components":[{"kind":"celebration"}]}`
	examType  = "test_cel_exam"
	examV1    = `{"components":[{"kind":"measure","key":"net","labels":{"tr":"Net"},"params":{"unit":"net","scope":"personal","entered_by":"self","min":-40,"max":120,"decimals":2,"sensitive":true}},
		{"kind":"ranking","params":{"source":"net"}},{"kind":"celebration","params":{"source":"ranking"}}]}`
	bloodType = "test_cel_blood"
	bloodV1   = `{"components":[{"kind":"celebration"}],"flags":{"sensitive_attendance":true}}`
	optType   = "test_cel_opt"
	optV1     = `{"components":[{"kind":"ranking","optional":{"default":false},"params":{"metric":"rank"}},
		{"kind":"celebration","params":{"source":"ranking"}},
		{"kind":"celebration","key":"cheer","optional":{"default":false}}]}`
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
	fw           *component.Service
	celebrations *celebration.Service
	rankings     *ranking.Service
	measures     *measure.Service
	scoreboards  *scoreboard.Service
	guests       *guest.Service
	events       *event.Service
	tayfas       *tayfa.Service
	pool         *pgxpool.Pool
	admin        *pgxpool.Pool
	clock        *clock
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
	e := &env{
		fw: fw, celebrations: celebration.New(fw), rankings: ranking.New(fw), measures: measure.New(fw),
		scoreboards: scoreboard.New(fw), guests: guest.New(fw), events: events, tayfas: tayfas,
		pool: d.App, admin: d.Admin, clock: c,
	}
	for key, def := range map[string]string{raceType: raceV1, gameType: gameV1, plainType: plainV1, examType: examV1, bloodType: bloodV1, optType: optV1} {
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Kutlama Tayfası", ClientRequestID: id.New().String()})
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

// notice is one celebration notification request.
type notice struct {
	actor, target id.ID
	recipients    []id.ID
	payload       map[string]any
}

// notices returns the celebration notification requests about ev, oldest
// first.
func (e *env) notices(t *testing.T, ev id.ID) []notice {
	t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT actor_id, subject_id, payload FROM domain_events
		WHERE type = $1 AND payload->>'type' = $2 AND subject_id = $3 ORDER BY id`, notify.EventType, notify.TypeCelebration, ev.PG())
	must(t, err)
	defer rows.Close()
	var out []notice
	for rows.Next() {
		var n notice
		var actor, target pgtype.UUID
		var raw []byte
		must(t, rows.Scan(&actor, &target, &raw))
		n.actor, n.target = id.ID(actor.Bytes), id.ID(target.Bytes)
		must(t, json.Unmarshal(raw, &n.payload))
		for _, v := range n.payload["recipient_ids"].([]any) {
			u, _ := id.Parse(v.(string))
			n.recipients = append(n.recipients, u)
		}
		out = append(out, n)
	}
	must(t, rows.Err())
	return out
}

// sameSet compares user sets regardless of order.
func sameSet(got []id.ID, want ...id.ID) bool {
	g, w := slices.Clone(got), slices.Clone(want)
	cmp := func(a, b id.ID) int { return slices.Compare(a[:], b[:]) }
	slices.SortFunc(g, cmp)
	slices.SortFunc(w, cmp)
	return slices.Equal(g, w)
}

// users returns the winners' people in order.
func users(ws []celebration.Winner) []id.ID {
	out := make([]id.ID, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.User)
	}
	return out
}

// A celebration of a ranking: the winners are the ranking's first place,
// read live; the first announcement tells the tayfa once, without the
// winner; the window is the component's.
func TestCelebrationFromRanking(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	ev := e.newEvent(t, tf, organizer, raceType)
	const key = "celebration"
	a := e.going(t, tf, ev.ID, authz.Member)
	b := e.going(t, tf, ev.ID, authz.Member)
	c := e.going(t, tf, ev.ID, authz.Member)
	bystander := e.member(t, tf, authz.Member)

	got, err := e.celebrations.Get(ctx, ref(bystander, tf, ev.ID, key))
	must(t, err)
	if got.Source != "ranking" || got.SourceKind != "ranking" || len(got.Winners) != 0 || got.Hidden || got.Announced || got.CallerCanAnnounce {
		t.Fatalf("başlangıç: %+v", got)
	}
	_, err = e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, key), nil)
	wantErr(t, "başlamadan", err, apperr.ErrCelebrationNotStarted)

	e.clock.set(ev.StartsAt.Add(time.Hour))
	if got, err := e.celebrations.Get(ctx, ref(organizer, tf, ev.ID, key)); err != nil || !got.CallerCanAnnounce {
		t.Fatalf("organizatör ilan edebilmeli: %+v %v", got, err)
	}
	_, err = e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, key), nil)
	wantErr(t, "kazanansız", err, apperr.ErrCelebrationNoWinner)
	_, err = e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, key), []id.ID{a})
	wantField(t, "kaynaklıda elle kazanan", err, "winner_ids")

	rr := ref(organizer, tf, ev.ID, "ranking")
	_, err = e.rankings.SetEntry(ctx, rr, a, 59.5)
	must(t, err)
	_, err = e.rankings.SetEntry(ctx, rr, b, 59.5)
	must(t, err)
	_, err = e.rankings.SetEntry(ctx, rr, c, 61)
	must(t, err)
	// A tie: both first places win.
	got, err = e.celebrations.Get(ctx, ref(bystander, tf, ev.ID, key))
	must(t, err)
	if !sameSet(users(got.Winners), a, b) || got.Announced {
		t.Fatalf("beraberlik: %+v", got)
	}
	if len(e.notices(t, ev.ID)) != 0 {
		t.Fatal("ilan edilmeden bildirim gitti")
	}

	base := e.changes(t, ev.ID, key)
	got, err = e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, key), nil)
	must(t, err)
	if !got.Announced || !got.AnnouncedAt.Equal(e.clock.now()) || !sameSet(users(got.Winners), a, b) {
		t.Fatalf("ilan: %+v", got)
	}
	if n := e.changes(t, ev.ID, key); n != base+1 {
		t.Fatalf("canlı güncelleme: %d", n-base)
	}
	ns := e.notices(t, ev.ID)
	if len(ns) != 1 || ns[0].actor != organizer || ns[0].target != ev.ID || !sameSet(ns[0].recipients, a, b, c, bystander) {
		t.Fatalf("bildirim: %+v", ns)
	}
	// The notice names the event only: no winner, no content.
	for k := range ns[0].payload {
		if !slices.Contains([]string{"type", "target_type", "target_id", "recipient_ids", "tayfa_id"}, k) {
			t.Errorf("bildirimde fazla alan: %q", k)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2`, celebration.AuditAnnounced, ev.ID.PG()); n != 1 {
		t.Errorf("denetim kaydı: %d", n)
	}

	// Announcing again notifies no one and changes nothing.
	e.clock.set(ev.StartsAt.Add(2 * time.Hour))
	got, err = e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, key), nil)
	must(t, err)
	if !got.AnnouncedAt.Equal(ev.StartsAt.Add(time.Hour)) || len(e.notices(t, ev.ID)) != 1 || e.changes(t, ev.ID, key) != base+1 {
		t.Fatalf("yeniden ilan: %+v, %d bildirim", got, len(e.notices(t, ev.ID)))
	}

	// A correction of the source shows at once.
	_, err = e.rankings.SetEntry(ctx, rr, c, 58)
	must(t, err)
	if got, err := e.celebrations.Get(ctx, ref(bystander, tf, ev.ID, key)); err != nil || !sameSet(users(got.Winners), c) || !got.Announced {
		t.Fatalf("düzeltme: %+v %v", got, err)
	}

	// Frozen: read only.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, key), nil)
	wantErr(t, "donmuş", err, apperr.ErrComponentFrozen)
	if got, err := e.celebrations.Get(ctx, ref(organizer, tf, ev.ID, key)); err != nil || got.CallerCanAnnounce || !got.Announced {
		t.Fatalf("donmuş okuma: %+v %v", got, err)
	}
}

// Winners chosen by hand: one to ten participants; announcing again
// replaces them without a second notice.
func TestCelebrationByHand(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	ev := e.newEvent(t, tf, organizer, plainType)
	const key = "celebration"
	a := e.going(t, tf, ev.ID, authz.Member)
	b := e.attended(t, tf, ev.ID, authz.Member)
	outsider := e.member(t, tf, authz.Member)
	r := ref(organizer, tf, ev.ID, key)
	e.clock.set(ev.StartsAt.Add(time.Hour))

	got, err := e.celebrations.Get(ctx, r)
	must(t, err)
	if got.Source != "" || got.SourceKind != "" || len(got.Winners) != 0 || got.Announced || !got.CallerCanAnnounce {
		t.Fatalf("başlangıç: %+v", got)
	}
	eleven := make([]id.ID, 0, celebration.MaxWinners+1)
	for range celebration.MaxWinners + 1 {
		eleven = append(eleven, e.going(t, tf, ev.ID, authz.Member))
	}
	for name, ws := range map[string][]id.ID{
		"kazanansız":    nil,
		"fazla kazanan": eleven,
		"aynı kişi":     {a, a},
		"boş kimlik":    {{}},
	} {
		_, err := e.celebrations.Announce(ctx, r, ws)
		wantField(t, name, err, "winner_ids")
	}
	for name, ws := range map[string][]id.ID{
		"katılımcı değil": {a, outsider},
		"üye değil":       {id.New()},
	} {
		_, err := e.celebrations.Announce(ctx, r, ws)
		wantErr(t, name, err, apperr.ErrCelebrationNotParticipant)
	}
	if n := e.count(t, `SELECT count(*) FROM event_celebrations WHERE event_id = $1`, ev.ID.PG()); n != 0 {
		t.Fatalf("reddedilen ilan yazıldı: %d", n)
	}

	base := e.changes(t, ev.ID, key)
	got, err = e.celebrations.Announce(ctx, r, eleven[:celebration.MaxWinners])
	must(t, err)
	if !got.Announced || !slices.Equal(users(got.Winners), eleven[:celebration.MaxWinners]) {
		t.Fatalf("on kazanan: %+v", got)
	}
	got, err = e.celebrations.Announce(ctx, r, []id.ID{b, a})
	must(t, err)
	if !slices.Equal(users(got.Winners), []id.ID{b, a}) {
		t.Fatalf("değişen kazananlar: %+v", got)
	}
	// The same winners again change nothing.
	_, err = e.celebrations.Announce(ctx, r, []id.ID{b, a})
	must(t, err)
	if n := e.changes(t, ev.ID, key); n != base+2 {
		t.Fatalf("canlı güncelleme: %d", n-base)
	}
	if ns := e.notices(t, ev.ID); len(ns) != 1 {
		t.Fatalf("bildirim sayısı: %d", len(ns))
	}
	if got, err := e.celebrations.Get(ctx, ref(outsider, tf, ev.ID, key)); err != nil || !slices.Equal(users(got.Winners), []id.ID{b, a}) || got.CallerCanAnnounce {
		t.Fatalf("üyenin okuması: %+v %v", got, err)
	}
	// The database keeps one row per person.
	_, err = e.pool.Exec(ctx, `INSERT INTO event_celebration_winners (id, event_id, component_key, user_id) VALUES ($1, $2, $3, $4)`,
		id.New().PG(), ev.ID.PG(), key, a.PG())
	if c := constraintOf(err); c != "event_celebration_winners_person" {
		t.Errorf("aynı kazanan: %q (%v)", c, err)
	}
}

// A scoreboard's guest player wins: the winner is the guest and the table;
// the notice still names no one.
func TestCelebrationScoreboardGuest(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	ev := e.newEvent(t, tf, organizer, gameType)
	const key = "celebration"
	a := e.going(t, tf, ev.ID, authz.Member)
	sr := ref(organizer, tf, ev.ID, "games")
	gs, err := e.guests.Add(ctx, sr, guest.Input{Nickname: "Kuzen"})
	must(t, err)
	g := gs.Guests[0].ID
	sb, err := e.scoreboards.CreateTable(ctx, sr, scoreboard.TableInput{Label: "Masa 1", Players: []scoreboard.PlayerInput{{User: a}, {Guest: g}}})
	must(t, err)
	tb := sb.Tables[0]
	e.clock.set(ev.StartsAt.Add(time.Hour))

	_, err = e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, key), nil)
	wantErr(t, "açık masa", err, apperr.ErrCelebrationNoWinner)
	// The guest scores more, whatever its seat.
	round := make([]scoreboard.ScoreInput, 0, len(tb.Players))
	for _, p := range tb.Players {
		pts := int32(3)
		if p.Guest == g {
			pts = 5
		}
		round = append(round, scoreboard.ScoreInput{Player: p.ID, Points: pts})
	}
	sb, err = e.scoreboards.AddRound(ctx, sr, tb.ID, round)
	must(t, err)
	if sb.Tables[0].Status != scoreboard.StatusFinished {
		_, err = e.scoreboards.FinishTable(ctx, sr, tb.ID)
		must(t, err)
	}
	got, err := e.celebrations.Announce(ctx, ref(organizer, tf, ev.ID, key), nil)
	must(t, err)
	if got.SourceKind != "scoreboard" || got.Source != "games" || len(got.Winners) != 1 ||
		got.Winners[0] != (celebration.Winner{Guest: g, Table: tb.ID}) {
		t.Fatalf("misafir kazanan: %+v", got)
	}
	ns := e.notices(t, ev.ID)
	if len(ns) != 1 || !sameSet(ns[0].recipients, a) {
		t.Fatalf("bildirim: %+v", ns)
	}
	if _, ok := ns[0].payload["guest_id"]; ok {
		t.Fatal("bildirimde misafir")
	}

	// Load is what other readers use.
	r, err := celebrationLoad(e, ev.ID)
	must(t, err)
	if !r.Announced || len(r.Winners) != 1 || r.Winners[0].Guest != g {
		t.Fatalf("Load: %+v", r)
	}
}

// celebrationLoad reads gameType's celebration with Load, as the badge job
// and the tayfa page would.
func celebrationLoad(e *env, ev id.ID) (celebration.Result, error) {
	var out celebration.Result
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT definition FROM catalog_event_type_versions WHERE type_key = $1 AND version = 1`, gameType).Scan(&raw); err != nil {
		return out, err
	}
	def, err := definition.Parse(raw)
	if err != nil {
		return out, err
	}
	comp, _ := def.Component("celebration")
	return celebration.Load(ctx, q, ev, def, comp)
}

// Nothing the source hides is celebrated: a ranking of a sensitive measure,
// and a type whose attendance is sensitive.
func TestCelebrationHidden(t *testing.T) {
	e := setup(t)
	tf, organizer := e.newTayfa(t)
	exam := e.newEvent(t, tf, organizer, examType)
	blood := e.newEvent(t, tf, organizer, bloodType)
	a := e.attended(t, tf, exam.ID, authz.Member)
	donor := e.going(t, tf, blood.ID, authz.Member)
	e.clock.set(exam.StartsAt.Add(time.Hour))
	_, err := e.measures.SetEntry(ctx, ref(a, tf, exam.ID, "net"), a, 80)
	must(t, err)

	for name, r := range map[string]component.Ref{
		"hassas ölçüm":   ref(organizer, tf, exam.ID, "celebration"),
		"hassas katılım": ref(organizer, tf, blood.ID, "celebration"),
	} {
		got, err := e.celebrations.Get(ctx, r)
		if err != nil || !got.Hidden || len(got.Winners) != 0 {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	_, err = e.celebrations.Announce(ctx, ref(organizer, tf, exam.ID, "celebration"), nil)
	wantErr(t, "hassas ölçüm", err, apperr.ErrCelebrationNoWinner)
	_, err = e.celebrations.Announce(ctx, ref(organizer, tf, blood.ID, "celebration"), []id.ID{donor})
	wantErr(t, "hassas katılım", err, apperr.ErrCelebrationNoWinner)
	if n := e.count(t, `SELECT count(*) FROM event_celebrations`); n != 0 {
		t.Fatalf("gizli kutlama ilan edildi: %d", n)
	}
}

func TestCelebrationOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral)
	race := e.newEvent(t, tf, admin, raceType)
	off := e.newEvent(t, tf, admin, optType)
	on1 := e.newEvent(t, tf, admin, optType,
		component.Selection{Key: "ranking", Enabled: on(true)}, component.Selection{Key: "cheer", Enabled: on(true)})
	runner := e.going(t, tf, on1.ID, authz.Member)
	e.clock.set(on1.StartsAt.Add(time.Hour))

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":          ref(admin, tf, general.ID, "celebration"),
		"tanımda olmayan anahtar": ref(admin, tf, race.ID, "cheer"),
		"başka türün bileşeni":    ref(admin, tf, race.ID, "ranking"),
	} {
		_, err := e.celebrations.Get(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.celebrations.Announce(ctx, r, nil)
		wantErr(t, name+" ilan", err, apperr.ErrComponentNotFound)
	}
	_, err := e.celebrations.Get(ctx, ref(admin, tf, off.ID, "cheer"))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.celebrations.Announce(ctx, ref(admin, tf, off.ID, "cheer"), []id.ID{admin})
	wantErr(t, "kapalı bileşende ilan", err, apperr.ErrComponentDisabled)

	// The source is off: no winner.
	if got, err := e.celebrations.Get(ctx, ref(admin, tf, off.ID, "celebration")); err != nil || len(got.Winners) != 0 || got.Hidden {
		t.Fatalf("kapalı kaynak: %+v %v", got, err)
	}
	// Switched on, it counts; so does the optional celebration.
	_, err = e.rankings.SetEntry(ctx, ref(admin, tf, on1.ID, "ranking"), runner, 1)
	must(t, err)
	if got, err := e.celebrations.Announce(ctx, ref(admin, tf, on1.ID, "celebration"), nil); err != nil || !slices.Equal(users(got.Winners), []id.ID{runner}) {
		t.Fatalf("açık kaynak: %+v %v", got, err)
	}
	if got, err := e.celebrations.Announce(ctx, ref(admin, tf, on1.ID, "cheer"), []id.ID{runner}); err != nil || !got.Announced {
		t.Fatalf("açılan kutlama: %+v %v", got, err)
	}

	// The type's next version drops the celebration: an event of the new
	// version has none.
	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, optType, optV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, optType, optV2)
	newer := e.newEvent(t, tf, admin, optType)
	if newer.TypeVersion != 2 || on1.TypeVersion != 1 {
		t.Fatalf("tür sürümleri: eski %d, yeni %d", on1.TypeVersion, newer.TypeVersion)
	}
	e.clock.set(newer.StartsAt.Add(time.Hour))
	_, err = e.celebrations.Announce(ctx, ref(admin, tf, newer.ID, "cheer"), []id.ID{admin})
	wantErr(t, "yeni sürümde olmayan kutlama", err, apperr.ErrComponentNotFound)

	// The trigger refuses writes that go around the service.
	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "yeni sürüm": newer.ID, "kapalı bileşen": off.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_celebrations (event_id, component_key, announced_by, announced_at, updated_at) VALUES ($1, 'cheer', $2, now(), now())`,
			ev.PG(), admin.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
}

// Who may announce for every role; IDOR; a cancelled event takes nothing.
func TestCelebrationPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, plainType)
	cancelled := e.newEvent(t, tf, admin, plainType)
	_, err := e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	const key = "celebration"
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
	own := e.newEvent(t, tf, admin, plainType)
	e.exec(t, `UPDATE events SET created_by = $2 WHERE id = $1`, own.ID.PG(), organizer.PG())
	winner := e.going(t, tf, ev.ID, authz.Member)
	_, err = e.events.SetRSVP(ctx, winner, tf, own.ID, event.RSVPGoing)
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
		{"kendi etkinliğinin organizatörü", organizer, own.ID, nil},
		{"başkasının etkinliğinde organizatör", organizer, ev.ID, apperr.ErrPermissionDenied},
		{"atanmış hakem", referee, ev.ID, apperr.ErrPermissionDenied},
		{"kazanan üye", winner, ev.ID, apperr.ErrPermissionDenied},
		{"üye", member, ev.ID, apperr.ErrPermissionDenied},
		{"susturulmuş üye", muted, ev.ID, apperr.ErrPermissionDenied},
		{"üye değil", id.New(), ev.ID, apperr.ErrTayfaNotFound},
	} {
		_, err := e.celebrations.Announce(ctx, ref(tc.user, tf, tc.ev, key), []id.ID{winner})
		if tc.want == nil {
			must(t, err)
		} else if ok := errors.Is(err, tc.want) || (tc.user == muted && errors.Is(err, apperr.ErrMuted)); !ok {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
		got, err := e.celebrations.Get(ctx, ref(tc.user, tf, tc.ev, key))
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			wantErr(t, tc.name+" okuma", err, tc.want)
			continue
		}
		// A mute does not hide the celebration.
		must(t, err)
		if got.CallerCanAnnounce != (tc.want == nil) {
			t.Errorf("%s: caller_can_announce = %v", tc.name, got.CallerCanAnnounce)
		}
	}
	// One notice per celebration, whoever announced first.
	if ns := e.notices(t, ev.ID); len(ns) != 1 || ns[0].actor != admin {
		t.Fatalf("bildirim: %+v", ns)
	}

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err = e.celebrations.Get(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.celebrations.Announce(ctx, ref(admin2, tf2, ev.ID, key), []id.ID{winner})
	wantErr(t, "başka tayfa ilan", err, apperr.ErrEventNotFound)

	_, err = e.celebrations.Announce(ctx, ref(admin, tf, cancelled.ID, key), []id.ID{winner})
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
	if got, err := e.celebrations.Get(ctx, ref(admin, tf, cancelled.ID, key)); err != nil || got.CallerCanAnnounce {
		t.Fatalf("iptal edilmiş etkinlikte ilan izni: %+v %v", got, err)
	}
}
