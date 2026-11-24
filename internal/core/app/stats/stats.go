// Package stats computes and reads the tayfa and personal statistics
// (issue #266; ADR-0026 §1–§2, ADR-0037 §7, ADR-0039 §2).
//
// Source: frozen events only — ended component.FreezeAfter ago, not
// cancelled, not deleted — so that a number never moves once it is shown.
// The tables (tayfa_stats, user_stats) are derived: Recompute deletes a
// tayfa's rows and writes them again from the component tables, which is
// idempotent. A year is the Europe/Istanbul year of the event's start;
// year 0 (AllTime) holds the all-time rows, computed directly so that a
// distinct count (participants, a log's distinct values) is right across
// years.
//
// What is counted (Compute): the common metrics (events, attended,
// participants, duration; overall and "<type>.<metric>"), the stored values
// of measures ("<type>.<measure>"; a suggestion is never stored so never
// counts) and the log statistics ("<type>.<log>.<stat>"; a vocabulary
// field counts the merge target). Nothing sensitive is kept: a sensitive
// measure is in no row, an event whose attendance is sensitive adds to the
// tayfa's events and attended only. A person's row is per tayfa (their
// contribution there); the profile total is the sum over tayfas (a
// vocabulary is the tayfa's, so distinct values do not merge across
// tayfas).
//
// When (the stats job, package jobs): an event that froze, or that was
// deleted or cancelled after it counted, queues its tayfa
// (event_stat_runs, tayfa_stat_queue); a vocabulary merge or its undo
// queues its tayfa through the outbox (Consumer). The job recomputes the
// queued tayfas, the whole tayfa each time (the event years in the merge
// event's payload are a subset of it). At the end, in the same
// transaction, the threshold and first badges are given (ADR-0039 §2):
// the tayfa's from its totals without sensitive events, each person's
// from their totals over every tayfa. Badges are never taken back.
//
// Reading: members of the tayfa read its totals and their own
// contributions, never anyone else's (Service.Tayfa; TayfaStatsService;
// issue #322). The showcase and
// the profile (#267) and the home cards (#268) read with TayfaYears,
// TayfaTotals and UserTotals; who may see what is theirs to decide
// (ADR-0026 §3–§4). Values are never logged.
package stats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/badge"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/vocabulary"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// Bounds of a year (tayfa_stats_year).
const (
	MinYear int32 = 2000
	MaxYear int32 = 2999
)

// ValidYear reports whether y is AllTime or a year the tables take.
func ValidYear(y int32) bool { return y == AllTime || (y >= MinYear && y <= MaxYear) }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// ---------------------------------------------------------------------------
// Queue

// Queue asks for the tayfa to be recomputed; an unknown tayfa is skipped.
func Queue(ctx context.Context, q *store.Queries, tayfa id.ID, now time.Time) error {
	if err := q.QueueTayfaStats(ctx, store.QueueTayfaStatsParams{TayfaID: tayfa.PG(), QueuedAt: ts(now)}); err != nil {
		return fmt.Errorf("istatistik kuyruğu: %w", err)
	}
	return nil
}

// ConsumerName is the statistics' position in core's event_consumers.
const ConsumerName = "core.stats"

// Consumer is the outbox consumer that queues a tayfa whose vocabulary
// merge or undo changed what its log statistics count (ADR-0037 §4). It
// writes to core's own database; delivering twice queues once.
func Consumer(pool *pgxpool.Pool, now func() time.Time) outbox.Consumer {
	return outbox.Consumer{
		Name:  ConsumerName,
		Types: []string{vocabulary.TypeMerged, vocabulary.TypeUnmerged},
		Deliver: func(ctx context.Context, e outbox.Event) error {
			return QueueEvent(ctx, store.New(pool), e, now().UTC())
		},
	}
}

// QueueEvent queues the tayfa a vocabulary event names. An event without
// a valid tayfa_id is skipped: retrying would never deliver it.
func QueueEvent(ctx context.Context, q *store.Queries, e outbox.Event, now time.Time) error {
	var p struct {
		TayfaID string `json:"tayfa_id"`
	}
	if json.Unmarshal(e.Payload, &p) != nil {
		return nil
	}
	tayfa, ok := id.Parse(p.TayfaID)
	if !ok {
		return nil
	}
	return Queue(ctx, q, tayfa, now)
}

