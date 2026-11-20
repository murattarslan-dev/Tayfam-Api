// Package scoreboard is the scoreboard component (kind "scoreboard", issue
// #250; ADR-0025 §4–§6, ADR-0037 §5): tables where a game is played and
// scored round by round, live.
//
// A component holds up to MaxTables tables (one when its `tables`
// parameter is off). A table seats players (participants of the event, and
// the event's guest players when the `guests` parameter is on; package
// guest, issue #252) or, in a team scoreboard, the teams of a teams
// component of the event (teams.Load). Its rules are resolved when it is created and kept on the
// table, so that a later change of the catalog does not change a game that
// was played:
//
//	side      player (default) or team
//	rounds    round (default; round by round), single (one match) or
//	          first_to (until a total reaches the target)
//	winner    high (default), low or first (the first to reach the target)
//	result    points (default) or wdl (win 1, draw ½, loss 0)
//	target    the target points; first_to and first need one
//	best_of   3 or 5: the first to win more than half of the games
//
// Each comes from the component's parameter (the organizer's choice where
// the definition leaves it to them), overridden by the table's preset and,
// for the target, by the table itself. A preset is catalog data
// (catalog_score_presets: Batak, 101 Okey …); the component's `presets`
// parameter lists the ones its tables may use, and a table of such a
// component must name one.
//
// A table is finished when its rounds decide it (Decided) or when a referee
// or manager says so; a manager may reopen it. Who may do what (ADR-0025
// §5; the framework's roles, package component):
//
//	read                         every active member who may see the
//	                             event's components
//	create, rename, reopen,
//	delete a table, seat players managers (the organizer, and moderators
//	                             and admins as for every event)
//	enter a round, finish        managers and the referees assigned to the
//	                             table, the component or the event
//	correct or delete a round    managers
//
// Every change is live (event.component_changed, ADR-0025 §6). Rounds are
// entered from the event's start until the write window closes. A person's
// seats and scores are exported; account deletion empties the seat and
// keeps the scores ("deleted user", ADR-0025 §11). Tables are attendance,
// not the plan: a series occurrence does not take them.
package scoreboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/guest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/teams"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompScoreboard

// The component's parameters (definition).
const (
	ParamSide    = "side"
	ParamTables  = "tables"
	ParamPresets = "presets"
	ParamRounds  = "rounds"
	ParamTarget  = "target"
	ParamWinner  = "winner"
	ParamResult  = "result"
	ParamBestOf  = "best_of"
)

// Rule values (definition.ScoreSides, ScoreRounds, ScoreWinners,
// ScoreResults).
const (
	SidePlayer    = "player"
	SideTeam      = "team"
	RoundsRound   = "round"
	RoundsSingle  = "single"
	RoundsFirstTo = "first_to"
	WinnerHigh    = "high"
	WinnerLow     = "low"
	WinnerFirst   = "first"
	ResultPoints  = "points"
	ResultWDL     = "wdl"
)

// Outcomes of a wdl score.
const (
	OutcomeWin  = "win"
	OutcomeDraw = "draw"
	OutcomeLoss = "loss"
)

// Table statuses.
const (
	StatusOpen     = "open"
	StatusFinished = "finished"
)

