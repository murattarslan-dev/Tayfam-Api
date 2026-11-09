package grpc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeDirect records what the transport passed and returns err.
type fakeDirect struct {
	caller, conversation, message id.ID
	sender                        id.ID
	send                          dm.SendInput
	list                          dm.ListInput
	size                          int32
	token                         string
	err                           error
}

func (f *fakeDirect) Send(_ context.Context, caller id.ID, in dm.SendInput) (dm.Message, dm.Conversation, error) {
	f.caller, f.send = caller, in
	c := id.New()
	return dm.Message{ID: id.New(), ConversationID: c, SenderID: caller, Body: in.Body},
		dm.Conversation{ID: c, Other: in.Recipient, Status: dm.StatusRequestSent}, f.err
}

func (f *fakeDirect) List(_ context.Context, caller id.ID, size int32, token string) ([]dm.Conversation, string, error) {
	f.caller, f.size, f.token = caller, size, token
	return []dm.Conversation{{ID: id.New(), Status: dm.StatusActive, Unread: 100, PeerLastRead: id.New()}}, "sonraki", f.err
}

func (f *fakeDirect) Get(_ context.Context, caller, c id.ID) (dm.Conversation, error) {
	f.caller, f.conversation = caller, c
	return dm.Conversation{ID: c, Status: dm.StatusRequestReceived}, f.err
}

func (f *fakeDirect) Messages(_ context.Context, caller, c id.ID, in dm.ListInput) ([]dm.Message, string, error) {
	f.caller, f.conversation, f.list = caller, c, in
	return []dm.Message{{ID: id.New(), ConversationID: c, Deleted: true}}, "", f.err
}

func (f *fakeDirect) Delete(_ context.Context, caller, c, m id.ID) error {
	f.caller, f.conversation, f.message = caller, c, m
	return f.err
}

func (f *fakeDirect) MarkRead(_ context.Context, caller, c, m id.ID) error {
	f.caller, f.conversation, f.message = caller, c, m
	return f.err
}

func (f *fakeDirect) Accept(_ context.Context, caller, c id.ID) (dm.Conversation, error) {
	f.caller, f.conversation = caller, c
	return dm.Conversation{ID: c, Status: dm.StatusActive}, f.err
}

func (f *fakeDirect) Decline(_ context.Context, caller, c id.ID) error {
	f.caller, f.conversation = caller, c
	return f.err
}

func (f *fakeDirect) VerifyForReport(_ context.Context, caller, m id.ID) (id.ID, id.ID, error) {
	f.caller, f.message = caller, m
	return f.conversation, f.sender, f.err
}

func directConn(t *testing.T, app DirectMessages, logs *bytes.Buffer) (chatv1.DirectMessageServiceClient, context.Context, id.ID) {
	t.Helper()
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	uv, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(auth.NewGuard(uv).ServerOptions()...)
	chatv1.RegisterDirectMessageServiceServer(srv, NewDirectMessageServer(app, slog.New(slog.NewJSONHandler(logs, nil))))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	signer, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-identity", Audience: "tayfam-api", TTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	user := id.New()
	tok, _, err := signer.Issue(user.String(), id.New().String())
	if err != nil {
		t.Fatal(err)
	}
	return chatv1.NewDirectMessageServiceClient(conn), auth.WithToken(context.Background(), tok), user
}

