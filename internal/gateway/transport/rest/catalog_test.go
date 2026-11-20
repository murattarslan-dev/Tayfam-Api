package rest

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// --- fakeCore: CatalogService (issue #237) ---

const testCatalogVersion = 7

func testCatalogType() *corev1.CatalogEventType {
	return &corev1.CatalogEventType{
		Key: "hike", InterestKeys: []string{"nature"}, Labels: map[string]string{"tr": "Doğa yürüyüşü"}, Icon: "hiking",
		Modes:  []corev1.EventMode{corev1.EventMode_EVENT_MODE_IN_PERSON, corev1.EventMode_EVENT_MODE_ONLINE},
		Status: corev1.CatalogStatus_CATALOG_STATUS_ACTIVE, Version: 2,
		Definition: `{"components":[{"kind":"roll_call","params":{"alarm":true}}]}`,
	}
}

func (f *fakeCore) GetCatalog(ctx context.Context, in *corev1.GetCatalogRequest, _ ...grpc.CallOption) (*corev1.GetCatalogResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	if in.GetKnownVersion() == testCatalogVersion {
		return &corev1.GetCatalogResponse{CatalogVersion: testCatalogVersion, NotModified: true}, nil
	}
	t := testCatalogType()
	if f.catalogDefinition != "" {
		t.Definition = f.catalogDefinition
	}
	return &corev1.GetCatalogResponse{
		CatalogVersion: testCatalogVersion,
		Interests: []*corev1.CatalogInterest{{
			Key: "nature", Position: 20, Labels: map[string]string{"tr": "Doğa"}, Icon: "forest",
			Status: corev1.CatalogStatus_CATALOG_STATUS_ACTIVE,
		}},
		EventTypes: []*corev1.CatalogEventType{t},
		StudyTags: []*corev1.CatalogStudyTag{
			{Key: "level_a", Position: 1, Labels: map[string]string{"tr": "Düzey A"}, Status: corev1.CatalogStatus_CATALOG_STATUS_ACTIVE},
			{Key: "subject_a", ParentKey: "level_a", Position: 2, Labels: map[string]string{"tr": "Ders A"}, Status: corev1.CatalogStatus_CATALOG_STATUS_RETIRED},
		},
	}, nil
}

func (f *fakeCore) GetEventTypeVersion(ctx context.Context, in *corev1.GetEventTypeVersionRequest, _ ...grpc.CallOption) (*corev1.GetEventTypeVersionResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	t := testCatalogType()
	t.Version, t.Status = in.GetVersion(), corev1.CatalogStatus_CATALOG_STATUS_RETIRED
	if f.catalogDefinition != "" {
		t.Definition = f.catalogDefinition
	}
	return &corev1.GetEventTypeVersionResponse{EventType: t}, nil
}

func TestCatalogRoute(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)

	w := as(h, "GET", "/v1/catalog", "")
	if w.Code != http.StatusOK {
		t.Fatalf("katalog: %d %s", w.Code, w.Body)
	}
	got, tok, _ := f.last()
	if got.(*corev1.GetCatalogRequest).GetKnownVersion() != 0 || tok != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", got, tok)
	}
	if etag := w.Header().Get("ETag"); etag != `"7"` {
		t.Fatalf("ETag = %q", etag)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	v := decodeBody[catalogJSON](t, w)
	if v.CatalogVersion != 7 || len(v.Interests) != 1 || len(v.EventTypes) != 1 {
		t.Fatalf("gövde: %+v", v)
	}
	in := v.Interests[0]
	if in.Key != "nature" || in.Position != 20 || in.Labels["tr"] != "Doğa" || in.Icon != "forest" || in.Status != "active" || in.DefaultWidgets == nil {
		t.Fatalf("ilgi alanı: %+v", in)
	}
	et := v.EventTypes[0]
	if et.Key != "hike" || !slices.Equal(et.InterestKeys, []string{"nature"}) || et.Labels["tr"] != "Doğa yürüyüşü" || et.Icon != "hiking" ||
		!slices.Equal(et.Modes, []string{"in_person", "online"}) || et.Status != "active" || et.Version != 2 {
		t.Fatalf("tür: %+v", et)
	}
	// The definition is an object in the body, not a string.
	if !strings.Contains(w.Body.String(), `"definition":{"components":[{"kind":"roll_call","params":{"alarm":true}}]}`) {
		t.Fatalf("tanım gövdede nesne değil: %s", w.Body)
	}
	tags := v.StudyTags
	if len(tags) != 2 || tags[0].Key != "level_a" || tags[0].ParentKey != "" || tags[0].Position != 1 || tags[0].Labels["tr"] != "Düzey A" ||
		tags[0].Status != "active" || tags[1].Key != "subject_a" || tags[1].ParentKey != "level_a" || tags[1].Status != "retired" {
		t.Fatalf("ders/konu etiketleri: %+v", tags)
	}
	// A level has no parent_key in the body.
	if !strings.Contains(w.Body.String(), `{"key":"level_a","position":1,`) {
		t.Fatalf("düzeyin parent_key'i: %s", w.Body)
	}
	// An empty list is [], never null.
	if !strings.Contains(w.Body.String(), `"default_widgets":[]`) {
		t.Fatalf("boş liste: %s", w.Body)
	}
}

