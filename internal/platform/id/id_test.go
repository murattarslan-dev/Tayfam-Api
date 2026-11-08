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
