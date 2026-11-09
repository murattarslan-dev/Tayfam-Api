package rrule

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustLoc(t testing.TB, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestParse(t *testing.T) {
	ok := []struct{ in, canon string }{
		{"FREQ=WEEKLY", "FREQ=WEEKLY"},
		{"RRULE:FREQ=WEEKLY;INTERVAL=1", "FREQ=WEEKLY"},
		{"freq=weekly;byday=we,mo", "FREQ=WEEKLY;BYDAY=MO,WE"},
		{"FREQ=WEEKLY;INTERVAL=2;BYDAY=SU,SA;COUNT=10", "FREQ=WEEKLY;INTERVAL=2;BYDAY=SA,SU;COUNT=10"},
		{"FREQ=MONTHLY;BYDAY=-1FR,+2SA", "FREQ=MONTHLY;BYDAY=-1FR,2SA"},
		{"FREQ=MONTHLY;UNTIL=20270101T000000Z", "FREQ=MONTHLY;UNTIL=20270101T000000Z"},
		{"FREQ=WEEKLY;WKST=MO", "FREQ=WEEKLY"},
		{"FREQ=WEEKLY;INTERVAL=52;COUNT=500", "FREQ=WEEKLY;INTERVAL=52;COUNT=500"},
	}
	for _, c := range ok {
		r, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if got := r.String(); got != c.canon {
			t.Errorf("Parse(%q).String() = %q, %q bekleniyordu", c.in, got, c.canon)
		}
	}

	bad := []string{
		"", "FREQ=DAILY", "FREQ=YEARLY", "FREQ=WEEKLY;FREQ=WEEKLY", "INTERVAL=2",
		"FREQ=WEEKLY;INTERVAL=0", "FREQ=WEEKLY;INTERVAL=53", "FREQ=WEEKLY;INTERVAL=-1", "FREQ=WEEKLY;INTERVAL=1000",
		"FREQ=WEEKLY;COUNT=0", "FREQ=WEEKLY;COUNT=501", "FREQ=WEEKLY;COUNT=10;UNTIL=20270101T000000Z",
		"FREQ=WEEKLY;UNTIL=20270101", "FREQ=WEEKLY;UNTIL=20270101T000000", "FREQ=WEEKLY;UNTIL=20271301T000000Z",
		"FREQ=WEEKLY;BYDAY=1MO", "FREQ=MONTHLY;BYDAY=MO", "FREQ=MONTHLY;BYDAY=6MO", "FREQ=MONTHLY;BYDAY=0MO",
		"FREQ=MONTHLY;BYDAY=10MO", "FREQ=WEEKLY;BYDAY=MO,MO", "FREQ=WEEKLY;BYDAY=XX", "FREQ=WEEKLY;BYDAY=",
		"FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR,SA,SU,MO", "FREQ=WEEKLY;WKST=SU", "FREQ=WEEKLY;BYHOUR=10",
		"FREQ=WEEKLY;BYSETPOS=1", "FREQ=WEEKLY;", ";FREQ=WEEKLY", "FREQ=WEEKLY;COUNT", "FREQ=WEEKLY;X-Y=1",
		"FREQ=WEEKLY;COUNT=1;INTERVAL=1;BYDAY=MO;WKST=MO;UNTIL=x;A=1",
		"FREQ=WEEKLY COUNT=2", "FREQ=WEEKLY\nCOUNT=2", "FREQ=WEEKLY;BYDAY=PZ", "FREQ=WEEKLY;COUNT=+5",
		strings.Repeat("A", MaxLen+1),
	}
	for _, s := range bad {
		if _, err := Parse(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) = %v, ErrInvalid bekleniyordu", s, err)
		}
	}
}

func TestValidate(t *testing.T) {
	ist := mustLoc(t, "Europe/Istanbul")
	// Saturday 7 November 2026, the first Saturday of the month.
	start := time.Date(2026, 11, 7, 10, 0, 0, 0, ist)
	cases := []struct {
		rule string
		ok   bool
	}{
		{"FREQ=WEEKLY", true},
		{"FREQ=WEEKLY;BYDAY=SA,SU", true},
		{"FREQ=WEEKLY;BYDAY=SU", false},
		{"FREQ=MONTHLY", true},
		{"FREQ=MONTHLY;BYDAY=1SA", true},
		{"FREQ=MONTHLY;BYDAY=2SA", false},
		{"FREQ=MONTHLY;BYDAY=-4SA", true}, // November 2026 has four Saturdays
		{"FREQ=WEEKLY;UNTIL=20261107T070000Z", false},
		{"FREQ=WEEKLY;UNTIL=20261114T070000Z", true},
		{"FREQ=WEEKLY;UNTIL=20351231T000000Z", false},
	}
	for _, c := range cases {
		r, err := Parse(c.rule)
		if err != nil {
			t.Fatalf("%s: %v", c.rule, err)
		}
		if err := r.Validate(start); (err == nil) != c.ok {
			t.Errorf("Validate(%s) = %v", c.rule, err)
		}
	}
}