func TestDirectMessageServiceConverts(t *testing.T) {
	app := &fakeDirect{}
	client, ctx, user := directConn(t, app, &bytes.Buffer{})
	to, req, conv, msg := id.New(), id.New(), id.New(), id.New()

	if _, err := client.ListConversations(context.Background(), &chatv1.ListConversationsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token yok: %v", err)
	}
	sent, err := client.SendDirectMessage(ctx, &chatv1.SendDirectMessageRequest{RecipientUserId: to.String(), Body: "merhaba", ClientRequestId: req.String()})
	if err != nil || app.caller != user || app.send.Recipient != to || app.send.Body != "merhaba" || app.send.ClientRequestID != req ||
		sent.GetMessage().GetSenderUserId() != user.String() || sent.GetConversation().GetStatus() != chatv1.ConversationStatus_CONVERSATION_STATUS_REQUEST_SENT ||
		sent.GetConversation().GetOtherUserId() != to.String() {
		t.Fatalf("gönderim = %v, %v", sent, err)
	}
	for name, r := range map[string]*chatv1.SendDirectMessageRequest{
		"recipient_user_id": {RecipientUserId: "x", ClientRequestId: req.String()},
		"client_request_id": {RecipientUserId: to.String()},
	} {
		_, err := client.SendDirectMessage(ctx, r)
		if code, reason, meta := reasonOf(err); code != codes.InvalidArgument || reason != "ERROR_REASON_VALIDATION" || meta["field"] != name {
			t.Errorf("%s: %v", name, err)
		}
	}
	list, err := client.ListConversations(ctx, &chatv1.ListConversationsRequest{PageSize: 3, PageToken: "t"})
	if err != nil || app.size != 3 || app.token != "t" || list.GetNextPageToken() != "sonraki" ||
		list.GetConversations()[0].GetUnreadCount() != 100 || list.GetConversations()[0].GetOtherUserId() != "" || list.GetConversations()[0].GetPeerLastReadMessageId() == "" {
		t.Fatalf("liste = %v, %v", list, err)
	}
	got, err := client.GetConversation(ctx, &chatv1.GetConversationRequest{ConversationId: conv.String()})
	if err != nil || app.conversation != conv || got.GetConversation().GetStatus() != chatv1.ConversationStatus_CONVERSATION_STATUS_REQUEST_RECEIVED {
		t.Fatalf("konuşma = %v, %v", got, err)
	}
	after := id.New()
	msgs, err := client.ListDirectMessages(ctx, &chatv1.ListDirectMessagesRequest{ConversationId: conv.String(), AfterMessageId: after.String(), PageSize: 7})
	if err != nil || app.list.After != after || app.list.PageSize != 7 || !msgs.GetMessages()[0].GetDeleted() {
		t.Fatalf("mesajlar = %v, %v", msgs, err)
	}
	if _, err := client.ListDirectMessages(ctx, &chatv1.ListDirectMessagesRequest{ConversationId: conv.String(), AfterMessageId: "x"}); func() string { _, _, m := reasonOf(err); return m["field"] }() != "after_message_id" {
		t.Fatalf("sonrası: %v", err)
	}
	if _, err := client.DeleteDirectMessage(ctx, &chatv1.DeleteDirectMessageRequest{ConversationId: conv.String(), MessageId: msg.String()}); err != nil || app.message != msg {
		t.Fatalf("silme: %v", err)
	}
	if _, err := client.MarkConversationRead(ctx, &chatv1.MarkConversationReadRequest{ConversationId: conv.String(), MessageId: "x"}); func() string { _, _, m := reasonOf(err); return m["field"] }() != "message_id" {
		t.Fatalf("okundu: %v", err)
	}
	acc, err := client.AcceptMessageRequest(ctx, &chatv1.AcceptMessageRequestRequest{ConversationId: conv.String()})
	if err != nil || acc.GetConversation().GetStatus() != chatv1.ConversationStatus_CONVERSATION_STATUS_ACTIVE {
		t.Fatalf("kabul: %v %v", acc, err)
	}
	if _, err := client.DeclineMessageRequest(ctx, &chatv1.DeclineMessageRequestRequest{ConversationId: conv.String()}); err != nil || app.conversation != conv {
		t.Fatalf("ret: %v", err)
	}
}

