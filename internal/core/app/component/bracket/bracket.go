// Package bracket is the bracket component (kind "bracket", issue #251;
// ADR-0025 §4–§6, ADR-0037 §3): a tournament seeded from the event's draw,
// played as an elimination, a league or a Swiss system, scored live.
//
// The format (`format`: elimination, league, swiss) and the games of a
// match (`best_of`: 1, 3, 5) are the component's parameters, the
// organizer's choice where the definition leaves it to them. A match is
// scored game by game by the scoreboard's rule (scoreboard.Decided and
// Compute): the type's first scoreboard component says whether a game is
// points (high or low wins) or a win, draw or loss (wdl). The rules are
// resolved when the bracket is started and kept, so that a later change
// does not change a tournament that is being played.
//
// The entrants are the places of the type's first draw component
// (draw.Load), which must have been drawn: a person or a team, read from
// the draw and not kept here. Two places in a row are a pairing:
//
//	elimination  round 1 pairs the draw's places (1–2, 3–4 …; an empty
//	             place or an odd last one is a bye); every next round pairs
//	             the winners of the one before in order. A drawn match
//	             stays open for another game. The last winner wins.
//	league       every two entrants meet once (the circle method); all
//	             rounds are paired when the bracket starts.
//	swiss        ⌈log2 n⌉ rounds; round 1 pairs the draw's order, every
//	             next round the standings top down without a rematch, a
//	             bye (a win) to the lowest without one.
//
// A league or Swiss table ranks by points (win 1, draw ½, bye 1), then the
// points among those level (the match between them), then Buchholz;
// there is no rating (ADR-0037 §3). The winners (Load, Result.Winners)
// are the elimination's last winner or the table's first place once the
// bracket is finished; more than one for a full tie.
//
// Who may do what (ADR-0025 §5; the framework's roles, package component):
//
//	read                        every active member who may see the
//	                            event's components
//	start, pair the next round,
//	correct or delete a game,
//	reset                       managers (the organizer, and moderators
//	                            and admins as for every event)
//	enter a game                managers and the referees assigned to the
//	                            match, the component or the event
//
// Games are entered from the event's start until the write window closes.
// A game of a round that a later round was paired from no longer changes
// (reset the bracket instead). Every change is live
// (event.component_changed, ADR-0025 §6). A bracket keeps no person: a
// person's matches follow their place in the draw, which empties with
// their account; their matches and own scores are exported. A bracket is
// attendance, not the plan: a series occurrence does not take it.
package bracket

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/draw"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompBracket

// The component's parameters (definition).
const (
	ParamFormat = "format"
	ParamBestOf = "best_of"
)

// Formats (definition.BracketFormats).
const (
	FormatElimination = "elimination"
	FormatLeague      = "league"
	FormatSwiss       = "swiss"
)

// Limits; the CHECKs on the tables (00046) hold the same.
const (
	// MaxElimination is the most places of the draw an elimination takes.
	MaxElimination = 128
	// MaxLeague is the most entrants of a league (190 matches).
	MaxLeague = 20
	// MaxSwiss is the most entrants of a Swiss bracket.
	MaxSwiss = 64
	// MaxGames is the most games of a match.
	MaxGames  = 9
	PointsMax = 1_000_000
)

// Audit actions. The records hold the format, counts, the round, the match
// and the game number only, never a score or a person.
const (
	AuditStarted       = "event.bracket_started"
	AuditReset         = "event.bracket_reset"
	AuditRoundPaired   = "event.bracket_round_paired"
	AuditGameAdded     = "event.bracket_game_added"
	AuditGameCorrected = "event.bracket_game_corrected"
	AuditGameDeleted   = "event.bracket_game_deleted"
)

// Rules are a bracket's rules.
type Rules struct {
	// Format is FormatElimination, FormatLeague or FormatSwiss.
	Format string
	// BestOf is 1, 3 or 5.
	BestOf int32
	// Result is scoreboard.ResultPoints or scoreboard.ResultWDL.
	Result string
	// Winner is scoreboard.WinnerHigh or scoreboard.WinnerLow (points).
	Winner string
}

