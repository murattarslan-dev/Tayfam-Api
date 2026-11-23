package grpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/admin"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
)

// Catalog management on the platform panel (issue #262; ADR-0025 §7). The
// app layer (package admin) checks everything; this file only converts.
// An enum value this version does not know becomes an empty string, which
// the checks refuse with the field's problem.

// catalogError maps a failed catalog check to ERROR_REASON_VALIDATION with
// the field and the problem in the ErrorInfo metadata; other errors as
// errorStatus does.
func (s *AdminServer) catalogError(ctx context.Context, err error) error {
	var p *admin.ProblemError
	if errors.As(err, &p) {
		return reasonStatus(codes.InvalidArgument, corev1.ErrorReason_ERROR_REASON_VALIDATION,
			map[string]string{"field": p.Field, "problem": p.Problem})
	}
	return errorStatus(ctx, s.log, err)
}

func catalogStatusOf(st corev1.CatalogStatus) string {
	switch st {
	case corev1.CatalogStatus_CATALOG_STATUS_ACTIVE:
		return catalog.StatusActive
	case corev1.CatalogStatus_CATALOG_STATUS_RETIRED:
		return catalog.StatusRetired
	}
	return ""
}

func eventModesOf(ms []corev1.EventMode) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		switch m {
		case corev1.EventMode_EVENT_MODE_IN_PERSON:
			out = append(out, catalog.ModeInPerson)
		case corev1.EventMode_EVENT_MODE_ONLINE:
			out = append(out, catalog.ModeOnline)
		default:
			out = append(out, "")
		}
	}
	return out
}

// reverse returns the key of a converter map whose value is v; "" when
// none is.
func reverse[K comparable, V comparable](m map[K]V, v V) K {
	for k, w := range m {
		if w == v {
			return k
		}
	}
	var zero K
	return zero
}

func catalogCheckPB(c admin.CheckResult) *corev1.CatalogCheck {
	out := &corev1.CatalogCheck{
		Problem: c.Problem, Field: c.Field, MinClientVersion: c.MinClientVersion,
		Version: c.Version, NewVersion: c.NewVersion,
	}
	for _, f := range c.Fields {
		out.Fields = append(out.Fields, &corev1.CatalogPreviewField{Key: f.Key, Kind: f.Kind, Required: f.Required, Labels: f.Labels})
	}
	for _, cp := range c.Components {
		out.Components = append(out.Components, &corev1.CatalogPreviewComponent{
			Kind: cp.Kind, Key: cp.Key, Labels: cp.Labels, Optional: cp.Optional != nil,
			DefaultOn: cp.Optional == nil || *cp.Optional, MinClientVersion: definition.MinClientVersions[cp.Kind],
		})
	}
	return out
}

// GetCatalogAdmin implements corev1.AdminServiceServer.
func (s *AdminServer) GetCatalogAdmin(ctx context.Context, _ *corev1.GetCatalogAdminRequest) (*corev1.GetCatalogAdminResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.app.GetCatalog(ctx, c)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.GetCatalogAdminResponse{
		CatalogVersion: v.Version,
		Interests:      make([]*corev1.CatalogInterest, 0, len(v.Interests)),
		EventTypes:     make([]*corev1.AdminCatalogEventType, 0, len(v.Types)),
		Badges:         make([]*corev1.CatalogBadge, 0, len(v.Badges)),
		StudyTags:      make([]*corev1.CatalogStudyTag, 0, len(v.StudyTags)),
		ScorePresets:   make([]*corev1.AdminCatalogScorePreset, 0, len(v.Presets)),
		BlockedDomains: make([]*corev1.AdminBlockedDomain, 0, len(v.Domains)),
	}
	for _, in := range v.Interests {
		out.Interests = append(out.Interests, &corev1.CatalogInterest{
			Key: in.Key, Position: in.Position, Labels: in.Labels, Icon: in.Icon,
			Status: catalogStatusPB(in.Status), DefaultWidgets: in.DefaultWidgets,
		})
	}
	for _, t := range v.Types {
		out.EventTypes = append(out.EventTypes, &corev1.AdminCatalogEventType{
			Type: catalogEventTypePB(t.EventType), Position: t.Position, MinClientVersion: t.Parsed.MinClientVersion(),
		})
	}
	for _, b := range v.Badges {
		out.Badges = append(out.Badges, catalogBadgePB(b))
	}
	for _, t := range v.StudyTags {
		out.StudyTags = append(out.StudyTags, &corev1.CatalogStudyTag{
			Key: t.Key, ParentKey: t.ParentKey, Position: t.Position, Labels: t.Labels, Status: catalogStatusPB(t.Status),
		})
	}
	for _, p := range v.Presets {
		out.ScorePresets = append(out.ScorePresets, &corev1.AdminCatalogScorePreset{
			Key: p.Key, Position: p.Position, Labels: p.Labels, Params: string(p.Params), Status: catalogStatusPB(p.Status),
		})
	}
	for _, d := range v.Domains {
		out.BlockedDomains = append(out.BlockedDomains, &corev1.AdminBlockedDomain{
			Domain: d.Domain, CreateTime: timestamppb.New(d.CreatedAt), Initial: d.Initial,
		})
	}
	return out, nil
}

