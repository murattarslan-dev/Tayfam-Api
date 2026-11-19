package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/vehicles"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeVehicles struct {
	err     error
	method  string
	ref     component.Ref
	in      vehicles.Input
	vehicle id.ID
	rider   id.ID
	out     vehicles.Vehicles
	calls   int
}

func (f *fakeVehicles) rec(method string, ref component.Ref, vehicle, rider id.ID) (vehicles.Vehicles, error) {
	f.method, f.ref, f.vehicle, f.rider, f.calls = method, ref, vehicle, rider, f.calls+1
	return f.out, f.err
}

func (f *fakeVehicles) List(_ context.Context, ref component.Ref) (vehicles.Vehicles, error) {
	return f.rec("List", ref, id.ID{}, id.ID{})
}

func (f *fakeVehicles) Set(_ context.Context, ref component.Ref, in vehicles.Input) (vehicles.Vehicles, error) {
	f.in = in
	return f.rec("Set", ref, id.ID{}, id.ID{})
}

func (f *fakeVehicles) Remove(_ context.Context, ref component.Ref, vehicle id.ID) (vehicles.Vehicles, error) {
	return f.rec("Remove", ref, vehicle, id.ID{})
}

func (f *fakeVehicles) RequestSeat(_ context.Context, ref component.Ref, vehicle id.ID) (vehicles.Vehicles, error) {
	return f.rec("RequestSeat", ref, vehicle, id.ID{})
}

func (f *fakeVehicles) ApproveSeat(_ context.Context, ref component.Ref, vehicle, rider id.ID) (vehicles.Vehicles, error) {
	return f.rec("ApproveSeat", ref, vehicle, rider)
}

func (f *fakeVehicles) ReleaseSeat(_ context.Context, ref component.Ref, vehicle, rider id.ID) (vehicles.Vehicles, error) {
	return f.rec("ReleaseSeat", ref, vehicle, rider)
}

func TestVehiclesComponentMethods(t *testing.T) {
	owner, rider, waiting, vid := id.New(), id.New(), id.New(), id.New()
	f := &fakeVehicles{out: vehicles.Vehicles{
		Items: []vehicles.Vehicle{{
			ID: vid, UserID: owner, Make: "Ford", Model: "Mustang", ModelYear: 1967, Color: "Kırmızı", Plate: "35 ABC 123", Seats: 3,
			Riders: []id.ID{rider}, Pending: []id.ID{waiting},
		}},
		PlateEnabled: true, SeatsEnabled: true, CallerCanParticipate: true, CallerCanManage: true,
	}}
	s := NewVehiclesComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "cars"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "cars"}
	check := func(method string, got *corev1.EventVehicles, err error, vehicle, r id.ID) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if f.method != method || f.ref != want || f.vehicle != vehicle || f.rider != r {
			t.Fatalf("%s: app'e giden %s %+v %s %s", method, f.method, f.ref, f.vehicle, f.rider)
		}
		v := got.GetVehicles()
		if len(v) != 1 || v[0].GetId() != vid.String() || v[0].GetUserId() != owner.String() || v[0].GetMake() != "Ford" ||
			v[0].GetModel() != "Mustang" || v[0].GetModelYear() != 1967 || v[0].GetColor() != "Kırmızı" || v[0].GetPlate() != "35 ABC 123" ||
			v[0].GetSeats() != 3 || len(v[0].GetRiderUserIds()) != 1 || v[0].GetRiderUserIds()[0] != rider.String() ||
			len(v[0].GetPendingUserIds()) != 1 || v[0].GetPendingUserIds()[0] != waiting.String() ||
			!got.GetPlateEnabled() || !got.GetSeatsEnabled() || !got.GetCallerCanParticipate() || !got.GetCallerCanManage() {
			t.Fatalf("%s: yanıt %v", method, got)
		}
	}

	list, err := s.ListVehicles(ctx, &corev1.ListVehiclesRequest{Ref: ref})
	check("List", list.GetVehicles(), err, id.ID{}, id.ID{})
	set, err := s.SetVehicle(ctx, &corev1.SetVehicleRequest{Ref: ref, Make: "Ford", Model: "Mustang", ModelYear: 1967, Color: "Kırmızı", Plate: "35 abc 123", Seats: 3})
	check("Set", set.GetVehicles(), err, id.ID{}, id.ID{})
	// The values reach the app layer as sent; it cleans them.
	if f.in != (vehicles.Input{Make: "Ford", Model: "Mustang", ModelYear: 1967, Color: "Kırmızı", Plate: "35 abc 123", Seats: 3}) {
		t.Fatalf("araç girdisi: %+v", f.in)
	}
	removed, err := s.RemoveVehicle(ctx, &corev1.RemoveVehicleRequest{Ref: ref, VehicleId: vid.String()})
	check("Remove", removed.GetVehicles(), err, vid, id.ID{})
	asked, err := s.RequestSeat(ctx, &corev1.RequestSeatRequest{Ref: ref, VehicleId: vid.String()})
	check("RequestSeat", asked.GetVehicles(), err, vid, id.ID{})
	approved, err := s.ApproveSeat(ctx, &corev1.ApproveSeatRequest{Ref: ref, VehicleId: vid.String(), UserId: rider.String()})
	check("ApproveSeat", approved.GetVehicles(), err, vid, rider)
	released, err := s.ReleaseSeat(ctx, &corev1.ReleaseSeatRequest{Ref: ref, VehicleId: vid.String(), UserId: rider.String()})
	check("ReleaseSeat", released.GetVehicles(), err, vid, rider)

	// No vehicles: an empty list, not nil fields that break a client.
	f.out = vehicles.Vehicles{Items: []vehicles.Vehicle{}}
	if list, err := s.ListVehicles(ctx, &corev1.ListVehiclesRequest{Ref: ref}); err != nil || len(list.GetVehicles().GetVehicles()) != 0 || list.GetVehicles().GetCallerCanParticipate() {
		t.Fatalf("araçsız: %v %v", list, err)
	}

	// The app's errors keep their reasons.
	for appErr, want := range map[error]corev1.ErrorReason{
		apperr.ErrVehicleFull:       corev1.ErrorReason_ERROR_REASON_VEHICLE_FULL,
		apperr.ErrVehicleSeatsTaken: corev1.ErrorReason_ERROR_REASON_VEHICLE_SEATS_TAKEN,
		apperr.ErrSeatRequestDriver: corev1.ErrorReason_ERROR_REASON_SEAT_REQUEST_DRIVER,
		apperr.ErrEventNotGoing:     corev1.ErrorReason_ERROR_REASON_EVENT_NOT_GOING,
		apperr.ErrComponentFrozen:   corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN,
	} {
		f.err = appErr
		_, err := s.RequestSeat(ctx, &corev1.RequestSeatRequest{Ref: ref, VehicleId: vid.String()})
		if code, info := infoOf(t, err); code != codes.FailedPrecondition || info.GetReason() != want.String() {
			t.Errorf("%v: %v", appErr, err)
		}
	}
	f.err = apperr.ErrEventItemNotFound
	if _, err := s.RemoveVehicle(ctx, &corev1.RemoveVehicleRequest{Ref: ref, VehicleId: vid.String()}); codeOf(t, err) != codes.NotFound {
		t.Fatalf("olmayan araç: %v", err)
	}
}