func TestDirectMessageErrors(t *testing.T) {
	logs := &bytes.Buffer{}
	app := &fakeDirect{}
	client, ctx, _ := directConn(t, app, logs)
	r := &chatv1.SendDirectMessageRequest{RecipientUserId: id.New().String(), Body: "GizliDMGövdesi", ClientRequestId: id.New().String()}
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{apperr.ErrConversationNotFound, codes.NotFound, "ERROR_REASON_CONVERSATION_NOT_FOUND"},
		{apperr.ErrDirectMessageNotAllowed, codes.PermissionDenied, "ERROR_REASON_DIRECT_MESSAGE_NOT_ALLOWED"},
		{apperr.ErrMessageRequestPending, codes.FailedPrecondition, "ERROR_REASON_MESSAGE_REQUEST_PENDING"},
		{apperr.ErrMessageRequestLimit, codes.ResourceExhausted, "ERROR_REASON_MESSAGE_REQUEST_LIMIT"},
		{apperr.ErrMessageRequestNotFound, codes.NotFound, "ERROR_REASON_MESSAGE_REQUEST_NOT_FOUND"},
		{apperr.ErrMessageNotFound, codes.NotFound, "ERROR_REASON_MESSAGE_NOT_FOUND"},
		{apperr.ErrDirectMessagesUnavailable, codes.Unavailable, ""},
		{apperr.ErrUnavailable, codes.Unavailable, ""},
	} {
		app.err = tc.err
		_, err := client.SendDirectMessage(ctx, r)
		if code, reason, _ := reasonOf(err); code != tc.code || reason != tc.reason {
			t.Errorf("%v: %v", tc.err, err)
		}
	}
	app.err = errInternalForTest
	_, err := client.SendDirectMessage(ctx, r)
	if status.Code(err) != codes.Internal || strings.Contains(status.Convert(err).Message(), "test") {
		t.Fatalf("iç hata: %v", err)
	}
	if strings.Contains(logs.String(), "GizliDMGövdesi") {
		t.Fatal("log mesaj metni içeriyor")
	}
}

// Direct message notices are listed, but core cannot request them: they are
// chat's own and never carry an actor.
func TestDirectNoticesListedNotRequested(t *testing.T) {
	for _, typ := range []chatv1.NotificationType{chatv1.NotificationType_NOTIFICATION_TYPE_DM_REQUEST, chatv1.NotificationType_NOTIFICATION_TYPE_DM_MESSAGE} {
		if _, ok := notificationTypes[typ]; ok {
			t.Errorf("%s core'dan istenebiliyor", typ)
		}
		if toProto(listedTypes, listedTypes[typ]) != typ {
			t.Errorf("%s listelenmiyor", typ)
		}
	}
	if _, ok := targetTypes[chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_DM_CONVERSATION]; ok {
		t.Error("konuşma hedefi core'dan istenebiliyor")
	}
	if len(listedTypes) != len(notificationTypes)+2 || len(listedTargets) != len(targetTypes)+1 {
		t.Error("liste eşlemesi eksik")
	}
}

// The verification answer for core's report check carries IDs only.
func TestVerifyDirectMessageForReport(t *testing.T) {
	app := &fakeDirect{conversation: id.New(), sender: id.New()}
	client, ctx, user := directConn(t, app, &bytes.Buffer{})
	msg := id.New()
	resp, err := client.VerifyDirectMessageForReport(ctx, &chatv1.VerifyDirectMessageForReportRequest{MessageId: msg.String()})
	if err != nil || app.caller != user || app.message != msg || resp.GetConversationId() != app.conversation.String() || resp.GetSenderUserId() != app.sender.String() {
		t.Fatalf("doğrulama = %v %v", resp, err)
	}
	if _, err := client.VerifyDirectMessageForReport(ctx, &chatv1.VerifyDirectMessageForReportRequest{MessageId: "x"}); func() string { _, _, m := reasonOf(err); return m["field"] }() != "message_id" {
		t.Fatalf("geçersiz: %v", err)
	}
	app.err = apperr.ErrMessageNotFound
	_, err = client.VerifyDirectMessageForReport(ctx, &chatv1.VerifyDirectMessageForReportRequest{MessageId: msg.String()})
	if code, reason, _ := reasonOf(err); code != codes.NotFound || reason != "ERROR_REASON_MESSAGE_NOT_FOUND" {
		t.Fatalf("bulunamadı: %v", err)
	}
}
