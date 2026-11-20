package rest

import (
	"context"
	"net/http"
	"strconv"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The bracket component (kind "bracket", issue #251; ADR-0025 §4–§6,
// ADR-0037 §3). core decides everything, pairs the rounds and scores the
// matches; the gateway only keeps junk out early. Scores travel in the
// body only and are never logged.

// Limits of core's bracket package.
const (
	bracketGamesMax  = 9
	bracketPointsMax = 1_000_000
	bracketBodyMax   = 1 << 10
)

// registerBracketComponent is the bracket component's line in the registry
// (componentRoutes).
func registerBracketComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "bracket"), h.authed(h.getBracket))
	mux.HandleFunc(componentPath("POST", "bracket"), h.authed(h.startBracket))
	mux.HandleFunc(componentPath("DELETE", "bracket"), h.authed(h.resetBracket))
	mux.HandleFunc(componentPath("POST", "bracket/rounds"), h.authed(h.pairBracketRound))
	mux.HandleFunc(componentPath("POST", "bracket/matches/{match_id}/games"), h.authed(h.addBracketGame))
	mux.HandleFunc(componentPath("PUT", "bracket/matches/{match_id}/games/{game}"), h.authed(h.correctBracketGame))
	mux.HandleFunc(componentPath("DELETE", "bracket/matches/{match_id}/games/{game}"), h.authed(h.deleteBracketGame))
}

func (h componentHandlers) brackets() corev1.BracketComponentServiceClient {
	return corev1.NewBracketComponentServiceClient(h.conn)
}

type bracketEntrantJSON struct {
	Position int32  `json:"position"`
	UserID   string `json:"user_id,omitempty"`
	TeamID   string `json:"team_id,omitempty"`
	GuestID  string `json:"guest_id,omitempty"`
}

type bracketGameJSON struct {
	Number   int32  `json:"number"`
	APoints  *int32 `json:"a_points,omitempty"`
	BPoints  *int32 `json:"b_points,omitempty"`
	AOutcome string `json:"a_outcome,omitempty"`
}

type bracketMatchJSON struct {
	ID             string            `json:"id"`
	Round          int32             `json:"round"`
	Slot           int32             `json:"slot"`
	APosition      int32             `json:"a_position,omitempty"`
	BPosition      int32             `json:"b_position,omitempty"`
	Games          []bracketGameJSON `json:"games"`
	Decided        bool              `json:"decided"`
	Bye            bool              `json:"bye"`
	WinnerPosition int32             `json:"winner_position,omitempty"`
	ATotal         float64           `json:"a_total"`
	BTotal         float64           `json:"b_total"`
	AGamesWon      int32             `json:"a_games_won"`
	BGamesWon      int32             `json:"b_games_won"`
	CallerCanScore bool              `json:"caller_can_score"`
}

type bracketStandingJSON struct {
	Position   int32   `json:"position"`
	Played     int32   `json:"played"`
	Won        int32   `json:"won"`
	Drawn      int32   `json:"drawn"`
	Lost       int32   `json:"lost"`
	Points     float64 `json:"points"`
	HeadToHead float64 `json:"head_to_head"`
	Buchholz   float64 `json:"buchholz"`
	Rank       int32   `json:"rank"`
}

type bracketJSON struct {
	Started         bool                  `json:"started"`
	Format          string                `json:"format"`
	BestOf          int32                 `json:"best_of"`
	Result          string                `json:"result"`
	Winner          string                `json:"winner"`
	DrawKey         string                `json:"draw_key,omitempty"`
	Drawn           bool                  `json:"drawn"`
	Subject         string                `json:"subject,omitempty"`
	TeamsKey        string                `json:"teams_key,omitempty"`
	Rounds          int32                 `json:"rounds"`
	CurrentRound    int32                 `json:"current_round"`
	StartedAt       *time.Time            `json:"started_at,omitempty"`
	Entrants        []bracketEntrantJSON  `json:"entrants"`
	Matches         []bracketMatchJSON    `json:"matches"`
	Standings       []bracketStandingJSON `json:"standings"`
	Finished        bool                  `json:"finished"`
	WinnerPositions []int32               `json:"winner_positions"`
	CallerCanManage bool                  `json:"caller_can_manage"`
}

func bracketFrom(b *corev1.Bracket) bracketJSON {
	out := bracketJSON{
		Started: b.GetStarted(), Format: b.GetFormat(), BestOf: b.GetBestOf(), Result: b.GetResult(), Winner: b.GetWinner(),
		DrawKey: b.GetDrawKey(), Drawn: b.GetDrawn(), Subject: b.GetSubject(), TeamsKey: b.GetTeamsKey(),
		Rounds: b.GetRounds(), CurrentRound: b.GetCurrentRound(), StartedAt: optTime(b.GetStartTime()),
		Entrants:        make([]bracketEntrantJSON, 0, len(b.GetEntrants())),
		Matches:         make([]bracketMatchJSON, 0, len(b.GetMatches())),
		Standings:       make([]bracketStandingJSON, 0, len(b.GetStandings())),
		Finished:        b.GetFinished(),
		WinnerPositions: append([]int32{}, b.GetWinnerPositions()...),
		CallerCanManage: b.GetCallerCanManage(),
	}
	wdl := b.GetResult() == "wdl"
	for _, e := range b.GetEntrants() {
		out.Entrants = append(out.Entrants, bracketEntrantJSON{
			Position: e.GetPosition(), UserID: e.GetUserId(), TeamID: e.GetTeamId(), GuestID: e.GetGuestId(),
		})
	}
	for _, m := range b.GetMatches() {
		jm := bracketMatchJSON{
			ID: m.GetId(), Round: m.GetRound(), Slot: m.GetSlot(), APosition: m.GetAPosition(), BPosition: m.GetBPosition(),
			Games: make([]bracketGameJSON, 0, len(m.GetGames())), Decided: m.GetDecided(), Bye: m.GetBye(),
			WinnerPosition: m.GetWinnerPosition(), ATotal: m.GetATotal(), BTotal: m.GetBTotal(),
			AGamesWon: m.GetAGamesWon(), BGamesWon: m.GetBGamesWon(), CallerCanScore: m.GetCallerCanScore(),
		}
		for _, g := range m.GetGames() {
			jg := bracketGameJSON{Number: g.GetNumber()}
			if wdl {
				jg.AOutcome = g.GetAOutcome()
			} else {
				pa, pb := g.GetAPoints(), g.GetBPoints()
				jg.APoints, jg.BPoints = &pa, &pb
			}
			jm.Games = append(jm.Games, jg)
		}
		out.Matches = append(out.Matches, jm)
	}
	for _, s := range b.GetStandings() {
		out.Standings = append(out.Standings, bracketStandingJSON{
			Position: s.GetPosition(), Played: s.GetPlayed(), Won: s.GetWon(), Drawn: s.GetDrawn(), Lost: s.GetLost(),
			Points: s.GetPoints(), HeadToHead: s.GetHeadToHead(), Buchholz: s.GetBuchholz(), Rank: s.GetRank(),
		})
	}
	return out
}

