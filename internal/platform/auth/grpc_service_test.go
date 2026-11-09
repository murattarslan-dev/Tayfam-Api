package auth_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
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
	userMethod    = "/tayfam.test.v1.UserService/Check"
	serviceMethod = "/tayfam.test.v1.EventService/Check"
	// unlisted is a method of a service-only service with no caller list.
	unlisted = "/tayfam.test.v1.EventService/Watch"
)

var coreSA = auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-core"}

// seen reports what Guard put in the context of the handler.
type seen struct {
	user    bool
	service auth.ServiceClaims
}

type recorder struct {
	healthpb.UnimplementedHealthServer
	ch chan seen
}

func (r *recorder) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	_, user := auth.CallerFrom(ctx)
	svc, _ := auth.ServiceCallerFrom(ctx)
	r.ch <- seen{user: user, service: svc}
	return &healthpb.HealthCheckResponse{}, nil
}

func (r *recorder) Watch(_ *healthpb.HealthCheckRequest, s grpc.ServerStreamingServer[healthpb.HealthCheckResponse]) error {
	r.ch <- seen{}
	return s.Send(&healthpb.HealthCheckResponse{})
}

func userPair(t *testing.T) (*auth.Signer, *auth.Verifier) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	s, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-identity", Audience: "tayfam-api", TTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	return s, v
}

