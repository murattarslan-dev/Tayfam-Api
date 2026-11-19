// Package goal is the goal bar component (kind "goal", issue #246;
// ADR-0025 §4, §5): a target for one of the event's measures and the
// progress towards it.
//
// The definition binds the goal to a measure of the same type version
// (parameter measure, checked by the catalog) and may give a default
// target (default_target). The organizer may set the event's own target
// (event_goals) or remove it, which brings the default back. The target is
// in the measure's unit.
//
// Progress is never stored: it is read from the measure's stored values
// (event_measurements), the single value of a total measure or the sum of
// a personal measure's values. A suggested value (prefill_from) is not a
// measurement and does not count. The goal never shows more than the
// measure does: for a sensitive personal measure, where each person sees
// only their own value, the sum could tell one person's value (two
// participants, or one), so only those who see every value see the
// progress; for a measure the organizer switched off there is no progress.
//
// Who may do what (the framework's actions, package component):
//
//	read    every active member who may see the event's components
//	target  managers (the organizer, and moderators and admins as for
//	        every event), from creating the event until the write window
//	        closes (component.FreezeAfter)
//
// Targets are not personal data and are not logged; audit records name the
// component only.
package goal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/measure"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// Kind is the component kind this package serves.
const Kind = definition.CompGoal

// MaxTarget bounds every target: the event_goals_target CHECK.
const MaxTarget = measure.MaxValue

// Audit actions of target changes. The records never hold a value.
const (
	AuditSet     = "event.goal_set"
	AuditRemoved = "event.goal_removed"
)

// Goal is one goal component of an event, as the caller sees it.
type Goal struct {
	// Measure is the key of the measure the goal is bound to; Unit and
	// Decimals are that measure's.
	Measure  string
	Unit     string
	Decimals int32
	// Target is the event's target: the organizer's, or the definition's
	// default without one; nil when there is neither. TargetSet reports
	// whether it is the organizer's, set at TargetUpdatedAt.
	Target          *float64
	TargetSet       bool
	TargetUpdatedAt time.Time
	// DefaultTarget is the definition's default target; nil without one.
	DefaultTarget *float64
	// Current is the measure's stored value, or the sum of its values;
	// nil when the caller may not see it (ProgressHidden) or the measure
	// is switched off (MeasureEnabled false).
	Current        *float64
	ProgressHidden bool
	MeasureEnabled bool
	// Reached reports whether Current has reached Target.
	Reached bool
	// CallerCanEdit reports whether the caller may set the target now.
	CallerCanEdit bool
}

// Service is the goal component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// bound is the measure the goal is bound to, from the event's type
// version.
type bound struct {
	comp      definition.Component
	unit      string
	scope, by string
	sensitive bool
	max       float64
	decimals  int
}

// boundOf returns the goal's measure. The catalog checks that it exists;
// a definition without it is an internal error, not a refusal.
func boundOf(sc *component.Scope) (bound, error) {
	key := sc.ParamString("measure")
	c, ok := sc.Definition.Component(key)
	if !ok || c.Kind != measure.Kind {
		return bound{}, fmt.Errorf("hedef %q: bağlı ölçüm %q tanımda yok", sc.Key, key)
	}
	b := bound{
		comp: c, unit: c.Params["unit"].String(), scope: c.Params["scope"].String(), by: c.Params["entered_by"].String(),
		sensitive: c.Params["sensitive"].Bool(), max: measure.MaxValue,
	}
	if v, ok := c.Params["max"].Value.(float64); ok {
		b.max = v
	}
	if n, ok := c.Params["decimals"].Value.(int64); ok && n >= 0 && n <= 3 {
		b.decimals = int(n)
	}
	return b, nil
}

// enabled reports whether the measure is on for the event: always, or by
// the organizer's selection (the definition's default without one).
func (b bound) enabled(ctx context.Context, sc *component.Scope) (bool, error) {
	if b.comp.Optional == nil {
		return true, nil
	}
	r, err := sc.Q().GetEventComponent(ctx, store.GetEventComponentParams{EventID: sc.Event.PG(), ComponentKey: b.comp.Key})
	if errors.Is(err, pgx.ErrNoRows) {
		return *b.comp.Optional, nil
	}
	if err != nil {
		return false, fmt.Errorf("bağlı ölçümün seçimi: %w", err)
	}
	return r.Enabled, nil
}

// visible reports whether the caller may see the progress: always, except
// for a sensitive personal measure, whose sum only those who see every
// value may see (managers, when the organizer enters the values).
func (b bound) visible(sc *component.Scope) bool {
	if !b.sensitive || b.scope != measure.ScopePersonal {
		return true
	}
	return b.by == measure.EnteredByOrganizer && sc.IsManager()
}

