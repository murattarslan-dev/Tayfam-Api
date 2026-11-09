package rest

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

func testAnnouncement() *corev1.Announcement {
	return &corev1.Announcement{
		Id: annID, TayfaId: tayfaID, AuthorUserId: userID, Title: "Pazar konvoyu", Body: "Saat 9'da",
		Pinned: true, CreateTime: timestamppb.New(t0), UpdateTime: timestamppb.New(t0),
	}
}

func (f *fakeCore) CreateAnnouncement(ctx context.Context, in *corev1.CreateAnnouncementRequest, _ ...grpc.CallOption) (*corev1.CreateAnnouncementResponse, error) {
	return &corev1.CreateAnnouncementResponse{Announcement: testAnnouncement()}, f.record(ctx, in)
}
func (f *fakeCore) GetAnnouncement(ctx context.Context, in *corev1.GetAnnouncementRequest, _ ...grpc.CallOption) (*corev1.GetAnnouncementResponse, error) {
	return &corev1.GetAnnouncementResponse{Announcement: testAnnouncement()}, f.record(ctx, in)
}
func (f *fakeCore) ListAnnouncements(ctx context.Context, in *corev1.ListAnnouncementsRequest, _ ...grpc.CallOption) (*corev1.ListAnnouncementsResponse, error) {
	orphan := testAnnouncement()
	orphan.AuthorUserId, orphan.Pinned = "", false
	return &corev1.ListAnnouncementsResponse{Announcements: []*corev1.Announcement{testAnnouncement(), orphan}, NextPageToken: "SONRAKI"}, f.record(ctx, in)
}
func (f *fakeCore) UpdateAnnouncement(ctx context.Context, in *corev1.UpdateAnnouncementRequest, _ ...grpc.CallOption) (*corev1.UpdateAnnouncementResponse, error) {
	return &corev1.UpdateAnnouncementResponse{Announcement: testAnnouncement()}, f.record(ctx, in)
}
func (f *fakeCore) SetAnnouncementPinned(ctx context.Context, in *corev1.SetAnnouncementPinnedRequest, _ ...grpc.CallOption) (*corev1.SetAnnouncementPinnedResponse, error) {
	a := testAnnouncement()
	a.Pinned = in.GetPinned()
	return &corev1.SetAnnouncementPinnedResponse{Announcement: a}, f.record(ctx, in)
}
func (f *fakeCore) DeleteAnnouncement(ctx context.Context, in *corev1.DeleteAnnouncementRequest, _ ...grpc.CallOption) (*corev1.DeleteAnnouncementResponse, error) {
	return &corev1.DeleteAnnouncementResponse{}, f.record(ctx, in)
}
func (f *fakeCore) RestoreAnnouncement(ctx context.Context, in *corev1.RestoreAnnouncementRequest, _ ...grpc.CallOption) (*corev1.RestoreAnnouncementResponse, error) {
	return &corev1.RestoreAnnouncementResponse{Announcement: testAnnouncement()}, f.record(ctx, in)
}

func TestAnnouncementRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/announcements"
	one := base + "/" + annID
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", base + "?page_size=10&page_token=P", "", 200, &corev1.ListAnnouncementsRequest{TayfaId: tayfaID, PageSize: 10, PageToken: "P"}},
		{"POST", base, `{"title":"Pazar konvoyu","body":"Saat 9'da","client_request_id":"` + reqID + `"}`, 201,
			&corev1.CreateAnnouncementRequest{TayfaId: tayfaID, Title: "Pazar konvoyu", Body: "Saat 9'da", ClientRequestId: reqID}},
		{"GET", one, "", 200, &corev1.GetAnnouncementRequest{TayfaId: tayfaID, AnnouncementId: annID}},
		{"DELETE", one, "", 204, &corev1.DeleteAnnouncementRequest{TayfaId: tayfaID, AnnouncementId: annID}},
		{"PUT", one + "/pin", "", 200, &corev1.SetAnnouncementPinnedRequest{TayfaId: tayfaID, AnnouncementId: annID, Pinned: true}},
		{"DELETE", one + "/pin", "", 200, &corev1.SetAnnouncementPinnedRequest{TayfaId: tayfaID, AnnouncementId: annID}},
		{"POST", one + "/restoration", "", 200, &corev1.RestoreAnnouncementRequest{TayfaId: tayfaID, AnnouncementId: annID}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, tok, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) || tok != "USERTOKEN" {
			t.Errorf("%s %s: %d %s; core'a giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}

	w := as(h, "POST", base, `{"title":"T","client_request_id":"`+reqID+`"}`)
	a := decodeBody[announcementJSON](t, w)
	if w.Header().Get("Location") != one || a.ID != annID || a.AuthorUserID != userID || !a.Pinned || !a.CreatedAt.Equal(t0) {
		t.Fatalf("oluşturma yanıtı: %v %+v", w.Header(), a)
	}
	w = as(h, "GET", base, "")
	l := decodeBody[announcementListJSON](t, w)
	if len(l.Announcements) != 2 || l.NextPageToken != "SONRAKI" || l.Announcements[1].AuthorUserID != "" {
		t.Fatalf("liste: %+v", l)
	}
	if strings.Count(w.Body.String(), "author_user_id") != 1 {
		t.Fatalf("silinmiş yazar alanı yazıldı: %s", w.Body)
	}
	w = as(h, "DELETE", one+"/pin", "")
	if decodeBody[announcementJSON](t, w).Pinned {
		t.Fatal("sabitleme kaldırılmadı")
	}
}

func TestUpdateAnnouncementMask(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	one := "/v1/tayfas/" + tayfaID + "/announcements/" + annID
	w := as(h, "PATCH", one, `{"body":""}`)
	got, _, _ := f.last()
	if req := got.(*corev1.UpdateAnnouncementRequest); w.Code != 200 || !slices.Equal(req.GetUpdateMask().GetPaths(), []string{"body"}) || req.GetAnnouncementId() != annID {
		t.Fatalf("boş gövde: %d %v", w.Code, got)
	}
	w = as(h, "PATCH", one, `{"title":"Yeni","body":"B"}`)
	got, _, _ = f.last()
	if req := got.(*corev1.UpdateAnnouncementRequest); w.Code != 200 || !slices.Equal(req.GetUpdateMask().GetPaths(), []string{"title", "body"}) || req.GetTitle() != "Yeni" {
		t.Fatalf("iki alan: %d %v", w.Code, got)
	}
}

func TestAnnouncementBadRequests(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/announcements"
	one := base + "/" + annID
	long := strings.Repeat("ş", 5001)
	for name, tc := range map[string]struct{ method, path, body string }{
		"boş başlık":        {"POST", base, `{"title":"  ","client_request_id":"` + reqID + `"}`},
		"uzun başlık":       {"POST", base, `{"title":"` + strings.Repeat("ş", 121) + `","client_request_id":"` + reqID + `"}`},
		"uzun gövde":        {"POST", base, `{"title":"T","body":"` + long + `","client_request_id":"` + reqID + `"}`},
		"istek kimliği yok": {"POST", base, `{"title":"T"}`},
		"bilinmeyen alan":   {"POST", base, `{"title":"T","client_request_id":"` + reqID + `","pinned":true}`},
		"yazar verilemez":   {"POST", base, `{"title":"T","client_request_id":"` + reqID + `","author_id":"` + userID + `"}`},
		"boş düzenleme":     {"PATCH", one, `{}`},
		"null düzenleme":    {"PATCH", one, `{"title":null}`},
		"boş başlıkla":      {"PATCH", one, `{"title":""}`},
		"bozuk duyuru id":   {"GET", base + "/kim", ""},
		"bozuk tayfa id":    {"GET", "/v1/tayfas/yok/announcements", ""},
		"bozuk sayfa":       {"GET", base + "?page_size=101", ""},
		"bozuk geri alma":   {"POST", base + "/x/restoration", ""},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek core'a gitti")
	}
	// The longest valid body (5000 four-byte characters) fits the limit.
	emoji := strings.Repeat("\U0001F697", 5000)
	if w := as(h, "POST", base, `{"title":"T","body":"`+emoji+`","client_request_id":"`+reqID+`"}`); w.Code != 201 {
		t.Fatalf("en uzun gövde: %d %s", w.Code, w.Body)
	}
}

func TestAnnouncementWritesNeedClientHeader(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	one := "/v1/tayfas/" + tayfaID + "/announcements/" + annID
	for _, rt := range []struct{ method, path string }{
		{"POST", "/v1/tayfas/" + tayfaID + "/announcements"},
		{"PATCH", one}, {"DELETE", one}, {"PUT", one + "/pin"}, {"DELETE", one + "/pin"}, {"POST", one + "/restoration"},
	} {
		w := do(h, rt.method, rt.path, `{}`, map[string]string{"Authorization": "Bearer T"})
		if w.Code != 400 || problemType(t, w) != "missing-client-header" {
			t.Errorf("%s %s: %d", rt.method, rt.path, w.Code)
		}
	}
	if w := do(h, "GET", one, "", map[string]string{}); w.Code != http.StatusUnauthorized {
		t.Fatalf("token'sız okuma: %d", w.Code)
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("istek core'a gitti")
	}
}

func TestAnnouncementProblems(t *testing.T) {
	for reason, want := range map[string]struct {
		code int
		typ  string
	}{
		"ERROR_REASON_MODULE_DISABLED":        {409, "module-disabled"},
		"ERROR_REASON_ANNOUNCEMENT_NOT_FOUND": {404, "announcement-not-found"},
		"ERROR_REASON_ROLE_HIERARCHY":         {403, "role-hierarchy"},
	} {
		h := coreRouter(&fakeCore{err: coreErr(codes.Unknown, reason)})
		w := as(h, "GET", "/v1/tayfas/"+tayfaID+"/announcements/"+annID, "")
		if w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, problemType(t, w))
		}
	}
}

// Without an AnnouncementClient the routes are not served.
func TestAnnouncementRoutesOptional(t *testing.T) {
	d := testCoreDeps(&fakeCore{}, 100)
	d.Announcements = nil
	if w := as(coreRouterWith(d), "GET", "/v1/tayfas/"+tayfaID+"/announcements", ""); w.Code != 404 {
		t.Fatalf("kod %d", w.Code)
	}
}
