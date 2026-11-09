package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/coreevents"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// CoreEventServiceName is the gRPC service whose methods only service
// tokens may call (ADR-0013); cmd/chat gives it to auth.Guard.
const CoreEventServiceName = "tayfam.chat.v1.CoreEventService"

// CoreEvents is what CoreEventServer needs from the app layer.
type CoreEvents interface {
	Apply(ctx context.Context, e coreevents.Event) error
}

// CoreEventServer implements chatv1.CoreEventServiceServer. auth.Guard lets
// only core's ServiceAccount reach it.
type CoreEventServer struct {
	chatv1.UnimplementedCoreEventServiceServer
	app CoreEvents
	log *slog.Logger
}

// NewCoreEventServer returns a CoreEventServer.
func NewCoreEventServer(app CoreEvents, log *slog.Logger) *CoreEventServer {
	return &CoreEventServer{app: app, log: log}
}

var notificationTypes = map[chatv1.NotificationType]string{
	chatv1.NotificationType_NOTIFICATION_TYPE_EVENT_CREATED:            notification.TypeEventCreated,
	chatv1.NotificationType_NOTIFICATION_TYPE_EVENT_UPDATED:            notification.TypeEventUpdated,
	chatv1.NotificationType_NOTIFICATION_TYPE_EVENT_CANCELLED:          notification.TypeEventCancelled,
	chatv1.NotificationType_NOTIFICATION_TYPE_RSVP_PROMOTED:            notification.TypeRSVPPromoted,
	chatv1.NotificationType_NOTIFICATION_TYPE_ANNOUNCEMENT:             notification.TypeAnnouncement,
	chatv1.NotificationType_NOTIFICATION_TYPE_ANSWER_RECEIVED:          notification.TypeAnswerReceived,
	chatv1.NotificationType_NOTIFICATION_TYPE_JOIN_REQUEST:             notification.TypeJoinRequest,
	chatv1.NotificationType_NOTIFICATION_TYPE_JOIN_APPROVED:            notification.TypeJoinApproved,
	chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_DELETION_REQUESTED: notification.TypeTayfaDeletionRequested,
	chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_WARNED:             notification.TypeTayfaWarned,
	chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_ARCHIVED:           notification.TypeTayfaArchived,
	chatv1.NotificationType_NOTIFICATION_TYPE_HIKE_OVERDUE:             notification.TypeHikeOverdue,
	chatv1.NotificationType_NOTIFICATION_TYPE_FRIEND_REQUEST:           notification.TypeFriendRequest,
	chatv1.NotificationType_NOTIFICATION_TYPE_FRIEND_ACCEPTED:          notification.TypeFriendAccepted,
	chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_INVITATION:         notification.TypeTayfaInvitation,
}

var targetTypes = map[chatv1.NotificationTargetType]string{
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_TAYFA:        notification.TargetTayfa,
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_MEMBERSHIP:   notification.TargetMembership,
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANNOUNCEMENT: notification.TargetAnnouncement,
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_EVENT:        notification.TargetEvent,
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANSWER:       notification.TargetAnswer,
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_USER:         notification.TargetUser,
}

var warningReasons = map[chatv1.TayfaWarningReason]string{
	chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_INACTIVE:    notification.ReasonInactive,
	chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_FEW_MEMBERS: notification.ReasonFewMembers,
}

// ApplyCoreEvent implements chatv1.CoreEventServiceServer.
func (s *CoreEventServer) ApplyCoreEvent(ctx context.Context, req *chatv1.ApplyCoreEventRequest) (*chatv1.ApplyCoreEventResponse, error) {
	// Defence in depth: the Guard has already checked the caller.
	if _, ok := auth.ServiceCallerFrom(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	e, err := coreEventFromProto(req)
	if err != nil {
		return nil, err
	}
	if err := s.app.Apply(ctx, e); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.ApplyCoreEventResponse{}, nil
}

func coreEventFromProto(req *chatv1.ApplyCoreEventRequest) (coreevents.Event, error) {
	var e coreevents.Event
	var ok bool
	if e.ID, ok = id.Parse(req.GetEventId()); !ok {
		return e, validation("event_id")
	}
	// Empty for a notification not about a tayfa; the app layer checks it
	// against the type.
	if t := req.GetTayfaId(); t != "" {
		if e.Tayfa, ok = id.Parse(t); !ok {
			return e, validation("tayfa_id")
		}
	}
	var actor id.ID
	if a := req.GetActorId(); a != "" {
		if actor, ok = id.Parse(a); !ok {
			return e, validation("actor_id")
		}
	}
	switch ev := req.GetEvent().(type) {
	case *chatv1.ApplyCoreEventRequest_NotificationRequested:
		r, err := notificationFromProto(ev.NotificationRequested)
		if err != nil {
			return e, err
		}
		r.Actor = actor
		e.Notification = &r
	case *chatv1.ApplyCoreEventRequest_TayfaPurged:
		e.Purged = true
	default:
		return e, validation("event")
	}
	return e, nil
}

func notificationFromProto(n *chatv1.NotificationRequested) (notification.Request, error) {
	var r notification.Request
	var ok bool
	if r.Type, ok = notificationTypes[n.GetType()]; !ok {
		return r, validation("notification_requested.type")
	}
	if r.TargetType, ok = targetTypes[n.GetTargetType()]; !ok {
		return r, validation("notification_requested.target_type")
	}
	if r.Target, ok = id.Parse(n.GetTargetId()); !ok {
		return r, validation("notification_requested.target_id")
	}
	if len(n.GetRecipientIds()) > notification.MaxRecipients {
		return r, validation("notification_requested.recipient_ids")
	}
	r.Recipients = make([]id.ID, 0, len(n.GetRecipientIds()))
	for _, s := range n.GetRecipientIds() {
		u, ok := id.Parse(s)
		if !ok {
			return r, validation("notification_requested.recipient_ids")
		}
		r.Recipients = append(r.Recipients, u)
	}
	if q := n.GetQuestionId(); q != "" {
		if r.Question, ok = id.Parse(q); !ok {
			return r, validation("notification_requested.question_id")
		}
	}
	if u := n.GetSubjectUserId(); u != "" {
		if r.Subject, ok = id.Parse(u); !ok {
			return r, validation("notification_requested.subject_user_id")
		}
	}
	if n.GetWarningReason() != chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_UNSPECIFIED {
		if r.Reason, ok = warningReasons[n.GetWarningReason()]; !ok {
			return r, validation("notification_requested.warning_reason")
		}
	}
	return r, nil
}
