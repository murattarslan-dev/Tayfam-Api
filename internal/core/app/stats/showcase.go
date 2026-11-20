package stats

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/badge"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// ShowcaseMinEvents is the fewest frozen, not cancelled events a tayfa
// needs before its statistics card appears in Keşfet (ADR-0026 §3). It is
// code, not configuration: a changed environment must not lower a privacy
// threshold (ADR-0011). Changing it is a product decision.
const ShowcaseMinEvents = 3

// Showcase is what Keşfet shows of a tayfa's statistics to someone who is
// not a member (issue #267; ADR-0026 §3, ADR-0039 §3): the all-time
// totals the showcase may carry and the tayfa's badges. No person, date,
// time, day, district or coordinate; no year breakdown.
type Showcase struct {
	Stats  []Stat
	Badges []badge.Earned
}

// typePolicy is what the showcase may show of one event type, over all
// of its versions (a total spans every version an event was made with).
type typePolicy struct {
	// sensitive: attendance is sensitive in some version (ADR-0037 §7).
	sensitive bool
	// measures: a measure that is showcase and not sensitive in every
	// version that has it.
	measures map[string]bool
}

// policyOf folds the versions of a type into its showcase policy.
func policyOf(defs []*definition.Definition) typePolicy {
	p := typePolicy{measures: map[string]bool{}}
	for _, d := range defs {
		if d == nil {
			continue
		}
		if d.Flags.SensitiveAttendance {
			p.sensitive = true
		}
		for _, c := range d.Components {
			if c.Kind != definition.CompMeasure {
				continue
			}
			ok := c.Params["showcase"].Bool() && !c.Params["sensitive"].Bool()
			if prev, seen := p.measures[c.Key]; seen {
				ok = ok && prev
			}
			p.measures[c.Key] = ok
		}
	}
	return p
}

// commonMetric reports whether m is one of the common metrics.
func commonMetric(m string) bool {
	switch m {
	case catalog.MetricEvents, catalog.MetricAttended, catalog.MetricParticipants, catalog.MetricDuration:
		return true
	}
	return false
}

// showcaseStats keeps the totals the showcase may carry (ADR-0026 §3):
//
//   - the overall common metrics (events, attended, participants,
//     duration);
//   - "<type>.<common metric>" of a type whose attendance is never
//     sensitive;
//   - "<type>.<measure>" when the measure is showcase and not sensitive in
//     every version of a type whose attendance is never sensitive.
//
// Log metrics never appear (the showcase is "the tayfa's total of the
// measures allowed in it and of the common metrics"), nor does a metric of
// an unknown type. An event whose attendance is sensitive still counts in
// the overall events and attended (ADR-0037 §7), which name no type.
func showcaseStats(totals []Stat, policies map[string]typePolicy) []Stat {
	out := make([]Stat, 0, len(totals))
	for _, s := range totals {
		typ, key, typed := strings.Cut(s.Metric, ".")
		if !typed {
			if s.Source == SourceCommon && commonMetric(s.Metric) {
				out = append(out, s)
			}
			continue
		}
		p, ok := policies[typ]
		if !ok || p.sensitive || strings.Contains(key, ".") {
			continue
		}
		switch s.Source {
		case SourceCommon:
			if commonMetric(key) {
				out = append(out, s)
			}
		case SourceMeasure:
			if p.measures[key] {
				out = append(out, s)
			}
		}
	}
	return out
}

// ShowcaseReader reads showcases, keeping the type policies it read for
// the next tayfa (a Keşfet feed has many tayfas of few types). It is for
// one request.
type ShowcaseReader struct {
	q     *store.Queries
	types map[string]typePolicy
}

// NewShowcaseReader returns a reader on q.
func NewShowcaseReader(q *store.Queries) *ShowcaseReader {
	return &ShowcaseReader{q: q, types: map[string]typePolicy{}}
}

// Tayfa returns the tayfa's showcase, or false when there is none: fewer
// than ShowcaseMinEvents frozen events (no statistics computed counts as
// none). Whether the tayfa may be shown at all — in Keşfet, to this
// viewer, with stats_showcase on — is the caller's to decide.
func (r *ShowcaseReader) Tayfa(ctx context.Context, tayfa id.ID) (Showcase, bool, error) {
	totals, _, err := TayfaTotals(ctx, r.q, tayfa, AllTime)
	if err != nil {
		return Showcase{}, false, err
	}
	events := 0.0
	for _, s := range totals {
		if s.Metric == catalog.MetricEvents && s.Source == SourceCommon {
			events = s.Value
		}
	}
	if events < ShowcaseMinEvents {
		return Showcase{}, false, nil
	}
	for _, s := range totals {
		typ, _, typed := strings.Cut(s.Metric, ".")
		if !typed {
			continue
		}
		if _, ok := r.types[typ]; ok {
			continue
		}
		p, err := r.policy(ctx, typ)
		if err != nil {
			return Showcase{}, false, err
		}
		r.types[typ] = p
	}
	policies := make(map[string]typePolicy, len(r.types))
	for k, p := range r.types {
		// An unknown type has no versions: nothing of it is shown.
		if p.measures != nil {
			policies[k] = p
		}
	}
	out := Showcase{Stats: showcaseStats(totals, policies)}
	if out.Badges, err = badge.ForTayfa(ctx, r.q, tayfa); err != nil {
		return Showcase{}, false, err
	}
	return out, true, nil
}

// policy reads every version of the type. A type the catalog does not
// know has the zero policy (measures nil), which shows nothing.
func (r *ShowcaseReader) policy(ctx context.Context, typ string) (typePolicy, error) {
	if !definition.ValidKey(typ) {
		return typePolicy{}, nil
	}
	rows, err := r.q.ListCatalogTypeDefinitions(ctx, typ)
	if err != nil {
		return typePolicy{}, fmt.Errorf("vitrin türü: %w", err)
	}
	if len(rows) == 0 {
		return typePolicy{}, nil
	}
	defs := make([]*definition.Definition, 0, len(rows))
	for _, row := range rows {
		t, err := catalog.TypeVersion(ctx, r.q, typ, row.Version)
		switch {
		case err == nil:
			defs = append(defs, t.Parsed)
		case errors.Is(err, apperr.ErrCatalogTypeNotFound):
			// Cannot happen for a listed version; nothing to add.
		default:
			return typePolicy{}, err
		}
	}
	return policyOf(defs), nil
}
