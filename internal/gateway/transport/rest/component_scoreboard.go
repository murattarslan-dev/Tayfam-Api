package rest

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The scoreboard component (kind "scoreboard", issue #250; ADR-0025 §4–§6,
// ADR-0037 §5). core decides everything; the gateway only keeps junk out
// early. Scores travel in the body only and are never logged.

// Limits of core's scoreboard package.
const (
	scoreLabelMax   = 30
	scorePlayersMax = 50
	scoreRoundsMax  = 200
	scorePointsMax  = 1_000_000
	scoreTargetMax  = 100_000
	// scoreBodyMax holds 50 seats or scores with their IDs.
	scoreBodyMax = 16 << 10
)

// registerScoreboardComponent is the scoreboard component's line in the
// registry (componentRoutes).
func registerScoreboardComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "scoreboard"), h.authed(h.getScoreboard))
	mux.HandleFunc(componentPath("POST", "scoreboard/tables"), h.authed(h.createScoreTable))
	mux.HandleFunc(componentPath("PATCH", "scoreboard/tables/{table_id}"), h.authed(h.updateScoreTable))
	mux.HandleFunc(componentPath("DELETE", "scoreboard/tables/{table_id}"), h.authed(h.deleteScoreTable))
	mux.HandleFunc(componentPath("PUT", "scoreboard/tables/{table_id}/players"), h.authed(h.setScorePlayers))
	mux.HandleFunc(componentPath("POST", "scoreboard/tables/{table_id}/rounds"), h.authed(h.addScoreRound))
	mux.HandleFunc(componentPath("PUT", "scoreboard/tables/{table_id}/rounds/{round}"), h.authed(h.correctScoreRound))
	mux.HandleFunc(componentPath("DELETE", "scoreboard/tables/{table_id}/rounds/{round}"), h.authed(h.deleteScoreRound))
	mux.HandleFunc(componentPath("POST", "scoreboard/tables/{table_id}/finish"), h.authed(h.finishScoreTable))
}

func (h componentHandlers) scoreboards() corev1.ScoreboardComponentServiceClient {
	return corev1.NewScoreboardComponentServiceClient(h.conn)
}

type scoreRulesJSON struct {
	Side   string `json:"side,omitempty"`
	Rounds string `json:"rounds,omitempty"`
	Winner string `json:"winner,omitempty"`
	Result string `json:"result,omitempty"`
	Target int32  `json:"target,omitempty"`
	BestOf int32  `json:"best_of,omitempty"`
}

type scorePresetJSON struct {
	Key        string            `json:"key"`
	Labels     map[string]string `json:"labels"`
	PlayersMin int32             `json:"players_min,omitempty"`
	PlayersMax int32             `json:"players_max,omitempty"`
	Rules      scoreRulesJSON    `json:"rules"`
	Retired    bool              `json:"retired"`
}

type scorePlayerJSON struct {
	ID       string `json:"id"`
	Position int32  `json:"position"`
	UserID   string `json:"user_id,omitempty"`
	TeamID   string `json:"team_id,omitempty"`
}

type scoreEntryJSON struct {
	PlayerID string `json:"player_id"`
	Points   *int32 `json:"points,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
}

type scoreRoundJSON struct {
	Number int32            `json:"number"`
	Scores []scoreEntryJSON `json:"scores"`
}

type scoreStandingJSON struct {
	PlayerID  string  `json:"player_id"`
	Total     float64 `json:"total"`
	RoundsWon int32   `json:"rounds_won"`
	Rank      int32   `json:"rank"`
}

type scoreTableJSON struct {
	ID             string              `json:"id"`
	Label          string              `json:"label"`
	PresetKey      string              `json:"preset_key,omitempty"`
	Rules          scoreRulesJSON      `json:"rules"`
	TeamsKey       string              `json:"teams_key,omitempty"`
	Status         string              `json:"status"`
	FinishedAt     *time.Time          `json:"finished_at,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
	Players        []scorePlayerJSON   `json:"players"`
	Rounds         []scoreRoundJSON    `json:"rounds"`
	Standings      []scoreStandingJSON `json:"standings"`
	WinnerIDs      []string            `json:"winner_ids"`
	CallerCanScore bool                `json:"caller_can_score"`
}

