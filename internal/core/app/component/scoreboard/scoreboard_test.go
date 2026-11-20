package scoreboard_test

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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/teams"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

// Test types (Notion Faz 3 kataloğu): gameType a board game night with
// tables and presets; matchType a team sport (one table, a single match of
// two teams); chessType wdl best of three; and a second version of
// gameType without the scoreboard.
const (
	gameType  = "test_board_games"
	gameV1    = `{"components":[{"kind":"scoreboard","params":{"tables":true,"presets":["batak","okey_101","backgammon","free"]}}]}`
	gameV2    = `{"components":[{"kind":"draw"}]}`
	matchType = "test_match"
	matchV1   = `{"components":[{"kind":"teams","params":{"mode":"manual","team_count":2}},{"kind":"scoreboard","params":{"side":"team","rounds":"single","winner":"high"}}]}`
	chessType = "test_chess"
	chessV1   = `{"components":[{"kind":"scoreboard","key":"games","params":{"result":"wdl","best_of":3}}]}`
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
	sb     *scoreboard.Service
	teams  *teams.Service
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
	e := &env{sb: scoreboard.New(fw), teams: teams.New(fw), fw: fw, events: events, tayfas: tayfas, pool: d.App, admin: d.Admin, clock: c}
	for key, def := range map[string]string{gameType: gameV1, matchType: matchV1, chessType: chessV1} {
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

func (e *env) addType(t *testing.T, key, def string) {
	t.Helper()
	e.exec(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
		VALUES ($1, ARRAY['games'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, key, def)
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, key)
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) newTayfa(t *testing.T) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Oyun Tayfası", ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID, admin
}

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
func (e *env) newEvent(t *testing.T, tf, organizer id.ID, typ string) event.Event {
	t.Helper()
	start := e.clock.now().Add(24 * time.Hour)
	v, err := e.events.Create(ctx, organizer, tf, event.Input{
		Type: typ, Title: "Oyun gecesi", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
		Location:        event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.com/oda"},
		ClientRequestID: id.New().String(),
	})
	must(t, err)
	return v
}

// start moves the clock into the event.
func (e *env) start(ev event.Event) { e.clock.set(ev.StartsAt.Add(time.Minute)) }

func ref(user, tf, ev id.ID, key string) component.Ref {
	return component.Ref{User: user, Tayfa: tf, Event: ev, Key: key}
}

func (e *env) changes(t *testing.T, ev id.ID, key string) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1 AND subject_id = $2 AND payload ->> 'component_key' = $3`,
		component.EventChanged, ev.PG(), key)
}

func players(users ...id.ID) []scoreboard.PlayerInput {
	out := make([]scoreboard.PlayerInput, 0, len(users))
	for _, u := range users {
		out = append(out, scoreboard.PlayerInput{User: u})
	}
	return out
}

// pts is a points round for the table's seats in order.
func pts(tb scoreboard.Table, points ...int32) []scoreboard.ScoreInput {
	out := make([]scoreboard.ScoreInput, 0, len(points))
	for i, p := range points {
		out = append(out, scoreboard.ScoreInput{Player: tb.Players[i].ID, Points: p})
	}
	return out
}

// wdl is a wdl round for the table's seats in order.
func wdl(tb scoreboard.Table, outcomes ...string) []scoreboard.ScoreInput {
	out := make([]scoreboard.ScoreInput, 0, len(outcomes))
	for i, o := range outcomes {
		out = append(out, scoreboard.ScoreInput{Player: tb.Players[i].ID, Outcome: o})
	}
	return out
}

func last(b scoreboard.Scoreboard) scoreboard.Table { return b.Tables[len(b.Tables)-1] }

func tableOf(t *testing.T, b scoreboard.Scoreboard, table id.ID) scoreboard.Table {
	t.Helper()
	for _, tb := range b.Tables {
		if tb.ID == table {
			return tb
		}
	}
	t.Fatalf("masa yok: %v", table)
	return scoreboard.Table{}
}

// ---------------------------------------------------------------------------
// Results

func seats(n int) []scoreboard.Player {
	out := make([]scoreboard.Player, 0, n)
	for i := range n {
		out = append(out, scoreboard.Player{ID: id.New(), Position: int32(i + 1)}) //nolint:gosec // small
	}
	return out
}

func round(n int32, ps []scoreboard.Player, points ...int32) scoreboard.Round {
	r := scoreboard.Round{Number: n}
	for i, p := range points {
		r.Scores = append(r.Scores, scoreboard.Score{Player: ps[i].ID, Points: p})
	}
	return r
}

func TestComputeAndDecided(t *testing.T) {
	p := seats(3)
	high := scoreboard.Rules{Side: "player", Rounds: "round", Winner: "high", Result: "points"}
	rounds := []scoreboard.Round{round(1, p, 10, 5, 10), round(2, p, 2, 9, 2)}
	got := scoreboard.Compute(high, p, rounds, false)
	if got.Standings[0].Player != p[1].ID || got.Standings[0].Total != 14 || got.Standings[0].Rank != 1 ||
		got.Standings[1].Rank != 2 || got.Standings[2].Rank != 2 || len(got.Winners) != 0 {
		t.Fatalf("yüksek kazanır: %+v", got)
	}
	if got := scoreboard.Compute(high, p, rounds, true); len(got.Winners) != 1 || got.Winners[0] != p[1].ID {
		t.Fatalf("biten masanın kazananı: %+v", got.Winners)
	}
	if scoreboard.Decided(high, p, rounds) {
		t.Fatal("hedefsiz el el oyun kendiliğinden bitti")
	}

	// Low wins (101 Okey); equal totals share the first place.
	low := high
	low.Winner = "low"
	got = scoreboard.Compute(low, p, []scoreboard.Round{round(1, p, -101, 40, -101)}, true)
	if got.Standings[0].Rank != 1 || got.Standings[1].Rank != 1 || got.Standings[2].Player != p[1].ID || len(got.Winners) != 2 {
		t.Fatalf("düşük kazanır: %+v", got)
	}

	// First to the target (backgammon to 5): the first to reach it wins,
	// even if another passes it later in the same round order.
	first := scoreboard.Rules{Side: "player", Rounds: "first_to", Winner: "first", Result: "points", Target: 5}
	two := p[:2]
	rs := []scoreboard.Round{round(1, two, 3, 2), round(2, two, 2, 0)}
	if !scoreboard.Decided(first, two, rs) {
		t.Fatal("hedefe ulaşan masa bitmedi")
	}
	if got := scoreboard.Compute(first, two, rs, true); got.Winners[0] != two[0].ID {
		t.Fatalf("ilk ulaşan: %+v", got)
	}
	if scoreboard.Decided(first, two, rs[:1]) {
		t.Fatal("hedefe ulaşılmadan bitti")
	}

	// A single match ends with its round.
	single := scoreboard.Rules{Side: "team", Rounds: "single", Winner: "high", Result: "points"}
	if !scoreboard.Decided(single, two, []scoreboard.Round{round(1, two, 3, 2)}) || scoreboard.Decided(single, two, nil) {
		t.Fatal("tek maç")
	}

	// wdl best of three: two wins decide; a draw counts ½.
	bo3 := scoreboard.Rules{Side: "player", Rounds: "round", Winner: "high", Result: "wdl", BestOf: 3}
	w := func(n int32, a, b string) scoreboard.Round {
		return scoreboard.Round{Number: n, Scores: []scoreboard.Score{{Player: two[0].ID, Outcome: a}, {Player: two[1].ID, Outcome: b}}}
	}
	rs = []scoreboard.Round{w(1, "draw", "draw"), w(2, "loss", "win")}
	if scoreboard.Decided(bo3, two, rs) {
		t.Fatal("BO3 tek galibiyetle bitti")
	}
	rs = append(rs, w(3, "loss", "win"))
	got = scoreboard.Compute(bo3, two, rs, scoreboard.Decided(bo3, two, rs))
	if len(got.Winners) != 1 || got.Winners[0] != two[1].ID || got.Standings[0].Total != 2.5 || got.Standings[0].RoundsWon != 2 ||
		got.Standings[1].Total != 0.5 {
		t.Fatalf("BO3: %+v", got)
	}
}

// The seeded presets (Notion Faz 3) all pass the reader; none has a brand
// name or a money rule (the reader refuses unknown rules).
func TestSeededPresets(t *testing.T) {
	e := setup(t)
	keys := []string{"batak", "okey_101", "okey", "backgammon", "pisti", "foosball", "team_rounds", "free"}
	got, err := scoreboard.LoadPresets(ctx, store.New(e.pool), keys)
	must(t, err)
	if len(got) != len(keys) {
		t.Fatalf("ön ayarlar: %d", len(got))
	}
	for i, p := range got {
		if p.Key != keys[i] || p.Labels["tr"] == "" || p.Retired {
			t.Errorf("ön ayar %d: %+v", i, p)
		}
	}
	if got[1].Rules.Winner != "low" || got[3].Rules.Target != 5 || got[6].Rules.Side != "team" || got[7].Rules != (scoreboard.Rules{}) {
		t.Fatalf("ön ayar kuralları: %+v", got)
	}
	for name, params := range map[string]string{
		"bilinmeyen kural": `{"stake": 10}`,
		"bozuk kazanan":    `{"winner": "most"}`,
		"bozuk hedef":      `{"target": 0}`,
		"bozuk BO":         `{"best_of": 2}`,
		"ters sınır":       `{"players_min": 4, "players_max": 2}`,
	} {
		if _, err := scoreboard.ParsePreset("x", []byte(`{"tr": "X"}`), []byte(params), "active"); err == nil {
			t.Errorf("%s kabul edildi", name)
		}
	}
	// core_app only reads the presets.
	if _, err := e.pool.Exec(ctx, `UPDATE catalog_score_presets SET params = '{}'`); err == nil {
		t.Fatal("core_app ön ayarı değiştirdi")
	}
}

// ---------------------------------------------------------------------------
// The service

// A board game night: tables with presets, a referee per table, rounds,
// corrections and the end of a game.
func TestScoreboardTablesAndRounds(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, gameType)
	p := e.going(t, tf, ev.ID, 5)
	const key = "scoreboard"
	r := ref(admin, tf, ev.ID, key)

	got, err := e.sb.Get(ctx, r)
	must(t, err)
	if len(got.Tables) != 0 || got.MaxTables != scoreboard.MaxTables || !got.CallerCanManage || len(got.Presets) != 4 ||
		got.Presets[0].Key != "batak" || got.Rules != (scoreboard.Rules{Side: "player", Rounds: "round", Winner: "high", Result: "points"}) {
		t.Fatalf("boş yazboz: %+v", got)
	}

	// A preset is required, one of the offered and active.
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Masa 1"})
	wantErr(t, "ön ayarsız masa", err, apperr.ErrScorePresetUnavailable)
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Masa 1", Preset: "okey"})
	wantErr(t, "sunulmayan ön ayar", err, apperr.ErrScorePresetUnavailable)
	e.exec(t, `UPDATE catalog_score_presets SET status = 'retired' WHERE key = 'free'`)
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Masa 1", Preset: "free"})
	wantErr(t, "emekli ön ayar", err, apperr.ErrScorePresetUnavailable)
	// Batak seats three or four.
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Masa 1", Preset: "batak", Players: players(p[:2]...)})
	wantField(t, "iki kişilik batak", err, "players")
	outsider := e.member(t, tf, authz.Member)
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Masa 1", Preset: "batak", Players: players(p[0], p[1], outsider)})
	wantErr(t, "katılımcı olmayan", err, apperr.ErrScorePlayerNotParticipant)

	before := e.changes(t, ev.ID, key)
	got, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "  Masa 1 ", Preset: "batak", Target: 51, Players: players(p[:4]...)})
	must(t, err)
	batak := last(got)
	if batak.Label != "Masa 1" || batak.Preset != "batak" || batak.Status != scoreboard.StatusOpen || len(batak.Players) != 4 ||
		batak.Players[0].User != p[0] || batak.Rules.Target != 51 || batak.Rules.Winner != "high" || batak.CallerCanScore {
		t.Fatalf("batak masası: %+v", batak)
	}
	if e.changes(t, ev.ID, key) != before+1 {
		t.Fatal("masa kurmak canlı olay yazmadı")
	}
	got, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Tavla", Preset: "backgammon", Players: players(p[0], p[1])})
	must(t, err)
	tavla := last(got)
	if tavla.Rules != (scoreboard.Rules{Side: "player", Rounds: "first_to", Winner: "first", Result: "points", Target: 5}) {
		t.Fatalf("tavla kuralları: %+v", tavla.Rules)
	}

	// A referee of the backgammon table only.
	referee := p[2]
	_, err = e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: referee, ComponentKey: key, Scope: tavla.ID})
	must(t, err)
	rr := ref(referee, tf, ev.ID, key)

	// Before the start no round is entered.
	_, err = e.sb.AddRound(ctx, rr, tavla.ID, pts(tavla, 1, 0))
	wantErr(t, "başlamadan el", err, apperr.ErrScoreNotStarted)
	e.start(ev)
	got, err = e.sb.Get(ctx, rr)
	must(t, err)
	if !tableOf(t, got, tavla.ID).CallerCanScore || tableOf(t, got, batak.ID).CallerCanScore || got.CallerCanManage {
		t.Fatalf("hakemin izinleri: %+v", got)
	}
	_, err = e.sb.AddRound(ctx, rr, batak.ID, pts(batak, 1, 2, 3, 4))
	wantErr(t, "başka masanın hakemi", err, apperr.ErrPermissionDenied)
	_, err = e.sb.AddRound(ctx, ref(p[3], tf, ev.ID, key), tavla.ID, pts(tavla, 1, 0))
	wantErr(t, "hakem olmayan katılımcı", err, apperr.ErrPermissionDenied)

	got, err = e.sb.AddRound(ctx, rr, tavla.ID, pts(tavla, 3, 2))
	must(t, err)
	if tb := tableOf(t, got, tavla.ID); tb.Status != scoreboard.StatusOpen || len(tb.Rounds) != 1 || tb.Rounds[0].Number != 1 {
		t.Fatalf("ilk el: %+v", tb)
	}
	// Correcting is the organizer's.
	_, err = e.sb.CorrectRound(ctx, rr, tavla.ID, 1, pts(tavla, 2, 2))
	wantErr(t, "hakemin düzeltmesi", err, apperr.ErrPermissionDenied)
	_, err = e.sb.DeleteRound(ctx, rr, tavla.ID, 1)
	wantErr(t, "hakemin silmesi", err, apperr.ErrPermissionDenied)
	_, err = e.sb.CorrectRound(ctx, r, tavla.ID, 2, pts(tavla, 2, 2))
	wantField(t, "girilmemiş el", err, "round")
	got, err = e.sb.CorrectRound(ctx, r, tavla.ID, 1, pts(tavla, 3, 1))
	must(t, err)
	if tb := tableOf(t, got, tavla.ID); tb.Rounds[0].Scores[1].Points != 1 {
		t.Fatalf("düzeltme: %+v", tb.Rounds)
	}
	// The round that reaches the target ends the game.
	got, err = e.sb.AddRound(ctx, rr, tavla.ID, pts(tavla, 2, 0))
	must(t, err)
	tb := tableOf(t, got, tavla.ID)
	if tb.Status != scoreboard.StatusFinished || tb.FinishedAt.IsZero() || len(tb.Result.Winners) != 1 ||
		tb.Result.Winners[0] != tb.Players[0].ID || tb.CallerCanScore {
		t.Fatalf("biten tavla: %+v", tb)
	}
	_, err = e.sb.AddRound(ctx, rr, tavla.ID, pts(tavla, 1, 0))
	wantErr(t, "biten masaya el", err, apperr.ErrScoreTableFinished)
	_, err = e.sb.SetPlayers(ctx, r, tavla.ID, players(p[2], p[3]))
	wantErr(t, "skorlu masanın yerleri", err, apperr.ErrScoreTableStarted)
	// The organizer reopens it and takes the last round back.
	_, err = e.sb.UpdateTable(ctx, rr, tavla.ID, scoreboard.TableUpdate{Status: scoreboard.StatusOpen})
	wantErr(t, "hakem yeniden açamaz", err, apperr.ErrPermissionDenied)
	_, err = e.sb.UpdateTable(ctx, r, tavla.ID, scoreboard.TableUpdate{Status: scoreboard.StatusOpen})
	must(t, err)
	_, err = e.sb.DeleteRound(ctx, r, tavla.ID, 1)
	wantField(t, "son olmayan el", err, "round")
	got, err = e.sb.DeleteRound(ctx, r, tavla.ID, 2)
	must(t, err)
	if tb := tableOf(t, got, tavla.ID); tb.Status != scoreboard.StatusOpen || len(tb.Rounds) != 1 || len(tb.Result.Winners) != 0 {
		t.Fatalf("geri alınan el: %+v", tb)
	}
	// The referee finishes it by hand.
	got, err = e.sb.FinishTable(ctx, rr, tavla.ID)
	must(t, err)
	if tb := tableOf(t, got, tavla.ID); tb.Status != scoreboard.StatusFinished || len(tb.Result.Winners) != 1 {
		t.Fatalf("elle bitirme: %+v", tb)
	}
	if _, err := e.sb.FinishTable(ctx, rr, tavla.ID); err != nil {
		t.Fatalf("biten masayı yeniden bitirme: %v", err)
	}

	// Scores are checked against the table.
	for name, in := range map[string][]scoreboard.ScoreInput{
		"başka masanın yeri": {{Player: tavla.Players[0].ID, Points: 1}},
		"sonuç":              {{Player: batak.Players[0].ID, Outcome: scoreboard.OutcomeWin}},
	} {
		_, err := e.sb.AddRound(ctx, r, batak.ID, in)
		wantField(t, name, err, "scores")
	}
	for name, in := range map[string][]scoreboard.ScoreInput{
		"boş":       nil,
		"iki kez":   {{Player: batak.Players[0].ID}, {Player: batak.Players[0].ID}},
		"çok büyük": {{Player: batak.Players[0].ID, Points: scoreboard.PointsMax + 1}},
		"bozuk":     {{Player: batak.Players[0].ID, Outcome: "lose"}},
	} {
		_, err := e.sb.AddRound(ctx, r, batak.ID, in)
		wantField(t, name, err, "scores")
	}
	_, err = e.sb.AddRound(ctx, r, id.New(), pts(batak, 1, 2, 3, 4))
	wantErr(t, "olmayan masa", err, apperr.ErrScoreTableNotFound)

	// Seats change until the first round; batak seats at most four.
	_, err = e.sb.SetPlayers(ctx, r, batak.ID, players(p...))
	wantField(t, "beş kişilik batak", err, "players")
	got, err = e.sb.SetPlayers(ctx, r, batak.ID, players(p[3], p[2], p[1]))
	must(t, err)
	batak = tableOf(t, got, batak.ID)
	if len(batak.Players) != 3 || batak.Players[0].User != p[3] {
		t.Fatalf("yerler: %+v", batak.Players)
	}
	got, err = e.sb.AddRound(ctx, r, batak.ID, pts(batak, 30, 60, 10))
	must(t, err)
	if tb := tableOf(t, got, batak.ID); tb.Status != scoreboard.StatusFinished || tb.Result.Winners[0] != batak.Players[1].ID {
		t.Fatalf("hedefi geçen batak: %+v", tb)
	}

	// Renaming, deleting with the table's referee assignments.
	got, err = e.sb.UpdateTable(ctx, r, tavla.ID, scoreboard.TableUpdate{Label: ptr("Tavla finali")})
	must(t, err)
	if tb := tableOf(t, got, tavla.ID); tb.Label != "Tavla finali" || tb.Status != scoreboard.StatusFinished {
		t.Fatalf("yeniden adlandırma: %+v", tb)
	}
	for name, in := range map[string]scoreboard.TableUpdate{
		"boş ad":    {Label: ptr("  ")},
		"uzun ad":   {Label: ptr(strings.Repeat("ç", scoreboard.LabelMax+1))},
		"denetim":   {Label: ptr("Masa\x00")},
		"bozuk dur": {Status: "paused"},
	} {
		_, err := e.sb.UpdateTable(ctx, r, tavla.ID, in)
		if in.Status != "" {
			wantField(t, name, err, "status")
		} else {
			wantField(t, name, err, "label")
		}
	}
	got, err = e.sb.DeleteTable(ctx, r, tavla.ID)
	must(t, err)
	if len(got.Tables) != 1 || e.count(t, `SELECT count(*) FROM event_referees WHERE scope_id = $1`, tavla.ID.PG()) != 0 ||
		e.count(t, `SELECT count(*) FROM event_scores WHERE table_id = $1`, tavla.ID.PG()) != 0 {
		t.Fatalf("silinen masa: %+v", got)
	}
	_, err = e.sb.DeleteTable(ctx, r, tavla.ID)
	wantErr(t, "silinmiş masa", err, apperr.ErrScoreTableNotFound)

	// At most MaxTables tables.
	for i := len(got.Tables); i < scoreboard.MaxTables; i++ {
		_, err := e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Masa", Preset: "okey_101"})
		must(t, err)
	}
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Masa", Preset: "okey_101"})
	wantErr(t, "dolu bileşen", err, apperr.ErrEventLimit)
}

// TestScoreboardScoresInSeatOrder pins issue #297: seat IDs made in the
// same millisecond are not ordered, so a round's scores follow the seats'
// positions, not their IDs. The seats are renumbered against their IDs to
// make the case every run.
func TestScoreboardScoresInSeatOrder(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, gameType)
	p := e.going(t, tf, ev.ID, 2)
	const key = "scoreboard"
	r := ref(admin, tf, ev.ID, key)

	got, err := e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Tavla", Preset: "backgammon", Players: players(p...)})
	must(t, err)
	table := last(got).ID
	e.exec(t, `UPDATE event_score_players AS sp SET position = o.n
		FROM (SELECT id, 10 + row_number() OVER (ORDER BY id DESC) AS n FROM event_score_players WHERE table_id = $1) AS o
		WHERE sp.id = o.id`, table.PG())
	got, err = e.sb.Get(ctx, r)
	must(t, err)
	tb := tableOf(t, got, table)
	if len(tb.Players) != 2 || string(tb.Players[0].ID[:]) < string(tb.Players[1].ID[:]) {
		t.Fatalf("kimliğe ters yerler: %+v", tb.Players)
	}

	e.start(ev)
	got, err = e.sb.AddRound(ctx, r, table, pts(tb, 3, 1))
	must(t, err)
	tb = tableOf(t, got, table)
	if len(tb.Rounds) != 1 || len(tb.Rounds[0].Scores) != 2 {
		t.Fatalf("el: %+v", tb.Rounds)
	}
	for i, sc := range tb.Rounds[0].Scores {
		if sc.Player != tb.Players[i].ID || sc.Points != []int32{3, 1}[i] {
			t.Fatalf("eldeki skorlar yer sırasında değil: %+v, yerler %+v", tb.Rounds[0].Scores, tb.Players)
		}
	}
}

func ptr(s string) *string { return &s }

// A team sport: one table of two teams of a teams component, a single
// match; and a wdl best-of-three match under its own key.
func TestScoreboardTeamsAndWDL(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, matchType)
	p := e.going(t, tf, ev.ID, 4)
	tm, err := e.teams.Replace(ctx, ref(admin, tf, ev.ID, "teams"), []teams.Input{{Members: p[:2]}, {Members: p[2:]}})
	must(t, err)
	r := ref(admin, tf, ev.ID, "scoreboard")
	two := []scoreboard.PlayerInput{{Team: tm.Teams[0].ID}, {Team: tm.Teams[1].ID}}

	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Maç", Players: two})
	wantField(t, "takım bileşenisiz", err, "teams_key")
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Maç", TeamsKey: "scoreboard", Players: two})
	wantField(t, "takım olmayan bileşen", err, "teams_key")
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Maç", TeamsKey: "teams", Players: players(p[0], p[2])})
	wantField(t, "takım masasına kişi", err, "players")
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Maç", TeamsKey: "teams", Players: []scoreboard.PlayerInput{{Team: id.New()}, {Team: tm.Teams[1].ID}}})
	wantErr(t, "olmayan takım", err, apperr.ErrScorePlayerNotParticipant)
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Maç", Preset: "free", TeamsKey: "teams", Players: two})
	wantErr(t, "ön ayar sunmayan bileşende ön ayar", err, apperr.ErrScorePresetUnavailable)
	got, err := e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Maç", TeamsKey: "teams", Players: two})
	must(t, err)
	match := last(got)
	if got.MaxTables != 1 || match.TeamsKey != "teams" || match.Players[1].Team != tm.Teams[1].ID || match.Rules.Rounds != "single" {
		t.Fatalf("takım masası: %+v", got)
	}
	_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Rövanş", TeamsKey: "teams", Players: two})
	wantErr(t, "tek masalı bileşen", err, apperr.ErrEventLimit)

	e.start(ev)
	got, err = e.sb.AddRound(ctx, r, match.ID, pts(match, 3, 2))
	must(t, err)
	if tb := last(got); tb.Status != scoreboard.StatusFinished || tb.Result.Winners[0] != match.Players[0].ID {
		t.Fatalf("tek maç: %+v", tb)
	}
	// The organizer may correct a finished match.
	got, err = e.sb.CorrectRound(ctx, r, match.ID, 1, pts(match, 3, 3))
	must(t, err)
	if tb := last(got); tb.Status != scoreboard.StatusFinished || len(tb.Result.Winners) != 2 {
		t.Fatalf("berabere düzeltilen maç: %+v", tb)
	}
	// A team taken away empties its seat; the score stays.
	_, err = e.teams.Replace(ctx, ref(admin, tf, ev.ID, "teams"), []teams.Input{{Members: p}})
	must(t, err)
	got, err = e.sb.Get(ctx, r)
	must(t, err)
	if tb := last(got); tb.Players[1].Team != (id.ID{}) || len(tb.Rounds[0].Scores) != 2 {
		t.Fatalf("kaldırılan takımın yeri: %+v", tb)
	}

	// wdl, best of three.
	chess := e.newEvent(t, tf, admin, chessType)
	c := e.going(t, tf, chess.ID, 2)
	rc := ref(admin, tf, chess.ID, "games")
	got, err = e.sb.CreateTable(ctx, rc, scoreboard.TableInput{Label: "Final", Players: players(c...)})
	must(t, err)
	game := last(got)
	if game.Rules.Result != "wdl" || game.Rules.BestOf != 3 {
		t.Fatalf("satranç kuralları: %+v", game.Rules)
	}
	e.start(chess)
	for name, in := range map[string][]scoreboard.ScoreInput{
		"iki galibiyet":  wdl(game, "win", "win"),
		"tek yer":        wdl(game, "win"),
		"galibiyetsiz":   wdl(game, "loss", "loss"),
		"yarım beraber":  wdl(game, "draw", "loss"),
		"puan":           {{Player: game.Players[0].ID, Points: 1}, {Player: game.Players[1].ID, Outcome: "loss"}},
		"puanlı sonuç":   {{Player: game.Players[0].ID, Outcome: "win", Points: 1}, {Player: game.Players[1].ID, Outcome: "loss"}},
		"sonuçsuz yer":   {{Player: game.Players[0].ID, Outcome: "win"}, {Player: game.Players[1].ID}},
		"bilinmeyen yer": {{Player: id.New(), Outcome: "win"}, {Player: game.Players[1].ID, Outcome: "loss"}},
	} {
		_, err := e.sb.AddRound(ctx, rc, game.ID, in)
		wantField(t, name, err, "scores")
	}
	_, err = e.sb.AddRound(ctx, rc, game.ID, wdl(game, "draw", "draw"))
	must(t, err)
	got, err = e.sb.AddRound(ctx, rc, game.ID, wdl(game, "win", "loss"))
	must(t, err)
	if last(got).Status != scoreboard.StatusOpen {
		t.Fatal("BO3 bir galibiyetle bitti")
	}
	got, err = e.sb.AddRound(ctx, rc, game.ID, wdl(game, "win", "loss"))
	must(t, err)
	tb := last(got)
	if tb.Status != scoreboard.StatusFinished || tb.Result.Winners[0] != game.Players[0].ID || tb.Result.Standings[0].Total != 2.5 ||
		tb.Rounds[0].Scores[0].Outcome != "draw" {
		t.Fatalf("BO3 sonucu: %+v", tb)
	}
}

// The component exists only where the event's type version has it; writes
// outside the type version are refused by the service and the trigger.
func TestScoreboardOutsideTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	general := e.newEvent(t, tf, admin, event.TypeGeneral)
	game := e.newEvent(t, tf, admin, gameType)
	chess := e.newEvent(t, tf, admin, chessType)
	for name, r := range map[string]component.Ref{
		"bileşensiz tür":          ref(admin, tf, general.ID, "scoreboard"),
		"tanımda olmayan anahtar": ref(admin, tf, chess.ID, "scoreboard"),
	} {
		_, err := e.sb.Get(ctx, r)
		wantErr(t, name, err, apperr.ErrComponentNotFound)
		_, err = e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Masa"})
		wantErr(t, name+" masa", err, apperr.ErrComponentNotFound)
	}

	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ($1, 2, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], $2::jsonb)`, gameType, gameV2)
	e.exec(t, `UPDATE catalog_event_types SET version = 2, definition = $2::jsonb WHERE key = $1`, gameType, gameV2)
	newer := e.newEvent(t, tf, admin, gameType)
	_, err := e.sb.CreateTable(ctx, ref(admin, tf, newer.ID, "scoreboard"), scoreboard.TableInput{Label: "Masa", Preset: "batak"})
	wantErr(t, "yeni sürümde olmayan yazboz", err, apperr.ErrComponentNotFound)
	if _, err := e.sb.CreateTable(ctx, ref(admin, tf, game.ID, "scoreboard"), scoreboard.TableInput{Label: "Masa", Preset: "batak"}); err != nil {
		t.Fatalf("eski sürümün etkinliği: %v", err)
	}

	for name, ev := range map[string]id.ID{"bileşensiz tür": general.ID, "yeni sürüm": newer.ID} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_score_tables (id, event_id, component_key, label, side, rounds, winner, result)
			VALUES (gen_random_uuid(), $1, 'scoreboard', 'Masa', 'player', 'round', 'high', 'points')`, ev.PG())
		if c := constraintOf(err); c != "event_component_guard" {
			t.Errorf("%s: tetikleyici %q (%v)", name, c, err)
		}
	}

	// The tables' CHECKs and keys.
	for name, tc := range map[string]struct{ sql, constraint string }{
		"boş etiket": {`INSERT INTO event_score_tables (id, event_id, component_key, label, side, rounds, winner, result)
			VALUES (gen_random_uuid(), $1, 'scoreboard', '', 'player', 'round', 'high', 'points')`, "event_score_tables_label"},
		"hedefsiz ilk ulaşan": {`INSERT INTO event_score_tables (id, event_id, component_key, label, side, rounds, winner, result)
			VALUES (gen_random_uuid(), $1, 'scoreboard', 'Masa', 'player', 'round', 'first', 'points')`, "event_score_tables_target_needed"},
		"takımsız takım masası": {`INSERT INTO event_score_tables (id, event_id, component_key, label, side, rounds, winner, result)
			VALUES (gen_random_uuid(), $1, 'scoreboard', 'Masa', 'team', 'round', 'high', 'points')`, "event_score_tables_side"},
		"yabancı masa": {`INSERT INTO event_score_players (id, table_id, event_id, component_key, position, user_id)
			VALUES (gen_random_uuid(), gen_random_uuid(), $1, 'scoreboard', 1, gen_random_uuid())`, "event_score_players_table"},
	} {
		_, err := e.pool.Exec(ctx, tc.sql, game.ID.PG())
		if c := constraintOf(err); c != tc.constraint {
			t.Errorf("%s: %q (%v)", name, c, err)
		}
	}
	// core_app cannot move a table, a seat or a score.
	for name, sql := range map[string]string{
		"masa":  `UPDATE event_score_tables SET event_id = $1`,
		"yer":   `UPDATE event_score_players SET table_id = $1`,
		"skor":  `UPDATE event_scores SET points = 1 WHERE id = $1`,
		"kural": `UPDATE event_score_tables SET target = 1 WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(ctx, sql, id.New().PG()); err == nil {
			t.Errorf("%s değiştirildi", name)
		}
	}
}

