package stats

import (
	"encoding/json"
	"math"
	"slices"
	"strings"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/vocabulary"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Sources of a metric (tayfa_stats.source, user_stats.source).
const (
	// SourceCommon: events, attended, participants, duration, overall and
	// per type ("<type>.<metric>").
	SourceCommon = "common"
	// SourceMeasure: "<type>.<measure>".
	SourceMeasure = "measure"
	// SourceLog: "<type>.<log>.<stat>".
	SourceLog = "log"
)

// AllTime is the year of the all-time rows.
const AllTime int32 = 0

// Log statistic names: the stat parameter's "count", "distinct:<field>"
// and "sum:<field>" become the metric's last part "count",
// "distinct_<field>" and "sum_<field>" (ADR-0037 §7:
// birding.sightings.distinct_species). Every log also counts its entries
// (StatCount), whether or not its definition lists it: a badge on a log
// (catalog.CheckBadge: "<type>.<log>") counts entries.
const StatCount = "count"

// Event is a frozen event of the tayfa with its type version.
type Event struct {
	ID   id.ID
	Type string
	// Year is the Europe/Istanbul year of the start.
	Year    int32
	Minutes int64
	// Sensitive: the type version's sensitive_attendance (ADR-0037 §7).
	Sensitive bool
	Def       *definition.Definition
	// Choices are the organizer's switches of optional components, by key.
	Choices map[string]bool
	// Attended are the people whose attendance was verified.
	Attended []id.ID
}

// Measurement is a stored value of a measure; User is zero for a total.
type Measurement struct {
	Event id.ID
	Key   string
	User  id.ID
	Value float64
}

// LogEntry is a live log entry. Vocabulary holds the fields bound to the
// tayfa vocabulary, resolved to the merge target.
type LogEntry struct {
	Event      id.ID
	Key        string
	User       id.ID
	Values     map[string]json.RawMessage
	Vocabulary map[string]id.ID
}

// Input is everything the statistics of one tayfa are computed from.
type Input struct {
	Events       []Event
	Measurements []Measurement
	Logs         []LogEntry
}

// Row is one computed statistic: the tayfa's when User is zero, else the
// person's contribution in the tayfa.
type Row struct {
	User   id.ID
	Year   int32
	Metric string
	Source string
	Value  float64
	Events int32
}

// Result is the computed statistics of one tayfa.
type Result struct {
	Tayfa []Row
	Users []Row
	// Badge are the tayfa's totals a tayfa badge may count, by year
	// (AllTime included): those of events whose attendance is not
	// sensitive only (ADR-0039 §2), with the log aliases
	// ("<type>.<log>": entries).
	Badge map[int32]map[string]float64
}

type cellKey struct {
	user   id.ID
	year   int32
	metric string
}

type cell struct {
	source   string
	sum      float64
	events   map[id.ID]struct{}
	distinct map[string]struct{}
}

// acc accumulates cells; every value goes to its year and to AllTime.
type acc map[cellKey]*cell

func (a acc) get(user id.ID, year int32, metric, source string, distinct bool) *cell {
	k := cellKey{user, year, metric}
	c, ok := a[k]
	if !ok {
		c = &cell{source: source, events: map[id.ID]struct{}{}}
		if distinct {
			c.distinct = map[string]struct{}{}
		}
		a[k] = c
	}
	return c
}

func (a acc) add(user id.ID, year int32, metric, source string, ev id.ID, v float64) {
	for _, y := range []int32{year, AllTime} {
		c := a.get(user, y, metric, source, false)
		c.sum += v
		c.events[ev] = struct{}{}
	}
}

func (a acc) addDistinct(user id.ID, year int32, metric, source string, ev id.ID, value string) {
	for _, y := range []int32{year, AllTime} {
		c := a.get(user, y, metric, source, true)
		c.distinct[value] = struct{}{}
		c.events[ev] = struct{}{}
	}
}

// touch makes sure the cell exists (a metric with nothing to count still
// shows zero for the events that had it).
func (a acc) touch(user id.ID, year int32, metric, source string, ev id.ID, distinct bool) {
	for _, y := range []int32{year, AllTime} {
		a.get(user, y, metric, source, distinct).events[ev] = struct{}{}
	}
}

func (a acc) rows() []Row {
	out := make([]Row, 0, len(a))
	for k, c := range a {
		v := math.Round(c.sum*1000) / 1000
		if c.distinct != nil {
			v = float64(len(c.distinct))
		}
		out = append(out, Row{User: k.user, Year: k.year, Metric: k.metric, Source: c.source, Value: v, Events: int32(min(len(c.events), math.MaxInt32))}) //nolint:gosec // bounded
	}
	slices.SortFunc(out, func(x, y Row) int {
		if c := strings.Compare(x.User.String(), y.User.String()); c != 0 {
			return c
		}
		if x.Year != y.Year {
			return int(x.Year - y.Year)
		}
		return strings.Compare(x.Metric, y.Metric)
	})
	return out
}

// sink sends a tayfa value to the tayfa's cells and, unless the event's
// attendance is sensitive, to the badge cells.
type sink struct {
	tayfa, badge acc
	sensitive    bool
}

func (s sink) add(year int32, metric, source string, ev id.ID, v float64) {
	s.tayfa.add(id.ID{}, year, metric, source, ev, v)
	if !s.sensitive {
		s.badge.add(id.ID{}, year, metric, source, ev, v)
	}
}

func (s sink) addDistinct(year int32, metric, source string, ev id.ID, value string) {
	s.tayfa.addDistinct(id.ID{}, year, metric, source, ev, value)
	if !s.sensitive {
		s.badge.addDistinct(id.ID{}, year, metric, source, ev, value)
	}
}

func (s sink) touch(year int32, metric, source string, ev id.ID, distinct bool) {
	s.tayfa.touch(id.ID{}, year, metric, source, ev, distinct)
	if !s.sensitive {
		s.badge.touch(id.ID{}, year, metric, source, ev, distinct)
	}
}

// enabled reports whether a component is on for the event.
func enabled(c definition.Component, choices map[string]bool) bool {
	if c.Optional == nil {
		return true
	}
	if on, ok := choices[c.Key]; ok {
		return on
	}
	return *c.Optional
}

// logStat is one statistic of a log: its metric name, operation and field.
type logStat struct {
	name, op string
	field    definition.Field
}

// logStats returns the log's statistics: always the entry count, then the
// definition's stat list (a repeated count once).
func logStats(c definition.Component) []logStat {
	out := []logStat{{name: StatCount, op: StatCount}}
	stats, _ := c.Params["stat"].Value.([]string)
	for _, st := range stats {
		op, key, ok := strings.Cut(st, ":")
		if !ok || (op != "distinct" && op != "sum") {
			continue
		}
		for _, f := range c.LogFields {
			if f.Key == key {
				out = append(out, logStat{name: op + "_" + key, op: op, field: f})
			}
		}
	}
	return out
}

// distinctValues returns the entry's values of a distinct statistic: the
// vocabulary value it chose, a text normalized like the vocabulary, or the
// chosen options.
func distinctValues(e LogEntry, f definition.Field) []string {
	if f.Vocabulary != "" {
		if v, ok := e.Vocabulary[f.Key]; ok {
			return []string{v.String()}
		}
		return nil
	}
	raw, ok := e.Values[f.Key]
	if !ok {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if f.Kind == definition.KindChoice {
			return []string{s}
		}
		if n := vocabulary.Normalize(s); n != "" {
			return []string{n}
		}
		return nil
	}
	var a []string
	if json.Unmarshal(raw, &a) == nil {
		return a
	}
	return nil
}

// numberValue returns the entry's numeric value of a sum statistic.
func numberValue(e LogEntry, f definition.Field) (float64, bool) {
	raw, ok := e.Values[f.Key]
	if !ok {
		return 0, false
	}
	var v float64
	if json.Unmarshal(raw, &v) != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// Compute computes one tayfa's statistics (ADR-0026 §2, ADR-0037 §7):
//
//   - common metrics, overall and per type ("<type>.<metric>"): events,
//     attended (verified attendances), participants (distinct people) and
//     duration (the events' minutes) for the tayfa; attended and duration
//     for each person;
//   - measures ("<type>.<key>"): a total measure's value, the sum of a
//     personal measure's values for the tayfa and each person's own;
//   - logs ("<type>.<log>.<stat>"): count, distinct and sum over the live
//     entries for the tayfa and each author's own.
//
// A sensitive measure counts nowhere. An event whose attendance is
// sensitive counts only in the tayfa's events and attended (and its total
// measures): no participants, no duration, no personal row, no badge
// total. A switched-off optional component counts nowhere.
func Compute(in Input) Result {
	tayfa, users, badge := acc{}, acc{}, acc{}
	byEvent := make(map[id.ID]*Event, len(in.Events))
	for i := range in.Events {
		byEvent[in.Events[i].ID] = &in.Events[i]
	}
	for i := range in.Events {
		e := &in.Events[i]
		s := sink{tayfa: tayfa, badge: badge, sensitive: e.Sensitive}
		for _, prefix := range []string{"", e.Type + "."} {
			s.add(e.Year, prefix+catalog.MetricEvents, SourceCommon, e.ID, 1)
			s.touch(e.Year, prefix+catalog.MetricAttended, SourceCommon, e.ID, false)
			for range e.Attended {
				s.add(e.Year, prefix+catalog.MetricAttended, SourceCommon, e.ID, 1)
			}
			if e.Sensitive {
				continue
			}
			s.touch(e.Year, prefix+catalog.MetricParticipants, SourceCommon, e.ID, true)
			s.add(e.Year, prefix+catalog.MetricDuration, SourceCommon, e.ID, float64(e.Minutes))
			for _, u := range e.Attended {
				s.addDistinct(e.Year, prefix+catalog.MetricParticipants, SourceCommon, e.ID, u.String())
				users.add(u, e.Year, prefix+catalog.MetricAttended, SourceCommon, e.ID, 1)
				users.add(u, e.Year, prefix+catalog.MetricDuration, SourceCommon, e.ID, float64(e.Minutes))
			}
		}
		if e.Def == nil {
			continue
		}
		for _, c := range e.Def.Components {
			if !enabled(c, e.Choices) {
				continue
			}
			switch c.Kind {
			case definition.CompMeasure:
				if c.Params["sensitive"].Bool() {
					continue
				}
				s.touch(e.Year, e.Type+"."+c.Key, SourceMeasure, e.ID, false)
			case definition.CompLog:
				for _, st := range logStats(c) {
					s.touch(e.Year, e.Type+"."+c.Key+"."+st.name, SourceLog, e.ID, st.op == "distinct")
				}
			}
		}
	}

	for _, m := range in.Measurements {
		e, ok := byEvent[m.Event]
		if !ok || e.Def == nil {
			continue
		}
		c, ok := e.Def.Component(m.Key)
		if !ok || c.Kind != definition.CompMeasure || c.Params["sensitive"].Bool() || !enabled(c, e.Choices) {
			continue
		}
		personal := c.Params["scope"].String() == "personal"
		if personal != (m.User != id.ID{}) {
			continue
		}
		metric := e.Type + "." + c.Key
		s := sink{tayfa: tayfa, badge: badge, sensitive: e.Sensitive}
		s.add(e.Year, metric, SourceMeasure, e.ID, m.Value)
		if personal && !e.Sensitive {
			users.add(m.User, e.Year, metric, SourceMeasure, e.ID, m.Value)
		}
	}

	for _, l := range in.Logs {
		e, ok := byEvent[l.Event]
		if !ok || e.Def == nil {
			continue
		}
		c, ok := e.Def.Component(l.Key)
		if !ok || c.Kind != definition.CompLog || !enabled(c, e.Choices) {
			continue
		}
		s := sink{tayfa: tayfa, badge: badge, sensitive: e.Sensitive}
		personal := !e.Sensitive && l.User != (id.ID{})
		for _, st := range logStats(c) {
			metric := e.Type + "." + c.Key + "." + st.name
			switch st.op {
			case StatCount:
				s.add(e.Year, metric, SourceLog, e.ID, 1)
				if personal {
					users.add(l.User, e.Year, metric, SourceLog, e.ID, 1)
				}
			case "sum":
				if v, ok := numberValue(l, st.field); ok {
					s.add(e.Year, metric, SourceLog, e.ID, v)
					if personal {
						users.add(l.User, e.Year, metric, SourceLog, e.ID, v)
					}
				}
			case "distinct":
				for _, v := range distinctValues(l, st.field) {
					s.addDistinct(e.Year, metric, SourceLog, e.ID, v)
					if personal {
						users.addDistinct(l.User, e.Year, metric, SourceLog, e.ID, v)
					}
				}
			}
		}
	}

	return Result{Tayfa: tayfa.rows(), Users: users.rows(), Badge: Totals(badge.rows())}
}

// Totals turns rows of one subject into badge totals by year, adding the
// log aliases: "<type>.<log>" is the entry count ("<type>.<log>.count").
func Totals(rows []Row) map[int32]map[string]float64 {
	out := map[int32]map[string]float64{}
	for _, r := range rows {
		m, ok := out[r.Year]
		if !ok {
			m = map[string]float64{}
			out[r.Year] = m
		}
		m[r.Metric] += r.Value
		// Only a log metric has three parts.
		if strings.Count(r.Metric, ".") == 2 {
			if base, ok := strings.CutSuffix(r.Metric, "."+StatCount); ok {
				m[base] += r.Value
			}
		}
	}
	return out
}