type scoreboardJSON struct {
	Tables          []scoreTableJSON  `json:"tables"`
	Presets         []scorePresetJSON `json:"presets"`
	Rules           scoreRulesJSON    `json:"rules"`
	MaxTables       int32             `json:"max_tables"`
	CallerCanManage bool              `json:"caller_can_manage"`
}

func scoreRulesFrom(r *corev1.ScoreRules) scoreRulesJSON {
	return scoreRulesJSON{
		Side: r.GetSide(), Rounds: r.GetRounds(), Winner: r.GetWinner(), Result: r.GetResult(),
		Target: r.GetTarget(), BestOf: r.GetBestOf(),
	}
}

func scoreTableFrom(t *corev1.ScoreTable) scoreTableJSON {
	out := scoreTableJSON{
		ID: t.GetId(), Label: t.GetLabel(), PresetKey: t.GetPresetKey(), Rules: scoreRulesFrom(t.GetRules()),
		TeamsKey: t.GetTeamsKey(), Status: t.GetStatus(), FinishedAt: optTime(t.GetFinishedTime()),
		CreatedAt: t.GetCreateTime().AsTime().UTC(),
		Players:   make([]scorePlayerJSON, 0, len(t.GetPlayers())),
		Rounds:    make([]scoreRoundJSON, 0, len(t.GetRounds())),
		Standings: make([]scoreStandingJSON, 0, len(t.GetStandings())),
		WinnerIDs: make([]string, 0, len(t.GetWinnerIds())), CallerCanScore: t.GetCallerCanScore(),
	}
	wdl := t.GetRules().GetResult() == "wdl"
	for _, p := range t.GetPlayers() {
		out.Players = append(out.Players, scorePlayerJSON{ID: p.GetId(), Position: p.GetPosition(), UserID: p.GetUserId(), TeamID: p.GetTeamId()})
	}
	for _, rd := range t.GetRounds() {
		r := scoreRoundJSON{Number: rd.GetNumber(), Scores: make([]scoreEntryJSON, 0, len(rd.GetScores()))}
		for _, s := range rd.GetScores() {
			e := scoreEntryJSON{PlayerID: s.GetPlayerId()}
			if wdl {
				e.Outcome = s.GetOutcome()
			} else {
				p := s.GetPoints()
				e.Points = &p
			}
			r.Scores = append(r.Scores, e)
		}
		out.Rounds = append(out.Rounds, r)
	}
	for _, s := range t.GetStandings() {
		out.Standings = append(out.Standings, scoreStandingJSON{PlayerID: s.GetPlayerId(), Total: s.GetTotal(), RoundsWon: s.GetRoundsWon(), Rank: s.GetRank()})
	}
	out.WinnerIDs = append(out.WinnerIDs, t.GetWinnerIds()...)
	return out
}

func scoreboardFrom(b *corev1.Scoreboard) scoreboardJSON {
	out := scoreboardJSON{
		Tables: make([]scoreTableJSON, 0, len(b.GetTables())), Presets: make([]scorePresetJSON, 0, len(b.GetPresets())),
		Rules: scoreRulesFrom(b.GetRules()), MaxTables: b.GetMaxTables(), CallerCanManage: b.GetCallerCanManage(),
	}
	for _, t := range b.GetTables() {
		out.Tables = append(out.Tables, scoreTableFrom(t))
	}
	for _, p := range b.GetPresets() {
		labels := p.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		out.Presets = append(out.Presets, scorePresetJSON{
			Key: p.GetKey(), Labels: labels, PlayersMin: p.GetPlayersMin(), PlayersMax: p.GetPlayersMax(),
			Rules: scoreRulesFrom(p.GetRules()), Retired: p.GetRetired(),
		})
	}
	return out
}

type scorePlayerBody struct {
	UserID string `json:"user_id"`
	TeamID string `json:"team_id"`
}

type scoreTableCreateBody struct {
	Label     string            `json:"label"`
	PresetKey string            `json:"preset_key"`
	Target    int32             `json:"target"`
	TeamsKey  string            `json:"teams_key"`
	Players   []scorePlayerBody `json:"players"`
}

