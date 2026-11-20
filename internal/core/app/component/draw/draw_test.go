package draw_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/draw"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/teams"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types (Notion Faz 3 kataloğu: satranç, espor): chessType a draw of
// the participants, and a second version without it; esportsType teams set
// by hand and a draw of the teams; optTeamsType a teams component that is
// off by default; optType an optional draw that is off by default.
const (
	chessType    = "test_chess"
	chessV1      = `{"components":[{"kind":"draw"}]}`
	chessV2      = `{"components":[{"kind":"meeting_point"}]}`
	esportsType  = "test_esports"
	esportsV1    = `{"components":[{"kind":"teams","params":{"mode":"manual"}},{"kind":"draw"}]}`
	optTeamsType = "test_opt_teams"
	optTeamsV1   = `{"components":[{"kind":"teams","optional":{"default":false}},{"kind":"draw"}]}`
	optType      = "test_opt"
	optV1        = `{"components":[{"kind":"draw","optional":{"default":false}}]}`
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
	draws  *draw.Service
	teams  *teams.Service
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

func pgErr(err error) (code, constraint string) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code, pg.ConstraintName
	}
	return "", ""
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
	e := &env{draws: draw.New(fw), teams: teams.New(fw), events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	for key, def := range map[string]string{chessType: chessV1, esportsType: esportsV1, optTeamsType: optTeamsV1, optType: optV1} {
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
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Kura Tayfası", ClientRequestID: id.New().String()})
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

// going adds n members who answered "going".
func (e *env) going(t *testing.T, tf, ev id.ID, n int) []id.ID {
	t.Helper()
	out := make([]id.ID, 0, n)
	for range n {
		u := e.member(t, tf, authz.Member)
		_, err := e.events.SetRSVP(ctx, u, tf, ev, event.RSVPGoing)
		must(t, err)
		out = append(out, u)
	}
	return out
}

// newEvent creates an event of typ that starts in a day and lasts three
// hours.
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string, sels ...component.Selection) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Turnuva", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
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

// users returns the people of a draw in order; it fails on a gap in the
// positions.
func users(t *testing.T, d draw.Draw) []id.ID {
	t.Helper()
	out := make([]id.ID, 0, len(d.Entries))
	for i, en := range d.Entries {
		if en.Position != int32(i+1) || en.Team != (id.ID{}) {
			t.Fatalf("kura yeri: %+v", d.Entries)
		}
		out = append(out, en.User)
	}
	return out
}

func sorted(ids []id.ID) []id.ID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b id.ID) int { return strings.Compare(a.String(), b.String()) })
	return out
}

// Shuffle permutes with crypto/rand: every element stays, and the order
// changes.
func TestShuffle(t *testing.T) {
	src := make([]int, 20)
	for i := range src {
		src[i] = i
	}
	moved := false
	for range 5 {
		s := slices.Clone(src)
		must(t, draw.Shuffle(s))
		got := slices.Clone(s)
		slices.Sort(got)
		if !slices.Equal(got, src) {
			t.Fatalf("karıştırma eleman kaybetti: %v", s)
		}
		moved = moved || !slices.Equal(s, src)
	}
	if !moved {
		t.Fatal("beş karıştırmada da sıra değişmedi")
	}
	for _, s := range [][]int{nil, {7}} {
		must(t, draw.Shuffle(s))
	}
}