// bracketGameBody is a game: both sides' points, or A's outcome.
type bracketGameBody struct {
	APoints  *int32 `json:"a_points"`
	BPoints  *int32 `json:"b_points"`
	AOutcome string `json:"a_outcome"`
}

// bracketGameIn reads and checks a game body: both points and no outcome,
// or an outcome and no points. On a bad one it answers invalid-request and
// ok is false; core checks the form against the bracket's rule.
func bracketGameIn(w http.ResponseWriter, r *http.Request) (a, b int32, outcome string, ok bool) {
	var g bracketGameBody
	if !decodeLimit(w, r, &g, bracketBodyMax) {
		return 0, 0, "", false
	}
	inRange := func(p *int32) bool { return *p >= -bracketPointsMax && *p <= bracketPointsMax }
	switch {
	case g.AOutcome == "" && g.APoints != nil && g.BPoints != nil && inRange(g.APoints) && inRange(g.BPoints):
		return *g.APoints, *g.BPoints, "", true
	case g.APoints == nil && g.BPoints == nil && (g.AOutcome == "win" || g.AOutcome == "draw" || g.AOutcome == "loss"):
		return 0, 0, g.AOutcome, true
	}
	badRequest(w, r)
	return 0, 0, "", false
}

// bracketMatchRef reads and checks the path's component and match; on a
// bad one it answers invalid-request and ok is false.
func bracketMatchRef(w http.ResponseWriter, r *http.Request) (ref *corev1.ComponentRef, match string, ok bool) {
	if ref, ok = componentRef(w, r); !ok {
		return nil, "", false
	}
	if match, ok = pathUUID(w, r, "match_id"); !ok {
		return nil, "", false
	}
	return ref, match, true
}

// bracketGamePath reads and checks the path's game number (1–9).
func bracketGamePath(w http.ResponseWriter, r *http.Request) (int32, bool) {
	n, err := strconv.ParseUint(r.PathValue("game"), 10, 8)
	if err != nil || n < 1 || n > bracketGamesMax {
		badRequest(w, r)
		return 0, false
	}
	return int32(n), true //nolint:gosec // 1..9
}

func (h componentHandlers) getBracket(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.brackets().GetBracket(ctx, &corev1.GetBracketRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bracketFrom(resp.GetBracket()))
}

// startBracket takes no body.
func (h componentHandlers) startBracket(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.brackets().StartBracket(ctx, &corev1.StartBracketRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bracketFrom(resp.GetBracket()))
}

func (h componentHandlers) resetBracket(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.brackets().ResetBracket(ctx, &corev1.ResetBracketRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bracketFrom(resp.GetBracket()))
}

// pairBracketRound takes no body.
func (h componentHandlers) pairBracketRound(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.brackets().PairBracketRound(ctx, &corev1.PairBracketRoundRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bracketFrom(resp.GetBracket()))
}

func (h componentHandlers) addBracketGame(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, match, ok := bracketMatchRef(w, r)
	if !ok {
		return
	}
	a, b, outcome, ok := bracketGameIn(w, r)
	if !ok {
		return
	}
	resp, err := h.brackets().AddBracketGame(ctx, &corev1.AddBracketGameRequest{Ref: ref, MatchId: match, APoints: a, BPoints: b, AOutcome: outcome})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bracketFrom(resp.GetBracket()))
}

func (h componentHandlers) correctBracketGame(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, match, ok := bracketMatchRef(w, r)
	if !ok {
		return
	}
	game, ok := bracketGamePath(w, r)
	if !ok {
		return
	}
	a, b, outcome, ok := bracketGameIn(w, r)
	if !ok {
		return
	}
	resp, err := h.brackets().CorrectBracketGame(ctx, &corev1.CorrectBracketGameRequest{
		Ref: ref, MatchId: match, Game: game, APoints: a, BPoints: b, AOutcome: outcome,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bracketFrom(resp.GetBracket()))
}

func (h componentHandlers) deleteBracketGame(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, match, ok := bracketMatchRef(w, r)
	if !ok {
		return
	}
	game, ok := bracketGamePath(w, r)
	if !ok {
		return
	}
	resp, err := h.brackets().DeleteBracketGame(ctx, &corev1.DeleteBracketGameRequest{Ref: ref, MatchId: match, Game: game})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bracketFrom(resp.GetBracket()))
}
