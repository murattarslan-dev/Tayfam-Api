package grpc

import (
	"context"
	"errors"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeCatalog struct {
	err     error
	known   int64
	key     string
	version int32
	called  bool
}

func (f *fakeCatalog) hike() catalog.EventType {
	return catalog.EventType{
		Key: "hike", InterestKeys: []string{"nature", "sports"}, Labels: map[string]string{"tr": "Doğa yürüyüşü"},
		Icon: "hiking", Modes: []string{catalog.ModeInPerson, catalog.ModeOnline}, Status: catalog.StatusRetired,
		Version: 3, Definition: []byte(`{"components":[{"kind":"route"}]}`),
	}
}

func (f *fakeCatalog) Get(_ context.Context, known int64) (catalog.Catalog, bool, error) {
	f.known, f.called = known, true
	if f.err != nil {
		return catalog.Catalog{}, false, f.err
	}
	if known == 7 {
		return catalog.Catalog{Version: 7}, true, nil
	}
	return catalog.Catalog{
		Version: 7,
		Interests: []catalog.Interest{{
			Key: "nature", Position: 20, Labels: map[string]string{"tr": "Doğa"}, Icon: "forest",
			Status: catalog.StatusActive, DefaultWidgets: []string{"next_event"},
		}},
		Types: []catalog.EventType{f.hike()},
		StudyTags: []catalog.StudyTag{
			{Key: "level_a", Position: 1, Labels: map[string]string{"tr": "Düzey A"}, Status: catalog.StatusActive},
			{Key: "subject_a", ParentKey: "level_a", Position: 2, Labels: map[string]string{"tr": "Ders A"}, Status: catalog.StatusRetired},
		},
	}, false, nil
}

func (f *fakeCatalog) TypeVersion(_ context.Context, key string, version int32) (catalog.EventType, error) {
	f.key, f.version, f.called = key, version, true
	if f.err != nil {
		return catalog.EventType{}, f.err
	}
	return f.hike(), nil
}

func TestCatalogMethods(t *testing.T) {
	f := &fakeCatalog{}
	s := NewCatalogServer(f, discard)
	ctx := callerCtx(id.New())

	resp, err := s.GetCatalog(ctx, &corev1.GetCatalogRequest{KnownVersion: 6})
	if err != nil {
		t.Fatal(err)
	}
	if f.known != 6 || resp.GetCatalogVersion() != 7 || resp.GetNotModified() || len(resp.GetInterests()) != 1 || len(resp.GetEventTypes()) != 1 {
		t.Fatalf("katalog: %v (bilinen %d)", resp, f.known)
	}
	in := resp.GetInterests()[0]
	if in.GetKey() != "nature" || in.GetPosition() != 20 || in.GetLabels()["tr"] != "Doğa" || in.GetIcon() != "forest" ||
		in.GetStatus() != corev1.CatalogStatus_CATALOG_STATUS_ACTIVE || !slices.Equal(in.GetDefaultWidgets(), []string{"next_event"}) {
		t.Fatalf("ilgi alanı: %v", in)
	}
	et := resp.GetEventTypes()[0]
	if et.GetKey() != "hike" || !slices.Equal(et.GetInterestKeys(), []string{"nature", "sports"}) || et.GetLabels()["tr"] != "Doğa yürüyüşü" ||
		et.GetIcon() != "hiking" || et.GetStatus() != corev1.CatalogStatus_CATALOG_STATUS_RETIRED || et.GetVersion() != 3 ||
		et.GetDefinition() != `{"components":[{"kind":"route"}]}` ||
		!slices.Equal(et.GetModes(), []corev1.EventMode{corev1.EventMode_EVENT_MODE_IN_PERSON, corev1.EventMode_EVENT_MODE_ONLINE}) {
		t.Fatalf("tür: %v", et)
	}
	tags := resp.GetStudyTags()
	if len(tags) != 2 || tags[0].GetKey() != "level_a" || tags[0].GetParentKey() != "" || tags[0].GetPosition() != 1 ||
		tags[0].GetLabels()["tr"] != "Düzey A" || tags[0].GetStatus() != corev1.CatalogStatus_CATALOG_STATUS_ACTIVE ||
		tags[1].GetKey() != "subject_a" || tags[1].GetParentKey() != "level_a" ||
		tags[1].GetStatus() != corev1.CatalogStatus_CATALOG_STATUS_RETIRED {
		t.Fatalf("ders/konu etiketleri: %v", tags)
	}

	// The caller's version is current: no lists.
	resp, err = s.GetCatalog(ctx, &corev1.GetCatalogRequest{KnownVersion: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetNotModified() || resp.GetCatalogVersion() != 7 || len(resp.GetInterests()) != 0 || len(resp.GetEventTypes()) != 0 ||
		len(resp.GetStudyTags()) != 0 {
		t.Fatalf("değişmemiş katalog: %v", resp)
	}

	v, err := s.GetEventTypeVersion(ctx, &corev1.GetEventTypeVersionRequest{Key: "hike", Version: 3})
	if err != nil {
		t.Fatal(err)
	}
	if f.key != "hike" || f.version != 3 || v.GetEventType().GetKey() != "hike" || v.GetEventType().GetVersion() != 3 {
		t.Fatalf("tür sürümü: %v (%s %d)", v, f.key, f.version)
	}
}

func TestCatalogErrors(t *testing.T) {
	ctx := callerCtx(id.New())
	for _, tc := range []struct {
		name   string
		err    error
		code   codes.Code
		reason corev1.ErrorReason
	}{
		{"bilinmeyen tür", apperr.ErrCatalogTypeNotFound, codes.NotFound, corev1.ErrorReason_ERROR_REASON_CATALOG_TYPE_NOT_FOUND},
		{"bozuk anahtar", apperr.Validation("key"), codes.InvalidArgument, corev1.ErrorReason_ERROR_REASON_VALIDATION},
	} {
		s := NewCatalogServer(&fakeCatalog{err: tc.err}, discard)
		_, err := s.GetEventTypeVersion(ctx, &corev1.GetEventTypeVersionRequest{Key: "x", Version: 1})
		code, info := infoOf(t, err)
		if code != tc.code || info.GetReason() != tc.reason.String() {
			t.Errorf("%s: %v %v", tc.name, code, info)
		}
	}
	// A catalog that fails its own validation is an internal error without
	// detail.
	s := NewCatalogServer(&fakeCatalog{err: errors.New(`tür "hike": tanım: components[0].kind: bilinmeyen değer`)}, discard)
	_, err := s.GetCatalog(ctx, &corev1.GetCatalogRequest{})
	if code, info := infoOf(t, err); code != codes.Internal || info != nil {
		t.Fatalf("iç hata: %v %v", code, info)
	}
}

func TestCatalogNeedsCaller(t *testing.T) {
	f := &fakeCatalog{}
	s := NewCatalogServer(f, discard)
	if _, err := s.GetCatalog(context.Background(), &corev1.GetCatalogRequest{}); codeOf(t, err) != codes.Unauthenticated {
		t.Fatalf("katalog: %v", err)
	}
	if _, err := s.GetEventTypeVersion(context.Background(), &corev1.GetEventTypeVersionRequest{Key: "hike", Version: 1}); codeOf(t, err) != codes.Unauthenticated {
		t.Fatalf("tür sürümü: %v", err)
	}
	if f.called {
		t.Fatal("kimliksiz istek app katmanına ulaştı")
	}
}
