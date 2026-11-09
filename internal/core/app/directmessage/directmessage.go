// Package directmessage answers chat's question "may the caller write to
// this user, and how?" (DirectMessageService,
// proto/tayfam/core/v1/direct_message.proto; issue #95). Conversations and
// messages live in chat; the rule lives here, in one place, because the
// social graph, memberships and attendance are core's (ADR-0009). chat asks
// with the caller's own token for every message it stores, from REST and
// WebSocket alike.
//
// The rule (data model v14 "Mesaj İsteği Modeli (v7)", "Arkadaşlık"), in
// order:
//
//  1. a block in either direction (social.IsBlocked): Denied;
//  2. accepted friends (social.Friends): Active, no shared tayfa needed;
//  3. no live tayfa in which both are active members: Denied;
//  4. shared event history, both attended an ended, not cancelled event:
//     Active;
//  5. otherwise: Request.
//
// An unknown or deleted recipient is Denied, exactly like a block: the
// answer is only the decision and never says which rule gave it.
//
// With Request the answer also carries the caller's daily request limit:
// RequestsPerDayNew when the caller's oldest active membership in a tayfa
// shared with the recipient is younger than NewMemberPeriod (someone who
// just joined an open tayfa to reach its members), else RequestsPerDay.
// chat counts the requests in its own data.
package directmessage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Decision is the answer to CheckDirectMessage.
type Decision int

// Decisions (direct_message.proto DirectMessageDecision).
const (
	Denied Decision = iota + 1
	Request
	Active
)

func (d Decision) String() string {
	switch d {
	case Denied:
		return "denied"
	case Request:
		return "request"
	case Active:
		return "active"
	}
	return "unknown"
}

// Daily request limits (v14 "Mesaj İsteği Modeli (v7)").
const (
	RequestsPerDayNew = 5
	RequestsPerDay    = 20
	NewMemberPeriod   = 7 * 24 * time.Hour
)

// Answer is the decision and, with Request, the caller's daily limit.
type Answer struct {
	Decision     Decision
	RequestLimit int // only with Request
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Now  func() time.Time // optional, for tests
}

// Service implements DirectMessageService.
type Service struct {
	q   *store.Queries
	now func() time.Time
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("directmessage: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{q: store.New(cfg.Pool), now: cfg.Now}, nil
}

// Check decides whether caller may write to recipient.
func (s *Service) Check(ctx context.Context, caller, recipient id.ID) (Answer, error) {
	if recipient == (id.ID{}) || recipient == caller {
		return Answer{}, apperr.Validation("recipient_id")
	}
	denied := Answer{Decision: Denied}
	_, err := s.q.GetLiveUserProfile(ctx, recipient.PG())
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return denied, nil
	case err != nil:
		return Answer{}, fmt.Errorf("alıcı profili: %w", err)
	}
	blocked, err := social.IsBlocked(ctx, s.q, caller, recipient)
	if err != nil {
		return Answer{}, err
	}
	if blocked {
		return denied, nil
	}
	friends, err := social.Friends(ctx, s.q, caller, recipient)
	if err != nil {
		return Answer{}, err
	}
	if friends {
		return Answer{Decision: Active}, nil
	}
	joined, err := s.q.SharedTayfaSenderJoinedAt(ctx, store.SharedTayfaSenderJoinedAtParams{SenderID: caller.PG(), RecipientID: recipient.PG()})
	if err != nil {
		return Answer{}, fmt.Errorf("ortak tayfa: %w", err)
	}
	if !joined.Valid {
		return denied, nil
	}
	now := s.now().UTC()
	history, err := s.q.HasSharedEventHistory(ctx, store.HasSharedEventHistoryParams{
		UserA: caller.PG(), UserB: recipient.PG(), Now: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return Answer{}, fmt.Errorf("ortak etkinlik geçmişi: %w", err)
	}
	if history {
		return Answer{Decision: Active}, nil
	}
	limit := RequestsPerDay
	if now.Sub(joined.Time) < NewMemberPeriod {
		limit = RequestsPerDayNew
	}
	return Answer{Decision: Request, RequestLimit: limit}, nil
}
