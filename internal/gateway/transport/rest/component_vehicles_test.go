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

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const vehiclesComponentService = "tayfam.core.v1.VehiclesComponentService"

// fakeVehicles is core's VehiclesComponentService for the vehicles tests.
type fakeVehicles struct {
	got   proto.Message
	token string
	out   *corev1.EventVehicles
	err   error
	calls int
}

func (f *fakeVehicles) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func (f *fakeVehicles) ListVehicles(ctx context.Context, in *corev1.ListVehiclesRequest, _ ...grpc.CallOption) (*corev1.ListVehiclesResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListVehiclesResponse{Vehicles: f.out}, f.err
}

func (f *fakeVehicles) SetVehicle(ctx context.Context, in *corev1.SetVehicleRequest, _ ...grpc.CallOption) (*corev1.SetVehicleResponse, error) {
	f.rec(ctx, in)
	return &corev1.SetVehicleResponse{Vehicles: f.out}, f.err
}

func (f *fakeVehicles) RemoveVehicle(ctx context.Context, in *corev1.RemoveVehicleRequest, _ ...grpc.CallOption) (*corev1.RemoveVehicleResponse, error) {
	f.rec(ctx, in)
	return &corev1.RemoveVehicleResponse{Vehicles: f.out}, f.err
}

func (f *fakeVehicles) RequestSeat(ctx context.Context, in *corev1.RequestSeatRequest, _ ...grpc.CallOption) (*corev1.RequestSeatResponse, error) {
	f.rec(ctx, in)
	return &corev1.RequestSeatResponse{Vehicles: f.out}, f.err
}

func (f *fakeVehicles) ApproveSeat(ctx context.Context, in *corev1.ApproveSeatRequest, _ ...grpc.CallOption) (*corev1.ApproveSeatResponse, error) {
	f.rec(ctx, in)
	return &corev1.ApproveSeatResponse{Vehicles: f.out}, f.err
}

func (f *fakeVehicles) ReleaseSeat(ctx context.Context, in *corev1.ReleaseSeatRequest, _ ...grpc.CallOption) (*corev1.ReleaseSeatResponse, error) {
	f.rec(ctx, in)
	return &corev1.ReleaseSeatResponse{Vehicles: f.out}, f.err
}

const (
	vehiclesPath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/cars/vehicles"
	vehiclePath  = vehiclesPath + "/" + vehicleID
	riderID      = "01965a3c-0000-7000-8000-0000000000a1"
)

func testEventVehicles() *corev1.EventVehicles {
	return &corev1.EventVehicles{
		Vehicles: []*corev1.EventVehicle{
			{Id: vehicleID, UserId: userID, Make: "Ford", Model: "Mustang", ModelYear: 1967, Color: "Kırmızı", Plate: "34 ABC 12", Seats: 3,
				RiderUserIds: []string{riderID}, PendingUserIds: []string{tayfaID}},
			{Id: eventID, UserId: riderID, Make: "Murat"},
		},
		PlateEnabled: true, SeatsEnabled: true, CallerCanParticipate: true,
	}
}

