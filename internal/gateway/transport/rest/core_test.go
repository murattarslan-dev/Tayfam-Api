package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

const (
	tayfaID  = "01965a3c-0000-7000-8000-000000000001"
	userID   = "01965a3c-0000-7000-8000-000000000002"
	inviteID = "01965a3c-0000-7000-8000-000000000003"
	annID    = "01965a3c-0000-7000-8000-000000000004"
	reqID    = "01965a3c-0000-7000-8000-0000000000aa"
)

// fakeVerifier accepts any token except BAD (invalid) and DOWN (key source
// unreachable).
type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, token string) (auth.Claims, error) {
	switch token {
	case "BAD":
		return auth.Claims{}, auth.ErrInvalidToken
	case "DOWN":
		return auth.Claims{}, errors.New("identity erişilemiyor")
	}
	return auth.Claims{Subject: userID}, nil
}

// fakeCore records the last request and the forwarded token, and answers
// every method with err or a fixed response.
type fakeCore struct {
	mu    sync.Mutex
	err   error
	calls int
	got   proto.Message
	token string
	tayfa *corev1.Tayfa
}

func (f *fakeCore) record(ctx context.Context, in proto.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.got, f.token = in, tokenFrom(ctx)
	return f.err
}

func (f *fakeCore) last() (proto.Message, string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got, f.token, f.calls
}

func testTayfa() *corev1.Tayfa {
	return &corev1.Tayfa{
		Id: tayfaID, Name: "Klasikçiler", Description: "Hafta sonu konvoyları",
		Interests:  []corev1.EventType{corev1.EventType_EVENT_TYPE_CONVOY, corev1.EventType_EVENT_TYPE_CAMP},
		JoinPolicy: corev1.JoinPolicy_JOIN_POLICY_APPROVAL,
		Modules: []*corev1.ModuleState{
			{Module: corev1.Module_MODULE_ANNOUNCEMENTS, Enabled: true},
			{Module: corev1.Module_MODULE_EVENTS, Enabled: true},
			{Module: corev1.Module_MODULE_QA, Enabled: false},
			{Module: corev1.Module_MODULE_POLLS, Enabled: true},
		},
		Status: corev1.TayfaStatus_TAYFA_STATUS_ACTIVE, MemberCount: 1, JoinCode: "K7M2P9QX",
		PendingJoinRequestCount: new(int32(3)),
		CreateTime:              timestamppb.New(t0), UpdateTime: timestamppb.New(t0),
	}
}

func testMembership() *corev1.Membership {
	return &corev1.Membership{
		TayfaId: tayfaID, UserId: userID, Role: corev1.Role_ROLE_MEMBER,
		Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE, JoinTime: timestamppb.New(t0),
	}
}

func (f *fakeCore) t() *corev1.Tayfa {
	if f.tayfa != nil {
		return f.tayfa
	}
	return testTayfa()
}

func (f *fakeCore) CreateTayfa(ctx context.Context, in *corev1.CreateTayfaRequest, _ ...grpc.CallOption) (*corev1.CreateTayfaResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &corev1.CreateTayfaResponse{Tayfa: f.t()}, nil
}
func (f *fakeCore) GetTayfa(ctx context.Context, in *corev1.GetTayfaRequest, _ ...grpc.CallOption) (*corev1.GetTayfaResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &corev1.GetTayfaResponse{Tayfa: f.t(), CallerRole: corev1.Role_ROLE_ADMIN}, nil
}
func (f *fakeCore) ListMyTayfas(ctx context.Context, in *corev1.ListMyTayfasRequest, _ ...grpc.CallOption) (*corev1.ListMyTayfasResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &corev1.ListMyTayfasResponse{
		Tayfas: []*corev1.MyTayfa{
			{TayfaId: tayfaID, Name: "Klasikçiler", Role: corev1.Role_ROLE_ADMIN, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE, Tayfa: f.t()},
			{TayfaId: inviteID, Name: "Kampçılar", Role: corev1.Role_ROLE_MEMBER, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING},
		},
		NextPageToken: "SONRAKI",
	}, nil
}
func (f *fakeCore) UpdateTayfa(ctx context.Context, in *corev1.UpdateTayfaRequest, _ ...grpc.CallOption) (*corev1.UpdateTayfaResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &corev1.UpdateTayfaResponse{Tayfa: f.t()}, nil
}
func (f *fakeCore) SetModule(ctx context.Context, in *corev1.SetModuleRequest, _ ...grpc.CallOption) (*corev1.SetModuleResponse, error) {
	return &corev1.SetModuleResponse{}, f.record(ctx, in)
}
func (f *fakeCore) RotateJoinCode(ctx context.Context, in *corev1.RotateJoinCodeRequest, _ ...grpc.CallOption) (*corev1.RotateJoinCodeResponse, error) {
	return &corev1.RotateJoinCodeResponse{JoinCode: "N3W4C0DE"}, f.record(ctx, in)
}
func (f *fakeCore) RequestDeletion(ctx context.Context, in *corev1.RequestDeletionRequest, _ ...grpc.CallOption) (*corev1.RequestDeletionResponse, error) {
	return &corev1.RequestDeletionResponse{DeletionTime: timestamppb.New(t0.Add(72 * time.Hour))}, f.record(ctx, in)
}
func (f *fakeCore) CancelDeletion(ctx context.Context, in *corev1.CancelDeletionRequest, _ ...grpc.CallOption) (*corev1.CancelDeletionResponse, error) {
	return &corev1.CancelDeletionResponse{}, f.record(ctx, in)
}

