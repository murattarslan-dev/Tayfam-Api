package grpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeTayfas records what transport passed on and returns err.
type fakeTayfas struct {
	err    error
	user   id.ID
	tayfa  id.ID
	create tayfa.CreateInput
	update tayfa.UpdateInput
	module string
	view   tayfa.View
}

func (f *fakeTayfas) Create(_ context.Context, u id.ID, in tayfa.CreateInput) (tayfa.View, error) {
	f.user, f.create = u, in
	return f.view, f.err
}
func (f *fakeTayfas) Get(_ context.Context, u, t id.ID) (tayfa.View, authz.Role, error) {
	f.user, f.tayfa = u, t
	return f.view, authz.Moderator, f.err
}
func (f *fakeTayfas) ListMine(_ context.Context, u id.ID, _ int32, _ string) ([]tayfa.MyTayfa, string, error) {
	f.user = u
	v := f.view
	return []tayfa.MyTayfa{
		{TayfaID: v.ID, Name: v.Name, Role: authz.Admin, Status: authz.StatusActive, Tayfa: &v},
		{TayfaID: id.New(), Name: "Bekleyen", Role: authz.Member, Status: authz.StatusPending},
	}, "sonraki", f.err
}
func (f *fakeTayfas) Update(_ context.Context, u, t id.ID, in tayfa.UpdateInput) (tayfa.View, error) {
	f.user, f.tayfa, f.update = u, t, in
	return f.view, f.err
}
func (f *fakeTayfas) SetModule(_ context.Context, u, t id.ID, m string, _ bool) error {
	f.user, f.tayfa, f.module = u, t, m
	return f.err
}
func (f *fakeTayfas) RotateJoinCode(_ context.Context, u, t id.ID) (string, error) {
	f.user, f.tayfa = u, t
	return "ABCDEFGH", f.err
}
func (f *fakeTayfas) RequestDeletion(_ context.Context, u, t id.ID) (time.Time, error) {
	f.user, f.tayfa = u, t
	return time.Now(), f.err
}
func (f *fakeTayfas) CancelDeletion(_ context.Context, u, t id.ID) error {
	f.user, f.tayfa = u, t
	return f.err
}

func callerCtx(u id.ID) context.Context {
	return auth.WithCallerForTest(context.Background(), auth.Claims{Subject: u.String(), SessionID: id.New().String(), Audience: "tayfam-api"})
}

// callAll calls every TayfaService method on tayfaID and returns the errors
// by method name.
func callAll(ctx context.Context, c corev1.TayfaServiceServer, tayfaID string) map[string]error {
	out := map[string]error{}
	_, out["CreateTayfa"] = c.CreateTayfa(ctx, &corev1.CreateTayfaRequest{Name: "Tayfa", ClientRequestId: id.New().String()})
	_, out["GetTayfa"] = c.GetTayfa(ctx, &corev1.GetTayfaRequest{TayfaId: tayfaID})
	_, out["ListMyTayfas"] = c.ListMyTayfas(ctx, &corev1.ListMyTayfasRequest{})
	_, out["UpdateTayfa"] = c.UpdateTayfa(ctx, &corev1.UpdateTayfaRequest{TayfaId: tayfaID, Name: "Yeni Ad", UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}}})
	_, out["SetModule"] = c.SetModule(ctx, &corev1.SetModuleRequest{TayfaId: tayfaID, Module: corev1.Module_MODULE_QA})
	_, out["RotateJoinCode"] = c.RotateJoinCode(ctx, &corev1.RotateJoinCodeRequest{TayfaId: tayfaID})
	_, out["RequestDeletion"] = c.RequestDeletion(ctx, &corev1.RequestDeletionRequest{TayfaId: tayfaID})
	_, out["CancelDeletion"] = c.CancelDeletion(ctx, &corev1.CancelDeletionRequest{TayfaId: tayfaID})
	return out
}

func TestTayfaServerNeedsCaller(t *testing.T) {
	f := &fakeTayfas{}
	srv := NewTayfaServer(f, discard)
	for method, err := range callAll(context.Background(), srv, id.New().String()) {
		if code, info := infoOf(t, err); code != codes.Unauthenticated || info != nil {
			t.Errorf("%s: %v", method, err)
		}
	}
	if f.user != (id.ID{}) {
		t.Fatal("token'sız istek app katmanına ulaştı")
	}
}

