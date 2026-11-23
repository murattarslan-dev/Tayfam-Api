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

const garageService = "tayfam.core.v1.MemberGarageService"

// fakeGarage is core's MemberGarageService for the garage tests.
type fakeGarage struct {
	got   proto.Message
	token string
	err   error
	calls int
}

func (f *fakeGarage) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func garageVehiclePB() *corev1.GarageVehicle {
	return &corev1.GarageVehicle{
		Id: itemID, OwnerId: userID, Make: "Anadol", Model: "A1", ModelYear: 1968, Color: "Kırmızı",
		CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now(), CallerCanEdit: true,
	}
}

func (f *fakeGarage) ListMyGarageVehicles(ctx context.Context, in *corev1.ListMyGarageVehiclesRequest, _ ...grpc.CallOption) (*corev1.ListMyGarageVehiclesResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListMyGarageVehiclesResponse{Vehicles: []*corev1.GarageVehicle{garageVehiclePB()}, CallerCanAdd: true}, f.err
}

func (f *fakeGarage) CreateGarageVehicle(ctx context.Context, in *corev1.CreateGarageVehicleRequest, _ ...grpc.CallOption) (*corev1.CreateGarageVehicleResponse, error) {
	f.rec(ctx, in)
	return &corev1.CreateGarageVehicleResponse{Vehicle: garageVehiclePB()}, f.err
}

func (f *fakeGarage) UpdateGarageVehicle(ctx context.Context, in *corev1.UpdateGarageVehicleRequest, _ ...grpc.CallOption) (*corev1.UpdateGarageVehicleResponse, error) {
	f.rec(ctx, in)
	return &corev1.UpdateGarageVehicleResponse{Vehicle: garageVehiclePB()}, f.err
}

func (f *fakeGarage) DeleteGarageVehicle(ctx context.Context, in *corev1.DeleteGarageVehicleRequest, _ ...grpc.CallOption) (*corev1.DeleteGarageVehicleResponse, error) {
	f.rec(ctx, in)
	return &corev1.DeleteGarageVehicleResponse{}, f.err
}

func (f *fakeGarage) ListTayfaGarage(ctx context.Context, in *corev1.ListTayfaGarageRequest, _ ...grpc.CallOption) (*corev1.ListTayfaGarageResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListTayfaGarageResponse{Vehicles: []*corev1.GarageVehicle{garageVehiclePB()}, NextPageToken: "sonraki", CallerVisible: true}, f.err
}

func (f *fakeGarage) SetGarageVisibility(ctx context.Context, in *corev1.SetGarageVisibilityRequest, _ ...grpc.CallOption) (*corev1.SetGarageVisibilityResponse, error) {
	f.rec(ctx, in)
	return &corev1.SetGarageVisibilityResponse{Visible: in.GetVisible()}, f.err
}

func (f *fakeGarage) RemoveTayfaGarageVehicle(ctx context.Context, in *corev1.RemoveTayfaGarageVehicleRequest, _ ...grpc.CallOption) (*corev1.RemoveTayfaGarageVehicleResponse, error) {
	f.rec(ctx, in)
	return &corev1.RemoveTayfaGarageVehicleResponse{}, f.err
}

const (
	myGarageURL        = "/v1/me/garage"
	myGarageVehicleURL = myGarageURL + "/" + itemID
	tayfaGarageURL     = "/v1/tayfas/" + tayfaID + "/garage"
)

