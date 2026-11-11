package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Notifications is what NotificationServer needs from the app layer.
type Notifications interface {
	List(ctx context.Context, caller id.ID, size int32, token string) ([]notification.Notification, string, error)
	UnreadCount(ctx context.Context, caller id.ID) (int32, error)
	MarkRead(ctx context.Context, caller, notification id.ID) error
	MarkAllRead(ctx context.Context, caller id.ID) error
	Preferences(ctx context.Context, caller id.ID) ([]notification.Preference, error)
	SetTayfaPreference(ctx context.Context, caller, tayfa id.ID, level string, until time.Time) (notification.Preference, error)
	SetConversationPreference(ctx context.Context, caller, conversation id.ID, level string, until time.Time) (notification.Preference, error)
}

// NotificationServer implements chatv1.NotificationServiceServer. Every
// method acts for the caller of the verified access token only.
type NotificationServer struct {
	chatv1.UnimplementedNotificationServiceServer
	app Notifications
	log *slog.Logger
}

// NewNotificationServer returns a NotificationServer.
func NewNotificationServer(app Notifications, log *slog.Logger) *NotificationServer {
	return &NotificationServer{app: app, log: log}
}

var levels = map[chatv1.NotificationLevel]string{
	chatv1.NotificationLevel_NOTIFICATION_LEVEL_ALL:      notification.LevelAll,
	chatv1.NotificationLevel_NOTIFICATION_LEVEL_MENTIONS: notification.LevelMentions,
	chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE:     notification.LevelNone,
}

// toProto returns the key of m whose value is v (the zero key if none).
func toProto[K comparable](m map[K]string, v string) K {
	for k, s := range m {
		if s == v {
			return k
		}
	}
	var zero K
	return zero
}

func levelToProto(s string) chatv1.NotificationLevel {
	for k, v := range levels {
		if v == s {
			return k
		}
	}
	return chatv1.NotificationLevel_NOTIFICATION_LEVEL_UNSPECIFIED
}

func scopeToProto(s string) chatv1.NotificationScopeType {
	switch s {
	case "tayfa":
		return chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_TAYFA
	case "channel":
		return chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_CHANNEL
	case "dm_conversation":
		return chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_DM_CONVERSATION
	}
	return chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_UNSPECIFIED
}

func idOrEmpty(v id.ID) string {
	if v == (id.ID{}) {
		return ""
	}
	return v.String()
}

