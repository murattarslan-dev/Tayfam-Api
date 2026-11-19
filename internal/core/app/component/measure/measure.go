// Package measure is the measure component (kind "measure", issue #245;
// ADR-0025 §4, §5, §11; ADR-0037 §2, §3): a value with a unit, for the
// whole event (scope "total": a hike's distance) or one per participant
// (scope "personal": goals, pages read).
//
// The definition's parameters decide everything about a measure: its unit
// (a closed list), scope, who enters it (entered_by), its bounds and
// decimals, whether it may reach the showcase (showcase, read by the
// statistics), whether only the person sees their value (sensitive) and
// the field it is suggested from (prefill_from). The statistics (issue
// #266), the goal bar (#246) and the ranking (#253) read the stored values
// (event_measurements): a total measure's single value, or a personal
// measure's value per participant. A suggestion is not stored and never
// counts until someone writes it.
//
// Who may do what (the framework's actions, package component):
//
//	read            every active member who may see the event's components;
//	                a sensitive measure shows each person only their own
//	                value, unless the caller enters values for others
//	total           managers (the organizer, and moderators and admins as
//	                for every event)
//	personal, self  a participant whose attendance was verified
//	                (authz.EventComponentParticipate), their own value only
//	personal,       managers and the referees the organizer assigned
//	organizer       (authz.EventComponentReferee), for any participant
//
// Values are entered from the event's start until the write window closes
// (component.FreezeAfter). Values are never logged; audit records name the
// component and, for a value entered for someone else, the person's ID.
package measure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompMeasure

// Scopes and writers: the values of the scope and entered_by parameters.
const (
	ScopeTotal    = "total"
	ScopePersonal = "personal"

	EnteredBySelf      = "self"
	EnteredByOrganizer = "organizer"
)

// MaxValue bounds every value: the event_measurements_value CHECK. A
// definition without min is bounded below by 0, without max by MaxValue.
const MaxValue = 1_000_000_000

// Audit actions of measurement changes. The records never hold a value.
const (
	AuditSet     = "event.measurement_set"
	AuditRemoved = "event.measurement_removed"
)

// Entry is one participant's value of a personal measure.
type Entry struct {
	User      id.ID
	Value     float64
	UpdatedAt time.Time
}

// Measure is one measure component of an event, as the caller sees it.
type Measure struct {
	// Unit, Scope and EnteredBy are the definition's parameters.
	Unit      string
	Scope     string
	EnteredBy string
	// Min, Max and Decimals are the bounds values are checked against and
	// the decimals they are rounded to.
	Min      float64
	Max      float64
	Decimals int32
	// Total is the value of a total measure; nil without one.
	Total          *float64
	TotalUpdatedAt time.Time
	// Entries are the values of a personal measure the caller may see,
	// oldest change first; empty for a total measure.
	Entries []Entry
	// Suggested is the value of the field the definition names
	// (prefill_from), once the event has ended and while a total measure
	// has no value; nil otherwise.
	Suggested *float64
	// CallerCanEdit reports whether the caller may write now: the total,
	// their own value or everyone's, by the measure's rule.
	CallerCanEdit bool
}

// Service is the measure component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// rules are a measure's parameters for this event.
type rules struct {
	unit, scope, by string
	min, max        float64
	decimals        int
	sensitive       bool
	prefill         string
}

func rulesOf(sc *component.Scope) rules {
	r := rules{
		unit: sc.ParamString("unit"), scope: sc.ParamString("scope"), by: sc.ParamString("entered_by"),
		min: 0, max: MaxValue, sensitive: sc.ParamBool("sensitive"), prefill: sc.ParamString("prefill_from"),
	}
	if p, ok := sc.Param("min"); ok {
		if v, ok := p.Value.(float64); ok {
			r.min = v
		}
	}
	if p, ok := sc.Param("max"); ok {
		if v, ok := p.Value.(float64); ok {
			r.max = v
		}
	}
	if n, ok := sc.ParamInt("decimals"); ok && n >= 0 && n <= 3 {
		r.decimals = int(n) //nolint:gosec // 0..3
	}
	return r
}

// clean rounds v to the measure's decimals and checks its bounds.
func (r rules) clean(v float64) (float64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	scale := math.Pow10(r.decimals)
	v = math.Round(v*scale) / scale
	if v < r.min || v > r.max || v < -MaxValue || v > MaxValue {
		return 0, false
	}
	return v, true
}

