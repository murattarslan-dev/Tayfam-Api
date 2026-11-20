package home

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
)

// Periods of a display widget.
const (
	// PeriodThisYear is the current Europe/Istanbul calendar year.
	PeriodThisYear = "this_year"
	// PeriodAllTime is every year (stats, log_summary).
	PeriodAllTime = "all_time"
	// PeriodLast30Days is the last 30 days (champions).
	PeriodLast30Days = "last_30_days"
)

// Bounds of the settings (ADR-0027 §4).
const (
	// MaxStatsMetrics bounds the metrics of the statistics card.
	MaxStatsMetrics = 4
	// MaxCountdownTitle bounds the countdown's title, in characters.
	MaxCountdownTitle = 60
	// MaxChampionTypes bounds the event types of the champions card.
	MaxChampionTypes = 10
	// MaxGoalTarget bounds a goal's target value.
	MaxGoalTarget = 1e9
	// MinYear and MaxYear bound a goal's year and a countdown's date.
	MinYear int32 = 2000
	MaxYear int32 = 2999
)

// FieldConfig is the validation field of a widget's settings.
const FieldConfig = "config"

// metricPattern is the form of a statistics metric (tayfa_stats.metric_key):
// a common metric, "<type>.<metric>" or "<type>.<log>.<stat>".
var metricPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}(\.[a-z][a-z0-9_]{1,39}(\.[a-z][a-z0-9_]{1,48})?)?$`)

// commonMetrics are the metrics without a type (ADR-0026 §2).
var commonMetrics = []string{catalog.MetricEvents, catalog.MetricAttended, catalog.MetricParticipants, catalog.MetricDuration}

// StatsConfig is the statistics card's settings: up to four metrics of
// the tayfa's statistics, of this year or of all time. Without settings
// the card shows the four common metrics of this year.
type StatsConfig struct {
	Metrics []string `json:"metric_keys"`
	Period  string   `json:"period"`
}

// GoalConfig is the yearly goal: a metric of the tayfa's statistics (a
// measure's total, for example) and its target. Year 0 is always the
// current year. The value fills from the statistics; there is no manual
// increase.
type GoalConfig struct {
	Metric string  `json:"metric_key"`
	Target float64 `json:"target"`
	Year   int32   `json:"year,omitempty"`
}

// CountdownConfig is the countdown: a title and the moment it counts to.
// Once the moment passed the card is not shown.
type CountdownConfig struct {
	Title string    `json:"title"`
	At    time.Time `json:"at"`
}

// ChampionsConfig is the champions card: the announced winners of the
// tayfa's events of these types (none: every type) in the period
// (PeriodLast30Days or PeriodThisYear).
type ChampionsConfig struct {
	EventTypes []string `json:"event_types"`
	Period     string   `json:"period"`
}

// LogSummaryConfig is a log statistic of the tayfa ("Bu yıl görülen 84 kuş
// türü"): a "<type>.<log>.<stat>" metric, of this year or of all time.
type LogSummaryConfig struct {
	Metric string `json:"metric_key"`
	Period string `json:"period"`
}

// Config is a widget's settings: at most the field of the widget's own key
// is set.
type Config struct {
	Stats      *StatsConfig
	Goal       *GoalConfig
	Countdown  *CountdownConfig
	Champions  *ChampionsConfig
	LogSummary *LogSummaryConfig
}

// empty reports whether no field is set.
func (c Config) empty() bool {
	return c.Stats == nil && c.Goal == nil && c.Countdown == nil && c.Champions == nil && c.LogSummary == nil
}

// only reports whether no field but key's is set.
func (c Config) only(key string) bool {
	set := map[string]bool{
		KeyStats: c.Stats != nil, KeyGoal: c.Goal != nil, KeyCountdown: c.Countdown != nil,
		KeyChampions: c.Champions != nil, KeyLogSummary: c.LogSummary != nil,
	}
	for k, on := range set {
		if on && k != key {
			return false
		}
	}
	return true
}

// withDefaults fills the settings a widget may go without: the statistics
// card's metrics and period, the champions card's period.
func withDefaults(key string, c Config) Config {
	switch key {
	case KeyStats:
		if c.Stats == nil {
			c.Stats = &StatsConfig{Metrics: slices.Clone(commonMetrics), Period: PeriodThisYear}
		}
	case KeyChampions:
		if c.Champions == nil {
			c.Champions = &ChampionsConfig{EventTypes: []string{}, Period: PeriodThisYear}
		}
	}
	return c
}

// checkTerms is the one place a home widget's free text (the countdown's
// title) meets the abuse term check (ADR-0027 §4, ADR-0012). The term list
// and its check are not in core yet; when they come, the title is checked
// here and nowhere else.
func checkTerms(string) error { return nil }

// cleanConfig checks the settings c of the widget w and returns them
// cleaned. An enabled widget that needs settings must have them; a widget
// without settings takes none.
func cleanConfig(w Widget, enabled bool, c Config) (Config, error) {
	bad := apperr.Validation(FieldConfig)
	if !c.only(w.Key) {
		return Config{}, bad
	}
	var err error
	switch w.Key {
	case KeyStats:
		if c.Stats != nil {
			c.Stats, err = cleanStats(*c.Stats)
		}
	case KeyGoal:
		if c.Goal != nil {
			c.Goal, err = cleanGoal(*c.Goal)
		}
	case KeyCountdown:
		if c.Countdown != nil {
			c.Countdown, err = cleanCountdown(*c.Countdown)
		}
	case KeyChampions:
		if c.Champions != nil {
			c.Champions, err = cleanChampions(*c.Champions)
		}
	case KeyLogSummary:
		if c.LogSummary != nil {
			c.LogSummary, err = cleanLogSummary(*c.LogSummary)
		}
	default:
		if !c.empty() {
			return Config{}, bad
		}
	}
	if err != nil {
		return Config{}, err
	}
	if enabled && w.NeedsConfig && c.empty() {
		return Config{}, bad
	}
	return c, nil
}

// validMetric reports whether m has the form of a statistics metric; a
// metric without a type must be a common one. Whether the metric exists
// is not asked: a metric nothing counted yet reads 0, and nothing
// sensitive is ever counted (ADR-0037 §7).
func validMetric(m string) bool {
	if !metricPattern.MatchString(m) {
		return false
	}
	return strings.Contains(m, ".") || slices.Contains(commonMetrics, m)
}

func validPeriod(p string, allowed ...string) bool { return slices.Contains(allowed, p) }

func cleanStats(c StatsConfig) (*StatsConfig, error) {
	bad := apperr.Validation(FieldConfig)
	if len(c.Metrics) == 0 || len(c.Metrics) > MaxStatsMetrics || !validPeriod(c.Period, PeriodThisYear, PeriodAllTime) {
		return nil, bad
	}
	out := StatsConfig{Metrics: make([]string, 0, len(c.Metrics)), Period: c.Period}
	for _, m := range c.Metrics {
		if !validMetric(m) || slices.Contains(out.Metrics, m) {
			return nil, bad
		}
		out.Metrics = append(out.Metrics, m)
	}
	return &out, nil
}

func cleanGoal(c GoalConfig) (*GoalConfig, error) {
	bad := apperr.Validation(FieldConfig)
	if !validMetric(c.Metric) || math.IsNaN(c.Target) || math.IsInf(c.Target, 0) || c.Target <= 0 || c.Target > MaxGoalTarget {
		return nil, bad
	}
	if c.Year != 0 && (c.Year < MinYear || c.Year > MaxYear) {
		return nil, bad
	}
	return &c, nil
}

func cleanCountdown(c CountdownConfig) (*CountdownConfig, error) {
	bad := apperr.Validation(FieldConfig)
	if len(c.Title) > (MaxCountdownTitle+16)*4 {
		return nil, bad
	}
	title := strings.TrimSpace(c.Title)
	if title == "" || !utf8.ValidString(title) || utf8.RuneCountInString(title) > MaxCountdownTitle {
		return nil, bad
	}
	for _, r := range title {
		if unicode.IsControl(r) {
			return nil, bad
		}
	}
	if err := checkTerms(title); err != nil {
		return nil, err
	}
	at := c.At.UTC().Truncate(time.Second)
	if y := int32(at.Year()); y < MinYear || y > MaxYear { //nolint:gosec // a year fits
		return nil, bad
	}
	return &CountdownConfig{Title: title, At: at}, nil
}

func cleanChampions(c ChampionsConfig) (*ChampionsConfig, error) {
	bad := apperr.Validation(FieldConfig)
	if len(c.EventTypes) > MaxChampionTypes || !validPeriod(c.Period, PeriodLast30Days, PeriodThisYear) {
		return nil, bad
	}
	out := ChampionsConfig{EventTypes: make([]string, 0, len(c.EventTypes)), Period: c.Period}
	for _, t := range c.EventTypes {
		if !definition.ValidKey(t) || slices.Contains(out.EventTypes, t) {
			return nil, bad
		}
		out.EventTypes = append(out.EventTypes, t)
	}
	return &out, nil
}

func cleanLogSummary(c LogSummaryConfig) (*LogSummaryConfig, error) {
	if !validMetric(c.Metric) || strings.Count(c.Metric, ".") != 2 || !validPeriod(c.Period, PeriodThisYear, PeriodAllTime) {
		return nil, apperr.Validation(FieldConfig)
	}
	return &c, nil
}

// encodeConfig is the stored form of key's settings (tayfa_home_widgets.
// config); nil when there are none.
func encodeConfig(key string, c Config) ([]byte, error) {
	var v any
	switch {
	case key == KeyStats && c.Stats != nil:
		v = c.Stats
	case key == KeyGoal && c.Goal != nil:
		v = c.Goal
	case key == KeyCountdown && c.Countdown != nil:
		v = c.Countdown
	case key == KeyChampions && c.Champions != nil:
		v = c.Champions
	case key == KeyLogSummary && c.LogSummary != nil:
		v = c.LogSummary
	default:
		return nil, nil
	}
	return json.Marshal(v)
}

// decodeConfig reads key's stored settings and checks them again, as if
// they were new: a row that no longer passes reads as having none.
func decodeConfig(w Widget, raw []byte) (Config, bool) {
	var c Config
	if len(raw) == 0 {
		return c, true
	}
	var err error
	switch w.Key {
	case KeyStats:
		c.Stats = &StatsConfig{}
		err = strict(raw, c.Stats)
	case KeyGoal:
		c.Goal = &GoalConfig{}
		err = strict(raw, c.Goal)
	case KeyCountdown:
		c.Countdown = &CountdownConfig{}
		err = strict(raw, c.Countdown)
	case KeyChampions:
		c.Champions = &ChampionsConfig{}
		err = strict(raw, c.Champions)
	case KeyLogSummary:
		c.LogSummary = &LogSummaryConfig{}
		err = strict(raw, c.LogSummary)
	default:
		return Config{}, false
	}
	if err != nil {
		return Config{}, false
	}
	c, err = cleanConfig(w, false, c)
	return c, err == nil
}

// strict decodes one JSON object into v, refusing unknown keys.
func strict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("fazladan içerik")
	}
	return nil
}
