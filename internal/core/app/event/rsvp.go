package event

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// promoteAll bounds one promotion round of an event without a capacity
// limit; capacity is at most capacityMax, so it is never reached in
// practice.
const promoteAll = capacityMax

// SetRSVP sets the caller's answer to going, maybe or not_going and returns
// the stored answer: waitlist when going did not fit. When a going answer
// changes, the oldest waitlisted answer becomes going in the same
// transaction (data model v14 "Bekleme listesi terfisi").
func (s *Service) SetRSVP(ctx context.Context, user, tayfa, event id.ID, want string) (string, error) {
	if want != RSVPGoing && want != RSVPMaybe && want != RSVPNotGoing {
		return "", apperr.Validation("status")
	}
	var stored string
	err := s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		if err := c.authorize(authz.EventRSVP); err != nil {
			return err
		}
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if err := changeable(ev, c.now); err != nil {
			return err
		}
		cur := ""
		r, err := q.GetRsvp(ctx, store.GetRsvpParams{EventID: event.PG(), UserID: user.PG()})
		switch {
		case err == nil:
			cur = r.Status
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("katılım: %w", err)
		}

		next := want
		if want == RSVPGoing {
			// Already going, or waiting in line: nothing changes and the
			// place in line is kept.
			if cur == RSVPGoing || cur == RSVPWaitlist {
				stored = cur
				return nil
			}
			if ev.Capacity.Valid {
				n, err := q.CountGoing(ctx, event.PG())
				if err != nil {
					return fmt.Errorf("katılımcı sayısı: %w", err)
				}
				if n >= int64(ev.Capacity.Int32) {
					next = RSVPWaitlist
				}
			}
		}
		stored = next
		if cur == next {
			return nil
		}
		if err := q.SetRsvp(ctx, store.SetRsvpParams{EventID: event.PG(), UserID: user.PG(), Status: next}); err != nil {
			return fmt.Errorf("katılım: %w", err)
		}
		// Someone who is no longer going leaves no vehicle (and no plate)
		// behind on a convoy.
		if next != RSVPGoing && ev.Type == TypeConvoy {
			if _, err := q.DeleteUserConvoyVehicle(ctx, store.DeleteUserConvoyVehicleParams{EventID: event.PG(), UserID: user.PG()}); err != nil {
				return fmt.Errorf("araç: %w", err)
			}
		}
		if err := audit(ctx, q, c, RSVPChanged, event, map[string]any{"status": next}); err != nil {
			return err
		}
		b.add(RSVPChanged, event, map[string]any{"event_id": event.String(), "user_id": user.String(), "status": next})
		if cur == RSVPGoing {
			return promote(ctx, q, c, b, event, ev.Capacity)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return stored, nil
}

// promote moves the oldest waitlisted answers to going while there is room.
// The caller holds the event's row lock.
func promote(ctx context.Context, q *store.Queries, c caller, b *batch, event id.ID, capacity pgtype.Int4) error {
	free := int64(promoteAll)
	if capacity.Valid {
		n, err := q.CountGoing(ctx, event.PG())
		if err != nil {
			return fmt.Errorf("katılımcı sayısı: %w", err)
		}
		free = int64(capacity.Int32) - n
	}
	if free <= 0 {
		return nil
	}
	users, err := q.NextWaitlisted(ctx, store.NextWaitlistedParams{EventID: event.PG(), N: int32(min(free, promoteAll))}) //nolint:gosec // bounded by promoteAll
	if err != nil {
		return fmt.Errorf("bekleme listesi: %w", err)
	}
	for _, u := range users {
		n, err := q.PromoteRsvp(ctx, store.PromoteRsvpParams{EventID: event.PG(), UserID: u})
		if err != nil {
			return fmt.Errorf("terfi: %w", err)
		}
		if n != 1 {
			continue
		}
		uid := id.ID(u.Bytes)
		if err := audit(ctx, q, c, RSVPPromoted, event, map[string]any{"user_id": uid.String()}); err != nil {
			return err
		}
		b.add(RSVPPromoted, event, map[string]any{"event_id": event.String(), "user_id": uid.String()})
	}
	return nil
}

// Attendee is one answer in an attendee list.
type Attendee struct {
	UserID id.ID
	Status string
	// UpdatedAt is when the answer last changed.
	UpdatedAt time.Time
}

// ListAttendees lists the answers with status st ("" means going), oldest
// first; for the waitlist that is the order in line.
func (s *Service) ListAttendees(ctx context.Context, user, tayfa, event id.ID, st string, size int32, token string) ([]Attendee, string, error) {
	if st == "" {
		st = RSVPGoing
	}
	switch st {
	case RSVPGoing, RSVPMaybe, RSVPNotGoing, RSVPWaitlist:
	default:
		return nil, "", apperr.Validation("status")
	}
	size, err := pageSize(size)
	if err != nil {
		return nil, "", err
	}
	scope := event.String() + "/" + st
	after, at, err := decodeCursor(token, "ListAttendees", tayfa, scope)
	if err != nil {
		return nil, "", err
	}
	c, err := s.load(ctx, s.q, user, tayfa, false)
	if err != nil {
		return nil, "", err
	}
	if err := c.authorize(authz.EventViewAttendees); err != nil {
		return nil, "", err
	}
	if _, err := getEvent(ctx, s.q, tayfa, event); err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListAttendees(ctx, store.ListAttendeesParams{
		EventID: event.PG(), Status: st, AfterTime: ts(at), AfterUserID: after.PG(), PageSize: size + 1,
	})
	if err != nil {
		return nil, "", fmt.Errorf("katılımcılar: %w", err)
	}
	next := ""
	if len(rows) > int(size) {
		rows = rows[:size]
		last := rows[len(rows)-1]
		next = encodeCursor("ListAttendees", tayfa, scope, last.UpdatedAt.Time, id.ID(last.UserID.Bytes))
	}
	out := make([]Attendee, len(rows))
	for i, r := range rows {
		out[i] = Attendee{UserID: id.ID(r.UserID.Bytes), Status: r.Status, UpdatedAt: r.UpdatedAt.Time.UTC()}
	}
	return out, next, nil
}
