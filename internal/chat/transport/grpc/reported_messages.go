package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// ReportedMessageServiceName is the gRPC service whose methods only core's
// service token may call (ADR-0013, ADR-0031); cmd/chat gives it to
// auth.Guard.
const ReportedMessageServiceName = "tayfam.chat.v1.ReportedMessageService"

// ReportedMessages answers whether a reported channel message is removed.
type ReportedMessages interface {
	MessageRemoved(ctx context.Context, tayfa, message id.ID) (bool, error)
}

// ReportedMessageServer implements chatv1.ReportedMessageServiceServer.
// auth.Guard lets only core's ServiceAccount reach it; the answer is one
// flag.
type ReportedMessageServer struct {
	chatv1.UnimplementedReportedMessageServiceServer
	app ReportedMessages
	log *slog.Logger
}

// NewReportedMessageServer returns a ReportedMessageServer.
func NewReportedMessageServer(app ReportedMessages, log *slog.Logger) *ReportedMessageServer {
	return &ReportedMessageServer{app: app, log: log}
}

// GetChannelMessageStatus implements chatv1.ReportedMessageServiceServer.
func (s *ReportedMessageServer) GetChannelMessageStatus(ctx context.Context, req *chatv1.GetChannelMessageStatusRequest) (*chatv1.GetChannelMessageStatusResponse, error) {
	// Defence in depth: the Guard has already checked the caller.
	if _, ok := auth.ServiceCallerFrom(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	tayfa, ok := id.Parse(req.GetTayfaId())
	if !ok {
		return nil, validation("tayfa_id")
	}
	message, ok := id.Parse(req.GetMessageId())
	if !ok {
		return nil, validation("message_id")
	}
	removed, err := s.app.MessageRemoved(ctx, tayfa, message)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.GetChannelMessageStatusResponse{Removed: removed}, nil
}
