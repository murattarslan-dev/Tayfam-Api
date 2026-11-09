package rest

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
)

const (
	channelID = "01965a3c-0000-7000-8000-000000000030"
	messageID = "01965a3c-0000-7000-8000-000000000031"
	requestID = "01965a3c-0000-7000-8000-000000000032"
)

func testChannel() *chatv1.Channel {
	return &chatv1.Channel{
		Id: channelID, TayfaId: tayfaID, Name: "genel", Visibility: chatv1.ChannelVisibility_CHANNEL_VISIBILITY_STAFF,
		SlowModeSeconds: 30, CreatorUserId: userID, CreateTime: timestamppb.New(t0), UpdateTime: timestamppb.New(t0),
		UnreadCount: 100, LastReadMessageId: messageID,
	}
}

func testMessage(body string) *chatv1.Message {
	return &chatv1.Message{Id: messageID, ChannelId: channelID, AuthorUserId: userID, Body: body, CreateTime: timestamppb.New(t0)}
}

func (f *fakeChat) ListChannels(ctx context.Context, in *chatv1.ListChannelsRequest, _ ...grpc.CallOption) (*chatv1.ListChannelsResponse, error) {
	return &chatv1.ListChannelsResponse{Channels: []*chatv1.Channel{testChannel(), {Id: channelID, Visibility: 9, ArchiveTime: timestamppb.New(t0)}}}, f.record(ctx, in)
}

func (f *fakeChat) GetChannel(ctx context.Context, in *chatv1.GetChannelRequest, _ ...grpc.CallOption) (*chatv1.GetChannelResponse, error) {
	return &chatv1.GetChannelResponse{Channel: testChannel()}, f.record(ctx, in)
}

func (f *fakeChat) CreateChannel(ctx context.Context, in *chatv1.CreateChannelRequest, _ ...grpc.CallOption) (*chatv1.CreateChannelResponse, error) {
	return &chatv1.CreateChannelResponse{Channel: testChannel()}, f.record(ctx, in)
}

func (f *fakeChat) UpdateChannel(ctx context.Context, in *chatv1.UpdateChannelRequest, _ ...grpc.CallOption) (*chatv1.UpdateChannelResponse, error) {
	return &chatv1.UpdateChannelResponse{Channel: testChannel()}, f.record(ctx, in)
}

func (f *fakeChat) ArchiveChannel(ctx context.Context, in *chatv1.ArchiveChannelRequest, _ ...grpc.CallOption) (*chatv1.ArchiveChannelResponse, error) {
	c := testChannel()
	c.ArchiveTime = timestamppb.New(t0)
	return &chatv1.ArchiveChannelResponse{Channel: c}, f.record(ctx, in)
}

func (f *fakeChat) UnarchiveChannel(ctx context.Context, in *chatv1.UnarchiveChannelRequest, _ ...grpc.CallOption) (*chatv1.UnarchiveChannelResponse, error) {
	return &chatv1.UnarchiveChannelResponse{Channel: testChannel()}, f.record(ctx, in)
}

func (f *fakeChat) SendMessage(ctx context.Context, in *chatv1.SendMessageRequest, _ ...grpc.CallOption) (*chatv1.SendMessageResponse, error) {
	return &chatv1.SendMessageResponse{Message: testMessage(in.GetBody())}, f.record(ctx, in)
}

func (f *fakeChat) ListMessages(ctx context.Context, in *chatv1.ListMessagesRequest, _ ...grpc.CallOption) (*chatv1.ListMessagesResponse, error) {
	return &chatv1.ListMessagesResponse{NextPageToken: "SONRAKI", Messages: []*chatv1.Message{
		testMessage("merhaba"),
		{Id: messageID, ChannelId: channelID, Deleted: true, Restorable: true, CreateTime: timestamppb.New(t0)},
	}}, f.record(ctx, in)
}

func (f *fakeChat) DeleteMessage(ctx context.Context, in *chatv1.DeleteMessageRequest, _ ...grpc.CallOption) (*chatv1.DeleteMessageResponse, error) {
	return &chatv1.DeleteMessageResponse{}, f.record(ctx, in)
}

func (f *fakeChat) RestoreMessage(ctx context.Context, in *chatv1.RestoreMessageRequest, _ ...grpc.CallOption) (*chatv1.RestoreMessageResponse, error) {
	return &chatv1.RestoreMessageResponse{Message: testMessage("geri geldi")}, f.record(ctx, in)
}

func (f *fakeChat) MarkChannelRead(ctx context.Context, in *chatv1.MarkChannelReadRequest, _ ...grpc.CallOption) (*chatv1.MarkChannelReadResponse, error) {
	return &chatv1.MarkChannelReadResponse{}, f.record(ctx, in)
}

