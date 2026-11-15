package grpc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/chatevents"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeChatEvents struct {
	mu  sync.Mutex
	got []chatevents.MediaReleased
}

func (f *fakeChatEvents) MediaReleased(_ context.Context, e chatevents.MediaReleased) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, e)
	return nil
}

func (f *fakeChatEvents) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// chatEventConn serves ChatEventService behind the same Guard policy as
// cmd/core: only tayfam-chat in the deployment's namespace (ADR-0028).
func chatEventConn(t *testing.T, c *satest.Cluster, app ChatEvents) (corev1.ChatEventServiceClient, string) {
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
		Verifier: c.Verifier(t, auth.ServiceAudience("core"), nil),
		Services: []string{UserEventServiceName, ChatEventServiceName},
		Callers: map[string][]auth.ServiceAccount{
			corev1.UserEventService_ApplyUserEvent_FullMethodName: {{Namespace: "tayfam-test", Name: "tayfam-identity"}},
			corev1.ChatEventService_ApplyChatEvent_FullMethodName: {{Namespace: "tayfam-test", Name: "tayfam-chat"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(g.ServerOptions()...)
	corev1.RegisterChatEventServiceServer(srv, NewChatEventServer(app, slog.New(slog.NewTextHandler(io.Discard, nil))))
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
	userTok, _, err := signer.Issue(id.New().String(), id.New().String())
	if err != nil {
		t.Fatal(err)
	}
	return corev1.NewChatEventServiceClient(conn), userTok
}

func TestApplyChatEventAuthentication(t *testing.T) {
	c := satest.New(t)
	app := &fakeChatEvents{}
	client, userTok := chatEventConn(t, c, app)
	chat := auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-chat"}
	req := &corev1.ApplyChatEventRequest{EventId: id.New().String(), Event: &corev1.ApplyChatEventRequest_MediaReleased{
		MediaReleased: &corev1.ChatMediaReleased{UploadId: id.New().String(), MessageId: id.New().String()}}}
	for _, tc := range []struct {
		name  string
		token string
		want  codes.Code
	}{
		{"chat ServiceAccount", c.ServiceToken(t, chat, "core.tayfam.internal"), codes.OK},
		{"token yok", "", codes.Unauthenticated},
		{"kullanıcı token'ı", userTok, codes.Unauthenticated},
		{"yanlış audience", c.ServiceToken(t, chat, "chat.tayfam.internal"), codes.Unauthenticated},
		{"identity", c.ServiceToken(t, auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-identity"}, "core.tayfam.internal"), codes.PermissionDenied},
		{"başka namespace", c.ServiceToken(t, auth.ServiceAccount{Namespace: "baska", Name: "tayfam-chat"}, "core.tayfam.internal"), codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.token != "" {
				ctx = auth.WithToken(ctx, tc.token)
			}
			before := app.count()
			_, err := client.ApplyChatEvent(ctx, req)
			if got := status.Code(err); got != tc.want {
				t.Fatalf("kod = %v, beklenen %v", got, tc.want)
			}
			if tc.want != codes.OK && app.count() != before {
				t.Fatal("reddedilen çağrı uygulamaya ulaştı")
			}
		})
	}
	ctx := auth.WithToken(context.Background(), c.ServiceToken(t, chat, "core.tayfam.internal"))
	for name, r := range map[string]*corev1.ApplyChatEventRequest{
		"olay kimliği": {EventId: "x", Event: req.Event},
		"olay yok":     {EventId: id.New().String()},
		"dosya": {EventId: id.New().String(), Event: &corev1.ApplyChatEventRequest_MediaReleased{
			MediaReleased: &corev1.ChatMediaReleased{UploadId: "x", MessageId: id.New().String()}}},
	} {
		if _, err := client.ApplyChatEvent(ctx, r); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}