func serve(t *testing.T, g *auth.Guard, rec *recorder) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(g.ServerOptions()...)
	for _, name := range []string{"tayfam.test.v1.UserService", "tayfam.test.v1.EventService"} {
		desc := healthpb.Health_ServiceDesc
		desc.ServiceName = name
		s.RegisterService(&desc, rec)
	}
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestGuardServiceMethods(t *testing.T) {
	c := satest.New(t)
	signer, uv := userPair(t)
	userTok, _, err := signer.Issue("0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c")
	if err != nil {
		t.Fatal(err)
	}
	g, err := auth.NewGuard(uv).WithServices(auth.ServicePolicy{
		Verifier: c.Verifier(t, coreAud, nil),
		Services: []string{"tayfam.test.v1.EventService"},
		Callers:  map[string][]auth.ServiceAccount{serviceMethod: {identitySA}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{ch: make(chan seen, 1)}
	conn := serve(t, g, rec)

	identityTok := c.ServiceToken(t, identitySA, coreAud)
	cases := []struct {
		name   string
		method string
		token  string
		want   codes.Code
	}{
		{"servis metodu: izinli ServiceAccount", serviceMethod, identityTok, codes.OK},
		{"servis metodu: RS256 token", serviceMethod, c.Token(t, satest.Spec{Alg: "RS256", Audience: []string{coreAud}, Subject: identitySA.Subject()}), codes.OK},
		{"servis metodu: token yok", serviceMethod, "", codes.Unauthenticated},
		{"servis metodu: kullanıcı token'ı", serviceMethod, userTok, codes.Unauthenticated},
		{"servis metodu: izinsiz ServiceAccount", serviceMethod, c.ServiceToken(t, coreSA, coreAud), codes.PermissionDenied},
		{"servis metodu: başka namespace", serviceMethod, c.ServiceToken(t, auth.ServiceAccount{Namespace: "baska", Name: "tayfam-identity"}, coreAud), codes.PermissionDenied},
		{"servis metodu: yanlış audience", serviceMethod, c.ServiceToken(t, identitySA, "chat.tayfam.internal"), codes.Unauthenticated},
		{"servis metodu: süresi dolmuş", serviceMethod, c.Token(t, satest.Spec{Audience: []string{coreAud}, Subject: identitySA.Subject(), IssuedAt: time.Now().Add(-time.Hour)}), codes.Unauthenticated},
		{"listede olmayan servis metodu", unlisted, identityTok, codes.Unauthenticated},
		{"kullanıcı metodu: kullanıcı token'ı", userMethod, userTok, codes.OK},
		{"kullanıcı metodu: servis token'ı", userMethod, identityTok, codes.Unauthenticated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.token != "" {
				ctx = auth.WithToken(ctx, tc.token)
			}
			var err error
			if tc.method == unlisted {
				var s grpc.ClientStream
				s, err = conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, tc.method)
				if err == nil {
					_ = s.SendMsg(&healthpb.HealthCheckRequest{})
					_ = s.CloseSend()
					err = s.RecvMsg(&healthpb.HealthCheckResponse{})
				}
			} else {
				err = conn.Invoke(ctx, tc.method, &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
			}
			if got := status.Code(err); got != tc.want {
				t.Fatalf("kod = %v, beklenen %v (%v)", got, tc.want, err)
			}
			if tc.want != codes.OK {
				return
			}
			s := <-rec.ch
			switch tc.method {
			case serviceMethod:
				if s.user || s.service.Account != identitySA {
					t.Fatalf("servis metodunda bağlam: %+v", s)
				}
			case userMethod:
				if !s.user || s.service.Account != (auth.ServiceAccount{}) {
					t.Fatalf("kullanıcı metodunda bağlam: %+v", s)
				}
			}
		})
	}
}

func TestGuardServiceMethodsWithoutVerifierAreClosed(t *testing.T) {
	c := satest.New(t)
	_, uv := userPair(t)
	g, err := auth.NewGuard(uv).WithServices(auth.ServicePolicy{
		Services: []string{"tayfam.test.v1.EventService"},
		Callers:  map[string][]auth.ServiceAccount{serviceMethod: {identitySA}},
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := serve(t, g, &recorder{ch: make(chan seen, 1)})
	err = conn.Invoke(auth.WithToken(context.Background(), c.ServiceToken(t, identitySA, coreAud)), serviceMethod, &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("kod = %v", status.Code(err))
	}
}

func TestGuardWithServicesRejectsBadPolicy(t *testing.T) {
	_, uv := userPair(t)
	svc := "tayfam.test.v1.EventService"
	for name, tc := range map[string]struct {
		public []string
		p      auth.ServicePolicy
	}{
		"açık servis metodu": {public: []string{serviceMethod}, p: auth.ServicePolicy{Services: []string{svc}}},
		"serviste olmayan metod": {p: auth.ServicePolicy{Services: []string{svc},
			Callers: map[string][]auth.ServiceAccount{userMethod: {identitySA}}}},
		"boş çağıran listesi": {p: auth.ServicePolicy{Services: []string{svc},
			Callers: map[string][]auth.ServiceAccount{serviceMethod: {}}}},
		"geçersiz ServiceAccount": {p: auth.ServicePolicy{Services: []string{svc},
			Callers: map[string][]auth.ServiceAccount{serviceMethod: {{Name: "tayfam-identity"}}}}},
		"geçersiz servis adı": {p: auth.ServicePolicy{Services: []string{"/" + svc}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := auth.NewGuard(uv, tc.public...).WithServices(tc.p); err == nil {
				t.Fatal("hata bekleniyordu")
			}
		})
	}
}

func TestServiceTokenFrom(t *testing.T) {
	c := satest.New(t)
	_, uv := userPair(t)
	g, err := auth.NewGuard(uv).WithServices(auth.ServicePolicy{
		Verifier: c.Verifier(t, coreAud, nil),
		Services: []string{"tayfam.test.v1.EventService"},
		Callers:  map[string][]auth.ServiceAccount{serviceMethod: {identitySA}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{ch: make(chan seen, 1)}
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(g.ServerOptions()...)
	desc := healthpb.Health_ServiceDesc
	desc.ServiceName = "tayfam.test.v1.EventService"
	s.RegisterService(&desc, rec)
	go func() { _ = s.Serve(lis) }()
	defer s.Stop()

	path := filepath.Join(t.TempDir(), "core-token")
	if err := os.WriteFile(path, []byte(c.ServiceToken(t, identitySA, coreAud)), 0o600); err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient("passthrough:///bufnet", append([]grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, auth.ServiceTokenFrom(auth.TokenFile(path))...)...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Invoke(context.Background(), serviceMethod, &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{}); err != nil {
		t.Fatal(err)
	}
	if got := <-rec.ch; got.service.Account != identitySA {
		t.Fatalf("çağıran = %+v", got)
	}
	// A user's token in the context is never sent alongside a service token.
	err = conn.Invoke(auth.WithToken(context.Background(), "kullanici"), serviceMethod, &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
	if err == nil {
		t.Fatal("kullanıcı token'ıyla servis çağrısı yapıldı")
	}
}