// Game is one game of a match: APoints and BPoints, or A's outcome in
// wdl (B's is the inverse).
type Game struct {
	Number   int32
	APoints  int32
	BPoints  int32
	AOutcome string
}

// Match is one match. A and B are places in the draw; 0 is no side.
type Match struct {
	ID    id.ID
	Round int32
	// Slot is the match's order in its round, 1-based.
	Slot  int32
	A     int32
	B     int32
	Games []Game
	// Decided reports whether the match has its result: a winner, a draw
	// (not in an elimination) or a bye.
	Decided bool
	// Bye reports a match with at most one side.
	Bye bool
	// Winner is the winning side's place; 0 for a draw, an undecided match
	// or a match without sides.
	Winner int32
	// ATotal and BTotal are the sides' totals (wdl: win 1, draw ½); AWon
	// and BWon the games each won alone.
	ATotal float64
	BTotal float64
	AWon   int32
	BWon   int32
	// CallerCanScore reports whether the caller may enter a game now (Get
	// and the changes only).
	CallerCanScore bool
}

// Standing is an entrant's place in a league or Swiss table.
type Standing struct {
	Position int32
	Played   int32
	Won      int32
	Drawn    int32
	Lost     int32
	// Points: a win 1, a draw ½, a bye 1.
	Points float64
	// HeadToHead is the points scored against those level on points.
	HeadToHead float64
	// Buchholz is the sum of the opponents' points.
	Buchholz float64
	// Rank is 1-based; entrants equal on all three share it.
	Rank int32
}

// Result is a started bracket, as Load reads it.
type Result struct {
	Rules
	// DrawKey is the draw component the bracket seeds from; Subject and
	// TeamsKey are the draw's.
	DrawKey  string
	Subject  string
	TeamsKey string
	// Rounds is the planned number of rounds: the league's, the Swiss
	// rounds to play, the most an elimination may take.
	Rounds int32
	// CurrentRound is the last paired round.
	CurrentRound int32
	StartedAt    time.Time
	// Entrants are the draw's places; a place whose person left or whose
	// team was removed is empty (zero User and Team).
	Entrants []draw.Entry
	// Matches are ordered by round and slot.
	Matches []Match
	// Standings rank a league or Swiss bracket; empty for an elimination.
	Standings []Standing
	// Finished reports that the bracket has its result.
	Finished bool
	// Winners are the winning places once Finished: the elimination's last
	// winner, or the table's first (more than one for a full tie). Empty
	// before.
	Winners []int32
}

// Entry returns the draw's place; ok is false for no such place.
func (r Result) Entry(place int32) (e draw.Entry, ok bool) {
	i := slices.IndexFunc(r.Entrants, func(x draw.Entry) bool { return x.Position == place })
	if i < 0 {
		return draw.Entry{}, false
	}
	return r.Entrants[i], true
}

// Champions returns the winners' places in the draw: for the celebration
// (issue #255) to read who won. An empty place (zero User and Team) is a
// winner whose person or team is gone.
func (r Result) Champions() []draw.Entry {
	out := make([]draw.Entry, 0, len(r.Winners))
	for _, w := range r.Winners {
		if e, ok := r.Entry(w); ok {
			out = append(out, e)
		}
	}
	return out
}

// Bracket is one bracket component of an event, as the caller sees it.
type Bracket struct {
	// Started reports whether the bracket was started; before it, Rules,
	// DrawKey and the draw's fields of Result are what a start would take.
	Started bool
	// Drawn reports whether the draw the bracket seeds from was made.
	Drawn bool
	Result
	// CallerCanManage reports whether the caller may start, pair, correct
	// and reset now.
	CallerCanManage bool
}

// GameInput is a game to enter: the points of both sides, or A's outcome
// in wdl.
type GameInput struct {
	APoints  int32
	BPoints  int32
	AOutcome string
}

// Service is the bracket component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// ---------------------------------------------------------------------------
// Reading

func pos(v pgtype.Int2) int32 {
	if !v.Valid {
		return 0
	}
	return int32(v.Int16)
}

