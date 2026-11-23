// Package ranking is the ranking component (kind "ranking", issue #253;
// ADR-0025 §4, §5; ADR-0037 §3): the participants of an event in order, by a
// time or a place.
//
// The definition decides where the values come from:
//
//   - source: the key of a personal measure of the same type version
//     (checked by the catalog). The ranking is computed from the measure's
//     stored values (event_measurements) and has no values of its own: there
//     is no second entry, and a write is refused (apperr.ErrRankingFromMeasure).
//     A suggested value (prefill_from) is not a measurement and does not
//     count; a measure the organizer switched off ranks nobody.
//   - metric (without source): the values are entered by hand, one per
//     participant (event_ranking_entries): a time in seconds ("duration",
//     at most two decimals) or a place ("rank", a whole number).
//
// direction orders the values: "asc" (the lowest first: a time, a place)
// or "desc" (the highest first). Without it a ranking entered by hand is
// ascending and one computed from a measure descending. Equal values share
// a place and the next place is skipped (1, 1, 3); the place is never
// stored.
//
// The ranking never shows more than its measure does: for a sensitive
// measure, where each person sees only their own value, the order would
// tell the others' values, so only those who see every value (managers,
// when the organizer enters the values) see the ranking; for everyone else
// it is hidden. Load, for the celebration, never shows it.
//
// Who may do what (the framework's actions, package component):
//
//	read    every active member who may see the event's components
//	enter   managers (the organizer, and moderators and admins as for every
//	        event) and the referees the organizer assigned
//	        (authz.EventComponentReferee), for a participant of the event,
//	        from the event's start until the write window closes
//	        (component.FreezeAfter)
//
// Values are never logged; audit records name the component and the
// person whose value changed.
package ranking

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/measure"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompRanking

// Metrics and directions: the values of the metric and direction
// parameters.
const (
	MetricDuration = "duration"
	MetricRank     = "rank"

	DirectionAsc  = "asc"
	DirectionDesc = "desc"
)

// Bounds of the values entered by hand; the event_ranking_entries_value
// CHECK holds the widest of them.
const (
	// MaxDuration is a time's upper bound in seconds (above 11 days).
	MaxDuration = 1_000_000
	// DurationDecimals is what a time is rounded to: hundredths.
	DurationDecimals = 2
	// MaxRank is a place's upper bound.
	MaxRank = 10_000
)

// Audit actions of the values entered by hand. The records never hold a
// value.
const (
	AuditSet     = "event.ranking_set"
	AuditRemoved = "event.ranking_removed"
)

// Entry is one participant's place in the ranking.
type Entry struct {
	User  id.ID
	Value float64
	// Rank is the participant's place, from 1; equal values share it.
	Rank      int32
	UpdatedAt time.Time
}

// Ranking is one ranking component of an event, as the caller sees it.
type Ranking struct {
	// Source is the key of the measure the ranking is computed from; ""
	// for one entered by hand, which has a Metric instead.
	Source    string
	Metric    string
	Direction string
	// Unit and Decimals are the measure's for a computed ranking; for one
	// entered by hand Unit is "" and Decimals those of the metric.
	Unit     string
	Decimals int32
	// Entries are the participants in order, the best first; empty when
	// Hidden or when the measure is switched off.
	Entries []Entry
	// Hidden reports that the caller may not see the ranking: its measure
	// is sensitive.
	Hidden bool
	// MeasureEnabled is false when the measure a computed ranking comes
	// from is switched off for the event; always true for a ranking
	// entered by hand.
	MeasureEnabled bool
	// CallerCanEdit reports whether the caller may enter values now; never
	// for a computed ranking.
	CallerCanEdit bool
}

// Result is a ranking as Load reads it, for the components that build on it
// (the celebration).
type Result struct {
	// Entries are the participants in order, the best first.
	Entries []Entry
	// Hidden reports that the ranking comes from a sensitive measure and
	// is not shown to anyone; Entries is then empty.
	Hidden bool
}

// Winners returns the entries in the first place: none for an empty or
// hidden ranking, more than one for a tie.
func (r Result) Winners() []Entry {
	var out []Entry
	for _, e := range r.Entries {
		if e.Rank != 1 {
			break
		}
		out = append(out, e)
	}
	return out
}

// Service is the ranking component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// rules are a ranking's parameters and, for a computed one, its measure's.
type rules struct {
	source    definition.Component
	computed  bool
	metric    string
	direction string
	unit      string
	decimals  int
	// sensitive and byOrganizer are the measure's.
	sensitive   bool
	byOrganizer bool
}

