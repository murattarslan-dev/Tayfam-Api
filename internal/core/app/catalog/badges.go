package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// Badge definitions (issue #258; ADR-0039 §1). A badge is data like the
// rest of the catalog: catalog_badges holds its key (immutable), scope,
// names, icon and rule; a new badge needs no code, a new rule kind does.
// The definitions come with the catalog data (#260) and are managed in the
// platform panel (#262), which checks a definition with CheckBadge before
// it writes it. Earning and showing badges is package badge's.
//
// A rule is a closed schema (ParseBadgeRule):
//
//	threshold  a metric's total reached value; period all_time or year
//	           (metric, value, period)
//	first      the first attendance, of a type or of any (event_type
//	           optional); personal only
//	champion   a celebration named the person, or their team, the winner
//	           (event_type optional); personal only
//
// A metric is a common metric (ADR-0026 §2: CommonMetrics) or
// "<type>.<key>", where key is a common metric of that type or a measure or
// log component of it. Nothing sensitive is a metric (ADR-0039 §2): a
// measure that is sensitive in any version of its type, and every metric
// of a type whose attendance is sensitive in any version, are refused; so
// is a first or champion rule of such a type.

// Badge scopes (catalog_badges.scope).
const (
	BadgeScopeUser  = "user"
	BadgeScopeTayfa = "tayfa"
)

// Badge rule kinds (rule.kind).
const (
	RuleThreshold = "threshold"
	RuleFirst     = "first"
	RuleChampion  = "champion"
)

// Threshold periods (rule.period).
const (
	PeriodAllTime = "all_time"
	PeriodYear    = "year"
)

// Common metrics (ADR-0026 §2): every type has them, "Genel" included. The
// statistics job (#266) computes them under these keys, overall and per
// type ("<type>.<metric>").
const (
	// MetricEvents counts the frozen, not cancelled events (tayfa).
	MetricEvents = "events"
	// MetricAttended counts attendances: a tayfa's total, a person's own.
	MetricAttended = "attended"
	// MetricParticipants counts distinct participants (tayfa).
	MetricParticipants = "participants"
	// MetricDuration sums the events' minutes.
	MetricDuration = "duration"
)

// commonMetrics maps a common metric to whether a personal badge may use
// it; every one is a tayfa metric.
var commonMetrics = map[string]bool{
	MetricEvents:       false,
	MetricAttended:     true,
	MetricParticipants: false,
	MetricDuration:     true,
}

// MaxBadgeValue bounds a threshold.
const MaxBadgeValue = 1e9

// maxBadgeRule bounds the stored rule, as the database does.
const maxBadgeRule = 512

// BadgeRule is a parsed rule. Metric, Value and Period are set for
// threshold; EventType is optional for first and champion.
type BadgeRule struct {
	Kind      string
	Metric    string
	Value     float64
	Period    string
	EventType string
}

// Badge is one badge definition.
type Badge struct {
	Key   string
	Scope string
	// Position orders the badges, ascending.
	Position int32
	// Labels maps a language to the badge's name.
	Labels map[string]string
	Icon   string
	Status string
	Rule   BadgeRule
}

