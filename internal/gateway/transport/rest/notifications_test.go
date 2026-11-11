package rest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
)

const notificationID = "01965a3c-0000-7000-8000-000000000020"

type fakeChat struct {
	mu    sync.Mutex
	err   error
	calls int
	got   proto.Message
	token string
}

func (f *fakeChat) record(ctx context.Context, in proto.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.got, f.token = in, tokenFrom(ctx)
	return f.err
}

func (f *fakeChat) last() (proto.Message, string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got, f.token, f.calls
}

func (f *fakeChat) ListNotifications(ctx context.Context, in *chatv1.ListNotificationsRequest, _ ...grpc.CallOption) (*chatv1.ListNotificationsResponse, error) {
	return &chatv1.ListNotificationsResponse{NextPageToken: "SONRAKI", Notifications: []*chatv1.Notification{
		{Id: notificationID, Type: chatv1.NotificationType_NOTIFICATION_TYPE_ANSWER_RECEIVED, TayfaId: tayfaID, ActorId: userID,
			TargetType: chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANSWER, TargetId: answerID, QuestionId: questionID,
			CreateTime: timestamppb.New(t0)},
		{Id: notificationID, Type: chatv1.NotificationType(99), TargetType: chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_TAYFA,
			TargetId: tayfaID, Read: true, CreateTime: timestamppb.New(t0)},
		{Id: notificationID, Type: chatv1.NotificationType_NOTIFICATION_TYPE_HIKE_OVERDUE, TayfaId: tayfaID,
			TargetType: chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_EVENT, TargetId: eventID, SubjectUserId: userID, CreateTime: timestamppb.New(t0)},
		{Id: notificationID, Type: chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_WARNED, TayfaId: tayfaID,
			TargetType: chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_TAYFA, TargetId: tayfaID,
			WarningReason: chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_INACTIVE, CreateTime: timestamppb.New(t0)},
		{Id: notificationID, Type: chatv1.NotificationType_NOTIFICATION_TYPE_DM_MESSAGE,
			TargetType: chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_DM_CONVERSATION, TargetId: conversationID, UnreadCount: 100, CreateTime: timestamppb.New(t0)},
	}}, f.record(ctx, in)
}

func (f *fakeChat) GetUnreadNotificationCount(ctx context.Context, in *chatv1.GetUnreadNotificationCountRequest, _ ...grpc.CallOption) (*chatv1.GetUnreadNotificationCountResponse, error) {
	return &chatv1.GetUnreadNotificationCountResponse{UnreadCount: 12}, f.record(ctx, in)
}

func (f *fakeChat) MarkNotificationRead(ctx context.Context, in *chatv1.MarkNotificationReadRequest, _ ...grpc.CallOption) (*chatv1.MarkNotificationReadResponse, error) {
	return &chatv1.MarkNotificationReadResponse{}, f.record(ctx, in)
}

func (f *fakeChat) MarkAllNotificationsRead(ctx context.Context, in *chatv1.MarkAllNotificationsReadRequest, _ ...grpc.CallOption) (*chatv1.MarkAllNotificationsReadResponse, error) {
	return &chatv1.MarkAllNotificationsReadResponse{}, f.record(ctx, in)
}

func (f *fakeChat) ListNotificationPreferences(ctx context.Context, in *chatv1.ListNotificationPreferencesRequest, _ ...grpc.CallOption) (*chatv1.ListNotificationPreferencesResponse, error) {
	return &chatv1.ListNotificationPreferencesResponse{Preferences: []*chatv1.NotificationPreference{{
		ScopeType: chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_TAYFA, ScopeId: tayfaID,
		Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE, UpdateTime: timestamppb.New(t0),
	}}}, f.record(ctx, in)
}

func (f *fakeChat) SetTayfaNotificationPreference(ctx context.Context, in *chatv1.SetTayfaNotificationPreferenceRequest, _ ...grpc.CallOption) (*chatv1.SetTayfaNotificationPreferenceResponse, error) {
	return &chatv1.SetTayfaNotificationPreferenceResponse{Preference: &chatv1.NotificationPreference{
		ScopeType: chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_TAYFA, ScopeId: in.GetTayfaId(), Level: in.GetLevel(),
		MuteUntil: in.GetMuteUntil(), UpdateTime: timestamppb.New(t0),
	}}, f.record(ctx, in)
}

func (f *fakeChat) SetConversationNotificationPreference(ctx context.Context, in *chatv1.SetConversationNotificationPreferenceRequest, _ ...grpc.CallOption) (*chatv1.SetConversationNotificationPreferenceResponse, error) {
	return &chatv1.SetConversationNotificationPreferenceResponse{Preference: &chatv1.NotificationPreference{
		ScopeType: chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_DM_CONVERSATION, ScopeId: in.GetConversationId(), Level: in.GetLevel(),
		MuteUntil: in.GetMuteUntil(), UpdateTime: timestamppb.New(t0),
	}}, f.record(ctx, in)
}

