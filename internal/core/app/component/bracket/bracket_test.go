package bracket_test

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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/bracket"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/draw"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types (Notion Faz 3 kataloğu): chessType a draw, a bracket whose
// format the organizer chooses (Swiss by default) and a wdl scoreboard,
// and a second version without the bracket; racketType a best-of-3 points
// league; noDrawType a bracket without a draw; optType an optional bracket
// that is off by default.
const (
	chessType  = "test_chess"
	chessV1    = `{"components":[{"kind":"draw"},{"kind":"bracket","params":{"format":{"organizer_choice":["elimination","league","swiss"],"default":"swiss"}}},{"kind":"scoreboard","params":{"result":"wdl"}}]}`
	chessV2    = `{"components":[{"kind":"draw"}]}`
	racketType = "test_racket"
	racketV1   = `{"components":[{"kind":"draw"},{"kind":"bracket","params":{"format":"league","best_of":3}},{"kind":"scoreboard"}]}`
	noDrawType = "test_no_draw"
	noDrawV1   = `{"components":[{"kind":"bracket"}]}`
	optType    = "test_opt"
	optV1      = `{"components":[{"kind":"draw"},{"kind":"bracket","optional":{"default":false}}]}`
	key        = "bracket"
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
	br     *bracket.Service
	draws  *draw.Service
	fw     *component.Service
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
	e := &env{br: bracket.New(fw), draws: draw.New(fw), fw: fw, events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	for k, def := range map[string]string{chessType: chessV1, racketType: racketV1, noDrawType: noDrawV1, optType: optV1} {
		e.addType(t, k, def)
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
func (e *env) addType(t *testing.T, k, def string) {
	t.Helper()
	e.exec(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
		VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, k, def)
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, k)
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
// hours; format, if given, is the organizer's choice.
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ, format string) event.Event {
	t.Helper()
	var sels []component.Selection
	if format != "" {
		sels = []component.Selection{{Key: key, Choices: map[string]string{"format": format}}}
	}
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Turnuva", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(), Components: sels,
	})
	must(t, err)
	return v
}

// tournament creates an event with n participants and its draw made.
func (e *env) tournament(t *testing.T, tf, admin id.ID, typ, format string, n int) (event.Event, []id.ID) {
	t.Helper()
	ev := e.newEvent(t, tf, admin, typ, format)
	players := e.going(t, tf, ev.ID, n)
	_, err := e.draws.Make(ctx, ref(admin, tf, ev.ID, "draw"))
	must(t, err)
	return ev, players
}

// start moves the clock into the event.
func (e *env) start(ev event.Event) { e.clock.set(ev.StartsAt.Add(time.Minute)) }

func ref(user, tf, ev id.ID, k string) component.Ref {
	return component.Ref{User: user, Tayfa: tf, Event: ev, Key: k}
}