// rulesOf reads the ranking c of the definition d. The catalog checks
// that a source is a personal measure; a definition without it is an
// internal error, not a refusal.
func rulesOf(d *definition.Definition, c definition.Component) (rules, error) {
	r := rules{direction: c.Params["direction"].String()}
	if src, ok := c.Params["source"]; ok {
		m, ok := d.Component(src.String())
		if !ok || m.Kind != measure.Kind || m.Params["scope"].String() != measure.ScopePersonal {
			return rules{}, fmt.Errorf("derece %q: kaynak ölçüm %q tanımda yok", c.Key, src.String())
		}
		r.source, r.computed = m, true
		r.unit = m.Params["unit"].String()
		if n, ok := m.Params["decimals"].Value.(int64); ok && n >= 0 && n <= 3 {
			r.decimals = int(n)
		}
		r.sensitive = m.Params["sensitive"].Bool()
		r.byOrganizer = m.Params["entered_by"].String() == measure.EnteredByOrganizer
		if r.direction == "" {
			r.direction = DirectionDesc
		}
		return r, nil
	}
	r.metric = c.Params["metric"].String()
	switch r.metric {
	case MetricDuration:
		r.decimals = DurationDecimals
	case MetricRank:
	default:
		return rules{}, fmt.Errorf("derece %q: bilinmeyen ölçüt %q", c.Key, r.metric)
	}
	if r.direction == "" {
		r.direction = DirectionAsc
	}
	return r, nil
}

// clean checks a value entered by hand against the metric: a time above
// zero, rounded to hundredths, up to MaxDuration; a whole place from 1 to
// MaxRank.
func (r rules) clean(v float64) (float64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	switch r.metric {
	case MetricDuration:
		v = math.Round(v*100) / 100
		return v, v > 0 && v <= MaxDuration
	case MetricRank:
		return v, v == math.Trunc(v) && v >= 1 && v <= MaxRank
	}
	return 0, false
}

// sourceEnabled reports whether the measure of a computed ranking is on
// for the event: always, or by the organizer's selection (the
// definition's default without one).
func (r rules) sourceEnabled(ctx context.Context, q *store.Queries, event id.ID) (bool, error) {
	if !r.computed || r.source.Optional == nil {
		return true, nil
	}
	row, err := q.GetEventComponent(ctx, store.GetEventComponentParams{EventID: event.PG(), ComponentKey: r.source.Key})
	if errors.Is(err, pgx.ErrNoRows) {
		return *r.source.Optional, nil
	}
	if err != nil {
		return false, fmt.Errorf("kaynak ölçümün seçimi: %w", err)
	}
	return row.Enabled, nil
}

// values reads the stored values: the measure's or the ones entered by
// hand.
func (r rules) values(ctx context.Context, q *store.Queries, event id.ID, key string) ([]Entry, error) {
	var out []Entry
	if r.computed {
		rows, err := q.ListEventMeasurements(ctx, store.ListEventMeasurementsParams{EventID: event.PG(), ComponentKey: r.source.Key})
		if err != nil {
			return nil, fmt.Errorf("kaynak ölçüm: %w", err)
		}
		out = make([]Entry, 0, len(rows))
		for _, row := range rows {
			out = append(out, Entry{User: id.ID(row.UserID.Bytes), Value: row.Value, UpdatedAt: row.UpdatedAt.Time.UTC()})
		}
	} else {
		rows, err := q.ListEventRankingEntries(ctx, store.ListEventRankingEntriesParams{EventID: event.PG(), ComponentKey: key})
		if err != nil {
			return nil, fmt.Errorf("dereceler: %w", err)
		}
		out = make([]Entry, 0, len(rows))
		for _, row := range rows {
			out = append(out, Entry{User: id.ID(row.UserID.Bytes), Value: row.Value, UpdatedAt: row.UpdatedAt.Time.UTC()})
		}
	}
	Order(out, r.direction)
	return out, nil
}

// Order sorts entries by value in direction ("desc": the highest first;
// anything else ascending) and gives them their places: equal values share
// a place and the next one is skipped (1, 1, 3). Among equal values the
// earlier entry comes first.
func Order(entries []Entry, direction string) {
	slices.SortStableFunc(entries, func(a, b Entry) int {
		c := cmp.Compare(a.Value, b.Value)
		if direction == DirectionDesc {
			c = -c
		}
		if c != 0 {
			return c
		}
		if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
			return c
		}
		return bytes.Compare(a.User[:], b.User[:])
	})
	for i := range entries {
		if i > 0 && entries[i].Value == entries[i-1].Value {
			entries[i].Rank = entries[i-1].Rank
			continue
		}
		entries[i].Rank = int32(i + 1) //nolint:gosec // at most the event's participants
	}
}

// Load reads the ranking comp of the definition def for event, for the
// components that build on it (the celebration), inside their own View or
// Change. The ranking of a sensitive measure is hidden; a switched-off
// measure ranks nobody. Whether the ranking itself is switched on is the
// caller's to check.
func Load(ctx context.Context, q *store.Queries, event id.ID, def *definition.Definition, comp definition.Component) (Result, error) {
	r, err := rulesOf(def, comp)
	if err != nil {
		return Result{}, err
	}
	if r.computed && r.sensitive {
		return Result{Entries: []Entry{}, Hidden: true}, nil
	}
	on, err := r.sourceEnabled(ctx, q, event)
	if err != nil {
		return Result{}, err
	}
	if !on {
		return Result{Entries: []Entry{}}, nil
	}
	entries, err := r.values(ctx, q, event, comp.Key)
	if err != nil {
		return Result{}, err
	}
	return Result{Entries: entries}, nil
}

