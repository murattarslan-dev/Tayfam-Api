package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/chatevents"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// ChatEventServiceName is the gRPC service whose methods only chat's
// service token may call (ADR-0013, ADR-0028); cmd/core gives it to
// auth.Guard.
const ChatEventServiceName = "tayfam.core.v1.ChatEventService"

// ChatEvents is what ChatEventServer needs from the app layer.
type ChatEvents interface {
	MediaReleased(ctx context.Context, e chatevents.MediaReleased) error
}

// ChatEventServer implements corev1.ChatEventServiceServer. auth.Guard lets
// only chat's ServiceAccount reach it.
type ChatEventServer struct {
	corev1.UnimplementedChatEventServiceServer
	app ChatEvents
	log *slog.Logger
}

// NewChatEventServer returns a ChatEventServer.
func NewChatEventServer(app ChatEvents, log *slog.Logger) *ChatEventServer {
	return &ChatEventServer{app: app, log: log}
}

// ApplyChatEvent implements corev1.ChatEventServiceServer.
func (s *ChatEventServer) ApplyChatEvent(ctx context.Context, req *corev1.ApplyChatEventRequest) (*corev1.ApplyChatEventResponse, error) {
	// Defence in depth: the Guard has already checked the caller.
	if _, ok := auth.ServiceCallerFrom(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	if _, ok := id.Parse(req.GetEventId()); !ok {
		return nil, validation("event_id")
	}
	switch ev := req.GetEvent().(type) {
	case *corev1.ApplyChatEventRequest_MediaReleased:
		up, ok := id.Parse(ev.MediaReleased.GetUploadId())
		if !ok {
			return nil, validation("media_released.upload_id")
		}
		msg, ok := id.Parse(ev.MediaReleased.GetMessageId())
		if !ok {
			return nil, validation("media_released.message_id")
		}
		if err := s.app.MediaReleased(ctx, chatevents.MediaReleased{Upload: up, Message: msg}); err != nil {
			return nil, errorStatus(ctx, s.log, err)
		}
	default:
		return nil, validation("event")
	}
	return &corev1.ApplyChatEventResponse{}, nil
}
