package event

import (
	"context"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The attendance side of the roll call component (kind "roll_call", issue
// #242; package component/rollcall). The component keeps who set out and
// came back; setting out counts as attendance (MethodHikeStart) and the
// activity record uses the roll call's times (recordActivity). These run
// inside the component's change, with the tayfa and the event locked.

// InCheckInWindow reports whether now is in the check-in window of an
// event from startsAt to endsAt: from 30 minutes before the start until the
// end (data model v14 "Konumla check-in").
func InCheckInWindow(startsAt, endsAt, now time.Time) bool {
	return !now.Before(startsAt.Add(-checkInLead)) && now.Before(endsAt)
}

// RollCallStarted records the attendance of user, who has just set out on
// the event's roll call (the row is written): the answer is marked attended
// with MethodHikeStart, or, when it already was, the activity record now
// starts with the roll call. user must have answered going
// (apperr.ErrEventNotGoing). The returned function writes the outbox
// events; run it after the component's own writes (component.Scope.Defer).
func RollCallStarted(ctx context.Context, q *store.Queries, t access.Tayfa, user, event id.ID, now time.Time) (func(context.Context) error, error) {
	tayfa := id.ID(t.ID.Bytes)
	ev, err := getEvent(ctx, q, tayfa, event)
	if err != nil {
		return nil, err
	}
	r, ok, err := rsvpRow(ctx, q, event, user)
	if err != nil {
		return nil, err
	}
	if !ok || r.Status != RSVPGoing {
		return nil, apperr.ErrEventNotGoing
	}
	b := &batch{tayfa: tayfa, actor: user}
	if r.Attendance == AttendanceAttended {
		err = recordActivity(ctx, q, ev, user, r.AttendanceMethod.String)
	} else {
		err = markAttended(ctx, q, caller{user: user, tayfa: t, now: now}, b, ev, user, MethodHikeStart, false)
	}
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return flush(ctx, q, b) }, nil
}

// RollCallReturned updates the activity record of user, who has just come
// back from the event's roll call: the real duration replaces the planned
// one while the record is not frozen and the attendance still stands.
func RollCallReturned(ctx context.Context, q *store.Queries, t access.Tayfa, user, event id.ID, now time.Time) error {
	ev, err := getEvent(ctx, q, id.ID(t.ID.Bytes), event)
	if err != nil {
		return err
	}
	r, ok, err := rsvpRow(ctx, q, event, user)
	if err != nil {
		return err
	}
	if !ok || r.Attendance != AttendanceAttended || !inCorrectionWindow(ev, now) {
		return nil
	}
	return recordActivity(ctx, q, ev, user, r.AttendanceMethod.String)
}