func (e *env) changes(t *testing.T, ev id.ID) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND subject_id = $2 AND payload ->> 'component_key' = $3`,
		component.EventChanged, ev.PG(), key)
}

// at returns the match of a round and slot.
func at(t *testing.T, b bracket.Bracket, round, slot int32) bracket.Match {
	t.Helper()
	for _, m := range b.Matches {
		if m.Round == round && m.Slot == slot {
			return m
		}
	}
	t.Fatalf("%d. turun %d. maçı yok: %+v", round, slot, b.Matches)
	return bracket.Match{}
}

func win() bracket.GameInput  { return bracket.GameInput{AOutcome: "win"} }
func loss() bracket.GameInput { return bracket.GameInput{AOutcome: "loss"} }
func tie() bracket.GameInput  { return bracket.GameInput{AOutcome: "draw"} }

// play enters a game in every undecided match of the current round: A
// wins.
func (e *env) play(t *testing.T, r component.Ref, b bracket.Bracket) bracket.Bracket {
	t.Helper()
	for _, m := range b.Matches {
		if m.Round == b.CurrentRound && !m.Decided {
			var err error
			b, err = e.br.AddGame(ctx, r, m.ID, win())
			must(t, err)
		}
	}
	return b
}

// An elimination: byes, a drawn match played on, rounds paired from the
// winners, games of a closed round fixed, the winner; live and audited.
func TestBracketElimination(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, chessType, bracket.FormatElimination)
	players := e.going(t, tf, ev.ID, 5)
	member := e.member(t, tf, authz.Member)
	r := ref(admin, tf, ev.ID, key)

	before, err := e.br.Get(ctx, ref(member, tf, ev.ID, key))
	must(t, err)
	if before.Started || before.Drawn || before.Format != bracket.FormatElimination || before.BestOf != 1 || before.Result.Result != "wdl" ||
		before.DrawKey != "draw" || before.CallerCanManage || len(before.Matches) != 0 {
		t.Fatalf("başlamamış: %+v", before)
	}
	_, err = e.br.Start(ctx, r)
	wantErr(t, "kurasız başlatma", err, apperr.ErrBracketDrawRequired)
	_, err = e.draws.Make(ctx, ref(admin, tf, ev.ID, "draw"))
	must(t, err)
	if got, err := e.br.Get(ctx, r); err != nil || !got.Drawn || got.Started || len(got.Entrants) != 5 || !got.CallerCanManage {
		t.Fatalf("kura çekildi: %+v %v", got, err)
	}

	b, err := e.br.Start(ctx, r)
	must(t, err)
	if !b.Started || b.Rounds != 3 || b.CurrentRound != 1 || len(b.Matches) != 3 || b.Finished || !b.StartedAt.Equal(e.clock.now()) {
		t.Fatalf("başlayan eleme: %+v", b)
	}
	if m := at(t, b, 1, 1); m.A != 1 || m.B != 2 || m.Decided || m.CallerCanScore {
		t.Fatalf("ilk maç (etkinlik başlamadı): %+v", m)
	}
	bye := at(t, b, 1, 3)
	if bye.A != 5 || bye.B != 0 || !bye.Bye || !bye.Decided || bye.Winner != 5 {
		t.Fatalf("bay: %+v", bye)
	}
	_, err = e.br.Start(ctx, r)
	wantErr(t, "ikinci başlatma", err, apperr.ErrBracketStarted)
	m1, m2 := at(t, b, 1, 1), at(t, b, 1, 2)
	_, err = e.br.AddGame(ctx, r, m1.ID, win())
	wantErr(t, "başlamadan oyun", err, apperr.ErrScoreNotStarted)

	e.start(ev)
	b, err = e.br.Get(ctx, r)
	must(t, err)
	if !at(t, b, 1, 1).CallerCanScore || at(t, b, 1, 3).CallerCanScore {
		t.Fatalf("oyun girme izni: %+v", b.Matches)
	}
	// A drawn elimination match stays open; another game decides it.
	b, err = e.br.AddGame(ctx, r, m1.ID, tie())
	must(t, err)
	if m := at(t, b, 1, 1); m.Decided || m.ATotal != 0.5 || len(m.Games) != 1 || m.Games[0].AOutcome != "draw" {
		t.Fatalf("berabere eleme maçı: %+v", m)
	}
	_, err = e.br.PairRound(ctx, r)
	wantErr(t, "açık tur", err, apperr.ErrBracketRoundOpen)
	_, err = e.br.AddGame(ctx, r, m1.ID, bracket.GameInput{APoints: 3, BPoints: 1})
	wantField(t, "wdl'de puan", err, "a_outcome")
	_, err = e.br.AddGame(ctx, r, m1.ID, bracket.GameInput{AOutcome: "won"})
	wantField(t, "bozuk sonuç", err, "a_outcome")
	b, err = e.br.AddGame(ctx, r, m1.ID, loss())
	must(t, err)
	if m := at(t, b, 1, 1); !m.Decided || m.Winner != 2 {
		t.Fatalf("ek oyun: %+v", m)
	}
	_, err = e.br.AddGame(ctx, r, m1.ID, win())
	wantErr(t, "sonuçlanmış maç", err, apperr.ErrBracketMatchDecided)
	_, err = e.br.AddGame(ctx, r, bye.ID, win())
	wantErr(t, "bay maçı", err, apperr.ErrBracketMatchDecided)
	_, err = e.br.AddGame(ctx, r, id.New(), win())
	wantErr(t, "olmayan maç", err, apperr.ErrBracketMatchNotFound)
	b, err = e.br.AddGame(ctx, r, m2.ID, win())
	must(t, err)

	// Round 2 pairs the winners in order; the odd last has a bye.
	b, err = e.br.PairRound(ctx, r)
	must(t, err)
	if b.CurrentRound != 2 || len(b.Matches) != 5 {
		t.Fatalf("2. tur: %+v", b)
	}
	if m := at(t, b, 2, 1); m.A != 2 || m.B != 3 {
		t.Fatalf("2. turun maçı: %+v", m)
	}
	if m := at(t, b, 2, 2); m.A != 5 || m.B != 0 || !m.Decided {
		t.Fatalf("2. turun bayı: %+v", m)
	}
	// Round 1 is closed: its games no longer change.
	_, err = e.br.CorrectGame(ctx, r, m1.ID, 2, win())
	wantErr(t, "kapanmış tur düzeltme", err, apperr.ErrBracketRoundClosed)
	_, err = e.br.DeleteGame(ctx, r, m2.ID, 1)
	wantErr(t, "kapanmış tur silme", err, apperr.ErrBracketRoundClosed)
	// A correction in the current round.
	semi := at(t, b, 2, 1)
	b, err = e.br.AddGame(ctx, r, semi.ID, win())
	must(t, err)
	b, err = e.br.CorrectGame(ctx, r, semi.ID, 1, loss())
	must(t, err)
	if m := at(t, b, 2, 1); m.Winner != 3 || len(m.Games) != 1 {
		t.Fatalf("düzeltme: %+v", m)
	}
	_, err = e.br.CorrectGame(ctx, r, semi.ID, 2, loss())
	wantErr(t, "girilmemiş oyun", err, apperr.ErrBracketGameNotFound)

	b, err = e.br.PairRound(ctx, r)
	must(t, err)
	final := at(t, b, 3, 1)
	if final.A != 3 || final.B != 5 || b.Finished {
		t.Fatalf("final: %+v", final)
	}
	b, err = e.br.AddGame(ctx, r, final.ID, loss())
	must(t, err)
	if !b.Finished || len(b.Winners) != 1 || b.Winners[0] != 5 || len(b.Standings) != 0 {
		t.Fatalf("şampiyon: %+v", b)
	}
	_, err = e.br.PairRound(ctx, r)
	wantErr(t, "biten eleme", err, apperr.ErrBracketFinished)

	// Load (for the celebration) reads the same; the champion is the
	// draw's fifth place.
	res, ok, err := bracket.Load(ctx, store.New(e.pool), ev.ID, key)
	must(t, err)
	champions := res.Champions()
	if !ok || !res.Finished || len(champions) != 1 || champions[0].Position != 5 {
		t.Fatalf("Load: %+v", res)
	}
	d, _, err := draw.Load(ctx, store.New(e.pool), ev.ID, "draw")
	must(t, err)
	if champions[0].User != d.Entries[4].User || !slices.Contains(players, champions[0].User) {
		t.Fatalf("şampiyon kim: %v", champions[0].User)
	}
	// Start, five games, a correction and two pairings; refusals write
	// nothing.
	if n := e.changes(t, ev.ID); n != 9 {
		t.Errorf("canlı olay: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND target_id = $2 AND actor_id = $3
		AND metadata ->> 'format' = 'elimination' AND (metadata ->> 'entrants')::int = 5`, bracket.AuditStarted, ev.ID.PG(), admin.PG()); n != 1 {
		t.Errorf("denetim kaydı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action LIKE 'event.bracket_%' AND metadata::text LIKE '%outcome%'`, ev.ID.PG()); n != 0 {
		t.Errorf("denetim kaydında skor: %d", n)
	}

	// A reset deletes the matches and the match referees; the draw stays,
	// and the bracket starts again.
	referee := e.member(t, tf, authz.Member)
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: referee, ComponentKey: key, Scope: final.ID})
	must(t, err)
	b, err = e.br.Reset(ctx, r)
	must(t, err)
	if b.Started || !b.Drawn || len(b.Matches) != 0 || len(b.Entrants) != 5 {
		t.Fatalf("sıfırlanan: %+v", b)
	}
	if n := e.count(t, `SELECT count(*) FROM event_bracket_matches WHERE event_id = $1`, ev.ID.PG()) +
		e.count(t, `SELECT count(*) FROM event_bracket_games WHERE event_id = $1`, ev.ID.PG()) +
		e.count(t, `SELECT count(*) FROM event_referees WHERE event_id = $1 AND scope_id IS NOT NULL`, ev.ID.PG()); n != 0 {
		t.Fatalf("sıfırlamadan kalan: %d", n)
	}
	_, err = e.br.Reset(ctx, r)
	wantErr(t, "ikinci sıfırlama", err, apperr.ErrBracketNotStarted)
	_, err = e.br.PairRound(ctx, r)
	wantErr(t, "başlamamış tur", err, apperr.ErrBracketNotStarted)
	if b, err = e.br.Start(ctx, r); err != nil || len(b.Matches) != 3 {
		t.Fatalf("yeniden başlatma: %+v %v", b, err)
	}
}