// ListNotifications implements chatv1.NotificationServiceServer.
func (s *NotificationServer) ListNotifications(ctx context.Context, req *chatv1.ListNotificationsRequest) (*chatv1.ListNotificationsResponse, error) {
	caller, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.List(ctx, caller, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListNotificationsResponse{Notifications: make([]*chatv1.Notification, 0, len(list)), NextPageToken: next}
	for _, n := range list {
		out.Notifications = append(out.Notifications, &chatv1.Notification{
			Id: n.ID.String(), Type: toProto(listedTypes, n.Type), TayfaId: idOrEmpty(n.Tayfa), ActorId: idOrEmpty(n.Actor),
			TargetType: toProto(listedTargets, n.TargetType), TargetId: n.Target.String(), QuestionId: idOrEmpty(n.Question),
			Read: !n.ReadAt.IsZero(), CreateTime: timestamppb.New(n.CreatedAt),
			SubjectUserId: idOrEmpty(n.Subject), WarningReason: toProto(warningReasons, n.Reason),
			UnreadCount: n.Unread,
		})
	}
	return out, nil
}

// GetUnreadNotificationCount implements chatv1.NotificationServiceServer.
func (s *NotificationServer) GetUnreadNotificationCount(ctx context.Context, _ *chatv1.GetUnreadNotificationCountRequest) (*chatv1.GetUnreadNotificationCountResponse, error) {
	caller, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.app.UnreadCount(ctx, caller)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.GetUnreadNotificationCountResponse{UnreadCount: n}, nil
}

// MarkNotificationRead implements chatv1.NotificationServiceServer.
func (s *NotificationServer) MarkNotificationRead(ctx context.Context, req *chatv1.MarkNotificationReadRequest) (*chatv1.MarkNotificationReadResponse, error) {
	caller, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	n, ok := id.Parse(req.GetNotificationId())
	if !ok {
		return nil, validation("notification_id")
	}
	if err := s.app.MarkRead(ctx, caller, n); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.MarkNotificationReadResponse{}, nil
}

// MarkAllNotificationsRead implements chatv1.NotificationServiceServer.
func (s *NotificationServer) MarkAllNotificationsRead(ctx context.Context, _ *chatv1.MarkAllNotificationsReadRequest) (*chatv1.MarkAllNotificationsReadResponse, error) {
	caller, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.app.MarkAllRead(ctx, caller); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.MarkAllNotificationsReadResponse{}, nil
}

// ListNotificationPreferences implements chatv1.NotificationServiceServer.
func (s *NotificationServer) ListNotificationPreferences(ctx context.Context, _ *chatv1.ListNotificationPreferencesRequest) (*chatv1.ListNotificationPreferencesResponse, error) {
	caller, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	prefs, err := s.app.Preferences(ctx, caller)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListNotificationPreferencesResponse{Preferences: make([]*chatv1.NotificationPreference, 0, len(prefs))}
	for _, p := range prefs {
		out.Preferences = append(out.Preferences, preferenceToProto(p))
	}
	return out, nil
}

// SetTayfaNotificationPreference implements chatv1.NotificationServiceServer.
func (s *NotificationServer) SetTayfaNotificationPreference(ctx context.Context, req *chatv1.SetTayfaNotificationPreferenceRequest) (*chatv1.SetTayfaNotificationPreferenceResponse, error) {
	caller, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, ok := id.Parse(req.GetTayfaId())
	if !ok {
		return nil, validation("tayfa_id")
	}
	level, until, err := preferenceInput(req.GetLevel(), req.GetMuteUntil())
	if err != nil {
		return nil, err
	}
	p, err := s.app.SetTayfaPreference(ctx, caller, tayfa, level, until)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.SetTayfaNotificationPreferenceResponse{Preference: preferenceToProto(p)}, nil
}

// SetConversationNotificationPreference implements
// chatv1.NotificationServiceServer.
func (s *NotificationServer) SetConversationNotificationPreference(ctx context.Context, req *chatv1.SetConversationNotificationPreferenceRequest) (*chatv1.SetConversationNotificationPreferenceResponse, error) {
	caller, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	conversation, ok := id.Parse(req.GetConversationId())
	if !ok {
		return nil, validation("conversation_id")
	}
	level, until, err := preferenceInput(req.GetLevel(), req.GetMuteUntil())
	if err != nil {
		return nil, err
	}
	p, err := s.app.SetConversationPreference(ctx, caller, conversation, level, until)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.SetConversationNotificationPreferenceResponse{Preference: preferenceToProto(p)}, nil
}

// preferenceInput converts the level and the optional mute_until of a
// preference request.
func preferenceInput(l chatv1.NotificationLevel, mu *timestamppb.Timestamp) (string, time.Time, error) {
	level, ok := levels[l]
	if !ok {
		return "", time.Time{}, validation("level")
	}
	var until time.Time
	if mu != nil {
		if err := mu.CheckValid(); err != nil {
			return "", time.Time{}, validation("mute_until")
		}
		until = mu.AsTime()
	}
	return level, until, nil
}

func preferenceToProto(p notification.Preference) *chatv1.NotificationPreference {
	out := &chatv1.NotificationPreference{
		ScopeType: scopeToProto(p.ScopeType), ScopeId: p.Scope.String(), Level: levelToProto(p.Level),
		UpdateTime: timestamppb.New(p.UpdatedAt),
	}
	if !p.MutedUntil.IsZero() {
		out.MuteUntil = timestamppb.New(p.MutedUntil)
	}
	return out
}
