package grpc

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/material"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeMaterials struct {
	err         error
	method      string
	user, tayfa id.ID
	target      id.ID
	in          material.Input
	crid        string
	filter      material.Filter
	m           material.Material
	calls       int
}

func (f *fakeMaterials) rec(method string, user, tayfa, target id.ID) {
	f.method, f.user, f.tayfa, f.target, f.calls = method, user, tayfa, target, f.calls+1
}

func (f *fakeMaterials) List(_ context.Context, user, tayfa id.ID, flt material.Filter) (material.Page, error) {
	f.rec("List", user, tayfa, id.ID{})
	f.filter = flt
	return material.Page{Materials: []material.Material{f.m}, NextPageToken: "sonraki", CallerCanAdd: true}, f.err
}

func (f *fakeMaterials) Get(_ context.Context, user, tayfa, m id.ID) (material.Material, error) {
	f.rec("Get", user, tayfa, m)
	return f.m, f.err
}

func (f *fakeMaterials) Create(_ context.Context, user, tayfa id.ID, in material.Input, crid string) (material.Material, error) {
	f.rec("Create", user, tayfa, id.ID{})
	f.in, f.crid = in, crid
	return f.m, f.err
}

func (f *fakeMaterials) Update(_ context.Context, user, tayfa, m id.ID, in material.Input) (material.Material, error) {
	f.rec("Update", user, tayfa, m)
	f.in = in
	return f.m, f.err
}

func (f *fakeMaterials) Delete(_ context.Context, user, tayfa, m id.ID) error {
	f.rec("Delete", user, tayfa, m)
	return f.err
}

func TestTayfaMaterialMethods(t *testing.T) {
	t0 := time.Date(2026, time.November, 23, 9, 0, 0, 0, time.UTC)
	author, p1, p2 := id.New(), id.New(), id.New()
	f := &fakeMaterials{m: material.Material{
		ID: id.New(), Kind: material.KindNotePhoto, Title: "Defter", Photos: []id.ID{p1, p2}, SubjectKey: "math",
		Author: author, CreatedAt: t0, UpdatedAt: t0, CallerCanDelete: true,
	}}
	s := NewTayfaMaterialServer(f, discard)
	user, tf := id.New(), id.New()
	ctx := callerCtx(user)

	list, err := s.ListMaterials(ctx, &corev1.ListMaterialsRequest{
		TayfaId: tf.String(), Kind: corev1.MaterialKind_MATERIAL_KIND_NOTE_PHOTO, SubjectKey: "math", TopicKey: "math_limit",
		Mine: true, PageSize: 5, PageToken: "t",
	})
	want := material.Filter{Kind: material.KindNotePhoto, SubjectKey: "math", TopicKey: "math_limit", Mine: true, PageSize: 5, PageToken: "t"}
	if err != nil || f.method != "List" || f.user != user || f.tayfa != tf || f.filter != want ||
		!list.GetCallerCanAdd() || list.GetNextPageToken() != "sonraki" {
		t.Fatalf("liste: %v %+v", err, f)
	}
	m := list.GetMaterials()[0]
	if m.GetKind() != corev1.MaterialKind_MATERIAL_KIND_NOTE_PHOTO || m.GetUrl() != "" || m.GetTopicKey() != "" ||
		!slices.Equal(m.GetPhotoUploadIds(), []string{p1.String(), p2.String()}) || m.GetAuthorId() != author.String() ||
		m.GetCallerCanEdit() || !m.GetCallerCanDelete() {
		t.Fatalf("materyal: %v", m)
	}

	crid := id.New().String()
	if _, err := s.CreateMaterial(ctx, &corev1.CreateMaterialRequest{
		TayfaId: tf.String(), Kind: corev1.MaterialKind_MATERIAL_KIND_LINK, Title: "Özet", Url: "https://example.com/",
		SubjectKey: "math", TopicKey: "math_limit", ClientRequestId: crid,
	}); err != nil || f.method != "Create" || f.crid != crid || f.in.Kind != material.KindLink || f.in.URL != "https://example.com/" ||
		f.in.Title != "Özet" || f.in.SubjectKey != "math" || f.in.TopicKey != "math_limit" || len(f.in.Photos) != 0 {
		t.Fatalf("ekleme: %v %+v", err, f)
	}
	if _, err := s.UpdateMaterial(ctx, &corev1.UpdateMaterialRequest{
		TayfaId: tf.String(), MaterialId: f.m.ID.String(), Title: "Yeni", PhotoUploadIds: []string{p2.String()}, SubjectKey: "math",
	}); err != nil || f.method != "Update" || f.target != f.m.ID || !slices.Equal(f.in.Photos, []id.ID{p2}) || f.in.Title != "Yeni" {
		t.Fatalf("düzenleme: %v %+v", err, f)
	}
	if _, err := s.GetMaterial(ctx, &corev1.GetMaterialRequest{TayfaId: tf.String(), MaterialId: f.m.ID.String()}); err != nil ||
		f.method != "Get" || f.target != f.m.ID {
		t.Fatalf("okuma: %v %+v", err, f)
	}
	if _, err := s.DeleteMaterial(ctx, &corev1.DeleteMaterialRequest{TayfaId: tf.String(), MaterialId: f.m.ID.String()}); err != nil ||
		f.method != "Delete" || f.target != f.m.ID {
		t.Fatalf("silme: %v %+v", err, f)
	}
}

func TestTayfaMaterialValidation(t *testing.T) {
	f := &fakeMaterials{}
	s := NewTayfaMaterialServer(f, discard)
	ctx := callerCtx(id.New())
	tf := id.New().String()
	four := []string{id.New().String(), id.New().String(), id.New().String(), id.New().String()}
	for name, call := range map[string]func() error{
		"tayfa": func() error {
			_, err := s.ListMaterials(ctx, &corev1.ListMaterialsRequest{TayfaId: "x"})
			return err
		},
		"kind": func() error {
			_, err := s.ListMaterials(ctx, &corev1.ListMaterialsRequest{TayfaId: tf, Kind: corev1.MaterialKind(99)})
			return err
		},
		"create kind": func() error {
			_, err := s.CreateMaterial(ctx, &corev1.CreateMaterialRequest{TayfaId: tf, Title: "x", ClientRequestId: id.New().String()})
			return err
		},
		"photo": func() error {
			_, err := s.CreateMaterial(ctx, &corev1.CreateMaterialRequest{TayfaId: tf, Kind: corev1.MaterialKind_MATERIAL_KIND_NOTE_PHOTO, Title: "x", PhotoUploadIds: []string{"x"}})
			return err
		},
		"four photos": func() error {
			_, err := s.UpdateMaterial(ctx, &corev1.UpdateMaterialRequest{TayfaId: tf, MaterialId: id.New().String(), Title: "x", PhotoUploadIds: four})
			return err
		},
		"material_id": func() error {
			_, err := s.DeleteMaterial(ctx, &corev1.DeleteMaterialRequest{TayfaId: tf, MaterialId: "x"})
			return err
		},
	} {
		if err := call(); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek uygulamaya ulaştı: %d", f.calls)
	}
	for err, want := range map[error]codes.Code{
		apperr.ErrMaterialNotFound: codes.NotFound,
		apperr.ErrBlockedLink:      codes.InvalidArgument,
		apperr.ErrMaterialLimit:    codes.FailedPrecondition,
	} {
		f.err = err
		if _, err := s.GetMaterial(ctx, &corev1.GetMaterialRequest{TayfaId: tf, MaterialId: id.New().String()}); codeOf(t, err) != want {
			t.Errorf("%v: %v", f.err, err)
		}
	}
}
