package keys

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func seed(label string) string {
	s := make([]byte, ed25519.SeedSize)
	copy(s, label)
	return base64.StdEncoding.EncodeToString(s)
}

func pub(label string) string {
	s := make([]byte, ed25519.SeedSize)
	copy(s, label)
	p, _ := ed25519.NewKeyFromSeed(s).Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(p)
}

func TestParse(t *testing.T) {
	r, err := Parse("2026-11:"+seed("yeni"), " 2026-05:"+pub("eski")+", ")
	if err != nil {
		t.Fatal(err)
	}
	keys := r.PublicKeys()
	if len(keys) != 2 || keys[0].ID != "2026-11" || keys[1].ID != "2026-05" {
		t.Fatalf("anahtarlar = %+v", keys)
	}
	if r.Active().ID != "2026-11" {
		t.Fatalf("aktif = %s", r.Active().ID)
	}
	if len(r.Static()) != 2 {
		t.Fatal("Static eksik")
	}
}

func TestParseRejects(t *testing.T) {
	for name, tc := range map[string][2]string{
		"ayraç yok":               {seed("a"), ""},
		"geçersiz kid":            {"Büyük:" + seed("a"), ""},
		"kısa tohum":              {"k1:" + base64.StdEncoding.EncodeToString([]byte("kısa")), ""},
		"emekli biçimi":           {"k1:" + seed("a"), "k0"},
		"emekli kısa anahtar":     {"k1:" + seed("a"), "k0:" + base64.StdEncoding.EncodeToString([]byte("kısa"))},
		"aktif ile aynı kid":      {"k1:" + seed("a"), "k1:" + pub("b")},
		"emekliler tekrar ediyor": {"k1:" + seed("a"), "k0:" + pub("b") + ",k0:" + pub("c")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(tc[0], tc[1]); err == nil {
				t.Fatal("hata bekleniyordu")
			}
		})
	}
}

func TestParseErrorDoesNotEchoSecret(t *testing.T) {
	secret := "k1:" + strings.Repeat("Z", 20) + "!!"
	_, err := Parse(secret, "")
	if err == nil || strings.Contains(err.Error(), "ZZZZ") {
		t.Fatalf("hata gizli değeri içeriyor: %v", err)
	}
}