// ParseBadgeRule reads a rule: a JSON object with only the keys of its
// kind. Errors name the place, never the value.
func ParseBadgeRule(raw []byte) (BadgeRule, error) {
	if len(raw) > maxBadgeRule {
		return BadgeRule{}, errors.New("kural: çok uzun")
	}
	var in struct {
		Kind      string   `json:"kind"`
		Metric    *string  `json:"metric"`
		Value     *float64 `json:"value"`
		Period    *string  `json:"period"`
		EventType *string  `json:"event_type"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return BadgeRule{}, errors.New("kural: geçersiz JSON ya da bilinmeyen alan")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return BadgeRule{}, errors.New("kural: fazladan içerik")
	}
	r := BadgeRule{Kind: in.Kind}
	switch in.Kind {
	case RuleThreshold:
		if in.Metric == nil || in.Value == nil || in.Period == nil || in.EventType != nil {
			return BadgeRule{}, errors.New("kural: eşik metric, value ve period ister, event_type almaz")
		}
		r.Metric, r.Value, r.Period = *in.Metric, *in.Value, *in.Period
		if !validMetric(r.Metric) {
			return BadgeRule{}, errors.New("kural: geçersiz metric")
		}
		if math.IsNaN(r.Value) || math.IsInf(r.Value, 0) || r.Value <= 0 || r.Value > MaxBadgeValue {
			return BadgeRule{}, errors.New("kural: value 0'dan büyük ve sınırın altında olmalı")
		}
		if r.Period != PeriodAllTime && r.Period != PeriodYear {
			return BadgeRule{}, errors.New("kural: geçersiz period")
		}
	case RuleFirst, RuleChampion:
		if in.Metric != nil || in.Value != nil || in.Period != nil {
			return BadgeRule{}, errors.New("kural: yalnızca event_type alır")
		}
		if in.EventType != nil {
			if !definition.ValidKey(*in.EventType) {
				return BadgeRule{}, errors.New("kural: geçersiz event_type")
			}
			r.EventType = *in.EventType
		}
	default:
		return BadgeRule{}, errors.New("kural: bilinmeyen kind")
	}
	return r, nil
}

// validMetric reports whether m has the form of a metric: a common metric,
// or "<type>.<key>".
func validMetric(m string) bool {
	typ, key, typed := strings.Cut(m, ".")
	if !typed {
		_, ok := commonMetrics[m]
		return ok
	}
	return definition.ValidKey(typ) && definition.ValidKey(key)
}

// MetricType splits a metric into its type and key; typ is "" for a
// common metric.
func (r BadgeRule) MetricType() (typ, key string) {
	if t, k, ok := strings.Cut(r.Metric, "."); ok {
		return t, k
	}
	return "", r.Metric
}

// RuleType is the event type the rule is about: the metric's type, or the
// event_type of first and champion; "" when none.
func (r BadgeRule) RuleType() string {
	if r.Kind == RuleThreshold {
		typ, _ := r.MetricType()
		return typ
	}
	return r.EventType
}

// Year reports whether earning the rule is per calendar year.
func (r BadgeRule) Year() bool { return r.Kind == RuleThreshold && r.Period == PeriodYear }

// checkBadgeShape checks a definition on its own: keys, names, scope, and
// the rule against its scope.
func checkBadgeShape(b Badge) error {
	if !definition.ValidKey(b.Key) || !definition.ValidKey(b.Icon) {
		return errors.New("geçersiz anahtar ya da ikon")
	}
	if b.Status != StatusActive && b.Status != StatusRetired {
		return errors.New("geçersiz durum")
	}
	if b.Position < 0 || b.Position > 9999 {
		return errors.New("geçersiz sıra")
	}
	if _, ok := b.Labels["tr"]; !ok || len(b.Labels) > definition.MaxLanguages {
		return errors.New("tr adı yok ya da çok fazla dil")
	}
	for lang, text := range b.Labels {
		if len(lang) != 2 || !definition.ValidLabel(text) {
			return errors.New("geçersiz ad")
		}
	}
	switch b.Scope {
	case BadgeScopeUser:
	case BadgeScopeTayfa:
		if b.Rule.Kind != RuleThreshold {
			return errors.New("tayfa rozeti yalnızca eşik kuralıyla verilir")
		}
	default:
		return errors.New("geçersiz kapsam")
	}
	if b.Rule.Kind == RuleThreshold {
		if _, key := b.Rule.MetricType(); isCommon(key) && b.Scope == BadgeScopeUser && !commonMetrics[key] {
			return errors.New("bu ortak metrik kişiye ait değil")
		}
	}
	return nil
}

// Badges reads every badge definition, retired ones included, by position,
// and checks each one's shape (ADR-0011): a row that does not pass fails
// the read. Whether a rule leans on sensitive data is CheckBadge's (at
// write time) and package badge's (before earning).
func Badges(ctx context.Context, q *store.Queries) ([]Badge, error) {
	rows, err := q.ListCatalogBadges(ctx)
	if err != nil {
		return nil, fmt.Errorf("rozetler: %w", err)
	}
	out := make([]Badge, 0, len(rows))
	for _, r := range rows {
		b := Badge{Key: r.Key, Scope: r.Scope, Position: r.Position, Icon: r.Icon, Status: r.Status}
		if err := json.Unmarshal(r.Labels, &b.Labels); err != nil {
			return nil, fmt.Errorf("rozet %q: adlar okunamadı: %w", r.Key, err)
		}
		if b.Rule, err = ParseBadgeRule(compact(r.Rule)); err != nil {
			return nil, fmt.Errorf("rozet %q: %w", r.Key, err)
		}
		if err := checkBadgeShape(b); err != nil {
			return nil, fmt.Errorf("rozet %q: %w", r.Key, err)
		}
		out = append(out, b)
	}
	return out, nil
}

// compact drops jsonb's spaces; the size limit is on the compact form.
func compact(raw []byte) []byte {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return raw
	}
	return b.Bytes()
}

// CheckBadge checks a definition before it is written (the platform panel,
// #262; the catalog data, #260): its shape, and its rule against every
// version of the type it names. The type must exist; a type whose
// attendance is sensitive in any version, and a measure that is sensitive
// in any version, cannot earn a badge (ADR-0039 §2). A personal badge
// counts only personal measures (ADR-0026 §2: user_stats). A log's records
// are the writer's, so a log counts for both. Errors describe the rule,
// never a value.
func CheckBadge(ctx context.Context, q *store.Queries, b Badge) error {
	if err := checkBadgeShape(b); err != nil {
		return err
	}
	typ := b.Rule.RuleType()
	if typ == "" {
		return nil
	}
	rows, err := q.ListCatalogTypeDefinitions(ctx, typ)
	if err != nil {
		return fmt.Errorf("rozetin türü: %w", err)
	}
	if len(rows) == 0 {
		return errors.New("rozetin türü katalogda yok")
	}
	_, key := b.Rule.MetricType()
	// measured: the metric is a component of the type, not a common one.
	measured := b.Rule.Kind == RuleThreshold && !isCommon(key)
	found := !measured
	for _, r := range rows {
		def, err := definition.Parse(compact(r.Definition))
		if err != nil {
			return fmt.Errorf("tür sürümü %d: %w", r.Version, err)
		}
		if def.Flags.SensitiveAttendance {
			return errors.New("katılımı hassas olan türden rozet çıkmaz")
		}
		if !measured {
			continue
		}
		c, ok := def.Component(key)
		if !ok {
			continue
		}
		switch c.Kind {
		case definition.CompMeasure:
			if c.Params["sensitive"].Bool() {
				return errors.New("hassas ölçümden rozet çıkmaz")
			}
			if b.Scope == BadgeScopeUser && c.Params["scope"].String() != "personal" {
				return errors.New("kişisel rozet yalnızca kişisel ölçümü sayar")
			}
			found = true
		case definition.CompLog:
			found = true
		}
	}
	if !found {
		return errors.New("rozetin metriği türde yok")
	}
	return nil
}

func isCommon(key string) bool {
	_, ok := commonMetrics[key]
	return ok
}
