package grpc

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/catalogcopy"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
)

type fakeCopies struct {
	mu      sync.Mutex
	got     []catalogcopy.Change
	applied bool
	err     error
}

func (f *fakeCopies) Apply(_ context.Context, c catalogcopy.Change) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, c)
	return f.applied, f.err
}

func (f *fakeCopies) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func catalogRequest() *identityv1.ApplyCatalogChangeRequest {
	return &identityv1.ApplyCatalogChangeRequest{EventId: "x", CatalogVersion: 3, Interests: []*identityv1.CatalogInterestState{
		{Key: "nature", Status: identityv1.CatalogInterestStatus_CATALOG_INTEREST_STATUS_ACTIVE},
		{Key: "water", Status: identityv1.CatalogInterestStatus_CATALOG_INTEREST_STATUS_RETIRED},
	}}
}

// TestApplyCatalogChange: the request maps to the app layer's change, and
// the app layer's answers to codes.
func TestApplyCatalogChange(t *testing.T) {
	f := &fakeCopies{applied: true}
	srv := NewCatalogCopyServer(f, discard())
	if _, err := srv.ApplyCatalogChange(context.Background(), catalogRequest()); status.Code(err) != codes.Unauthenticated || len(f.got) != 0 {
		t.Fatalf("servis çağıranı olmadan: %v", err)
	}
	ctx := auth.WithServiceCallerForTest(context.Background(), auth.ServiceClaims{})
	resp, err := srv.ApplyCatalogChange(ctx, catalogRequest())
	if err != nil || !resp.GetApplied() {
		t.Fatalf("yanıt = %v, %v", resp, err)
	}
	want := catalogcopy.Change{Version: 3, Interests: []catalogcopy.Interest{
		{Key: "nature", Status: catalogcopy.StatusActive}, {Key: "water", Status: catalogcopy.StatusRetired},
	}}
	if len(f.got) != 1 || f.got[0].Version != want.Version || len(f.got[0].Interests) != 2 ||
		f.got[0].Interests[0] != want.Interests[0] || f.got[0].Interests[1] != want.Interests[1] {
		t.Fatalf("değişiklik = %+v", f.got)
	}

	// An unspecified status never reaches the app layer.
	req := catalogRequest()
	req.Interests[1].Status = identityv1.CatalogInterestStatus_CATALOG_INTEREST_STATUS_UNSPECIFIED
	if _, err := srv.ApplyCatalogChange(ctx, req); status.Code(err) != codes.InvalidArgument || len(f.got) != 1 {
		t.Fatalf("belirsiz durum: %v", err)
	}
	req.Interests[1].Status = identityv1.CatalogInterestStatus(99)
	if _, err := srv.ApplyCatalogChange(ctx, req); status.Code(err) != codes.InvalidArgument || len(f.got) != 1 {
		t.Fatalf("bilinmeyen durum: %v", err)
	}
	req = catalogRequest()
	for range catalogcopy.MaxInterests {
		req.Interests = append(req.Interests, req.Interests[0])
	}
	if _, err := srv.ApplyCatalogChange(ctx, req); status.Code(err) != codes.InvalidArgument || len(f.got) != 1 {
		t.Fatalf("çok fazla ilgi alanı: %v", err)
	}

	f.applied, f.err = false, catalogcopy.ErrInvalid
	if _, err := srv.ApplyCatalogChange(ctx, catalogRequest()); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("geçersiz: %v", err)
	}
	f.err = errors.New("veritabanı")
	if _, err := srv.ApplyCatalogChange(ctx, catalogRequest()); status.Code(err) != codes.Internal {
		t.Fatalf("iç hata: %v", err)
	}
	f.err = nil
	if resp, err := srv.ApplyCatalogChange(ctx, catalogRequest()); err != nil || resp.GetApplied() {
		t.Fatalf("eski sürüm: %v, %v", resp, err)
	}
}

// TestApplyCatalogChangeAuthentication: behind the same Guard policy as
// cmd/identity, only core's ServiceAccount reaches the method (ADR-0013,
// ADR-0042).
func TestApplyCatalogChangeAuthentication(t *testing.T) {
	c := satest.New(t)
	g, err := auth.NewGuard(nil, PublicMethods...).WithServices(auth.ServicePolicy{
		Verifier: c.Verifier(t, auth.ServiceAudience("identity"), nil),
		Services: []string{CatalogCopyServiceName},
		Callers: map[string][]auth.ServiceAccount{
			identityv1.CatalogCopyService_ApplyCatalogChange_FullMethodName: {{Namespace: "tayfam-test", Name: "tayfam-core"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &fakeCopies{applied: true}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(g.ServerOptions()...)
	identityv1.RegisterCatalogCopyServiceServer(srv, NewCatalogCopyServer(app, discard()))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := identityv1.NewCatalogCopyServiceClient(conn)

	core := auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-core"}
	for _, tc := range []struct {
		name  string
		token string
		want  codes.Code
	}{
		{"core ServiceAccount", c.ServiceToken(t, core, "identity.tayfam.internal"), codes.OK},
		{"token yok", "", codes.Unauthenticated},
		{"yanlış audience", c.ServiceToken(t, core, "core.tayfam.internal"), codes.Unauthenticated},
		{"yanlış ServiceAccount (chat)", c.ServiceToken(t, auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-chat"}, "identity.tayfam.internal"), codes.PermissionDenied},
		{"yanlış ServiceAccount (gateway)", c.ServiceToken(t, auth.ServiceAccount{Namespace: "tayfam-test", Name: "tayfam-gateway"}, "identity.tayfam.internal"), codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.token != "" {
				ctx = auth.WithToken(ctx, tc.token)
			}
			before := app.calls()
			_, err := client.ApplyCatalogChange(ctx, catalogRequest())
			if got := status.Code(err); got != tc.want {
				t.Fatalf("kod = %v, beklenen %v", got, tc.want)
			}
			if tc.want != codes.OK && app.calls() != before {
				t.Fatal("reddedilen çağrı uygulamaya ulaştı")
			}
		})
	}
}
