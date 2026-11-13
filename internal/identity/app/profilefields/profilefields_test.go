package profilefields

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func p[T any](v T) *T { return &v }

func TestApplyCleans(t *testing.T) {
	got, err := Apply(Values{}, Update{
		Interests:  p([]string{"hike", "convoy"}),
		About:      p("  Doğa   yürüyüşü\r\n\n  ve kamp  "),
		Occupation: p("  Yazılım   geliştirici "),
		Team:       p("Bursa\nspor"),
		Languages:  p([]string{"TR", " en "}),
		SocialAccounts: p([]SocialAccount{
			{Platform: "x", Username: "@ece_su"}, {Platform: "instagram", Username: "ece.su"},
		}),
	})
	want := Values{
		Interests: []string{"convoy", "hike"}, About: "Doğa yürüyüşü\n\nve kamp", Occupation: "Yazılım geliştirici",
		Team: "Bursa spor", Languages: []string{"tr", "en"},
		SocialAccounts: []SocialAccount{{"instagram", "ece.su"}, {"x", "ece_su"}}, Declined: []string{},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Apply = %#v %v", got, err)
	}
	if !got.Complete() {
		t.Fatal("bütün alanlar dolu ama tamamlanmadı")
	}
}

func TestApplyRejects(t *testing.T) {
	for name, u := range map[string]Update{
		"bilinmeyen ilgi":        {Interests: p([]string{"chess"})},
		"tekrar eden ilgi":       {Interests: p([]string{"hike", "hike"})},
		"161 karakter hakkımda":  {About: p(strings.Repeat("ş", 161))},
		"61 karakter meslek":     {Occupation: p(strings.Repeat("a", 61))},
		"kontrol karakteri":      {About: p("a\u0007b")},
		"6 dil":                  {Languages: p([]string{"tr", "en", "de", "fr", "es", "it"})},
		"bilinmeyen dil":         {Languages: p([]string{"xx"})},
		"üç harfli dil":          {Languages: p([]string{"tur"})},
		"tekrar eden dil":        {Languages: p([]string{"tr", "TR"})},
		"bilinmeyen platform":    {SocialAccounts: p([]SocialAccount{{"facebook", "ece"}})},
		"aynı platform iki kez":  {SocialAccounts: p([]SocialAccount{{"x", "a"}, {"x", "b"}})},
		"URL":                    {SocialAccounts: p([]SocialAccount{{"instagram", "https://evil.example/ece"}})},
		"alan adı":               {SocialAccounts: p([]SocialAccount{{"instagram", "instagram.com/ece"}})},
		"x için uzun":            {SocialAccounts: p([]SocialAccount{{"x", strings.Repeat("a", 16)}})},
		"boş kullanıcı adı":      {SocialAccounts: p([]SocialAccount{{"x", "@"}})},
		"bilinmeyen ret":         {Declined: p([]string{"birth_date"})},
		"tekrar eden ret":        {Declined: p([]string{"team", "team"})},
		"aynı istekte değer+ret": {Team: p("Bursaspor"), Declined: p([]string{"team"})},
	} {
		if _, err := Apply(Values{}, u); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestDecline: "Paylaşmak istemiyorum" answers the field and clears its
// value; a value given later takes it off the declined set.
func TestDecline(t *testing.T) {
	v, err := Apply(Values{Team: "Bursaspor"}, Update{Declined: p([]string{"team", "social_accounts"})})
	if err != nil || v.Team != "" || !v.Answered(FieldTeam) || !v.Answered(FieldSocialAccounts) || v.Answered(FieldAbout) {
		t.Fatalf("ret = %#v %v", v, err)
	}
	v, err = Apply(v, Update{Team: p("Amedspor")})
	if err != nil || v.Team != "Amedspor" || !reflect.DeepEqual(v.Declined, []string{"social_accounts"}) {
		t.Fatalf("sonradan değer = %#v %v", v, err)
	}
	// Clearing a value leaves the field unanswered.
	v, err = Apply(v, Update{Team: p("  ")})
	if err != nil || v.Answered(FieldTeam) {
		t.Fatalf("temizleme = %#v %v", v, err)
	}
}

func TestPending(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	all := Values{Declined: append([]string(nil), AllFields...)}
	some := Values{Team: "Bursaspor"}
	for name, tc := range map[string]struct {
		v    Values
		s    Snooze
		want bool
	}{
		"hepsi reddedildi":              {all, Snooze{}, false},
		"eksik, ertelenmedi":            {some, Snooze{}, true},
		"eksik, erteleme sürüyor":       {some, Snooze{Until: now.Add(time.Hour), FieldsVersion: FieldsVersion}, false},
		"eksik, erteleme bitti":         {some, Snooze{Until: now, FieldsVersion: FieldsVersion}, true},
		"eksik, eski sürümde ertelendi": {some, Snooze{Until: now.Add(SnoozeFor), FieldsVersion: FieldsVersion - 1}, true},
	} {
		if got := Pending(tc.v, tc.s, now); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestLanguagesSorted(t *testing.T) {
	for i := 1; i < len(languages); i++ {
		if languages[i-1] >= languages[i] {
			t.Fatalf("sırasız: %s %s", languages[i-1], languages[i])
		}
	}
}