// SaveCatalogInterest implements corev1.AdminServiceServer.
func (s *AdminServer) SaveCatalogInterest(ctx context.Context, req *corev1.SaveCatalogInterestRequest) (*corev1.SaveCatalogInterestResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	in := req.GetInterest()
	res, err := s.app.SaveInterest(ctx, c, catalog.Interest{
		Key: in.GetKey(), Position: in.GetPosition(), Labels: in.GetLabels(), Icon: in.GetIcon(),
		Status: catalogStatusOf(in.GetStatus()), DefaultWidgets: in.GetDefaultWidgets(),
	}, req.GetCreate(), req.GetValidateOnly(), req.GetReason())
	if err != nil {
		return nil, s.catalogError(ctx, err)
	}
	return &corev1.SaveCatalogInterestResponse{Check: catalogCheckPB(res.Check), CatalogVersion: res.CatalogVersion}, nil
}

// SaveCatalogEventType implements corev1.AdminServiceServer.
func (s *AdminServer) SaveCatalogEventType(ctx context.Context, req *corev1.SaveCatalogEventTypeRequest) (*corev1.SaveCatalogEventTypeResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	t := req.GetType()
	res, err := s.app.SaveEventType(ctx, c, admin.TypeInput{
		Key: t.GetKey(), InterestKeys: t.GetInterestKeys(), Position: req.GetPosition(), Labels: t.GetLabels(),
		Icon: t.GetIcon(), Modes: eventModesOf(t.GetModes()), Status: catalogStatusOf(t.GetStatus()),
		BaseVersion: t.GetVersion(), Definition: []byte(t.GetDefinition()),
	}, req.GetValidateOnly(), req.GetReason())
	if err != nil {
		return nil, s.catalogError(ctx, err)
	}
	return &corev1.SaveCatalogEventTypeResponse{Check: catalogCheckPB(res.Check), CatalogVersion: res.CatalogVersion}, nil
}

// SaveCatalogBadge implements corev1.AdminServiceServer.
func (s *AdminServer) SaveCatalogBadge(ctx context.Context, req *corev1.SaveCatalogBadgeRequest) (*corev1.SaveCatalogBadgeResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetBadge()
	r := b.GetRule()
	res, err := s.app.SaveBadge(ctx, c, catalog.Badge{
		Key: b.GetKey(), Scope: reverse(badgeScopesPB, b.GetScope()), Position: b.GetPosition(), Labels: b.GetLabels(),
		Icon: b.GetIcon(), Status: catalogStatusOf(b.GetStatus()),
		Rule: catalog.BadgeRule{
			Kind: reverse(badgeKindsPB, r.GetKind()), Metric: r.GetMetric(), Value: r.GetValue(),
			Period: reverse(badgePeriodsPB, r.GetPeriod()), EventType: r.GetEventType(),
		},
	}, req.GetCreate(), req.GetValidateOnly(), req.GetReason())
	if err != nil {
		return nil, s.catalogError(ctx, err)
	}
	return &corev1.SaveCatalogBadgeResponse{Check: catalogCheckPB(res.Check), CatalogVersion: res.CatalogVersion}, nil
}

// SaveCatalogStudyTag implements corev1.AdminServiceServer.
func (s *AdminServer) SaveCatalogStudyTag(ctx context.Context, req *corev1.SaveCatalogStudyTagRequest) (*corev1.SaveCatalogStudyTagResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	t := req.GetTag()
	res, err := s.app.SaveStudyTag(ctx, c, catalog.StudyTag{
		Key: t.GetKey(), ParentKey: t.GetParentKey(), Position: t.GetPosition(), Labels: t.GetLabels(),
		Status: catalogStatusOf(t.GetStatus()),
	}, req.GetCreate(), req.GetValidateOnly(), req.GetReason())
	if err != nil {
		return nil, s.catalogError(ctx, err)
	}
	return &corev1.SaveCatalogStudyTagResponse{Check: catalogCheckPB(res.Check), CatalogVersion: res.CatalogVersion}, nil
}

// SaveCatalogScorePreset implements corev1.AdminServiceServer.
func (s *AdminServer) SaveCatalogScorePreset(ctx context.Context, req *corev1.SaveCatalogScorePresetRequest) (*corev1.SaveCatalogScorePresetResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	p := req.GetPreset()
	res, err := s.app.SaveScorePreset(ctx, c, admin.ScorePreset{
		Key: p.GetKey(), Position: p.GetPosition(), Labels: p.GetLabels(), Params: []byte(p.GetParams()),
		Status: catalogStatusOf(p.GetStatus()),
	}, req.GetCreate(), req.GetValidateOnly(), req.GetReason())
	if err != nil {
		return nil, s.catalogError(ctx, err)
	}
	return &corev1.SaveCatalogScorePresetResponse{Check: catalogCheckPB(res.Check), CatalogVersion: res.CatalogVersion}, nil
}

// BlockDomain implements corev1.AdminServiceServer.
func (s *AdminServer) BlockDomain(ctx context.Context, req *corev1.BlockDomainRequest) (*corev1.BlockDomainResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.app.BlockDomain(ctx, c, req.GetDomain(), req.GetReason()); err != nil {
		return nil, s.catalogError(ctx, err)
	}
	return &corev1.BlockDomainResponse{}, nil
}

// UnblockDomain implements corev1.AdminServiceServer.
func (s *AdminServer) UnblockDomain(ctx context.Context, req *corev1.UnblockDomainRequest) (*corev1.UnblockDomainResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.app.UnblockDomain(ctx, c, req.GetDomain(), req.GetReason()); err != nil {
		return nil, s.catalogError(ctx, err)
	}
	return &corev1.UnblockDomainResponse{}, nil
}
