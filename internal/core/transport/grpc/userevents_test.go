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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeUserEvents struct {
	mu  sync.Mutex
	got []userevents.Event
}

func (f *fakeUserEvents) Apply(_ context.Context, e userevents.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, e)
	return nil
}

func (f *fakeUserEvents) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// userEventConn serves UserEventService behind the same Guard policy as
// cmd/core: only tayfam-identity in the deployment's namespace.
func userEventConn(t *testing.T, c *satest.Cluster, app UserEvents) (client corev1.UserEventServiceClient, userTok string) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	uv, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := auth.NewGuard(uv).WithServices(auth.ServicePolicy{
		Verifier: c.Verifier(t, auth.ServiceAudience("core"), nil),
		Services: []string{UserEventServiceName},
		Callers: map[string][]auth.ServiceAccount{
			corev1.UserEventService_ApplyUserEvent_FullMethodName: {{Namespace: "tayfam-test", Name: "tayfam-identity"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(g.ServerOptions()...)
	corev1.RegisterUserEventServiceServer(srv, NewUserEventServer(app, slog.New(slog.NewTextHandler(io.Discard, nil))))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// A valid user token of the same deployment, for the "user token on a
	// service method" case.
	signer, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-identity", Audience: "tayfam-api", TTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	userTok, _, err = signer.Issue(id.New().String(), id.New().String())
	if err != nil {
		t.Fatal(err)
	}
	return corev1.NewUserEventServiceClient(conn), userTok
}

func TestApplyUserEventAuthentication(t *testing.T) {
	c := satest.New(t)
	app := &fakeUserEvents{}
	client, userTok := userEventConn(t, c, app)
	identity := auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-identity"}
	req := &corev1.ApplyUserEventRequest{EventId: id.New().String(), UserId: id.New().String(),
		Event: &corev1.ApplyUserEventRequest_Deleted{Deleted: &corev1.UserDeleted{}}}

	for _, tc := range []struct {
		name  string
		token string
		want  codes.Code
	}{
		{"identity ServiceAccount", c.ServiceToken(t, identity, "core.tayfam.internal"), codes.OK},
		{"token yok", "", codes.Unauthenticated},
		{"kullanıcı token'ı", userTok, codes.Unauthenticated},
		{"yanlış audience (chat)", c.ServiceToken(t, identity, "chat.tayfam.internal"), codes.Unauthenticated},
		{"yanlış ServiceAccount (chat)", c.ServiceToken(t, auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-chat"}, "core.tayfam.internal"), codes.PermissionDenied},
		{"başka namespace'teki identity", c.ServiceToken(t, auth.ServiceAccount{Namespace: "baska", Name: "tayfam-identity"}, "core.tayfam.internal"), codes.PermissionDenied},
		{"süresi dolmuş", c.Token(t, satest.Spec{Audience: []string{"core.tayfam.internal"}, Subject: identity.Subject(), IssuedAt: time.Now().Add(-time.Hour)}), codes.Unauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.token != "" {
				ctx = auth.WithToken(ctx, tc.token)
			}
			before := app.count()
			_, err := client.ApplyUserEvent(ctx, req)
			if got := status.Code(err); got != tc.want {
				t.Fatalf("kod = %v, beklenen %v", got, tc.want)
			}
			if tc.want != codes.OK && app.count() != before {
				t.Fatal("reddedilen çağrı uygulamaya ulaştı")
			}
		})
	}
}

func TestApplyUserEventValidation(t *testing.T) {
	c := satest.New(t)
	app := &fakeUserEvents{}
	client, _ := userEventConn(t, c, app)
	ctx := auth.WithToken(context.Background(), c.ServiceToken(t, auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-identity"}, "core.tayfam.internal"))
	eid, user := id.New().String(), id.New().String()
	profile := func(name, avatar string) *corev1.ApplyUserEventRequest_ProfileUpdated {
		return &corev1.ApplyUserEventRequest_ProfileUpdated{ProfileUpdated: &corev1.UserProfileUpdated{DisplayName: name, AvatarUploadId: avatar}}
	}
	for name, req := range map[string]*corev1.ApplyUserEventRequest{
		"olay kimliği":  {EventId: "x", UserId: user, Event: profile("Ad", "")},
		"kullanıcı":     {EventId: eid, UserId: "", Event: profile("Ad", "")},
		"olay türü yok": {EventId: eid, UserId: user},
		"avatar":        {EventId: eid, UserId: user, Event: profile("Ad", "avatar")},
		"görünürlük": {EventId: eid, UserId: user, Event: &corev1.ApplyUserEventRequest_ProfileUpdated{ProfileUpdated: &corev1.UserProfileUpdated{
			DisplayName: "Ad", ActivityVisibility: corev1.ActivityVisibility(99)}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.ApplyUserEvent(ctx, req); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("err = %v", err)
			}
		})
	}
	avatar := id.New()
	if _, err := client.ApplyUserEvent(ctx, &corev1.ApplyUserEventRequest{EventId: eid, UserId: user, Event: profile("Deniz", avatar.String())}); err != nil {
		t.Fatal(err)
	}
	got := app.got[len(app.got)-1]
	if got.Profile == nil || got.Profile.DisplayName != "Deniz" || got.Profile.AvatarUploadID != avatar || got.Deleted || got.User.String() != user || got.ID.String() != eid {
		t.Fatalf("olay = %+v", got)
	}
	if got.Profile.ActivityVisibility != "" {
		t.Fatalf("belirtilmemiş görünürlük = %q", got.Profile.ActivityVisibility)
	}
	for pb, want := range map[corev1.ActivityVisibility]string{
		corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE: userevents.VisibilityPrivate,
		corev1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA:   userevents.VisibilityTayfa,
		corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC:  userevents.VisibilityPublic,
	} {
		req := &corev1.ApplyUserEventRequest{EventId: id.New().String(), UserId: user, Event: &corev1.ApplyUserEventRequest_ProfileUpdated{
			ProfileUpdated: &corev1.UserProfileUpdated{DisplayName: "Deniz", ActivityVisibility: pb}}}
		if _, err := client.ApplyUserEvent(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got := app.got[len(app.got)-1]; got.Profile.ActivityVisibility != want {
			t.Fatalf("%v: %q", pb, got.Profile.ActivityVisibility)
		}
	}
}
