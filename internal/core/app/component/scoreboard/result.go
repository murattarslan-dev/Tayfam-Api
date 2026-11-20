package scoreboard

import (
	"cmp"
	"slices"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Standing is one seat's place at a table.
type Standing struct {
	// Player is the seat (Player.ID).
	Player id.ID
	// Total is the sum of the seat's points; in wdl mode a win counts 1, a
	// draw ½ and a loss 0.
	Total float64
	// RoundsWon counts the rounds the seat won alone: the highest (low:
	// lowest) points of the round, or the win in wdl mode. In a best-of
	// match it decides the winner.
	RoundsWon int32
	// Rank is 1-based; seats that compare equal share it (1, 1, 3).
	Rank int32
}

// Result is a table's standings and, once it is finished, its winners.
type Result struct {
	// Standings are ordered by rank, then by seat position.
	Standings []Standing
	// Winners are the seats ranked first at a finished table: more than one
	// for a draw. Empty while the table is open.
	Winners []id.ID
}

// value is what one score adds to a seat's total.
func value(r Rules, s Score) float64 {
	if r.Result != ResultWDL {
		return float64(s.Points)
	}
	switch s.Outcome {
	case OutcomeWin:
		return 1
	case OutcomeDraw:
		return 0.5
	}
	return 0
}

// roundWinner returns the seat that won a round alone; ok is false for a
// tie or an empty round.
func roundWinner(r Rules, rd Round) (seat id.ID, ok bool) {
	if r.Result == ResultWDL {
		for _, s := range rd.Scores {
			if s.Outcome == OutcomeWin {
				return s.Player, true
			}
		}
		return id.ID{}, false
	}
	best, n := int32(0), 0
	for _, s := range rd.Scores {
		better := n == 0 || s.Points > best
		if r.Winner == WinnerLow {
			better = n == 0 || s.Points < best
		}
		switch {
		case better:
			best, seat, n = s.Points, s.Player, 1
		case s.Points == best:
			n++
		}
	}
	return seat, n == 1
}

type tally struct {
	total float64
	won   int32
	// reached is the 1-based round in which the total first reached the
	// target; 0 when it has not.
	reached int
}

func tallies(r Rules, players []Player, rounds []Round) map[id.ID]*tally {
	out := make(map[id.ID]*tally, len(players))
	for _, p := range players {
		out[p.ID] = &tally{}
	}
	for i, rd := range rounds {
		for _, s := range rd.Scores {
			if t, ok := out[s.Player]; ok {
				t.total += value(r, s)
			}
		}
		if w, ok := roundWinner(r, rd); ok {
			if t, ok := out[w]; ok {
				t.won++
			}
		}
		if r.Target > 0 {
			for _, t := range out {
				if t.reached == 0 && t.total >= float64(r.Target) {
					t.reached = i + 1
				}
			}
		}
	}
	return out
}

// compare orders two seats: negative when a is ahead of b, 0 for equals.
func compare(r Rules, a, b *tally) int {
	if r.BestOf > 1 {
		if c := cmp.Compare(b.won, a.won); c != 0 {
			return c
		}
	}
	if r.Winner == WinnerFirst && r.Target > 0 {
		switch {
		case a.reached > 0 && b.reached == 0:
			return -1
		case a.reached == 0 && b.reached > 0:
			return 1
		case a.reached != b.reached:
			return cmp.Compare(a.reached, b.reached)
		}
	}
	if r.Winner == WinnerLow && r.Result != ResultWDL {
		return cmp.Compare(a.total, b.total)
	}
	return cmp.Compare(b.total, a.total)
}

// Decided reports whether the rounds end the game by the rules: the one
// round of a single match, a seat that won more than half of a best-of
// match (or all its games were played), or a seat whose total reached the
// target. A table is finished then without a manager's word.
func Decided(r Rules, players []Player, rounds []Round) bool {
	if len(rounds) == 0 {
		return false
	}
	if r.Rounds == RoundsSingle {
		return true
	}
	t := tallies(r, players, rounds)
	if r.BestOf > 1 {
		if len(rounds) >= int(r.BestOf) {
			return true
		}
		for _, x := range t {
			if x.won > r.BestOf/2 {
				return true
			}
		}
	}
	if r.Target > 0 {
		for _, x := range t {
			if x.reached > 0 {
				return true
			}
		}
	}
	return false
}

// Compute ranks the seats of a table by its rules and rounds; finished
// names the winners. Players are in seat order.
func Compute(r Rules, players []Player, rounds []Round, finished bool) Result {
	t := tallies(r, players, rounds)
	order := make([]Player, len(players))
	copy(order, players)
	slices.SortStableFunc(order, func(a, b Player) int { return compare(r, t[a.ID], t[b.ID]) })
	out := Result{Standings: make([]Standing, 0, len(order)), Winners: []id.ID{}}
	for i, p := range order {
		rank := int32(i + 1) //nolint:gosec // at most MaxPlayers
		if i > 0 && compare(r, t[order[i-1].ID], t[p.ID]) == 0 {
			rank = out.Standings[i-1].Rank
		}
		out.Standings = append(out.Standings, Standing{Player: p.ID, Total: t[p.ID].total, RoundsWon: t[p.ID].won, Rank: rank})
		if finished && rank == 1 {
			out.Winners = append(out.Winners, p.ID)
		}
	}
	return out
}
