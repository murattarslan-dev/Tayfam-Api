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

	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Event is one core event: exactly one of Notification, Purged,
// MemberChanged, StatusChanged, Attendance and Schedule.
type Event struct {
	ID    id.ID // core's outbox event ID
	Tayfa id.ID // zero only for a notification not about a tayfa
	// OccurredAt is when core wrote the event.
	OccurredAt time.Time
	// Notification is a notification.requested; its Event, Tayfa and Actor
	// come from the event.
	Notification *notification.Request
	Purged       bool
	// MemberChanged is the member of a membership.changed; MemberEnded is
	// set when they are no longer a member (left, removed, banned).
	MemberChanged id.ID
	MemberEnded   bool
	// StatusChanged: the tayfa's read-only state may have changed.
	StatusChanged bool
	// Attendance: a member's answer to an event changed (issue #183).
	Attendance *Attendance
	// Schedule: an event's end moved or it was cancelled (issue #183).
	Schedule *Schedule
}

// Attendance is an event_attendance_changed: whether User is in Event's
// channel may have changed. The answer itself is not carried.
type Attendance struct {
	Event, User id.ID
}

// Schedule is an event_schedule_changed.
type Schedule struct {
	Event id.ID
	// End is the event's end, or the moment it was cancelled.
	End time.Time
}

// AccessCache is the channel permission cache that membership and tayfa
// state changes clear (issue #81). chat keeps no copy of memberships.
type AccessCache interface {
	Forget(tayfa, user id.ID)
	ForgetTayfa(tayfa id.ID)
}

// Live is the real-time layer (realtime.Live, issue #82): after the cache
// is cleared it checks the affected subscriptions again with each
// connection's own token, and tells connected recipients about new
// notifications. Methods must not block.
type Live interface {
	MembershipChanged(tayfa, user id.ID)
	TayfaChanged(tayfa id.ID)
	TayfaPurged(tayfa id.ID)
	Notified(r notification.Request)
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	// Access is cleared by membership and tayfa state changes; optional.
	Access AccessCache
	// Live hears about changes for connected clients; optional.
	Live Live
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
	for _, set := range []bool{e.Notification != nil, e.Purged, e.MemberChanged != (id.ID{}), e.StatusChanged, e.Attendance != nil, e.Schedule != nil} {
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
	switch {
	case e.Attendance != nil:
		return s.attendance(e)
	case e.Schedule != nil:
		return s.schedule(ctx, e)
	case e.MemberEnded:
		// Out of the tayfa's selected-member channels first, then the
		// cache and the live rechecks below. Deleting again changes
		// nothing.
		if err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
			return channel.LeaveTayfa(ctx, store.New(tx), e.Tayfa, e.MemberChanged)
		}); err != nil {
			return err
		}
	}
	// Cache clearing is idempotent and needs no transaction. The live
	// rechecks come after it, so they ask core afresh.
	if e.MemberChanged != (id.ID{}) || e.StatusChanged || e.Purged {
		if s.cfg.Access != nil {
			if e.MemberChanged != (id.ID{}) {
				s.cfg.Access.Forget(e.Tayfa, e.MemberChanged)
			} else {
				s.cfg.Access.ForgetTayfa(e.Tayfa)
			}
		}
		if s.cfg.Live != nil {
			switch {
			case e.MemberChanged != (id.ID{}):
				s.cfg.Live.MembershipChanged(e.Tayfa, e.MemberChanged)
			case e.StatusChanged:
				s.cfg.Live.TayfaChanged(e.Tayfa)
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
		// Direct message notices are chat's own; core never asks for them.
		if notification.IsDirect(r.Type) {
			return apperr.Validation("notification_requested.type")
		}
		if err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
			return notification.Store(ctx, store.New(tx), r)
		}); err != nil {
			return err
		}
		if s.cfg.Live != nil {
			s.cfg.Live.Notified(r)
		}
		return nil
	}
	if err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := notification.PurgeTayfa(ctx, q, e.Tayfa); err != nil {
			return err
		}
		return channel.PurgeTayfa(ctx, q, e.Tayfa)
	}); err != nil {
		return err
	}
	if s.cfg.Live != nil {
		s.cfg.Live.TayfaPurged(e.Tayfa)
	}
	return nil
}

// attendance: the user's channel permissions are asked afresh and their
// subscriptions in the tayfa checked again (an event channel they left is
// cut). Nothing is stored.
func (s *Service) attendance(e Event) error {
	a := e.Attendance
	if a.Event == (id.ID{}) || a.User == (id.ID{}) {
		return apperr.Validation("event_attendance_changed")
	}
	if s.cfg.Access != nil {
		s.cfg.Access.Forget(e.Tayfa, a.User)
	}
	if s.cfg.Live != nil {
		s.cfg.Live.MembershipChanged(e.Tayfa, a.User)
	}
	return nil
}

// schedule moves the planned archive time of the event's open channel,
// unless chat already has newer information (events may arrive out of
// order).
func (s *Service) schedule(ctx context.Context, e Event) error {
	sc := e.Schedule
	if sc.Event == (id.ID{}) || sc.End.IsZero() {
		return apperr.Validation("event_schedule_changed")
	}
	if e.OccurredAt.IsZero() {
		return apperr.Validation("occurred_at")
	}
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		return channel.EventEnded(ctx, store.New(tx), e.Tayfa, sc.Event, sc.End.UTC(), e.OccurredAt.UTC())
	})
}