// A league: every round paired at the start, best of three by points,
// standings, corrections in any round, only the last game deleted.
func TestBracketLeague(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev, _ := e.tournament(t, tf, admin, racketType, "", 4)
	r := ref(admin, tf, ev.ID, key)
	b, err := e.br.Start(ctx, r)
	must(t, err)
	if b.Format != bracket.FormatLeague || b.BestOf != 3 || b.Result.Result != "points" || b.Winner != "high" ||
		b.Rounds != 3 || b.CurrentRound != 3 || len(b.Matches) != 6 || len(b.Standings) != 4 {
		t.Fatalf("lig: %+v", b)
	}
	_, err = e.br.PairRound(ctx, r)
	wantErr(t, "ligde sonraki tur", err, apperr.ErrBracketFinished)

	e.start(ev)
	first := at(t, b, 1, 1)
	_, err = e.br.AddGame(ctx, r, first.ID, bracket.GameInput{AOutcome: "win"})
	wantField(t, "puanlı oyunda sonuç", err, "a_outcome")
	_, err = e.br.AddGame(ctx, r, first.ID, bracket.GameInput{APoints: bracket.PointsMax + 1})
	wantField(t, "çok büyük puan", err, "a_points")
	for _, g := range [][2]int32{{11, 5}, {7, 11}} {
		b, err = e.br.AddGame(ctx, r, first.ID, bracket.GameInput{APoints: g[0], BPoints: g[1]})
		must(t, err)
	}
	if m := at(t, b, 1, 1); m.Decided || m.AWon != 1 || m.BWon != 1 || m.ATotal != 18 {
		t.Fatalf("BO3 sürüyor: %+v", m)
	}
	_, err = e.br.DeleteGame(ctx, r, first.ID, 1)
	wantErr(t, "son olmayan oyun", err, apperr.ErrBracketGameNotFound)
	b, err = e.br.DeleteGame(ctx, r, first.ID, 2)
	must(t, err)
	if m := at(t, b, 1, 1); len(m.Games) != 1 {
		t.Fatalf("silinen oyun: %+v", m)
	}
	// A wins every match two games to nil.
	for _, m := range b.Matches {
		for len(m.Games) < 2 {
			b, err = e.br.AddGame(ctx, r, m.ID, bracket.GameInput{APoints: 11, BPoints: 3})
			must(t, err)
			m = at(t, b, m.Round, m.Slot)
		}
	}
	if !b.Finished || len(b.Winners) != 1 || b.Standings[0].Rank != 1 || b.Standings[0].Points < 2 {
		t.Fatalf("biten lig: %+v", b)
	}
	// A correction in round 1 after round 3 is allowed in a league.
	if b, err = e.br.CorrectGame(ctx, r, first.ID, 1, bracket.GameInput{APoints: 2, BPoints: 11}); err != nil {
		t.Fatalf("ligde düzeltme: %v", err)
	}
	if m := at(t, b, 1, 1); m.Decided || m.AWon != 1 || m.BWon != 1 {
		t.Fatalf("düzeltilen maç: %+v", m)
	}
	if b.Finished {
		t.Fatal("açık maçla lig bitmiş görünüyor")
	}
	_, err = e.br.AddGame(ctx, r, first.ID, bracket.GameInput{APoints: 0, BPoints: 0})
	must(t, err)
	b, err = e.br.Get(ctx, r)
	must(t, err)
	// 0–0 in the third game: one game each, the totals decide (2+11+0
	// against 11+3+0).
	if m := at(t, b, 1, 1); !m.Decided || m.Winner != m.B {
		t.Fatalf("üçüncü oyun: %+v", m)
	}
	// Nine games at most.
	for range bracket.MaxGames {
		_, _ = e.br.AddGame(ctx, r, first.ID, bracket.GameInput{})
	}
	if n := e.count(t, `SELECT count(*) FROM event_bracket_games WHERE match_id = $1`, first.ID.PG()); n != 3 {
		t.Fatalf("sonuçlanmış maça oyun eklendi: %d", n)
	}
}