// writable reports whether the event, the tayfa and the window take a
// value now, apart from the caller.
func writable(sc *component.Scope) bool {
	return sc.Enabled && sc.Started() && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// entersForOthers reports whether the caller may enter values for others:
// a manager or an assigned referee of a personal measure the organizer
// enters.
func entersForOthers(ctx context.Context, sc *component.Scope, r rules) bool {
	return r.scope == ScopePersonal && r.by == EnteredByOrganizer && sc.AuthorizeReferee(ctx, id.ID{}) == nil
}

// canEdit is what CallerCanEdit reports.
func canEdit(ctx context.Context, sc *component.Scope, r rules) (bool, error) {
	if !writable(sc) {
		return false, nil
	}
	switch {
	case r.scope == ScopeTotal:
		return sc.IsManager(), nil
	case r.by == EnteredByOrganizer:
		return entersForOthers(ctx, sc, r), nil
	case sc.Authorize(authz.EventComponentParticipate) != nil:
		return false, nil
	}
	return sc.Attended(ctx)
}

// Get returns the measure. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Measure, error) {
	var out Measure
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Measure{}, err
	}
	return out, nil
}

// read builds the measure as the caller sees it, inside View or Change.
func read(ctx context.Context, sc *component.Scope) (Measure, error) {
	r := rulesOf(sc)
	out := Measure{
		Unit: r.unit, Scope: r.scope, EnteredBy: r.by, Min: r.min, Max: r.max, Decimals: int32(r.decimals), //nolint:gosec // 0..3
		Entries: []Entry{},
	}
	switch r.scope {
	case ScopeTotal:
		row, err := sc.Q().GetEventMeasurementTotal(ctx, store.GetEventMeasurementTotalParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return Measure{}, fmt.Errorf("ölçüm: %w", err)
		default:
			v := row.Value
			out.Total, out.TotalUpdatedAt = &v, row.UpdatedAt.Time.UTC()
		}
	case ScopePersonal:
		rows, err := sc.Q().ListEventMeasurements(ctx, store.ListEventMeasurementsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return Measure{}, fmt.Errorf("ölçümler: %w", err)
		}
		all := !r.sensitive || entersForOthers(ctx, sc, r)
		for _, row := range rows {
			u := id.ID(row.UserID.Bytes)
			if !all && u != sc.User {
				continue
			}
			out.Entries = append(out.Entries, Entry{User: u, Value: row.Value, UpdatedAt: row.UpdatedAt.Time.UTC()})
		}
	default:
		return Measure{}, fmt.Errorf("ölçüm: bilinmeyen kapsam %q", r.scope)
	}
	if r.prefill != "" && sc.Ended() && out.Total == nil {
		v, ok, err := suggestion(ctx, sc, r)
		if err != nil {
			return Measure{}, err
		}
		if ok {
			out.Suggested = &v
		}
	}
	ok, err := canEdit(ctx, sc, r)
	if err != nil {
		return Measure{}, err
	}
	out.CallerCanEdit = ok
	return out, nil
}

// suggestion reads the value of the field the measure is suggested from;
// ok is false without a value or with one the measure would refuse.
func suggestion(ctx context.Context, sc *component.Scope, r rules) (v float64, ok bool, err error) {
	raw, err := sc.Q().GetMeasurementPrefill(ctx, store.GetMeasurementPrefillParams{EventID: sc.Event.PG(), FieldKey: r.prefill})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("önerilen değer: %w", err)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false, nil
	}
	v, ok = r.clean(v)
	return v, ok, nil
}

// SetTotal writes the value of a total measure. Managers only. Refused: a
// value outside the bounds or not finite (validation error on "value"), a
// personal measure (apperr.ErrMeasurementScope), an event that has not
// started (apperr.ErrMeasurementNotStarted) and the refusals of
// component.Service.Change. The same value again changes nothing.
func (s *Service) SetTotal(ctx context.Context, ref component.Ref, value float64) (Measure, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Measure{}, apperr.Validation("value")
	}
	var out Measure
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		r := rulesOf(sc)
		if r.scope != ScopeTotal {
			return apperr.ErrMeasurementScope
		}
		if !sc.Started() {
			return apperr.ErrMeasurementNotStarted
		}
		v, ok := r.clean(value)
		if !ok {
			return apperr.Validation("value")
		}
		prev, err := sc.Q().GetEventMeasurementTotal(ctx, store.GetEventMeasurementTotalParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("ölçüm: %w", err)
		}
		if err != nil || prev.Value != v {
			if err := sc.Q().SetEventMeasurementTotal(ctx, store.SetEventMeasurementTotalParams{
				ID: id.New().PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, Value: v,
				EnteredBy: sc.User.PG(), Now: ts(sc.Now),
			}); err != nil {
				return fmt.Errorf("ölçümü yazma: %w", err)
			}
			sc.Changed()
			if err := sc.Audit(ctx, AuditSet, map[string]any{"scope": ScopeTotal}); err != nil {
				return err
			}
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Measure{}, err
	}
	return out, nil
}

// RemoveTotal removes the value of a total measure. Managers only; the
// refusals of SetTotal apart from the value and the start. Removing a value
// that is not there is not an error.
func (s *Service) RemoveTotal(ctx context.Context, ref component.Ref) (Measure, error) {
	var out Measure
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if rulesOf(sc).scope != ScopeTotal {
			return apperr.ErrMeasurementScope
		}
		n, err := sc.Q().DeleteEventMeasurementTotal(ctx, store.DeleteEventMeasurementTotalParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("ölçümü kaldırma: %w", err)
		}
		if n > 0 {
			sc.Changed()
			if err := sc.Audit(ctx, AuditRemoved, map[string]any{"scope": ScopeTotal}); err != nil {
				return err
			}
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Measure{}, err
	}
	return out, nil
}