func optPos(p int32) pgtype.Int2 {
	if p == 0 {
		return pgtype.Int2{}
	}
	return pgtype.Int2{Int16: int16(p), Valid: true} //nolint:gosec // a draw place, 1..500
}

// Load reads the bracket of the component key of event with its matches,
// standings and winners: for the components that use it (the
// celebration), inside their own View or Change. ok is false when the
// bracket was not started.
func Load(ctx context.Context, q *store.Queries, event id.ID, key string) (r Result, ok bool, err error) {
	b, err := q.GetBracket(ctx, store.GetBracketParams{EventID: event.PG(), ComponentKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("eşleşme ağacı: %w", err)
	}
	d, drawn, err := draw.Load(ctx, q, event, b.DrawKey)
	if err != nil {
		return Result{}, false, err
	}
	if !drawn {
		return Result{}, false, fmt.Errorf("eşleşme ağacının kurası yok: %s", b.DrawKey)
	}
	rows, err := q.ListBracketMatches(ctx, store.ListBracketMatchesParams{EventID: event.PG(), ComponentKey: key})
	if err != nil {
		return Result{}, false, fmt.Errorf("maçlar: %w", err)
	}
	games, err := q.ListBracketGames(ctx, store.ListBracketGamesParams{EventID: event.PG(), ComponentKey: key})
	if err != nil {
		return Result{}, false, fmt.Errorf("oyunlar: %w", err)
	}
	byMatch := make(map[id.ID][]Game, len(rows))
	for _, g := range games {
		m := id.ID(g.MatchID.Bytes)
		byMatch[m] = append(byMatch[m], Game{
			Number: int32(g.Number), APoints: g.APoints.Int32, BPoints: g.BPoints.Int32, AOutcome: g.AOutcome.String,
		})
	}
	r = Result{
		Rules:   Rules{Format: b.Format, BestOf: int32(b.BestOf), Result: b.Result, Winner: b.Winner},
		DrawKey: b.DrawKey, Subject: d.Subject, TeamsKey: d.TeamsKey,
		Rounds: int32(b.Rounds), StartedAt: b.StartedAt.Time.UTC(), Entrants: d.Entries,
		Matches: make([]Match, 0, len(rows)),
	}
	for _, row := range rows {
		m := Match{
			ID: id.ID(row.ID.Bytes), Round: int32(row.Round), Slot: int32(row.Slot),
			A: pos(row.APosition), B: pos(row.BPosition), Games: byMatch[id.ID(row.ID.Bytes)],
		}
		if m.Games == nil {
			m.Games = []Game{}
		}
		evaluate(r.Rules, &m)
		r.Matches = append(r.Matches, m)
	}
	finish(&r)
	return r, true, nil
}

// drawKey is the key of the type's first draw component, or "".
func drawKey(def *definition.Definition) string {
	for _, c := range def.Components {
		if c.Kind == definition.CompDraw {
			return c.Key
		}
	}
	return ""
}

// rules are the rules a start would take now: the component's parameters
// and the type's first scoreboard component's way of scoring.
func rules(sc *component.Scope) Rules {
	r := Rules{Format: FormatElimination, BestOf: 1, Result: scoreboard.ResultPoints, Winner: scoreboard.WinnerHigh}
	if f := sc.ParamString(ParamFormat); slices.Contains(definition.BracketFormats, f) {
		r.Format = f
	}
	if n, ok := sc.ParamInt(ParamBestOf); ok && (n == 3 || n == 5) {
		r.BestOf = int32(n) //nolint:gosec // 3 or 5
	}
	for _, c := range sc.Definition.Components {
		if c.Kind != definition.CompScoreboard {
			continue
		}
		if c.Params[scoreboard.ParamResult].String() == scoreboard.ResultWDL {
			r.Result = scoreboard.ResultWDL
		}
		if c.Params[scoreboard.ParamWinner].String() == scoreboard.WinnerLow {
			r.Winner = scoreboard.WinnerLow
		}
		break
	}
	return r
}

// writable is what a change needs besides the caller's role: the checks
// Change makes.
func writable(sc *component.Scope) bool {
	return sc.Enabled && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// denied reports whether err is a refusal of the caller's role.
func denied(err error) bool {
	return errors.Is(err, apperr.ErrPermissionDenied) || errors.Is(err, apperr.ErrMuted)
}

// load reads the component as the caller sees it.
func load(ctx context.Context, sc *component.Scope) (Bracket, error) {
	r, ok, err := Load(ctx, sc.Q(), sc.Event, sc.Key)
	if err != nil {
		return Bracket{}, err
	}
	open := writable(sc)
	out := Bracket{Started: ok, Result: r, CallerCanManage: open && sc.IsManager()}
	if !ok {
		key := drawKey(sc.Definition)
		out.Result = Result{
			Rules: rules(sc), DrawKey: key, Entrants: []draw.Entry{}, Matches: []Match{},
			Standings: []Standing{}, Winners: []int32{},
		}
		if key != "" {
			d, drawn, err := draw.Load(ctx, sc.Q(), sc.Event, key)
			if err != nil {
				return Bracket{}, err
			}
			if drawn {
				out.Drawn = true
				out.Subject, out.TeamsKey, out.Entrants = d.Subject, d.TeamsKey, d.Entries
			}
		}
		return out, nil
	}
	out.Drawn = true
	for i := range out.Matches {
		m := &out.Matches[i]
		if !open || !sc.Started() || m.Decided || m.Round != out.CurrentRound {
			continue
		}
		switch err := sc.AuthorizeReferee(ctx, m.ID); {
		case err == nil:
			m.CallerCanScore = true
		case !denied(err):
			return Bracket{}, err
		}
	}
	return out, nil
}

// Get returns the bracket. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Bracket, error) {
	var out Bracket
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return Bracket{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Changes

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// done records a change: live, audited, and the component as the caller
// now sees it.
func done(ctx context.Context, sc *component.Scope, action string, meta map[string]any, out *Bracket) error {
	sc.Changed()
	if err := sc.Audit(ctx, action, meta); err != nil {
		return err
	}
	var err error
	*out, err = load(ctx, sc)
	return err
}

// addRound writes a round of pairings.
func addRound(ctx context.Context, sc *component.Scope, drawKey string, round int32, pairs []pairing) error {
	for i, p := range pairs {
		if err := sc.Q().AddBracketMatch(ctx, store.AddBracketMatchParams{
			ID: id.New().PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, DrawKey: drawKey,
			Round: int16(round), Slot: int16(i + 1), //nolint:gosec // at most 64 rounds and MaxElimination/2 matches
			APosition: optPos(p[0]), BPosition: optPos(p[1]), CreatedAt: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("maç: %w", err)
		}
	}
	return nil
}

// Start starts the bracket from the draw; managers only. Refused: a
// bracket that was started (apperr.ErrBracketStarted), a draw that was
// not made or a type without one (apperr.ErrBracketDrawRequired), fewer
// than two entrants (apperr.ErrBracketTooFew), more than the format takes
// (apperr.ErrEventLimit) and the refusals of component.Service.Change.
func (s *Service) Start(ctx context.Context, ref component.Ref) (Bracket, error) {
	var out Bracket
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		q := sc.Q()
		// Change locks the event: two starts cannot pass this check
		// together, and the primary key refuses a second row anyway.
		_, made, err := Load(ctx, q, sc.Event, sc.Key)
		if err != nil {
			return err
		}
		if made {
			return apperr.ErrBracketStarted
		}
		key := drawKey(sc.Definition)
		if key == "" {
			return apperr.ErrBracketDrawRequired
		}
		d, drawn, err := draw.Load(ctx, q, sc.Event, key)
		if err != nil {
			return err
		}
		if !drawn {
			return apperr.ErrBracketDrawRequired
		}
		places := make([]int32, 0, len(d.Entries))
		entrants := make([]int32, 0, len(d.Entries))
		for _, e := range d.Entries {
			p := e.Position
			if e.Empty() {
				p = 0
			} else {
				entrants = append(entrants, p)
			}
			places = append(places, p)
		}
		if len(entrants) < 2 {
			return apperr.ErrBracketTooFew
		}
		r := rules(sc)
		var rounds [][]pairing
		var planned int
		switch r.Format {
		case FormatLeague:
			if len(entrants) > MaxLeague {
				return apperr.ErrEventLimit
			}
			rounds = roundRobin(entrants)
			planned = len(rounds)
		case FormatSwiss:
			if len(entrants) > MaxSwiss {
				return apperr.ErrEventLimit
			}
			rounds = [][]pairing{swissRound(entrants, func(int32, int32) bool { return false }, func(int32) bool { return false })}
			planned = log2Ceil(len(entrants))
		default:
			if len(places) > MaxElimination {
				return apperr.ErrEventLimit
			}
			rounds = [][]pairing{consecutive(places)}
			planned = log2Ceil(len(places))
		}
		if err := q.CreateBracket(ctx, store.CreateBracketParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, DrawKey: key, Format: r.Format,
			BestOf: int16(r.BestOf), Result: r.Result, Winner: r.Winner, //nolint:gosec // 1, 3 or 5
			Rounds: int16(max(planned, 1)), StartedAt: ts(sc.Now), //nolint:gosec // at most 19
		}); err != nil {
			return fmt.Errorf("eşleşme ağacı: %w", err)
		}
		for i, pairs := range rounds {
			if err := addRound(ctx, sc, key, int32(i+1), pairs); err != nil { //nolint:gosec // at most 19
				return err
			}
		}
		return done(ctx, sc, AuditStarted, map[string]any{"format": r.Format, "entrants": len(entrants)}, &out)
	})
	if err != nil {
		return Bracket{}, err
	}
	return out, nil
}

// started reads the started bracket; apperr.ErrBracketNotStarted without
// one.
func started(ctx context.Context, sc *component.Scope) (Result, error) {
	r, ok, err := Load(ctx, sc.Q(), sc.Event, sc.Key)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, apperr.ErrBracketNotStarted
	}
	return r, nil
}