func expand(t *testing.T, rule string, start time.Time, n int) []time.Time {
	t.Helper()
	r, err := Parse(rule)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(start); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	occ, _ := r.Between(start, start.Add(-time.Second), start.AddDate(10, 0, 0), n)
	return occ
}

func dates(ts []time.Time, loc *time.Location) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.In(loc).Format("2006-01-02 15:04 MST")
	}
	return out
}

func wantDates(t *testing.T, got []time.Time, loc *time.Location, want ...string) {
	t.Helper()
	g := dates(got, loc)
	if strings.Join(g, "|") != strings.Join(want, "|") {
		t.Errorf("örnekler:\n %v\nbeklenen:\n %v", g, want)
	}
}

func TestWeekly(t *testing.T) {
	ist := mustLoc(t, "Europe/Istanbul")
	start := time.Date(2026, 11, 9, 19, 0, 0, 0, ist) // Monday
	wantDates(t, expand(t, "FREQ=WEEKLY;BYDAY=MO,TH;COUNT=5", start, 100), ist,
		"2026-11-09 19:00 +03", "2026-11-12 19:00 +03", "2026-11-16 19:00 +03", "2026-11-19 19:00 +03", "2026-11-23 19:00 +03")
	wantDates(t, expand(t, "FREQ=WEEKLY;INTERVAL=2;COUNT=3", start, 100), ist,
		"2026-11-09 19:00 +03", "2026-11-23 19:00 +03", "2026-12-07 19:00 +03")
	// The start's week only yields days after the start (Thursday, not the
	// Monday before a Wednesday start).
	wed := time.Date(2026, 11, 11, 8, 30, 0, 0, ist)
	wantDates(t, expand(t, "FREQ=WEEKLY;BYDAY=MO,WE;INTERVAL=2;COUNT=4", wed, 100), ist,
		"2026-11-11 08:30 +03", "2026-11-23 08:30 +03", "2026-11-25 08:30 +03", "2026-12-07 08:30 +03")
	// UNTIL is inclusive.
	wantDates(t, expand(t, "FREQ=WEEKLY;UNTIL=20261123T160000Z", start, 100), ist,
		"2026-11-09 19:00 +03", "2026-11-16 19:00 +03", "2026-11-23 19:00 +03")
}

func TestMonthly(t *testing.T) {
	ist := mustLoc(t, "Europe/Istanbul")
	// The 31st: months without it are skipped.
	start := time.Date(2027, 1, 31, 9, 0, 0, 0, ist)
	wantDates(t, expand(t, "FREQ=MONTHLY;COUNT=4", start, 100), ist,
		"2027-01-31 09:00 +03", "2027-03-31 09:00 +03", "2027-05-31 09:00 +03", "2027-07-31 09:00 +03")
	// The last Friday and the second Saturday.
	sat := time.Date(2026, 11, 14, 10, 0, 0, 0, ist)
	wantDates(t, expand(t, "FREQ=MONTHLY;BYDAY=2SA,-1FR;COUNT=4", sat, 100), ist,
		"2026-11-14 10:00 +03", "2026-11-27 10:00 +03", "2026-12-12 10:00 +03", "2026-12-25 10:00 +03")
	// The fifth Saturday exists only in some months.
	fifth := time.Date(2026, 8, 29, 10, 0, 0, 0, ist)
	wantDates(t, expand(t, "FREQ=MONTHLY;BYDAY=5SA;COUNT=3", fifth, 100), ist,
		"2026-08-29 10:00 +03", "2026-10-31 10:00 +03", "2027-01-30 10:00 +03")
}

// TestDaylightSaving: the local wall-clock time stays the same across a
// daylight saving change; the UTC time moves. Europe/Istanbul has no DST,
// so Europe/Berlin is used.
func TestDaylightSaving(t *testing.T) {
	ber := mustLoc(t, "Europe/Berlin")
	// Thursdays at 19:00; Berlin moves to summer time on 29 March 2026.
	start := time.Date(2026, 3, 19, 19, 0, 0, 0, ber)
	occ := expand(t, "FREQ=WEEKLY;COUNT=3", start, 100)
	wantDates(t, occ, ber, "2026-03-19 19:00 CET", "2026-03-26 19:00 CET", "2026-04-02 19:00 CEST")
	wantDates(t, occ, time.UTC, "2026-03-19 18:00 UTC", "2026-03-26 18:00 UTC", "2026-04-02 17:00 UTC")

	// And back to winter time on 25 October 2026.
	autumn := time.Date(2026, 10, 18, 10, 0, 0, 0, ber)
	occ = expand(t, "FREQ=WEEKLY;COUNT=2", autumn, 100)
	wantDates(t, occ, ber, "2026-10-18 10:00 CEST", "2026-10-25 10:00 CET")
	wantDates(t, occ, time.UTC, "2026-10-18 08:00 UTC", "2026-10-25 09:00 UTC")

	// A time inside the spring gap (02:30 on 29 March does not exist) moves
	// forward by the gap.
	gap := time.Date(2026, 3, 22, 2, 30, 0, 0, ber)
	occ = expand(t, "FREQ=WEEKLY;COUNT=3", gap, 100)
	wantDates(t, occ, ber, "2026-03-22 02:30 CET", "2026-03-29 03:30 CEST", "2026-04-05 02:30 CEST")
}