// Who may do what for every role; a cancelled or frozen event takes
// nothing, and reads stay open.
func TestScoreboardPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, gameType)
	const key = "scoreboard"
	organizer := e.member(t, tf, authz.Member)
	member := e.member(t, tf, authz.Member)
	moderator := e.member(t, tf, authz.Moderator)
	muted := e.member(t, tf, authz.Member)
	e.mute(t, tf, muted)
	own := e.newEvent(t, tf, admin, gameType)
	e.exec(t, `UPDATE events SET created_by = $2 WHERE id = $1`, own.ID.PG(), organizer.PG())
	// A muted referee of the component scores nothing.
	_, err := e.fw.AssignReferee(ctx, admin, tf, ev.ID, component.RefereeInput{User: muted, ComponentKey: key})
	must(t, err)

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
		{"susturulmuş hakem", muted, ev, apperr.ErrPermissionDenied},
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
		e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
		r := ref(tc.user, tf, tc.ev.ID, key)
		got, err := e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Masa", Preset: "free"})
		denied("masa kurma", err)
		if tc.want != nil {
			got, err = e.sb.CreateTable(ctx, ref(admin, tf, tc.ev.ID, key), scoreboard.TableInput{Label: "Masa", Preset: "free"})
			must(t, err)
		}
		table := last(got)
		_, err = e.sb.SetPlayers(ctx, r, table.ID, nil)
		denied("yerler", err)
		pl := e.going(t, tf, tc.ev.ID, 2)
		got, err = e.sb.SetPlayers(ctx, ref(admin, tf, tc.ev.ID, key), table.ID, players(pl...))
		must(t, err)
		table = tableOf(t, got, table.ID)
		e.start(tc.ev)
		_, err = e.sb.AddRound(ctx, r, table.ID, pts(table, 1, 2))
		denied("el", err)
		if tc.want != nil {
			_, err = e.sb.AddRound(ctx, ref(admin, tf, tc.ev.ID, key), table.ID, pts(table, 1, 2))
			must(t, err)
		}
		_, err = e.sb.CorrectRound(ctx, r, table.ID, 1, pts(table, 2, 1))
		denied("düzeltme", err)
		_, err = e.sb.FinishTable(ctx, r, table.ID)
		denied("bitirme", err)

		got, err = e.sb.Get(ctx, r)
		if errors.Is(tc.want, apperr.ErrTayfaNotFound) {
			wantErr(t, tc.name+" okuma", err, tc.want)
			continue
		}
		must(t, err)
		if got.CallerCanManage != (tc.want == nil) {
			t.Errorf("%s: caller_can_manage = %v", tc.name, got.CallerCanManage)
		}
	}

	// Another tayfa's event does not exist through this tayfa.
	tf2, admin2 := e.newTayfa(t)
	_, err = e.sb.Get(ctx, ref(admin2, tf2, ev.ID, key))
	wantErr(t, "başka tayfa", err, apperr.ErrEventNotFound)

	// Frozen and cancelled events take nothing; reads stay open.
	got, err := e.sb.Get(ctx, ref(admin, tf, ev.ID, key))
	must(t, err)
	table := got.Tables[0]
	e.clock.set(ev.EndsAt.Add(component.FreezeAfter))
	_, err = e.sb.AddRound(ctx, ref(admin, tf, ev.ID, key), table.ID, pts(table, 1, 1))
	wantErr(t, "donmuş el", err, apperr.ErrComponentFrozen)
	_, err = e.sb.CreateTable(ctx, ref(admin, tf, ev.ID, key), scoreboard.TableInput{Label: "Masa", Preset: "free"})
	wantErr(t, "donmuş masa", err, apperr.ErrComponentFrozen)
	if got, err := e.sb.Get(ctx, ref(member, tf, ev.ID, key)); err != nil || got.CallerCanManage || got.Tables[0].CallerCanScore {
		t.Fatalf("donmuş okuma: %+v %v", got, err)
	}

	e.clock.set(time.Now().UTC().Truncate(time.Microsecond))
	cancelled := e.newEvent(t, tf, admin, gameType)
	_, err = e.events.Cancel(ctx, admin, tf, cancelled.ID, "")
	must(t, err)
	_, err = e.sb.CreateTable(ctx, ref(admin, tf, cancelled.ID, key), scoreboard.TableInput{Label: "Masa", Preset: "free"})
	wantErr(t, "iptal edilmiş etkinlik", err, apperr.ErrEventCancelled)
}

// A person's seats and scores are exported (ExportScores): only theirs.
func TestScoreboardExport(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, gameType)
	p := e.going(t, tf, ev.ID, 2)
	r := ref(admin, tf, ev.ID, "scoreboard")
	got, err := e.sb.CreateTable(ctx, r, scoreboard.TableInput{Label: "Tavla", Preset: "backgammon", Players: players(p...)})
	must(t, err)
	tb := last(got)
	e.start(ev)
	_, err = e.sb.AddRound(ctx, r, tb.ID, pts(tb, 2, 1))
	must(t, err)
	rows, err := store.New(e.pool).ExportScores(ctx, store.ExportScoresParams{UserID: p[0].PG(), After: pgtype.UUID{Bytes: [16]byte{}, Valid: true}, N: 10})
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("dışa aktarma: %d satır", len(rows))
	}
	data := string(rows[0].Data)
	if !strings.Contains(data, tb.ID.String()) || !strings.Contains(data, `"points": 2`) || !strings.Contains(data, `"position": 1`) ||
		strings.Contains(data, p[1].String()) || strings.Contains(data, `"points": 1`) {
		t.Fatalf("dışa aktarılan: %s", data)
	}
}
