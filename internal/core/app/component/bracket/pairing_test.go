package bracket

import (
	"slices"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
)

func TestConsecutiveAndLog2(t *testing.T) {
	if got := consecutive([]int32{1, 2, 3, 0, 5}); !slices.Equal(got, []pairing{{1, 2}, {3, 0}, {5, 0}}) {
		t.Fatalf("sırayla eşleme: %v", got)
	}
	if got := consecutive(nil); len(got) != 0 {
		t.Fatalf("boş: %v", got)
	}
	for n, want := range map[int]int{1: 0, 2: 1, 3: 2, 4: 2, 5: 3, 8: 3, 9: 4, 128: 7} {
		if got := log2Ceil(n); got != want {
			t.Errorf("log2Ceil(%d) = %d, %d bekleniyordu", n, got, want)
		}
	}
}

// Every two entrants of a league meet exactly once, nobody twice in a
// round; with an odd number one sits out each round.
func TestRoundRobin(t *testing.T) {
	for _, n := range []int{2, 3, 4, 5, 8, MaxLeague} {
		entrants := make([]int32, n)
		for i := range entrants {
			entrants[i] = int32(i + 1) //nolint:gosec // small
		}
		rounds := roundRobin(entrants)
		wantRounds := n - 1
		if n%2 == 1 {
			wantRounds = n
		}
		if len(rounds) != wantRounds {
			t.Fatalf("%d katılımcı: %d tur", n, len(rounds))
		}
		met := map[pairing]int{}
		for _, round := range rounds {
			seen := map[int32]bool{}
			if len(round) != n/2 {
				t.Fatalf("%d katılımcı: turda %d maç", n, len(round))
			}
			for _, p := range round {
				if p[0] == 0 || p[1] == 0 || seen[p[0]] || seen[p[1]] {
					t.Fatalf("%d katılımcı: bozuk tur %v", n, round)
				}
				seen[p[0]], seen[p[1]] = true, true
				met[pairing{min(p[0], p[1]), max(p[0], p[1])}]++
			}
		}
		if len(met) != n*(n-1)/2 {
			t.Fatalf("%d katılımcı: %d farklı eşleşme", n, len(met))
		}
		for p, c := range met {
			if c != 1 {
				t.Fatalf("%d katılımcı: %v %d kez", n, p, c)
			}
		}
	}
}

// A Swiss round avoids a rematch when it can, gives the bye to the lowest
// entrant without one, and pairs in order when no other way is left.
func TestSwissRound(t *testing.T) {
	never := func(int32) bool { return false }
	met := func(pairs ...pairing) func(a, b int32) bool {
		return func(a, b int32) bool {
			return slices.Contains(pairs, pairing{a, b}) || slices.Contains(pairs, pairing{b, a})
		}
	}
	if got := swissRound([]int32{1, 2, 3, 4}, met(), never); !slices.Equal(got, []pairing{{1, 2}, {3, 4}}) {
		t.Fatalf("ilk tur: %v", got)
	}
	if got := swissRound([]int32{1, 2, 3, 4}, met(pairing{1, 2}, pairing{3, 4}), never); !slices.Equal(got, []pairing{{1, 3}, {2, 4}}) {
		t.Fatalf("rövanşsız: %v", got)
	}
	// 1 met 2 and 3: going back pairs 1–4 and 2–3.
	if got := swissRound([]int32{1, 2, 3, 4}, met(pairing{1, 2}, pairing{1, 3}, pairing{2, 4}), never); !slices.Equal(got, []pairing{{1, 4}, {2, 3}}) {
		t.Fatalf("geri dönüş: %v", got)
	}
	// Everyone met everyone: in order.
	all := met(pairing{1, 2}, pairing{1, 3}, pairing{1, 4}, pairing{2, 3}, pairing{2, 4}, pairing{3, 4})
	if got := swissRound([]int32{4, 3, 2, 1}, all, never); !slices.Equal(got, []pairing{{4, 3}, {2, 1}}) {
		t.Fatalf("rövanş kaçınılmaz: %v", got)
	}
	// The bye goes to the lowest without one.
	hadBye := func(a int32) bool { return a == 5 }
	if got := swissRound([]int32{1, 2, 3, 4, 5}, met(), hadBye); !slices.Equal(got, []pairing{{1, 2}, {3, 5}, {4, 0}}) {
		t.Fatalf("bay: %v", got)
	}
}

func game(n, a, b int32) Game { return Game{Number: n, APoints: a, BPoints: b} }

func wdl(n int32, o string) Game { return Game{Number: n, AOutcome: o} }