// Junk is refused before the app layer is asked.
func TestVehiclesComponentValidation(t *testing.T) {
	f := &fakeVehicles{}
	s := NewVehiclesComponentServer(f, discard)
	tf, ev, good := id.New().String(), id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "vehicles"}
	field := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}

	for name, bad := range map[string]*corev1.ComponentRef{
		"başvurusuz":     nil,
		"bozuk tayfa":    {TayfaId: "x", EventId: ev, ComponentKey: "vehicles"},
		"bozuk etkinlik": {TayfaId: tf, EventId: "x", ComponentKey: "vehicles"},
		"bozuk anahtar":  {TayfaId: tf, EventId: ev, ComponentKey: "Vehicles"},
	} {
		_, e1 := s.ListVehicles(ctx, &corev1.ListVehiclesRequest{Ref: bad})
		_, e2 := s.SetVehicle(ctx, &corev1.SetVehicleRequest{Ref: bad, Make: "Ford"})
		_, e3 := s.RemoveVehicle(ctx, &corev1.RemoveVehicleRequest{Ref: bad, VehicleId: good})
		_, e4 := s.RequestSeat(ctx, &corev1.RequestSeatRequest{Ref: bad, VehicleId: good})
		_, e5 := s.ApproveSeat(ctx, &corev1.ApproveSeatRequest{Ref: bad, VehicleId: good, UserId: good})
		_, e6 := s.ReleaseSeat(ctx, &corev1.ReleaseSeatRequest{Ref: bad, VehicleId: good, UserId: good})
		for i, err := range []error{e1, e2, e3, e4, e5, e6} {
			if codeOf(t, err) != codes.InvalidArgument {
				t.Errorf("%s / %d: %v", name, i, err)
			}
		}
	}
	_, err := s.SetVehicle(ctx, &corev1.SetVehicleRequest{Ref: ref, Make: "Ford", Seats: -1})
	if field(err) != "seats" {
		t.Error("eksi koltuk")
	}
	_, err = s.SetVehicle(ctx, &corev1.SetVehicleRequest{Ref: ref, Make: "Ford", Seats: vehicles.MaxSeats + 1})
	if field(err) != "seats" {
		t.Error("dokuz koltuk")
	}
	_, err = s.RemoveVehicle(ctx, &corev1.RemoveVehicleRequest{Ref: ref, VehicleId: "x"})
	if field(err) != "vehicle_id" {
		t.Error("bozuk araç kimliği")
	}
	_, err = s.RequestSeat(ctx, &corev1.RequestSeatRequest{Ref: ref})
	if field(err) != "vehicle_id" {
		t.Error("araçsız koltuk isteği")
	}
	_, err = s.ApproveSeat(ctx, &corev1.ApproveSeatRequest{Ref: ref, VehicleId: good, UserId: "x"})
	if field(err) != "user_id" {
		t.Error("onayda bozuk kişi")
	}
	_, err = s.ReleaseSeat(ctx, &corev1.ReleaseSeatRequest{Ref: ref, VehicleId: good})
	if field(err) != "user_id" {
		t.Error("bırakmada kişi yok")
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.SetVehicle(context.Background(), &corev1.SetVehicleRequest{Ref: ref, Make: "Ford"}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the vehicles component.
func TestVehiclesComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.VehiclesComponentService"]
	if !ok || len(info.Methods) != 6 {
		t.Fatalf("araçlar servisi: %v", info)
	}
}
