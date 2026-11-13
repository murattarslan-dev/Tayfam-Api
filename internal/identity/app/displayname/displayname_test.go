package displayname

import (
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	for name, tc := range map[string]struct {
		in, want string
		ok       bool
	}{
		"düz":                  {"Ayşe", "Ayşe", true},
		"boşluklar":            {"  Ayşe   Yılmaz ", "Ayşe Yılmaz", true},
		"boş":                  {"   ", "", true},
		"40 karakter":          {strings.Repeat("ş", 40), strings.Repeat("ş", 40), true},
		"41 karakter":          {strings.Repeat("ş", 41), "", false},
		"kontrol karakteri":    {"Ali\u0007", "", false},
		"C1 kontrol":           {"Ali\u0090x", "", false},
		"sekme boşluk sayılır": {"Ali\tVeli", "Ali Veli", true},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := Clean(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("Clean(%q) = %q, %v; istenen %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}