// A Swiss bracket: ⌈log2 n⌉ rounds, a bye to a different entrant each
// round, no rematch, standings and winners.
func TestBracketSwiss(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev, _ := e.tournament(t, tf, admin, chessType, "", 5)
	r := ref(admin, tf, ev.ID, key)
	b, err := e.br.Start(ctx, r)
	must(t, err)
	if b.Format != bracket.FormatSwiss || b.Rounds != 3 || len(b.Matches) != 3 {
		t.Fatalf("İsviçre: %+v", b)
	}
	if m := at(t, b, 1, 3); m.A != 5 || !m.Bye {
		t.Fatalf("ilk turun bayı: %+v", m)
	}
	e.start(ev)
	byes := map[int32]bool{}
	met := map[[2]int32]bool{}
	for round := int32(1); ; round++ {
		b = e.play(t, r, b)
		for _, m := range b.Matches {
			if m.Round != round {
				continue
			}
			if m.Bye {
				if byes[m.Winner] {
					t.Fatalf("%d. turda ikinci bay: %d", round, m.Winner)
				}
				byes[m.Winner] = true
				continue
			}
			p := [2]int32{min(m.A, m.B), max(m.A, m.B)}
			if met[p] {
				t.Fatalf("%d. turda rövanş: %v", round, p)
			}
			met[p] = true
		}
		if round == 3 {
			break
		}
		if b.Finished {
			t.Fatalf("%d. turda bitti", round)
		}
		b, err = e.br.PairRound(ctx, r)
		must(t, err)
	}
	if !b.Finished || len(b.Winners) == 0 || len(byes) != 3 || len(b.Standings) != 5 {
		t.Fatalf("biten İsviçre: %+v", b)
	}
	firsts := 0
	for _, st := range b.Standings {
		if st.Rank == 1 {
			firsts++
			if !slices.Contains(b.Winners, st.Position) {
				t.Fatalf("birinci kazananlarda yok: %v %+v", b.Winners, b.Standings)
			}
		}
	}
	if firsts != len(b.Winners) {
		t.Fatalf("kazananlar: %v %+v", b.Winners, b.Standings)
	}
	_, err = e.br.PairRound(ctx, r)
	wantErr(t, "biten İsviçre", err, apperr.ErrBracketFinished)
}

