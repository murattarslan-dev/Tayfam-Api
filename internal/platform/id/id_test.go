package id

import (
	"regexp"
	"testing"
	"time"
)

var canonical = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIsV7(t *testing.T) {
	for range 1000 {
		if s := New().String(); !canonical.MatchString(s) {
			t.Fatalf("UUIDv7 biçimi değil: %s", s)
		}
	}
}

func TestOrderedByTime(t *testing.T) {
	a := newAt(time.UnixMilli(1_700_000_000_000))
	b := newAt(time.UnixMilli(1_700_000_000_001))
	if a.String() >= b.String() {
		t.Fatalf("zaman sırası bozuk: %s >= %s", a, b)
	}
	if got := a.String()[:13]; got != "018bcfe5-6800" {
		t.Fatalf("zaman alanı = %s", got)
	}
}

func TestUnique(t *testing.T) {
	seen := make(map[ID]bool, 10000)
	for range 10000 {
		u := New()
		if seen[u] {
			t.Fatal("tekrar eden ID")
		}
		seen[u] = true
	}
}

func TestPGRoundTrip(t *testing.T) {
	u := New()
	got, ok := FromPG(u.PG())
	if !ok || got != u {
		t.Fatal("pgtype dönüşümü bozuk")
	}
}

func TestParse(t *testing.T) {
	u := New()
	got, ok := Parse(u.String())
	if !ok || got != u {
		t.Fatal("String/Parse turu bozuk")
	}
	for _, s := range []string{"", "x", u.String()[:35], u.String() + "0", "zzzzzzzz-zzzz-7zzz-8zzz-zzzzzzzzzzzz", "0190a1b2c3d47e5f8a9b0c1d2e3f4a5b0000"} {
		if _, ok := Parse(s); ok {
			t.Errorf("Parse(%q) kabul edildi", s)
		}
	}
}

func TestAfter(t *testing.T) {
	less := func(a, b ID) bool { return a.String() < b.String() }
	// A previous ID from the future: After still sorts after it.
	future := newAt(time.Now().Add(time.Hour))
	for range 100 {
		next := After(future)
		if !less(future, next) || !canonical.MatchString(next.String()) {
			t.Fatalf("%s !< %s", future, next)
		}
		future = next
	}
	// The usual case: a fresh ID.
	past := newAt(time.Now().Add(-time.Hour))
	if n := After(past); !less(past, n) || n.String()[:13] == past.String()[:13] {
		t.Fatalf("yeni ID bekleniyordu: %s", n)
	}
	// Carry through the random bits.
	edge := newAt(time.Now().Add(time.Hour))
	for i := 9; i < 16; i++ {
		edge[i] = 0xff
	}
	if n := After(edge); !less(edge, n) || !canonical.MatchString(n.String()) {
		t.Fatalf("taşma: %s → %s", edge, n)
	}
	edge[8] = 0xbf // variant 10, all six random bits set
	if n := After(edge); !less(edge, n) || !canonical.MatchString(n.String()) {
		t.Fatalf("milisaniye taşması: %s → %s", edge, n)
	}
}