// ---------------------------------------------------------------------------
// Recompute

type typeVersion struct {
	key     string
	version int32
}

// load reads the tayfa's frozen data.
func load(ctx context.Context, q *store.Queries, tayfa id.ID, frozenBefore time.Time) (Input, error) {
	var in Input
	evs, err := q.ListStatEvents(ctx, store.ListStatEventsParams{TayfaID: tayfa.PG(), FrozenBefore: ts(frozenBefore)})
	if err != nil {
		return in, fmt.Errorf("istatistik etkinlikleri: %w", err)
	}
	if len(evs) == 0 {
		return in, nil
	}
	defs := map[typeVersion]*definition.Definition{}
	index := make(map[id.ID]int, len(evs))
	in.Events = make([]Event, 0, len(evs))
	for _, r := range evs {
		tv := typeVersion{r.Type, r.TypeVersion}
		def, ok := defs[tv]
		if !ok {
			t, err := catalog.TypeVersion(ctx, q, r.Type, r.TypeVersion)
			switch {
			case err == nil:
				def = t.Parsed
			case errors.Is(err, apperr.ErrCatalogTypeNotFound):
				// No definition: only the common metrics count.
			default:
				return in, err
			}
			defs[tv] = def
		}
		ev := id.ID(r.ID.Bytes)
		index[ev] = len(in.Events)
		in.Events = append(in.Events, Event{
			ID: ev, Type: r.Type, Year: r.Year, Minutes: max(r.Minutes, 0), Sensitive: r.Sensitive, Def: def,
		})
	}
	choices, err := q.ListStatComponentChoices(ctx, store.ListStatComponentChoicesParams{TayfaID: tayfa.PG(), FrozenBefore: ts(frozenBefore)})
	if err != nil {
		return in, fmt.Errorf("bileşen seçimleri: %w", err)
	}
	for _, c := range choices {
		if i, ok := index[id.ID(c.EventID.Bytes)]; ok {
			if in.Events[i].Choices == nil {
				in.Events[i].Choices = map[string]bool{}
			}
			in.Events[i].Choices[c.ComponentKey] = c.Enabled
		}
	}
	att, err := q.ListStatAttendance(ctx, store.ListStatAttendanceParams{TayfaID: tayfa.PG(), FrozenBefore: ts(frozenBefore)})
	if err != nil {
		return in, fmt.Errorf("katılımlar: %w", err)
	}
	for _, a := range att {
		if i, ok := index[id.ID(a.EventID.Bytes)]; ok {
			in.Events[i].Attended = append(in.Events[i].Attended, id.ID(a.UserID.Bytes))
		}
	}
	ms, err := q.ListStatMeasurements(ctx, store.ListStatMeasurementsParams{TayfaID: tayfa.PG(), FrozenBefore: ts(frozenBefore)})
	if err != nil {
		return in, fmt.Errorf("ölçümler: %w", err)
	}
	for _, m := range ms {
		var u id.ID
		if m.UserID.Valid {
			u = id.ID(m.UserID.Bytes)
		}
		in.Measurements = append(in.Measurements, Measurement{Event: id.ID(m.EventID.Bytes), Key: m.ComponentKey, User: u, Value: m.Value})
	}
	ls, err := q.ListStatLogEntries(ctx, store.ListStatLogEntriesParams{TayfaID: tayfa.PG(), FrozenBefore: ts(frozenBefore)})
	if err != nil {
		return in, fmt.Errorf("günlük kayıtları: %w", err)
	}
	for _, l := range ls {
		e := LogEntry{Event: id.ID(l.EventID.Bytes), Key: l.ComponentKey, User: id.ID(l.UserID.Bytes)}
		if err := json.Unmarshal(l.FieldValues, &e.Values); err != nil {
			return in, fmt.Errorf("günlük kaydının değerleri: %w", err)
		}
		var voc map[string]string
		if err := json.Unmarshal(l.Vocabulary, &voc); err != nil {
			return in, fmt.Errorf("günlük kaydının sözlük değerleri: %w", err)
		}
		e.Vocabulary = make(map[string]id.ID, len(voc))
		for k, v := range voc {
			if vid, ok := id.Parse(v); ok {
				e.Vocabulary[k] = vid
			}
		}
		in.Logs = append(in.Logs, e)
	}
	return in, nil
}

