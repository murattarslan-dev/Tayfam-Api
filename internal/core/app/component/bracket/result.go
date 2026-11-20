package bracket

import (
	"cmp"
	"slices"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// side is the seat of a match's side in the scoreboard's computation: 1
// for A, 2 for B.
func side(n byte) id.ID {
	var s id.ID
	s[15] = n
	return s
}

// inverse is B's outcome of a game A had outcome o in.
func inverse(o string) string {
	switch o {
	case scoreboard.OutcomeWin:
		return scoreboard.OutcomeLoss
	case scoreboard.OutcomeLoss:
		return scoreboard.OutcomeWin
	}
	return o
}

// scoreRules are the scoreboard rules a match is played by: one game, or
// a best-of match.
func scoreRules(r Rules) scoreboard.Rules {
	s := scoreboard.Rules{Side: scoreboard.SidePlayer, Rounds: scoreboard.RoundsSingle, Winner: r.Winner, Result: r.Result}
	if r.BestOf > 1 {
		s.Rounds, s.BestOf = scoreboard.RoundsRound, r.BestOf
	}
	return s
}

// evaluate sets a match's totals, whether it is decided and its winner,
// by the scoreboard's rules (scoreboard.Decided, scoreboard.Compute). A
// match with one side is a bye the side wins; a match without sides is
// decided without a winner. An elimination match needs a winner: a draw
// leaves it open for another game.
func evaluate(r Rules, m *Match) {
	m.Decided, m.Bye, m.Winner = false, false, 0
	m.ATotal, m.BTotal, m.AWon, m.BWon = 0, 0, 0, 0
	if m.A == 0 || m.B == 0 {
		m.Decided, m.Bye, m.Winner = true, true, m.A+m.B
		return
	}
	a, b := side(1), side(2)
	players := []scoreboard.Player{{ID: a, Position: 1}, {ID: b, Position: 2}}
	rounds := make([]scoreboard.Round, 0, len(m.Games))
	for _, g := range m.Games {
		rd := scoreboard.Round{Number: g.Number}
		if r.Result == scoreboard.ResultWDL {
			rd.Scores = []scoreboard.Score{{Player: a, Outcome: g.AOutcome}, {Player: b, Outcome: inverse(g.AOutcome)}}
		} else {
			rd.Scores = []scoreboard.Score{{Player: a, Points: g.APoints}, {Player: b, Points: g.BPoints}}
		}
		rounds = append(rounds, rd)
	}
	sr := scoreRules(r)
	decided := scoreboard.Decided(sr, players, rounds)
	res := scoreboard.Compute(sr, players, rounds, decided)
	for _, s := range res.Standings {
		if s.Player == a {
			m.ATotal, m.AWon = s.Total, s.RoundsWon
		} else {
			m.BTotal, m.BWon = s.Total, s.RoundsWon
		}
	}
	switch {
	case !decided:
	case len(res.Winners) == 1:
		m.Decided, m.Winner = true, m.A
		if res.Winners[0] == b {
			m.Winner = m.B
		}
	case r.Format != FormatElimination:
		// A draw.
		m.Decided = true
	}
}

// standings ranks the entrants of a league or a Swiss bracket: points (a
// win 1, a draw ½, a bye 1), then the points among those level on points
// (the match between them), then Buchholz (the sum of the opponents'
// points); entrants equal in all three share the rank. Undecided matches
// do not count.
func standings(entrants []int32, matches []Match) []Standing {
	byPos := make(map[int32]*Standing, len(entrants))
	out := make([]Standing, len(entrants))
	for i, p := range entrants {
		out[i] = Standing{Position: p}
		byPos[p] = &out[i]
	}
	type result struct {
		a, b   int32
		pa, pb float64
	}
	var played []result
	for _, m := range matches {
		if !m.Decided {
			continue
		}
		if m.Bye {
			if s, ok := byPos[m.Winner]; ok && m.Winner != 0 {
				s.Played++
				s.Won++
				s.Points++
			}
			continue
		}
		sa, oka := byPos[m.A]
		sb, okb := byPos[m.B]
		if !oka || !okb {
			continue
		}
		r := result{a: m.A, b: m.B}
		switch m.Winner {
		case m.A:
			r.pa = 1
			sa.Won++
			sb.Lost++
		case m.B:
			r.pb = 1
			sb.Won++
			sa.Lost++
		default:
			r.pa, r.pb = 0.5, 0.5
			sa.Drawn++
			sb.Drawn++
		}
		sa.Played++
		sb.Played++
		sa.Points += r.pa
		sb.Points += r.pb
		played = append(played, r)
	}
	for _, r := range played {
		sa, sb := byPos[r.a], byPos[r.b]
		sa.Buchholz += sb.Points
		sb.Buchholz += sa.Points
	}
	for _, r := range played {
		sa, sb := byPos[r.a], byPos[r.b]
		if sa.Points == sb.Points {
			sa.HeadToHead += r.pa
			sb.HeadToHead += r.pb
		}
	}
	level := func(a, b Standing) int {
		if c := cmp.Compare(b.Points, a.Points); c != 0 {
			return c
		}
		if c := cmp.Compare(b.HeadToHead, a.HeadToHead); c != 0 {
			return c
		}
		return cmp.Compare(b.Buchholz, a.Buchholz)
	}
	slices.SortStableFunc(out, func(a, b Standing) int {
		if c := level(a, b); c != 0 {
			return c
		}
		return cmp.Compare(a.Position, b.Position)
	})
	for i := range out {
		out[i].Rank = int32(i + 1) //nolint:gosec // at most MaxSwiss
		if i > 0 && level(out[i-1], out[i]) == 0 {
			out[i].Rank = out[i-1].Rank
		}
	}
	return out
}

// advancing are the winners of a round's matches in slot order: the
// sides of the elimination's next round.
func advancing(matches []Match, round int32) []int32 {
	var out []int32
	for _, m := range matches {
		if m.Round == round && m.Winner != 0 {
			out = append(out, m.Winner)
		}
	}
	return out
}

// roundDecided reports whether every match of a round is decided.
func roundDecided(matches []Match, round int32) bool {
	for _, m := range matches {
		if m.Round == round && !m.Decided {
			return false
		}
	}
	return true
}

// finish sets the current round, the standings, whether the bracket is
// finished and its winners.
func finish(r *Result) {
	r.CurrentRound = 0
	for _, m := range r.Matches {
		r.CurrentRound = max(r.CurrentRound, m.Round)
	}
	r.Standings, r.Finished, r.Winners = []Standing{}, false, []int32{}
	if r.CurrentRound == 0 {
		return
	}
	done := roundDecided(r.Matches, r.CurrentRound)
	if r.Format == FormatElimination {
		if adv := advancing(r.Matches, r.CurrentRound); done && len(adv) <= 1 {
			r.Finished, r.Winners = true, adv
		}
		return
	}
	var entrants []int32
	for _, m := range r.Matches {
		for _, p := range []int32{m.A, m.B} {
			if p != 0 && !slices.Contains(entrants, p) {
				entrants = append(entrants, p)
			}
		}
	}
	slices.Sort(entrants)
	r.Standings = standings(entrants, r.Matches)
	switch r.Format {
	case FormatLeague:
		done = true
		for _, m := range r.Matches {
			done = done && m.Decided
		}
	case FormatSwiss:
		done = done && r.CurrentRound >= r.Rounds
	}
	if done {
		r.Finished = true
		for _, s := range r.Standings {
			if s.Rank == 1 {
				r.Winners = append(r.Winners, s.Position)
			}
		}
	}
}
