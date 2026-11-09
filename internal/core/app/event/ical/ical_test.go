package ical

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Pazar buluşması", "Pazar buluşması"},
		{`a\b`, `a\\b`},
		{"a;b,c", `a\;b\,c`},
		{"satır 1\nsatır 2", `satır 1\nsatır 2`},
		{"satır 1\r\nsatır 2", `satır 1\nsatır 2`},
		{"satır 1\rsatır 2", `satır 1\nsatır 2`},
		{"\r\n\r\n", `\n\n`},
		{"a\tb", "a\tb"},
		{"a\x00b\x1bc\x7fd\u0085e", "abcde"},
		{"geçersiz \xff utf8", "geçersiz � utf8"},
		{`\n`, `\\n`},
	}
	for _, c := range cases {
		if got := Escape(c.in); got != c.want {
			t.Errorf("Escape(%q) = %q, %q bekleniyordu", c.in, got, c.want)
		}
	}
}

func TestFold(t *testing.T) {
	// Short lines are untouched.
	if got := fold("SUMMARY:kısa"); got != "SUMMARY:kısa\r\n" {
		t.Errorf("fold = %q", got)
	}
	// Exactly 75 octets: no fold.
	s75 := "DESCRIPTION:" + strings.Repeat("a", 75-len("DESCRIPTION:"))
	if got := fold(s75); got != s75+"\r\n" {
		t.Errorf("75 oktet katlandı: %q", got)
	}
	for _, s := range []string{
		"DESCRIPTION:" + strings.Repeat("a", 200),
		"DESCRIPTION:" + strings.Repeat("ğüşİöç", 40),
		"SUMMARY:" + strings.Repeat("🚗", 50),
		"X:" + strings.Repeat("ab€", 77),
	} {
		got := fold(s)
		if !strings.HasSuffix(got, "\r\n") {
			t.Fatalf("CRLF ile bitmiyor: %q", got)
		}
		lines := strings.Split(strings.TrimSuffix(got, "\r\n"), "\r\n")
		var unfolded strings.Builder
		for i, l := range lines {
			if len(l) > maxLine {
				t.Errorf("satır %d: %d oktet", i, len(l))
			}
			if !utf8.ValidString(l) {
				t.Errorf("satır %d UTF-8 dizisini bölüyor: %q", i, l)
			}
			if i > 0 {
				if !strings.HasPrefix(l, " ") {
					t.Errorf("devam satırı boşlukla başlamıyor: %q", l)
				}
				l = l[1:]
			}
			unfolded.WriteString(l)
		}
		if unfolded.String() != s {
			t.Errorf("açılan satır farklı:\n%q\n%q", unfolded.String(), s)
		}
	}
}

func TestWrite(t *testing.T) {
	start := time.Date(2026, 11, 15, 7, 0, 0, 0, time.UTC)
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.FixedZone("TRT", 3*3600))
	out := string(Write(Event{
		UID: "0195f000-0000-7000-8000-000000000001", Start: start, End: start.Add(3 * time.Hour),
		Created: start.Add(-48 * time.Hour), Modified: start.Add(-24 * time.Hour),
		Summary: "Ege turu; kahvaltı, sonra yol", Description: "Satır 1\nSUMMARY:sahte\r\nEND:VEVENT",
		Location: "Kordon, Konak, İzmir",
	}, now))

	want := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Tayfam//Tayfam API//TR",
		"CALSCALE:GREGORIAN",
		"METHOD:PUBLISH",
		"BEGIN:VEVENT",
		"UID:0195f000-0000-7000-8000-000000000001",
		"DTSTAMP:20261109T090000Z",
		"DTSTART:20261115T070000Z",
		"DTEND:20261115T100000Z",
		"CREATED:20261113T070000Z",
		"LAST-MODIFIED:20261114T070000Z",
		`SUMMARY:Ege turu\; kahvaltı\, sonra yol`,
		`DESCRIPTION:Satır 1\nSUMMARY:sahte\nEND:VEVENT`,
		`LOCATION:Kordon\, Konak\, İzmir`,
		"STATUS:CONFIRMED",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n") + "\r\n"
	if out != want {
		t.Errorf("çıktı:\n%s\nbeklenen:\n%s", out, want)
	}
	// Injected line breaks never start a property.
	var summaries, ends int
	for _, l := range strings.Split(out, "\r\n") {
		if strings.HasPrefix(l, "SUMMARY:") {
			summaries++
		}
		if l == "END:VEVENT" {
			ends++
		}
	}
	if summaries != 1 || ends != 1 {
		t.Errorf("enjekte satır: SUMMARY %d, END:VEVENT %d", summaries, ends)
	}

	c := string(Write(Event{UID: "x", Start: start, End: start.Add(time.Hour), Summary: "İptal", URL: "https://meet.example/abc", Cancelled: true}, now))
	if !strings.Contains(c, "\r\nSTATUS:CANCELLED\r\n") || strings.Contains(c, "CONFIRMED") {
		t.Errorf("iptal: %s", c)
	}
	if !strings.Contains(c, "\r\nURL:https://meet.example/abc\r\n") {
		t.Errorf("URL: %s", c)
	}
	// Empty optional values are left out; a URL with a line break is dropped.
	c = string(Write(Event{UID: "x", Start: start, End: start.Add(time.Hour), URL: "https://a.example/\r\nX:y"}, now))
	for _, p := range []string{"SUMMARY", "DESCRIPTION", "LOCATION", "URL", "CREATED", "LAST-MODIFIED", "X:y"} {
		if strings.Contains(c, p) {
			t.Errorf("%s yazılmamalıydı: %s", p, c)
		}
	}
}
