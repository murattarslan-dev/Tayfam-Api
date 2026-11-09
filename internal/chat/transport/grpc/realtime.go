package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/realtime"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Tickets is what RealtimeServer needs from the app layer.
type Tickets interface {
	Issue(ctx context.Context, user, session id.ID) (realtime.Ticket, error)
}

// RealtimeServer implements chatv1.RealtimeServiceServer (issue #82).
type RealtimeServer struct {
	chatv1.UnimplementedRealtimeServiceServer
	app Tickets
	log *slog.Logger
}

// NewRealtimeServer returns a RealtimeServer.
func NewRealtimeServer(app Tickets, log *slog.Logger) *RealtimeServer {
	return &RealtimeServer{app: app, log: log}
}

// CreateRealtimeTicket implements chatv1.RealtimeServiceServer. The ticket
// belongs to the session of the verified access token (its sid claim).
func (s *RealtimeServer) CreateRealtimeTicket(ctx context.Context, _ *chatv1.CreateRealtimeTicketRequest) (*chatv1.CreateRealtimeTicketResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	c, _ := auth.CallerFrom(ctx)
	session, ok := id.Parse(c.SessionID)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	t, err := s.app.Issue(ctx, user, session)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.CreateRealtimeTicketResponse{Ticket: t.Value, ExpireTime: timestamppb.New(t.ExpiresAt)}, nil
}
