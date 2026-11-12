package rest

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
)

const conversationID = "01965a3c-0000-7000-8000-000000000040"

func testConversation() *chatv1.Conversation {
	return &chatv1.Conversation{
		Id: conversationID, OtherUserId: userID, Status: chatv1.ConversationStatus_CONVERSATION_STATUS_REQUEST_RECEIVED,
		UnreadCount: 3, LastReadMessageId: messageID, LastMessageTime: timestamppb.New(t0), CreateTime: timestamppb.New(t0),
	}
}

func testDirectMessage() *chatv1.DirectMessage {
	return &chatv1.DirectMessage{
		Id: messageID, ConversationId: conversationID, SenderUserId: userID, CreateTime: timestamppb.New(t0),
		Encryption: chatv1.MessageEncryption_MESSAGE_ENCRYPTION_E2E,
	}
}

// dmEnvelopes is one envelope in a send request's JSON.
const dmEnvelopes = `[{"device_id":"` + deviceID + `","registration_id":9,"type":"prekey","content":"AQI="}]`

func dmSendBody(extra string) string {
	return `{"recipient_user_id":"` + userID + `","client_request_id":"` + requestID + `","envelopes":` + dmEnvelopes + extra + `}`
}

func (f *fakeChat) SendDirectMessage(ctx context.Context, in *chatv1.SendDirectMessageRequest, _ ...grpc.CallOption) (*chatv1.SendDirectMessageResponse, error) {
	return &chatv1.SendDirectMessageResponse{Message: testDirectMessage(), Conversation: testConversation()}, f.record(ctx, in)
}

func (f *fakeChat) ListConversations(ctx context.Context, in *chatv1.ListConversationsRequest, _ ...grpc.CallOption) (*chatv1.ListConversationsResponse, error) {
	return &chatv1.ListConversationsResponse{NextPageToken: "SONRAKI", Conversations: []*chatv1.Conversation{
		testConversation(), {Id: conversationID, Status: 9, PeerLastReadMessageId: messageID, LastMessageTime: timestamppb.New(t0), CreateTime: timestamppb.New(t0)},
	}}, f.record(ctx, in)
}

func (f *fakeChat) GetConversation(ctx context.Context, in *chatv1.GetConversationRequest, _ ...grpc.CallOption) (*chatv1.GetConversationResponse, error) {
	return &chatv1.GetConversationResponse{Conversation: testConversation()}, f.record(ctx, in)
}

func (f *fakeChat) ListDirectMessages(ctx context.Context, in *chatv1.ListDirectMessagesRequest, _ ...grpc.CallOption) (*chatv1.ListDirectMessagesResponse, error) {
	return &chatv1.ListDirectMessagesResponse{Messages: []*chatv1.DirectMessage{
		testDirectMessage(), {Id: messageID, ConversationId: conversationID, Deleted: true, CreateTime: timestamppb.New(t0)},
	}}, f.record(ctx, in)
}

func (f *fakeChat) DeleteDirectMessage(ctx context.Context, in *chatv1.DeleteDirectMessageRequest, _ ...grpc.CallOption) (*chatv1.DeleteDirectMessageResponse, error) {
	return &chatv1.DeleteDirectMessageResponse{}, f.record(ctx, in)
}

func (f *fakeChat) MarkConversationRead(ctx context.Context, in *chatv1.MarkConversationReadRequest, _ ...grpc.CallOption) (*chatv1.MarkConversationReadResponse, error) {
	return &chatv1.MarkConversationReadResponse{}, f.record(ctx, in)
}

func (f *fakeChat) AcceptMessageRequest(ctx context.Context, in *chatv1.AcceptMessageRequestRequest, _ ...grpc.CallOption) (*chatv1.AcceptMessageRequestResponse, error) {
	c := testConversation()
	c.Status = chatv1.ConversationStatus_CONVERSATION_STATUS_ACTIVE
	return &chatv1.AcceptMessageRequestResponse{Conversation: c}, f.record(ctx, in)
}

