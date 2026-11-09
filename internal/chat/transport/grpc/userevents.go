package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// UserEventServiceName is the gRPC service whose methods only service
// tokens may call (ADR-0013); cmd/chat gives it to auth.Guard.
const UserEventServiceName = "tayfam.chat.v1.UserEventService"

// UserEvents is what UserEventServer needs from the app layer.
type UserEvents interface {
	Apply(ctx context.Context, e userevents.Event) error
}

// UserEventServer implements chatv1.UserEventServiceServer. auth.Guard lets
// only identity's ServiceAccount reach it.
type UserEventServer struct {
	chatv1.UnimplementedUserEventServiceServer
	app UserEvents
	log *slog.Logger
}

// NewUserEventServer returns a UserEventServer.
func NewUserEventServer(app UserEvents, log *slog.Logger) *UserEventServer {
	return &UserEventServer{app: app, log: log}
}

// ApplyUserEvent implements chatv1.UserEventServiceServer.
func (s *UserEventServer) ApplyUserEvent(ctx context.Context, req *chatv1.ApplyUserEventRequest) (*chatv1.ApplyUserEventResponse, error) {
	// Defence in depth: the Guard has already checked the caller.
	if _, ok := auth.ServiceCallerFrom(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	eid, ok := id.Parse(req.GetEventId())
	if !ok {
		return nil, validation("event_id")
	}
	user, ok := id.Parse(req.GetUserId())
	if !ok {
		return nil, validation("user_id")
	}
	e := userevents.Event{ID: eid, User: user, OccurredAt: req.GetOccurredAt().AsTime()}
	switch ev := req.GetEvent().(type) {
	case *chatv1.ApplyUserEventRequest_ProfileUpdated:
		p := &userevents.Profile{DisplayName: ev.ProfileUpdated.GetDisplayName()}
		if a := ev.ProfileUpdated.GetAvatarUploadId(); a != "" {
			if p.AvatarUploadID, ok = id.Parse(a); !ok {
				return nil, validation("profile_updated.avatar_upload_id")
			}
		}
		e.Profile = p
	case *chatv1.ApplyUserEventRequest_Deleted:
		e.Deleted = true
	default:
		return nil, validation("event")
	}
	if err := s.app.Apply(ctx, e); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.ApplyUserEventResponse{}, nil
}