// Recompute computes the tayfa's statistics from its frozen events as of
// now, replaces its rows, and gives the threshold and first badges of
// badges (from badge.Usable) that the tayfa and its people reach, all in
// the caller's transaction (q). It is idempotent.
func Recompute(ctx context.Context, q *store.Queries, tayfa id.ID, now time.Time, badges []catalog.Badge) error {
	in, err := load(ctx, q, tayfa, now.Add(-component.FreezeAfter))
	if err != nil {
		return err
	}
	res := Compute(in)
	if err := q.DeleteTayfaStats(ctx, tayfa.PG()); err != nil {
		return fmt.Errorf("eski tayfa istatistiği: %w", err)
	}
	if err := q.DeleteTayfaUserStats(ctx, tayfa.PG()); err != nil {
		return fmt.Errorf("eski kişisel istatistik: %w", err)
	}
	if len(res.Tayfa) > 0 {
		p := store.InsertTayfaStatsParams{TayfaID: tayfa.PG(), ComputedAt: ts(now)}
		for _, r := range res.Tayfa {
			p.Years = append(p.Years, r.Year)
			p.MetricKeys = append(p.MetricKeys, r.Metric)
			p.Sources = append(p.Sources, r.Source)
			p.Vals = append(p.Vals, r.Value)
			p.EventCounts = append(p.EventCounts, r.Events)
		}
		if err := q.InsertTayfaStats(ctx, p); err != nil {
			return fmt.Errorf("tayfa istatistiği: %w", err)
		}
	}
	var people []id.ID
	if len(res.Users) > 0 {
		p := store.InsertUserStatsParams{TayfaID: tayfa.PG(), ComputedAt: ts(now)}
		for _, r := range res.Users {
			p.UserIds = append(p.UserIds, r.User.PG())
			p.Years = append(p.Years, r.Year)
			p.MetricKeys = append(p.MetricKeys, r.Metric)
			p.Sources = append(p.Sources, r.Source)
			p.Vals = append(p.Vals, r.Value)
			p.EventCounts = append(p.EventCounts, r.Events)
			if !slices.Contains(people, r.User) {
				people = append(people, r.User)
			}
		}
		if err := q.InsertUserStats(ctx, p); err != nil {
			return fmt.Errorf("kişisel istatistik: %w", err)
		}
	}
	return grant(ctx, q, tayfa, people, res.Badge, now, badges)
}