func testChatDeps(f *fakeChat) ChatDeps {
	return ChatDeps{
		Notifications: f, Channels: f, DirectMessages: f, E2E: f, Push: f, RealtimeURL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"},
		Verifier: fakeVerifier{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func chatRouter(f *fakeChat) http.Handler {
	return NewRouter(Deps{Version: "test", Chat: testChatDeps(f)})
}

func chatErr(code codes.Code, reason string) error {
	st, _ := status.New(code, "istek reddedildi").WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "chat.tayfam.app"})
	return st.Err()
}

func TestNotificationRoutes(t *testing.T) {
	f := &fakeChat{}
	h := chatRouter(f)
	pref := "/v1/notification-preferences/tayfas/" + tayfaID
	convPref := "/v1/notification-preferences/conversations/" + conversationID
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", "/v1/notifications?page_size=20&page_token=P", "", 200, &chatv1.ListNotificationsRequest{PageSize: 20, PageToken: "P"}},
		{"GET", "/v1/notifications/unread-count", "", 200, &chatv1.GetUnreadNotificationCountRequest{}},
		{"PUT", "/v1/notifications/" + notificationID + "/read", "", 204, &chatv1.MarkNotificationReadRequest{NotificationId: notificationID}},
		{"POST", "/v1/notifications/read-all", "", 204, &chatv1.MarkAllNotificationsReadRequest{}},
		{"GET", "/v1/notification-preferences", "", 200, &chatv1.ListNotificationPreferencesRequest{}},
		{"PUT", pref, `{"level":"none","mute_until":"2030-01-02T03:04:05Z"}`, 200, &chatv1.SetTayfaNotificationPreferenceRequest{
			TayfaId: tayfaID, Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE, MuteUntil: &timestamppb.Timestamp{Seconds: 1893553445}}},
		{"PUT", pref, `{"level":"mentions"}`, 200, &chatv1.SetTayfaNotificationPreferenceRequest{TayfaId: tayfaID, Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_MENTIONS}},
		{"DELETE", pref, "", 204, &chatv1.SetTayfaNotificationPreferenceRequest{TayfaId: tayfaID, Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_ALL}},
		{"PUT", convPref, `{"level":"none","mute_until":"2030-01-02T03:04:05Z"}`, 200, &chatv1.SetConversationNotificationPreferenceRequest{
			ConversationId: conversationID, Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE, MuteUntil: &timestamppb.Timestamp{Seconds: 1893553445}}},
		{"PUT", convPref, `{"level":"mentions"}`, 200, &chatv1.SetConversationNotificationPreferenceRequest{ConversationId: conversationID, Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_MENTIONS}},
		{"DELETE", convPref, "", 204, &chatv1.SetConversationNotificationPreferenceRequest{ConversationId: conversationID, Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_ALL}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, tok, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) || tok != "USERTOKEN" {
			t.Errorf("%s %s: %d %s; chat'e giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}

	w := as(h, "GET", "/v1/notifications", "")
	l := decodeBody[notificationListJSON](t, w)
	n := l.Notifications
	if len(n) != 5 || n[4].Type != "dm_message" || n[4].TargetType != "dm_conversation" || n[4].UnreadCount != 100 || n[4].ActorID != "" || n[0].UnreadCount != 0 ||
		n[2].Type != "hike_overdue" || n[2].SubjectUserID != userID || n[3].Type != "tayfa_warned" || n[3].WarningReason != "inactive" || n[0].WarningReason != "" ||
		l.NextPageToken != "SONRAKI" || n[0].Type != "answer_received" || n[0].TargetType != "answer" ||
		n[0].QuestionID != questionID || n[0].ActorID != userID || n[0].Read || !n[0].CreatedAt.Equal(t0) ||
		n[1].Type != "unknown" || !n[1].Read || n[1].TayfaID != "" {
		t.Fatalf("liste: %+v", l)
	}
	if strings.Count(w.Body.String(), "question_id") != 1 || strings.Count(w.Body.String(), "warning_reason") != 1 || strings.Count(w.Body.String(), "unread_count") != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("boş alanlar/önbellek: %s %v", w.Body, w.Header())
	}
	if c := decodeBody[unreadCountJSON](t, as(h, "GET", "/v1/notifications/unread-count", "")); c.UnreadCount != 12 {
		t.Fatalf("okunmamış: %+v", c)
	}
	// The DM preference answers like the tayfa one, with its own scope.
	w = as(h, "PUT", convPref, `{"level":"none","mute_until":"2030-01-02T03:04:05Z"}`)
	cp := decodeBody[notificationPreferenceJSON](t, w)
	if cp.ScopeType != "dm_conversation" || cp.ScopeID != conversationID || cp.Level != "none" || cp.MuteUntil == nil || !cp.MuteUntil.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("konuşma tercihi: %s", w.Body)
	}
	if w := as(h, "DELETE", convPref, ""); w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("konuşma tercihi sıfırlama: %d %s", w.Code, w.Body)
	}
	p := decodeBody[notificationPreferenceListJSON](t, as(h, "GET", "/v1/notification-preferences", ""))
	if len(p.Preferences) != 1 || p.Preferences[0].Level != "none" || p.Preferences[0].ScopeType != "tayfa" || p.Preferences[0].MuteUntil != nil {
		t.Fatalf("tercihler: %+v", p)
	}
}

