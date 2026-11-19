// Package rollcall is the roll call and overdue alarm component (kind
// "roll_call", issue #242; ADR-0025 §4, §9): who set out and who came back,
// and the alarm when someone does not return. It replaces the Faz 1 hike
// roll call for every type that has the component.
//
// The plan is the expected return (event_roll_calls). Whether the alarm is
// on and its grace period are parameters of the component in the event's
// type version (alarm, return_grace_minutes); the alarm itself is raised
// by the background job (jobs.AlertOverdueHikes). Setting out counts as
// attendance (package event: RollCallStarted). No device location is
// taken, stored or logged (ADR-0008): a roll call row holds times only.
//
// Who may do what (the framework's actions, package component):
//
//	read     every active member who may see the event's components; the
//	         records of others only for managers
//	plan     managers: the organizer, and moderators and admins as for
//	         every event; until the write window closes
//	set out  the caller, for themselves (authz.EventCheckIn, muted members
//	         included: a safety signal); going, in the check-in window
//	return   the caller, for themselves, after setting out; a safety
//	         signal (component.BySafetySignal): also after the end, after
//	         the component froze and on a cancelled event
package rollcall

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompRollCall

const (
	// DefaultGraceMinutes is the grace period after the expected return
	// when the definition does not give return_grace_minutes (the same
	// default as roll_call_alarm_minutes in the database).
	DefaultGraceMinutes = 30
	// MaxEntries is how many records a manager sees, those still out first.
	MaxEntries = 1000

	// clientSkew and clientMaxAge bound client times (setting out and
	// returning): not in the future beyond a little clock skew, not older
	// than 48 hours (v14 "Çevrimdışı yazma"). The database allows 5
	// minutes.
	clientSkew   = 2 * time.Minute
	clientMaxAge = 48 * time.Hour
)

// Audit actions of roll call changes. The records name the component and
// the person only, never a time or a place.
const (
	AuditPlanned   = "event.roll_call_planned"
	AuditUnplanned = "event.roll_call_unplanned"
	AuditStarted   = "event.roll_call_started"
	AuditReturned  = "event.roll_call_returned"
)

// Entry is one participant's record. Times are the client's; ReturnedAt is
// zero while out.
type Entry struct {
	User       id.ID
	StartedAt  time.Time
	ReturnedAt time.Time
}

// RollCall is one roll call component of an event, as the caller sees it.
type RollCall struct {
	// Alarm and ReturnGraceMinutes are the component's parameters.
	Alarm              bool
	ReturnGraceMinutes int32
	// ExpectedReturnAt is zero without a plan (no alarm then).
	ExpectedReturnAt time.Time
	// Mine is the caller's own record; nil before they set out.
	Mine *Entry
	// Entries are everyone's records, only for managers.
	Entries []Entry
	// CallerCanManage reports whether the caller manages the event's
	// components (and sees Entries).
	CallerCanManage bool
	// CallerCanEdit reports whether the caller may change the plan now.
	CallerCanEdit bool
}

// Service is the roll call component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// clientTime checks a client-reported time against now; zero means now.
func clientTime(t, now time.Time, field string) (time.Time, error) {
	if t.IsZero() {
		return now, nil
	}
	t = t.UTC().Truncate(time.Microsecond)
	if t.After(now.Add(clientSkew)) || t.Before(now.Add(-clientMaxAge)) {
		return time.Time{}, apperr.Validation(field)
	}
	return t, nil
}

// grace is the component's grace period in minutes.
func grace(sc *component.Scope) int32 {
	if n, ok := sc.ParamInt("return_grace_minutes"); ok && n >= 0 && n <= 1440 {
		return int32(n) //nolint:gosec // 0..1440
	}
	return DefaultGraceMinutes
}

// view fills out with what the caller sees of the component.
func view(ctx context.Context, sc *component.Scope, out *RollCall) error {
	*out = RollCall{Alarm: sc.ParamBool("alarm"), ReturnGraceMinutes: grace(sc), CallerCanManage: sc.IsManager()}
	out.CallerCanEdit = out.CallerCanManage && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
	q := sc.Q()
	p, err := q.GetRollCallPlan(ctx, store.GetRollCallPlanParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
	switch {
	case err == nil:
		out.ExpectedReturnAt = p.ExpectedReturnAt.Time.UTC()
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("yoklama planı: %w", err)
	}
	mine, ok, err := checkin(ctx, sc)
	if err != nil {
		return err
	}
	if ok {
		out.Mine = &mine
	}
	if !out.CallerCanManage {
		return nil
	}
	rows, err := q.ListRollCallCheckins(ctx, store.ListRollCallCheckinsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, N: MaxEntries})
	if err != nil {
		return fmt.Errorf("yoklama: %w", err)
	}
	out.Entries = make([]Entry, 0, len(rows))
	for _, r := range rows {
		e := Entry{User: id.ID(r.UserID.Bytes), StartedAt: r.StartedAt.Time.UTC()}
		if r.ReturnedAt.Valid {
			e.ReturnedAt = r.ReturnedAt.Time.UTC()
		}
		out.Entries = append(out.Entries, e)
	}
	return nil
}

// checkin loads the caller's record; ok is false without one.
func checkin(ctx context.Context, sc *component.Scope) (Entry, bool, error) {
	r, err := sc.Q().GetRollCallCheckin(ctx, store.GetRollCallCheckinParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("yoklama: %w", err)
	}
	e := Entry{User: sc.User, StartedAt: r.StartedAt.Time.UTC()}
	if r.ReturnedAt.Valid {
		e.ReturnedAt = r.ReturnedAt.Time.UTC()
	}
	return e, true, nil
}

