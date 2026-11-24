package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/celebration"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Celebrations is what CelebrationComponentServer needs from the app layer
// (celebration.Service).
type Celebrations interface {
	Get(ctx context.Context, ref component.Ref) (celebration.Celebration, error)
	Announce(ctx context.Context, ref component.Ref, winners []id.ID) (celebration.Celebration, error)
}

// CelebrationComponentServer implements
// corev1.CelebrationComponentServiceServer (issue #255; ADR-0025 §4, §5).
// Every method needs a verified token (auth.Guard); the acting user is
// always the token's.
type CelebrationComponentServer struct {
	corev1.UnimplementedCelebrationComponentServiceServer
	app Celebrations
	log *slog.Logger
}

// NewCelebrationComponentServer returns a CelebrationComponentServer.
func NewCelebrationComponentServer(app Celebrations, log *slog.Logger) *CelebrationComponentServer {
	return &CelebrationComponentServer{app: app, log: log}
}

// registerCelebrationComponent is the celebration component's line in the
// registry (componentRegistrars).
func registerCelebrationComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterCelebrationComponentServiceServer(srv, NewCelebrationComponentServer(celebration.New(fw), log))
	return nil
}

func eventCelebrationPB(c celebration.Celebration) *corev1.EventCelebration {
	out := &corev1.EventCelebration{
		Source: c.Source, SourceKind: c.SourceKind, Hidden: c.Hidden, Announced: c.Announced,
		CallerCanAnnounce: c.CallerCanAnnounce, Winners: make([]*corev1.CelebrationWinner, 0, len(c.Winners)),
	}
	if c.Announced {
		out.AnnounceTime = timestamppb.New(c.AnnouncedAt)
	}
	for _, w := range c.Winners {
		out.Winners = append(out.Winners, &corev1.CelebrationWinner{
			UserId: idString(w.User), TeamId: idString(w.Team), GuestId: idString(w.Guest), TableId: idString(w.Table),
			VoteOptionId: idString(w.VoteOption),
		})
	}
	return out
}

// GetCelebration implements corev1.CelebrationComponentServiceServer.
func (s *CelebrationComponentServer) GetCelebration(ctx context.Context, req *corev1.GetCelebrationRequest) (*corev1.GetCelebrationResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	c, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetCelebrationResponse{Celebration: eventCelebrationPB(c)}, nil
}

// AnnounceCelebration implements corev1.CelebrationComponentServiceServer.
func (s *CelebrationComponentServer) AnnounceCelebration(ctx context.Context, req *corev1.AnnounceCelebrationRequest) (*corev1.AnnounceCelebrationResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if len(req.GetWinnerIds()) > celebration.MaxWinners {
		return nil, validation("winner_ids")
	}
	winners := make([]id.ID, 0, len(req.GetWinnerIds()))
	for _, raw := range req.GetWinnerIds() {
		u, err := parseID(raw, "winner_ids")
		if err != nil {
			return nil, err
		}
		winners = append(winners, u)
	}
	c, err := s.app.Announce(ctx, ref, winners)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AnnounceCelebrationResponse{Celebration: eventCelebrationPB(c)}, nil
}
