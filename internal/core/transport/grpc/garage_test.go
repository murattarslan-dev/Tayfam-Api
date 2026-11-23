package grpc

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/garage"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeGarage struct {
	err         error
	method      string
	user, tayfa id.ID
	target      id.ID
	in          garage.Input
	crid        string
	filter      garage.Filter
	visible     bool
	v           garage.Vehicle
	calls       int
}

func (f *fakeGarage) rec(method string, user, tayfa, target id.ID) {
	f.method, f.user, f.tayfa, f.target, f.calls = method, user, tayfa, target, f.calls+1
}

func (f *fakeGarage) Mine(_ context.Context, user id.ID) (garage.Mine, error) {
	f.rec("Mine", user, id.ID{}, id.ID{})
	return garage.Mine{Vehicles: []garage.Vehicle{f.v}, CallerCanAdd: true}, f.err
}

func (f *fakeGarage) Create(_ context.Context, user id.ID, in garage.Input, crid string) (garage.Vehicle, error) {
	f.rec("Create", user, id.ID{}, id.ID{})
	f.in, f.crid = in, crid
	return f.v, f.err
}

func (f *fakeGarage) Update(_ context.Context, user, v id.ID, in garage.Input) (garage.Vehicle, error) {
	f.rec("Update", user, id.ID{}, v)
	f.in = in
	return f.v, f.err
}

func (f *fakeGarage) Delete(_ context.Context, user, v id.ID) error {
	f.rec("Delete", user, id.ID{}, v)
	return f.err
}

func (f *fakeGarage) List(_ context.Context, user, tayfa id.ID, flt garage.Filter) (garage.Page, error) {
	f.rec("List", user, tayfa, id.ID{})
	f.filter = flt
	return garage.Page{Vehicles: []garage.Vehicle{f.v}, NextPageToken: "sonraki", CallerVisible: true}, f.err
}

func (f *fakeGarage) SetVisibility(_ context.Context, user, tayfa id.ID, visible bool) (bool, error) {
	f.rec("SetVisibility", user, tayfa, id.ID{})
	f.visible = visible
	return visible, f.err
}

func (f *fakeGarage) Remove(_ context.Context, user, tayfa, v id.ID) error {
	f.rec("Remove", user, tayfa, v)
	return f.err
}