func TestTayfaServerValidatesRequest(t *testing.T) {
	f := &fakeTayfas{}
	srv := NewTayfaServer(f, discard)
	ctx := callerCtx(id.New())
	field := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}
	for _, bad := range []string{"", "abc", "' OR 1=1 --", id.New().String() + "x"} {
		for method, err := range callAll(ctx, srv, bad) {
			if method == "CreateTayfa" || method == "ListMyTayfas" {
				continue
			}
			if got := field(err); got != "tayfa_id" {
				t.Errorf("%s %q: %s", method, bad, got)
			}
		}
	}
	if f.tayfa != (id.ID{}) {
		t.Fatal("geçersiz tayfa_id app katmanına ulaştı")
	}

	_, err := srv.CreateTayfa(ctx, &corev1.CreateTayfaRequest{Name: "Tayfa", Interests: []corev1.EventType{corev1.EventType_EVENT_TYPE_UNSPECIFIED}})
	if field(err) != "interests" {
		t.Error("belirtilmemiş ilgi alanı")
	}
	_, err = srv.CreateTayfa(ctx, &corev1.CreateTayfaRequest{Name: "Tayfa", Interests: []corev1.EventType{99}})
	if field(err) != "interests" {
		t.Error("bilinmeyen ilgi alanı")
	}
	_, err = srv.CreateTayfa(ctx, &corev1.CreateTayfaRequest{Name: "Tayfa", JoinPolicy: 7})
	if field(err) != "join_policy" {
		t.Error("bilinmeyen katılım politikası")
	}
	tid := id.New().String()
	_, err = srv.SetModule(ctx, &corev1.SetModuleRequest{TayfaId: tid})
	if field(err) != "module" {
		t.Error("belirtilmemiş modül")
	}
	_, err = srv.UpdateTayfa(ctx, &corev1.UpdateTayfaRequest{TayfaId: tid, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"join_policy"}}})
	if field(err) != "join_policy" {
		t.Error("maskede belirtilmemiş katılım politikası")
	}

	// UNSPECIFIED join policy on create means approval: passed on as "".
	if _, err := srv.CreateTayfa(ctx, &corev1.CreateTayfaRequest{Name: "Tayfa", Interests: []corev1.EventType{corev1.EventType_EVENT_TYPE_HIKE}}); err != nil {
		t.Fatal(err)
	}
	if f.create.JoinPolicy != "" || len(f.create.Interests) != 1 || f.create.Interests[0] != "hike" {
		t.Fatalf("girdi = %+v", f.create)
	}
	// Fields outside the mask are not converted.
	if _, err := srv.UpdateTayfa(ctx, &corev1.UpdateTayfaRequest{TayfaId: tid, JoinPolicy: 9, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}}}); err != nil {
		t.Fatal(err)
	}
	// The event creation setting is passed on with its mask path.
	if _, err := srv.UpdateTayfa(ctx, &corev1.UpdateTayfaRequest{TayfaId: tid, MembersCanCreateEvents: true, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"members_can_create_events"}}}); err != nil {
		t.Fatal(err)
	}
	if !f.update.MembersCanCreateEvents || len(f.update.Fields) != 1 || f.update.Fields[0] != tayfa.FieldMembersCanCreateEvents {
		t.Fatalf("girdi = %+v", f.update)
	}
}