// A participant draw: everyone once, fixed, audited and live; no second
// draw.
func TestDrawParticipants(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, chessType)
	member := e.member(t, tf, authz.Member)
	const key = "draw"

	before, err := e.draws.Get(ctx, ref(member, tf, ev.ID, key))
	must(t, err)
	if before.Drawn || before.Subject != draw.SubjectParticipants || before.TeamsKey != "" || len(before.Entries) != 0 || before.CallerCanDraw {
		t.Fatalf("çekilmemiş kura: %+v", before)
	}
	if got, err := e.draws.Get(ctx, ref(admin, tf, ev.ID, key)); err != nil || !got.CallerCanDraw {
		t.Fatalf("yönetici çekebilmeli: %+v %v", got, err)
	}

	players := e.going(t, tf, ev.ID, 5)
	// Someone who answered "maybe" is not drawn.
	maybe := e.member(t, tf, authz.Member)
	_, err = e.events.SetRSVP(ctx, maybe, tf, ev.ID, event.RSVPMaybe)
	must(t, err)

	got, err := e.draws.Make(ctx, ref(admin, tf, ev.ID, key))
	must(t, err)
	if !got.Drawn || got.Subject != draw.SubjectParticipants || got.CallerCanDraw || !got.DrawnAt.Equal(e.clock.now()) {
		t.Fatalf("çekilen kura: %+v", got)
	}
	order := users(t, got)
	if !slices.Equal(sorted(order), sorted(players)) {
		t.Fatalf("kuradakiler: %v, katılımcılar %v", order, players)
	}
	if n := e.changes(t, ev.ID, key); n != 1 {
		t.Errorf("canlı olay: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND actor_id = $3
		AND metadata ->> 'subject' = 'participants' AND (metadata ->> 'entries')::int = 5`, draw.AuditDrawn, ev.ID.PG(), admin.PG()); n != 1 {
		t.Errorf("denetim kaydı: %d", n)
	}

	// The result is fixed: no second draw, and everyone sees the same order.
	_, err = e.draws.Make(ctx, ref(admin, tf, ev.ID, key))
	wantErr(t, "ikinci kura", err, apperr.ErrDrawAlreadyMade)
	again, err := e.draws.Get(ctx, ref(member, tf, ev.ID, key))
	must(t, err)
	if !slices.Equal(users(t, again), order) || !again.DrawnAt.Equal(got.DrawnAt) {
		t.Fatalf("kura değişti: %v, %v", users(t, again), order)
	}
	if n := e.changes(t, ev.ID, key); n != 1 {
		t.Errorf("reddedilen kurada canlı olay: %d", n)
	}

	// Load (for the bracket) reads the same; someone who left the tayfa is
	// an empty place in the same position.
	e.exec(t, `DELETE FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), order[2].PG())
	r, ok, err := draw.Load(ctx, store.New(e.pool), ev.ID, key)
	must(t, err)
	if !ok || len(r.Entries) != 5 || r.Entries[2].User != (id.ID{}) || r.Entries[2].Position != 3 || r.Entries[3].User != order[3] {
		t.Fatalf("Load: %+v %v", r, ok)
	}
	if _, ok, err := draw.Load(ctx, store.New(e.pool), id.New(), key); err != nil || ok {
		t.Fatalf("kurasız etkinlik: %v %v", ok, err)
	}
}

// A team draw takes the teams with people of the type's teams component;
// a removed team leaves its place empty.
func TestDrawTeams(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, esportsType)
	p := e.going(t, tf, ev.ID, 4)

	got, err := e.draws.Get(ctx, ref(admin, tf, ev.ID, "draw"))
	must(t, err)
	if got.Subject != draw.SubjectTeams || got.TeamsKey != "teams" {
		t.Fatalf("takım kurası olmalı: %+v", got)
	}
	_, err = e.draws.Make(ctx, ref(admin, tf, ev.ID, "draw"))
	wantErr(t, "takımsız", err, apperr.ErrDrawTooFew)

	formed, err := e.teams.Replace(ctx, ref(admin, tf, ev.ID, "teams"), []teams.Input{
		{Members: p[:1]}, {Members: p[1:2]}, {Members: nil}, {Members: p[2:]},
	})
	must(t, err)
	got, err = e.draws.Make(ctx, ref(admin, tf, ev.ID, "draw"))
	must(t, err)
	var drawn []id.ID
	for i, en := range got.Entries {
		if en.Position != int32(i+1) || en.User != (id.ID{}) || en.Team == (id.ID{}) {
			t.Fatalf("takım yeri: %+v", got.Entries)
		}
		drawn = append(drawn, en.Team)
	}
	want := []id.ID{formed.Teams[0].ID, formed.Teams[1].ID, formed.Teams[3].ID}
	if got.Subject != draw.SubjectTeams || got.TeamsKey != "teams" || !slices.Equal(sorted(drawn), sorted(want)) {
		t.Fatalf("takım kurası: %+v", got)
	}

	// Teams formed again: the fourth team goes, and its place empties.
	_, err = e.teams.Replace(ctx, ref(admin, tf, ev.ID, "teams"), []teams.Input{{Members: p[:2]}, {Members: p[2:]}})
	must(t, err)
	after, err := e.draws.Get(ctx, ref(admin, tf, ev.ID, "draw"))
	must(t, err)
	gone := slices.Index(drawn, formed.Teams[3].ID)
	if gone < 0 || len(after.Entries) != len(drawn) {
		t.Fatalf("kura yerleri: %+v", after.Entries)
	}
	for i, en := range after.Entries {
		wantTeam := drawn[i]
		if i == gone {
			wantTeam = id.ID{}
		}
		if en.Team != wantTeam || en.Position != int32(i+1) {
			t.Fatalf("yer %d: %v, %v bekleniyordu", i+1, en.Team, wantTeam)
		}
	}

	// With the teams component off, the participants are drawn.
	off := e.newEvent(t, tf, admin, optTeamsType)
	e.going(t, tf, off.ID, 2)
	if got, err := e.draws.Make(ctx, ref(admin, tf, off.ID, "draw")); err != nil || got.Subject != draw.SubjectParticipants || len(got.Entries) != 2 {
		t.Fatalf("takım bileşeni kapalı: %+v %v", got, err)
	}
	// Switched on, the teams are.
	onTeams := e.newEvent(t, tf, admin, optTeamsType, component.Selection{Key: "teams", Enabled: on(true)})
	if got, err := e.draws.Get(ctx, ref(admin, tf, onTeams.ID, "draw")); err != nil || got.Subject != draw.SubjectTeams {
		t.Fatalf("takım bileşeni açık: %+v %v", got, err)
	}
}