func TestVehiclesComponentRoutes(t *testing.T) {
	f := &fakeVehicles{out: testEventVehicles()}
	h := componentRouter(fakeServices{vehiclesComponentService: f})
	ref := &corev1.ComponentRef{TayfaId: tayfaID, EventId: eventID, ComponentKey: "cars"}

	for _, tc := range []struct {
		method, path, body string
		want               proto.Message
	}{
		{"GET", vehiclesPath, "", &corev1.ListVehiclesRequest{Ref: ref}},
		{"PUT", vehiclesPath + "/mine", `{"make":"Ford","model":"Mustang","model_year":1967,"color":"Kırmızı","plate":"34 ABC 12","seats":3}`,
			&corev1.SetVehicleRequest{Ref: ref, Make: "Ford", Model: "Mustang", ModelYear: 1967, Color: "Kırmızı", Plate: "34 ABC 12", Seats: 3}},
		{"PUT", vehiclesPath + "/mine", `{"make":"Murat"}`, &corev1.SetVehicleRequest{Ref: ref, Make: "Murat"}},
		{"DELETE", vehiclePath, "", &corev1.RemoveVehicleRequest{Ref: ref, VehicleId: vehicleID}},
		{"PUT", vehiclePath + "/seat-requests/mine", "", &corev1.RequestSeatRequest{Ref: ref, VehicleId: vehicleID}},
		{"PUT", vehiclePath + "/seat-requests/" + riderID + "/approval", "", &corev1.ApproveSeatRequest{Ref: ref, VehicleId: vehicleID, UserId: riderID}},
		{"DELETE", vehiclePath + "/seat-requests/" + riderID, "", &corev1.ReleaseSeatRequest{Ref: ref, VehicleId: vehicleID, UserId: riderID}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s %s: core'a giden %v (%q), beklenen %v", tc.method, tc.path, f.got, f.token, tc.want)
		}
		got := decodeBody[eventVehiclesJSON](t, w)
		if len(got.Vehicles) != 2 || !got.PlateEnabled || !got.SeatsEnabled || !got.CallerCanParticipate || got.CallerCanManage {
			t.Fatalf("%s %s: yanıt %+v", tc.method, tc.path, got)
		}
		v := got.Vehicles[0]
		if v.ID != vehicleID || v.UserID != userID || v.Make != "Ford" || v.Model != "Mustang" || v.ModelYear != 1967 || v.Color != "Kırmızı" ||
			v.Plate != "34 ABC 12" || v.Seats != 3 || len(v.RiderUserIDs) != 1 || v.RiderUserIDs[0] != riderID || len(v.PendingUserIDs) != 1 {
			t.Fatalf("%s %s: araç %+v", tc.method, tc.path, v)
		}
	}

	// A vehicle without optional values: no empty strings, empty arrays
	// rather than null, and no plate key.
	w := as(h, "GET", vehiclesPath, "")
	for _, want := range []string{`{"id":"` + eventID + `","user_id":"` + riderID + `","make":"Murat","seats":0,"rider_user_ids":[],"pending_user_ids":[]}`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("yalın araç: %s", w.Body)
		}
	}
	// No vehicles: an empty array.
	f.out = &corev1.EventVehicles{}
	if w := as(h, "GET", vehiclesPath, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"vehicles":[]`) || !strings.Contains(w.Body.String(), `"caller_can_participate":false`) {
		t.Fatalf("araçsız okuma: %d %s", w.Code, w.Body)
	}
}

func TestVehiclesComponentRoutesValidateInput(t *testing.T) {
	f := &fakeVehicles{out: &corev1.EventVehicles{}}
	h := componentRouter(fakeServices{vehiclesComponentService: f})

	for name, body := range map[string]string{
		"boş gövde":       `{}`,
		"marka yok":       `{"make":" "}`,
		"uzun marka":      `{"make":"` + strings.Repeat("m", 41) + `"}`,
		"uzun model":      `{"make":"Ford","model":"` + strings.Repeat("m", 41) + `"}`,
		"uzun renk":       `{"make":"Ford","color":"` + strings.Repeat("r", 31) + `"}`,
		"uzun plaka":      `{"make":"Ford","plate":"` + strings.Repeat("A", 17) + `"}`,
		"eksi yıl":        `{"make":"Ford","model_year":-1}`,
		"eksi koltuk":     `{"make":"Ford","seats":-1}`,
		"dokuz koltuk":    `{"make":"Ford","seats":9}`,
		"metin koltuk":    `{"make":"Ford","seats":"2"}`,
		"fazla alan":      `{"make":"Ford","user_id":"` + riderID + `"}`,
		"masraf alanı":    `{"make":"Ford","cost":100}`,
		"bozuk JSON":      `{"make":`,
		"dizi gövde":      `[]`,
		"null olan gövde": `null`,
	} {
		if w := as(h, "PUT", vehiclesPath+"/mine", body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// The path is checked before core is asked.
	for _, rt := range []struct{ method, path string }{
		{"GET", "/v1/tayfas/x/events/" + eventID + "/components/cars/vehicles"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/x/components/cars/vehicles"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Cars/vehicles"},
		{"DELETE", vehiclesPath + "/x"},
		{"PUT", vehiclesPath + "/x/seat-requests/mine"},
		{"PUT", vehiclePath + "/seat-requests/x/approval"},
		{"PUT", vehiclesPath + "/x/seat-requests/" + riderID + "/approval"},
		{"DELETE", vehiclePath + "/seat-requests/x"},
		{"DELETE", vehiclePath + "/seat-requests/mine"},
	} {
		if w := as(h, rt.method, rt.path, ""); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call; a write without the client header is refused.
	for _, rt := range []struct{ method, path string }{
		{"GET", vehiclesPath}, {"PUT", vehiclesPath + "/mine"}, {"DELETE", vehiclePath},
		{"PUT", vehiclePath + "/seat-requests/mine"}, {"PUT", vehiclePath + "/seat-requests/" + riderID + "/approval"},
		{"DELETE", vehiclePath + "/seat-requests/" + riderID},
	} {
		if w := do(h, rt.method, rt.path, `{"make":"Ford"}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", rt.method, rt.path, w.Code)
		}
		if rt.method == "GET" {
			continue
		}
		if w := do(h, rt.method, rt.path, `{"make":"Ford"}`, map[string]string{"Authorization": "Bearer T"}); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
			t.Fatalf("istemci başlıksız %s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestVehiclesComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_COMPONENT_NOT_FOUND":  {codes.NotFound, http.StatusNotFound, "component-not-found"},
		"ERROR_REASON_COMPONENT_DISABLED":   {codes.FailedPrecondition, http.StatusConflict, "component-disabled"},
		"ERROR_REASON_COMPONENT_FROZEN":     {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_EVENT_NOT_GOING":      {codes.FailedPrecondition, http.StatusConflict, "event-not-going"},
		"ERROR_REASON_EVENT_ITEM_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "event-item-not-found"},
		"ERROR_REASON_VEHICLE_FULL":         {codes.FailedPrecondition, http.StatusConflict, "vehicle-full"},
		"ERROR_REASON_VEHICLE_SEATS_TAKEN":  {codes.FailedPrecondition, http.StatusConflict, "vehicle-seats-taken"},
		"ERROR_REASON_SEAT_REQUEST_DRIVER":  {codes.FailedPrecondition, http.StatusConflict, "seat-request-driver"},
		"ERROR_REASON_PERMISSION_DENIED":    {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{vehiclesComponentService: &fakeVehicles{err: coreErr(want.grpc, reason)}})
		if w := as(h, "PUT", vehiclePath+"/seat-requests/mine", ""); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
