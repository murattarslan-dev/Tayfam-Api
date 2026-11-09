package grpc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/realtime"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeTickets struct {
	user, session id.ID
	calls         int
	err           error
}

func (f *fakeTickets) Issue(_ context.Context, user, session id.ID) (realtime.Ticket, error) {
	f.calls++
	f.user, f.session = user, session
	return realtime.Ticket{Value: "BILET", ExpiresAt: time.Date(2026, 11, 9, 12, 0, 30, 0, time.UTC)}, f.err
}

func realtimeConn(t *testing.T, c *satest.Cluster, app Tickets) (chatv1.RealtimeServiceClient, *auth.Signer) {
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
	chatv1.RegisterRealtimeServiceServer(srv, NewRealtimeServer(app, slog.New(slog.NewTextHandler(io.Discard, nil))))
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
	return chatv1.NewRealtimeServiceClient(conn), signer
}

// The ticket belongs to the user and the session of the verified token;
// service tokens get none.
func TestCreateRealtimeTicket(t *testing.T) {
	c := satest.New(t)
	app := &fakeTickets{}
	client, signer := realtimeConn(t, c, app)
	user, session := id.New(), id.New()
	tok, _, err := signer.Issue(user.String(), session.String())
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithToken(context.Background(), tok)
	resp, err := client.CreateRealtimeTicket(ctx, &chatv1.CreateRealtimeTicketRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetTicket() != "BILET" || resp.GetExpireTime().AsTime().Second() != 30 || app.user != user || app.session != session {
		t.Fatalf("cevap %v, uygulamaya giden %v %v", resp, app.user, app.session)
	}

	app.err = apperr.ErrTicketLimit
	_, err = client.CreateRealtimeTicket(ctx, &chatv1.CreateRealtimeTicketRequest{})
	st := status.Convert(err)
	info, _ := st.Details()[0].(*errdetails.ErrorInfo)
	if st.Code() != codes.ResourceExhausted || info.GetReason() != "ERROR_REASON_TICKET_LIMIT" {
		t.Fatalf("sınır: %v", err)
	}
	app.err = apperr.ErrSessionEnded
	if _, err := client.CreateRealtimeTicket(ctx, &chatv1.CreateRealtimeTicketRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("kapatılan oturum: %v", err)
	}
	app.err = errors.New("veritabanı")
	if _, err := client.CreateRealtimeTicket(ctx, &chatv1.CreateRealtimeTicketRequest{}); status.Code(err) != codes.Internal {
		t.Fatalf("iç hata: %v", err)
	}

	calls := app.calls
	sctx := auth.WithToken(context.Background(), c.ServiceToken(t, identitySA, "chat.tayfam.internal"))
	if _, err := client.CreateRealtimeTicket(sctx, &chatv1.CreateRealtimeTicketRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("servis token'ı: %v", err)
	}
	if _, err := client.CreateRealtimeTicket(context.Background(), &chatv1.CreateRealtimeTicketRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token yok: %v", err)
	}
	if app.calls != calls {
		t.Fatal("reddedilen çağrı uygulamaya ulaştı")
	}
}

func TestApplySessionEvents(t *testing.T) {
	c := satest.New(t)
	app := &fakeApp{}
	users, _, _ := eventConn(t, c, app)
	ctx := auth.WithToken(context.Background(), c.ServiceToken(t, identitySA, "chat.tayfam.internal"))
	u, s := id.New(), id.New()
	if _, err := users.ApplyUserEvent(ctx, &chatv1.ApplyUserEventRequest{EventId: id.New().String(), UserId: u.String(),
		Event: &chatv1.ApplyUserEventRequest_SessionRevoked{SessionRevoked: &chatv1.SessionRevoked{SessionId: s.String()}}}); err != nil {
		t.Fatal(err)
	}
	if got := app.user[len(app.user)-1]; got.User != u || got.SessionRevoked != s || got.Sanctioned || got.Deleted {
		t.Fatalf("oturum iptali = %+v", got)
	}
	if _, err := users.ApplyUserEvent(ctx, &chatv1.ApplyUserEventRequest{EventId: id.New().String(), UserId: u.String(),
		Event: &chatv1.ApplyUserEventRequest_Sanctioned{Sanctioned: &chatv1.UserSanctioned{}}}); err != nil {
		t.Fatal(err)
	}
	if got := app.user[len(app.user)-1]; got.User != u || !got.Sanctioned || got.SessionRevoked != (id.ID{}) {
		t.Fatalf("yaptırım = %+v", got)
	}
	if _, err := users.ApplyUserEvent(ctx, &chatv1.ApplyUserEventRequest{EventId: id.New().String(), UserId: u.String(),
		Event: &chatv1.ApplyUserEventRequest_SessionRevoked{SessionRevoked: &chatv1.SessionRevoked{SessionId: "x"}}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bozuk oturum: %v", err)
	}
}