func TestBetweenWindowAndLimit(t *testing.T) {
	ist := mustLoc(t, "Europe/Istanbul")
	start := time.Date(2026, 11, 9, 19, 0, 0, 0, ist)
	r, err := Parse("FREQ=WEEKLY")
	if err != nil {
		t.Fatal(err)
	}
	// The window excludes after and includes before.
	occ, more := r.Between(start, start, start.AddDate(0, 0, 14), 10)
	wantDates(t, occ, ist, "2026-11-16 19:00 +03", "2026-11-23 19:00 +03")
	if !more {
		t.Error("açık uçlu kuralda more = false")
	}
	// The limit stops early and reports more.
	occ, more = r.Between(start, start, start.AddDate(1, 0, 0), 3)
	if len(occ) != 3 || !more {
		t.Errorf("sınır: %d örnek, more=%v", len(occ), more)
	}
	// An exhausted rule reports no more.
	c, _ := Parse("FREQ=WEEKLY;COUNT=2")
	occ, more = c.Between(start, start, start.AddDate(1, 0, 0), 10)
	if len(occ) != 1 || more {
		t.Errorf("COUNT=2: %d örnek, more=%v", len(occ), more)
	}
	// A window entirely after the last occurrence.
	occ, more = c.Between(start, start.AddDate(0, 1, 0), start.AddDate(0, 2, 0), 10)
	if len(occ) != 0 || more {
		t.Errorf("bitmiş kural: %d örnek, more=%v", len(occ), more)
	}
	// Zero limit returns nothing.
	if occ, _ := r.Between(start, start, start.AddDate(1, 0, 0), 0); len(occ) != 0 {
		t.Error("limit 0")
	}
}

// TestBounded: an open-ended rule whose window lies far in the future still
// ends after maxPeriods periods.
func TestBounded(t *testing.T) {
	start := time.Date(2026, 11, 9, 19, 0, 0, 0, time.UTC)
	r, _ := Parse("FREQ=MONTHLY;BYDAY=5FR")
	done := make(chan struct{})
	go func() {
		defer close(done)
		occ, more := r.Between(start, start.AddDate(5000, 0, 0), start.AddDate(6000, 0, 0), 100)
		if len(occ) != 0 || more {
			t.Errorf("üst sınır: %d örnek, more=%v", len(occ), more)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("genişletme sınırlı değil")
	}
}

// FuzzParse: no input panics; an accepted rule round-trips through String
// and expands within its limits, in order and inside the window.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"FREQ=WEEKLY", "FREQ=WEEKLY;BYDAY=MO,WE,FR;INTERVAL=2", "FREQ=MONTHLY;BYDAY=-1SU;COUNT=12",
		"RRULE:FREQ=MONTHLY;UNTIL=20271231T235959Z", "FREQ=WEEKLY;WKST=MO;COUNT=500", "freq=monthly;byday=+5sa",
	} {
		f.Add(s)
	}
	ber, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		f.Fatal(err)
	}
	start := time.Date(2026, 3, 1, 2, 30, 0, 0, ber)
	f.Fuzz(func(t *testing.T, s string) {
		r, err := Parse(s)
		if err != nil {
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("hata türü: %v", err)
			}
			return
		}
		canon := r.String()
		if len(canon) > MaxLen {
			t.Fatalf("kanonik biçim çok uzun: %d", len(canon))
		}
		r2, err := Parse(canon)
		if err != nil || r2.String() != canon {
			t.Fatalf("gidiş-dönüş: %q → %q (%v)", s, canon, err)
		}
		const limit = 64
		after := start.AddDate(0, 1, 0)
		before := after.AddDate(1, 0, 0)
		occ, _ := r.Between(start, after, before, limit)
		if len(occ) > limit {
			t.Fatalf("%d örnek > %d", len(occ), limit)
		}
		prev := after
		for _, o := range occ {
			if !o.After(prev) || o.After(before) {
				t.Fatalf("sıra/pencere dışı örnek: %v (önceki %v)", o, prev)
			}
			prev = o
		}
	})
}