// PairRound pairs the next round of an elimination or a Swiss bracket;
// managers only. Refused: a bracket that was not started
// (apperr.ErrBracketNotStarted), an undecided match in the current round
// (apperr.ErrBracketRoundOpen), no next round (apperr.ErrBracketFinished;
// a league's rounds are all paired at the start) and the refusals of
// component.Service.Change.
func (s *Service) PairRound(ctx context.Context, ref component.Ref) (Bracket, error) {
	var out Bracket
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		r, err := started(ctx, sc)
		if err != nil {
			return err
		}
		if r.Finished || r.Format == FormatLeague {
			return apperr.ErrBracketFinished
		}
		if !roundDecided(r.Matches, r.CurrentRound) {
			return apperr.ErrBracketRoundOpen
		}
		var pairs []pairing
		if r.Format == FormatSwiss {
			pairs = swissPairs(r)
		} else {
			pairs = consecutive(advancing(r.Matches, r.CurrentRound))
		}
		next := r.CurrentRound + 1
		if err := addRound(ctx, sc, r.DrawKey, next, pairs); err != nil {
			return err
		}
		return done(ctx, sc, AuditRoundPaired, map[string]any{"round": next, "matches": len(pairs)}, &out)
	})
	if err != nil {
		return Bracket{}, err
	}
	return out, nil
}