type scoreTableUpdateBody struct {
	Label  *string `json:"label"`
	Status string  `json:"status"`
}

type scorePlayersBody struct {
	// A pointer so that a body without it is refused; [] clears the seats.
	Players *[]scorePlayerBody `json:"players"`
}

type scoreEntryBody struct {
	PlayerID string `json:"player_id"`
	Points   int32  `json:"points"`
	Outcome  string `json:"outcome"`
}

type scoreRoundBody struct {
	Scores []scoreEntryBody `json:"scores"`
}

// scoreLabelOK checks a label as core does: 1–30 characters after
// trimming.
func scoreLabelOK(s string) bool {
	n := runes(s)
	return n >= 1 && n <= scoreLabelMax
}

// scorePlayersIn checks seats: exactly one of user_id and team_id each,
// none twice, at most 50.
func scorePlayersIn(in []scorePlayerBody) ([]*corev1.ScorePlayerInput, bool) {
	if len(in) > scorePlayersMax {
		return nil, false
	}
	seen := make(map[string]bool, len(in))
	out := make([]*corev1.ScorePlayerInput, 0, len(in))
	for _, p := range in {
		who := p.UserID
		if who == "" {
			who = p.TeamID
		}
		if (p.UserID == "") == (p.TeamID == "") || !validUUID(who) || seen[who] {
			return nil, false
		}
		seen[who] = true
		out = append(out, &corev1.ScorePlayerInput{UserId: p.UserID, TeamId: p.TeamID})
	}
	return out, true
}

// scoreRoundIn reads and checks a round body; on a bad one it answers
// invalid-request and ok is false.
func scoreRoundIn(w http.ResponseWriter, r *http.Request) (scores []*corev1.ScoreEntry, ok bool) {
	var b scoreRoundBody
	if !decodeLimit(w, r, &b, scoreBodyMax) {
		return nil, false
	}
	if len(b.Scores) == 0 || len(b.Scores) > scorePlayersMax {
		badRequest(w, r)
		return nil, false
	}
	seen := make(map[string]bool, len(b.Scores))
	for _, s := range b.Scores {
		if !validUUID(s.PlayerID) || seen[s.PlayerID] || s.Points < -scorePointsMax || s.Points > scorePointsMax {
			badRequest(w, r)
			return nil, false
		}
		switch s.Outcome {
		case "", "win", "draw", "loss":
		default:
			badRequest(w, r)
			return nil, false
		}
		seen[s.PlayerID] = true
		scores = append(scores, &corev1.ScoreEntry{PlayerId: s.PlayerID, Points: s.Points, Outcome: s.Outcome})
	}
	return scores, true
}

// scoreTableRef reads and checks the path's component and table; on a bad
// one it answers invalid-request and ok is false.
func scoreTableRef(w http.ResponseWriter, r *http.Request) (ref *corev1.ComponentRef, table string, ok bool) {
	if ref, ok = componentRef(w, r); !ok {
		return nil, "", false
	}
	if table, ok = pathUUID(w, r, "table_id"); !ok {
		return nil, "", false
	}
	return ref, table, true
}

// scoreRoundPath reads and checks the path's round number (1–200).
func scoreRoundPath(w http.ResponseWriter, r *http.Request) (int32, bool) {
	n, err := strconv.ParseUint(r.PathValue("round"), 10, 8)
	if err != nil || n < 1 || n > scoreRoundsMax {
		badRequest(w, r)
		return 0, false
	}
	return int32(n), true //nolint:gosec // 1..200
}

func (h componentHandlers) getScoreboard(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.scoreboards().GetScoreboard(ctx, &corev1.GetScoreboardRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scoreboardFrom(resp.GetScoreboard()))
}

