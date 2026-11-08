// Package birthdate validates birth dates and encrypts them for storage
// (ADR-0012).
//
// The birth date is stored only in identity, encrypted in the application
// with AES-256-GCM (standard library, CLAUDE.md rule 4). The database, its
// backups and anyone who reads them without the key see only ciphertext.
// The user ID is bound as additional authenticated data, so a ciphertext
// copied to another row does not decrypt. Other services never receive the
// date; they get derived answers such as "under 18".
package birthdate

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // saat dilimi veritabanı binary'ye gömülür

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// MinimumAge is the youngest age that may register (ADR-0012).
const MinimumAge = 16

// adultAge is the age below which reports get priority (ADR-0012).
const adultAge = 18

var (
	// ErrInvalid is returned for dates that are malformed, before 1900 or in
	// the future.
	ErrInvalid = errors.New("geçersiz doğum tarihi")
	// ErrUnderAge is returned for users younger than MinimumAge.
	ErrUnderAge = errors.New("yaş sınırının altında")
	// errDecrypt hides why decryption failed.
	errDecrypt = errors.New("doğum tarihi çözülemedi")
)

// istanbul is the time zone ages are computed in (veri modeli v14). It is
// read-only after initialisation.
var istanbul = mustLoad("Europe/Istanbul")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err) // time/tzdata is embedded, so this cannot fail
	}
	return loc
}

// Date is a calendar date without time or zone.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// Parse reads a date in YYYY-MM-DD form and rejects impossible dates.
func Parse(s string) (Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil || t.Format(time.DateOnly) != s {
		return Date{}, ErrInvalid
	}
	return Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}, nil
}

func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// Age returns the age in whole years on the Istanbul calendar day of now.
// Someone born on 29 February turns a year older on 1 March in common years.
func (d Date) Age(now time.Time) int {
	today := now.In(istanbul)
	age := today.Year() - d.Year
	if today.Month() < d.Month || (today.Month() == d.Month && today.Day() < d.Day) {
		age--
	}
	return age
}

// IsMinor reports whether the person is under 18 on now.
func (d Date) IsMinor(now time.Time) bool { return d.Age(now) < adultAge }

// Validate checks that d is a plausible birth date for a user registering
// at now: on or after 1900-01-01, not in the future, and at least
// MinimumAge years ago.
func (d Date) Validate(now time.Time) error {
	today := now.In(istanbul)
	todayDate := Date{today.Year(), today.Month(), today.Day()}
	if d.Year < 1900 || d.after(todayDate) {
		return ErrInvalid
	}
	if d.Age(now) < MinimumAge {
		return ErrUnderAge
	}
	return nil
}

func (d Date) after(o Date) bool {
	if d.Year != o.Year {
		return d.Year > o.Year
	}
	if d.Month != o.Month {
		return d.Month > o.Month
	}
	return d.Day > o.Day
}

// Cipher encrypts birth dates. Keys are versioned; new values use the
// highest version and old values stay readable until re-encrypted.
type Cipher struct {
	active byte
	aeads  map[byte]cipher.AEAD
}

// ParseKeys builds a Cipher from "<version>:<base64 32-byte key>,..." where
// version is 1–255. The value comes from a secret (SOPS, ADR-0006).
func ParseKeys(s string) (*Cipher, error) {
	c := &Cipher{aeads: map[byte]cipher.AEAD{}}
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		v, b64, ok := strings.Cut(entry, ":")
		n, err := strconv.Atoi(v)
		if !ok || err != nil || n < 1 || n > 255 {
			return nil, errors.New("doğum tarihi anahtarı \"<1-255>:<base64>\" biçiminde olmalı")
		}
		ver := byte(n)
		if _, dup := c.aeads[ver]; dup {
			return nil, fmt.Errorf("doğum tarihi anahtar sürümü tekrar ediyor: %d", n)
		}
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(key) != 32 {
			// Do not echo the input: it is secret.
			return nil, fmt.Errorf("doğum tarihi anahtarı %d: 32 baytlık base64 olmalı", n)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("doğum tarihi anahtarı %d: %w", n, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("doğum tarihi anahtarı %d: %w", n, err)
		}
		c.aeads[ver] = aead
		c.active = max(c.active, ver)
	}
	if len(c.aeads) == 0 {
		return nil, errors.New("doğum tarihi anahtarı yok")
	}
	return c, nil
}

func aad(user id.ID) []byte {
	return append([]byte("tayfam/identity/users.birth_date/"), user[:]...)
}

// Encrypt returns version || nonce || ciphertext for user's birth date.
func (c *Cipher) Encrypt(user id.ID, d Date) []byte {
	aead := c.aeads[c.active]
	nonce := make([]byte, aead.NonceSize())
	_, _ = rand.Read(nonce) // crypto/rand.Read never returns an error
	out := make([]byte, 0, 1+len(nonce)+len(time.DateOnly)+aead.Overhead())
	out = append(out, c.active)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, []byte(d.String()), aad(user))
}

// Decrypt reverses Encrypt. It fails if the value was made for another user,
// with an unknown key, or was modified.
func (c *Cipher) Decrypt(user id.ID, data []byte) (Date, error) {
	if len(data) < 1 {
		return Date{}, errDecrypt
	}
	aead, ok := c.aeads[data[0]]
	if !ok || len(data) < 1+aead.NonceSize()+aead.Overhead() {
		return Date{}, errDecrypt
	}
	nonce := data[1 : 1+aead.NonceSize()]
	plain, err := aead.Open(nil, nonce, data[1+aead.NonceSize():], aad(user))
	if err != nil {
		return Date{}, errDecrypt
	}
	d, err := Parse(string(plain))
	if err != nil {
		return Date{}, errDecrypt
	}
	return d, nil
}

// NeedsReencrypt reports whether data was encrypted with an older key.
func (c *Cipher) NeedsReencrypt(data []byte) bool {
	return len(data) > 0 && data[0] != c.active
}
