package rest

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const materialService = "tayfam.core.v1.TayfaMaterialService"

// fakeMaterials is core's TayfaMaterialService for the material tests.
type fakeMaterials struct {
	got   proto.Message
	token string
	err   error
	calls int
}

func (f *fakeMaterials) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func materialPB() *corev1.Material {
	return &corev1.Material{
		Id: itemID, Kind: corev1.MaterialKind_MATERIAL_KIND_LINK, Title: "Türev özeti",
		Url: "https://tr.wikipedia.org/wiki/T%C3%BCrev", Domain: "tr.wikipedia.org", SubjectKey: "math",
		AuthorId: userID, CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now(), CallerCanEdit: true, CallerCanDelete: true,
	}
}

func (f *fakeMaterials) ListMaterials(ctx context.Context, in *corev1.ListMaterialsRequest, _ ...grpc.CallOption) (*corev1.ListMaterialsResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListMaterialsResponse{Materials: []*corev1.Material{materialPB()}, NextPageToken: "sonraki", CallerCanAdd: true}, f.err
}

func (f *fakeMaterials) GetMaterial(ctx context.Context, in *corev1.GetMaterialRequest, _ ...grpc.CallOption) (*corev1.GetMaterialResponse, error) {
	f.rec(ctx, in)
	return &corev1.GetMaterialResponse{Material: materialPB()}, f.err
}

func (f *fakeMaterials) CreateMaterial(ctx context.Context, in *corev1.CreateMaterialRequest, _ ...grpc.CallOption) (*corev1.CreateMaterialResponse, error) {
	f.rec(ctx, in)
	return &corev1.CreateMaterialResponse{Material: materialPB()}, f.err
}

func (f *fakeMaterials) UpdateMaterial(ctx context.Context, in *corev1.UpdateMaterialRequest, _ ...grpc.CallOption) (*corev1.UpdateMaterialResponse, error) {
	f.rec(ctx, in)
	return &corev1.UpdateMaterialResponse{Material: materialPB()}, f.err
}

func (f *fakeMaterials) DeleteMaterial(ctx context.Context, in *corev1.DeleteMaterialRequest, _ ...grpc.CallOption) (*corev1.DeleteMaterialResponse, error) {
	f.rec(ctx, in)
	return &corev1.DeleteMaterialResponse{}, f.err
}

const (
	materialsURL = "/v1/tayfas/" + tayfaID + "/materials"
	materialURL  = materialsURL + "/" + itemID
)

func TestMaterialRoutes(t *testing.T) {
	f := &fakeMaterials{}
	h := componentRouter(fakeServices{materialService: f})
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", materialsURL, "", http.StatusOK, &corev1.ListMaterialsRequest{TayfaId: tayfaID}},
		{"GET", materialsURL + "?kind=note_photo&subject_key=math&topic_key=math_limit&mine=true&page_size=10&page_token=abc", "", http.StatusOK,
			&corev1.ListMaterialsRequest{TayfaId: tayfaID, Kind: corev1.MaterialKind_MATERIAL_KIND_NOTE_PHOTO, SubjectKey: "math",
				TopicKey: "math_limit", Mine: true, PageSize: 10, PageToken: "abc"}},
		{"POST", materialsURL, `{"kind":"link","title":"Özet","url":"https://example.com/a","subject_key":"math","client_request_id":"` + itemID + `"}`,
			http.StatusCreated, &corev1.CreateMaterialRequest{TayfaId: tayfaID, Kind: corev1.MaterialKind_MATERIAL_KIND_LINK, Title: "Özet",
				Url: "https://example.com/a", SubjectKey: "math", ClientRequestId: itemID}},
		{"POST", materialsURL, `{"kind":"note_photo","title":"Defter","photo_upload_ids":["` + entryID + `"],"subject_key":"math","topic_key":"math_limit","client_request_id":"` + itemID + `"}`,
			http.StatusCreated, &corev1.CreateMaterialRequest{TayfaId: tayfaID, Kind: corev1.MaterialKind_MATERIAL_KIND_NOTE_PHOTO, Title: "Defter",
				PhotoUploadIds: []string{entryID}, SubjectKey: "math", TopicKey: "math_limit", ClientRequestId: itemID}},
		{"GET", materialURL, "", http.StatusOK, &corev1.GetMaterialRequest{TayfaId: tayfaID, MaterialId: itemID}},
		{"PUT", materialURL, `{"title":"Yeni","url":"https://example.com/b","subject_key":"math"}`, http.StatusOK,
			&corev1.UpdateMaterialRequest{TayfaId: tayfaID, MaterialId: itemID, Title: "Yeni", Url: "https://example.com/b", SubjectKey: "math"}},
		{"DELETE", materialURL, "", http.StatusNoContent, &corev1.DeleteMaterialRequest{TayfaId: tayfaID, MaterialId: itemID}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s %s: core'a giden %v (%q), beklenen %v", tc.method, tc.path, f.got, f.token, tc.want)
		}
	}

	m := decodeBody[materialJSON](t, as(h, "GET", materialURL, ""))
	if m.Kind != "link" || m.Domain != "tr.wikipedia.org" || m.URL == "" || m.TopicKey != "" || m.PhotoUploadIDs != nil ||
		!m.CallerCanEdit || !m.CallerCanDelete || m.AuthorUserID != userID {
		t.Fatalf("materyal: %+v", m)
	}
	l := decodeBody[materialListJSON](t, as(h, "GET", materialsURL, ""))
	if len(l.Materials) != 1 || l.NextPageToken != "sonraki" || !l.CallerCanAdd {
		t.Fatalf("liste: %+v", l)
	}
}

