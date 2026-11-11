package grpc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
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
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakePush struct {
	caller   push.Caller
	platform string
	token    string
	removed  bool
	err      error
}

func (f *fakePush) Register(_ context.Context, c push.Caller, platform, token string) error {
	f.caller, f.platform, f.token = c, platform, token
	return f.err
}

func (f *fakePush) Unregister(_ context.Context, c push.Caller) error {
	f.caller, f.removed = c, true
	return f.err
}

func pushConn(t *testing.T, app Push, logs *bytes.Buffer) (chatv1.PushServiceClient, context.Context, id.ID, id.ID) {
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
	chatv1.RegisterPushServiceServer(srv, NewPushServer(app, slog.New(slog.NewJSONHandler(logs, nil))))
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
	user, session := id.New(), id.New()
	tok, _, err := signer.Issue(user.String(), session.String())
	if err != nil {
		t.Fatal(err)
	}
	return chatv1.NewPushServiceClient(conn), auth.WithToken(context.Background(), tok), user, session
}

// The token belongs to the caller's session: requests never name it.
func TestPushServer(t *testing.T) {
	f := &fakePush{}
	logs := &bytes.Buffer{}
	c, ctx, user, session := pushConn(t, f, logs)
	if _, err := c.RegisterPushToken(ctx, &chatv1.RegisterPushTokenRequest{Token: "tok:SECRET", Platform: chatv1.PushPlatform_PUSH_PLATFORM_IOS}); err != nil {
		t.Fatal(err)
	}
	if f.caller.User != user || f.caller.Session != session || f.caller.IssuedAt.IsZero() || f.platform != push.PlatformIOS || f.token != "tok:SECRET" {
		t.Fatalf("iletilen = %+v", f)
	}
	if _, err := c.RegisterPushToken(ctx, &chatv1.RegisterPushTokenRequest{Token: "tok:SECRET"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("platformsuz: %v", err)
	}
	if _, err := c.UnregisterPushToken(ctx, &chatv1.UnregisterPushTokenRequest{}); err != nil || !f.removed || f.caller.Session != session {
		t.Fatalf("silme: %v %+v", err, f)
	}
	// No token: no call.
	if _, err := c.UnregisterPushToken(context.Background(), &chatv1.UnregisterPushTokenRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	for err, want := range map[error]codes.Code{
		apperr.ErrSessionEnded:      codes.Unauthenticated,
		apperr.ErrRateLimited:       codes.ResourceExhausted,
		apperr.Validation("token"):  codes.InvalidArgument,
		errors.New("tok:SECRET iç"): codes.Internal,
	} {
		f.err = err
		if _, err := c.RegisterPushToken(ctx, &chatv1.RegisterPushTokenRequest{Token: "tok:SECRET", Platform: chatv1.PushPlatform_PUSH_PLATFORM_ANDROID}); status.Code(err) != want {
			t.Errorf("%v → %v", f.err, err)
		} else if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("hata token'ı içeriyor: %v", err)
		}
	}
	_ = logs
}
