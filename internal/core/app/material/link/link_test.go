package link

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]Link{
		"https://tr.wikipedia.org/wiki/T%C3%BCrev":     {"https://tr.wikipedia.org/wiki/T%C3%BCrev", "tr.wikipedia.org"},
		"  HTTPS://Www.Example-Okul.com.tr/ders?id=3 ": {"https://www.example-okul.com.tr/ders?id=3", "www.example-okul.com.tr"},
		"https://okul.org.:443/a#b":                    {"https://okul.org/a#b", "okul.org"},
		"https://xn--kitap-9ua.com/":                   {"https://xn--kitap-9ua.com/", "xn--kitap-9ua.com"},
		"https://tr.wikipedia.org/wiki/Türev":          {"https://tr.wikipedia.org/wiki/T%C3%BCrev", "tr.wikipedia.org"},
	} {
		got, ok := Parse(in)
		if !ok || got != want {
			t.Errorf("Parse(%q) = %+v %v, %+v bekleniyordu", in, got, ok, want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for name, in := range map[string]string{
		"boş":                      "  ",
		"http":                     "http://example.com/",
		"şemasız":                  "example.com/ders",
		"javascript":               "javascript:alert(1)",
		"data":                     "data:text/html,x",
		"opak":                     "https:example.com",
		"hostsuz":                  "https:///ders",
		"kullanıcı bilgisi":        "https://user@example.com/",
		"kullanıcı ve parola":      "https://user:pw@example.com/",
		"IPv4":                     "https://93.184.216.34/",
		"IPv4 kısa":                "https://127.1/",
		"IPv4 onluk":               "https://2130706433/",
		"IPv4 onaltılık":           "https://0x7f.0x1/",
		"IPv4 sonda nokta":         "https://10.0.0.1./",
		"IPv6":                     "https://[::1]/",
		"IPv6 eşlemeli":            "https://[::ffff:127.0.0.1]/",
		"tek etiket":               "https://intranet/",
		"localhost":                "https://localhost/",
		"localhost alt alanı":      "https://a.localhost/",
		".local":                   "https://yazici.local/",
		".internal":                "https://metadata.google.internal/",
		".onion":                   "https://abc.onion/",
		"başka port":               "https://example.com:8443/",
		"boşluk":                   "https://example.com/a b",
		"sekme":                    "https://example.com/a\tb",
		"satır sonu":               "https://example.com/\nx",
		"ters bölü":                "https://example.com\\@evil.com/",
		"ters bölü yolda":          "https://example.com/a\\b",
		"yön işareti":              "https://example.com/‮gpj.exe",
		"sıfır genişlik":           "https://exa​mple.com/",
		"ASCII olmayan alan adı":   "https://kitapçı.com/",
		"alt çizgi":                "https://a_b.example.com/",
		"tire ile başlayan etiket": "https://-a.example.com/",
		"boş etiket":               "https://a..example.com/",
		"uzun etiket":              "https://" + strings.Repeat("a", 64) + ".com/",
		"geçersiz UTF-8":           "https://example.com/\xff",
		"501 karakter":             "https://example.com/" + strings.Repeat("a", 481),
		"çok uzun ham metin":       "https://example.com/" + strings.Repeat("a", 4000),
		"yüzde kodlu host":         "https://ex%41mple.com/",
	} {
		if got, ok := Parse(in); ok {
			t.Errorf("%s: %q kabul edildi (%+v)", name, in, got)
		}
	}
	if _, ok := Parse("https://example.com/" + strings.Repeat("a", 480)); !ok {
		t.Error("500 karakter reddedildi")
	}
}

func TestDomain(t *testing.T) {
	if d, ok := Domain("Bit.LY."); !ok || d != "bit.ly" {
		t.Errorf("Domain = %q %v", d, ok)
	}
	for _, in := range []string{"", "com", "1.2.3.4", "a.1com", "a.b-", "a.-b"} {
		if _, ok := Domain(in); ok {
			t.Errorf("Domain(%q) kabul edildi", in)
		}
	}
}

// TestNoNetwork: the link check never touches the network — the server
// never opens a link (ADR-0040 §3). Nothing this package depends on, all
// the way down, can resolve a name or dial.
func TestNoNetwork(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go aracı yok")
	}
	out, err := exec.Command(goBin, "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	deps := strings.Fields(string(out))
	if !slices.Contains(deps, "net/url") {
		t.Fatalf("bağımlılıklar okunamadı: %v", deps)
	}
	for _, banned := range []string{"net", "net/http", "crypto/tls", "os/exec"} {
		if slices.Contains(deps, banned) {
			t.Errorf("bağlantı denetimi %s paketine bağımlı", banned)
		}
	}
}