func (f *fakeCore) JoinByCode(ctx context.Context, in *corev1.JoinByCodeRequest, _ ...grpc.CallOption) (*corev1.JoinByCodeResponse, error) {
	return &corev1.JoinByCodeResponse{Membership: testMembership()}, f.record(ctx, in)
}
func (f *fakeCore) CreateInviteLink(ctx context.Context, in *corev1.CreateInviteLinkRequest, _ ...grpc.CallOption) (*corev1.CreateInviteLinkResponse, error) {
	return &corev1.CreateInviteLinkResponse{
		Invite: &corev1.InviteLink{InviteId: inviteID, TayfaId: tayfaID, CreatedByUserId: userID, CreateTime: timestamppb.New(t0), ExpireTime: timestamppb.New(t0.Add(in.GetTtl().AsDuration())), MaxUses: in.GetMaxUses()},
		Token:  "DAVETTOKENI",
	}, f.record(ctx, in)
}
func (f *fakeCore) ListInviteLinks(ctx context.Context, in *corev1.ListInviteLinksRequest, _ ...grpc.CallOption) (*corev1.ListInviteLinksResponse, error) {
	return &corev1.ListInviteLinksResponse{Invites: []*corev1.InviteLink{{InviteId: inviteID, TayfaId: tayfaID, MaxUses: 5, UseCount: 2}}}, f.record(ctx, in)
}
func (f *fakeCore) RevokeInviteLink(ctx context.Context, in *corev1.RevokeInviteLinkRequest, _ ...grpc.CallOption) (*corev1.RevokeInviteLinkResponse, error) {
	return &corev1.RevokeInviteLinkResponse{}, f.record(ctx, in)
}
func (f *fakeCore) PreviewInvite(ctx context.Context, in *corev1.PreviewInviteRequest, _ ...grpc.CallOption) (*corev1.PreviewInviteResponse, error) {
	return &corev1.PreviewInviteResponse{TayfaId: tayfaID, Name: "Klasikçiler", MemberCount: 12}, f.record(ctx, in)
}
func (f *fakeCore) JoinByInvite(ctx context.Context, in *corev1.JoinByInviteRequest, _ ...grpc.CallOption) (*corev1.JoinByInviteResponse, error) {
	return &corev1.JoinByInviteResponse{Membership: testMembership()}, f.record(ctx, in)
}
func (f *fakeCore) ListMembers(ctx context.Context, in *corev1.ListMembersRequest, _ ...grpc.CallOption) (*corev1.ListMembersResponse, error) {
	return &corev1.ListMembersResponse{Members: []*corev1.Membership{testMembership()}}, f.record(ctx, in)
}
func (f *fakeCore) ListPending(ctx context.Context, in *corev1.ListPendingRequest, _ ...grpc.CallOption) (*corev1.ListPendingResponse, error) {
	m := testMembership()
	m.Status, m.JoinTime = corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING, nil
	return &corev1.ListPendingResponse{Pending: []*corev1.PendingMember{{
		Membership: m, RequestTime: timestamppb.New(t0), Source: corev1.JoinSource_JOIN_SOURCE_INVITE_LINK,
		InviterUserId: tayfaID, InviteId: inviteID,
	}}}, f.record(ctx, in)
}
func (f *fakeCore) ApproveMember(ctx context.Context, in *corev1.ApproveMemberRequest, _ ...grpc.CallOption) (*corev1.ApproveMemberResponse, error) {
	return &corev1.ApproveMemberResponse{Membership: testMembership()}, f.record(ctx, in)
}
func (f *fakeCore) RejectMember(ctx context.Context, in *corev1.RejectMemberRequest, _ ...grpc.CallOption) (*corev1.RejectMemberResponse, error) {
	return &corev1.RejectMemberResponse{}, f.record(ctx, in)
}
func (f *fakeCore) ChangeRole(ctx context.Context, in *corev1.ChangeRoleRequest, _ ...grpc.CallOption) (*corev1.ChangeRoleResponse, error) {
	m := testMembership()
	m.Role = in.GetNewRole()
	return &corev1.ChangeRoleResponse{Membership: m}, f.record(ctx, in)
}
func (f *fakeCore) MuteMember(ctx context.Context, in *corev1.MuteMemberRequest, _ ...grpc.CallOption) (*corev1.MuteMemberResponse, error) {
	m := testMembership()
	m.MuteExpireTime = in.GetExpireTime()
	return &corev1.MuteMemberResponse{Membership: m}, f.record(ctx, in)
}
func (f *fakeCore) UnmuteMember(ctx context.Context, in *corev1.UnmuteMemberRequest, _ ...grpc.CallOption) (*corev1.UnmuteMemberResponse, error) {
	return &corev1.UnmuteMemberResponse{Membership: testMembership()}, f.record(ctx, in)
}
func (f *fakeCore) BanMember(ctx context.Context, in *corev1.BanMemberRequest, _ ...grpc.CallOption) (*corev1.BanMemberResponse, error) {
	return &corev1.BanMemberResponse{}, f.record(ctx, in)
}
func (f *fakeCore) RemoveMember(ctx context.Context, in *corev1.RemoveMemberRequest, _ ...grpc.CallOption) (*corev1.RemoveMemberResponse, error) {
	return &corev1.RemoveMemberResponse{}, f.record(ctx, in)
}
func (f *fakeCore) LeaveTayfa(ctx context.Context, in *corev1.LeaveTayfaRequest, _ ...grpc.CallOption) (*corev1.LeaveTayfaResponse, error) {
	return &corev1.LeaveTayfaResponse{}, f.record(ctx, in)
}