func (f *fakeChat) DeclineMessageRequest(ctx context.Context, in *chatv1.DeclineMessageRequestRequest, _ ...grpc.CallOption) (*chatv1.DeclineMessageRequestResponse, error) {
	return &chatv1.DeclineMessageRequestResponse{}, f.record(ctx, in)
}

func (f *fakeChat) ListDeclinedRequests(ctx context.Context, in *chatv1.ListDeclinedRequestsRequest, _ ...grpc.CallOption) (*chatv1.ListDeclinedRequestsResponse, error) {
	return &chatv1.ListDeclinedRequestsResponse{NextPageToken: "SONRAKI", DeclinedRequests: []*chatv1.DeclinedRequest{
		{ConversationId: conversationID, SenderUserId: userID, DeclineTime: timestamppb.New(t0), CreateTime: timestamppb.New(t0)},
	}}, f.record(ctx, in)
}

func (f *fakeChat) RemoveDeclinedRequest(ctx context.Context, in *chatv1.RemoveDeclinedRequestRequest, _ ...grpc.CallOption) (*chatv1.RemoveDeclinedRequestResponse, error) {
	return &chatv1.RemoveDeclinedRequestResponse{}, f.record(ctx, in)
}

func TestDirectMessageRoutes(t *testing.T) {
	f := &fakeChat{}
	h := chatRouter(f)
	c := "/v1/direct-messages/conversations/" + conversationID
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"POST", "/v1/direct-messages", dmSendBody(""), 201, &chatv1.SendDirectMessageRequest{
			RecipientUserId: userID, ClientRequestId: requestID,
			Envelopes: []*chatv1.OutgoingEnvelope{{DeviceId: deviceID, RegistrationId: 9, Type: chatv1.EnvelopeType_ENVELOPE_TYPE_PREKEY, Content: []byte{1, 2}}},
		}},
		{"GET", "/v1/direct-messages/conversations?page_size=10&page_token=P", "", 200, &chatv1.ListConversationsRequest{PageSize: 10, PageToken: "P"}},
		{"GET", c, "", 200, &chatv1.GetConversationRequest{ConversationId: conversationID}},
		{"GET", c + "/messages?page_size=20", "", 200, &chatv1.ListDirectMessagesRequest{ConversationId: conversationID, PageSize: 20}},
		{"GET", c + "/messages?after_message_id=" + messageID, "", 200, &chatv1.ListDirectMessagesRequest{ConversationId: conversationID, AfterMessageId: messageID}},
		{"DELETE", c + "/messages/" + messageID, "", 204, &chatv1.DeleteDirectMessageRequest{ConversationId: conversationID, MessageId: messageID}},
		{"PUT", c + "/read", `{"message_id":"` + messageID + `"}`, 204, &chatv1.MarkConversationReadRequest{ConversationId: conversationID, MessageId: messageID}},
		{"POST", c + "/accept", "", 200, &chatv1.AcceptMessageRequestRequest{ConversationId: conversationID}},
		{"POST", c + "/decline", "", 204, &chatv1.DeclineMessageRequestRequest{ConversationId: conversationID}},
		{"GET", "/v1/direct-messages/declined-requests?page_size=5&page_token=P", "", 200, &chatv1.ListDeclinedRequestsRequest{PageSize: 5, PageToken: "P"}},
		{"DELETE", "/v1/direct-messages/declined-requests/" + conversationID, "", 204, &chatv1.RemoveDeclinedRequestRequest{ConversationId: conversationID}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, tok, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) || tok != "USERTOKEN" {
			t.Errorf("%s %s: %d %s; chat'e giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}

	sent := decodeBody[directMessageSentJSON](t, as(h, "POST", "/v1/direct-messages", dmSendBody("")))
	if sent.Message.Encryption != "e2e" || sent.Message.SenderUserID != userID || sent.Conversation.Status != "request_received" || sent.Conversation.UnreadCount != 3 {
		t.Fatalf("gönderim: %+v", sent)
	}
	l := decodeBody[conversationListJSON](t, as(h, "GET", "/v1/direct-messages/conversations", ""))
	if len(l.Conversations) != 2 || l.Conversations[0].OtherUserID != userID || l.Conversations[1].Status != "unknown" ||
		l.Conversations[1].OtherUserID != "" || l.Conversations[1].PeerLastReadMessageID != messageID || l.NextPageToken != "SONRAKI" {
		t.Fatalf("liste: %+v", l)
	}
	w := as(h, "GET", c+"/messages", "")
	m := decodeBody[directMessageListJSON](t, w)
	if len(m.Messages) != 2 || m.Messages[0].Encryption != "e2e" || !m.Messages[1].Deleted || m.Messages[1].SenderUserID != "" ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("mesajlar: %+v %v", m, w.Header())
	}
	// There is no body field any more (issue #151).
	if strings.Contains(w.Body.String(), `"body"`) {
		t.Fatalf("metin alanı: %s", w.Body)
	}
	if a := decodeBody[conversationJSON](t, as(h, "POST", c+"/accept", "")); a.Status != "active" {
		t.Fatalf("kabul: %+v", a)
	}
	w = as(h, "GET", "/v1/direct-messages/declined-requests", "")
	d := decodeBody[declinedRequestListJSON](t, w)
	if len(d.DeclinedRequests) != 1 || d.DeclinedRequests[0].ConversationID != conversationID || d.DeclinedRequests[0].SenderUserID != userID ||
		!d.DeclinedRequests[0].DeclinedAt.Equal(t0) || d.NextPageToken != "SONRAKI" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reddedilenler: %+v %v", d, w.Header())
	}
}

