package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// UserEventServiceName is the gRPC service whose methods only service
// tokens may call (ADR-0013); cmd/core gives it to auth.Guard.
const UserEventServiceName = "tayfam.core.v1.UserEventService"

// UserEvents is what UserEventServer needs from the app layer.
type UserEvents interface {
	Apply(ctx context.Context, e userevents.Event) error
}

// UserEventServer implements corev1.UserEventServiceServer. auth.Guard lets
// only identity's ServiceAccount reach it.
type UserEventServer struct {
	corev1.UnimplementedUserEventServiceServer
	app UserEvents
	log *slog.Logger
}

// NewUserEventServer returns a UserEventServer.
func NewUserEventServer(app UserEvents, log *slog.Logger) *UserEventServer {
	return &UserEventServer{app: app, log: log}
}

// ApplyUserEvent implements corev1.UserEventServiceServer.
func (s *UserEventServer) ApplyUserEvent(ctx context.Context, req *corev1.ApplyUserEventRequest) (*corev1.ApplyUserEventResponse, error) {
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
	case *corev1.ApplyUserEventRequest_ProfileUpdated:
		p := &userevents.Profile{DisplayName: ev.ProfileUpdated.GetDisplayName()}
		if a := ev.ProfileUpdated.GetAvatarUploadId(); a != "" {
			if p.AvatarUploadID, ok = id.Parse(a); !ok {
				return nil, validation("profile_updated.avatar_upload_id")
			}
		}
		switch ev.ProfileUpdated.GetActivityVisibility() {
		case corev1.ActivityVisibility_ACTIVITY_VISIBILITY_UNSPECIFIED:
		case corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE:
			p.ActivityVisibility = userevents.VisibilityPrivate
		case corev1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA:
			p.ActivityVisibility = userevents.VisibilityTayfa
		case corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC:
			p.ActivityVisibility = userevents.VisibilityPublic
		default:
			return nil, validation("profile_updated.activity_visibility")
		}
		e.Profile = p
	case *corev1.ApplyUserEventRequest_Deleted:
		e.Deleted = true
	default:
		return nil, validation("event")
	}
	if err := s.app.Apply(ctx, e); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ApplyUserEventResponse{}, nil
}