func testCoreDeps(f *fakeCore, burst int) CoreDeps {
	return CoreDeps{
		Tayfas: f, Members: f, Announcements: f, Events: f, QA: f, Polls: f, Verifier: fakeVerifier{},
		Social: f, Invitations: f, Profiles: f, Accounts: f, Reports: f, Media: f, Avatars: f, DataExports: f,
		Log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		TrustCFConnectingIP: true,
		CreateTayfaLimit:    ratelimit.New(60, burst),
		JoinCodeLimit:       ratelimit.New(60, burst), InviteLimit: ratelimit.New(60, burst),
		CheckInLimit: ratelimit.New(60, burst),
		Now:          func() time.Time { return t0 },
	}
}

func coreRouter(f *fakeCore) http.Handler { return coreRouterWith(testCoreDeps(f, 100)) }

func coreRouterWith(d CoreDeps) http.Handler {
	return NewRouter(Deps{Version: "test", Core: d})
}

// as sends a request as a signed-in mobile user.
func as(h http.Handler, method, path, body string, extra ...string) *httptest.ResponseRecorder {
	hdr := map[string]string{"Authorization": "Bearer USERTOKEN", clientHeader: "android"}
	for i := 0; i+1 < len(extra); i += 2 {
		hdr[extra[i]] = extra[i+1]
	}
	return do(h, method, path, body, hdr)
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("gövde çözülemedi: %v: %s", err, w.Body)
	}
	return v
}

func coreErr(code codes.Code, reason string) error {
	st, _ := status.New(code, "istek reddedildi").WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "core.tayfam.app"})
	return st.Err()
}

