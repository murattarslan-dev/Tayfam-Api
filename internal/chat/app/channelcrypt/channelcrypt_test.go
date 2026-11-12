package channelcrypt

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, KeySize)) }

func TestRoundTrip(t *testing.T) {
	c, err := ParseKeys("1:" + key(1))
	if err != nil {
		t.Fatal(err)
	}
	msg, ch := id.New(), id.New()
	body := "Cumartesi İzmir çıkışı saat 08:00'de 🚗"
	s := c.Encrypt(msg, ch, body)
	if s.Version != 1 {
		t.Fatalf("sürüm = %d", s.Version)
	}
	if bytes.Contains(s.Data, []byte("İzmir")) {
		t.Fatal("şifreli değer açık metni içeriyor")
	}
	got, err := c.Decrypt(msg, ch, s)
	if err != nil || got != body {
		t.Fatalf("Decrypt = %q, %v", got, err)
	}
	// Same body twice: different nonces, different ciphertexts.
	if bytes.Equal(c.Encrypt(msg, ch, body).Data, s.Data) {
		t.Fatal("nonce tekrar etti")
	}
}

func TestMaxBodyFitsDatabaseLimit(t *testing.T) {
	c := Ephemeral()
	// 4000 four-byte characters: the largest body the app accepts.
	s := c.Encrypt(id.New(), id.New(), strings.Repeat("🚗", 4000))
	if len(s.Data) > 16028 {
		t.Fatalf("uzunluk %d, channel_messages_body_len sınırını aşıyor", len(s.Data))
	}
}

func TestAADBindsMessageAndChannel(t *testing.T) {
	c := Ephemeral()
	msg, ch := id.New(), id.New()
	s := c.Encrypt(msg, ch, "gizli")
	if _, err := c.Decrypt(id.New(), ch, s); !errors.Is(err, ErrDecrypt) {
		t.Errorf("başka mesaj: %v", err)
	}
	if _, err := c.Decrypt(msg, id.New(), s); !errors.Is(err, ErrDecrypt) {
		t.Errorf("başka kanal: %v", err)
	}
	// Swapped IDs are a different AAD as well.
	if _, err := c.Decrypt(ch, msg, s); !errors.Is(err, ErrDecrypt) {
		t.Errorf("yer değiştirmiş kimlikler: %v", err)
	}
}

func TestTamperAndUnknownVersion(t *testing.T) {
	c := Ephemeral()
	msg, ch := id.New(), id.New()
	s := c.Encrypt(msg, ch, "gizli")
	bad := Sealed{Version: s.Version, Data: bytes.Clone(s.Data)}
	bad.Data[len(bad.Data)-1] ^= 1
	if _, err := c.Decrypt(msg, ch, bad); !errors.Is(err, ErrDecrypt) {
		t.Errorf("değiştirilmiş veri: %v", err)
	}
	if _, err := c.Decrypt(msg, ch, Sealed{Version: 2, Data: s.Data}); !errors.Is(err, ErrDecrypt) {
		t.Errorf("bilinmeyen sürüm: %v", err)
	}
	for _, short := range [][]byte{nil, {1}, s.Data[:27]} {
		if _, err := c.Decrypt(msg, ch, Sealed{Version: 1, Data: short}); !errors.Is(err, ErrDecrypt) {
			t.Errorf("kısa veri (%d): %v", len(short), err)
		}
	}
}

func TestRotation(t *testing.T) {
	old, err := ParseKeys("1:" + key(1))
	if err != nil {
		t.Fatal(err)
	}
	msg, ch := id.New(), id.New()
	s1 := old.Encrypt(msg, ch, "eski")

	rotated, err := ParseKeys(" 2:" + key(2) + " , 1:" + key(1))
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ActiveVersion() != 2 {
		t.Fatalf("etkin sürüm = %d", rotated.ActiveVersion())
	}
	if got, err := rotated.Decrypt(msg, ch, s1); err != nil || got != "eski" {
		t.Fatalf("eski sürüm okunamadı: %q, %v", got, err)
	}
	if !rotated.NeedsReencrypt(s1) {
		t.Error("eski sürüm yeniden şifreleme istemiyor")
	}
	s2 := rotated.Encrypt(msg, ch, "yeni")
	if s2.Version != 2 || rotated.NeedsReencrypt(s2) {
		t.Errorf("yeni kayıt sürümü = %d", s2.Version)
	}
	// The old ring cannot read the new key's bodies.
	if _, err := old.Decrypt(msg, ch, s2); !errors.Is(err, ErrDecrypt) {
		t.Errorf("eski halka yeni anahtarı okudu: %v", err)
	}
}

func TestParseKeysRejects(t *testing.T) {
	secret := key(7)
	for _, s := range []string{
		"", " , ", secret, "0:" + secret, "256:" + secret, "x:" + secret,
		"1:" + secret + ",1:" + key(8),
		"1:" + base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"1:bozuk!!",
	} {
		_, err := ParseKeys(s)
		if err == nil {
			t.Errorf("ParseKeys(%q) kabul edildi", s)
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "bozuk") {
			t.Errorf("hata girdiyi içeriyor: %v", err)
		}
	}
}

func TestLoadRequiresKeyInProd(t *testing.T) {
	if _, err := Load("prod", " "); !errors.Is(err, ErrKeyRequired) {
		t.Fatalf("prod anahtarsız: %v", err)
	}
	for _, env := range []string{"local", "dev"} {
		if c, err := Load(env, ""); c != nil || err != nil {
			t.Fatalf("%s anahtarsız: %v %v", env, c, err)
		}
	}
	if c, err := Load("prod", "1:"+key(1)); c == nil || err != nil {
		t.Fatalf("prod anahtarlı: %v", err)
	}
	if _, err := Load("dev", "bozuk"); err == nil {
		t.Fatal("bozuk anahtar kabul edildi")
	}
}