// authorizeEntry checks that the caller may write user's value of a
// personal measure: their own with entered_by self, anyone's as a manager
// or an assigned referee with entered_by organizer.
func authorizeEntry(ctx context.Context, sc *component.Scope, r rules, user id.ID) error {
	if r.scope != ScopePersonal {
		return apperr.ErrMeasurementScope
	}
	switch r.by {
	case EnteredBySelf:
		if err := sc.Authorize(authz.EventComponentParticipate); err != nil {
			return err
		}
		if user != sc.User {
			return apperr.ErrPermissionDenied
		}
		return nil
	case EnteredByOrganizer:
		return sc.AuthorizeReferee(ctx, id.ID{})
	}
	return fmt.Errorf("ölçüm: bilinmeyen giren %q", r.by)
}

// SetEntry writes user's value of a personal measure. Refused: a value
// outside the bounds or not finite (validation error on "value"), a total
// measure (apperr.ErrMeasurementScope), a caller who may not write this
// value (apperr.ErrPermissionDenied, apperr.ErrMuted), an event that has
// not started (apperr.ErrMeasurementNotStarted), one's own value without a
// verified attendance (apperr.ErrNotAttended), a value for someone who is
// not a participant (apperr.ErrMeasurementNotParticipant) and the refusals
// of component.Service.Change. The same value again changes nothing.
func (s *Service) SetEntry(ctx context.Context, ref component.Ref, user id.ID, value float64) (Measure, error) {
	if user == (id.ID{}) {
		return Measure{}, apperr.Validation("user_id")
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Measure{}, apperr.Validation("value")
	}
	var out Measure
	// Who may write depends on the definition: the change is opened to
	// readers and authorizeEntry decides.
	err := s.fw.Change(ctx, ref, Kind, component.ByAction(authz.EventComponentView), func(sc *component.Scope) error {
		r := rulesOf(sc)
		if err := authorizeEntry(ctx, sc, r, user); err != nil {
			return err
		}
		if !sc.Started() {
			return apperr.ErrMeasurementNotStarted
		}
		if r.by == EnteredBySelf {
			attended, err := sc.Attended(ctx)
			if err != nil {
				return err
			}
			if !attended {
				return apperr.ErrNotAttended
			}
		} else {
			member, err := sc.ActiveMember(ctx, user)
			if err != nil {
				return err
			}
			going, attended, err := sc.Participation(ctx, user)
			if err != nil {
				return err
			}
			if !member || (!going && !attended) {
				return apperr.ErrMeasurementNotParticipant
			}
		}
		v, ok := r.clean(value)
		if !ok {
			return apperr.Validation("value")
		}
		prev, err := sc.Q().GetEventMeasurement(ctx, store.GetEventMeasurementParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: user.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("ölçüm: %w", err)
		}
		if err != nil || prev.Value != v {
			if err := sc.Q().SetEventMeasurement(ctx, store.SetEventMeasurementParams{
				ID: id.New().PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: user.PG(), Value: v,
				EnteredBy: sc.User.PG(), Now: ts(sc.Now),
			}); err != nil {
				return fmt.Errorf("ölçümü yazma: %w", err)
			}
			sc.Changed()
			if user != sc.User {
				if err := sc.Audit(ctx, AuditSet, map[string]any{"scope": ScopePersonal, "user_id": user.String()}); err != nil {
					return err
				}
			}
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Measure{}, err
	}
	return out, nil
}

// RemoveEntry removes user's value of a personal measure; who may remove it
// is who may write it, without the participation checks. Removing a value
// that is not there is not an error.
func (s *Service) RemoveEntry(ctx context.Context, ref component.Ref, user id.ID) (Measure, error) {
	if user == (id.ID{}) {
		return Measure{}, apperr.Validation("user_id")
	}
	var out Measure
	err := s.fw.Change(ctx, ref, Kind, component.ByAction(authz.EventComponentView), func(sc *component.Scope) error {
		if err := authorizeEntry(ctx, sc, rulesOf(sc), user); err != nil {
			return err
		}
		n, err := sc.Q().DeleteEventMeasurement(ctx, store.DeleteEventMeasurementParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: user.PG()})
		if err != nil {
			return fmt.Errorf("ölçümü kaldırma: %w", err)
		}
		if n > 0 {
			sc.Changed()
			if user != sc.User {
				if err := sc.Audit(ctx, AuditRemoved, map[string]any{"scope": ScopePersonal, "user_id": user.String()}); err != nil {
					return err
				}
			}
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Measure{}, err
	}
	return out, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
