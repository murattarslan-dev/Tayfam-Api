package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/draw"
)

// Draws is what DrawComponentServer needs from the app layer
// (draw.Service).
type Draws interface {
	Get(ctx context.Context, ref component.Ref) (draw.Draw, error)
	Make(ctx context.Context, ref component.Ref) (draw.Draw, error)
}

// DrawComponentServer implements corev1.DrawComponentServiceServer (issue
// #249; ADR-0025 §4). Every method needs a verified token (auth.Guard); the
// acting user is always the token's.
type DrawComponentServer struct {
	corev1.UnimplementedDrawComponentServiceServer
	app Draws
	log *slog.Logger
}

// NewDrawComponentServer returns a DrawComponentServer.
func NewDrawComponentServer(app Draws, log *slog.Logger) *DrawComponentServer {
	return &DrawComponentServer{app: app, log: log}
}

// registerDrawComponent is the draw component's line in the registry
// (componentRegistrars).
func registerDrawComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterDrawComponentServiceServer(srv, NewDrawComponentServer(draw.New(fw), log))
	return nil
}

// drawPB converts a draw; an empty place has no user or team ID
// (idString).
func drawPB(d draw.Draw) *corev1.Draw {
	out := &corev1.Draw{
		Drawn: d.Drawn, Subject: d.Subject, TeamsKey: d.TeamsKey,
		Entries: make([]*corev1.DrawEntry, 0, len(d.Entries)), CallerCanDraw: d.CallerCanDraw,
	}
	if d.Drawn {
		out.DrawnTime = timestamppb.New(d.DrawnAt)
	}
	for _, e := range d.Entries {
		out.Entries = append(out.Entries, &corev1.DrawEntry{Position: e.Position, UserId: idString(e.User), TeamId: idString(e.Team)})
	}
	return out
}

// GetDraw implements corev1.DrawComponentServiceServer.
func (s *DrawComponentServer) GetDraw(ctx context.Context, req *corev1.GetDrawRequest) (*corev1.GetDrawResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetDrawResponse{Draw: drawPB(out)}, nil
}

// MakeDraw implements corev1.DrawComponentServiceServer.
func (s *DrawComponentServer) MakeDraw(ctx context.Context, req *corev1.MakeDrawRequest) (*corev1.MakeDrawResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Make(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.MakeDrawResponse{Draw: drawPB(out)}, nil
}