// Starts that cannot be made, and the type version: an event of a type
// without the bracket, a switched-off one, a newer version without it.
func TestBracketStartRefusals(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)

	noDraw := e.newEvent(t, tf, admin, noDrawType, "")
	_, err := e.br.Start(ctx, ref(admin, tf, noDraw.ID, key))
	wantErr(t, "kurasız tür", err, apperr.ErrBracketDrawRequired)
	if got, err := e.br.Get(ctx, ref(admin, tf, noDraw.ID, key)); err != nil || got.DrawKey != "" || got.Drawn {
		t.Fatalf("kurasız tür okuma: %+v %v", got, err)
	}

	// Only one entrant left in the draw.
	few, players := e.tournament(t, tf, admin, chessType, "", 2)
	e.exec(t, `DELETE FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), players[0].PG())
	_, err = e.br.Start(ctx, ref(admin, tf, few.ID, key))
	wantErr(t, "tek katılımcı", err, apperr.ErrBracketTooFew)

	// A league of more than 20.
	big, _ := e.tournament(t, tf, admin, chessType, bracket.FormatLeague, bracket.MaxLeague+1)
	_, err = e.br.Start(ctx, ref(admin, tf, big.ID, key))
	wantErr(t, "büyük lig", err, apperr.ErrEventLimit)

	general := e.newEvent(t, tf, admin, event.TypeGeneral, "")
	_, err = e.br.Get(ctx, ref(admin, tf, general.ID, key))
	wantErr(t, "bileşensiz tür", err, apperr.ErrComponentNotFound)
	off := e.newEvent(t, tf, admin, optType, "")
	_, err = e.br.Start(ctx, ref(admin, tf, off.ID, key))
	wantErr(t, "kapalı bileşen", err, apperr.ErrComponentDisabled)

	chess, _ := e.tournament(t, tf, admin, chessType, "", 2)
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, chessType, chessV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, chessType, chessV2)
	newer := e.newEvent(t, tf, admin, chessType, "")
	e.going(t, tf, newer.ID, 2)
	_, err = e.draws.Make(ctx, ref(admin, tf, newer.ID, "draw"))
	must(t, err)
	_, err = e.br.Start(ctx, ref(admin, tf, newer.ID, key))
	wantErr(t, "yeni sürümde olmayan eşleşme ağacı", err, apperr.ErrComponentNotFound)
	if _, err := e.br.Start(ctx, ref(admin, tf, chess.ID, key)); err != nil {
		t.Fatalf("eski sürümün etkinliği: %v", err)
	}

	// The trigger refuses writes that go around the service.
	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "yeni sürüm": newer.ID, "kapalı bileşen": off.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_brackets (event_id, component_key, draw_key, format, best_of, result, winner, rounds, started_at)
			VALUES ($1, 'bracket', 'draw', 'swiss', 1, 'wdl', 'high', 1, now())`, ev.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}
	// A match side must be a place of the bracket's draw; a game is
	// points or an outcome.
	_, err = e.pool.Exec(ctx, `INSERT INTO event_bracket_matches (id, event_id, component_key, draw_key, round, slot, a_position, created_at)
		VALUES (gen_random_uuid(), $1, 'bracket', 'draw', 9, 1, 99, now())`, chess.ID.PG())
	if c := constraintOf(err); c != "event_bracket_matches_a" {
		t.Errorf("kurada olmayan yer: %q (%v)", c, err)
	}
	b, err := e.br.Get(ctx, ref(admin, tf, chess.ID, key))
	must(t, err)
	_, err = e.pool.Exec(ctx, `INSERT INTO event_bracket_games (match_id, event_id, component_key, number, a_points, a_outcome)
		VALUES ($1, $2, 'bracket', 1, 3, 'win')`, b.Matches[0].ID.PG(), chess.ID.PG())
	if c := constraintOf(err); c != "event_bracket_games_one" {
		t.Errorf("puan ve sonuç birlikte: %q (%v)", c, err)
	}
	_, err = e.pool.Exec(ctx, `UPDATE event_bracket_matches SET a_position = 2 WHERE event_id = $1`, chess.ID.PG())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("core_app maçı değiştirebildi: %v", err)
	}
}

