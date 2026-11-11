package grpc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeNotifications struct {
	caller id.ID
	scope  id.ID
	level  string
	until  time.Time
	list   []notification.Notification
	// convErr is SetConversationPreference's answer.
	convErr error
}

func (f *fakeNotifications) List(_ context.Context, caller id.ID, _ int32, _ string) ([]notification.Notification, string, error) {
	f.caller = caller
	return f.list, "", nil
}

func (f *fakeNotifications) UnreadCount(_ context.Context, caller id.ID) (int32, error) {
	f.caller = caller
	return 7, nil
}

func (f *fakeNotifications) MarkRead(_ context.Context, caller, _ id.ID) error {
	f.caller = caller
	return apperr.ErrNotificationNotFound
}

func (f *fakeNotifications) MarkAllRead(_ context.Context, caller id.ID) error {
	f.caller = caller
	return nil
}

func (f *fakeNotifications) Preferences(_ context.Context, caller id.ID) ([]notification.Preference, error) {
	f.caller = caller
	return nil, nil
}

func (f *fakeNotifications) SetTayfaPreference(_ context.Context, caller, tayfa id.ID, level string, until time.Time) (notification.Preference, error) {
	f.caller, f.level, f.until = caller, level, until
	return notification.Preference{ScopeType: notification.ScopeTayfa, Scope: tayfa, Level: level, MutedUntil: until}, nil
}

func (f *fakeNotifications) SetConversationPreference(_ context.Context, caller, conversation id.ID, level string, until time.Time) (notification.Preference, error) {
	f.caller, f.scope, f.level, f.until = caller, conversation, level, until
	if f.convErr != nil {
		return notification.Preference{}, f.convErr
	}
	return notification.Preference{ScopeType: notification.ScopeDMConversation, Scope: conversation, Level: level, MutedUntil: until}, nil
}

