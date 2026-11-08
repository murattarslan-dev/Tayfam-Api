package birthdate

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

// noon in Istanbul on the given day.
func on(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, istanbul) }

func TestParse(t *testing.T) {
	for _, s := range []string{"", "2000-1-01", "2000-02-30", "2001-02-29", "20000101", "2000-01-01T00:00:00Z", " 2000-01-01"} {
		if _, err := Parse(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) kabul edildi", s)
		}
	}
	d, err := Parse("2004-02-29")
	if err != nil || d != (Date{2004, time.February, 29}) {
		t.Fatalf("Parse = %v, %v", d, err)
	}
}

func TestAgeAndValidate(t *testing.T) {
	now := on(2026, time.November, 8)
	tests := []struct {
		date  string
		age   int
		err   error
		minor bool
	}{
		{"2010-11-08", 16, nil, true},         // bugün 16 oldu
		{"2010-11-09", 15, ErrUnderAge, true}, // yarın 16 olacak
		{"2008-11-08", 18, nil, false},        // bugün 18 oldu
		{"2008-11-09", 17, nil, true},
		{"1900-01-01", 126, nil, false},
		{"1899-12-31", 126, ErrInvalid, false},
		{"2026-11-09", -1, ErrInvalid, true}, // gelecekte
	}
	for _, tt := range tests {
		d, _ := Parse(tt.date)
		if got := d.Age(now); got != tt.age {
			t.Errorf("%s: yaş = %d, %d bekleniyordu", tt.date, got, tt.age)
		}
		if err := d.Validate(now); !errors.Is(err, tt.err) {
			t.Errorf("%s: Validate = %v, %v bekleniyordu", tt.date, err, tt.err)
		}
		if got := d.IsMinor(now); got != tt.minor {
			t.Errorf("%s: IsMinor = %v", tt.date, got)
		}
	}
}

func TestAgeUsesIstanbulCalendar(t *testing.T) {
	d, _ := Parse("2010-11-08")
	// 7 Kasım 21:30 UTC = 8 Kasım 00:30 İstanbul: doğum günü başladı.
	if got := d.Age(time.Date(2026, 11, 7, 21, 30, 0, 0, time.UTC)); got != 16 {
		t.Fatalf("İstanbul gece yarısından sonra yaş = %d", got)
	}
	if got := d.Age(time.Date(2026, 11, 7, 20, 30, 0, 0, time.UTC)); got != 15 {
		t.Fatalf("İstanbul gece yarısından önce yaş = %d", got)
	}
}

func TestLeapDayBirthday(t *testing.T) {
	d, _ := Parse("2008-02-29")
	if got := d.Age(on(2026, time.February, 28)); got != 17 {
		t.Fatalf("28 Şubat'ta yaş = %d", got)
	}
	if got := d.Age(on(2026, time.March, 1)); got != 18 {
		t.Fatalf("1 Mart'ta yaş = %d", got)
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	c, err := ParseKeys("1:" + key(1))
	if err != nil {
		t.Fatal(err)
	}
	u := id.New()
	d := Date{1990, time.May, 19}
	ct := c.Encrypt(u, d)
	if bytes.Contains(ct, []byte("1990")) {
		t.Fatal("şifreli değer açık metni içeriyor")
	}
	if len(ct) < 30 || len(ct) > 128 {
		t.Fatalf("uzunluk %d veritabanı kısıtı dışında", len(ct))
	}
	got, err := c.Decrypt(u, ct)
	if err != nil || got != d {
		t.Fatalf("Decrypt = %v, %v", got, err)
	}
	if bytes.Equal(ct, c.Encrypt(u, d)) {
		t.Fatal("aynı tarih aynı şifreli değeri üretti (nonce tekrarı)")
	}
}

func TestDecryptRejects(t *testing.T) {
	c, _ := ParseKeys("1:" + key(1))
	other, _ := ParseKeys("1:" + key(2))
	u := id.New()
	ct := c.Encrypt(u, Date{1990, time.May, 19})
	tampered := bytes.Clone(ct)
	tampered[len(tampered)-1] ^= 1
	unknownVer := bytes.Clone(ct)
	unknownVer[0] = 9

	for name, tc := range map[string]struct {
		c    *Cipher
		user id.ID
		data []byte
	}{
		"başka kullanıcının satırı": {c, id.New(), ct},
		"başka anahtar":             {other, u, ct},
		"değiştirilmiş":             {c, u, tampered},
		"bilinmeyen sürüm":          {c, u, unknownVer},
		"kısa":                      {c, u, ct[:10]},
		"boş":                       {c, u, nil},
	} {
		if _, err := tc.c.Decrypt(tc.user, tc.data); !errors.Is(err, errDecrypt) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestKeyRotation(t *testing.T) {
	old, _ := ParseKeys("1:" + key(1))
	u := id.New()
	ct := old.Encrypt(u, Date{2000, time.January, 2})

	rotated, err := ParseKeys("1:" + key(1) + ",2:" + key(2))
	if err != nil {
		t.Fatal(err)
	}
	if d, err := rotated.Decrypt(u, ct); err != nil || d.Year != 2000 {
		t.Fatalf("eski anahtarla şifreli değer okunamadı: %v", err)
	}
	if !rotated.NeedsReencrypt(ct) {
		t.Fatal("eski sürüm yeniden şifreleme gerektirmeli")
	}
	if fresh := rotated.Encrypt(u, Date{2000, time.January, 2}); fresh[0] != 2 || rotated.NeedsReencrypt(fresh) {
		t.Fatal("yeni değer en yüksek sürümle şifrelenmeli")
	}
}

func TestParseKeysRejects(t *testing.T) {
	// A 16-byte key (too short), built at runtime so it never looks like a
	// committed credential.
	secret := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
	for name, s := range map[string]string{
		"boş":          "",
		"sürüm yok":    key(1),
		"sürüm 0":      "0:" + key(1),
		"sürüm 256":    "256:" + key(1),
		"kısa anahtar": "1:" + secret,
		"tekrar":       "1:" + key(1) + ",1:" + key(2),
	} {
		_, err := ParseKeys(s)
		if err == nil {
			t.Errorf("%s: kabul edildi", name)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: hata gizli değeri içeriyor", name)
		}
	}
}
