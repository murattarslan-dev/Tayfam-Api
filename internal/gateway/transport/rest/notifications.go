package rest

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
)

// NotificationClient is chat's NotificationService as the gateway uses it.
type NotificationClient interface {
	ListNotifications(ctx context.Context, in *chatv1.ListNotificationsRequest, opts ...grpc.CallOption) (*chatv1.ListNotificationsResponse, error)
	GetUnreadNotificationCount(ctx context.Context, in *chatv1.GetUnreadNotificationCountRequest, opts ...grpc.CallOption) (*chatv1.GetUnreadNotificationCountResponse, error)
	MarkNotificationRead(ctx context.Context, in *chatv1.MarkNotificationReadRequest, opts ...grpc.CallOption) (*chatv1.MarkNotificationReadResponse, error)
	MarkAllNotificationsRead(ctx context.Context, in *chatv1.MarkAllNotificationsReadRequest, opts ...grpc.CallOption) (*chatv1.MarkAllNotificationsReadResponse, error)
	ListNotificationPreferences(ctx context.Context, in *chatv1.ListNotificationPreferencesRequest, opts ...grpc.CallOption) (*chatv1.ListNotificationPreferencesResponse, error)
	SetTayfaNotificationPreference(ctx context.Context, in *chatv1.SetTayfaNotificationPreferenceRequest, opts ...grpc.CallOption) (*chatv1.SetTayfaNotificationPreferenceResponse, error)
}

// ChatDeps configures the notification (issue #64) and channel (issue #81)
// routes.
type ChatDeps struct {
	Notifications NotificationClient
	// Channels enables the channel routes when set.
	Channels ChannelClient
	// DirectMessages enables the direct message routes when set (issue
	// #95).
	DirectMessages DirectMessageClient
	// E2E enables the end-to-end encryption routes when set (issue #127).
	E2E E2EClient
	// Push enables the push token routes when set (issue #134).
	Push PushClient
	// RealtimeURL (chat's HTTP port) enables the WebSocket route (issue
	// #82).
	RealtimeURL *url.URL
	// Verifier checks the user's token before anything reaches chat; chat
	// checks it again itself (ADR-0011).
	Verifier TokenVerifier
	Log      *slog.Logger
}

// maxPreferenceBody fits a level and a timestamp.
const maxPreferenceBody = 1 << 10

type chatHandlers struct {
	d ChatDeps
}

func registerNotifications(mux *http.ServeMux, d ChatDeps) {
	h := chatHandlers{d: d}
	a := func(next coreHandler) http.HandlerFunc { return authenticated(d.Verifier, d.Log, next) }
	mux.HandleFunc("GET /v1/notifications", a(h.list))
	mux.HandleFunc("GET /v1/notifications/unread-count", a(h.unreadCount))
	mux.HandleFunc("POST /v1/notifications/read-all", a(h.markAllRead))
	mux.HandleFunc("PUT /v1/notifications/{notification_id}/read", a(h.markRead))
	mux.HandleFunc("GET /v1/notification-preferences", a(h.preferences))
	mux.HandleFunc("PUT /v1/notification-preferences/tayfas/{tayfa_id}", a(h.setTayfaPreference))
	mux.HandleFunc("DELETE /v1/notification-preferences/tayfas/{tayfa_id}", a(h.resetTayfaPreference))
}

func (h chatHandlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	writeUpstreamError(w, r, h.d.Log, "chat", err)
}

