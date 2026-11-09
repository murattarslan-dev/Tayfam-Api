package refcode

import (
	"regexp"
	"testing"
)

func TestNewMatchesFormat(t *testing.T) {
	re := regexp.MustCompile(`^TYF-[2-9A-HJ-NP-Z]{6}$`)
	seen := map[byte]bool{}
	for range 4000 {
		c := New()
		if !re.MatchString(c) {
			t.Fatalf("kod biçimi: %s", c)
		}
		for i := 4; i < len(c); i++ {
			seen[c[i]] = true
		}
	}
	// Every character of the alphabet is used, none outside it.
	if len(seen) != len(alphabet) {
		t.Fatalf("%d farklı karakter, %d bekleniyordu", len(seen), len(alphabet))
	}
}

func TestNormalize(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"TYF-8K2M4Q", "TYF-8K2M4Q"},
		{"tyf-8k2m4q", "TYF-8K2M4Q"},
		{"\t8k2m4q\n", "TYF-8K2M4Q"}, // spaces around are dropped
		{"8K2M4Q", "TYF-8K2M4Q"},
		{"TYF-8K2M4O", ""}, // O is not in the alphabet
		{"TYF-8K2M4", ""},
		{"TYF-8K2M4QQ", ""},
		{"", ""},
		{"TYF-", ""},
		{"XYZ-8K2M4Q", ""},
	} {
		in, want := tc.in, tc.want
		got, ok := Normalize(in)
		if got != want || ok != (want != "") {
			t.Errorf("Normalize(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := Normalize(string(make([]byte, 64))); ok {
		t.Fatal("uzun girdi kabul edildi")
	}
}