func TestNotificationBadRequests(t *testing.T) {
	f := &fakeChat{}
	h := chatRouter(f)
	pref := "/v1/notification-preferences/tayfas/" + tayfaID
	convPref := "/v1/notification-preferences/conversations/" + conversationID
	for name, tc := range map[string]struct{ method, path, body string }{
		"bozuk bildirim id":  {"PUT", "/v1/notifications/x/read", ""},
		"bozuk sayfa":        {"GET", "/v1/notifications?page_size=101", ""},
		"bozuk tayfa":        {"PUT", "/v1/notification-preferences/tayfas/x", `{"level":"none"}`},
		"seviye yok":         {"PUT", pref, `{}`},
		"bilinmeyen seviye":  {"PUT", pref, `{"level":"loud"}`},
		"bozuk süre":         {"PUT", pref, `{"level":"none","mute_until":"yarın"}`},
		"başkası adına":      {"PUT", pref, `{"level":"none","user_id":"` + userID + `"}`},
		"bozuk konuşma":      {"PUT", "/v1/notification-preferences/conversations/x", `{"level":"none"}`},
		"bozuk konuşma sil":  {"DELETE", "/v1/notification-preferences/conversations/x", ""},
		"konuşma seviyesiz":  {"PUT", convPref, `{}`},
		"konuşma bilinmeyen": {"PUT", convPref, `{"level":"loud"}`},
		"konuşma bozuk süre": {"PUT", convPref, `{"level":"none","mute_until":"yarın"}`},
		"konuşma başkası":    {"PUT", convPref, `{"level":"none","user_id":"` + userID + `"}`},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek chat'e gitti")
	}
	if w := do(h, "GET", "/v1/notifications", "", nil); w.Code != 401 {
		t.Fatalf("token'sız: %d", w.Code)
	}
}

func TestNotificationErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		typ    string
	}{
		{chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_NOTIFICATION_NOT_FOUND.String()), 404, "notification-not-found"},
		{chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String()), 404, "tayfa-not-found"},
		{chatErr(codes.InvalidArgument, chatv1.ErrorReason_ERROR_REASON_VALIDATION.String()), 400, "invalid-request"},
		// A reason from another domain is not trusted.
		{coreErr(codes.NotFound, "ERROR_REASON_NOTIFICATION_NOT_FOUND"), 500, "internal"},
	} {
		f := &fakeChat{err: tc.err}
		w := as(chatRouter(f), "PUT", "/v1/notifications/"+notificationID+"/read", "")
		if w.Code != tc.status || problemType(t, w) != tc.typ {
			t.Errorf("%v: %d %s", tc.err, w.Code, problemType(t, w))
		}
	}
}

// Another user's conversation is answered like a missing one: chat's
// CONVERSATION_NOT_FOUND becomes 404 conversation-not-found, nothing else
// (issue #138).
func TestConversationPreferenceErrors(t *testing.T) {
	path := "/v1/notification-preferences/conversations/" + conversationID
	for _, tc := range []struct {
		method, body string
		err          error
		status       int
		typ          string
	}{
		{"PUT", `{"level":"none"}`, chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_CONVERSATION_NOT_FOUND.String()), 404, "conversation-not-found"},
		{"DELETE", "", chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_CONVERSATION_NOT_FOUND.String()), 404, "conversation-not-found"},
		{"PUT", `{"level":"all","mute_until":"2030-01-02T03:04:05Z"}`, chatErr(codes.InvalidArgument, chatv1.ErrorReason_ERROR_REASON_VALIDATION.String()), 400, "invalid-request"},
		{"PUT", `{"level":"none"}`, status.Error(codes.Unavailable, "yok"), 503, "unavailable"},
	} {
		f := &fakeChat{err: tc.err}
		w := as(chatRouter(f), tc.method, path, tc.body)
		if w.Code != tc.status || problemType(t, w) != tc.typ {
			t.Errorf("%s %v: %d %s", tc.method, tc.err, w.Code, problemType(t, w))
		}
		if strings.Contains(w.Body.String(), conversationID) {
			t.Errorf("%s: hata cevabı konuşma kimliğini taşıyor: %s", tc.method, w.Body)
		}
	}
}
