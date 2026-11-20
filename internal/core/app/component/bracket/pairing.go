package bracket

import "slices"

// A pairing is two sides of a match by their place in the draw; 0 is no
// side (a bye or an empty place).
type pairing [2]int32

// swissSteps bounds the search for a Swiss round without a rematch; past
// it the round is paired in order and a rematch is allowed.
const swissSteps = 20_000

// consecutive pairs the sides in order: 1–2, 3–4 …; with an odd number
// the last one has a bye. It is the draw's own reading (two places in a
// row are a pairing) and the elimination's next round.
func consecutive(sides []int32) []pairing {
	out := make([]pairing, 0, (len(sides)+1)/2)
	for i := 0; i < len(sides); i += 2 {
		p := pairing{sides[i], 0}
		if i+1 < len(sides) {
			p[1] = sides[i+1]
		}
		out = append(out, p)
	}
	return out
}

// roundRobin returns the rounds of a league in which every two entrants
// meet once (the circle method). With an odd number one entrant sits out
// each round; that pairing is left out.
func roundRobin(entrants []int32) [][]pairing {
	ps := slices.Clone(entrants)
	if len(ps)%2 == 1 {
		ps = append(ps, 0)
	}
	n := len(ps)
	out := make([][]pairing, 0, n-1)
	for range n - 1 {
		round := make([]pairing, 0, n/2)
		for i := range n / 2 {
			a, b := ps[i], ps[n-1-i]
			if a != 0 && b != 0 {
				round = append(round, pairing{a, b})
			}
		}
		out = append(out, round)
		// The first stays; the others turn by one.
		last := ps[n-1]
		copy(ps[2:], ps[1:n-1])
		ps[1] = last
	}
	return out
}

// swissRound pairs a Swiss round. order is the entrants by their standing
// (best first); met reports whether two have played each other; hadBye
// whether one had a bye. With an odd number the lowest entrant without a
// bye gets one. The others are paired top down without a rematch when
// that is possible, and in order when it is not.
func swissRound(order []int32, met func(a, b int32) bool, hadBye func(a int32) bool) []pairing {
	rest := slices.Clone(order)
	var bye int32
	if len(rest)%2 == 1 {
		i := len(rest) - 1
		for j := len(rest) - 1; j >= 0; j-- {
			if !hadBye(rest[j]) {
				i = j
				break
			}
		}
		bye = rest[i]
		rest = slices.Delete(rest, i, i+1)
	}
	steps := swissSteps
	out, ok := matchUp(rest, met, &steps)
	if !ok {
		out = consecutive(rest)
	}
	if bye != 0 {
		out = append(out, pairing{bye, 0})
	}
	return out
}

// matchUp pairs the first of rest with the next one it has not met and
// the others the same way, going back when the others cannot be paired.
func matchUp(rest []int32, met func(a, b int32) bool, steps *int) ([]pairing, bool) {
	if len(rest) == 0 {
		return []pairing{}, true
	}
	*steps--
	if *steps < 0 {
		return nil, false
	}
	a := rest[0]
	for j := 1; j < len(rest); j++ {
		b := rest[j]
		if met(a, b) {
			continue
		}
		others := make([]int32, 0, len(rest)-2)
		others = append(others, rest[1:j]...)
		others = append(others, rest[j+1:]...)
		if tail, ok := matchUp(others, met, steps); ok {
			return append([]pairing{{a, b}}, tail...), true
		}
		if *steps < 0 {
			return nil, false
		}
	}
	return nil, false
}

// log2Ceil is the number of rounds that halve n to one: 1 for 2, 3 for
// 5–8.
func log2Ceil(n int) int {
	r := 0
	for v := 1; v < n; v *= 2 {
		r++
	}
	return r
}