func notificationConn(t *testing.T, c *satest.Cluster, app Notifications) (chatv1.NotificationServiceClient, *auth.Signer) {
	t.Helper()
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	uv, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := auth.NewGuard(uv).WithServices(auth.ServicePolicy{
		Verifier: c.Verifier(t, auth.ServiceAudience("chat"), nil),
		Services: []string{UserEventServiceName, CoreEventServiceName},
		Callers: map[string][]auth.ServiceAccount{
			chatv1.UserEventService_ApplyUserEvent_FullMethodName: {identitySA},
			chatv1.CoreEventService_ApplyCoreEvent_FullMethodName: {coreSA},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(g.ServerOptions()...)
	chatv1.RegisterNotificationServiceServer(srv, NewNotificationServer(app, slog.New(slog.NewTextHandler(io.Discard, nil))))
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
	return chatv1.NewNotificationServiceClient(conn), signer
}

// The caller comes from the verified user token only; service tokens do not
// reach user methods (ADR-0013).
func TestNotificationServiceCaller(t *testing.T) {
	c := satest.New(t)
	tf, target := id.New(), id.New()
	app := &fakeNotifications{list: []notification.Notification{{
		ID: id.New(), Type: notification.TypeJoinRequest, Tayfa: tf, TargetType: notification.TargetMembership, Target: target, CreatedAt: time.Now(),
	}}}
	client, signer := notificationConn(t, c, app)
	user := id.New()
	tok, _, err := signer.Issue(user.String(), id.New().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithToken(context.Background(), tok)

	resp, err := client.ListNotifications(ctx, &chatv1.ListNotificationsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	n := resp.GetNotifications()[0]
	if app.caller != user || n.GetType() != chatv1.NotificationType_NOTIFICATION_TYPE_JOIN_REQUEST ||
		n.GetTargetType() != chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_MEMBERSHIP || n.GetActorId() != "" || n.GetRead() {
		t.Fatalf("yanıt = %v", n)
	}

	svcCtx := auth.WithToken(context.Background(), c.ServiceToken(t, coreSA, "chat.tayfam.internal"))
	if _, err := client.GetUnreadNotificationCount(svcCtx, &chatv1.GetUnreadNotificationCountRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("servis token'ı: %v", err)
	}
	if _, err := client.GetUnreadNotificationCount(context.Background(), &chatv1.GetUnreadNotificationCountRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token yok: %v", err)
	}

	if _, err := client.MarkNotificationRead(ctx, &chatv1.MarkNotificationReadRequest{NotificationId: id.New().String()}); status.Code(err) != codes.NotFound {
		t.Fatalf("bulunamadı: %v", err)
	}
	if _, err := client.MarkNotificationRead(ctx, &chatv1.MarkNotificationReadRequest{NotificationId: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("geçersiz kimlik: %v", err)
	}

	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	p, err := client.SetTayfaNotificationPreference(ctx, &chatv1.SetTayfaNotificationPreferenceRequest{
		TayfaId: tf.String(), Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE, MuteUntil: timestamppb.New(until),
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.level != notification.LevelNone || !app.until.Equal(until) || p.GetPreference().GetScopeType() != chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_TAYFA {
		t.Fatalf("tercih = %v", p)
	}
	if _, err := client.SetTayfaNotificationPreference(ctx, &chatv1.SetTayfaNotificationPreferenceRequest{TayfaId: tf.String()}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("seviyesiz: %v", err)
	}
}

// The conversation preference acts for the verified caller only; another
// user's conversation is NOT_FOUND with the conversation reason, and the
// request carries no user field to act for someone else (issue #138).
func TestConversationNotificationPreference(t *testing.T) {
	c := satest.New(t)
	app := &fakeNotifications{}
	client, signer := notificationConn(t, c, app)
	user, conv := id.New(), id.New()
	tok, _, err := signer.Issue(user.String(), id.New().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithToken(context.Background(), tok)

	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	p, err := client.SetConversationNotificationPreference(ctx, &chatv1.SetConversationNotificationPreferenceRequest{
		ConversationId: conv.String(), Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE, MuteUntil: timestamppb.New(until),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := p.GetPreference()
	if app.caller != user || app.scope != conv || app.level != notification.LevelNone || !app.until.Equal(until) ||
		got.GetScopeType() != chatv1.NotificationScopeType_NOTIFICATION_SCOPE_TYPE_DM_CONVERSATION || got.GetScopeId() != conv.String() ||
		!got.GetMuteUntil().AsTime().Equal(until) {
		t.Fatalf("tercih = %v", p)
	}

	for name, req := range map[string]*chatv1.SetConversationNotificationPreferenceRequest{
		"seviyesiz":     {ConversationId: conv.String()},
		"bozuk kimlik":  {ConversationId: "x", Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE},
		"bozuk süre":    {ConversationId: conv.String(), Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE, MuteUntil: &timestamppb.Timestamp{Nanos: -1}},
		"bilinmeyen sv": {ConversationId: conv.String(), Level: chatv1.NotificationLevel(42)},
	} {
		if _, err := client.SetConversationNotificationPreference(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}

	// MENTIONS reaches the app, which refuses it for a conversation.
	app.convErr = apperr.Validation("level")
	if _, err := client.SetConversationNotificationPreference(ctx, &chatv1.SetConversationNotificationPreferenceRequest{
		ConversationId: conv.String(), Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_MENTIONS,
	}); status.Code(err) != codes.InvalidArgument || app.level != notification.LevelMentions {
		t.Fatalf("mentions: %v", err)
	}

	app.convErr = apperr.ErrConversationNotFound
	_, err = client.SetConversationNotificationPreference(ctx, &chatv1.SetConversationNotificationPreferenceRequest{
		ConversationId: id.New().String(), Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE,
	})
	if code, reason, _ := reasonOf(err); code != codes.NotFound || reason != chatv1.ErrorReason_ERROR_REASON_CONVERSATION_NOT_FOUND.String() {
		t.Fatalf("başkasının konuşması: %v", err)
	}

	svcCtx := auth.WithToken(context.Background(), c.ServiceToken(t, coreSA, "chat.tayfam.internal"))
	if _, err := client.SetConversationNotificationPreference(svcCtx, &chatv1.SetConversationNotificationPreferenceRequest{
		ConversationId: conv.String(), Level: chatv1.NotificationLevel_NOTIFICATION_LEVEL_NONE,
	}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("servis token'ı: %v", err)
	}
}