func TestGarageRoutes(t *testing.T) {
	f := &fakeGarage{}
	h := componentRouter(fakeServices{garageService: f})
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", myGarageURL, "", http.StatusOK, &corev1.ListMyGarageVehiclesRequest{}},
		{"POST", myGarageURL, `{"make":"Anadol","model":"A1","model_year":1968,"color":"Kırmızı","note":"Babamdan","photo_upload_ids":["` + entryID + `"],"client_request_id":"` + itemID + `"}`,
			http.StatusCreated, &corev1.CreateGarageVehicleRequest{Vehicle: &corev1.GarageVehicleInput{
				Make: "Anadol", Model: "A1", ModelYear: 1968, Color: "Kırmızı", Note: "Babamdan", PhotoUploadIds: []string{entryID},
			}, ClientRequestId: itemID}},
		{"PUT", myGarageVehicleURL, `{"make":"Anadol"}`, http.StatusOK,
			&corev1.UpdateGarageVehicleRequest{VehicleId: itemID, Vehicle: &corev1.GarageVehicleInput{Make: "Anadol"}}},
		{"DELETE", myGarageVehicleURL, "", http.StatusNoContent, &corev1.DeleteGarageVehicleRequest{VehicleId: itemID}},
		{"GET", tayfaGarageURL, "", http.StatusOK, &corev1.ListTayfaGarageRequest{TayfaId: tayfaID}},
		{"GET", tayfaGarageURL + "?owner_user_id=" + userID + "&page_size=5&page_token=abc", "", http.StatusOK,
			&corev1.ListTayfaGarageRequest{TayfaId: tayfaID, OwnerId: userID, PageSize: 5, PageToken: "abc"}},
		{"PUT", tayfaGarageURL + "/visibility", `{"visible":false}`, http.StatusOK, &corev1.SetGarageVisibilityRequest{TayfaId: tayfaID}},
		{"PUT", tayfaGarageURL + "/visibility", `{"visible":true}`, http.StatusOK, &corev1.SetGarageVisibilityRequest{TayfaId: tayfaID, Visible: true}},
		{"DELETE", tayfaGarageURL + "/" + itemID, "", http.StatusNoContent, &corev1.RemoveTayfaGarageVehicleRequest{TayfaId: tayfaID, VehicleId: itemID}},
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

	m := decodeBody[myGarageJSON](t, as(h, "GET", myGarageURL, ""))
	if len(m.Vehicles) != 1 || !m.CallerCanAdd {
		t.Fatalf("garaj: %+v", m)
	}
	v := m.Vehicles[0]
	if v.Make != "Anadol" || v.ModelYear != 1968 || v.OwnerUserID != userID || v.PhotoUploadIDs == nil || v.Note != "" || !v.CallerCanEdit {
		t.Fatalf("araç: %+v", v)
	}
	l := decodeBody[tayfaGarageJSON](t, as(h, "GET", tayfaGarageURL, ""))
	if len(l.Vehicles) != 1 || l.NextPageToken != "sonraki" || !l.CallerVisible {
		t.Fatalf("tayfa garajı: %+v", l)
	}
	// No plate anywhere in the garage's JSON.
	if w := as(h, "GET", myGarageURL, ""); strings.Contains(w.Body.String(), "plate") {
		t.Fatalf("garajda plaka alanı: %s", w.Body)
	}
}

func TestGarageRoutesValidateInput(t *testing.T) {
	f := &fakeGarage{}
	h := componentRouter(fakeServices{garageService: f})
	crid := `,"client_request_id":"` + itemID + `"`
	for _, rt := range []struct{ method, path, body string }{
		{"POST", myGarageURL, `{"make":"Anadol"}`},
		{"POST", myGarageURL, `{"make":"  "` + crid + `}`},
		{"POST", myGarageURL, `{"model":"A1"` + crid + `}`},
		{"POST", myGarageURL, `{"make":"Anadol","plate":"06 AN 1968"` + crid + `}`},
		{"POST", myGarageURL, `{"make":"Anadol","model_year":-1` + crid + `}`},
		{"POST", myGarageURL, `{"make":"Anadol","note":"` + strings.Repeat("a", 1025) + `"` + crid + `}`},
		{"POST", myGarageURL, `{"make":"Anadol","photo_upload_ids":["x"]` + crid + `}`},
		{"POST", myGarageURL, `{"make":"Anadol","photo_upload_ids":["` + entryID + `","` + entryID + `","` + entryID + `","` + entryID + `"]` + crid + `}`},
		{"PUT", myGarageURL + "/x", `{"make":"Anadol"}`},
		{"PUT", myGarageVehicleURL, `{"make":"Anadol","client_request_id":"` + itemID + `"}`},
		{"DELETE", myGarageURL + "/x", ""},
		{"GET", "/v1/tayfas/x/garage", ""},
		{"GET", tayfaGarageURL + "?owner_user_id=x", ""},
		{"GET", tayfaGarageURL + "?page_size=101", ""},
		{"PUT", tayfaGarageURL + "/visibility", `{}`},
		{"PUT", tayfaGarageURL + "/visibility", `{"visible":"evet"}`},
		{"DELETE", tayfaGarageURL + "/x", ""},
	} {
		if w := as(h, rt.method, rt.path, rt.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	for _, rt := range []struct{ method, path string }{
		{"GET", myGarageURL}, {"POST", myGarageURL}, {"PUT", myGarageVehicleURL}, {"DELETE", myGarageVehicleURL},
		{"GET", tayfaGarageURL}, {"PUT", tayfaGarageURL + "/visibility"}, {"DELETE", tayfaGarageURL + "/" + itemID},
	} {
		if w := do(h, rt.method, rt.path, `{}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", rt.method, rt.path, w.Code)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestGarageErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_GARAGE_VEHICLE_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "garage-vehicle-not-found"},
		"ERROR_REASON_GARAGE_LIMIT":             {codes.FailedPrecondition, http.StatusConflict, "garage-limit"},
	} {
		h := componentRouter(fakeServices{garageService: &fakeGarage{err: coreErr(want.grpc, reason)}})
		body := `{"make":"Anadol","client_request_id":"` + itemID + `"}`
		if w := as(h, "POST", myGarageURL, body); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