// grant gives the badges the tayfa and the people reach now.
func grant(ctx context.Context, q *store.Queries, tayfa id.ID, people []id.ID, totals map[int32]map[string]float64, now time.Time, badges []catalog.Badge) error {
	if aw := due(badges, catalog.BadgeScopeTayfa, totals); len(aw) > 0 {
		if _, err := badge.GrantTayfa(ctx, q, tayfa, aw, now); err != nil {
			return err
		}
	}
	if !hasScope(badges, catalog.BadgeScopeUser) {
		return nil
	}
	for _, u := range people {
		sums, err := q.ListUserStatSums(ctx, u.PG())
		if err != nil {
			return fmt.Errorf("kişinin toplamları: %w", err)
		}
		rows := make([]Row, 0, len(sums))
		for _, s := range sums {
			rows = append(rows, Row{Year: s.Year, Metric: s.MetricKey, Value: s.Value})
		}
		if aw := due(badges, catalog.BadgeScopeUser, Totals(rows)); len(aw) > 0 {
			if _, err := badge.GrantUser(ctx, q, u, aw, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasScope(badges []catalog.Badge, scope string) bool {
	return slices.ContainsFunc(badges, func(b catalog.Badge) bool { return b.Scope == scope })
}

// due returns the awards of every year in totals, once each.
func due(badges []catalog.Badge, scope string, totals map[int32]map[string]float64) []badge.Award {
	if !hasScope(badges, scope) || len(totals) == 0 {
		return nil
	}
	years := []int32{}
	for y := range totals {
		if y != AllTime {
			years = append(years, y)
		}
	}
	slices.Sort(years)
	if len(years) == 0 {
		years = []int32{AllTime}
	}
	var out []badge.Award
	for _, y := range years {
		for _, a := range badge.Due(badges, badge.Totals{Scope: scope, AllTime: totals[AllTime], Year: y, YearTotals: totals[y]}) {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Reading

// Stat is one metric's value: a tayfa's total, or a person's total over
// their tayfas.
type Stat struct {
	Metric string
	Source string
	Value  float64
	// Events is how many events made the value.
	Events int32
}

// TayfaYears returns the years the tayfa has statistics for, newest first.
func TayfaYears(ctx context.Context, q *store.Queries, tayfa id.ID) ([]int32, error) {
	ys, err := q.ListTayfaStatYears(ctx, tayfa.PG())
	if err != nil {
		return nil, fmt.Errorf("istatistik yılları: %w", err)
	}
	return ys, nil
}

// TayfaTotals returns the tayfa's totals of a year (AllTime: all time) and
// when they were computed (zero when there are none). The number of
// frozen events the showcase threshold looks at (ADR-0026 §3) is the
// all-time catalog.MetricEvents.
func TayfaTotals(ctx context.Context, q *store.Queries, tayfa id.ID, year int32) ([]Stat, time.Time, error) {
	rows, err := q.ListTayfaStats(ctx, store.ListTayfaStatsParams{TayfaID: tayfa.PG(), Year: year})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("tayfa istatistiği: %w", err)
	}
	out := make([]Stat, 0, len(rows))
	var at time.Time
	for _, r := range rows {
		out = append(out, Stat{Metric: r.MetricKey, Source: r.Source, Value: r.Value, Events: r.EventCount})
		if t := r.ComputedAt.Time.UTC(); t.After(at) {
			at = t
		}
	}
	return out, at, nil
}

// UserTotals returns the person's totals of a year (AllTime: all time),
// summed over their tayfas. Whether the reader may see them is the
// caller's to decide (stats_visibility, ADR-0026 §4).
func UserTotals(ctx context.Context, q *store.Queries, user id.ID, year int32) ([]Stat, error) {
	rows, err := q.ListUserStats(ctx, store.ListUserStatsParams{UserID: user.PG(), Year: year})
	if err != nil {
		return nil, fmt.Errorf("kişisel istatistik: %w", err)
	}
	out := make([]Stat, 0, len(rows))
	for _, r := range rows {
		out = append(out, Stat{Metric: r.MetricKey, Source: r.Source, Value: r.Value, Events: r.EventCount})
	}
	return out, nil
}

// Service serves the tayfa's own statistics to its members.
type Service struct {
	fw *component.Service
}

// New returns a Service on the component framework (Service.Tayfa).
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// View is a tayfa's statistics of one year (AllTime: all time) for its
// members.
type View struct {
	Year int32
	// Years are the years with statistics, newest first.
	Years  []int32
	Totals []Stat
	// Own are the reader's own contributions (measure and log metrics) in
	// the tayfa. Nobody else's contribution is ever in a View (platform
	// owner, 2026-11-23; issue #322): not for an admin or a moderator
	// either.
	Own []Stat
	// ComputedAt is zero when nothing was computed yet.
	ComputedAt time.Time
}

// Tayfa returns the tayfa's totals and the reader's own contributions
// (measures and logs; nothing sensitive is stored) for the year; other
// members' contributions are never read (issue #322). Open to
// every active member (authz.TayfaView), muted ones included. Refused:
// anyone but an active member (apperr.ErrTayfaNotFound), a bad year
// (validation error on year).
func (s *Service) Tayfa(ctx context.Context, user, tayfa id.ID, year int32) (View, error) {
	if !ValidYear(year) {
		return View{}, apperr.Validation("year")
	}
	var out View
	err := s.fw.Tayfa(ctx, user, tayfa, false, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.TayfaView); err != nil {
			return err
		}
		q := sc.Q()
		years, err := TayfaYears(ctx, q, tayfa)
		if err != nil {
			return err
		}
		totals, at, err := TayfaTotals(ctx, q, tayfa, year)
		if err != nil {
			return err
		}
		rows, err := q.ListOwnTayfaStatContributions(ctx, store.ListOwnTayfaStatContributionsParams{TayfaID: tayfa.PG(), UserID: user.PG(), Year: year})
		if err != nil {
			return fmt.Errorf("kendi katkıları: %w", err)
		}
		out = View{Year: year, Years: years, Totals: totals, Own: make([]Stat, 0, len(rows)), ComputedAt: at}
		for _, r := range rows {
			out.Own = append(out.Own, Stat{Metric: r.MetricKey, Source: r.Source, Value: r.Value, Events: r.EventCount})
		}
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return out, nil
}
