// Package joinqr joins people to an event, and its tayfa, by the QR the
// organizer shows (Şimdi başlat; issue #264, ADR-0038 §3; Notion "Faz 3 —
// Katalog ayrıntıları ve şema ekleri (2026-11-18)" §D).
//
// The organizer (or an admin of the tayfa) asks for a short-lived token
// (event.Service.CreateJoinQR); the QR carries only the token
// (https://tayfam.app/q#<token>, in the fragment), and the scanning user
// sends it in the request body. Physically standing next to the organizer
// stands in for approval only in an instant tayfa; a standard tayfa keeps
// its join policy, so a QR never opens a closed tayfa.
//
// Every refusal (unknown or expired token, an event or tayfa that cannot
// be joined, a caller banned in the tayfa, blocked with the organizer
// either way, deleted or banned on the platform) is the same
// apperr.ErrInvalidJoinQR (ADR-0011). The token is never logged, audited,
// put in an event or an error; the database keeps only its hash.
package joinqr

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Limiter limits join attempts per user (ratelimit.Limiter): 5 a minute
// (ADR-0038 §3).
type Limiter interface {
	Allow(key string) bool
}

// Config wires a Service.
type Config struct {
	Pool        *pgxpool.Pool
	Events      *event.Service
	Memberships *membership.Service
	// JoinLimiter limits Join attempts per user against token guessing.
	JoinLimiter Limiter
	Now         func() time.Time
}

// Service creates and redeems join QR tokens.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Events == nil || cfg.Memberships == nil || cfg.JoinLimiter == nil {
		return nil, errors.New("joinqr: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}, nil
}

// Create returns a new join token for the event (event.Service.CreateJoinQR).
func (s *Service) Create(ctx context.Context, user, tayfa, ev id.ID) (event.JoinQR, error) {
	return s.cfg.Events.CreateJoinQR(ctx, user, tayfa, ev)
}

// Result is the outcome of a join.
type Result struct {
	TayfaID id.ID
	EventID id.ID
	// Pending: a join request was left in an approval tayfa (or was
	// already waiting); RSVP and Attendance are empty.
	Pending    bool
	RSVP       string
	Attendance event.Attendance
}

// Join redeems a join token for user (ADR-0038 §3): in an instant tayfa
// user becomes an active member and answers going; in a standard tayfa the
// join policy applies (open: member and going, approval: a join request);
// an active member answers going and is checked in. At most
// event.MaxQRJoins joins count per event; a repeat that changes nothing
// does not count.
func (s *Service) Join(ctx context.Context, user id.ID, token string) (Result, error) {
	if token == "" {
		return Result{}, apperr.Validation("token")
	}
	if !s.cfg.JoinLimiter.Allow(user.String()) {
		return Result{}, apperr.ErrRateLimited
	}
	hash, ok := event.HashJoinQRToken(token)
	if !ok {
		return Result{}, apperr.ErrInvalidJoinQR
	}
	var out Result
	err := txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		k, err := q.GetJoinQRToken(ctx, hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrInvalidJoinQR
		}
		if err != nil {
			return fmt.Errorf("QR token: %w", err)
		}
		if !k.ExpiresAt.Time.After(s.cfg.Now()) {
			return apperr.ErrInvalidJoinQR
		}
		tayfa, eventID := id.ID(k.TayfaID.Bytes), id.ID(k.EventID.Bytes)
		if err := s.mayJoin(ctx, q, user, k); err != nil {
			return err
		}
		if err := s.cfg.Events.CheckJoinQRTx(ctx, q, tayfa, eventID); err != nil {
			return err
		}
		n, err := q.GetJoinQRCount(ctx, k.EventID)
		if err != nil {
			return fmt.Errorf("QR katılım sayısı: %w", err)
		}
		if n >= event.MaxQRJoins {
			return apperr.ErrJoinQRLimit
		}
		// The memberships trigger checks the token again for an active
		// join of an instant tayfa.
		if err := q.SetJoinQRToken(ctx, id.ID(k.ID.Bytes).String()); err != nil {
			return fmt.Errorf("QR token: %w", err)
		}
		m, existing, err := s.cfg.Memberships.JoinByQRTx(ctx, q, tayfa, user)
		if err != nil {
			return err
		}
		out = Result{TayfaID: tayfa, EventID: eventID}
		changed := !existing
		if m.Status == authz.StatusPending {
			out.Pending = true
		} else {
			rsvp, att, attChanged, err := s.cfg.Events.AttendByQRTx(ctx, q, user, tayfa, eventID, existing)
			if err != nil {
				return err
			}
			out.RSVP, out.Attendance = rsvp, att
			changed = changed || attChanged
		}
		if !changed {
			return nil
		}
		added, err := q.AddJoinQRCount(ctx, store.AddJoinQRCountParams{EventID: k.EventID, MaxJoins: event.MaxQRJoins})
		if err != nil {
			return fmt.Errorf("QR katılım sayısı: %w", err)
		}
		if added != 1 {
			return apperr.ErrJoinQRLimit
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return out, nil
}

// mayJoin refuses a caller who may not join by this token: the token's own
// creator, a deleted or platform banned account, and anyone blocked with
// the organizer or the token's creator either way.
func (s *Service) mayJoin(ctx context.Context, q *store.Queries, user id.ID, k store.GetJoinQRTokenRow) error {
	creator := id.ID(k.CreatedBy.Bytes)
	if creator == user {
		return apperr.ErrInvalidJoinQR
	}
	gone, err := q.JoinQRUserGone(ctx, user.PG())
	if err != nil {
		return fmt.Errorf("hesap: %w", err)
	}
	if gone {
		return apperr.ErrInvalidJoinQR
	}
	others := []id.ID{creator}
	if k.OrganizerID.Valid {
		if org := id.ID(k.OrganizerID.Bytes); org != creator {
			others = append(others, org)
		}
	}
	for _, o := range others {
		blocked, err := social.IsBlocked(ctx, q, user, o)
		if err != nil {
			return err
		}
		if blocked {
			return apperr.ErrInvalidJoinQR
		}
	}
	return nil
}
