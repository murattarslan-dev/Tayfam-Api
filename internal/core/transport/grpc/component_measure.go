package grpc

import (
	"context"
	"log/slog"
	"math"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/measure"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Measures is what MeasureComponentServer needs from the app layer
// (measure.Service).
type Measures interface {
	Get(ctx context.Context, ref component.Ref) (measure.Measure, error)
	SetTotal(ctx context.Context, ref component.Ref, value float64) (measure.Measure, error)
	RemoveTotal(ctx context.Context, ref component.Ref) (measure.Measure, error)
	SetEntry(ctx context.Context, ref component.Ref, user id.ID, value float64) (measure.Measure, error)
	RemoveEntry(ctx context.Context, ref component.Ref, user id.ID) (measure.Measure, error)
}

// MeasureComponentServer implements corev1.MeasureComponentServiceServer
// (issue #245; ADR-0025 §4, §5). Every method needs a verified token
// (auth.Guard); the acting user is always the token's. Values are never
// logged.
type MeasureComponentServer struct {
	corev1.UnimplementedMeasureComponentServiceServer
	app Measures
	log *slog.Logger
}

// NewMeasureComponentServer returns a MeasureComponentServer.
func NewMeasureComponentServer(app Measures, log *slog.Logger) *MeasureComponentServer {
	return &MeasureComponentServer{app: app, log: log}
}

// registerMeasureComponent is the measure component's line in the registry
// (componentRegistrars).
func registerMeasureComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterMeasureComponentServiceServer(srv, NewMeasureComponentServer(measure.New(fw), log))
	return nil
}

func measureScopePB(s string) corev1.MeasureScope {
	switch s {
	case measure.ScopeTotal:
		return corev1.MeasureScope_MEASURE_SCOPE_TOTAL
	case measure.ScopePersonal:
		return corev1.MeasureScope_MEASURE_SCOPE_PERSONAL
	}
	return corev1.MeasureScope_MEASURE_SCOPE_UNSPECIFIED
}

func measureEnteredByPB(s string) corev1.MeasureEnteredBy {
	switch s {
	case measure.EnteredBySelf:
		return corev1.MeasureEnteredBy_MEASURE_ENTERED_BY_SELF
	case measure.EnteredByOrganizer:
		return corev1.MeasureEnteredBy_MEASURE_ENTERED_BY_ORGANIZER
	}
	return corev1.MeasureEnteredBy_MEASURE_ENTERED_BY_UNSPECIFIED
}

func eventMeasurePB(m measure.Measure) *corev1.EventMeasure {
	out := &corev1.EventMeasure{
		Unit: m.Unit, Scope: measureScopePB(m.Scope), EnteredBy: measureEnteredByPB(m.EnteredBy),
		Min: m.Min, Max: m.Max, Decimals: m.Decimals,
		Total: m.Total, Suggested: m.Suggested, CallerCanEdit: m.CallerCanEdit,
		Entries: make([]*corev1.MeasureEntry, 0, len(m.Entries)),
	}
	if m.Total != nil && !m.TotalUpdatedAt.IsZero() {
		out.TotalUpdateTime = timestamppb.New(m.TotalUpdatedAt)
	}
	for _, e := range m.Entries {
		out.Entries = append(out.Entries, &corev1.MeasureEntry{UserId: e.User.String(), Value: e.Value, UpdateTime: timestamppb.New(e.UpdatedAt)})
	}
	return out
}

// finite refuses NaN and infinities before the app layer is asked.
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// measureEntryRef reads the request's component and person.
func measureEntryRef(ctx context.Context, ref *corev1.ComponentRef, rawUser string) (component.Ref, id.ID, error) {
	r, err := componentRef(ctx, ref)
	if err != nil {
		return component.Ref{}, id.ID{}, err
	}
	u, err := parseID(rawUser, "user_id")
	if err != nil {
		return component.Ref{}, id.ID{}, err
	}
	return r, u, nil
}

// GetMeasure implements corev1.MeasureComponentServiceServer.
func (s *MeasureComponentServer) GetMeasure(ctx context.Context, req *corev1.GetMeasureRequest) (*corev1.GetMeasureResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	m, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetMeasureResponse{Measure: eventMeasurePB(m)}, nil
}

// SetMeasureTotal implements corev1.MeasureComponentServiceServer.
func (s *MeasureComponentServer) SetMeasureTotal(ctx context.Context, req *corev1.SetMeasureTotalRequest) (*corev1.SetMeasureTotalResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if !finite(req.GetValue()) {
		return nil, validation("value")
	}
	m, err := s.app.SetTotal(ctx, ref, req.GetValue())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetMeasureTotalResponse{Measure: eventMeasurePB(m)}, nil
}

// RemoveMeasureTotal implements corev1.MeasureComponentServiceServer.
func (s *MeasureComponentServer) RemoveMeasureTotal(ctx context.Context, req *corev1.RemoveMeasureTotalRequest) (*corev1.RemoveMeasureTotalResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	m, err := s.app.RemoveTotal(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveMeasureTotalResponse{Measure: eventMeasurePB(m)}, nil
}

// SetMeasureEntry implements corev1.MeasureComponentServiceServer.
func (s *MeasureComponentServer) SetMeasureEntry(ctx context.Context, req *corev1.SetMeasureEntryRequest) (*corev1.SetMeasureEntryResponse, error) {
	ref, u, err := measureEntryRef(ctx, req.GetRef(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	if !finite(req.GetValue()) {
		return nil, validation("value")
	}
	m, err := s.app.SetEntry(ctx, ref, u, req.GetValue())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetMeasureEntryResponse{Measure: eventMeasurePB(m)}, nil
}

// RemoveMeasureEntry implements corev1.MeasureComponentServiceServer.
func (s *MeasureComponentServer) RemoveMeasureEntry(ctx context.Context, req *corev1.RemoveMeasureEntryRequest) (*corev1.RemoveMeasureEntryResponse, error) {
	ref, u, err := measureEntryRef(ctx, req.GetRef(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	m, err := s.app.RemoveEntry(ctx, ref, u)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveMeasureEntryResponse{Measure: eventMeasurePB(m)}, nil
}
