package rest

import (
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// Badges (issue #258; ADR-0039): the definitions come with the catalog,
// the earned ones with the profile and the tayfa.

var (
	badgeScopes = newEnum(map[string]corev1.BadgeScope{
		"user": corev1.BadgeScope_BADGE_SCOPE_USER, "tayfa": corev1.BadgeScope_BADGE_SCOPE_TAYFA,
	})
	badgeRuleKinds = newEnum(map[string]corev1.BadgeRuleKind{
		"threshold": corev1.BadgeRuleKind_BADGE_RULE_KIND_THRESHOLD,
		"first":     corev1.BadgeRuleKind_BADGE_RULE_KIND_FIRST,
		"champion":  corev1.BadgeRuleKind_BADGE_RULE_KIND_CHAMPION,
	})
	badgePeriods = newEnum(map[string]corev1.BadgePeriod{
		"all_time": corev1.BadgePeriod_BADGE_PERIOD_ALL_TIME, "year": corev1.BadgePeriod_BADGE_PERIOD_YEAR,
	})
)

// catalogBadgeRuleJSON is a badge's rule; metric, value and period are
// only a threshold's, event_type only first's and champion's.
type catalogBadgeRuleJSON struct {
	Kind      string   `json:"kind"`
	Metric    string   `json:"metric,omitempty"`
	Value     *float64 `json:"value,omitempty"`
	Period    string   `json:"period,omitempty"`
	EventType string   `json:"event_type,omitempty"`
}

type catalogBadgeJSON struct {
	Key      string               `json:"key"`
	Scope    string               `json:"scope"`
	Position int32                `json:"position"`
	Labels   map[string]string    `json:"labels"`
	Icon     string               `json:"icon"`
	Status   string               `json:"status"`
	Rule     catalogBadgeRuleJSON `json:"rule"`
}

func catalogBadgeFrom(b *corev1.CatalogBadge) catalogBadgeJSON {
	r := b.GetRule()
	rule := catalogBadgeRuleJSON{Kind: badgeRuleKinds.name(r.GetKind()), EventType: r.GetEventType()}
	if r.GetKind() == corev1.BadgeRuleKind_BADGE_RULE_KIND_THRESHOLD {
		v := r.GetValue()
		rule.Metric, rule.Value, rule.Period = r.GetMetric(), &v, badgePeriods.name(r.GetPeriod())
	}
	return catalogBadgeJSON{
		Key: b.GetKey(), Scope: badgeScopes.name(b.GetScope()), Position: b.GetPosition(), Labels: nonNilMap(b.GetLabels()),
		Icon: b.GetIcon(), Status: catalogStatuses.name(b.GetStatus()), Rule: rule,
	}
}

// earnedBadgeJSON is an earned badge; its name and icon are the catalog's.
type earnedBadgeJSON struct {
	BadgeKey string `json:"badge_key"`
	// Only a yearly badge's.
	Year int32 `json:"year,omitempty"`
	// Only a champion badge's.
	TayfaID  string    `json:"tayfa_id,omitempty"`
	EventID  string    `json:"event_id,omitempty"`
	EarnedAt time.Time `json:"earned_at"`
}

func earnedBadgesFrom(in []*corev1.EarnedBadge) []earnedBadgeJSON {
	out := make([]earnedBadgeJSON, 0, len(in))
	for _, b := range in {
		out = append(out, earnedBadgeJSON{
			BadgeKey: b.GetBadgeKey(), Year: b.GetYear(), TayfaID: b.GetTayfaId(), EventID: b.GetEventId(),
			EarnedAt: b.GetEarnedAt().AsTime(),
		})
	}
	return out
}
