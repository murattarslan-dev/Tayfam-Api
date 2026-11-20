package grpc

import (
	"context"
	"log/slog"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
)

// Catalogs is what CatalogServer needs from the app layer
// (catalog.Service).
type Catalogs interface {
	Get(ctx context.Context, known int64) (c catalog.Catalog, notModified bool, err error)
	TypeVersion(ctx context.Context, key string, version int32) (catalog.EventType, error)
}

// CatalogServer implements corev1.CatalogServiceServer (issue #237,
// ADR-0025 §8). Every method needs a verified token (auth.Guard); the
// catalog itself is the same for every caller.
type CatalogServer struct {
	corev1.UnimplementedCatalogServiceServer
	app Catalogs
	log *slog.Logger
}

// NewCatalogServer returns a CatalogServer.
func NewCatalogServer(app Catalogs, log *slog.Logger) *CatalogServer {
	return &CatalogServer{app: app, log: log}
}

// GetCatalog implements corev1.CatalogServiceServer.
func (s *CatalogServer) GetCatalog(ctx context.Context, req *corev1.GetCatalogRequest) (*corev1.GetCatalogResponse, error) {
	if _, err := callerID(ctx); err != nil {
		return nil, err
	}
	c, notModified, err := s.app.Get(ctx, req.GetKnownVersion())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.GetCatalogResponse{CatalogVersion: c.Version, NotModified: notModified}
	if notModified {
		return out, nil
	}
	out.Interests = make([]*corev1.CatalogInterest, 0, len(c.Interests))
	for _, in := range c.Interests {
		out.Interests = append(out.Interests, &corev1.CatalogInterest{
			Key: in.Key, Position: in.Position, Labels: in.Labels, Icon: in.Icon,
			Status: catalogStatusPB(in.Status), DefaultWidgets: in.DefaultWidgets,
		})
	}
	out.EventTypes = make([]*corev1.CatalogEventType, 0, len(c.Types))
	for _, t := range c.Types {
		out.EventTypes = append(out.EventTypes, catalogEventTypePB(t))
	}
	out.StudyTags = make([]*corev1.CatalogStudyTag, 0, len(c.StudyTags))
	for _, t := range c.StudyTags {
		out.StudyTags = append(out.StudyTags, &corev1.CatalogStudyTag{
			Key: t.Key, ParentKey: t.ParentKey, Position: t.Position, Labels: t.Labels, Status: catalogStatusPB(t.Status),
		})
	}
	out.Badges = make([]*corev1.CatalogBadge, 0, len(c.Badges))
	for _, b := range c.Badges {
		out.Badges = append(out.Badges, catalogBadgePB(b))
	}
	return out, nil
}

var (
	badgeScopesPB = map[string]corev1.BadgeScope{
		catalog.BadgeScopeUser: corev1.BadgeScope_BADGE_SCOPE_USER, catalog.BadgeScopeTayfa: corev1.BadgeScope_BADGE_SCOPE_TAYFA,
	}
	badgeKindsPB = map[string]corev1.BadgeRuleKind{
		catalog.RuleThreshold: corev1.BadgeRuleKind_BADGE_RULE_KIND_THRESHOLD,
		catalog.RuleFirst:     corev1.BadgeRuleKind_BADGE_RULE_KIND_FIRST,
		catalog.RuleChampion:  corev1.BadgeRuleKind_BADGE_RULE_KIND_CHAMPION,
	}
	badgePeriodsPB = map[string]corev1.BadgePeriod{
		catalog.PeriodAllTime: corev1.BadgePeriod_BADGE_PERIOD_ALL_TIME, catalog.PeriodYear: corev1.BadgePeriod_BADGE_PERIOD_YEAR,
	}
)

func catalogBadgePB(b catalog.Badge) *corev1.CatalogBadge {
	return &corev1.CatalogBadge{
		Key: b.Key, Scope: badgeScopesPB[b.Scope], Position: b.Position, Labels: b.Labels, Icon: b.Icon,
		Status: catalogStatusPB(b.Status),
		Rule: &corev1.CatalogBadgeRule{
			Kind: badgeKindsPB[b.Rule.Kind], Metric: b.Rule.Metric, Value: b.Rule.Value,
			Period: badgePeriodsPB[b.Rule.Period], EventType: b.Rule.EventType,
		},
	}
}

// GetEventTypeVersion implements corev1.CatalogServiceServer.
func (s *CatalogServer) GetEventTypeVersion(ctx context.Context, req *corev1.GetEventTypeVersionRequest) (*corev1.GetEventTypeVersionResponse, error) {
	if _, err := callerID(ctx); err != nil {
		return nil, err
	}
	t, err := s.app.TypeVersion(ctx, req.GetKey(), req.GetVersion())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetEventTypeVersionResponse{EventType: catalogEventTypePB(t)}, nil
}

func catalogEventTypePB(t catalog.EventType) *corev1.CatalogEventType {
	modes := make([]corev1.EventMode, 0, len(t.Modes))
	for _, m := range t.Modes {
		switch m {
		case catalog.ModeInPerson:
			modes = append(modes, corev1.EventMode_EVENT_MODE_IN_PERSON)
		case catalog.ModeOnline:
			modes = append(modes, corev1.EventMode_EVENT_MODE_ONLINE)
		}
	}
	return &corev1.CatalogEventType{
		Key: t.Key, InterestKeys: t.InterestKeys, Labels: t.Labels, Icon: t.Icon, Modes: modes,
		Status: catalogStatusPB(t.Status), Version: t.Version, Definition: string(t.Definition),
	}
}

func catalogStatusPB(s string) corev1.CatalogStatus {
	switch s {
	case catalog.StatusActive:
		return corev1.CatalogStatus_CATALOG_STATUS_ACTIVE
	case catalog.StatusRetired:
		return corev1.CatalogStatus_CATALOG_STATUS_RETIRED
	}
	return corev1.CatalogStatus_CATALOG_STATUS_UNSPECIFIED
}
