// Package coreevents applies core's outbox events to chat (ADR-0009, issue
// #64): notification.requested becomes in-app notifications, tayfa.purged
// removes what chat keeps about the tayfa. chat keeps no copy of core's
// memberships; core names the recipients.
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
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Event is one core event: exactly one of Notification and Purged.
type Event struct {
	ID    id.ID // core's outbox event ID
	Tayfa id.ID // zero only for a notification not about a tayfa
	// Notification is a notification.requested; its Event, Tayfa and Actor
	// come from the event.
	Notification *notification.Request
	Purged       bool
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
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
	switch {
	case e.ID == (id.ID{}):
		return apperr.Validation("event_id")
	case e.Purged && e.Tayfa == (id.ID{}):
		// A notification checks its tayfa by type (Request.Validate).
		return apperr.Validation("tayfa_id")
	case (e.Notification == nil) == !e.Purged:
		return apperr.Validation("event")
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
		return notification.PurgeTayfa(ctx, store.New(tx), e.Tayfa)
	})
}
