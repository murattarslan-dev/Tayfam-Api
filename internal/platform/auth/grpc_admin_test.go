package auth_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
)

const (
	adminService = "tayfam.test.v1.AdminService"
	adminMethod  = "/" + adminService + "/Check"
	// adminSignIn is a public method inside an admin service, like the
	// panel's sign-in.
	adminSignIn = "/" + adminService + "/Watch"
)

type adminSeen struct {
	user, service, admin bool
}

type adminRecorder struct {
	healthpb.UnimplementedHealthServer
	ch chan adminSeen
}

func (r *adminRecorder) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	_, user := auth.CallerFrom(ctx)
	_, svc := auth.ServiceCallerFrom(ctx)
	_, admin := auth.AdminCallerFrom(ctx)
	r.ch <- adminSeen{user: user, service: svc, admin: admin}
	return &healthpb.HealthCheckResponse{}, nil
}

func (r *adminRecorder) Watch(_ *healthpb.HealthCheckRequest, s grpc.ServerStreamingServer[healthpb.HealthCheckResponse]) error {
	r.ch <- adminSeen{}
	return s.Send(&healthpb.HealthCheckResponse{})
}

func adminSigner(t *testing.T) (*auth.AdminSigner, *auth.AdminVerifier) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize) // same key as userPair
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	s, err := auth.NewAdminSigner(auth.AdminSignerConfig{Key: key, Issuer: "tayfam-identity"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewAdminVerifier(auth.AdminVerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity"})
	if err != nil {
		t.Fatal(err)
	}
	return s, v
}

// TestGuardAdminMethods: on an internal service, admin tokens reach admin
// methods only, and admin methods take admin tokens only (ADR-0008).
func TestGuardAdminMethods(t *testing.T) {
	c := satest.New(t)
	userSigner, uv := userPair(t)
	as, av := adminSigner(t)
	const user, sid = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"
	userTok, _, err := userSigner.Issue(user, sid)
	if err != nil {
		t.Fatal(err)
	}
	adminTok, _, err := as.Issue(user, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	identityTok := c.ServiceToken(t, identitySA, coreAud)

	g, err := auth.NewGuard(uv, adminSignIn).WithServices(auth.ServicePolicy{
		Verifier: c.Verifier(t, coreAud, nil),
		Services: []string{"tayfam.test.v1.EventService"},
		Callers:  map[string][]auth.ServiceAccount{serviceMethod: {identitySA}},
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err = g.WithAdmin(auth.AdminPolicy{Verifier: av, Services: []string{adminService}})
	if err != nil {
		t.Fatal(err)
	}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(g.ServerOptions()...)
	rec := &adminRecorder{ch: make(chan adminSeen, 1)}
	for _, name := range []string{"tayfam.test.v1.UserService", "tayfam.test.v1.EventService", adminService} {
		desc := healthpb.Health_ServiceDesc
		desc.ServiceName = name
		srv.RegisterService(&desc, rec)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cases := []struct {
		name   string
		method string
		token  string
		want   codes.Code
		seen   adminSeen
	}{
		{"admin metodu: admin token'ı", adminMethod, adminTok, codes.OK, adminSeen{admin: true}},
		{"admin metodu: token yok", adminMethod, "", codes.Unauthenticated, adminSeen{}},
		{"admin metodu: uygulama token'ı", adminMethod, userTok, codes.Unauthenticated, adminSeen{}},
		{"admin metodu: servis token'ı", adminMethod, identityTok, codes.Unauthenticated, adminSeen{}},
		{"kullanıcı metodu: admin token'ı", userMethod, adminTok, codes.Unauthenticated, adminSeen{}},
		{"kullanıcı metodu: uygulama token'ı", userMethod, userTok, codes.OK, adminSeen{user: true}},
		{"servis metodu: admin token'ı", serviceMethod, adminTok, codes.Unauthenticated, adminSeen{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.token != "" {
				ctx = auth.WithToken(ctx, tc.token)
			}
			err := conn.Invoke(ctx, tc.method, &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
			if got := status.Code(err); got != tc.want {
				t.Fatalf("kod = %v, beklenen %v (%v)", got, tc.want, err)
			}
			if tc.want == codes.OK {
				if s := <-rec.ch; s != tc.seen {
					t.Fatalf("bağlam = %+v, beklenen %+v", s, tc.seen)
				}
			}
		})
	}

	// The public sign-in method of the admin service needs no token.
	s, err := conn.NewStream(context.Background(), &grpc.StreamDesc{ServerStreams: true}, adminSignIn)
	if err == nil {
		_ = s.SendMsg(&healthpb.HealthCheckRequest{})
		_ = s.CloseSend()
		err = s.RecvMsg(&healthpb.HealthCheckResponse{})
	}
	if err != nil {
		t.Fatalf("açık admin metodu: %v", err)
	}
	<-rec.ch
}

func TestGuardAdminWithoutVerifierIsClosed(t *testing.T) {
	_, uv := userPair(t)
	as, _ := adminSigner(t)
	tok, _, err := as.Issue("0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	g, err := auth.NewGuard(uv).WithAdmin(auth.AdminPolicy{Services: []string{adminService}})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(g.ServerOptions()...)
	desc := healthpb.Health_ServiceDesc
	desc.ServiceName = adminService
	srv.RegisterService(&desc, &adminRecorder{ch: make(chan adminSeen, 1)})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	err = conn.Invoke(auth.WithToken(context.Background(), tok), adminMethod, &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("kod = %v", status.Code(err))
	}
}

func TestGuardWithAdminRejectsOverlap(t *testing.T) {
	_, uv := userPair(t)
	svc := "tayfam.test.v1.EventService"
	g, err := auth.NewGuard(uv).WithServices(auth.ServicePolicy{Services: []string{svc}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.WithAdmin(auth.AdminPolicy{Services: []string{svc}}); err == nil {
		t.Fatal("servis servisi admin servisi olarak da kabul edildi")
	}
	ga, err := auth.NewGuard(uv).WithAdmin(auth.AdminPolicy{Services: []string{svc}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ga.WithServices(auth.ServicePolicy{Services: []string{svc}}); err == nil {
		t.Fatal("admin servisi servis servisi olarak da kabul edildi")
	}
	if _, err := auth.NewGuard(uv).WithAdmin(auth.AdminPolicy{Services: []string{"a/b"}}); err == nil {
		t.Fatal("geçersiz servis adı kabul edildi")
	}
}
