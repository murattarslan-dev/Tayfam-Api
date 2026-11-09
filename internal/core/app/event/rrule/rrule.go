// Package rrule parses and expands a narrow subset of RFC 5545 recurrence
// rules for event series (issue #51; data model v14 "Tekrarlayan
// etkinlik"). It uses the standard library only.
//
// Supported, and nothing else:
//
//	FREQ=WEEKLY | MONTHLY            required
//	INTERVAL=1..52                   default 1
//	BYDAY=MO,TU,…                    WEEKLY: weekdays without an ordinal
//	BYDAY=2SA,-1FR,…                 MONTHLY: weekdays with an ordinal ±1..5
//	COUNT=1..500 | UNTIL=…Z          at most one; UNTIL in UTC form only
//	WKST=MO                          accepted, nothing else
//
// An optional "RRULE:" prefix is accepted. Names and values are case
// insensitive; String returns the canonical form that is stored.
//
// Without BYDAY a WEEKLY rule repeats on the start's weekday and a MONTHLY
// rule on the start's day of the month; months without that day (the 31st,
// a fifth Friday) are skipped, as RFC 5545 says.
//
// Rules come from clients, so every path is bounded: the text length, the
// number of parts and BYDAY entries, COUNT, how far UNTIL may lie after the
// start, the number of occurrences one call returns and the number of
// periods an expansion walks (maxPeriods). A rule cannot make Between loop
// or allocate without limit.
package rrule

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Limits.
const (
	// MaxLen is the longest rule text accepted (event_series_rrule_len).
	MaxLen = 500
	// MaxInterval bounds INTERVAL.
	MaxInterval = 52
	// MaxCount bounds COUNT.
	MaxCount = 500
	// MaxSpan bounds how far UNTIL may be after the series start.
	MaxSpan = 5 * 366 * 24 * time.Hour

	maxParts = 6
	maxByDay = 7
	// maxPeriods bounds the weeks or months one expansion walks. Weekly at
	// INTERVAL=1 this is about 96 years.
	maxPeriods = 5000
)

// ErrInvalid is returned for any rule outside the supported subset. It
// never carries the rule text.
var ErrInvalid = errors.New("tekrar kuralı geçersiz ya da desteklenmiyor")

// Freq is the rule's frequency.
type Freq int

// Frequencies.
const (
	Weekly Freq = iota + 1
	Monthly
)

// Day is a BYDAY entry: a weekday and, for MONTHLY rules, its ordinal in the
// month (1 = first, -1 = last). N is 0 for WEEKLY rules.
type Day struct {
	N       int
	Weekday time.Weekday
}

// Rule is a parsed recurrence rule.
type Rule struct {
	Freq     Freq
	Interval int
	// ByDay is sorted (by ordinal, then Monday first) and has no duplicates.
	ByDay []Day
	// Count is 0 when the rule has no COUNT.
	Count int
	// Until is zero when the rule has no UNTIL; otherwise UTC.
	Until time.Time
}

var dayNames = [...]string{
	time.Sunday: "SU", time.Monday: "MO", time.Tuesday: "TU", time.Wednesday: "WE",
	time.Thursday: "TH", time.Friday: "FR", time.Saturday: "SA",
}

const untilLayout = "20060102T150405Z"

