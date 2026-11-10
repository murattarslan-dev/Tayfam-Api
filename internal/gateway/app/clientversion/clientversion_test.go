package clientversion

import (
	"errors"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	valid := []string{
		"0.0.0", "0.1.0", "1.4.2", "10.20.30", "1.0.0-alpha", "1.0.0-alpha.1",
		"1.0.0-0.3.7", "1.0.0-x.7.z.92", "1.0.0-x-y-z.--", "1.0.0+20130313144700",
		"1.0.0-beta+exp.sha.5114f85", "1.0.0+21AF26D3----117B344092BD", "1.0.0+001",
		"18446744073709551615.0.0",
	}
	for _, s := range valid {
		v, err := Parse(s)
		if err != nil {
			t.Errorf("Parse(%q): %v", s, err)
			continue
		}
		if v.String() != s {
			t.Errorf("String() = %q, want %q", v.String(), s)
		}
	}
	invalid := []string{
		"", "1", "1.2", "1.2.3.4", "v1.2.3", "01.2.3", "1.02.3", "1.2.03",
		"1.2.3-", "1.2.3+", "1.2.3-01", "1.2.3-a..b", "1.2.3+a..b", "1.2.3-a_b",
		"1.2.3 ", " 1.2.3", "-1.2.3", "1.-2.3", "a.b.c", "1.2.3-ğ",
		"18446744073709551616.0.0", "1.2.3-" + strings.Repeat("a", 64),
	}
	for _, s := range invalid {
		if _, err := Parse(s); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("Parse(%q) = %v, want ErrInvalidVersion", s, err)
		}
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "2.1.0", -1},
		{"2.1.0", "2.1.1", -1},
		{"1.10.0", "1.9.0", 1},
		{"0.1.0", "0.0.9", 1},
		// Pre-release ranks below its release.
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		// The chain from semver.org §11.
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-alpha.beta", "1.0.0-beta", -1},
		{"1.0.0-beta", "1.0.0-beta.2", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-beta.11", "1.0.0-rc.1", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		// Numbers below words; numeric, not textual, order.
		{"1.0.0-1", "1.0.0-a", -1},
		{"1.0.0-9", "1.0.0-10", -1},
		{"1.0.0-99999999999999999999", "1.0.0-100000000000000000000", -1},
		// Build metadata never counts.
		{"1.0.0+1", "1.0.0+2", 0},
		{"1.0.0-rc.1+b", "1.0.0-rc.1", 0},
		{"1.0.0+b", "1.0.1", -1},
	}
	for _, tt := range tests {
		if got := MustParse(tt.a).Compare(MustParse(tt.b)); got != tt.want {
			t.Errorf("%s ? %s = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := MustParse(tt.b).Compare(MustParse(tt.a)); got != -tt.want {
			t.Errorf("%s ? %s = %d, want %d (ters)", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestParseHeader(t *testing.T) {
	tests := []struct {
		in   string
		plat Platform
		ok   bool
	}{
		{"android/1.4.2", Android, true},
		{"ios/0.1.0-beta.3", IOS, true},
		{"web/2.0.0+5f3a", Web, true},
		{"", "", false},
		{"android", "", false},
		{"android/", "", false},
		{"/1.0.0", "", false},
		{"Android/1.0.0", "", false},
		{"windows/1.0.0", "", false},
		{"android/v1.0.0", "", false},
		{"android/1.0", "", false},
		{"android/1.0.0/x", "", false},
		{"android 1.0.0", "", false},
		{"android/" + strings.Repeat("1", 80) + ".0.0", "", false},
	}
	for _, tt := range tests {
		plat, _, err := ParseHeader(tt.in)
		if (err == nil) != tt.ok || plat != tt.plat {
			t.Errorf("ParseHeader(%q) = %q, %v", tt.in, plat, err)
		}
	}
}

func TestNewPolicy(t *testing.T) {
	p, err := NewPolicy(map[Platform]RawRange{
		Android: {Min: "1.2.0", Latest: "1.5.0"},
		IOS:     {Min: "1.1.0"},
		Web:     {Latest: "2.0.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Range(Android); r.Min.String() != "1.2.0" || r.Latest.String() != "1.5.0" {
		t.Errorf("android = %+v", r)
	}
	if r := p.Range(IOS); r.Latest.String() != "1.1.0" {
		t.Errorf("son sürüm boşsa en düşük sürüm olmalı: %+v", r)
	}
	if r := p.Range(Web); !r.Min.IsZero() || r.Latest.String() != "2.0.0" {
		t.Errorf("web = %+v", r)
	}

	bad := map[string]map[Platform]RawRange{
		"geçersiz en düşük":     {Android: {Min: "1.2"}},
		"geçersiz son":          {IOS: {Min: "1.0.0", Latest: "latest"}},
		"son en düşükten küçük": {Web: {Min: "2.0.0", Latest: "1.9.9"}},
		"v öneki":               {Android: {Min: "v1.0.0"}},
		"bilinmeyen platform":   {Platform("windows"): {Min: "1.0.0"}},
	}
	for name, raw := range bad {
		if _, err := NewPolicy(raw); err == nil {
			t.Errorf("%s: hata bekleniyordu", name)
		}
	}
}

func TestCheck(t *testing.T) {
	p, err := NewPolicy(map[Platform]RawRange{
		Android: {Min: "1.2.0"},
		IOS:     {Min: "1.0.0-rc.2"},
		// web: no minimum, nothing refused.
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		values []string
		want   Decision
	}{
		{"başlık yok", nil, Missing},
		{"en düşük sürümün kendisi", []string{"android/1.2.0"}, Allowed},
		{"daha yeni", []string{"android/1.10.0"}, Allowed},
		{"eski", []string{"android/1.1.9"}, Outdated},
		{"en düşüğün ön sürümü eski sayılır", []string{"android/1.2.0-rc.1"}, Outdated},
		{"derleme bilgisi sayılmaz", []string{"android/1.2.0+7"}, Allowed},
		{"ön sürüm sınırı: daha yeni ön sürüm", []string{"ios/1.0.0-rc.10"}, Allowed},
		{"ön sürüm sınırı: eski ön sürüm", []string{"ios/1.0.0-rc.1"}, Outdated},
		{"ön sürüm sınırı: sürümün kendisi", []string{"ios/1.0.0"}, Allowed},
		{"en düşük tanımsız", []string{"web/0.0.1"}, Allowed},
		{"bozuk başlık", []string{"android/1.2"}, Invalid},
		{"bilinmeyen platform", []string{"desktop/9.9.9"}, Invalid},
		{"iki başlık", []string{"android/1.2.0", "android/1.2.0"}, Invalid},
	}
	for _, tt := range tests {
		if got := p.Check(tt.values); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}

	// The zero Policy refuses nothing that parses.
	var zero Policy
	if got := zero.Check([]string{"android/0.0.1"}); got != Allowed {
		t.Errorf("boş politika: %v", got)
	}
}

// TestWithout: a platform that is not served (web in prod, ADR-0017) is
// Invalid whatever its version, and Served leaves it out; the original
// policy is unchanged.
func TestWithout(t *testing.T) {
	p, err := NewPolicy(map[Platform]RawRange{Android: {Min: "1.2.0"}})
	if err != nil {
		t.Fatal(err)
	}
	mobile := p.Without(Web)
	for _, h := range []string{"web/0.0.1", "web/99.0.0"} {
		if got := mobile.Check([]string{h}); got != Invalid {
			t.Errorf("%s: %v, want Invalid", h, got)
		}
	}
	if got := mobile.Check([]string{"android/1.2.0"}); got != Allowed {
		t.Errorf("android: %v", got)
	}
	if got := mobile.Check(nil); got != Missing {
		t.Errorf("başlık yok: %v", got)
	}
	if got := mobile.Served(); len(got) != 2 || got[0] != Android || got[1] != IOS {
		t.Errorf("Served = %v", got)
	}
	if got := p.Check([]string{"web/0.0.1"}); got != Allowed {
		t.Errorf("özgün politika değişti: %v", got)
	}
	if got := len(p.Served()); got != 3 {
		t.Errorf("özgün politika platformları: %d", got)
	}
	var zero Policy
	if got := zero.Without(Web).Check([]string{"web/1.0.0"}); got != Invalid {
		t.Errorf("boş politika web: %v", got)
	}
}
