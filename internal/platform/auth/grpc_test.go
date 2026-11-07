package auth

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/durationpb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

// keyServer is a stand-in KeyService.
type keyServer struct {
	identityv1.UnimplementedKeyServiceServer
	keys []PublicKey
}

func (s *keyServer) ListPublicKeys(context.Context, *identityv1.ListPublicKeysRequest) (*identityv1.ListPublicKeysResponse, error) {
	resp := &identityv1.ListPublicKeysResponse{CacheTtl: durationpb.New(5 * time.Minute)}
	for _, k := range s.keys {
		resp.Keys = append(resp.Keys, &identityv1.PublicKey{Kid: k.ID, Algorithm: identityv1.KeyAlgorithm_KEY_ALGORITHM_ED25519, PublicKey: k.Key})
	}
	return resp, nil
}

// echoCaller is the health handler registered under a non-health service
// name. It stands in for a protected method and reports the caller it saw.
// It also checks that the guard decides on the actual route: the generated
// handler still reports "/grpc.health.v1.Health/Check" as its method.
type echoCaller struct {
	healthpb.UnimplementedHealthServer
	seen chan Claims
}

func (e *echoCaller) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	c, _ := CallerFrom(ctx)
	e.seen <- c
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

const listKeys = "/tayfam.identity.v1.KeyService/ListPublicKeys"

func startServer(t *testing.T, g *Guard, register func(*grpc.Server), dialOpts ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(g.ServerOptions()...)
	register(s)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", append([]grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, dialOpts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestGuard(t *testing.T) {
	key := testKey(t, "k1")
	s, v := newPair(t, key, time.Now(), nil)
	tok, _, err := s.Issue(testUser, testSession)
	if err != nil {
		t.Fatal(err)
	}

	echo := &echoCaller{seen: make(chan Claims, 1)}
	conn := startServer(t, NewGuard(v, listKeys), func(srv *grpc.Server) {
		identityv1.RegisterKeyServiceServer(srv, &keyServer{keys: []PublicKey{key.Public()}})
		healthpb.RegisterHealthServer(srv, health.NewServer())
		// The same handler registered under a protected service name.
		desc := healthpb.Health_ServiceDesc
		desc.ServiceName = "tayfam.test.v1.Protected"
		srv.RegisterService(&desc, echo)
	})
	protected := func(ctx context.Context) error {
		return conn.Invoke(ctx, "/tayfam.test.v1.Protected/Check", &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
	}
	ctx := context.Background()

	t.Run("açık metod token istemez", func(t *testing.T) {
		if _, err := identityv1.NewKeyServiceClient(conn).ListPublicKeys(ctx, &identityv1.ListPublicKeysRequest{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("health token istemez", func(t *testing.T) {
		if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
			t.Fatal(err)
		}
	})
	for name, md := range map[string]metadata.MD{
		"token yok":       nil,
		"bearer yok":      metadata.Pairs("authorization", tok),
		"geçersiz token":  metadata.Pairs("authorization", "Bearer a.b.c"),
		"iki token":       metadata.Pairs("authorization", "Bearer "+tok, "authorization", "Bearer "+tok),
		"küçük harf şema": metadata.Pairs("authorization", "bearer "+tok),
	} {
		t.Run("korumalı metod: "+name, func(t *testing.T) {
			err := protected(metadata.NewOutgoingContext(ctx, md))
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("kod = %v", status.Code(err))
			}
			if st, _ := status.FromError(err); st.Message() != "kimlik doğrulanamadı" {
				t.Fatalf("hata ayrıntı sızdırıyor: %q", st.Message())
			}
		})
	}
	t.Run("korumalı metod: geçerli token", func(t *testing.T) {
		if err := protected(WithToken(ctx, tok)); err != nil {
			t.Fatal(err)
		}
		if c := <-echo.seen; c.Subject != testUser || c.SessionID != testSession {
			t.Fatalf("caller = %+v", c)
		}
	})
}

func TestGuardKeySourceDownIsUnavailable(t *testing.T) {
	key := testKey(t, "k1")
	s, _ := newPair(t, key, time.Now(), nil)
	tok, _, _ := s.Issue(testUser, testSession)
	v, _ := NewVerifier(VerifierConfig{Keys: failingKeys{}, Issuer: testIssuer, Audience: testAudience})
	conn := startServer(t, NewGuard(v), func(srv *grpc.Server) {
		desc := healthpb.Health_ServiceDesc
		desc.ServiceName = "tayfam.test.v1.Protected"
		srv.RegisterService(&desc, &echoCaller{seen: make(chan Claims, 1)})
	})
	err := conn.Invoke(WithToken(context.Background(), tok), "/tayfam.test.v1.Protected/Check", &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("kod = %v", status.Code(err))
	}
}

func TestFetchFromIdentityAndForwardToken(t *testing.T) {
	key := testKey(t, "k1")
	signer, _ := newPair(t, key, time.Now(), nil)
	tok, _, _ := signer.Issue(testUser, testSession)

	// identity: serves keys publicly and a protected method.
	idEcho := &echoCaller{seen: make(chan Claims, 1)}
	staticV, _ := NewVerifier(VerifierConfig{Keys: StaticKeys{"k1": key.Public().Key}, Issuer: testIssuer, Audience: testAudience})
	idConn := startServer(t, NewGuard(staticV, listKeys), func(srv *grpc.Server) {
		identityv1.RegisterKeyServiceServer(srv, &keyServer{keys: []PublicKey{key.Public(), {ID: "k2", Key: make([]byte, 3)}}})
		desc := healthpb.Health_ServiceDesc
		desc.ServiceName = "tayfam.test.v1.Protected"
		srv.RegisterService(&desc, idEcho)
	}, ForwardToken()...)

	// core: verifies with keys fetched from identity, then calls identity
	// on behalf of the same user.
	remoteV, err := NewVerifier(VerifierConfig{Keys: NewRemoteKeys(FetchFromIdentity(identityv1.NewKeyServiceClient(idConn))), Issuer: testIssuer, Audience: testAudience})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := NewGuard(remoteV).authenticate(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tok)), "/tayfam.core.v1.X/Y")
	if err != nil {
		t.Fatalf("uzak anahtarla doğrulanamadı: %v", err)
	}
	// The client was built with ForwardToken: the verified caller's token
	// travels on unchanged, without the handler touching metadata.
	if err := idConn.Invoke(ctx, "/tayfam.test.v1.Protected/Check", &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{}); err != nil {
		t.Fatal(err)
	}
	if c := <-idEcho.seen; c.Subject != testUser {
		t.Fatalf("identity'de caller = %+v", c)
	}

	// Outside a verified request nothing is attached.
	if _, ok := metadata.FromOutgoingContext(withToken(context.Background())); ok {
		t.Fatal("doğrulanmamış bağlamda token eklendi")
	}
}
