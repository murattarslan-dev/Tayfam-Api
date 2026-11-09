// Package minor answers whether users are under 18 (MinorService,
// proto/tayfam/identity/v1/minor.proto; ADR-0012 "Şikâyetlerde yaş
// önceliği", issue #76).
//
// The birth date is decrypted here, in memory, only to compute the answer;
// the answer is a yes/no per user and nothing else leaves identity. Neither
// the date nor the age is logged or returned. A user whose birth date cannot
// be read (no completed registration, a deleted account, a value that does
// not decrypt) is left out of the answer and the caller treats them as a
// minor (the safe side).
package minor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// MaxUsers bounds one question; a report needs two answers.
const MaxUsers = 10

// ErrTooMany: more than MaxUsers IDs, or none.
var ErrTooMany = errors.New("kullanıcı sayısı 1–10 olmalı")

// Config wires a Service.
type Config struct {
	Pool   *pgxpool.Pool
	Cipher *birthdate.Cipher
	Now    func() time.Time // optional, for tests
}

// Service answers IsMinor.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Cipher == nil {
		return nil, errors.New("minor: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// IsMinor returns, for each user it can answer for, whether they are under
// 18 now. Users it cannot answer for are missing from the map.
func (s *Service) IsMinor(ctx context.Context, users []id.ID) (map[id.ID]bool, error) {
	if len(users) == 0 || len(users) > MaxUsers {
		return nil, ErrTooMany
	}
	in := make([]pgtype.UUID, len(users))
	for i, u := range users {
		in[i] = u.PG()
	}
	rows, err := s.q.ListBirthDatesForMinorCheck(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("doğum tarihleri: %w", err)
	}
	now := s.cfg.Now()
	out := make(map[id.ID]bool, len(rows))
	for _, r := range rows {
		u := id.ID(r.ID.Bytes)
		d, err := s.cfg.Cipher.Decrypt(u, r.BirthDateEnc)
		if err != nil {
			// Unreadable: no answer, so the caller takes the safe side.
			// Nothing about the value is logged.
			continue
		}
		out[u] = d.IsMinor(now)
	}
	return out, nil
}
