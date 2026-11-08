// Package id generates UUIDv7 identifiers (RFC 9562) on the server
// (CLAUDE.md). The first 48 bits are the Unix time in milliseconds, so IDs
// sort by creation time; the remaining 74 bits are random from crypto/rand.
package id

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// ID is a UUID.
type ID [16]byte

// New returns a new UUIDv7.
func New() ID { return newAt(time.Now()) }

func newAt(t time.Time) ID {
	var u ID
	_, _ = rand.Read(u[6:]) // crypto/rand.Read never returns an error
	ms := uint64(t.UnixMilli())
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms)
	copy(u[0:6], ts[2:8])
	u[6] = (u[6] & 0x0f) | 0x70 // version 7
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant
	return u
}

// String returns the canonical 36-character form.
func (u ID) String() string {
	var b [36]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:], u[10:])
	return string(b[:])
}

// PG converts u for pgx.
func (u ID) PG() pgtype.UUID { return pgtype.UUID{Bytes: u, Valid: true} }

// FromPG converts a pgx UUID; ok is false for NULL.
func FromPG(p pgtype.UUID) (ID, bool) { return ID(p.Bytes), p.Valid }

// Parse reads the canonical 36-character form.
func Parse(s string) (ID, bool) {
	var u ID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, false
	}
	hexs := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:]
	if _, err := hex.Decode(u[:], []byte(hexs)); err != nil {
		return ID{}, false
	}
	return u, true
}
