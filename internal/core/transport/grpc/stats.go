package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/stats"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Stats is what TayfaStatsServer needs from the app layer (stats.Service).
type Stats interface {
	Tayfa(ctx context.Context, user, tayfa id.ID, year int32) (stats.View, error)
}

// TayfaStatsServer implements corev1.TayfaStatsServiceServer (issue #266;
// ADR-0026). Every method needs a verified token (auth.Guard); the acting
// user is always the token's. Values are never logged. The deprecated
// contributions field is never filled: a reader gets only their own
// contributions (issue #322).
type TayfaStatsServer struct {
	corev1.UnimplementedTayfaStatsServiceServer
	app Stats
	log *slog.Logger
}

// NewTayfaStatsServer returns a TayfaStatsServer.
func NewTayfaStatsServer(app Stats, log *slog.Logger) *TayfaStatsServer {
	return &TayfaStatsServer{app: app, log: log}
}

// registerTayfaStats is the tayfa statistics' line in the registry
// (componentRegistrars): they are computed from the components' data.
func registerTayfaStats(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterTayfaStatsServiceServer(srv, NewTayfaStatsServer(stats.New(fw), log))
	return nil
}

// GetTayfaStats implements corev1.TayfaStatsServiceServer.
func (s *TayfaStatsServer) GetTayfaStats(ctx context.Context, req *corev1.GetTayfaStatsRequest) (*corev1.GetTayfaStatsResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	if !stats.ValidYear(req.GetYear()) {
		return nil, validation("year")
	}
	v, err := s.app.Tayfa(ctx, user, t, req.GetYear())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.GetTayfaStatsResponse{
		Year: v.Year, Years: v.Years,
		Totals:           make([]*corev1.TayfaStat, 0, len(v.Totals)),
		OwnContributions: make([]*corev1.TayfaStat, 0, len(v.Own)),
	}
	if out.Years == nil {
		out.Years = []int32{}
	}
	if !v.ComputedAt.IsZero() {
		out.ComputeTime = timestamppb.New(v.ComputedAt)
	}
	for _, st := range v.Totals {
		out.Totals = append(out.Totals, &corev1.TayfaStat{MetricKey: st.Metric, Source: st.Source, Value: st.Value, EventCount: st.Events})
	}
	for _, st := range v.Own {
		out.OwnContributions = append(out.OwnContributions, &corev1.TayfaStat{MetricKey: st.Metric, Source: st.Source, Value: st.Value, EventCount: st.Events})
	}
	return out, nil
}