// swissPairs pairs a Swiss bracket's next round from its standings.
func swissPairs(r Result) []pairing {
	met := make(map[pairing]bool)
	bye := make(map[int32]bool)
	for _, m := range r.Matches {
		switch {
		case m.A != 0 && m.B != 0:
			met[pairing{m.A, m.B}], met[pairing{m.B, m.A}] = true, true
		case m.A+m.B != 0:
			bye[m.A+m.B] = true
		}
	}
	order := make([]int32, 0, len(r.Standings))
	for _, st := range r.Standings {
		order = append(order, st.Position)
	}
	return swissRound(order, func(a, b int32) bool { return met[pairing{a, b}] }, func(a int32) bool { return bye[a] })
}

// match finds a match of the bracket.
func match(r Result, m id.ID) (Match, error) {
	i := slices.IndexFunc(r.Matches, func(x Match) bool { return x.ID == m })
	if i < 0 {
		return Match{}, apperr.ErrBracketMatchNotFound
	}
	return r.Matches[i], nil
}

// closed reports whether a later round was paired from the match's round:
// its games no longer change. A league's rounds are all paired at the
// start and do not depend on each other.
func closed(r Result, m Match) bool {
	return r.Format != FormatLeague && m.Round < r.CurrentRound
}

// cleanGame checks a game by the bracket's rule: the points of both sides
// (validation error on a_points), or A's outcome in wdl (validation error
// on a_outcome).
func cleanGame(r Rules, in GameInput) error {
	if r.Result == scoreboard.ResultWDL {
		switch in.AOutcome {
		case scoreboard.OutcomeWin, scoreboard.OutcomeDraw, scoreboard.OutcomeLoss:
		default:
			return apperr.Validation("a_outcome")
		}
		if in.APoints != 0 || in.BPoints != 0 {
			return apperr.Validation("a_points")
		}
		return nil
	}
	if in.AOutcome != "" {
		return apperr.Validation("a_outcome")
	}
	if in.APoints < -PointsMax || in.APoints > PointsMax || in.BPoints < -PointsMax || in.BPoints > PointsMax {
		return apperr.Validation("a_points")
	}
	return nil
}

