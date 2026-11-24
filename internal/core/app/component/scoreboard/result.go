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
	// Rank is 1-based; seats that compare equal share it (1, 1, 3). At a
	// pair table every seat carries its pair's total, rounds and rank, and
	// the pairs are ranked 1 and 2 (1 and 1 for a draw).
	Rank int32
}

// Result is a table's standings and, once it is finished, its winners.
type Result struct {
	// Standings are ordered by rank, then by seat position.
	Standings []Standing
	// Winners are the seats ranked first at a finished table: more than one
	// for a draw, both seats of the winning pair at a pair table. Empty
	// while the table is open.
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

// pairUnits is a pair table's seats as its pairs: the first seat of each
// pair (in seat order) stands for it. unit maps every seat to that seat;
// rounds keeps one score per pair and round, on that seat (the pair's
// score is written to both of its seats; it counts once).
func pairUnits(players []Player, rounds []Round) (unit map[id.ID]id.ID, firsts []Player, out []Round) {
	unit = make(map[id.ID]id.ID, len(players))
	firstOf := map[int32]id.ID{}
	for _, p := range players {
		pair := PairOf(p.Position)
		f, ok := firstOf[pair]
		if !ok {
			f = p.ID
			firstOf[pair] = f
			firsts = append(firsts, p)
		}
		unit[p.ID] = f
	}
	out = make([]Round, 0, len(rounds))
	for _, rd := range rounds {
		u := Round{Number: rd.Number, Scores: make([]Score, 0, len(firstOf))}
		seen := make(map[id.ID]bool, len(firstOf))
		for _, sc := range rd.Scores {
			f, ok := unit[sc.Player]
			if !ok || seen[f] {
				continue
			}
			seen[f] = true
			sc.Player = f
			u.Scores = append(u.Scores, sc)
		}
		out = append(out, u)
	}
	return unit, firsts, out
}

// Decided reports whether the rounds end the game by the rules: the one
// round of a single match, a seat (at a pair table, a pair) that won more
// than half of a best-of match (or all its games were played), or a seat
// (pair) whose total reached the target. A table is finished then without
// a manager's word.
func Decided(r Rules, players []Player, rounds []Round) bool {
	if r.Side == SidePair {
		_, firsts, units := pairUnits(players, rounds)
		players, rounds = firsts, units
	}
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
// names the winners. Players are in seat order. At a pair table the pairs
// are ranked and every seat takes its pair's place.
func Compute(r Rules, players []Player, rounds []Round, finished bool) Result {
	if r.Side != SidePair {
		return compute(r, players, rounds, finished)
	}
	unit, firsts, units := pairUnits(players, rounds)
	res := compute(r, firsts, units, finished)
	byUnit := make(map[id.ID]Standing, len(res.Standings))
	for _, s := range res.Standings {
		byUnit[s.Player] = s
	}
	order := make([]Player, len(players))
	copy(order, players)
	slices.SortStableFunc(order, func(a, b Player) int {
		return cmp.Compare(byUnit[unit[a.ID]].Rank, byUnit[unit[b.ID]].Rank)
	})
	out := Result{Standings: make([]Standing, 0, len(order)), Winners: []id.ID{}}
	for _, p := range order {
		s := byUnit[unit[p.ID]]
		out.Standings = append(out.Standings, Standing{Player: p.ID, Total: s.Total, RoundsWon: s.RoundsWon, Rank: s.Rank})
		if finished && s.Rank == 1 {
			out.Winners = append(out.Winners, p.ID)
		}
	}
	return out
}

// compute ranks seats (or, for a pair table, the pairs' first seats).
func compute(r Rules, players []Player, rounds []Round, finished bool) Result {
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