// Who may do what for every role; a match referee scores their match
// only; IDOR; a cancelled or frozen event takes nothing, and reads stay
// open.
func TestBracketPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	organizer := e.member(t, tf, authz.Member)
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	muted := e.member(t, tf, authz.Member)
	e.mute(t, tf, muted)

	for _, tc := range []struct {
		name string
		user id.ID
		own  bool
		want error
	}{
		{"yönetici", admin, false, nil},
		{"moderatör", moderator, false, nil},
		{"kendi etkinliğinin organizatörü", organizer, true, nil},
		{"başkasının etkinliğinde organizatör", organizer, false, apperr.ErrPermissionDenied},
		{"üye", member, false, apperr.ErrPermissionDenied},
		{"susturulmuş üye", muted, false, apperr.ErrPermissionDenied},
		{"üye değil", id.New(), false, apperr.ErrTayfaNotFound},
	} {
		e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
		ev, _ := e.tournament(t, tf, admin, chessType, "", 4)
		if tc.own {
			e.exec(t, `UPDATE events SET created_by = $2 WHERE id = $1`, ev.ID.PG(), organizer.PG())
		}
		denied := func(what string, err error) {
			t.Helper()
			if tc.want == nil {
				must(t, err)
			} else if ok := errors.Is(err, tc.want) || (tc.user == muted && errors.Is(err, apperr.ErrMuted)); !ok {
				t.Errorf("%s %s: %v, %v bekleniyordu", tc.name, what, err, tc.want)
			}
		}
		r, adm := ref(tc.user, tf, ev.ID, key), ref(admin, tf, ev.ID, key)
		b, err := e.br.Start(ctx, r)
		denied("başlatma", err)
		if tc.want != nil {
			b, err = e.br.Start(ctx, adm)
			must(t, err)
		}
		e.start(ev)
		m := b.Matches[0]
		_, err = e.br.AddGame(ctx, r, m.ID, win())
		denied("oyun", err)
		if tc.want != nil {
			_, err = e.br.AddGame(ctx, adm, m.ID, win())
			must(t, err)
		}
		_, err = e.br.CorrectGame(ctx, r, m.ID, 1, loss())
		denied("düzeltme", err)
		_, err = e.br.DeleteGame(ctx, r, m.ID, 1)
		denied("silme", err)
		_, err = e.br.PairRound(ctx, r)
		if tc.want == nil {
			wantErr(t, tc.name+" açık tur", err, apperr.ErrBracketRoundOpen)
		} else {
			denied("tur", err)
		}
		_, err = e.br.Reset(ctx, r)
		denied("sıfırlama", err)

		got, err := e.br.Get(ctx, r)
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			wantErr(t, tc.name+" okuma", err, tc.want)
			continue
		}
		must(t, err)
		if got.CallerCanManage != (tc.want == nil) {
			t.Errorf("%s: caller_can_manage = %v", tc.name, got.CallerCanManage)
		}
	}

	// A referee of one match scores that match only, and corrects nothing.
	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	ev, _ := e.tournament(t, tf, admin, chessType, "", 4)
	adm := ref(admin, tf, ev.ID, key)
	b, err := e.br.Start(ctx, adm)
	must(t, err)
	referee := e.member(t, tf, authz.Member)
	mine, other := b.Matches[0], b.Matches[1]
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: referee, ComponentKey: key, Scope: mine.ID})
	must(t, err)
	e.start(ev)
	rr := ref(referee, tf, ev.ID, key)
	got, err := e.br.Get(ctx, rr)
	must(t, err)
	if !at(t, got, 1, mine.Slot).CallerCanScore || at(t, got, 1, other.Slot).CallerCanScore || got.CallerCanManage {
		t.Fatalf("hakemin izinleri: %+v", got)
	}
	_, err = e.br.AddGame(ctx, rr, other.ID, win())
	wantErr(t, "başka maçın hakemi", err, apperr.ErrPermissionDenied)
	_, err = e.br.AddGame(ctx, rr, mine.ID, win())
	must(t, err)
	_, err = e.br.CorrectGame(ctx, rr, mine.ID, 1, loss())
	wantErr(t, "hakem düzeltmesi", err, apperr.ErrPermissionDenied)

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err = e.br.Get(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)
	_, err = e.br.AddGame(ctx, ref(admin2, tf2, ev.ID, key), other.ID, win())
	wantErr(t, "başka tayfa oyun", err, apperr.ErrEventNotFound)

	// The write window closes seven days after the end; reads stay open.
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.br.AddGame(ctx, adm, other.ID, win())
	wantErr(t, "donmuş oyun", err, apperr.ErrComponentFrozen)
	_, err = e.br.Reset(ctx, adm)
	wantErr(t, "donmuş sıfırlama", err, apperr.ErrComponentFrozen)
	if got, err := e.br.Get(ctx, ref(member, tf, ev.ID, key)); err != nil || got.CallerCanManage || at(t, got, 1, other.Slot).CallerCanScore {
		t.Fatalf("donmuş okuma: %+v %v", got, err)
	}

	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	cancelled, _ := e.tournament(t, tf, admin, chessType, "", 2)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.br.Start(ctx, ref(admin, tf, cancelled.ID, key))
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
}