// writeGame writes game number of match m.
func writeGame(ctx context.Context, sc *component.Scope, r Rules, m id.ID, number int32, in GameInput) error {
	p := store.AddBracketGameParams{
		MatchID: m.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key,
		Number: int16(number), CreatedAt: ts(sc.Now), //nolint:gosec // 1..MaxGames
	}
	if r.Result == scoreboard.ResultWDL {
		p.AOutcome = pgtype.Text{String: in.AOutcome, Valid: true}
	} else {
		p.APoints = pgtype.Int4{Int32: in.APoints, Valid: true}
		p.BPoints = pgtype.Int4{Int32: in.BPoints, Valid: true}
	}
	if err := sc.Q().AddBracketGame(ctx, p); err != nil {
		return fmt.Errorf("oyun: %w", err)
	}
	return nil
}

// deleteGame deletes game number of match m; apperr.ErrBracketGameNotFound
// when there is none.
func deleteGame(ctx context.Context, sc *component.Scope, m id.ID, number int32) error {
	n, err := sc.Q().DeleteBracketGame(ctx, store.DeleteBracketGameParams{
		MatchID: m.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, Number: int16(number), //nolint:gosec // 1..MaxGames
	})
	if err != nil {
		return fmt.Errorf("oyun: %w", err)
	}
	if n == 0 {
		return apperr.ErrBracketGameNotFound
	}
	return nil
}

// AddGame enters the next game of an undecided match; managers and the
// match's referees, once the event has started. Refused: a bad game
// (validation error on a_points or a_outcome), a bracket that was not
// started (apperr.ErrBracketNotStarted), a match it does not have
// (apperr.ErrBracketMatchNotFound), a decided match or a bye
// (apperr.ErrBracketMatchDecided), a match of a closed round
// (apperr.ErrBracketRoundClosed), more than MaxGames games
// (apperr.ErrEventLimit), an event that has not started
// (apperr.ErrScoreNotStarted) and the refusals of component.Service.Change.
func (s *Service) AddGame(ctx context.Context, ref component.Ref, m id.ID, in GameInput) (Bracket, error) {
	var out Bracket
	err := s.fw.Change(ctx, ref, Kind, component.ByReferee(m), func(sc *component.Scope) error {
		if !sc.Started() {
			return apperr.ErrScoreNotStarted
		}
		r, err := started(ctx, sc)
		if err != nil {
			return err
		}
		x, err := match(r, m)
		if err != nil {
			return err
		}
		if closed(r, x) {
			return apperr.ErrBracketRoundClosed
		}
		if x.Decided {
			return apperr.ErrBracketMatchDecided
		}
		if len(x.Games) >= MaxGames {
			return apperr.ErrEventLimit
		}
		if err := cleanGame(r.Rules, in); err != nil {
			return err
		}
		number := int32(len(x.Games) + 1) //nolint:gosec // at most MaxGames
		if err := writeGame(ctx, sc, r.Rules, m, number, in); err != nil {
			return err
		}
		return done(ctx, sc, AuditGameAdded, map[string]any{"match_id": m.String(), "game": number}, &out)
	})
	if err != nil {
		return Bracket{}, err
	}
	return out, nil
}