// Get returns the roll call. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (RollCall, error) {
	var out RollCall
	if err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error { return view(ctx, sc, &out) }); err != nil {
		return RollCall{}, err
	}
	return out, nil
}

// SetExpectedReturn sets the plan's expected return, or removes the plan
// when at is zero. Managers only. Refused: a time not after the event's
// start (validation error on "expected_return_time") and the refusals of
// component.Service.Change. Setting the time that is already there and
// removing a plan that is not there change nothing and are not errors.
func (s *Service) SetExpectedReturn(ctx context.Context, ref component.Ref, at time.Time) (RollCall, error) {
	at = at.UTC().Truncate(time.Microsecond)
	var out RollCall
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		q := sc.Q()
		if at.IsZero() {
			n, err := q.DeleteRollCallPlan(ctx, store.DeleteRollCallPlanParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
			if err != nil {
				return fmt.Errorf("yoklama planını kaldırma: %w", err)
			}
			if n > 0 {
				sc.Changed()
				if err := sc.Audit(ctx, AuditUnplanned, nil); err != nil {
					return err
				}
			}
			return view(ctx, sc, &out)
		}
		if !at.After(sc.StartsAt) {
			return apperr.Validation("expected_return_time")
		}
		prev, err := q.GetRollCallPlan(ctx, store.GetRollCallPlanParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		switch {
		case err == nil && prev.ExpectedReturnAt.Time.Equal(at):
			// The same plan again (a retried request) changes nothing.
			return view(ctx, sc, &out)
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("yoklama planı: %w", err)
		}
		if err := q.SetRollCallPlan(ctx, store.SetRollCallPlanParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key,
			ExpectedReturnAt: ts(at), UpdatedAt: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("yoklama planını yazma: %w", err)
		}
		sc.Changed()
		if err := sc.Audit(ctx, AuditPlanned, nil); err != nil {
			return err
		}
		return view(ctx, sc, &out)
	})
	if err != nil {
		return RollCall{}, err
	}
	return out, nil
}

// Start records that the caller set out; startedAt is the client's time
// (zero: now). It counts as attendance (event.MethodHikeStart). Refused:
// a time outside the client time rules (validation error on
// "start_time"), the server's time outside the check-in window
// (apperr.ErrCheckInClosed), a caller who did not answer going
// (apperr.ErrEventNotGoing) and the refusals of component.Service.Change;
// muted members may set out. A repeat returns the first start.
func (s *Service) Start(ctx context.Context, ref component.Ref, startedAt time.Time) (RollCall, error) {
	var out RollCall
	err := s.fw.Change(ctx, ref, Kind, component.ByAction(authz.EventCheckIn), func(sc *component.Scope) error {
		start, err := clientTime(startedAt, sc.Now, "start_time")
		if err != nil {
			return err
		}
		if !event.InCheckInWindow(sc.StartsAt, sc.EndsAt, sc.Now) {
			return apperr.ErrCheckInClosed
		}
		going, err := sc.Going(ctx)
		if err != nil {
			return err
		}
		if !going {
			return apperr.ErrEventNotGoing
		}
		n, err := sc.Q().StartRollCallCheckin(ctx, store.StartRollCallCheckinParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG(),
			StartedAt: ts(start), ReceivedAt: ts(sc.Now),
		})
		if err != nil {
			return fmt.Errorf("yoklama: %w", err)
		}
		if n == 0 {
			// Already out (or back): the first start stands.
			return view(ctx, sc, &out)
		}
		emit, err := event.RollCallStarted(ctx, sc.Q(), sc.TayfaRow(), sc.User, sc.Event, sc.Now)
		if err != nil {
			return err
		}
		sc.Defer(emit)
		sc.Changed()
		if err := sc.Audit(ctx, AuditStarted, map[string]any{"user_id": sc.User.String()}); err != nil {
			return err
		}
		return view(ctx, sc, &out)
	})
	if err != nil {
		return RollCall{}, err
	}
	return out, nil
}

// Return records the caller's return; returnedAt is the client's time
// (zero: now). A safety signal (component.BySafetySignal): it works after
// the end, after the component froze, on a cancelled event and for a
// muted member. Refused: no start (apperr.ErrHikeNotStarted), a time
// outside the client time rules or before the start (validation error on
// "return_time"), a read-only tayfa and the refusals every component
// shares. A repeat returns the first return.
func (s *Service) Return(ctx context.Context, ref component.Ref, returnedAt time.Time) (RollCall, error) {
	var out RollCall
	err := s.fw.Change(ctx, ref, Kind, component.BySafetySignal(authz.EventCheckIn), func(sc *component.Scope) error {
		back, err := clientTime(returnedAt, sc.Now, "return_time")
		if err != nil {
			return err
		}
		mine, ok, err := checkin(ctx, sc)
		if err != nil {
			return err
		}
		if !ok {
			return apperr.ErrHikeNotStarted
		}
		if !mine.ReturnedAt.IsZero() {
			return view(ctx, sc, &out)
		}
		if back.Before(mine.StartedAt) {
			return apperr.Validation("return_time")
		}
		if _, err := sc.Q().ReturnRollCallCheckin(ctx, store.ReturnRollCallCheckinParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG(),
			ReturnedAt: ts(back), ReceivedAt: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("yoklama: %w", err)
		}
		if err := event.RollCallReturned(ctx, sc.Q(), sc.TayfaRow(), sc.User, sc.Event, sc.Now); err != nil {
			return err
		}
		sc.Changed()
		if err := sc.Audit(ctx, AuditReturned, map[string]any{"user_id": sc.User.String()}); err != nil {
			return err
		}
		return view(ctx, sc, &out)
	})
	if err != nil {
		return RollCall{}, err
	}
	return out, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