func (h componentHandlers) createScoreTable(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b scoreTableCreateBody
	if !decodeLimit(w, r, &b, scoreBodyMax) {
		return
	}
	players, ok := scorePlayersIn(b.Players)
	if !ok || !scoreLabelOK(b.Label) || b.Target < 0 || b.Target > scoreTargetMax ||
		(b.PresetKey != "" && !catalogKeyPattern.MatchString(b.PresetKey)) ||
		(b.TeamsKey != "" && !catalogKeyPattern.MatchString(b.TeamsKey)) {
		badRequest(w, r)
		return
	}
	resp, err := h.scoreboards().CreateScoreTable(ctx, &corev1.CreateScoreTableRequest{
		Ref: ref, Label: b.Label, PresetKey: b.PresetKey, Target: b.Target, TeamsKey: b.TeamsKey, Players: players,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, scoreboardFrom(resp.GetScoreboard()))
}

func (h componentHandlers) updateScoreTable(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, table, ok := scoreTableRef(w, r)
	if !ok {
		return
	}
	var b scoreTableUpdateBody
	if !decodeLimit(w, r, &b, scoreBodyMax) {
		return
	}
	if (b.Label == nil && b.Status == "") || (b.Label != nil && !scoreLabelOK(*b.Label)) ||
		(b.Status != "" && b.Status != "open" && b.Status != "finished") {
		badRequest(w, r)
		return
	}
	req := &corev1.UpdateScoreTableRequest{Ref: ref, TableId: table, Status: b.Status}
	if b.Label != nil {
		label := strings.TrimSpace(*b.Label)
		req.Label = &label
	}
	resp, err := h.scoreboards().UpdateScoreTable(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scoreboardFrom(resp.GetScoreboard()))
}

func (h componentHandlers) deleteScoreTable(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, table, ok := scoreTableRef(w, r)
	if !ok {
		return
	}
	resp, err := h.scoreboards().DeleteScoreTable(ctx, &corev1.DeleteScoreTableRequest{Ref: ref, TableId: table})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scoreboardFrom(resp.GetScoreboard()))
}

func (h componentHandlers) setScorePlayers(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, table, ok := scoreTableRef(w, r)
	if !ok {
		return
	}
	var b scorePlayersBody
	if !decodeLimit(w, r, &b, scoreBodyMax) {
		return
	}
	if b.Players == nil {
		badRequest(w, r)
		return
	}
	players, ok := scorePlayersIn(*b.Players)
	if !ok {
		badRequest(w, r)
		return
	}
	resp, err := h.scoreboards().SetScorePlayers(ctx, &corev1.SetScorePlayersRequest{Ref: ref, TableId: table, Players: players})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scoreboardFrom(resp.GetScoreboard()))
}

func (h componentHandlers) addScoreRound(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, table, ok := scoreTableRef(w, r)
	if !ok {
		return
	}
	scores, ok := scoreRoundIn(w, r)
	if !ok {
		return
	}
	resp, err := h.scoreboards().AddScoreRound(ctx, &corev1.AddScoreRoundRequest{Ref: ref, TableId: table, Scores: scores})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scoreboardFrom(resp.GetScoreboard()))
}

func (h componentHandlers) correctScoreRound(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, table, ok := scoreTableRef(w, r)
	if !ok {
		return
	}
	round, ok := scoreRoundPath(w, r)
	if !ok {
		return
	}
	scores, ok := scoreRoundIn(w, r)
	if !ok {
		return
	}
	resp, err := h.scoreboards().CorrectScoreRound(ctx, &corev1.CorrectScoreRoundRequest{Ref: ref, TableId: table, Round: round, Scores: scores})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scoreboardFrom(resp.GetScoreboard()))
}

func (h componentHandlers) deleteScoreRound(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, table, ok := scoreTableRef(w, r)
	if !ok {
		return
	}
	round, ok := scoreRoundPath(w, r)
	if !ok {
		return
	}
	resp, err := h.scoreboards().DeleteScoreRound(ctx, &corev1.DeleteScoreRoundRequest{Ref: ref, TableId: table, Round: round})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scoreboardFrom(resp.GetScoreboard()))
}

// finishScoreTable takes no body.
func (h componentHandlers) finishScoreTable(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, table, ok := scoreTableRef(w, r)
	if !ok {
		return
	}
	resp, err := h.scoreboards().FinishScoreTable(ctx, &corev1.FinishScoreTableRequest{Ref: ref, TableId: table})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scoreboardFrom(resp.GetScoreboard()))
}