func TestCoreRoutesNeedVerifiedToken(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	for _, tc := range []struct {
		name string
		hdr  map[string]string
		code int
		typ  string
	}{
		{"token yok", map[string]string{}, 401, "unauthenticated"},
		{"geçersiz token", map[string]string{"Authorization": "Bearer BAD"}, 401, "unauthenticated"},
		{"anahtar alınamıyor", map[string]string{"Authorization": "Bearer DOWN"}, 503, "unavailable"},
		{"çok uzun token", map[string]string{"Authorization": "Bearer " + strings.Repeat("a", 5000)}, 401, "unauthenticated"},
	} {
		w := do(h, "GET", "/v1/tayfas/"+tayfaID, "", tc.hdr)
		if w.Code != tc.code || problemType(t, w) != tc.typ {
			t.Errorf("%s: %d %s", tc.name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatalf("doğrulanmamış istek core'a gitti (%d çağrı)", n)
	}
}

func TestCoreTokenForwardedUnchanged(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	if w := do(h, "GET", "/v1/tayfas/"+tayfaID, "", map[string]string{"Authorization": "Bearer MOBILTOKEN"}); w.Code != 200 {
		t.Fatalf("kod %d", w.Code)
	}
	if _, tok, _ := f.last(); tok != "MOBILTOKEN" {
		t.Fatalf("iletilen token %q", tok)
	}
	// The former web cookie is no credential (ADR-0017): 401, core is not
	// called.
	r := httptest.NewRequest(http.MethodGet, "/v1/tayfas/"+tayfaID, nil)
	r.AddCookie(&http.Cookie{Name: "tayfam_at", Value: "WEBTOKEN"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if _, tok, n := f.last(); rec.Code != 401 || problemType(t, rec) != "unauthenticated" || tok != "MOBILTOKEN" || n != 1 {
		t.Fatalf("çerezle: %d %q (%d çağrı)", rec.Code, tok, n)
	}
}

func TestCoreWritesNeedClientHeader(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	for _, rt := range []struct{ method, path string }{
		{"POST", "/v1/tayfas"},
		{"PATCH", "/v1/tayfas/" + tayfaID},
		{"DELETE", "/v1/tayfas/" + tayfaID + "/membership"},
		{"PUT", "/v1/tayfas/" + tayfaID + "/members/" + userID + "/ban"},
		{"POST", "/v1/tayfa-joins"},
		{"POST", "/v1/invite-previews"},
	} {
		w := do(h, rt.method, rt.path, `{}`, map[string]string{"Authorization": "Bearer T"})
		if w.Code != 400 || problemType(t, w) != "missing-client-header" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("başlıksız yazma isteği core'a gitti")
	}
}

func TestCreateTayfa(t *testing.T) {
	f := &fakeCore{}
	w := as(coreRouter(f), "POST", "/v1/tayfas",
		`{"name":"Klasikçiler","description":"Hafta sonu","interests":["convoy","camp"],"join_policy":"open","client_request_id":"`+reqID+`"}`)
	if w.Code != 201 {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	if w.Header().Get("Location") != "/v1/tayfas/"+tayfaID || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("başlıklar: %v", w.Header())
	}
	got, _, _ := f.last()
	want := &corev1.CreateTayfaRequest{
		Name: "Klasikçiler", Description: "Hafta sonu",
		Interests:  []corev1.EventType{corev1.EventType_EVENT_TYPE_CONVOY, corev1.EventType_EVENT_TYPE_CAMP},
		JoinPolicy: corev1.JoinPolicy_JOIN_POLICY_OPEN, ClientRequestId: reqID,
	}
	if !proto.Equal(got, want) {
		t.Fatalf("core'a giden = %v", got)
	}
	b := decodeBody[tayfaJSON](t, w)
	if b.ID != tayfaID || b.JoinPolicy != "approval" || !slices.Equal(b.Interests, []string{"convoy", "camp"}) ||
		b.Status != "active" || b.JoinCode != "K7M2P9QX" || !b.CreatedAt.Equal(t0) ||
		b.PendingJoinRequestCount == nil || *b.PendingJoinRequestCount != 3 {
		t.Fatalf("gövde = %+v", b)
	}
	if len(b.Modules) != 4 || !b.Modules["events"] || b.Modules["qa"] {
		t.Fatalf("modüller = %v", b.Modules)
	}
	if strings.Contains(w.Body.String(), "deletion_at") || strings.Contains(w.Body.String(), "suspended_at") {
		t.Fatal("boş zaman alanları yazıldı")
	}
}

func TestCreateTayfaBadBodies(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	for name, body := range map[string]string{
		"kısa ad":             `{"name":"  a ","client_request_id":"` + reqID + `"}`,
		"uzun ad":             `{"name":"` + strings.Repeat("ş", 51) + `","client_request_id":"` + reqID + `"}`,
		"uzun açıklama":       `{"name":"Tayfa","description":"` + strings.Repeat("a", 501) + `","client_request_id":"` + reqID + `"}`,
		"istek kimliği yok":   `{"name":"Tayfa"}`,
		"bozuk istek kimliği": `{"name":"Tayfa","client_request_id":"1"}`,
		"bilinmeyen ilgi":     `{"name":"Tayfa","interests":["golf"],"client_request_id":"` + reqID + `"}`,
		"tekrarlı ilgi":       `{"name":"Tayfa","interests":["camp","camp"],"client_request_id":"` + reqID + `"}`,
		"bilinmeyen politika": `{"name":"Tayfa","join_policy":"secret","client_request_id":"` + reqID + `"}`,
		"bilinmeyen alan":     `{"name":"Tayfa","client_request_id":"` + reqID + `","owner_id":"x"}`,
	} {
		if w := as(h, "POST", "/v1/tayfas", body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek core'a gitti")
	}
}

func TestCreateTayfaLimitedPerIP(t *testing.T) {
	f := &fakeCore{}
	d := testCoreDeps(f, 100)
	d.CreateTayfaLimit = ratelimit.Every(time.Hour, 1)
	h := coreRouterWith(d)
	body := func(id string) string { return `{"name":"Tayfa","client_request_id":"` + id + `"}` }
	other := "01965a3c-0000-7000-8000-0000000000bb"
	send := func(ip, id string) int { return as(h, "POST", "/v1/tayfas", body(id), "CF-Connecting-IP", ip).Code }

	// Bozuk istek saatlik hakkı harcamaz.
	if w := as(h, "POST", "/v1/tayfas", `{"name":"a","client_request_id":"`+reqID+`"}`, "CF-Connecting-IP", "203.0.113.7"); w.Code != 400 {
		t.Fatalf("bozuk istek: %d", w.Code)
	}
	if c := send("203.0.113.7", reqID); c != 201 {
		t.Fatalf("ilk açma: %d", c)
	}
	if c := send("203.0.113.7", other); c != 429 {
		t.Fatalf("aynı IP'den ikinci tayfa: %d", c)
	}
	// Aynı isteğin tekrarı (çevrimdışı yeniden gönderim) sınıra takılmaz;
	// core aynı tayfayı döndürür.
	if c := send("203.0.113.7", reqID); c != 201 {
		t.Fatalf("tekrar gönderim: %d", c)
	}
	if c := send("198.51.100.9", other); c != 201 {
		t.Fatalf("farklı IP etkilendi: %d", c)
	}
	if _, _, n := f.last(); n != 3 {
		t.Fatalf("core çağrısı %d", n)
	}
}

func TestCreateRetriesWindow(t *testing.T) {
	now := t0
	c := newCreateRetries(func() time.Time { return now })
	c.remember("ip", "a")
	if !c.isRetry("ip", "a") || c.isRetry("ip", "b") || c.isRetry("baska", "a") {
		t.Fatal("tekrar tanınmadı")
	}
	now = now.Add(30 * time.Minute)
	c.remember("ip", "a") // pencereyi uzatmaz
	now = now.Add(31 * time.Minute)
	if c.isRetry("ip", "a") {
		t.Fatal("pencere tekrarlarla uzadı")
	}
	c.maxKeys = 1
	c.remember("ip2", "x")
	if !c.isRetry("ip2", "x") {
		t.Fatal("süresi dolan kayıt temizlenmedi")
	}
}

func TestGetAndListTayfas(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "GET", "/v1/tayfas/"+tayfaID, "")
	v := decodeBody[tayfaViewJSON](t, w)
	if w.Code != 200 || v.CallerRole != "admin" || v.Tayfa.ID != tayfaID {
		t.Fatalf("getTayfa: %d %+v", w.Code, v)
	}
	if got, _, _ := f.last(); !proto.Equal(got, &corev1.GetTayfaRequest{TayfaId: tayfaID}) {
		t.Fatalf("core'a giden = %v", got)
	}

	w = as(h, "GET", "/v1/tayfas?page_size=20&page_token=OPAK%2Btoken", "")
	l := decodeBody[myTayfaListJSON](t, w)
	if w.Code != 200 || len(l.Tayfas) != 2 || l.NextPageToken != "SONRAKI" {
		t.Fatalf("liste: %d %+v", w.Code, l)
	}
	if l.Tayfas[0].Tayfa == nil || l.Tayfas[0].Status != "active" || l.Tayfas[1].Tayfa != nil || l.Tayfas[1].Status != "pending" {
		t.Fatalf("girdiler: %+v", l.Tayfas)
	}
	if got, _, _ := f.last(); !proto.Equal(got, &corev1.ListMyTayfasRequest{PageSize: 20, PageToken: "OPAK+token"}) {
		t.Fatalf("sayfalama iletilmedi: %v", got)
	}
}

func TestPagingAndPathValidation(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	for _, path := range []string{
		"/v1/tayfas?page_size=101",
		"/v1/tayfas?page_size=-1",
		"/v1/tayfas?page_size=on",
		"/v1/tayfas?page_token=" + strings.Repeat("a", 1025),
		"/v1/tayfas/yok",
		"/v1/tayfas/" + tayfaID + "x",
		"/v1/tayfas/" + tayfaID + "/members?status=pending",
		"/v1/tayfas/" + tayfaID + "/members/kim",
	} {
		method := "GET"
		if strings.Contains(path, "/members/") {
			method = "DELETE"
		}
		if w := as(h, method, path, ""); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", path, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek core'a gitti")
	}
}

func TestUpdateTayfaBuildsMaskFromPresentFields(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "PATCH", "/v1/tayfas/"+tayfaID, `{"description":"","join_policy":"open"}`)
	if w.Code != 200 {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	got, _, _ := f.last()
	req := got.(*corev1.UpdateTayfaRequest)
	if !slices.Equal(req.GetUpdateMask().GetPaths(), []string{"description", "join_policy"}) || req.GetJoinPolicy() != corev1.JoinPolicy_JOIN_POLICY_OPEN || req.GetTayfaId() != tayfaID {
		t.Fatalf("istek = %v", req)
	}
	w = as(h, "PATCH", "/v1/tayfas/"+tayfaID, `{"interests":[]}`)
	if got, _, _ := f.last(); w.Code != 200 || !slices.Equal(got.(*corev1.UpdateTayfaRequest).GetUpdateMask().GetPaths(), []string{"interests"}) {
		t.Fatalf("boş ilgi listesi: %d %v", w.Code, got)
	}
	for name, body := range map[string]string{
		"boş":                 `{}`,
		"yalnızca null":       `{"name":null}`,
		"bilinmeyen politika": `{"join_policy":"x"}`,
		"kısa ad":             `{"name":"ab"}`,
		"bilinmeyen alan":     `{"status":"archived"}`,
	} {
		if w := as(h, "PATCH", "/v1/tayfas/"+tayfaID, body); w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

func TestTayfaAdminActions(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "PUT", "/v1/tayfas/"+tayfaID+"/modules/qa", `{"enabled":false}`)
	if got, _, _ := f.last(); w.Code != 204 || !proto.Equal(got, &corev1.SetModuleRequest{TayfaId: tayfaID, Module: corev1.Module_MODULE_QA}) {
		t.Fatalf("modül: %d %v", w.Code, got)
	}
	if w := as(h, "PUT", "/v1/tayfas/"+tayfaID+"/modules/chat", `{"enabled":true}`); w.Code != 400 {
		t.Fatalf("bilinmeyen modül: %d", w.Code)
	}
	if w := as(h, "PUT", "/v1/tayfas/"+tayfaID+"/modules/qa", `{}`); w.Code != 400 {
		t.Fatalf("enabled yok: %d", w.Code)
	}

	w = as(h, "POST", "/v1/tayfas/"+tayfaID+"/join-code", "")
	if w.Code != 200 || decodeBody[map[string]string](t, w)["join_code"] != "N3W4C0DE" {
		t.Fatalf("kod yenileme: %d %s", w.Code, w.Body)
	}

	w = as(h, "POST", "/v1/tayfas/"+tayfaID+"/deletion", "")
	if w.Code != 202 || !decodeBody[map[string]time.Time](t, w)["deletion_at"].Equal(t0.Add(72*time.Hour)) {
		t.Fatalf("silme: %d %s", w.Code, w.Body)
	}
	w = as(h, "DELETE", "/v1/tayfas/"+tayfaID+"/deletion", "")
	if got, _, _ := f.last(); w.Code != 204 || !proto.Equal(got, &corev1.CancelDeletionRequest{TayfaId: tayfaID}) {
		t.Fatalf("silme iptali: %d %v", w.Code, got)
	}
}

func TestMemberManagement(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID
	until := t0.Add(24 * time.Hour)
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"DELETE", base + "/membership", "", 204, &corev1.LeaveTayfaRequest{TayfaId: tayfaID}},
		{"GET", base + "/members", "", 200, &corev1.ListMembersRequest{TayfaId: tayfaID, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE}},
		{"GET", base + "/members?status=banned&page_token=P", "", 200, &corev1.ListMembersRequest{TayfaId: tayfaID, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_BANNED, PageToken: "P"}},
		{"PATCH", base + "/members/" + userID, `{"role":"moderator"}`, 200, &corev1.ChangeRoleRequest{TayfaId: tayfaID, UserId: userID, NewRole: corev1.Role_ROLE_MODERATOR}},
		{"DELETE", base + "/members/" + userID, "", 204, &corev1.RemoveMemberRequest{TayfaId: tayfaID, UserId: userID}},
		{"PUT", base + "/members/" + userID + "/mute", `{"muted_until":"` + until.Format(time.RFC3339) + `"}`, 200, &corev1.MuteMemberRequest{TayfaId: tayfaID, UserId: userID, ExpireTime: timestamppb.New(until)}},
		{"DELETE", base + "/members/" + userID + "/mute", "", 200, &corev1.UnmuteMemberRequest{TayfaId: tayfaID, UserId: userID}},
		{"PUT", base + "/members/" + userID + "/ban", "", 204, &corev1.BanMemberRequest{TayfaId: tayfaID, UserId: userID}},
		{"GET", base + "/join-requests?page_size=10", "", 200, &corev1.ListPendingRequest{TayfaId: tayfaID, PageSize: 10}},
		{"POST", base + "/join-requests/" + userID + "/approval", "", 200, &corev1.ApproveMemberRequest{TayfaId: tayfaID, UserId: userID}},
		{"DELETE", base + "/join-requests/" + userID, "", 204, &corev1.RejectMemberRequest{TayfaId: tayfaID, UserId: userID}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, _, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) {
			t.Errorf("%s %s: %d %s; core'a giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}

	w := as(h, "PUT", base+"/members/"+userID+"/mute", `{"muted_until":"`+until.Format(time.RFC3339)+`"}`)
	if m := decodeBody[membershipJSON](t, w); m.MutedUntil == nil || !m.MutedUntil.Equal(until) || m.Role != "member" {
		t.Fatalf("susturma yanıtı: %+v", m)
	}
	w = as(h, "GET", base+"/join-requests", "")
	jr := decodeBody[joinRequestListJSON](t, w)
	if len(jr.JoinRequests) != 1 || jr.JoinRequests[0].Source != "invite_link" || jr.JoinRequests[0].UserID != userID || jr.JoinRequests[0].InviteID != inviteID {
		t.Fatalf("istekler: %+v", jr)
	}
	for name, tc := range map[string]struct{ method, path, body string }{
		"bilinmeyen rol":    {"PATCH", base + "/members/" + userID, `{"role":"owner"}`},
		"susturma zamansız": {"PUT", base + "/members/" + userID + "/mute", `{}`},
		"bozuk zaman":       {"PUT", base + "/members/" + userID + "/mute", `{"muted_until":"yarın"}`},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

func TestInvites(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/invites"
	w := as(h, "POST", base, `{"ttl_seconds":86400,"max_uses":5}`)
	if w.Code != 201 {
		t.Fatalf("davet: %d %s", w.Code, w.Body)
	}
	got, _, _ := f.last()
	if req := got.(*corev1.CreateInviteLinkRequest); req.GetTtl().AsDuration() != 24*time.Hour || req.GetMaxUses() != 5 || req.GetTayfaId() != tayfaID {
		t.Fatalf("istek = %v", req)
	}
	ci := decodeBody[createdInviteJSON](t, w)
	if ci.Token != "DAVETTOKENI" || ci.Invite.ID != inviteID || !ci.Invite.ExpiresAt.Equal(t0.Add(24*time.Hour)) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("yanıt = %+v", ci)
	}
	for _, body := range []string{`{"ttl_seconds":60,"max_uses":5}`, `{"ttl_seconds":2592001,"max_uses":5}`, `{"ttl_seconds":3600,"max_uses":0}`, `{"ttl_seconds":3600,"max_uses":101}`} {
		if w := as(h, "POST", base, body); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}

	w = as(h, "GET", base, "")
	if l := decodeBody[inviteListJSON](t, w); w.Code != 200 || len(l.Invites) != 1 || l.Invites[0].UseCount != 2 || strings.Contains(w.Body.String(), "token") {
		t.Fatalf("liste: %d %s", w.Code, w.Body)
	}
	w = as(h, "DELETE", base+"/"+inviteID, "")
	if got, _, _ := f.last(); w.Code != 204 || !proto.Equal(got, &corev1.RevokeInviteLinkRequest{TayfaId: tayfaID, InviteId: inviteID}) {
		t.Fatalf("iptal: %d %v", w.Code, got)
	}
}

func TestJoiningCarriesSecretsInBody(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "POST", "/v1/tayfa-joins", `{"code":"k7m2p9qx"}`)
	if got, _, _ := f.last(); w.Code != 201 || !proto.Equal(got, &corev1.JoinByCodeRequest{Code: "k7m2p9qx"}) {
		t.Fatalf("kodla katılım: %d %v", w.Code, got)
	}
	if m := decodeBody[membershipJSON](t, w); m.Status != "active" || m.TayfaID != tayfaID {
		t.Fatalf("üyelik: %+v", m)
	}
	w = as(h, "POST", "/v1/invite-previews", `{"token":"GIZLI"}`)
	if got, _, _ := f.last(); w.Code != 200 || !proto.Equal(got, &corev1.PreviewInviteRequest{Token: "GIZLI"}) {
		t.Fatalf("önizleme: %d %v", w.Code, got)
	}
	if p := decodeBody[invitePreviewJSON](t, w); p.Name != "Klasikçiler" || p.MemberCount != 12 || p.AlreadyMember {
		t.Fatalf("önizleme yanıtı: %+v", p)
	}
	w = as(h, "POST", "/v1/invite-joins", `{"token":"GIZLI"}`)
	if got, _, _ := f.last(); w.Code != 201 || !proto.Equal(got, &corev1.JoinByInviteRequest{Token: "GIZLI"}) {
		t.Fatalf("davetle katılım: %d %v", w.Code, got)
	}
	for _, tc := range []struct{ path, body string }{
		{"/v1/tayfa-joins", `{"code":""}`},
		{"/v1/tayfa-joins", `{"code":"` + strings.Repeat("A", 33) + `"}`},
		{"/v1/invite-joins", `{"token":""}`},
		{"/v1/invite-previews", `{"token":"` + strings.Repeat("A", 65) + `"}`},
	} {
		if w := as(h, "POST", tc.path, tc.body); w.Code != 400 {
			t.Errorf("%s %s: %d", tc.path, tc.body, w.Code)
		}
	}
	// URL'de token taşıyan bir uç yoktur.
	if w := as(h, "GET", "/v1/invites/GIZLI", ""); w.Code != 404 {
		t.Fatalf("token'lı yol: %d", w.Code)
	}
}

func TestJoinLimitsPerIP(t *testing.T) {
	f := &fakeCore{}
	h := coreRouterWith(testCoreDeps(f, 2))
	for _, path := range []string{"/v1/tayfa-joins", "/v1/invite-joins"} {
		body := `{"code":"K7M2P9QX"}`
		if path == "/v1/invite-joins" {
			body = `{"token":"GIZLI"}`
		}
		for range 2 {
			if w := as(h, "POST", path, body, "CF-Connecting-IP", "203.0.113.7"); w.Code != 201 {
				t.Fatalf("%s: %d", path, w.Code)
			}
		}
		// Bozuk gövde de hakkı harcar (tahmin denemesi sayılır).
		if w := as(h, "POST", path, `{`, "CF-Connecting-IP", "203.0.113.7"); w.Code != 429 {
			t.Fatalf("%s sınır aşıldı ama kod %d", path, w.Code)
		}
		if w := as(h, "POST", path, body, "CF-Connecting-IP", "198.51.100.9"); w.Code != 201 {
			t.Fatalf("%s: farklı IP etkilendi", path)
		}
	}
	// Önizleme ve davetle katılım aynı kovayı paylaşır.
	if w := as(h, "POST", "/v1/invite-previews", `{"token":"GIZLI"}`, "CF-Connecting-IP", "203.0.113.7"); w.Code != 429 {
		t.Fatalf("önizleme: %d", w.Code)
	}
}

func TestCoreErrorsBecomeProblems(t *testing.T) {
	cases := []struct {
		err  error
		code int
		typ  string
	}{
		{status.Error(codes.Unauthenticated, "x"), 401, "unauthenticated"},
		{status.Error(codes.Unavailable, "x"), 503, "unavailable"},
		{status.Error(codes.Internal, "SELECT * FROM memberships iç ayrıntı"), 500, "internal"},
		// identity'nin alan adıyla gelen ya da yabancı ErrorInfo'ya güvenilmez.
		{func() error {
			st, _ := status.New(codes.NotFound, "x").WithDetails(&errdetails.ErrorInfo{Reason: "ERROR_REASON_TAYFA_NOT_FOUND", Domain: "identity.tayfam.app"})
			return st.Err()
		}(), 500, "internal"},
		{coreErr(codes.FailedPrecondition, "ERROR_REASON_SOMETHING_NEW"), 500, "internal"},
	}
	for reason, p := range problemsByDomain["core.tayfam.app"] {
		cases = append(cases, struct {
			err  error
			code int
			typ  string
		}{coreErr(codes.Unknown, reason), p.status, p.code})
	}
	for _, tc := range cases {
		w := as(coreRouter(&fakeCore{err: tc.err}), "GET", "/v1/tayfas/"+tayfaID, "")
		if w.Code != tc.code || problemType(t, w) != tc.typ {
			t.Errorf("%v: %d %s", tc.err, w.Code, problemType(t, w))
		}
		if strings.Contains(w.Body.String(), "SELECT") || strings.Contains(w.Body.String(), "reddedildi") {
			t.Error("iç ayrıntı istemciye sızdı")
		}
	}
	// errors.proto'daki her sebep eşlenmiş olmalı.
	for v, name := range corev1.ErrorReason_name {
		if v == 0 {
			continue
		}
		if _, ok := problemsByDomain["core.tayfam.app"][name]; !ok {
			t.Errorf("%s problem type'a eşlenmemiş", name)
		}
	}
}

func TestUnknownEnumValuesFromNewerCore(t *testing.T) {
	tf := testTayfa()
	tf.Status = corev1.TayfaStatus(99)
	tf.Interests = append(tf.Interests, corev1.EventType(42))
	tf.Modules = append(tf.Modules, &corev1.ModuleState{Module: corev1.Module(7), Enabled: true})
	w := as(coreRouter(&fakeCore{tayfa: tf}), "GET", "/v1/tayfas/"+tayfaID, "")
	v := decodeBody[tayfaViewJSON](t, w)
	if v.Tayfa.Status != "unknown" || v.Tayfa.Interests[2] != "unknown" || len(v.Tayfa.Modules) != 4 {
		t.Fatalf("bilinmeyen değerler: %+v", v.Tayfa)
	}
}