func TestMemberGarageMethods(t *testing.T) {
	t0 := time.Date(2026, time.November, 23, 9, 0, 0, 0, time.UTC)
	owner, p1 := id.New(), id.New()
	f := &fakeGarage{v: garage.Vehicle{
		ID: id.New(), Owner: owner, Make: "Anadol", Model: "A1", ModelYear: 1968, Photos: []id.ID{p1},
		CreatedAt: t0, UpdatedAt: t0, CallerCanRemove: true,
	}}
	s := NewMemberGarageServer(f, discard)
	user, tf := id.New(), id.New()
	ctx := callerCtx(user)

	mine, err := s.ListMyGarageVehicles(ctx, &corev1.ListMyGarageVehiclesRequest{})
	if err != nil || f.method != "Mine" || f.user != user || !mine.GetCallerCanAdd() || len(mine.GetVehicles()) != 1 {
		t.Fatalf("garaj: %v %+v", err, f)
	}
	pb := mine.GetVehicles()[0]
	if pb.GetOwnerId() != owner.String() || pb.GetMake() != "Anadol" || pb.GetModelYear() != 1968 ||
		!slices.Equal(pb.GetPhotoUploadIds(), []string{p1.String()}) || !pb.GetCallerCanRemove() || !pb.GetCreateTime().AsTime().Equal(t0) {
		t.Fatalf("araç: %v", pb)
	}

	crid := id.New().String()
	_, err = s.CreateGarageVehicle(ctx, &corev1.CreateGarageVehicleRequest{
		Vehicle: &corev1.GarageVehicleInput{Make: "Anadol", Note: "Not", ModelYear: 1968, PhotoUploadIds: []string{p1.String()}}, ClientRequestId: crid,
	})
	if err != nil || f.method != "Create" || f.crid != crid || f.in.Make != "Anadol" || f.in.Note != "Not" || !slices.Equal(f.in.Photos, []id.ID{p1}) {
		t.Fatalf("ekleme: %v %+v", err, f)
	}
	v := id.New()
	_, err = s.UpdateGarageVehicle(ctx, &corev1.UpdateGarageVehicleRequest{VehicleId: v.String(), Vehicle: &corev1.GarageVehicleInput{Make: "Murat"}})
	if err != nil || f.method != "Update" || f.target != v || f.in.Make != "Murat" {
		t.Fatalf("düzenleme: %v %+v", err, f)
	}
	_, err = s.DeleteGarageVehicle(ctx, &corev1.DeleteGarageVehicleRequest{VehicleId: v.String()})
	if err != nil || f.method != "Delete" || f.target != v {
		t.Fatalf("silme: %v %+v", err, f)
	}
	list, err := s.ListTayfaGarage(ctx, &corev1.ListTayfaGarageRequest{TayfaId: tf.String(), OwnerId: owner.String(), PageSize: 5, PageToken: "t"})
	if err != nil || f.method != "List" || f.tayfa != tf || f.filter != (garage.Filter{Owner: owner, PageSize: 5, PageToken: "t"}) ||
		!list.GetCallerVisible() || list.GetNextPageToken() != "sonraki" {
		t.Fatalf("tayfa garajı: %v %+v", err, f)
	}
	vis, err := s.SetGarageVisibility(ctx, &corev1.SetGarageVisibilityRequest{TayfaId: tf.String(), Visible: true})
	if err != nil || f.method != "SetVisibility" || f.tayfa != tf || !f.visible || !vis.GetVisible() {
		t.Fatalf("görünürlük: %v %+v", err, f)
	}
	_, err = s.RemoveTayfaGarageVehicle(ctx, &corev1.RemoveTayfaGarageVehicleRequest{TayfaId: tf.String(), VehicleId: v.String()})
	if err != nil || f.method != "Remove" || f.tayfa != tf || f.target != v {
		t.Fatalf("kaldırma: %v %+v", err, f)
	}

	// Bad IDs never reach the app.
	calls := f.calls
	for name, call := range map[string]func() error{
		"araç kimliği": func() error {
			_, err := s.UpdateGarageVehicle(ctx, &corev1.UpdateGarageVehicleRequest{VehicleId: "x", Vehicle: &corev1.GarageVehicleInput{Make: "a"}})
			return err
		},
		"fotoğraf": func() error {
			_, err := s.CreateGarageVehicle(ctx, &corev1.CreateGarageVehicleRequest{Vehicle: &corev1.GarageVehicleInput{Make: "a", PhotoUploadIds: []string{"x"}}})
			return err
		},
		"dört fotoğraf": func() error {
			p := p1.String()
			_, err := s.CreateGarageVehicle(ctx, &corev1.CreateGarageVehicleRequest{Vehicle: &corev1.GarageVehicleInput{Make: "a", PhotoUploadIds: []string{p, p, p, p}}})
			return err
		},
		"sahip": func() error {
			_, err := s.ListTayfaGarage(ctx, &corev1.ListTayfaGarageRequest{TayfaId: tf.String(), OwnerId: "x"})
			return err
		},
		"tayfa": func() error {
			_, err := s.SetGarageVisibility(ctx, &corev1.SetGarageVisibilityRequest{TayfaId: "x"})
			return err
		},
	} {
		if err := call(); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.calls != calls {
		t.Fatal("bozuk istek uygulamaya ulaştı")
	}

	f.err = apperr.ErrGarageLimit
	if _, err := s.CreateGarageVehicle(ctx, &corev1.CreateGarageVehicleRequest{Vehicle: &corev1.GarageVehicleInput{Make: "a"}}); codeOf(t, err) != codes.FailedPrecondition {
		t.Fatalf("sınır: %v", err)
	}
	if _, err := s.ListMyGarageVehicles(context.Background(), &corev1.ListMyGarageVehiclesRequest{}); codeOf(t, err) != codes.Unauthenticated {
		t.Fatalf("kimliksiz: %v", err)
	}
}