func TestCatalogRouteNotModified(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	for name, tag := range map[string]string{
		"güçlü":   `"7"`,
		"zayıf":   `W/"7"`,
		"listede": `"abc", "7"`,
	} {
		w := as(h, "GET", "/v1/catalog", "", "If-None-Match", tag)
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("ETag") != `"7"` {
			t.Errorf("%s: %d %q %q", name, w.Code, w.Body, w.Header().Get("ETag"))
		}
		if got, _, _ := f.last(); got.(*corev1.GetCatalogRequest).GetKnownVersion() != 7 {
			t.Errorf("%s: core'a giden sürüm %v", name, got)
		}
	}
	// Anything that is not one of this endpoint's tags is ignored; a stale
	// version gets the catalog.
	for name, tag := range map[string]string{
		"eski sürüm": `"6"`,
		"tırnaksız":  `7`,
		"yıldız":     `*`,
		"metin":      `"katalog"`,
		"eksi":       `"-7"`,
		"taşma":      `"99999999999999999999"`,
		"çok uzun":   `"7", ` + strings.Repeat(`"x", `, 40),
	} {
		w := as(h, "GET", "/v1/catalog", "", "If-None-Match", tag)
		if w.Code != http.StatusOK || w.Header().Get("ETag") != `"7"` {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

func TestEventTypeVersionRoute(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "GET", "/v1/catalog/event-types/hike/versions/3", "")
	if w.Code != http.StatusOK {
		t.Fatalf("tür sürümü: %d %s", w.Code, w.Body)
	}
	got, tok, _ := f.last()
	in := got.(*corev1.GetEventTypeVersionRequest)
	if in.GetKey() != "hike" || in.GetVersion() != 3 || tok != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", in, tok)
	}
	v := decodeBody[catalogEventTypeJSON](t, w)
	if v.Key != "hike" || v.Version != 3 || v.Status != "retired" || string(v.Definition) != `{"components":[{"kind":"roll_call","params":{"alarm":true}}]}` {
		t.Fatalf("gövde: %+v", v)
	}

	_, _, before := f.last()
	for name, path := range map[string]string{
		"bozuk anahtar":  "/v1/catalog/event-types/Hike/versions/1",
		"tek harf":       "/v1/catalog/event-types/h/versions/1",
		"tireli anahtar": "/v1/catalog/event-types/hike-night/versions/1",
		"sıfır sürüm":    "/v1/catalog/event-types/hike/versions/0",
		"eksi sürüm":     "/v1/catalog/event-types/hike/versions/-1",
		"artılı sürüm":   "/v1/catalog/event-types/hike/versions/+1",
		"baştan sıfırlı": "/v1/catalog/event-types/hike/versions/01",
		"metin sürüm":    "/v1/catalog/event-types/hike/versions/latest",
		"taşan sürüm":    "/v1/catalog/event-types/hike/versions/99999999999",
		"kesirli sürüm":  "/v1/catalog/event-types/hike/versions/1.0",
	} {
		if w := as(h, "GET", path, ""); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if _, _, after := f.last(); after != before {
		t.Fatal("geçersiz istek core'a ulaştı")
	}

	f.err = coreErr(codes.NotFound, "ERROR_REASON_CATALOG_TYPE_NOT_FOUND")
	w = as(h, "GET", "/v1/catalog/event-types/paragliding/versions/1", "")
	if w.Code != http.StatusNotFound || problemType(t, w) != "catalog-type-not-found" {
		t.Fatalf("bilinmeyen tür: %d %s", w.Code, w.Body)
	}
}

func TestCatalogRoutesNeedToken(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	for _, path := range []string{"/v1/catalog", "/v1/catalog/event-types/hike/versions/1"} {
		w := do(h, "GET", path, "", nil)
		if w.Code != http.StatusUnauthorized || problemType(t, w) != "unauthenticated" {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if _, _, calls := f.last(); calls != 0 {
		t.Fatal("kimliksiz istek core'a ulaştı")
	}
}

// A definition that is not a JSON object never reaches the client as a
// broken body.
func TestCatalogRoutesRefuseBrokenDefinition(t *testing.T) {
	for name, def := range map[string]string{"bozuk": `{"components":`, "dizi": `[]`, "boş": ` `} {
		h := coreRouter(&fakeCore{catalogDefinition: def})
		for _, path := range []string{"/v1/catalog", "/v1/catalog/event-types/hike/versions/1"} {
			w := as(h, "GET", path, "")
			if w.Code != http.StatusInternalServerError || problemType(t, w) != "internal" {
				t.Errorf("%s %s: %d %s", name, path, w.Code, w.Body)
			}
		}
	}
}