// Too few to draw, and too many.
func TestDrawTooFewAndLimit(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, chessType)
	_, err := e.draws.Make(ctx, ref(admin, tf, ev.ID, "draw"))
	wantErr(t, "katılımcısız", err, apperr.ErrDrawTooFew)
	e.going(t, tf, ev.ID, 1)
	_, err = e.draws.Make(ctx, ref(admin, tf, ev.ID, "draw"))
	wantErr(t, "tek katılımcı", err, apperr.ErrDrawTooFew)
	if n := e.count(t, `SELECT count(*) FROM event_draws WHERE event_id = $1`, ev.ID.PG()); n != 0 {
		t.Fatalf("reddedilen kura yazıldı: %d", n)
	}

	big := e.newEvent(t, tf, admin, chessType)
	e.exec(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at)
		SELECT $1, gen_random_uuid(), 'member', 'active', 'code', now() FROM generate_series(1, $2::int)`, tf.PG(), draw.MaxEntries+1)
	e.exec(t, `INSERT INTO event_rsvps (event_id, user_id, status)
		SELECT $1, user_id, 'going' FROM memberships WHERE tayfa_id = $2 AND role = 'member'`, big.ID.PG(), tf.PG())
	_, err = e.draws.Make(ctx, ref(admin, tf, big.ID, "draw"))
	wantErr(t, "500'den çok", err, apperr.ErrEventLimit)
}

// The database keeps the result fixed: core_app cannot change or delete a
// place, and the trigger lets a place only empty, for the owner too.
func TestDrawFixedInDatabase(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, chessType)
	p := e.going(t, tf, ev.ID, 3)
	_, err := e.draws.Make(ctx, ref(admin, tf, ev.ID, "draw"))
	must(t, err)
	other := id.New()

	for name, sql := range map[string]string{
		"sıra":       `UPDATE event_draw_entries SET position = position + 10 WHERE event_id = $1`,
		"silme":      `DELETE FROM event_draw_entries WHERE event_id = $1`,
		"kura":       `UPDATE event_draws SET drawn_at = now() WHERE event_id = $1`,
		"kura silme": `DELETE FROM event_draws WHERE event_id = $1`,
	} {
		_, err := e.pool.Exec(ctx, sql, ev.ID.PG())
		if code, _ := pgErr(err); code != "42501" {
			t.Errorf("core_app %s: %v", name, err)
		}
	}
	_, err = e.pool.Exec(ctx, `UPDATE event_draw_entries SET user_id = $2 WHERE event_id = $1 AND position = 1`, ev.ID.PG(), other.PG())
	if _, c := pgErr(err); c != "event_draw_entries_fixed" {
		t.Errorf("başka kişiye geçirme: %v", err)
	}
	_, err = e.admin.Exec(ctx, `UPDATE event_draw_entries SET position = 9 WHERE event_id = $1 AND position = 1`, ev.ID.PG())
	if _, c := pgErr(err); c != "event_draw_entries_fixed" {
		t.Errorf("sahip rolüyle sıra değiştirme: %v", err)
	}
	// A second draw row cannot be written either.
	_, err = e.pool.Exec(ctx, `INSERT INTO event_draws (event_id, component_key, subject, drawn_at) VALUES ($1, 'draw', 'participants', now())`, ev.ID.PG())
	if code, _ := pgErr(err); code != "23505" {
		t.Errorf("ikinci kura satırı: %v", err)
	}
	// A place belongs to a person or a team, never both.
	_, err = e.admin.Exec(ctx, `INSERT INTO event_draw_entries (id, event_id, component_key, position, user_id, team_key) VALUES (gen_random_uuid(), $1, 'draw', 99, $2, 'teams')`, ev.ID.PG(), other.PG())
	if _, c := pgErr(err); c != "event_draw_entries_one" {
		t.Errorf("hem kişi hem takım: %v", err)
	}
	// Emptying a place is allowed (account deletion).
	tag, err := e.pool.Exec(ctx, `UPDATE event_draw_entries SET user_id = NULL WHERE user_id = $1`, p[0].PG())
	must(t, err)
	if tag.RowsAffected() != 1 {
		t.Fatalf("yer boşalmadı: %d", tag.RowsAffected())
	}
}

func TestDrawOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral)
	chess := e.newEvent(t, tf, admin, chessType)
	off := e.newEvent(t, tf, admin, optType)
	extra := e.newEvent(t, tf, admin, optType, component.Selection{Key: "draw", Enabled: on(true)})

	for name, r := range map[string]component.Ref{
		"bileşensiz tür":          ref(admin, tf, general.ID, "draw"),
		"tanımda olmayan anahtar": ref(admin, tf, chess.ID, "lots"),
	} {
		_, err := e.draws.Get(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.draws.Make(ctx, r)
		wantErr(t, name+" çekme", err, apperr.ErrComponentNotFound)
	}
	_, err := e.draws.Get(ctx, ref(admin, tf, off.ID, "draw"))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)
	_, err = e.draws.Make(ctx, ref(admin, tf, off.ID, "draw"))
	wantErr(t, "kapalı bileşende çekme", err, apperr.ErrComponentDisabled)
	e.going(t, tf, extra.ID, 2)
	if _, err := e.draws.Make(ctx, ref(admin, tf, extra.ID, "draw")); err != nil {
		t.Fatalf("açılan bileşen: %v", err)
	}

	// The type's next version drops the draw: the old event keeps it, an
	// event of the new version has none.
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, chessType, chessV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, chessType, chessV2)
	newer := e.newEvent(t, tf, admin, chessType)
	if newer.TypeVersion != 2 || chess.TypeVersion != 1 {
		t.Fatalf("tür sürümleri: eski %d, yeni %d", chess.TypeVersion, newer.TypeVersion)
	}
	e.going(t, tf, newer.ID, 2)
	_, err = e.draws.Make(ctx, ref(admin, tf, newer.ID, "draw"))
	wantErr(t, "yeni sürümde olmayan kura", err, apperr.ErrComponentNotFound)
	e.going(t, tf, chess.ID, 2)
	if got, err := e.draws.Make(ctx, ref(admin, tf, chess.ID, "draw")); err != nil || len(got.Entries) != 2 {
		t.Fatalf("eski sürümün etkinliği: %+v %v", got, err)
	}

	// The trigger refuses writes that go around the service.
	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "yeni sürüm": newer.ID, "kapalı bileşen": off.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_draws (event_id, component_key, subject, drawn_at) VALUES ($1, 'draw', 'participants', now())`, ev.PG())
		if _, c := pgErr(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
}