func TestDirectMessageBadRequests(t *testing.T) {
	f := &fakeChat{}
	h := chatRouter(f)
	c := "/v1/direct-messages/conversations/" + conversationID
	send := dmSendBody
	for name, tc := range map[string]struct{ method, path, body string }{
		"bozuk konuşma":      {"GET", "/v1/direct-messages/conversations/x", ""},
		"bozuk mesaj":        {"DELETE", c + "/messages/x", ""},
		"alıcı yok":          {"POST", "/v1/direct-messages", `{"client_request_id":"` + requestID + `","envelopes":` + dmEnvelopes + `}`},
		"bozuk alıcı":        {"POST", "/v1/direct-messages", `{"recipient_user_id":"x","client_request_id":"` + requestID + `","envelopes":` + dmEnvelopes + `}`},
		"zarf yok":           {"POST", "/v1/direct-messages", `{"recipient_user_id":"` + userID + `","client_request_id":"` + requestID + `"}`},
		"boş zarf listesi":   {"POST", "/v1/direct-messages", `{"recipient_user_id":"` + userID + `","client_request_id":"` + requestID + `","envelopes":[]}`},
		"düz metin":          {"POST", "/v1/direct-messages", `{"recipient_user_id":"` + userID + `","body":"x","client_request_id":"` + requestID + `"}`},
		"metin ve zarf":      {"POST", "/v1/direct-messages", send(`,"body":"x"`)},
		"istek kimliği yok":  {"POST", "/v1/direct-messages", `{"recipient_user_id":"` + userID + `","envelopes":` + dmEnvelopes + `}`},
		"gönderen alanı":     {"POST", "/v1/direct-messages", send(`,"sender_user_id":"` + userID + `"`)},
		"bozuk after":        {"GET", c + "/messages?after_message_id=x", ""},
		"bozuk sayfa":        {"GET", "/v1/direct-messages/conversations?page_size=x", ""},
		"okundu kimliği yok": {"PUT", c + "/read", `{}`},
		"bozuk reddedilen":   {"DELETE", "/v1/direct-messages/declined-requests/x", ""},
		"bozuk ret sayfası":  {"GET", "/v1/direct-messages/declined-requests?page_size=x", ""},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek chat'e gitti")
	}
	if w := do(h, "GET", "/v1/direct-messages/conversations", "", nil); w.Code != 401 {
		t.Fatalf("token'sız: %d", w.Code)
	}
	if w := do(h, "POST", "/v1/direct-messages", send(""), map[string]string{"Authorization": "Bearer USERTOKEN"}); w.Code != 400 {
		t.Fatalf("istemci başlığı olmadan: %d", w.Code)
	}
	// Removing from the declined list changes state: token and client
	// header are required.
	if w := do(h, "DELETE", "/v1/direct-messages/declined-requests/"+conversationID, "", map[string]string{clientHeader: "android"}); w.Code != 401 {
		t.Fatalf("token'sız çıkarma: %d", w.Code)
	}
	if w := do(h, "DELETE", "/v1/direct-messages/declined-requests/"+conversationID, "", map[string]string{"Authorization": "Bearer USERTOKEN"}); w.Code != 400 {
		t.Fatalf("istemci başlığı olmadan çıkarma: %d", w.Code)
	}
	if w := do(h, "GET", "/v1/direct-messages/declined-requests", "", nil); w.Code != 401 {
		t.Fatalf("token'sız liste: %d", w.Code)
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("yetkisiz istek chat'e gitti")
	}
}