// A person's matches are exported through their place in the draw
// (ExportBracketMatches): their own score only, never the opponent's.
func TestBracketExport(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev, _ := e.tournament(t, tf, admin, racketType, "", 2)
	r := ref(admin, tf, ev.ID, key)
	_, err := e.br.Start(ctx, r)
	must(t, err)
	e.start(ev)
	b, err := e.br.Get(ctx, r)
	must(t, err)
	m := b.Matches[0]
	_, err = e.br.AddGame(ctx, r, m.ID, bracket.GameInput{APoints: 11, BPoints: 7})
	must(t, err)
	d, _, err := draw.Load(ctx, store.New(e.pool), ev.ID, "draw")
	must(t, err)
	q := store.New(e.pool)
	for _, tc := range []struct {
		user        id.ID
		own, theirs string
	}{
		{d.Entries[m.A-1].User, `"points": 11`, `"points": 7`},
		{d.Entries[m.B-1].User, `"points": 7`, `"points": 11`},
	} {
		rows, err := q.ExportBracketMatches(ctx, store.ExportBracketMatchesParams{UserID: tc.user.PG(), After: pgtype.UUID{Bytes: [16]byte{}, Valid: true}, N: 10})
		must(t, err)
		if len(rows) != 1 {
			t.Fatalf("dışa aktarma: %d satır", len(rows))
		}
		data := string(rows[0].Data)
		if !strings.Contains(data, ev.ID.String()) || !strings.Contains(data, tc.own) || strings.Contains(data, tc.theirs) {
			t.Fatalf("dışa aktarılan: %s", data)
		}
	}
}