// visible reports whether the caller may see a computed ranking: always,
// except for a sensitive measure, whose order only those who see every
// value may see (managers, when the organizer enters the values).
func (r rules) visible(sc *component.Scope) bool {
	return !r.computed || !r.sensitive || (r.byOrganizer && sc.IsManager())
}

// writable reports whether the event, the tayfa and the window take a
// value now, apart from the caller.
func writable(sc *component.Scope) bool {
	return sc.Enabled && sc.Started() && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// Get returns the ranking. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Ranking, error) {
	var out Ranking
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Ranking{}, err
	}
	return out, nil
}

// read builds the ranking as the caller sees it, inside View or Change.
func read(ctx context.Context, sc *component.Scope) (Ranking, error) {
	r, err := rulesOf(sc.Definition, sc.Component)
	if err != nil {
		return Ranking{}, err
	}
	out := Ranking{
		Metric: r.metric, Direction: r.direction, Unit: r.unit, Decimals: int32(r.decimals), //nolint:gosec // 0..3
		Entries: []Entry{},
	}
	if r.computed {
		out.Source = r.source.Key
	} else {
		out.CallerCanEdit = writable(sc) && sc.AuthorizeReferee(ctx, id.ID{}) == nil
	}
	out.MeasureEnabled, err = r.sourceEnabled(ctx, sc.Q(), sc.Event)
	if err != nil {
		return Ranking{}, err
	}
	if !out.MeasureEnabled {
		return out, nil
	}
	if !r.visible(sc) {
		out.Hidden = true
		return out, nil
	}
	out.Entries, err = r.values(ctx, sc.Q(), sc.Event, sc.Key)
	if err != nil {
		return Ranking{}, err
	}
	return out, nil
}

// SetEntry writes user's value of a ranking entered by hand. Managers and
// assigned referees only. Refused: a value the metric does not take
// (validation error on "value"), a ranking computed from a measure
// (apperr.ErrRankingFromMeasure), an event that has not started
// (apperr.ErrRankingNotStarted), someone who is not a participant
// (apperr.ErrRankingNotParticipant) and the refusals of
// component.Service.Change. The same value again changes nothing.
func (s *Service) SetEntry(ctx context.Context, ref component.Ref, user id.ID, value float64) (Ranking, error) {
	if user == (id.ID{}) {
		return Ranking{}, apperr.Validation("user_id")
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Ranking{}, apperr.Validation("value")
	}
	var out Ranking
	err := s.fw.Change(ctx, ref, Kind, component.ByReferee(id.ID{}), func(sc *component.Scope) error {
		r, err := rulesOf(sc.Definition, sc.Component)
		if err != nil {
			return err
		}
		if r.computed {
			return apperr.ErrRankingFromMeasure
		}
		if !sc.Started() {
			return apperr.ErrRankingNotStarted
		}
		member, err := sc.ActiveMember(ctx, user)
		if err != nil {
			return err
		}
		going, attended, err := sc.Participation(ctx, user)
		if err != nil {
			return err
		}
		if !member || (!going && !attended) {
			return apperr.ErrRankingNotParticipant
		}
		v, ok := r.clean(value)
		if !ok {
			return apperr.Validation("value")
		}
		prev, err := sc.Q().GetEventRankingEntry(ctx, store.GetEventRankingEntryParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: user.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("derece: %w", err)
		}
		if err != nil || prev.Value != v {
			if err := sc.Q().SetEventRankingEntry(ctx, store.SetEventRankingEntryParams{
				ID: id.New().PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: user.PG(), Value: v,
				EnteredBy: sc.User.PG(), Now: ts(sc.Now),
			}); err != nil {
				return fmt.Errorf("dereceyi yazma: %w", err)
			}
			sc.Changed()
			if err := sc.Audit(ctx, AuditSet, map[string]any{"user_id": user.String()}); err != nil {
				return err
			}
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Ranking{}, err
	}
	return out, nil
}

// RemoveEntry removes user's value of a ranking entered by hand; who may
// remove it is who may write it, without the start and participant checks.
// Removing a value that is not there is not an error.
func (s *Service) RemoveEntry(ctx context.Context, ref component.Ref, user id.ID) (Ranking, error) {
	if user == (id.ID{}) {
		return Ranking{}, apperr.Validation("user_id")
	}
	var out Ranking
	err := s.fw.Change(ctx, ref, Kind, component.ByReferee(id.ID{}), func(sc *component.Scope) error {
		r, err := rulesOf(sc.Definition, sc.Component)
		if err != nil {
			return err
		}
		if r.computed {
			return apperr.ErrRankingFromMeasure
		}
		n, err := sc.Q().DeleteEventRankingEntry(ctx, store.DeleteEventRankingEntryParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: user.PG()})
		if err != nil {
			return fmt.Errorf("dereceyi kaldırma: %w", err)
		}
		if n > 0 {
			sc.Changed()
			if err := sc.Audit(ctx, AuditRemoved, map[string]any{"user_id": user.String()}); err != nil {
				return err
			}
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Ranking{}, err
	}
	return out, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
