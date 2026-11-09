package grpc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
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

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeMinors struct {
	mu      sync.Mutex
	calls   int
	answers map[id.ID]bool
	err     error
}

func (f *fakeMinors) IsMinor(_ context.Context, users []id.ID) (map[id.ID]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	out := map[id.ID]bool{}
	for _, u := range users {
		if m, ok := f.answers[u]; ok {
			out[u] = m
		}
	}
	return out, f.err
}

func (f *fakeMinors) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// minorConn serves MinorService behind the same Guard policy as
// cmd/identity: only tayfam-core in the deployment's namespace.
func minorConn(t *testing.T, c *satest.Cluster, app Minors) (client identityv1.MinorServiceClient, userTok string) {
	t.Helper()
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	uv, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := auth.NewGuard(uv, PublicMethods...).WithServices(auth.ServicePolicy{
		Verifier: c.Verifier(t, auth.ServiceAudience("identity"), nil),
		Services: []string{MinorServiceName},
		Callers: map[string][]auth.ServiceAccount{
			identityv1.MinorService_IsMinor_FullMethodName: {{Namespace: "tayfam-test", Name: "tayfam-core"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(g.ServerOptions()...)
	identityv1.RegisterMinorServiceServer(srv, NewMinorServer(app, slog.New(slog.NewTextHandler(io.Discard, nil))))
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
	userTok, _, err = signer.Issue(id.New().String(), id.New().String())
	if err != nil {
		t.Fatal(err)
	}
	return identityv1.NewMinorServiceClient(conn), userTok
}

// TestIsMinorAuthentication: only core's ServiceAccount, with identity's
// audience, gets an answer (ADR-0012, ADR-0013).
func TestIsMinorAuthentication(t *testing.T) {
	c := satest.New(t)
	app := &fakeMinors{}
	client, userTok := minorConn(t, c, app)
	core := auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-core"}
	req := &identityv1.IsMinorRequest{UserIds: []string{id.New().String()}}
	for _, tc := range []struct {
		name  string
		token string
		want  codes.Code
	}{
		{"core ServiceAccount", c.ServiceToken(t, core, "identity.tayfam.internal"), codes.OK},
		{"token yok", "", codes.Unauthenticated},
		{"kullanıcı token'ı", userTok, codes.Unauthenticated},
		{"yanlış audience (core)", c.ServiceToken(t, core, "core.tayfam.internal"), codes.Unauthenticated},
		{"yanlış ServiceAccount (chat)", c.ServiceToken(t, auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-chat"}, "identity.tayfam.internal"), codes.PermissionDenied},
		{"yanlış ServiceAccount (gateway)", c.ServiceToken(t, auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-gateway"}, "identity.tayfam.internal"), codes.PermissionDenied},
		{"başka namespace'teki core", c.ServiceToken(t, auth.ServiceAccount{Namespace: "baska", Name: "tayfam-core"}, "identity.tayfam.internal"), codes.PermissionDenied},
		{"süresi dolmuş", c.Token(t, satest.Spec{Audience: []string{"identity.tayfam.internal"}, Subject: core.Subject(), IssuedAt: time.Now().Add(-time.Hour)}), codes.Unauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.token != "" {
				ctx = auth.WithToken(ctx, tc.token)
			}
			before := app.count()
			_, err := client.IsMinor(ctx, req)
			if got := status.Code(err); got != tc.want {
				t.Fatalf("kod = %v, beklenen %v", got, tc.want)
			}
			if tc.want != codes.OK && app.count() != before {
				t.Fatal("reddedilen çağrı uygulamaya ulaştı")
			}
		})
	}
}

func TestIsMinorAnswers(t *testing.T) {
	c := satest.New(t)
	adult, minor, unknown := id.New(), id.New(), id.New()
	app := &fakeMinors{answers: map[id.ID]bool{adult: false, minor: true}}
	client, _ := minorConn(t, c, app)
	ctx := auth.WithToken(context.Background(), c.ServiceToken(t, auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-core"}, "identity.tayfam.internal"))

	resp, err := client.IsMinor(ctx, &identityv1.IsMinorRequest{UserIds: []string{adult.String(), minor.String(), unknown.String(), minor.String()}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, a := range resp.GetAnswers() {
		got[a.GetUserId()] = a.GetMinor()
	}
	if len(resp.GetAnswers()) != 2 || got[adult.String()] || !got[minor.String()] {
		t.Fatalf("cevaplar = %v", resp.GetAnswers())
	}
	for name, ids := range map[string][]string{
		"boş":   nil,
		"bozuk": {"x"},
		"fazla": {id.New().String(), id.New().String(), id.New().String(), id.New().String(), id.New().String(), id.New().String(), id.New().String(), id.New().String(), id.New().String(), id.New().String(), id.New().String()},
	} {
		if _, err := client.IsMinor(ctx, &identityv1.IsMinorRequest{UserIds: ids}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	app.err = errors.New("veritabanı")
	if _, err := client.IsMinor(ctx, &identityv1.IsMinorRequest{UserIds: []string{adult.String()}}); status.Code(err) != codes.Internal {
		t.Errorf("iç hata: %v", err)
	}
}