// clean rounds a target to the measure's decimals and checks its bounds:
// above zero, at most MaxTarget and, for a total measure, at most the
// measure's own maximum (a higher one could never be reached).
func (b bound) clean(v float64) (float64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	scale := math.Pow10(b.decimals)
	v = math.Round(v*scale) / scale
	if v <= 0 || v > MaxTarget || (b.scope == measure.ScopeTotal && v > b.max) {
		return 0, false
	}
	return v, true
}

// defaultTarget is the definition's default target; ok is false without
// one (or with 0, which is no target).
func defaultTarget(sc *component.Scope) (v float64, ok bool) {
	p, set := sc.Param("default_target")
	if !set {
		return 0, false
	}
	v, ok = p.Value.(float64)
	return v, ok && v > 0
}

// canEdit is what CallerCanEdit reports: the checks Change makes.
func canEdit(sc *component.Scope) bool {
	return sc.Enabled && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow()) && sc.IsManager()
}

// Get returns the goal. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Goal, error) {
	var out Goal
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Goal{}, err
	}
	return out, nil
}

// read builds the goal as the caller sees it, inside View or Change.
func read(ctx context.Context, sc *component.Scope) (Goal, error) {
	b, err := boundOf(sc)
	if err != nil {
		return Goal{}, err
	}
	out := Goal{Measure: b.comp.Key, Unit: b.unit, Decimals: int32(b.decimals), CallerCanEdit: canEdit(sc)} //nolint:gosec // 0..3
	if v, ok := defaultTarget(sc); ok {
		out.DefaultTarget, out.Target = &v, &v
	}
	row, err := sc.Q().GetEventGoal(ctx, store.GetEventGoalParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Goal{}, fmt.Errorf("hedef: %w", err)
	default:
		v := row.Target
		out.Target, out.TargetSet, out.TargetUpdatedAt = &v, true, row.UpdatedAt.Time.UTC()
	}
	out.MeasureEnabled, err = b.enabled(ctx, sc)
	if err != nil {
		return Goal{}, err
	}
	if !out.MeasureEnabled {
		return out, nil
	}
	if !b.visible(sc) {
		out.ProgressHidden = true
		return out, nil
	}
	p, err := sc.Q().SumEventGoalProgress(ctx, store.SumEventGoalProgressParams{
		EventID: sc.Event.PG(), MeasureKey: b.comp.Key, Personal: b.scope == measure.ScopePersonal,
	})
	if err != nil {
		return Goal{}, fmt.Errorf("hedef ilerlemesi: %w", err)
	}
	cur := p.Total
	out.Current = &cur
	out.Reached = out.Target != nil && cur >= *out.Target
	return out, nil
}

// SetTarget sets the event's target. Managers only. Refused: a target that
// is not above zero, above MaxTarget or, for a total measure, above the
// measure's maximum (validation error on "target"), and the refusals of
// component.Service.Change. The same target again changes nothing.
func (s *Service) SetTarget(ctx context.Context, ref component.Ref, target float64) (Goal, error) {
	if math.IsNaN(target) || math.IsInf(target, 0) {
		return Goal{}, apperr.Validation("target")
	}
	var out Goal
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		b, err := boundOf(sc)
		if err != nil {
			return err
		}
		v, ok := b.clean(target)
		if !ok {
			return apperr.Validation("target")
		}
		prev, err := sc.Q().GetEventGoal(ctx, store.GetEventGoalParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("hedef: %w", err)
		}
		if err != nil || prev.Target != v {
			if err := sc.Q().SetEventGoal(ctx, store.SetEventGoalParams{
				EventID: sc.Event.PG(), ComponentKey: sc.Key, Target: v, Now: ts(sc.Now),
			}); err != nil {
				return fmt.Errorf("hedefi yazma: %w", err)
			}
			sc.Changed()
			if err := sc.Audit(ctx, AuditSet, nil); err != nil {
				return err
			}
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Goal{}, err
	}
	return out, nil
}

// RemoveTarget removes the event's target; the definition's default holds
// again. Managers only; the refusals of component.Service.Change. Removing
// a target that is not there is not an error.
func (s *Service) RemoveTarget(ctx context.Context, ref component.Ref) (Goal, error) {
	var out Goal
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		n, err := sc.Q().DeleteEventGoal(ctx, store.DeleteEventGoalParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("hedefi kaldırma: %w", err)
		}
		if n > 0 {
			sc.Changed()
			if err := sc.Audit(ctx, AuditRemoved, nil); err != nil {
				return err
			}
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Goal{}, err
	}
	return out, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
