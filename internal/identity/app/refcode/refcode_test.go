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
	for in, want := range map[string]string{
		"TYF-8K2M4Q":     "TYF-8K2M4Q",
		"tyf-8k2m4q":     "TYF-8K2M4Q",
		" 8k2m4q ":       "TYF-8K2M4Q",
		"8K2M4Q":         "TYF-8K2M4Q",
		"TYF-8K2M4O":     "", // O is not in the alphabet
		"TYF-8K2M4":      "",
		"TYF-8K2M4QQ":    "",
		"":               "",
		"TYF-":           "",
		"XYZ-8K2M4Q":     "",
		"TYF-8K2M4Q\n\t": "TYF-8K2M4Q",
	} {
		got, ok := Normalize(in)
		if got != want || ok != (want != "") {
			t.Errorf("Normalize(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := Normalize(string(make([]byte, 64))); ok {
		t.Fatal("uzun girdi kabul edildi")
	}
}