var notificationTypeNames = map[chatv1.NotificationType]string{
	chatv1.NotificationType_NOTIFICATION_TYPE_EVENT_CREATED:            "event_created",
	chatv1.NotificationType_NOTIFICATION_TYPE_EVENT_UPDATED:            "event_updated",
	chatv1.NotificationType_NOTIFICATION_TYPE_EVENT_CANCELLED:          "event_cancelled",
	chatv1.NotificationType_NOTIFICATION_TYPE_RSVP_PROMOTED:            "rsvp_promoted",
	chatv1.NotificationType_NOTIFICATION_TYPE_ANNOUNCEMENT:             "announcement",
	chatv1.NotificationType_NOTIFICATION_TYPE_ANSWER_RECEIVED:          "answer_received",
	chatv1.NotificationType_NOTIFICATION_TYPE_JOIN_REQUEST:             "join_request",
	chatv1.NotificationType_NOTIFICATION_TYPE_JOIN_APPROVED:            "join_approved",
	chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_DELETION_REQUESTED: "tayfa_deletion_requested",
	chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_WARNED:             "tayfa_warned",
	chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_ARCHIVED:           "tayfa_archived",
	chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_SUSPENDED:          "tayfa_suspended",
	chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_UNSUSPENDED:        "tayfa_unsuspended",
	chatv1.NotificationType_NOTIFICATION_TYPE_HIKE_OVERDUE:             "hike_overdue",
	chatv1.NotificationType_NOTIFICATION_TYPE_FRIEND_REQUEST:           "friend_request",
	chatv1.NotificationType_NOTIFICATION_TYPE_FRIEND_ACCEPTED:          "friend_accepted",
	chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_INVITATION:         "tayfa_invitation",
	chatv1.NotificationType_NOTIFICATION_TYPE_REPORT_RESOLVED:          "report_resolved",
	chatv1.NotificationType_NOTIFICATION_TYPE_DM_REQUEST:               "dm_request",
	chatv1.NotificationType_NOTIFICATION_TYPE_DM_MESSAGE:               "dm_message",
}

var warningReasonNames = map[chatv1.TayfaWarningReason]string{
	chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_INACTIVE:    "inactive",
	chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_FEW_MEMBERS: "few_members",
}

var notificationTargetNames = map[chatv1.NotificationTargetType]string{
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_TAYFA:           "tayfa",
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_MEMBERSHIP:      "membership",
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANNOUNCEMENT:    "announcement",
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_EVENT:           "event",
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANSWER:          "answer",
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_USER:            "user",
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_REPORT:          "report",
	chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_DM_CONVERSATION: "dm_conversation",
}

var notificationLevelNames = map[chatv1.NotificationLevel]string{
	chatv1.NotificationLevel_NOTIFICATION_LEVEL_ALL:      "all",
	chatv1.NotificationLevel_NOTIFICATION_LEVEL_MENTIONS: "mentions",
	chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE:     "none",
}

var notificationScopeNames = map[chatv1.NotificationScopeType]string{
	chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_TAYFA:           "tayfa",
	chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_CHANNEL:         "channel",
	chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_DM_CONVERSATION: "dm_conversation",
}

// nameOr returns m[k], or "unknown" for a value this gateway does not know
// (a newer chat during a rollout).
func nameOr[K comparable](m map[K]string, k K) string {
	if v, ok := m[k]; ok {
		return v
	}
	return "unknown"
}

type notificationJSON struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	TayfaID    string `json:"tayfa_id,omitempty"`
	ActorID    string `json:"actor_id,omitempty"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	QuestionID string `json:"question_id,omitempty"`
	// hike_overdue: the hiker.
	SubjectUserID string `json:"subject_user_id,omitempty"`
	// tayfa_warned only.
	WarningReason string `json:"warning_reason,omitempty"`
	// dm_request and dm_message only: unread messages in the conversation
	// (issue #101).
	UnreadCount int32     `json:"unread_count,omitempty"`
	Read        bool      `json:"read"`
	CreatedAt   time.Time `json:"created_at"`
}

type notificationListJSON struct {
	Notifications []notificationJSON `json:"notifications"`
	NextPageToken string             `json:"next_page_token,omitempty"`
}

type unreadCountJSON struct {
	UnreadCount int32 `json:"unread_count"`
}

type notificationPreferenceJSON struct {
	ScopeType string     `json:"scope_type"`
	ScopeID   string     `json:"scope_id"`
	Level     string     `json:"level"`
	MuteUntil *time.Time `json:"mute_until,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type notificationPreferenceListJSON struct {
	Preferences []notificationPreferenceJSON `json:"preferences"`
}