// Parse parses s. Any part outside the subset is ErrInvalid.
func Parse(s string) (Rule, error) {
	if len(s) == 0 || len(s) > MaxLen {
		return Rule{}, ErrInvalid
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x21 || c > 0x7e {
			return Rule{}, ErrInvalid
		}
	}
	s = strings.ToUpper(s)
	s = strings.TrimPrefix(s, "RRULE:")
	parts := strings.Split(s, ";")
	if len(parts) > maxParts {
		return Rule{}, ErrInvalid
	}
	r := Rule{Interval: 1}
	seen := map[string]bool{}
	for _, p := range parts {
		name, value, ok := strings.Cut(p, "=")
		if !ok || name == "" || value == "" || seen[name] {
			return Rule{}, ErrInvalid
		}
		seen[name] = true
		var err error
		switch name {
		case "FREQ":
			switch value {
			case "WEEKLY":
				r.Freq = Weekly
			case "MONTHLY":
				r.Freq = Monthly
			default:
				return Rule{}, ErrInvalid
			}
		case "INTERVAL":
			r.Interval, err = number(value, 1, MaxInterval)
		case "COUNT":
			r.Count, err = number(value, 1, MaxCount)
		case "UNTIL":
			if len(value) != len(untilLayout) {
				return Rule{}, ErrInvalid
			}
			r.Until, err = time.Parse(untilLayout, value)
			r.Until = r.Until.UTC()
		case "BYDAY":
			r.ByDay, err = byDay(value)
		case "WKST":
			if value != "MO" {
				return Rule{}, ErrInvalid
			}
		default:
			return Rule{}, ErrInvalid
		}
		if err != nil {
			return Rule{}, ErrInvalid
		}
	}
	if r.Freq == 0 || (r.Count != 0 && !r.Until.IsZero()) {
		return Rule{}, ErrInvalid
	}
	for _, d := range r.ByDay {
		// WEEKLY takes plain weekdays, MONTHLY needs an ordinal.
		if (r.Freq == Weekly) != (d.N == 0) {
			return Rule{}, ErrInvalid
		}
	}
	return r, nil
}

// number parses 1–3 ASCII digits in [lo, hi].
func number(s string, lo, hi int) (int, error) {
	if len(s) == 0 || len(s) > 3 {
		return 0, ErrInvalid
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, ErrInvalid
		}
		n = n*10 + int(s[i]-'0')
	}
	if n < lo || n > hi {
		return 0, ErrInvalid
	}
	return n, nil
}

func byDay(s string) ([]Day, error) {
	items := strings.Split(s, ",")
	if len(items) > maxByDay {
		return nil, ErrInvalid
	}
	out := make([]Day, 0, len(items))
	for _, it := range items {
		if len(it) < 2 {
			return nil, ErrInvalid
		}
		wd := slices.Index(dayNames[:], it[len(it)-2:])
		if wd < 0 {
			return nil, ErrInvalid
		}
		d := Day{Weekday: time.Weekday(wd)}
		if ord := it[:len(it)-2]; ord != "" {
			sign := 1
			switch ord[0] {
			case '-':
				sign, ord = -1, ord[1:]
			case '+':
				ord = ord[1:]
			}
			if len(ord) != 1 {
				return nil, ErrInvalid
			}
			n, err := number(ord, 1, 5)
			if err != nil {
				return nil, err
			}
			d.N = sign * n
		}
		if slices.Contains(out, d) {
			return nil, ErrInvalid
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Day) int {
		if a.N != b.N {
			return a.N - b.N
		}
		return mondayIndex(a.Weekday) - mondayIndex(b.Weekday)
	})
	return out, nil
}

// mondayIndex is 0 for Monday … 6 for Sunday (WKST=MO).
func mondayIndex(d time.Weekday) int { return (int(d) + 6) % 7 }

// String returns the canonical form, which Parse reads back to the same
// rule.
func (r Rule) String() string {
	var b strings.Builder
	b.WriteString("FREQ=")
	if r.Freq == Monthly {
		b.WriteString("MONTHLY")
	} else {
		b.WriteString("WEEKLY")
	}
	if r.Interval > 1 {
		b.WriteString(";INTERVAL=" + strconv.Itoa(r.Interval))
	}
	if len(r.ByDay) > 0 {
		b.WriteString(";BYDAY=")
		for i, d := range r.ByDay {
			if i > 0 {
				b.WriteByte(',')
			}
			if d.N != 0 {
				b.WriteString(strconv.Itoa(d.N))
			}
			b.WriteString(dayNames[d.Weekday])
		}
	}
	if r.Count > 0 {
		b.WriteString(";COUNT=" + strconv.Itoa(r.Count))
	}
	if !r.Until.IsZero() {
		b.WriteString(";UNTIL=" + r.Until.UTC().Format(untilLayout))
	}
	return b.String()
}