func TestMaterialRoutesValidateInput(t *testing.T) {
	f := &fakeMaterials{}
	h := componentRouter(fakeServices{materialService: f})
	crid := `,"client_request_id":"` + itemID + `"`
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/v1/tayfas/x/materials", ""},
		{"GET", materialsURL + "?kind=pdf", ""},
		{"GET", materialsURL + "?mine=evet", ""},
		{"GET", materialsURL + "?subject_key=Matematik", ""},
		{"GET", materialsURL + "?page_size=101", ""},
		{"POST", materialsURL, `{"kind":"link","title":"x","url":"https://a.com","subject_key":"math"}`},
		{"POST", materialsURL, `{"kind":"pdf","title":"x","subject_key":"math"` + crid + `}`},
		{"POST", materialsURL, `{"kind":"link","title":"  ","url":"https://a.com","subject_key":"math"` + crid + `}`},
		{"POST", materialsURL, `{"kind":"link","title":"x","url":"https://a.com"` + crid + `}`},
		{"POST", materialsURL, `{"kind":"link","title":"x","url":"https://a.com","subject_key":"math","topic_key":"Konu!"` + crid + `}`},
		{"POST", materialsURL, `{"kind":"link","title":"x","url":"https://a.com/` + strings.Repeat("a", 2048) + `","subject_key":"math"` + crid + `}`},
		{"POST", materialsURL, `{"kind":"note_photo","title":"x","photo_upload_ids":["x"],"subject_key":"math"` + crid + `}`},
		{"POST", materialsURL, `{"kind":"note_photo","title":"x","photo_upload_ids":["` + entryID + `","` + entryID + `","` + entryID + `","` + entryID + `"],"subject_key":"math"` + crid + `}`},
		{"POST", materialsURL, `{"kind":"link","title":"` + strings.Repeat("a", 1025) + `","url":"https://a.com","subject_key":"math"` + crid + `}`},
		{"POST", materialsURL, `{"kind":"link","title":"x","url":"https://a.com","subject_key":"math","file":"x.pdf"` + crid + `}`},
		{"PUT", materialsURL + "/x", `{"title":"x","subject_key":"math"}`},
		{"PUT", materialURL, `{"title":"x","subject_key":"math","kind":"link"}`},
		{"DELETE", materialsURL + "/x", ""},
	} {
		if w := as(h, rt.method, rt.path, rt.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	for _, rt := range []struct{ method, path string }{
		{"GET", materialsURL}, {"POST", materialsURL}, {"PUT", materialURL}, {"DELETE", materialURL},
	} {
		if w := do(h, rt.method, rt.path, `{}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", rt.method, rt.path, w.Code)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestMaterialErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_MATERIAL_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "material-not-found"},
		"ERROR_REASON_BLOCKED_LINK":       {codes.InvalidArgument, http.StatusBadRequest, "blocked-link"},
		"ERROR_REASON_MATERIAL_LIMIT":     {codes.FailedPrecondition, http.StatusConflict, "material-limit"},
		"ERROR_REASON_MODULE_DISABLED":    {codes.FailedPrecondition, http.StatusConflict, "module-disabled"},
	} {
		h := componentRouter(fakeServices{materialService: &fakeMaterials{err: coreErr(want.grpc, reason)}})
		body := `{"kind":"link","title":"x","url":"https://a.com","subject_key":"math","client_request_id":"` + itemID + `"}`
		if w := as(h, "POST", materialsURL, body); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