func preferenceFrom(p *chatv1.NotificationPreference) notificationPreferenceJSON {
	return notificationPreferenceJSON{
		ScopeType: nameOr(notificationScopeNames, p.GetScopeType()), ScopeID: p.GetScopeId(),
		Level: nameOr(notificationLevelNames, p.GetLevel()), MuteUntil: optTime(p.GetMuteUntil()),
		UpdatedAt: p.GetUpdateTime().AsTime(),
	}
}

func (h chatHandlers) list(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Notifications.ListNotifications(ctx, &chatv1.ListNotificationsRequest{PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := notificationListJSON{Notifications: make([]notificationJSON, 0, len(resp.GetNotifications())), NextPageToken: resp.GetNextPageToken()}
	for _, n := range resp.GetNotifications() {
		reason := ""
		if n.GetWarningReason() != chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_UNSPECIFIED {
			reason = nameOr(warningReasonNames, n.GetWarningReason())
		}
		out.Notifications = append(out.Notifications, notificationJSON{
			ID: n.GetId(), Type: nameOr(notificationTypeNames, n.GetType()), TayfaID: n.GetTayfaId(), ActorID: n.GetActorId(),
			TargetType: nameOr(notificationTargetNames, n.GetTargetType()), TargetID: n.GetTargetId(), QuestionID: n.GetQuestionId(),
			SubjectUserID: n.GetSubjectUserId(), WarningReason: reason, UnreadCount: n.GetUnreadCount(),
			Read: n.GetRead(), CreatedAt: n.GetCreateTime().AsTime(),
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (h chatHandlers) unreadCount(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Notifications.GetUnreadNotificationCount(ctx, &chatv1.GetUnreadNotificationCountRequest{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, unreadCountJSON{UnreadCount: resp.GetUnreadCount()})
}

func (h chatHandlers) markRead(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "notification_id")
	if !ok {
		return
	}
	if _, err := h.d.Notifications.MarkNotificationRead(ctx, &chatv1.MarkNotificationReadRequest{NotificationId: id}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h chatHandlers) markAllRead(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if _, err := h.d.Notifications.MarkAllNotificationsRead(ctx, &chatv1.MarkAllNotificationsReadRequest{}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h chatHandlers) preferences(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Notifications.ListNotificationPreferences(ctx, &chatv1.ListNotificationPreferencesRequest{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := notificationPreferenceListJSON{Preferences: make([]notificationPreferenceJSON, 0, len(resp.GetPreferences()))}
	for _, p := range resp.GetPreferences() {
		out.Preferences = append(out.Preferences, preferenceFrom(p))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

type preferenceBody struct {
	Level     string     `json:"level"`
	MuteUntil *time.Time `json:"mute_until"`
}

func (h chatHandlers) setTayfaPreference(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b preferenceBody
	if !decodeLimit(w, r, &b, maxPreferenceBody) {
		return
	}
	var level chatv1.NotificationLevel
	for k, v := range notificationLevelNames {
		if v == b.Level {
			level = k
		}
	}
	if level == chatv1.NotificationLevel_NOTIFICATION_LEVEL_UNSPECIFIED {
		badRequest(w, r)
		return
	}
	req := &chatv1.SetTayfaNotificationPreferenceRequest{TayfaId: id, Level: level}
	if b.MuteUntil != nil {
		req.MuteUntil = timestamppb.New(*b.MuteUntil)
	}
	h.setPreference(ctx, w, r, req, http.StatusOK)
}

func (h chatHandlers) resetTayfaPreference(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	h.setPreference(ctx, w, r, &chatv1.SetTayfaNotificationPreferenceRequest{TayfaId: id, Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_ALL}, http.StatusNoContent)
}

func (h chatHandlers) setPreference(ctx context.Context, w http.ResponseWriter, r *http.Request, req *chatv1.SetTayfaNotificationPreferenceRequest, code int) {
	resp, err := h.d.Notifications.SetTayfaNotificationPreference(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if code == http.StatusNoContent {
		w.WriteHeader(code)
		return
	}
	writeJSON(w, code, preferenceFrom(resp.GetPreference()))
}