func TestChannelRoutes(t *testing.T) {
	f := &fakeChat{}
	h := chatRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/channels"
	ch := base + "/" + channelID
	mask := func(p ...string) *fieldmaskpb.FieldMask { return &fieldmaskpb.FieldMask{Paths: p} }
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", base, "", 200, &chatv1.ListChannelsRequest{TayfaId: tayfaID}},
		{"POST", base, `{"name":"genel"}`, 201, &chatv1.CreateChannelRequest{TayfaId: tayfaID, Name: "genel"}},
		{"POST", base, `{"name":"yönetim","visibility":"staff","slow_mode_seconds":60}`, 201, &chatv1.CreateChannelRequest{
			TayfaId: tayfaID, Name: "yönetim", Visibility: chatv1.ChannelVisibility_CHANNEL_VISIBILITY_STAFF, SlowModeSeconds: 60}},
		{"GET", ch, "", 200, &chatv1.GetChannelRequest{TayfaId: tayfaID, ChannelId: channelID}},
		{"PATCH", ch, `{"slow_mode_seconds":0,"visibility":"members"}`, 200, &chatv1.UpdateChannelRequest{
			TayfaId: tayfaID, ChannelId: channelID, Visibility: chatv1.ChannelVisibility_CHANNEL_VISIBILITY_MEMBERS,
			UpdateMask: mask("visibility", "slow_mode_seconds")}},
		{"PUT", ch + "/archive", "", 200, &chatv1.ArchiveChannelRequest{TayfaId: tayfaID, ChannelId: channelID}},
		{"DELETE", ch + "/archive", "", 200, &chatv1.UnarchiveChannelRequest{TayfaId: tayfaID, ChannelId: channelID}},
		{"GET", ch + "/messages?page_size=20&page_token=P", "", 200, &chatv1.ListMessagesRequest{TayfaId: tayfaID, ChannelId: channelID, PageSize: 20, PageToken: "P"}},
		{"GET", ch + "/messages?after_message_id=" + messageID, "", 200, &chatv1.ListMessagesRequest{TayfaId: tayfaID, ChannelId: channelID, AfterMessageId: messageID}},
		{"POST", ch + "/messages", `{"body":"Kordon'da buluşalım","client_request_id":"` + requestID + `"}`, 201, &chatv1.SendMessageRequest{
			TayfaId: tayfaID, ChannelId: channelID, Body: "Kordon'da buluşalım", ClientRequestId: requestID}},
		{"DELETE", ch + "/messages/" + messageID, "", 204, &chatv1.DeleteMessageRequest{TayfaId: tayfaID, ChannelId: channelID, MessageId: messageID}},
		{"POST", ch + "/messages/" + messageID + "/restoration", "", 200, &chatv1.RestoreMessageRequest{TayfaId: tayfaID, ChannelId: channelID, MessageId: messageID}},
		{"PUT", ch + "/read", `{"message_id":"` + messageID + `"}`, 204, &chatv1.MarkChannelReadRequest{TayfaId: tayfaID, ChannelId: channelID, MessageId: messageID}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, tok, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) || tok != "USERTOKEN" {
			t.Errorf("%s %s: %d %s; chat'e giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}

	l := decodeBody[channelListJSON](t, as(h, "GET", base, ""))
	c := l.Channels
	if len(c) != 2 || c[0].Visibility != "staff" || c[0].UnreadCount == nil || *c[0].UnreadCount != 100 || c[0].LastReadMessageID != messageID ||
		c[0].CreatorUserID != userID || c[0].ArchivedAt != nil || c[1].Visibility != "unknown" || c[1].ArchivedAt == nil {
		t.Fatalf("liste: %+v", l)
	}
	// Single channel: no read fields.
	if w := as(h, "GET", ch, ""); strings.Contains(w.Body.String(), "unread_count") {
		t.Fatalf("tek kanal okunma alanı içeriyor: %s", w.Body)
	}
	w := as(h, "GET", ch+"/messages", "")
	m := decodeBody[messageListJSON](t, w)
	if len(m.Messages) != 2 || m.Messages[0].Body != "merhaba" || m.Messages[0].Deleted || !m.Messages[1].Deleted || m.Messages[1].Body != "" ||
		!m.Messages[1].Restorable || m.Messages[1].AuthorUserID != "" || m.NextPageToken != "SONRAKI" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("mesajlar: %+v %v", m, w.Header())
	}
	if strings.Count(w.Body.String(), `"body"`) != 1 || strings.Count(w.Body.String(), "restorable") != 1 {
		t.Fatalf("boş alanlar: %s", w.Body)
	}
}

func TestChannelBadRequests(t *testing.T) {
	f := &fakeChat{}
	h := chatRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/channels"
	ch := base + "/" + channelID
	long := strings.Repeat("ş", 41)
	for name, tc := range map[string]struct{ method, path, body string }{
		"bozuk tayfa":           {"GET", "/v1/tayfas/x/channels", ""},
		"bozuk kanal":           {"GET", base + "/x", ""},
		"bozuk mesaj":           {"DELETE", ch + "/messages/x", ""},
		"isim yok":              {"POST", base, `{}`},
		"uzun isim":             {"POST", base, `{"name":"` + long + `"}`},
		"bilinmeyen görünürlük": {"POST", base, `{"name":"a","visibility":"public"}`},
		"yavaş mod sınırı":      {"POST", base, `{"name":"a","slow_mode_seconds":3601}`},
		"boş güncelleme":        {"PATCH", ch, `{}`},
		"bilinmeyen alan":       {"PATCH", ch, `{"archived":true}`},
		"boş mesaj":             {"POST", ch + "/messages", `{"body":"","client_request_id":"` + requestID + `"}`},
		"uzun mesaj":            {"POST", ch + "/messages", `{"body":"` + strings.Repeat("a", 4001) + `","client_request_id":"` + requestID + `"}`},
		"istek kimliği yok":     {"POST", ch + "/messages", `{"body":"x"}`},
		"yazar alanı":           {"POST", ch + "/messages", `{"body":"x","client_request_id":"` + requestID + `","author_user_id":"` + userID + `"}`},
		"bozuk after":           {"GET", ch + "/messages?after_message_id=x", ""},
		"okundu kimliği yok":    {"PUT", ch + "/read", `{}`},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek chat'e gitti")
	}
	if w := do(h, "GET", base, "", nil); w.Code != 401 {
		t.Fatalf("token'sız: %d", w.Code)
	}
	if w := do(h, "POST", ch+"/messages", `{"body":"x","client_request_id":"`+requestID+`"}`, map[string]string{"Authorization": "Bearer USERTOKEN"}); w.Code != 400 {
		t.Fatalf("istemci başlığı olmadan: %d", w.Code)
	}
}

func slowModeErr(meta map[string]string) error {
	st, _ := status.New(codes.ResourceExhausted, "istek reddedildi").WithDetails(&errdetails.ErrorInfo{
		Reason: chatv1.ErrorReason_ERROR_REASON_SLOW_MODE.String(), Domain: "chat.tayfam.app", Metadata: meta,
	})
	return st.Err()
}

func TestChannelErrors(t *testing.T) {
	send := "/v1/tayfas/" + tayfaID + "/channels/" + channelID + "/messages"
	body := `{"body":"x","client_request_id":"` + requestID + `"}`
	for _, tc := range []struct {
		err    error
		status int
		typ    string
		retry  string
	}{
		{chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_CHANNEL_NOT_FOUND.String()), 404, "channel-not-found", ""},
		{chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_MESSAGE_NOT_FOUND.String()), 404, "message-not-found", ""},
		{chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String()), 404, "tayfa-not-found", ""},
		{chatErr(codes.PermissionDenied, chatv1.ErrorReason_ERROR_REASON_PERMISSION_DENIED.String()), 403, "permission-denied", ""},
		{chatErr(codes.PermissionDenied, chatv1.ErrorReason_ERROR_REASON_MUTED.String()), 403, "muted", ""},
		{chatErr(codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_CHANNEL_ARCHIVED.String()), 409, "channel-archived", ""},
		{chatErr(codes.AlreadyExists, chatv1.ErrorReason_ERROR_REASON_CHANNEL_NAME_TAKEN.String()), 409, "channel-name-taken", ""},
		{chatErr(codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_TAYFA_READ_ONLY.String()), 409, "tayfa-read-only", ""},
		{chatErr(codes.PermissionDenied, chatv1.ErrorReason_ERROR_REASON_ROLE_HIERARCHY.String()), 403, "role-hierarchy", ""},
		{slowModeErr(map[string]string{"retry_after_seconds": "12"}), 429, "slow-mode", "12"},
		// The shared per-user message rate (issue #101): no Retry-After.
		{chatErr(codes.ResourceExhausted, chatv1.ErrorReason_ERROR_REASON_RATE_LIMITED.String()), 429, "rate-limited", ""},
		// A bad or oversized value from chat is not passed on.
		{slowModeErr(map[string]string{"retry_after_seconds": "1e9"}), 429, "slow-mode", ""},
		{slowModeErr(map[string]string{"retry_after_seconds": "99999"}), 429, "slow-mode", ""},
		{status.Error(codes.Unavailable, "x"), 503, "unavailable", ""},
		// A reason from another domain is not trusted.
		{coreErr(codes.PermissionDenied, "ERROR_REASON_MUTED"), 500, "internal", ""},
	} {
		f := &fakeChat{err: tc.err}
		w := as(chatRouter(f), "POST", send, body)
		if w.Code != tc.status || problemType(t, w) != tc.typ || w.Header().Get("Retry-After") != tc.retry {
			t.Errorf("%v: %d %s %q", tc.err, w.Code, problemType(t, w), w.Header().Get("Retry-After"))
		}
	}
}
