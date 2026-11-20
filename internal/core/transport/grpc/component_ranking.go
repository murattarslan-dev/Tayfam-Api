package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/ranking"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Rankings is what RankingComponentServer needs from the app layer
// (ranking.Service).
type Rankings interface {
	Get(ctx context.Context, ref component.Ref) (ranking.Ranking, error)
	SetEntry(ctx context.Context, ref component.Ref, user id.ID, value float64) (ranking.Ranking, error)
	RemoveEntry(ctx context.Context, ref component.Ref, user id.ID) (ranking.Ranking, error)
}

// RankingComponentServer implements corev1.RankingComponentServiceServer
// (issue #253; ADR-0025 §4, §5; ADR-0037 §3). Every method needs a
// verified token (auth.Guard); the acting user is always the token's.
// Values are never logged.
type RankingComponentServer struct {
	corev1.UnimplementedRankingComponentServiceServer
	app Rankings
	log *slog.Logger
}

// NewRankingComponentServer returns a RankingComponentServer.
func NewRankingComponentServer(app Rankings, log *slog.Logger) *RankingComponentServer {
	return &RankingComponentServer{app: app, log: log}
}

// registerRankingComponent is the ranking component's line in the registry
// (componentRegistrars).
func registerRankingComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterRankingComponentServiceServer(srv, NewRankingComponentServer(ranking.New(fw), log))
	return nil
}

func rankingMetricPB(s string) corev1.RankingMetric {
	switch s {
	case ranking.MetricDuration:
		return corev1.RankingMetric_RANKING_METRIC_DURATION
	case ranking.MetricRank:
		return corev1.RankingMetric_RANKING_METRIC_RANK
	}
	return corev1.RankingMetric_RANKING_METRIC_UNSPECIFIED
}

func rankingDirectionPB(s string) corev1.RankingDirection {
	switch s {
	case ranking.DirectionAsc:
		return corev1.RankingDirection_RANKING_DIRECTION_ASC
	case ranking.DirectionDesc:
		return corev1.RankingDirection_RANKING_DIRECTION_DESC
	}
	return corev1.RankingDirection_RANKING_DIRECTION_UNSPECIFIED
}

func eventRankingPB(r ranking.Ranking) *corev1.EventRanking {
	out := &corev1.EventRanking{
		Source: r.Source, Metric: rankingMetricPB(r.Metric), Direction: rankingDirectionPB(r.Direction),
		Unit: r.Unit, Decimals: r.Decimals, Hidden: r.Hidden, MeasureEnabled: r.MeasureEnabled, CallerCanEdit: r.CallerCanEdit,
		Entries: make([]*corev1.RankingEntry, 0, len(r.Entries)),
	}
	for _, e := range r.Entries {
		out.Entries = append(out.Entries, &corev1.RankingEntry{
			UserId: e.User.String(), Value: e.Value, Rank: e.Rank, UpdateTime: timestamppb.New(e.UpdatedAt),
		})
	}
	return out
}

// GetRanking implements corev1.RankingComponentServiceServer.
func (s *RankingComponentServer) GetRanking(ctx context.Context, req *corev1.GetRankingRequest) (*corev1.GetRankingResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	r, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetRankingResponse{Ranking: eventRankingPB(r)}, nil
}

// SetRankingEntry implements corev1.RankingComponentServiceServer.
func (s *RankingComponentServer) SetRankingEntry(ctx context.Context, req *corev1.SetRankingEntryRequest) (*corev1.SetRankingEntryResponse, error) {
	ref, u, err := measureEntryRef(ctx, req.GetRef(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	if !finite(req.GetValue()) {
		return nil, validation("value")
	}
	r, err := s.app.SetEntry(ctx, ref, u, req.GetValue())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetRankingEntryResponse{Ranking: eventRankingPB(r)}, nil
}

// RemoveRankingEntry implements corev1.RankingComponentServiceServer.
func (s *RankingComponentServer) RemoveRankingEntry(ctx context.Context, req *corev1.RemoveRankingEntryRequest) (*corev1.RemoveRankingEntryResponse, error) {
	ref, u, err := measureEntryRef(ctx, req.GetRef(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	r, err := s.app.RemoveEntry(ctx, ref, u)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveRankingEntryResponse{Ranking: eventRankingPB(r)}, nil
}
