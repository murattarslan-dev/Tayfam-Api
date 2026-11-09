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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/announcement"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeAnnouncements records what transport passed on and returns err.
type fakeAnnouncements struct {
	err    error
	user   id.ID
	tayfa  id.ID
	a      id.ID
	create announcement.CreateInput
	update announcement.UpdateInput
	pinned bool
	view   announcement.View
}

func (f *fakeAnnouncements) Create(_ context.Context, u, t id.ID, in announcement.CreateInput) (announcement.View, error) {
	f.user, f.tayfa, f.create = u, t, in
	return f.view, f.err
}
func (f *fakeAnnouncements) Get(_ context.Context, u, t, a id.ID) (announcement.View, error) {
	f.user, f.tayfa, f.a = u, t, a
	return f.view, f.err
}
func (f *fakeAnnouncements) List(_ context.Context, u, t id.ID, _ int32, _ string) ([]announcement.View, string, error) {
	f.user, f.tayfa = u, t
	return []announcement.View{f.view, f.view}, "sonraki", f.err
}
func (f *fakeAnnouncements) Update(_ context.Context, u, t, a id.ID, in announcement.UpdateInput) (announcement.View, error) {
	f.user, f.tayfa, f.a, f.update = u, t, a, in
	return f.view, f.err
}
func (f *fakeAnnouncements) SetPinned(_ context.Context, u, t, a id.ID, p bool) (announcement.View, error) {
	f.user, f.tayfa, f.a, f.pinned = u, t, a, p
	return f.view, f.err
}
func (f *fakeAnnouncements) Delete(_ context.Context, u, t, a id.ID) error {
	f.user, f.tayfa, f.a = u, t, a
	return f.err
}
func (f *fakeAnnouncements) Restore(_ context.Context, u, t, a id.ID) (announcement.View, error) {
	f.user, f.tayfa, f.a = u, t, a
	return f.view, f.err
}

// callAllAnnouncements calls every AnnouncementService method.
func callAllAnnouncements(ctx context.Context, c corev1.AnnouncementServiceServer, tayfaID, aID string) map[string]error {
	out := map[string]error{}
	_, out["CreateAnnouncement"] = c.CreateAnnouncement(ctx, &corev1.CreateAnnouncementRequest{TayfaId: tayfaID, Title: "Duyuru", ClientRequestId: id.New().String()})
	_, out["GetAnnouncement"] = c.GetAnnouncement(ctx, &corev1.GetAnnouncementRequest{TayfaId: tayfaID, AnnouncementId: aID})
	_, out["ListAnnouncements"] = c.ListAnnouncements(ctx, &corev1.ListAnnouncementsRequest{TayfaId: tayfaID})
	_, out["UpdateAnnouncement"] = c.UpdateAnnouncement(ctx, &corev1.UpdateAnnouncementRequest{TayfaId: tayfaID, AnnouncementId: aID, Title: "Yeni", UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}}})
	_, out["SetAnnouncementPinned"] = c.SetAnnouncementPinned(ctx, &corev1.SetAnnouncementPinnedRequest{TayfaId: tayfaID, AnnouncementId: aID, Pinned: true})
	_, out["DeleteAnnouncement"] = c.DeleteAnnouncement(ctx, &corev1.DeleteAnnouncementRequest{TayfaId: tayfaID, AnnouncementId: aID})
	_, out["RestoreAnnouncement"] = c.RestoreAnnouncement(ctx, &corev1.RestoreAnnouncementRequest{TayfaId: tayfaID, AnnouncementId: aID})
	return out
}

func TestAnnouncementServerNeedsCaller(t *testing.T) {
	f := &fakeAnnouncements{}
	srv := NewAnnouncementServer(f, discard)
	for method, err := range callAllAnnouncements(context.Background(), srv, id.New().String(), id.New().String()) {
		if code, info := infoOf(t, err); code != codes.Unauthenticated || info != nil {
			t.Errorf("%s: %v", method, err)
		}
	}
	if f.user != (id.ID{}) {
		t.Fatal("token'sız istek app katmanına ulaştı")
	}
}