// A match is scored by the scoreboard's rule; an elimination match needs a
// winner.
func TestEvaluate(t *testing.T) {
	chess := Rules{Format: FormatSwiss, BestOf: 1, Result: scoreboard.ResultWDL, Winner: scoreboard.WinnerHigh}
	for _, tc := range []struct {
		name    string
		r       Rules
		m       Match
		decided bool
		winner  int32
	}{
		{"oyunsuz", chess, Match{A: 3, B: 7}, false, 0},
		{"galibiyet", chess, Match{A: 3, B: 7, Games: []Game{wdl(1, "win")}}, true, 3},
		{"mağlubiyet", chess, Match{A: 3, B: 7, Games: []Game{wdl(1, "loss")}}, true, 7},
		{"İsviçre'de beraberlik", chess, Match{A: 3, B: 7, Games: []Game{wdl(1, "draw")}}, true, 0},
		{"elemede beraberlik açık kalır", Rules{Format: FormatElimination, BestOf: 1, Result: "wdl", Winner: "high"},
			Match{A: 3, B: 7, Games: []Game{wdl(1, "draw")}}, false, 0},
		{"elemede ek oyun", Rules{Format: FormatElimination, BestOf: 1, Result: "wdl", Winner: "high"},
			Match{A: 3, B: 7, Games: []Game{wdl(1, "draw"), wdl(2, "loss")}}, true, 7},
		{"BO3 sürüyor", Rules{Format: FormatLeague, BestOf: 3, Result: "points", Winner: "high"},
			Match{A: 1, B: 2, Games: []Game{game(1, 11, 4), game(2, 5, 11)}}, false, 0},
		{"BO3 bitti", Rules{Format: FormatLeague, BestOf: 3, Result: "points", Winner: "high"},
			Match{A: 1, B: 2, Games: []Game{game(1, 11, 4), game(2, 5, 11), game(3, 11, 9)}}, true, 1},
		{"düşük kazanır", Rules{Format: FormatLeague, BestOf: 1, Result: "points", Winner: "low"},
			Match{A: 1, B: 2, Games: []Game{game(1, 30, 12)}}, true, 2},
		{"bay", chess, Match{A: 5}, true, 5},
		{"B tarafı bay", chess, Match{B: 4}, true, 4},
		{"tarafsız", chess, Match{}, true, 0},
	} {
		m := tc.m
		evaluate(tc.r, &m)
		if m.Decided != tc.decided || m.Winner != tc.winner {
			t.Errorf("%s: sonuçlandı %v, kazanan %d", tc.name, m.Decided, m.Winner)
		}
	}
	m := Match{A: 1, B: 2, Games: []Game{game(1, 11, 4), game(2, 5, 11), game(3, 11, 9)}}
	evaluate(Rules{Format: FormatLeague, BestOf: 3, Result: "points", Winner: "high"}, &m)
	if m.ATotal != 27 || m.BTotal != 24 || m.AWon != 2 || m.BWon != 1 || m.Bye {
		t.Fatalf("toplamlar: %+v", m)
	}
}

func decided(a, b, winner int32) Match {
	return Match{A: a, B: b, Decided: true, Winner: winner, Bye: a == 0 || b == 0}
}

// Standings: points, then the match between those level, then Buchholz.
func TestStandings(t *testing.T) {
	ranks := func(s []Standing) (order, rank []int32) {
		for _, x := range s {
			order, rank = append(order, x.Position), append(rank, x.Rank)
		}
		return order, rank
	}
	// 1 and 2 are level on 2 points; 2 beat 1. 3 and 4 are level on 1; 4
	// beat 3.
	s := standings([]int32{1, 2, 3, 4}, []Match{
		decided(2, 1, 2), decided(1, 3, 1), decided(1, 4, 1),
		decided(2, 3, 3), decided(2, 4, 2), decided(3, 4, 4),
	})
	if order, rank := ranks(s); !slices.Equal(order, []int32{2, 1, 4, 3}) || !slices.Equal(rank, []int32{1, 2, 3, 4}) {
		t.Fatalf("aralarındaki maç: %v %v", order, rank)
	}
	if s[0].Points != 2 || s[0].Won != 2 || s[0].Lost != 1 || s[0].Played != 3 || s[0].HeadToHead != 1 {
		t.Fatalf("birinci: %+v", s[0])
	}
	// 1 and 5 are level on 2 points and did not meet: Buchholz (1's
	// opponents scored more). A draw is half a point.
	s = standings([]int32{1, 2, 3, 4, 5, 6}, []Match{
		decided(1, 2, 1), decided(3, 4, 3), decided(5, 6, 5),
		decided(1, 3, 1), decided(5, 2, 5), decided(4, 6, 0),
	})
	if order, _ := ranks(s); order[0] != 1 || order[1] != 5 || s[0].Buchholz != 1 || s[1].Buchholz != 0.5 {
		t.Fatalf("Buchholz: %+v", s)
	}
	if four := s[slices.IndexFunc(s, func(x Standing) bool { return x.Position == 4 })]; four.Points != 0.5 || four.Drawn != 1 {
		t.Fatalf("beraberlik: %+v", four)
	}
	// A cycle: those level on everything share the first place. A bye is a
	// win (4 is level on points but met nobody); an undecided match does
	// not count.
	s = standings([]int32{1, 2, 3, 4}, []Match{
		decided(1, 2, 1), decided(2, 3, 2), decided(3, 1, 3), decided(4, 0, 4), {A: 4, B: 1},
	})
	if order, rank := ranks(s); !slices.Equal(order, []int32{1, 2, 3, 4}) || !slices.Equal(rank, []int32{1, 1, 1, 4}) ||
		s[3].Points != 1 || s[3].Won != 1 || s[3].Played != 1 {
		t.Fatalf("eşitlik: %+v", s)
	}
}
