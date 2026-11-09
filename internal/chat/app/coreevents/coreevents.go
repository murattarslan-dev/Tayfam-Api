// Package coreevents applies core's outbox events to chat (ADR-0009, issue
// #64): notification.requested becomes in-app notifications, tayfa.purged
// removes what chat keeps about the tayfa (notifications, preferences,
// channels with their messages and read markers). membership.changed and
// the tayfa state changes only clear the channel permission cache (issue
// #81). chat keeps no copy of core's memberships; core names the
// recipients and decides permissions.
//
// Events arrive at least once and may arrive out of order; applying one
// again leaves the same result (ADR-0002).
package coreevents

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Event is one core event: exactly one of Notification, Purged,
// MemberChanged and StatusChanged.
type Event struct {
	ID    id.ID // core's outbox event ID
	Tayfa id.ID // zero only for a notification not about a tayfa
	// Notification is a notification.requested; its Event, Tayfa and Actor
	// come from the event.
	Notification *notification.Request
	Purged       bool
	// MemberChanged is the member of a membership.changed.
	MemberChanged id.ID
	// StatusChanged: the tayfa's read-only state may have changed.
	StatusChanged bool
}

// AccessCache is the channel permission cache that membership and tayfa
// state changes clear (issue #81). chat keeps no copy of memberships.
type AccessCache interface {
	Forget(tayfa, user id.ID)
	ForgetTayfa(tayfa id.ID)
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	// Access is cleared by membership and tayfa state changes; optional.
	Access AccessCache
}

// Service applies core events.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("coreevents: eksik yapılandırma")
	}
	return &Service{cfg: cfg}, nil
}

// Apply applies e in one transaction.
func (s *Service) Apply(ctx context.Context, e Event) error {
	kinds := 0
	for _, set := range []bool{e.Notification != nil, e.Purged, e.MemberChanged != (id.ID{}), e.StatusChanged} {
		if set {
			kinds++
		}
	}
	switch {
	case e.ID == (id.ID{}):
		return apperr.Validation("event_id")
	case kinds != 1:
		return apperr.Validation("event")
	case e.Notification == nil && e.Tayfa == (id.ID{}):
		// A notification checks its tayfa by type (Request.Validate).
		return apperr.Validation("tayfa_id")
	}
	// Cache clearing is idempotent and needs no transaction.
	if e.MemberChanged != (id.ID{}) || e.StatusChanged || e.Purged {
		if s.cfg.Access != nil {
			if e.MemberChanged != (id.ID{}) {
				s.cfg.Access.Forget(e.Tayfa, e.MemberChanged)
			} else {
				s.cfg.Access.ForgetTayfa(e.Tayfa)
			}
		}
		if !e.Purged {
			return nil
		}
	}
	if e.Notification != nil {
		r := *e.Notification
		r.Event, r.Tayfa = e.ID, e.Tayfa
		if err := r.Validate(); err != nil {
			return err
		}
		return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
			return notification.Store(ctx, store.New(tx), r)
		})
	}
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := notification.PurgeTayfa(ctx, q, e.Tayfa); err != nil {
			return err
		}
		return channel.PurgeTayfa(ctx, q, e.Tayfa)
	})
}