func TestAnnouncementServerValidatesIDs(t *testing.T) {
	f := &fakeAnnouncements{}
	srv := NewAnnouncementServer(f, discard)
	ctx := callerCtx(id.New())
	field := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}
	good := id.New().String()
	for _, bad := range []string{"", "abc", "' OR 1=1 --"} {
		for method, err := range callAllAnnouncements(ctx, srv, bad, good) {
			if got := field(err); got != "tayfa_id" {
				t.Errorf("%s tayfa %q: %s", method, bad, got)
			}
		}
		for method, err := range callAllAnnouncements(ctx, srv, good, bad) {
			if method == "CreateAnnouncement" || method == "ListAnnouncements" {
				continue
			}
			if got := field(err); got != "announcement_id" {
				t.Errorf("%s duyuru %q: %s", method, bad, got)
			}
		}
	}
	// Only Create and List (valid tayfa_id, no announcement_id) got through.
	if f.a != (id.ID{}) || f.tayfa.String() != good {
		t.Fatal("geçersiz kimlik app katmanına ulaştı")
	}
}

func TestAnnouncementServerConverts(t *testing.T) {
	user, tid, aid := id.New(), id.New(), id.New()
	now := time.Now().UTC()
	f := &fakeAnnouncements{view: announcement.View{ID: aid, TayfaID: tid, Title: "Başlık", Body: "Gövde", Pinned: true, CreatedAt: now, UpdatedAt: now}}
	srv := NewAnnouncementServer(f, discard)
	ctx := callerCtx(user)

	resp, err := srv.UpdateAnnouncement(ctx, &corev1.UpdateAnnouncementRequest{
		TayfaId: tid.String(), AnnouncementId: aid.String(), Title: "T", Body: "B",
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"body"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.user != user || f.tayfa != tid || f.a != aid || len(f.update.Fields) != 1 || f.update.Fields[0] != "body" || f.update.Body != "B" {
		t.Fatalf("app'e giden = %+v", f)
	}
	p := resp.GetAnnouncement()
	if p.GetId() != aid.String() || p.GetTayfaId() != tid.String() || p.GetAuthorUserId() != "" || !p.GetPinned() ||
		p.GetTitle() != "Başlık" || !p.GetCreateTime().AsTime().Equal(now) {
		t.Fatalf("dönüştürme = %v", p)
	}
	if _, err := srv.SetAnnouncementPinned(ctx, &corev1.SetAnnouncementPinnedRequest{TayfaId: tid.String(), AnnouncementId: aid.String(), Pinned: true}); err != nil || !f.pinned {
		t.Fatalf("sabitleme: %v", err)
	}
	crid := id.New().String()
	if _, err := srv.CreateAnnouncement(ctx, &corev1.CreateAnnouncementRequest{TayfaId: tid.String(), Title: "T", Body: "B", ClientRequestId: crid}); err != nil ||
		f.create != (announcement.CreateInput{Title: "T", Body: "B", ClientRequestID: crid}) {
		t.Fatalf("oluşturma: %+v %v", f.create, err)
	}
	list, err := srv.ListAnnouncements(ctx, &corev1.ListAnnouncementsRequest{TayfaId: tid.String()})
	if err != nil || len(list.GetAnnouncements()) != 2 || list.GetNextPageToken() != "sonraki" {
		t.Fatalf("liste: %v %v", list, err)
	}

	f.err = apperr.ErrModuleDisabled
	for method, err := range callAllAnnouncements(ctx, srv, tid.String(), aid.String()) {
		code, info := infoOf(t, err)
		if code != codes.FailedPrecondition || info.GetReason() != corev1.ErrorReason_ERROR_REASON_MODULE_DISABLED.String() {
			t.Errorf("%s: %v", method, err)
		}
	}
}

// TestAnnouncementServiceBehindGuard serves the real services behind
// auth.Guard, as cmd/core does.
func TestAnnouncementServiceBehindGuard(t *testing.T) {
	key, err := auth.ParseSigningKey("test", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
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
	pool := dbtest.New(t, "core", coredb.Migrations())
	tayfas, err := tayfa.New(tayfa.Config{Pool: pool, Log: discard})
	if err != nil {
		t.Fatal(err)
	}
	anns, err := announcement.New(announcement.Config{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(auth.NewGuard(v).ServerOptions()...)
	corev1.RegisterTayfaServiceServer(s, NewTayfaServer(tayfas, discard))
	corev1.RegisterAnnouncementServiceServer(s, NewAnnouncementServer(anns, discard))
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
	tc, ac := corev1.NewTayfaServiceClient(conn), corev1.NewAnnouncementServiceClient(conn)

	owner, stranger := id.New(), id.New()
	created, err := tc.CreateTayfa(as(owner), &corev1.CreateTayfaRequest{Name: "Sahibin Tayfası", ClientRequestId: id.New().String()})
	if err != nil {
		t.Fatal(err)
	}
	tid := created.GetTayfa().GetId()
	a, err := ac.CreateAnnouncement(as(owner), &corev1.CreateAnnouncementRequest{TayfaId: tid, Title: "Pazar konvoyu", Body: "Saat 9'da", ClientRequestId: id.New().String()})
	if err != nil {
		t.Fatal(err)
	}
	aid := a.GetAnnouncement().GetId()
	if a.GetAnnouncement().GetAuthorUserId() != owner.String() {
		t.Fatalf("yazar = %v", a)
	}

	client := announcementClientServer{c: ac}
	for method, err := range callAllAnnouncements(context.Background(), client, tid, aid) {
		if code, info := infoOf(t, err); code != codes.Unauthenticated || info != nil {
			t.Errorf("token'sız %s: %v", method, err)
		}
	}
	for method, err := range callAllAnnouncements(as(stranger), client, tid, aid) {
		code, info := infoOf(t, err)
		if code != codes.NotFound || info.GetReason() != corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String() {
			t.Errorf("yabancı %s: %v", method, err)
		}
	}
	got, err := ac.GetAnnouncement(as(owner), &corev1.GetAnnouncementRequest{TayfaId: tid, AnnouncementId: aid})
	if err != nil || got.GetAnnouncement().GetTitle() != "Pazar konvoyu" || got.GetAnnouncement().GetPinned() {
		t.Fatalf("duyuru değişti: %v %v", got, err)
	}
}

// announcementClientServer adapts the client to callAllAnnouncements.
type announcementClientServer struct {
	corev1.UnimplementedAnnouncementServiceServer
	c corev1.AnnouncementServiceClient
}

func (s announcementClientServer) CreateAnnouncement(ctx context.Context, r *corev1.CreateAnnouncementRequest) (*corev1.CreateAnnouncementResponse, error) {
	return s.c.CreateAnnouncement(ctx, r)
}
func (s announcementClientServer) GetAnnouncement(ctx context.Context, r *corev1.GetAnnouncementRequest) (*corev1.GetAnnouncementResponse, error) {
	return s.c.GetAnnouncement(ctx, r)
}
func (s announcementClientServer) ListAnnouncements(ctx context.Context, r *corev1.ListAnnouncementsRequest) (*corev1.ListAnnouncementsResponse, error) {
	return s.c.ListAnnouncements(ctx, r)
}
func (s announcementClientServer) UpdateAnnouncement(ctx context.Context, r *corev1.UpdateAnnouncementRequest) (*corev1.UpdateAnnouncementResponse, error) {
	return s.c.UpdateAnnouncement(ctx, r)
}
func (s announcementClientServer) SetAnnouncementPinned(ctx context.Context, r *corev1.SetAnnouncementPinnedRequest) (*corev1.SetAnnouncementPinnedResponse, error) {
	return s.c.SetAnnouncementPinned(ctx, r)
}
func (s announcementClientServer) DeleteAnnouncement(ctx context.Context, r *corev1.DeleteAnnouncementRequest) (*corev1.DeleteAnnouncementResponse, error) {
	return s.c.DeleteAnnouncement(ctx, r)
}
func (s announcementClientServer) RestoreAnnouncement(ctx context.Context, r *corev1.RestoreAnnouncementRequest) (*corev1.RestoreAnnouncementResponse, error) {
	return s.c.RestoreAnnouncement(ctx, r)
}