func TestTayfaServerConverts(t *testing.T) {
	user := id.New()
	view := tayfa.View{
		ID: id.New(), Name: "Tayfa", Interests: []string{"camp", "general"}, JoinPolicy: "open", Status: "archived",
		Modules:     []tayfa.ModuleState{{Module: "announcements", Enabled: true}, {Module: "events"}, {Module: "qa", Enabled: true}, {Module: "polls"}},
		MemberCount: 3, JoinCode: "ABCDEFGH", DeletionAt: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
		MembersCanCreateEvents: true,
	}
	f := &fakeTayfas{view: view}
	srv := NewTayfaServer(f, discard)
	resp, err := srv.GetTayfa(callerCtx(user), &corev1.GetTayfaRequest{TayfaId: view.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if f.user != user || f.tayfa != view.ID {
		t.Fatal("kullanıcı token'dan alınmadı")
	}
	p := resp.GetTayfa()
	if resp.GetCallerRole() != corev1.Role_ROLE_MODERATOR || p.GetId() != view.ID.String() ||
		p.GetJoinPolicy() != corev1.JoinPolicy_JOIN_POLICY_OPEN || p.GetStatus() != corev1.TayfaStatus_TAYFA_STATUS_ARCHIVED ||
		len(p.GetInterests()) != 2 || p.GetInterests()[1] != corev1.EventType_EVENT_TYPE_GENERAL ||
		len(p.GetModules()) != 4 || p.GetModules()[2].GetModule() != corev1.Module_MODULE_QA || !p.GetModules()[2].GetEnabled() ||
		p.GetMemberCount() != 3 || p.GetSuspendTime() != nil || p.GetDeletionTime() == nil || !p.GetMembersCanCreateEvents() {
		t.Fatalf("dönüştürme = %v", resp)
	}

	list, err := srv.ListMyTayfas(callerCtx(user), &corev1.ListMyTayfasRequest{})
	if err != nil || len(list.GetTayfas()) != 2 || list.GetNextPageToken() != "sonraki" {
		t.Fatalf("liste = %v %v", list, err)
	}
	if a, p := list.GetTayfas()[0], list.GetTayfas()[1]; a.GetTayfa() == nil || a.GetStatus() != corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE ||
		p.GetTayfa() != nil || p.GetStatus() != corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING || p.GetRole() != corev1.Role_ROLE_MEMBER {
		t.Fatalf("liste = %v", list)
	}
}

// startGuarded serves a real TayfaService behind auth.Guard, as cmd/core
// does, and returns a client and a token issuer.
func startGuarded(t *testing.T) (corev1.TayfaServiceClient, func(user id.ID) context.Context) {
	t.Helper()
	key, err := auth.ParseSigningKey("test", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-test", Audience: "tayfam-api", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{key.ID: key.Public().Key}, Issuer: "tayfam-test", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	pool := dbtest.New(t, coredb.Migrations())
	app, err := tayfa.New(tayfa.Config{Pool: pool, Log: discard})
	if err != nil {
		t.Fatal(err)
	}

	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(auth.NewGuard(v).ServerOptions()...)
	corev1.RegisterTayfaServiceServer(s, NewTayfaServer(app, discard))
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	as := func(user id.ID) context.Context {
		tok, _, err := signer.Issue(user.String(), id.New().String())
		if err != nil {
			t.Fatal(err)
		}
		return auth.WithToken(context.Background(), tok)
	}
	return corev1.NewTayfaServiceClient(conn), as
}

// clientServer adapts the client to callAll.
type clientServer struct {
	corev1.UnimplementedTayfaServiceServer
	c corev1.TayfaServiceClient
}

func (s clientServer) CreateTayfa(ctx context.Context, r *corev1.CreateTayfaRequest) (*corev1.CreateTayfaResponse, error) {
	return s.c.CreateTayfa(ctx, r)
}
func (s clientServer) GetTayfa(ctx context.Context, r *corev1.GetTayfaRequest) (*corev1.GetTayfaResponse, error) {
	return s.c.GetTayfa(ctx, r)
}
func (s clientServer) ListMyTayfas(ctx context.Context, r *corev1.ListMyTayfasRequest) (*corev1.ListMyTayfasResponse, error) {
	return s.c.ListMyTayfas(ctx, r)
}
func (s clientServer) UpdateTayfa(ctx context.Context, r *corev1.UpdateTayfaRequest) (*corev1.UpdateTayfaResponse, error) {
	return s.c.UpdateTayfa(ctx, r)
}
func (s clientServer) SetModule(ctx context.Context, r *corev1.SetModuleRequest) (*corev1.SetModuleResponse, error) {
	return s.c.SetModule(ctx, r)
}
func (s clientServer) RotateJoinCode(ctx context.Context, r *corev1.RotateJoinCodeRequest) (*corev1.RotateJoinCodeResponse, error) {
	return s.c.RotateJoinCode(ctx, r)
}
func (s clientServer) RequestDeletion(ctx context.Context, r *corev1.RequestDeletionRequest) (*corev1.RequestDeletionResponse, error) {
	return s.c.RequestDeletion(ctx, r)
}
func (s clientServer) CancelDeletion(ctx context.Context, r *corev1.CancelDeletionRequest) (*corev1.CancelDeletionResponse, error) {
	return s.c.CancelDeletion(ctx, r)
}

func TestTayfaServiceBehindGuard(t *testing.T) {
	client, as := startGuarded(t)
	owner, stranger := id.New(), id.New()

	created, err := client.CreateTayfa(as(owner), &corev1.CreateTayfaRequest{Name: "Sahibin Tayfası", ClientRequestId: id.New().String()})
	if err != nil {
		t.Fatal(err)
	}
	tid := created.GetTayfa().GetId()
	if created.GetTayfa().GetJoinCode() == "" {
		t.Fatal("yöneticiye katılım kodu dönmedi")
	}

	t.Run("token'sız istek reddedilir", func(t *testing.T) {
		for method, err := range callAll(context.Background(), clientServer{c: client}, tid) {
			if code, info := infoOf(t, err); code != codes.Unauthenticated || info != nil {
				t.Errorf("%s: %v", method, err)
			}
		}
	})

	t.Run("başka tayfanın ID'si NotFound (IDOR)", func(t *testing.T) {
		ctx := as(stranger)
		// The stranger has a tayfa of their own; that changes nothing.
		if _, err := client.CreateTayfa(ctx, &corev1.CreateTayfaRequest{Name: "Yabancının Tayfası", ClientRequestId: id.New().String()}); err != nil {
			t.Fatal(err)
		}
		unknown := id.New().String()
		for _, target := range []string{tid, unknown} {
			for method, err := range callAll(ctx, clientServer{c: client}, target) {
				if method == "CreateTayfa" || method == "ListMyTayfas" {
					continue
				}
				code, info := infoOf(t, err)
				if code != codes.NotFound || info.GetReason() != corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String() || info.GetDomain() != errorDomain {
					t.Errorf("%s %s: %v", method, target, err)
				}
			}
		}
		list, err := client.ListMyTayfas(ctx, &corev1.ListMyTayfasRequest{})
		if err != nil || len(list.GetTayfas()) != 1 || list.GetTayfas()[0].GetTayfaId() == tid {
			t.Fatalf("yabancının listesi = %v %v", list, err)
		}
	})

	t.Run("sahibin tayfası değişmedi", func(t *testing.T) {
		got, err := client.GetTayfa(as(owner), &corev1.GetTayfaRequest{TayfaId: tid})
		if err != nil || got.GetTayfa().GetName() != "Sahibin Tayfası" || got.GetTayfa().GetDeletionTime() != nil ||
			got.GetTayfa().GetJoinCode() != created.GetTayfa().GetJoinCode() || got.GetCallerRole() != corev1.Role_ROLE_ADMIN {
			t.Fatalf("tayfa = %v %v", got, err)
		}
	})

	t.Run("hata yolları", func(t *testing.T) {
		ctx := as(owner)
		reason := func(err error) string {
			_, info := infoOf(t, err)
			return info.GetReason()
		}
		_, err := client.CreateTayfa(ctx, &corev1.CreateTayfaRequest{Name: "İkinci", ClientRequestId: id.New().String()})
		if reason(err) != corev1.ErrorReason_ERROR_REASON_TAYFA_CREATE_LIMIT.String() {
			t.Errorf("günlük sınır: %v", err)
		}
		_, err = client.UpdateTayfa(ctx, &corev1.UpdateTayfaRequest{TayfaId: tid})
		if reason(err) != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Errorf("boş maske: %v", err)
		}
		if _, err := client.CancelDeletion(ctx, &corev1.CancelDeletionRequest{TayfaId: tid}); reason(err) != corev1.ErrorReason_ERROR_REASON_DELETION_NOT_REQUESTED.String() {
			t.Errorf("talepsiz iptal: %v", err)
		}
		if _, err := client.RequestDeletion(ctx, &corev1.RequestDeletionRequest{TayfaId: tid}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.RequestDeletion(ctx, &corev1.RequestDeletionRequest{TayfaId: tid}); reason(err) != corev1.ErrorReason_ERROR_REASON_DELETION_ALREADY_REQUESTED.String() {
			t.Errorf("ikinci talep: %v", err)
		}
		if _, err := client.RotateJoinCode(ctx, &corev1.RotateJoinCodeRequest{TayfaId: tid}); reason(err) != corev1.ErrorReason_ERROR_REASON_TAYFA_READ_ONLY.String() {
			t.Errorf("salt okunur: %v", err)
		}
		if _, err := client.CancelDeletion(ctx, &corev1.CancelDeletionRequest{TayfaId: tid}); err != nil {
			t.Fatal(err)
		}
	})
}

// The fake must satisfy the interface the server needs.
var _ Tayfas = (*fakeTayfas)(nil)