// Who may do what for every role; IDOR; a cancelled or frozen event takes
// nothing, and reads stay open.
func TestDrawPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	const key = "draw"
	organizer := e.member(t, tf, authz.Member)
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	muted := e.member(t, tf, authz.Member)
	e.mute(t, tf, muted)

	for _, tc := range []struct {
		name  string
		user  id.ID
		own   bool
		allow bool
		want  error
	}{
		{"yönetici", admin, false, true, nil},
		{"moderatör", moderator, false, true, nil},
		{"kendi etkinliğinin organizatörü", organizer, true, true, nil},
		{"başkasının etkinliğinde organizatör", organizer, false, false, apperr.ErrPermissionDenied},
		{"üye", member, false, false, apperr.ErrPermissionDenied},
		{"susturulmuş üye", muted, false, false, apperr.ErrPermissionDenied},
		{"üye değil", id.New(), false, false, apperr.ErrTayfaNotFound},
	} {
		ev := e.newEvent(t, tf, admin, chessType)
		if tc.own {
			// Members may not create events by default: the organizer is
			// written directly.
			e.exec(t, `UPDATE events SET created_by = $2 WHERE id = $1`, ev.ID.PG(), organizer.PG())
		}
		e.going(t, tf, ev.ID, 2)
		r := ref(tc.user, tf, ev.ID, key)
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			_, err := e.draws.Get(ctx, r)
			wantErr(t, tc.name+" okuma", err, tc.want)
			_, err = e.draws.Make(ctx, r)
			wantErr(t, tc.name+" çekme", err, tc.want)
			continue
		}
		// A mute does not hide the draw.
		got, err := e.draws.Get(ctx, r)
		must(t, err)
		if got.CallerCanDraw != tc.allow {
			t.Errorf("%s: caller_can_draw = %v", tc.name, got.CallerCanDraw)
		}
		_, err = e.draws.Make(ctx, r)
		switch {
		case tc.want == nil:
			must(t, err)
		case !errors.Is(err, tc.want) && !(tc.user == muted && errors.Is(err, apperr.ErrMuted)):
			t.Errorf("%s çekme: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}

	// Another tayfa's event does not exist through this tayfa.
	ev := e.newEvent(t, tf, admin, chessType)
	e.going(t, tf, ev.ID, 2)
	tf2, admin2 := e.newTayfa(t)
	_, err := e.draws.Get(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.draws.Make(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa çekme", err, apperr.ErrEventNotFound)

	// After the event the draw can still be made, until the write window
	// closes.
	late := e.newEvent(t, tf, admin, chessType)
	e.going(t, tf, late.ID, 2)
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.draws.Make(ctx, ref(admin, tf, ev.ID, key))
	wantErr(t, "donmuş çekme", err, apperr.ErrComponentFrozen)
	if got, err := e.draws.Get(ctx, ref(member, tf, ev.ID, key)); err != nil || got.CallerCanDraw || got.Drawn {
		t.Fatalf("donmuş okuma: %+v %v", got, err)
	}
	e.clock.set(late.EndsAt.Add(time.Hour))
	_, err = e.draws.Make(ctx, ref(admin, tf, late.ID, key))
	must(t, err)

	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	cancelled := e.newEvent(t, tf, admin, chessType)
	e.going(t, tf, cancelled.ID, 2)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.draws.Make(ctx, ref(admin, tf, cancelled.ID, key))
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
	if got, err := e.draws.Get(ctx, ref(admin, tf, cancelled.ID, key)); err != nil || got.CallerCanDraw {
		t.Fatalf("iptal edilmiş etkinlikte çekme izni: %+v %v", got, err)
	}
}

// A person's place is exported (ExportDrawEntries): only theirs, never the
// others in the draw.
func TestDrawExport(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, chessType)
	p := e.going(t, tf, ev.ID, 2)
	got, err := e.draws.Make(ctx, ref(admin, tf, ev.ID, "draw"))
	must(t, err)
	place := slices.IndexFunc(got.Entries, func(en draw.Entry) bool { return en.User == p[0] }) + 1
	rows, err := store.New(e.pool).ExportDrawEntries(ctx, store.ExportDrawEntriesParams{UserID: p[0].PG(), After: pgtype.UUID{Bytes: [16]byte{}, Valid: true}, N: 10})
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("dışa aktarma: %d satır", len(rows))
	}
	data := string(rows[0].Data)
	if !strings.Contains(data, ev.ID.String()) || !strings.Contains(data, `"position": `+strconv.Itoa(place)) || strings.Contains(data, p[1].String()) {
		t.Fatalf("dışa aktarılan: %s", data)
	}
}