// Limits; the same CHECKs are on the tables (00045).
const (
	MaxTables  = 20
	LabelMax   = 30
	MaxPlayers = 50
	MaxRounds  = 200
	PointsMax  = 1_000_000
	TargetMax  = 100_000
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// Audit actions of the component's changes. The records hold IDs, the
// preset and counts only, never a score.
const (
	AuditTableCreated  = "event.score_table_created"
	AuditTableUpdated  = "event.score_table_updated"
	AuditTableDeleted  = "event.score_table_deleted"
	AuditPlayersSet    = "event.score_players_set"
	AuditRoundAdded    = "event.score_round_added"
	AuditRoundCorrect  = "event.score_round_corrected"
	AuditRoundDeleted  = "event.score_round_deleted"
	AuditTableFinished = "event.score_table_finished"
)

// Rules are a table's rules; a zero Target or BestOf is none.
type Rules struct {
	Side   string
	Rounds string
	Winner string
	Result string
	Target int32
	BestOf int32
}

// Preset is a catalog preset. Its rules are partial: an empty string or a
// zero leaves the rule to the component.
type Preset struct {
	Key    string
	Labels map[string]string
	// PlayersMin and PlayersMax bound the seats of a table; 0 for no bound.
	PlayersMin int32
	PlayersMax int32
	Rules      Rules
	Retired    bool
}

// Player is a seat at a table: a person, a team or a guest player. All are
// zero for a seat whose person deleted their account or whose team was
// removed. A guest's seat keeps Guest when its nickname is cleared.
type Player struct {
	ID       id.ID
	Position int32
	User     id.ID
	Team     id.ID
	Guest    id.ID
}

// Score is a seat's score in a round: Points, or Outcome in wdl mode.
type Score struct {
	Player  id.ID
	Points  int32
	Outcome string
}

// Round is one round (hand, game) of a table.
type Round struct {
	// Number is 1-based.
	Number int32
	Scores []Score
}

// Table is one table of a component with its seats, rounds and result.
type Table struct {
	ID    id.ID
	Label string
	// Preset is the preset's key; empty without one.
	Preset string
	Rules  Rules
	// TeamsKey is the teams component a team table seats teams of.
	TeamsKey   string
	Status     string
	FinishedAt time.Time
	CreatedAt  time.Time
	Players    []Player
	Rounds     []Round
	Result     Result
	// CallerCanScore reports whether the caller may enter a round or finish
	// the table now (Get only).
	CallerCanScore bool
}

// Scoreboard is one scoreboard component of an event, as the caller sees
// it.
type Scoreboard struct {
	// Tables in the order they were created.
	Tables []Table
	// Presets are the presets the component offers and those its tables
	// use, in catalog order.
	Presets []Preset
	// Rules are the component's own rules, before a preset.
	Rules Rules
	// MaxTables is 1 when the component has no `tables` parameter on.
	MaxTables int32
	// CallerCanManage reports whether the caller may manage the tables and
	// correct scores now.
	CallerCanManage bool
}

// PlayerInput is a seat to set: a person (User), a team (Team) or a guest
// player (Guest).
type PlayerInput struct {
	User  id.ID
	Team  id.ID
	Guest id.ID
}

// TableInput is a new table.
type TableInput struct {
	Label string
	// Preset is a preset key; required when the component offers presets.
	Preset string
	// Target overrides the target points; 0 keeps the rules'.
	Target int32
	// TeamsKey names the teams component a team table seats teams of.
	TeamsKey string
	// Players may be empty: they are seated later (SetPlayers).
	Players []PlayerInput
}

// TableUpdate changes a table; nil and "" leave a field as it is.
type TableUpdate struct {
	Label *string
	// Status is StatusOpen (reopen) or StatusFinished.
	Status string
}

// ScoreInput is a seat's score in a round to enter.
type ScoreInput struct {
	Player  id.ID
	Points  int32
	Outcome string
}

// Service is the scoreboard component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// ---------------------------------------------------------------------------
// Rules and presets

// ruleOf returns a string parameter when it is one of allowed, or def.
func ruleOf(sc *component.Scope, name string, allowed []string, def string) string {
	if v := sc.ParamString(name); slices.Contains(allowed, v) {
		return v
	}
	return def
}

// componentRules are the component's rules as they hold for the event.
func componentRules(sc *component.Scope) Rules {
	r := Rules{
		Side:   ruleOf(sc, ParamSide, definition.ScoreSides, SidePlayer),
		Rounds: ruleOf(sc, ParamRounds, definition.ScoreRounds, RoundsRound),
		Winner: ruleOf(sc, ParamWinner, definition.ScoreWinners, WinnerHigh),
		Result: ruleOf(sc, ParamResult, definition.ScoreResults, ResultPoints),
	}
	if n, ok := sc.ParamInt(ParamTarget); ok && n >= 1 && n <= TargetMax {
		r.Target = int32(n) //nolint:gosec // 1..TargetMax
	}
	if n, ok := sc.ParamInt(ParamBestOf); ok && (n == 3 || n == 5) {
		r.BestOf = int32(n) //nolint:gosec // 3 or 5
	}
	return r
}

// overlay returns r with the rules p sets.
func overlay(r, p Rules) Rules {
	if p.Side != "" {
		r.Side = p.Side
	}
	if p.Rounds != "" {
		r.Rounds = p.Rounds
	}
	if p.Winner != "" {
		r.Winner = p.Winner
	}
	if p.Result != "" {
		r.Result = p.Result
	}
	if p.Target != 0 {
		r.Target = p.Target
	}
	if p.BestOf != 0 {
		r.BestOf = p.BestOf
	}
	return r
}

// offered are the preset keys the component offers.
func offered(sc *component.Scope) []string {
	p, _ := sc.Param(ParamPresets)
	keys, _ := p.Value.([]string)
	return keys
}

// presetParams is the stored form of a preset's rules.
type presetParams struct {
	PlayersMin *int32  `json:"players_min"`
	PlayersMax *int32  `json:"players_max"`
	Side       *string `json:"side"`
	Rounds     *string `json:"rounds"`
	Target     *int32  `json:"target"`
	Winner     *string `json:"winner"`
	Result     *string `json:"result"`
	BestOf     *int32  `json:"best_of"`
}

// enum checks an optional enumerated rule.
func enum(v *string, allowed []string) (string, bool) {
	if v == nil {
		return "", true
	}
	return *v, slices.Contains(allowed, *v)
}

// ParsePreset reads a preset row: the labels (Turkish required) and the
// rules. A preset that breaks the schema is an error: the catalog is data
// and is checked where it is read.
func ParsePreset(key string, labels, params []byte, status string) (Preset, error) {
	out := Preset{Key: key, Retired: status != "active"}
	if err := json.Unmarshal(labels, &out.Labels); err != nil || out.Labels["tr"] == "" {
		return Preset{}, fmt.Errorf("ön ayar %s: adlar bozuk", key)
	}
	var p presetParams
	dec := json.NewDecoder(strings.NewReader(string(params)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Preset{}, fmt.Errorf("ön ayar %s: kurallar bozuk: %w", key, err)
	}
	var ok [4]bool
	out.Rules.Side, ok[0] = enum(p.Side, definition.ScoreSides)
	out.Rules.Rounds, ok[1] = enum(p.Rounds, definition.ScoreRounds)
	out.Rules.Winner, ok[2] = enum(p.Winner, definition.ScoreWinners)
	out.Rules.Result, ok[3] = enum(p.Result, definition.ScoreResults)
	if ok != [4]bool{true, true, true, true} {
		return Preset{}, fmt.Errorf("ön ayar %s: kural değeri bozuk", key)
	}
	if p.Target != nil {
		if *p.Target < 1 || *p.Target > TargetMax {
			return Preset{}, fmt.Errorf("ön ayar %s: hedef bozuk", key)
		}
		out.Rules.Target = *p.Target
	}
	if p.BestOf != nil {
		if *p.BestOf != 3 && *p.BestOf != 5 {
			return Preset{}, fmt.Errorf("ön ayar %s: best_of bozuk", key)
		}
		out.Rules.BestOf = *p.BestOf
	}
	if p.PlayersMin != nil {
		out.PlayersMin = *p.PlayersMin
	}
	if p.PlayersMax != nil {
		out.PlayersMax = *p.PlayersMax
	}
	if out.PlayersMin < 0 || out.PlayersMax < 0 || out.PlayersMax > MaxPlayers ||
		(out.PlayersMax > 0 && out.PlayersMin > out.PlayersMax) {
		return Preset{}, fmt.Errorf("ön ayar %s: oyuncu sayısı bozuk", key)
	}
	return out, nil
}

// LoadPresets reads the presets with the given keys, in catalog order.
// Unknown keys are left out.
func LoadPresets(ctx context.Context, q *store.Queries, keys []string) ([]Preset, error) {
	if len(keys) == 0 {
		return []Preset{}, nil
	}
	rows, err := q.ListScorePresets(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("ön ayarlar: %w", err)
	}
	out := make([]Preset, 0, len(rows))
	for _, r := range rows {
		p, err := ParsePreset(r.Key, r.Labels, r.Params, r.Status)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Reading

func optID(u pgtype.UUID) id.ID {
	if !u.Valid {
		return id.ID{}
	}
	return id.ID(u.Bytes)
}

func tableOf(r store.EventScoreTable) Table {
	t := Table{
		ID: id.ID(r.ID.Bytes), Label: r.Label, Preset: r.PresetKey.String,
		Rules:    Rules{Side: r.Side, Rounds: r.Rounds, Winner: r.Winner, Result: r.Result},
		TeamsKey: r.TeamKey.String, Status: r.Status, CreatedAt: r.CreatedAt.Time,
		Players: []Player{}, Rounds: []Round{},
	}
	if r.Target.Valid {
		t.Rules.Target = r.Target.Int32
	}
	if r.BestOf.Valid {
		t.Rules.BestOf = int32(r.BestOf.Int16)
	}
	if r.FinishedAt.Valid {
		t.FinishedAt = r.FinishedAt.Time
	}
	return t
}

// Load reads the tables of the scoreboard component key of event with
// their seats, rounds and results: for the components that build on the
// scoreboard (the bracket, the celebration), inside their own View or
// Change.
func Load(ctx context.Context, q *store.Queries, event id.ID, key string) ([]Table, error) {
	rows, err := q.ListScoreTables(ctx, store.ListScoreTablesParams{EventID: event.PG(), ComponentKey: key})
	if err != nil {
		return nil, fmt.Errorf("masalar: %w", err)
	}
	out := make([]Table, 0, len(rows))
	at := make(map[id.ID]int, len(rows))
	for _, r := range rows {
		t := tableOf(r)
		at[t.ID] = len(out)
		out = append(out, t)
	}
	players, err := q.ListScorePlayers(ctx, store.ListScorePlayersParams{EventID: event.PG(), ComponentKey: key})
	if err != nil {
		return nil, fmt.Errorf("masa oyuncuları: %w", err)
	}
	for _, p := range players {
		if i, ok := at[id.ID(p.TableID.Bytes)]; ok {
			out[i].Players = append(out[i].Players, Player{
				ID: id.ID(p.ID.Bytes), Position: int32(p.Position), User: optID(p.UserID), Team: optID(p.TeamID),
				Guest: optID(p.GuestID),
			})
		}
	}
	scores, err := q.ListScores(ctx, store.ListScoresParams{EventID: event.PG(), ComponentKey: key})
	if err != nil {
		return nil, fmt.Errorf("skorlar: %w", err)
	}
	for _, s := range scores {
		i, ok := at[id.ID(s.TableID.Bytes)]
		if !ok {
			continue
		}
		t := &out[i]
		n := int32(s.Round)
		if len(t.Rounds) == 0 || t.Rounds[len(t.Rounds)-1].Number != n {
			t.Rounds = append(t.Rounds, Round{Number: n, Scores: []Score{}})
		}
		rd := &t.Rounds[len(t.Rounds)-1]
		rd.Scores = append(rd.Scores, Score{Player: id.ID(s.PlayerID.Bytes), Points: s.Points.Int32, Outcome: s.Outcome.String})
	}
	for i := range out {
		t := &out[i]
		t.Result = Compute(t.Rules, t.Players, t.Rounds, t.Status == StatusFinished)
	}
	return out, nil
}

// writable is what a change needs besides the caller's role: the checks
// Change makes.
func writable(sc *component.Scope) bool {
	return sc.Enabled && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// maxTables is the component's table limit.
func maxTables(sc *component.Scope) int {
	if sc.ParamBool(ParamTables) {
		return MaxTables
	}
	return 1
}

// denied reports whether err is a refusal of the caller's role.
func denied(err error) bool {
	return errors.Is(err, apperr.ErrPermissionDenied) || errors.Is(err, apperr.ErrMuted)
}

// load reads the component as the caller sees it.
func load(ctx context.Context, sc *component.Scope) (Scoreboard, error) {
	tables, err := Load(ctx, sc.Q(), sc.Event, sc.Key)
	if err != nil {
		return Scoreboard{}, err
	}
	keys := slices.Clone(offered(sc))
	for _, t := range tables {
		if t.Preset != "" && !slices.Contains(keys, t.Preset) {
			keys = append(keys, t.Preset)
		}
	}
	presets, err := LoadPresets(ctx, sc.Q(), keys)
	if err != nil {
		return Scoreboard{}, err
	}
	open := writable(sc)
	for i := range tables {
		t := &tables[i]
		if !open || !sc.Started() || t.Status != StatusOpen {
			continue
		}
		switch err := sc.AuthorizeReferee(ctx, t.ID); {
		case err == nil:
			t.CallerCanScore = true
		case !denied(err):
			return Scoreboard{}, err
		}
	}
	return Scoreboard{
		Tables: tables, Presets: presets, Rules: componentRules(sc),
		MaxTables:       int32(maxTables(sc)), //nolint:gosec // 1 or MaxTables
		CallerCanManage: open && sc.IsManager(),
	}, nil
}

// Get returns the component. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Scoreboard, error) {
	var out Scoreboard
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return Scoreboard{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Tables

// cleanLabel trims a table's label and refuses an empty one, invalid
// UTF-8, control characters and more than LabelMax characters.
func cleanLabel(s string) (string, error) {
	if len(s) > (LabelMax+64)*maxRuneBytes {
		return "", apperr.Validation("label")
	}
	t := strings.TrimSpace(s)
	if t == "" || !utf8.ValidString(t) || strings.IndexFunc(t, unicode.IsControl) >= 0 || utf8.RuneCountInString(t) > LabelMax {
		return "", apperr.Validation("label")
	}
	return t, nil
}

// who returns the one person, team or guest a seat names; ok is false
// unless exactly one is set.
func (p PlayerInput) who() (w id.ID, ok bool) {
	n := 0
	for _, v := range []id.ID{p.User, p.Team, p.Guest} {
		if v != (id.ID{}) {
			w, n = v, n+1
		}
	}
	return w, n == 1
}

// cleanPlayers checks the form of seats: each a person, a team or a guest,
// none twice, at most MaxPlayers.
func cleanPlayers(in []PlayerInput) error {
	if len(in) > MaxPlayers {
		return apperr.Validation("players")
	}
	seen := make(map[id.ID]bool, len(in))
	for _, p := range in {
		who, ok := p.who()
		if !ok {
			return apperr.Validation("players")
		}
		if seen[who] {
			return apperr.Validation("players")
		}
		seen[who] = true
	}
	return nil
}

// seatable checks seats against a table's side, the preset's bounds and
// the event: people must be participants, teams must be teams of the
// table's teams component, guests must be the event's guest players and
// the component must take guests (`guests`).
func seatable(ctx context.Context, sc *component.Scope, side, teamsKey string, bounds Preset, in []PlayerInput) error {
	if len(in) == 0 {
		return nil
	}
	if (bounds.PlayersMin > 0 && len(in) < int(bounds.PlayersMin)) || (bounds.PlayersMax > 0 && len(in) > int(bounds.PlayersMax)) {
		return apperr.Validation("players")
	}
	ok := map[id.ID]bool{}
	var guests map[id.ID]bool
	if side == SideTeam {
		list, err := teams.Load(ctx, sc.Q(), sc.Event, teamsKey)
		if err != nil {
			return err
		}
		for _, t := range list {
			ok[t.ID] = true
		}
	} else {
		rows, err := sc.Q().ListTeamCandidates(ctx, sc.Event.PG())
		if err != nil {
			return fmt.Errorf("katılımcılar: %w", err)
		}
		for _, r := range rows {
			ok[id.ID(r.Bytes)] = true
		}
	}
	for _, p := range in {
		who := p.User
		switch {
		case side == SideTeam:
			if p.Team == (id.ID{}) {
				return apperr.Validation("players")
			}
			who = p.Team
		case p.Guest != (id.ID{}):
			if !sc.ParamBool(guest.Param) {
				return apperr.ErrGuestPlayersDisabled
			}
			if guests == nil {
				var err error
				if guests, err = guest.IDs(ctx, sc.Q(), sc.Event); err != nil {
					return err
				}
			}
			if !guests[p.Guest] {
				return apperr.ErrGuestPlayerNotFound
			}
			continue
		case p.User == (id.ID{}):
			return apperr.Validation("players")
		}
		if !ok[who] {
			return apperr.ErrScorePlayerNotParticipant
		}
	}
	return nil
}

func optText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// seat writes a table's seats in order.
func seat(ctx context.Context, sc *component.Scope, table id.ID, teamsKey string, in []PlayerInput) error {
	for i, p := range in {
		row := store.AddScorePlayerParams{
			ID: id.New().PG(), TableID: table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key,
			Position: int16(i + 1), CreatedAt: ts(sc.Now), //nolint:gosec // 1..MaxPlayers
		}
		switch {
		case p.Team != (id.ID{}):
			row.TeamKey, row.TeamID = optText(teamsKey), p.Team.PG()
		case p.Guest != (id.ID{}):
			row.GuestID = p.Guest.PG()
		default:
			row.UserID = p.User.PG()
		}
		if err := sc.Q().AddScorePlayer(ctx, row); err != nil {
			return fmt.Errorf("masa oyuncusu: %w", err)
		}
	}
	return nil
}

// presetFor resolves the preset a new table names: required and one of
// the offered, active presets when the component offers any; refused when
// it offers none.
func presetFor(ctx context.Context, sc *component.Scope, key string) (Preset, error) {
	keys := offered(sc)
	if len(keys) == 0 {
		if key != "" {
			return Preset{}, apperr.ErrScorePresetUnavailable
		}
		return Preset{}, nil
	}
	if !slices.Contains(keys, key) {
		return Preset{}, apperr.ErrScorePresetUnavailable
	}
	list, err := LoadPresets(ctx, sc.Q(), []string{key})
	if err != nil {
		return Preset{}, err
	}
	if len(list) != 1 || list[0].Retired {
		return Preset{}, apperr.ErrScorePresetUnavailable
	}
	return list[0], nil
}

// CreateTable adds a table with its rules resolved and its seats;
// managers only. Refused: a bad label, target, teams key or seats
// (validation errors), a full component (apperr.ErrEventLimit), a preset
// the component does not offer (apperr.ErrScorePresetUnavailable), a
// person who is not a participant or a team that is not one
// (apperr.ErrScorePlayerNotParticipant), a guest in a component without
// guests (apperr.ErrGuestPlayersDisabled) or not of the event
// (apperr.ErrGuestPlayerNotFound) and the refusals of
// component.Service.Change.
func (s *Service) CreateTable(ctx context.Context, ref component.Ref, in TableInput) (Scoreboard, error) {
	label, err := cleanLabel(in.Label)
	if err != nil {
		return Scoreboard{}, err
	}
	switch {
	case in.Preset != "" && !definition.ValidKey(in.Preset):
		return Scoreboard{}, apperr.Validation("preset_key")
	case in.Target < 0 || in.Target > TargetMax:
		return Scoreboard{}, apperr.Validation("target")
	case in.TeamsKey != "" && !definition.ValidKey(in.TeamsKey):
		return Scoreboard{}, apperr.Validation("teams_key")
	}
	if err := cleanPlayers(in.Players); err != nil {
		return Scoreboard{}, err
	}
	var out Scoreboard
	err = s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		n, err := sc.Q().CountScoreTables(ctx, store.CountScoreTablesParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("masa sayısı: %w", err)
		}
		if n >= int64(maxTables(sc)) {
			return apperr.ErrEventLimit
		}
		preset, err := presetFor(ctx, sc, in.Preset)
		if err != nil {
			return err
		}
		rules := overlay(componentRules(sc), preset.Rules)
		if in.Target > 0 {
			rules.Target = in.Target
		}
		if rules.Target == 0 && (rules.Rounds == RoundsFirstTo || rules.Winner == WinnerFirst) {
			return apperr.Validation("target")
		}
		teamsKey := ""
		if rules.Side == SideTeam {
			comp, ok := sc.Definition.Component(in.TeamsKey)
			if in.TeamsKey == "" || !ok || comp.Kind != definition.CompTeams {
				return apperr.Validation("teams_key")
			}
			teamsKey = in.TeamsKey
		} else if in.TeamsKey != "" {
			return apperr.Validation("teams_key")
		}
		if err := seatable(ctx, sc, rules.Side, teamsKey, preset, in.Players); err != nil {
			return err
		}
		table := id.New()
		row := store.CreateScoreTableParams{
			ID: table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, Label: label,
			PresetKey: optText(preset.Key), Side: rules.Side, Rounds: rules.Rounds, Winner: rules.Winner,
			Result: rules.Result, TeamKey: optText(teamsKey), Now: ts(sc.Now),
		}
		if rules.Target > 0 {
			row.Target = pgtype.Int4{Int32: rules.Target, Valid: true}
		}
		if rules.BestOf > 1 {
			row.BestOf = pgtype.Int2{Int16: int16(rules.BestOf), Valid: true} //nolint:gosec // 3 or 5
		}
		if err := sc.Q().CreateScoreTable(ctx, row); err != nil {
			return fmt.Errorf("masa: %w", err)
		}
		if err := seat(ctx, sc, table, teamsKey, in.Players); err != nil {
			return err
		}
		return done(ctx, sc, AuditTableCreated, map[string]any{
			"table_id": table.String(), "preset_key": preset.Key, "players": len(in.Players),
		}, &out)
	})
	if err != nil {
		return Scoreboard{}, err
	}
	return out, nil
}

// done marks the change, writes its audit record and reads the result for
// the caller.
func done(ctx context.Context, sc *component.Scope, action string, meta map[string]any, out *Scoreboard) error {
	sc.Changed()
	if err := sc.Audit(ctx, action, meta); err != nil {
		return err
	}
	var err error
	*out, err = load(ctx, sc)
	return err
}

// lockTable reads a table of the component for a change, locking it.
func lockTable(ctx context.Context, sc *component.Scope, table id.ID) (Table, error) {
	r, err := sc.Q().GetScoreTableForUpdate(ctx, store.GetScoreTableForUpdateParams{
		ID: table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Table{}, apperr.ErrScoreTableNotFound
	case err != nil:
		return Table{}, fmt.Errorf("masa: %w", err)
	}
	return tableOf(r), nil
}

// tableWith reads a table of the component with its seats and rounds.
func tableWith(ctx context.Context, sc *component.Scope, table id.ID) (Table, error) {
	all, err := Load(ctx, sc.Q(), sc.Event, sc.Key)
	if err != nil {
		return Table{}, err
	}
	for _, t := range all {
		if t.ID == table {
			return t, nil
		}
	}
	return Table{}, apperr.ErrScoreTableNotFound
}

// setStatus writes a table's label and status.
func setStatus(ctx context.Context, sc *component.Scope, t Table, label, status string) error {
	p := store.UpdateScoreTableParams{
		Label: label, Status: status, Now: ts(sc.Now), ID: t.ID.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key,
	}
	if status == StatusFinished {
		at := t.FinishedAt
		if t.Status != StatusFinished || at.IsZero() {
			at = sc.Now
		}
		p.FinishedAt = ts(at)
	}
	if err := sc.Q().UpdateScoreTable(ctx, p); err != nil {
		return fmt.Errorf("masa güncelleme: %w", err)
	}
	return nil
}

// UpdateTable renames, reopens or finishes a table; managers only.
// Refused: a bad label or status (validation errors), a table the
// component does not have (apperr.ErrScoreTableNotFound) and the refusals
// of component.Service.Change.
func (s *Service) UpdateTable(ctx context.Context, ref component.Ref, table id.ID, in TableUpdate) (Scoreboard, error) {
	label := ""
	if in.Label != nil {
		var err error
		if label, err = cleanLabel(*in.Label); err != nil {
			return Scoreboard{}, err
		}
	}
	if in.Status != "" && in.Status != StatusOpen && in.Status != StatusFinished {
		return Scoreboard{}, apperr.Validation("status")
	}
	var out Scoreboard
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		t, err := lockTable(ctx, sc, table)
		if err != nil {
			return err
		}
		if in.Label == nil {
			label = t.Label
		}
		status := t.Status
		if in.Status != "" {
			status = in.Status
		}
		if err := setStatus(ctx, sc, t, label, status); err != nil {
			return err
		}
		return done(ctx, sc, AuditTableUpdated, map[string]any{"table_id": table.String(), "status": status}, &out)
	})
	if err != nil {
		return Scoreboard{}, err
	}
	return out, nil
}

// DeleteTable removes a table with its seats, scores and referee
// assignments; managers only. Refused: a table the component does not
// have (apperr.ErrScoreTableNotFound) and the refusals of
// component.Service.Change.
func (s *Service) DeleteTable(ctx context.Context, ref component.Ref, table id.ID) (Scoreboard, error) {
	var out Scoreboard
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		n, err := sc.Q().DeleteScoreTable(ctx, store.DeleteScoreTableParams{ID: table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("masa silme: %w", err)
		}
		if n == 0 {
			return apperr.ErrScoreTableNotFound
		}
		if err := sc.RemoveScopeReferees(ctx, table); err != nil {
			return err
		}
		return done(ctx, sc, AuditTableDeleted, map[string]any{"table_id": table.String()}, &out)
	})
	if err != nil {
		return Scoreboard{}, err
	}
	return out, nil
}

// SetPlayers replaces a table's seats, in order, until its first round;
// managers only. An empty list clears them. Refused: bad seats or more
// than the preset allows (validation error on players), a table with
// scores (apperr.ErrScoreTableStarted), a table the component does not
// have (apperr.ErrScoreTableNotFound), a person who is not a participant
// or a team that is not one (apperr.ErrScorePlayerNotParticipant), a guest
// as in CreateTable and the refusals of component.Service.Change.
func (s *Service) SetPlayers(ctx context.Context, ref component.Ref, table id.ID, in []PlayerInput) (Scoreboard, error) {
	if err := cleanPlayers(in); err != nil {
		return Scoreboard{}, err
	}
	var out Scoreboard
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		t, err := lockTable(ctx, sc, table)
		if err != nil {
			return err
		}
		n, err := sc.Q().CountTableScores(ctx, store.CountTableScoresParams{TableID: table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("masa skorları: %w", err)
		}
		if n > 0 {
			return apperr.ErrScoreTableStarted
		}
		var bounds Preset
		if t.Preset != "" {
			list, err := LoadPresets(ctx, sc.Q(), []string{t.Preset})
			if err != nil {
				return err
			}
			if len(list) == 1 {
				bounds = list[0]
			}
		}
		if err := seatable(ctx, sc, t.Rules.Side, t.TeamsKey, bounds, in); err != nil {
			return err
		}
		if err := sc.Q().DeleteScorePlayers(ctx, store.DeleteScorePlayersParams{TableID: table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key}); err != nil {
			return fmt.Errorf("masa oyuncularını silme: %w", err)
		}
		if err := seat(ctx, sc, table, t.TeamsKey, in); err != nil {
			return err
		}
		return done(ctx, sc, AuditPlayersSet, map[string]any{"table_id": table.String(), "players": len(in)}, &out)
	})
	if err != nil {
		return Scoreboard{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Rounds

// cleanScores checks the form of a round's scores: at least one, each for
// a different seat, points within ±PointsMax (wdl: a known outcome and no
// points).
func cleanScores(in []ScoreInput) error {
	if len(in) == 0 || len(in) > MaxPlayers {
		return apperr.Validation("scores")
	}
	seen := make(map[id.ID]bool, len(in))
	for _, s := range in {
		if s.Player == (id.ID{}) || seen[s.Player] || s.Points < -PointsMax || s.Points > PointsMax {
			return apperr.Validation("scores")
		}
		if s.Outcome != "" && s.Outcome != OutcomeWin && s.Outcome != OutcomeDraw && s.Outcome != OutcomeLoss {
			return apperr.Validation("scores")
		}
		seen[s.Player] = true
	}
	return nil
}

// checkRound checks a round's scores against the table: seats of the
// table, and the result mode. In wdl mode a round has at least two seats
// and is a win for one and a loss for the rest, or a draw for all.
func checkRound(t Table, in []ScoreInput) error {
	seats := make(map[id.ID]bool, len(t.Players))
	for _, p := range t.Players {
		seats[p.ID] = true
	}
	wins, draws := 0, 0
	for _, s := range in {
		if !seats[s.Player] {
			return apperr.Validation("scores")
		}
		if t.Rules.Result != ResultWDL {
			if s.Outcome != "" {
				return apperr.Validation("scores")
			}
			continue
		}
		if s.Outcome == "" || s.Points != 0 {
			return apperr.Validation("scores")
		}
		switch s.Outcome {
		case OutcomeWin:
			wins++
		case OutcomeDraw:
			draws++
		}
	}
	decisive := wins == 1 && draws == 0
	drawn := draws == len(in)
	if t.Rules.Result == ResultWDL && (len(in) < 2 || (!decisive && !drawn)) {
		return apperr.Validation("scores")
	}
	return nil
}

// writeRound writes a round's scores.
func writeRound(ctx context.Context, sc *component.Scope, t Table, round int32, in []ScoreInput) error {
	for _, s := range in {
		row := store.AddScoreParams{
			ID: id.New().PG(), TableID: t.ID.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key,
			PlayerID: s.Player.PG(), Round: int16(round), CreatedAt: ts(sc.Now), //nolint:gosec // 1..MaxRounds
		}
		if t.Rules.Result == ResultWDL {
			row.Outcome = optText(s.Outcome)
		} else {
			row.Points = pgtype.Int4{Int32: s.Points, Valid: true}
		}
		if err := sc.Q().AddScore(ctx, row); err != nil {
			return fmt.Errorf("skor: %w", err)
		}
	}
	return nil
}

// settle finishes an open table whose rounds decide it.
func settle(ctx context.Context, sc *component.Scope, table id.ID) error {
	t, err := tableWith(ctx, sc, table)
	if err != nil {
		return err
	}
	if t.Status == StatusOpen && Decided(t.Rules, t.Players, t.Rounds) {
		return setStatus(ctx, sc, t, t.Label, StatusFinished)
	}
	return nil
}

// AddRound enters the next round of an open table; managers and the
// table's referees, once the event has started. A round that decides the
// game finishes the table. Refused: bad scores (validation error on
// scores), a table the component does not have
// (apperr.ErrScoreTableNotFound), a finished one
// (apperr.ErrScoreTableFinished), more than MaxRounds rounds
// (apperr.ErrEventLimit), an event that has not started
// (apperr.ErrScoreNotStarted) and the refusals of component.Service.Change.
func (s *Service) AddRound(ctx context.Context, ref component.Ref, table id.ID, in []ScoreInput) (Scoreboard, error) {
	if err := cleanScores(in); err != nil {
		return Scoreboard{}, err
	}
	var out Scoreboard
	err := s.fw.Change(ctx, ref, Kind, component.ByReferee(table), func(sc *component.Scope) error {
		if !sc.Started() {
			return apperr.ErrScoreNotStarted
		}
		if _, err := lockTable(ctx, sc, table); err != nil {
			return err
		}
		t, err := tableWith(ctx, sc, table)
		if err != nil {
			return err
		}
		if t.Status == StatusFinished {
			return apperr.ErrScoreTableFinished
		}
		if err := checkRound(t, in); err != nil {
			return err
		}
		round, err := sc.Q().LastScoreRound(ctx, store.LastScoreRoundParams{TableID: table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("son el: %w", err)
		}
		if round >= MaxRounds {
			return apperr.ErrEventLimit
		}
		if err := writeRound(ctx, sc, t, round+1, in); err != nil {
			return err
		}
		if err := settle(ctx, sc, table); err != nil {
			return err
		}
		return done(ctx, sc, AuditRoundAdded, map[string]any{"table_id": table.String(), "round": round + 1}, &out)
	})
	if err != nil {
		return Scoreboard{}, err
	}
	return out, nil
}

// CorrectRound replaces the scores of an entered round; managers only
// (ADR-0025 §5), on an open or a finished table. A correction that decides
// the game finishes an open table; a finished table stays finished (a
// manager reopens it). Refused: a round the table does not have
// (validation error on round), bad scores (validation error on scores),
// a table the component does not have (apperr.ErrScoreTableNotFound) and
// the refusals of component.Service.Change.
func (s *Service) CorrectRound(ctx context.Context, ref component.Ref, table id.ID, round int32, in []ScoreInput) (Scoreboard, error) {
	if round < 1 || round > MaxRounds {
		return Scoreboard{}, apperr.Validation("round")
	}
	if err := cleanScores(in); err != nil {
		return Scoreboard{}, err
	}
	var out Scoreboard
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if _, err := lockTable(ctx, sc, table); err != nil {
			return err
		}
		t, err := tableWith(ctx, sc, table)
		if err != nil {
			return err
		}
		if err := checkRound(t, in); err != nil {
			return err
		}
		n, err := sc.Q().DeleteScoreRound(ctx, store.DeleteScoreRoundParams{
			TableID: table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, Round: int16(round), //nolint:gosec // 1..MaxRounds
		})
		if err != nil {
			return fmt.Errorf("el silme: %w", err)
		}
		if n == 0 {
			return apperr.Validation("round")
		}
		if err := writeRound(ctx, sc, t, round, in); err != nil {
			return err
		}
		if err := settle(ctx, sc, table); err != nil {
			return err
		}
		return done(ctx, sc, AuditRoundCorrect, map[string]any{"table_id": table.String(), "round": round}, &out)
	})
	if err != nil {
		return Scoreboard{}, err
	}
	return out, nil
}

// DeleteRound removes the last round of a table; managers only. The
// table's status does not change. Refused: a round that is not the last
// one (validation error on round), a table the component does not have
// (apperr.ErrScoreTableNotFound) and the refusals of
// component.Service.Change.
func (s *Service) DeleteRound(ctx context.Context, ref component.Ref, table id.ID, round int32) (Scoreboard, error) {
	if round < 1 || round > MaxRounds {
		return Scoreboard{}, apperr.Validation("round")
	}
	var out Scoreboard
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if _, err := lockTable(ctx, sc, table); err != nil {
			return err
		}
		last, err := sc.Q().LastScoreRound(ctx, store.LastScoreRoundParams{TableID: table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("son el: %w", err)
		}
		if last == 0 || round != last {
			return apperr.Validation("round")
		}
		if _, err := sc.Q().DeleteScoreRound(ctx, store.DeleteScoreRoundParams{
			TableID: table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, Round: int16(round), //nolint:gosec // 1..MaxRounds
		}); err != nil {
			return fmt.Errorf("el silme: %w", err)
		}
		return done(ctx, sc, AuditRoundDeleted, map[string]any{"table_id": table.String(), "round": round}, &out)
	})
	if err != nil {
		return Scoreboard{}, err
	}
	return out, nil
}

// FinishTable finishes an open table; managers and the table's referees,
// once the event has started. Finishing a finished table changes nothing.
// Refused: a table the component does not have
// (apperr.ErrScoreTableNotFound), an event that has not started
// (apperr.ErrScoreNotStarted) and the refusals of component.Service.Change.
func (s *Service) FinishTable(ctx context.Context, ref component.Ref, table id.ID) (Scoreboard, error) {
	var out Scoreboard
	err := s.fw.Change(ctx, ref, Kind, component.ByReferee(table), func(sc *component.Scope) error {
		if !sc.Started() {
			return apperr.ErrScoreNotStarted
		}
		t, err := lockTable(ctx, sc, table)
		if err != nil {
			return err
		}
		if t.Status == StatusFinished {
			out, err = load(ctx, sc)
			return err
		}
		if err := setStatus(ctx, sc, t, t.Label, StatusFinished); err != nil {
			return err
		}
		return done(ctx, sc, AuditTableFinished, map[string]any{"table_id": table.String()}, &out)
	})
	if err != nil {
		return Scoreboard{}, err
	}
	return out, nil
}