func TestDirectMessageErrors(t *testing.T) {
	body := dmSendBody("")
	for _, tc := range []struct {
		err    error
		status int
		typ    string
	}{
		{chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_CONVERSATION_NOT_FOUND.String()), 404, "conversation-not-found"},
		{chatErr(codes.PermissionDenied, chatv1.ErrorReason_ERROR_REASON_DIRECT_MESSAGE_NOT_ALLOWED.String()), 403, "direct-message-not-allowed"},
		{chatErr(codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_MESSAGE_REQUEST_PENDING.String()), 409, "message-request-pending"},
		{chatErr(codes.ResourceExhausted, chatv1.ErrorReason_ERROR_REASON_MESSAGE_REQUEST_LIMIT.String()), 429, "message-request-limit"},
		{chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_MESSAGE_REQUEST_NOT_FOUND.String()), 404, "message-request-not-found"},
		{chatErr(codes.ResourceExhausted, chatv1.ErrorReason_ERROR_REASON_RATE_LIMITED.String()), 429, "rate-limited"},
		{chatErr(codes.InvalidArgument, chatv1.ErrorReason_ERROR_REASON_VALIDATION.String()), 400, "invalid-request"},
		{status.Error(codes.Unavailable, "x"), 503, "unavailable"},
		// A reason from another domain is not trusted.
		{coreErr(codes.PermissionDenied, "ERROR_REASON_DIRECT_MESSAGE_NOT_ALLOWED"), 500, "internal"},
	} {
		f := &fakeChat{err: tc.err}
		w := as(chatRouter(f), "POST", "/v1/direct-messages", body)
		if w.Code != tc.status || problemType(t, w) != tc.typ {
			t.Errorf("%v: %d %s", tc.err, w.Code, problemType(t, w))
		}
	}
}

// Removing from the declined list maps chat's answers; nobody's list but
// the caller's is reachable (chat decides; the gateway passes the reason).
func TestRemoveDeclinedRequestErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		typ    string
	}{
		{chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_CONVERSATION_NOT_FOUND.String()), 404, "conversation-not-found"},
		{chatErr(codes.NotFound, chatv1.ErrorReason_ERROR_REASON_MESSAGE_REQUEST_NOT_FOUND.String()), 404, "message-request-not-found"},
		{status.Error(codes.Unavailable, "x"), 503, "unavailable"},
	} {
		f := &fakeChat{err: tc.err}
		w := as(chatRouter(f), "DELETE", "/v1/direct-messages/declined-requests/"+conversationID, "")
		if w.Code != tc.status || problemType(t, w) != tc.typ {
			t.Errorf("%v: %d %s", tc.err, w.Code, problemType(t, w))
		}
	}
}