// Validate checks the rule against the series start, whose location is the
// series time zone: the start must itself be an occurrence of the rule (it
// is the first one and COUNT counts it), and UNTIL must lie after the start
// and at most MaxSpan later.
func (r Rule) Validate(start time.Time) error {
	if !r.Until.IsZero() && (!r.Until.After(start) || r.Until.Sub(start) > MaxSpan) {
		return ErrInvalid
	}
	if len(r.ByDay) == 0 {
		return nil
	}
	y, m, d := start.Date()
	for _, bd := range r.ByDay {
		if r.Freq == Weekly && bd.Weekday == start.Weekday() {
			return nil
		}
		if r.Freq == Monthly {
			if day, ok := nthWeekday(y, m, bd); ok && day == d {
				return nil
			}
		}
	}
	return ErrInvalid
}

// Between returns the occurrences t of the rule with after < t ≤ before,
// in order, at most limit of them. start is the first occurrence (DTSTART)
// in the series time zone; every occurrence keeps its local wall-clock time
// across daylight saving changes. more reports whether the rule has
// occurrences after the last one returned: past before, or beyond limit.
//
// A wall-clock time that does not exist on a given day (inside a spring
// forward gap) is resolved by time.Date, which moves it forward by the
// length of the gap, as RFC 5545 asks.
func (r Rule) Between(start, after, before time.Time, limit int) (occ []time.Time, more bool) {
	if limit <= 0 || r.Freq == 0 || r.Interval < 1 {
		return nil, false
	}
	loc := start.Location()
	hh, mm, ss := start.Clock()
	sy, sm, sd := start.Date()
	emitted := 0
	stop := false
	// consider handles one candidate in order; it sets stop when nothing
	// later can be returned.
	consider := func(t time.Time) {
		if (r.Count > 0 && emitted >= r.Count) || (!r.Until.IsZero() && t.After(r.Until)) {
			stop = true
			return
		}
		emitted++
		if !t.After(after) {
			return
		}
		if t.After(before) || len(occ) == limit {
			more, stop = true, true
			return
		}
		occ = append(occ, t)
	}
	ns := start.Nanosecond()
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, hh, mm, ss, ns, loc) }

	consider(start)
	// Calendar arithmetic is done on UTC dates, independent of the zone.
	startDate := time.Date(sy, sm, sd, 0, 0, 0, 0, time.UTC)
	switch r.Freq {
	case Weekly:
		days := r.ByDay
		if len(days) == 0 {
			days = []Day{{Weekday: start.Weekday()}}
		}
		weekStart := startDate.AddDate(0, 0, -mondayIndex(start.Weekday()))
		for k := 0; k < maxPeriods && !stop; k++ {
			ws := weekStart.AddDate(0, 0, 7*r.Interval*k)
			for _, d := range days {
				day := ws.AddDate(0, 0, mondayIndex(d.Weekday))
				t := at(day.Date())
				if !t.After(start) {
					continue
				}
				if consider(t); stop {
					break
				}
			}
		}
	case Monthly:
		monthStart := time.Date(sy, sm, 1, 0, 0, 0, 0, time.UTC)
		dates := make([]int, 0, maxByDay)
		for k := 0; k < maxPeriods && !stop; k++ {
			ms := monthStart.AddDate(0, r.Interval*k, 0)
			y, m, _ := ms.Date()
			dates = dates[:0]
			if len(r.ByDay) == 0 {
				if sd <= daysIn(y, m) {
					dates = append(dates, sd)
				}
			} else {
				for _, bd := range r.ByDay {
					if d, ok := nthWeekday(y, m, bd); ok && !slices.Contains(dates, d) {
						dates = append(dates, d)
					}
				}
				slices.Sort(dates)
			}
			for _, d := range dates {
				t := at(y, m, d)
				if !t.After(start) {
					continue
				}
				if consider(t); stop {
					break
				}
			}
		}
	}
	return occ, more
}

func daysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// nthWeekday returns the day of month of the d.N-th d.Weekday in y-m;
// ok is false when the month has no such day.
func nthWeekday(y int, m time.Month, d Day) (int, bool) {
	n := daysIn(y, m)
	if d.N > 0 {
		first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC).Weekday()
		day := 1 + (int(d.Weekday)-int(first)+7)%7 + 7*(d.N-1)
		return day, day <= n
	}
	if d.N < 0 {
		last := time.Date(y, m, n, 0, 0, 0, 0, time.UTC).Weekday()
		day := n - (int(last)-int(d.Weekday)+7)%7 + 7*(d.N+1)
		return day, day >= 1
	}
	return 0, false
}