// CorrectGame replaces an entered game; managers only (ADR-0025 §5), on a
// decided match too. Refused: a game the match does not have
// (apperr.ErrBracketGameNotFound), a match of a closed round
// (apperr.ErrBracketRoundClosed), a bad game and the refusals of AddGame
// but the event's start and the decided match.
func (s *Service) CorrectGame(ctx context.Context, ref component.Ref, m id.ID, number int32, in GameInput) (Bracket, error) {
	if number < 1 || number > MaxGames {
		return Bracket{}, apperr.Validation("game")
	}
	var out Bracket
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		r, err := started(ctx, sc)
		if err != nil {
			return err
		}
		x, err := match(r, m)
		if err != nil {
			return err
		}
		if closed(r, x) {
			return apperr.ErrBracketRoundClosed
		}
		if err := cleanGame(r.Rules, in); err != nil {
			return err
		}
		if err := deleteGame(ctx, sc, m, number); err != nil {
			return err
		}
		if err := writeGame(ctx, sc, r.Rules, m, number, in); err != nil {
			return err
		}
		return done(ctx, sc, AuditGameCorrected, map[string]any{"match_id": m.String(), "game": number}, &out)
	})
	if err != nil {
		return Bracket{}, err
	}
	return out, nil
}

// DeleteGame deletes the last game of a match; managers only. Refused: a
// game that is not the match's last (apperr.ErrBracketGameNotFound), a
// match of a closed round (apperr.ErrBracketRoundClosed) and the refusals
// of CorrectGame.
func (s *Service) DeleteGame(ctx context.Context, ref component.Ref, m id.ID, number int32) (Bracket, error) {
	if number < 1 || number > MaxGames {
		return Bracket{}, apperr.Validation("game")
	}
	var out Bracket
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		r, err := started(ctx, sc)
		if err != nil {
			return err
		}
		x, err := match(r, m)
		if err != nil {
			return err
		}
		if closed(r, x) {
			return apperr.ErrBracketRoundClosed
		}
		if int(number) != len(x.Games) {
			return apperr.ErrBracketGameNotFound
		}
		if err := deleteGame(ctx, sc, m, number); err != nil {
			return err
		}
		return done(ctx, sc, AuditGameDeleted, map[string]any{"match_id": m.String(), "game": number}, &out)
	})
	if err != nil {
		return Bracket{}, err
	}
	return out, nil
}

// Reset deletes the started bracket with its matches and games, and the
// referee assignments to its matches; managers only. The draw stays.
// Refused: a bracket that was not started (apperr.ErrBracketNotStarted)
// and the refusals of component.Service.Change.
func (s *Service) Reset(ctx context.Context, ref component.Ref) (Bracket, error) {
	var out Bracket
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		r, err := started(ctx, sc)
		if err != nil {
			return err
		}
		for _, m := range r.Matches {
			if err := sc.RemoveScopeReferees(ctx, m.ID); err != nil {
				return err
			}
		}
		if err := sc.Q().DeleteBracket(ctx, store.DeleteBracketParams{EventID: sc.Event.PG(), ComponentKey: sc.Key}); err != nil {
			return fmt.Errorf("eşleşme ağacı: %w", err)
		}
		return done(ctx, sc, AuditReset, map[string]any{"format": r.Format, "matches": len(r.Matches)}, &out)
	})
	if err != nil {
		return Bracket{}, err
	}
	return out, nil
}
