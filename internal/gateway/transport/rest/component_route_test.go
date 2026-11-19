package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const routeComponentService = "tayfam.core.v1.RouteComponentService"

// fakeRoutes is core's RouteComponentService for the route tests.
type fakeRoutes struct {
	get   *corev1.GetRouteRequest
	set   *corev1.SetRouteRequest
	token string
	out   *corev1.EventRoute
	err   error
	calls int
}

func (f *fakeRoutes) GetRoute(ctx context.Context, in *corev1.GetRouteRequest, _ ...grpc.CallOption) (*corev1.GetRouteResponse, error) {
	f.get, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.GetRouteResponse{Route: f.out}, f.err
}

func (f *fakeRoutes) SetRoute(ctx context.Context, in *corev1.SetRouteRequest, _ ...grpc.CallOption) (*corev1.SetRouteResponse, error) {
	f.set, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.SetRouteResponse{Route: f.out}, f.err
}

const routePath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/track/route"

func TestRouteComponentRoutes(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	f := &fakeRoutes{out: &corev1.EventRoute{
		Points: []*corev1.LatLng{{Lat: 38.5, Lng: 27.25}, {Lat: 38.75, Lng: 27.5}}, UpdateTime: timestamppb.New(at), CallerCanEdit: true,
	}}
	h := componentRouter(fakeServices{routeComponentService: f})

	w := as(h, "GET", routePath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("okuma: %d %s", w.Code, w.Body)
	}
	if ref := f.get.GetRef(); ref.GetTayfaId() != tayfaID || ref.GetEventId() != eventID || ref.GetComponentKey() != "track" || f.token != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", f.get, f.token)
	}
	got := decodeBody[eventRouteJSON](t, w)
	if len(got.Points) != 2 || got.Points[1] != (latLngJSON{Lat: 38.75, Lng: 27.5}) || got.UpdatedAt == nil || !got.UpdatedAt.Equal(at) || !got.CallerCanEdit {
		t.Fatalf("rota: %+v", got)
	}

	w = as(h, "PUT", routePath, `{"points":[{"lat":1,"lng":2},{"lat":-90,"lng":180}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("yazma: %d %s", w.Code, w.Body)
	}
	if pts := f.set.GetPoints(); f.set.GetRef().GetComponentKey() != "track" || f.set.GetRef().GetEventId() != eventID || len(pts) != 2 ||
		pts[0].GetLat() != 1 || pts[0].GetLng() != 2 || pts[1].GetLat() != -90 || pts[1].GetLng() != 180 {
		t.Fatalf("core'a giden rota: %v", f.set)
	}
	if out := decodeBody[eventRouteJSON](t, w); len(out.Points) != 2 {
		t.Fatalf("yazma yanıtı: %+v", out)
	}

	// No points removes the route; the answer has an empty array and no
	// time.
	f.out = &corev1.EventRoute{}
	w = as(h, "PUT", routePath, `{"points":[]}`)
	if w.Code != http.StatusOK || len(f.set.GetPoints()) != 0 || !strings.Contains(w.Body.String(), `"points":[]`) || strings.Contains(w.Body.String(), "updated_at") {
		t.Fatalf("kaldırma: %d %s", w.Code, w.Body)
	}
	if w := as(h, "GET", routePath, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"points":[]`) || !strings.Contains(w.Body.String(), `"caller_can_edit":false`) {
		t.Fatalf("rotasız okuma: %d %s", w.Code, w.Body)
	}
}

func TestRouteComponentRoutesValidateInput(t *testing.T) {
	f := &fakeRoutes{out: &corev1.EventRoute{}}
	h := componentRouter(fakeServices{routeComponentService: f})
	many := `{"points":[` + strings.Repeat(`{"lat":1,"lng":2},`, routeMaxPoints) + `{"lat":1,"lng":2}]}`

	for name, body := range map[string]string{
		"boş gövde":             `{}`,
		"null noktalar":         `{"points":null}`,
		"tek nokta":             `{"points":[{"lat":1,"lng":2}]}`,
		"501 nokta":             many,
		"enlem aralık dışı":     `{"points":[{"lat":90.5,"lng":2},{"lat":1,"lng":2}]}`,
		"boylam aralık dışı":    `{"points":[{"lat":1,"lng":-180.5},{"lat":1,"lng":2}]}`,
		"metin enlem":           `{"points":[{"lat":"1","lng":2},{"lat":1,"lng":2}]}`,
		"noktada fazla alan":    `{"points":[{"lat":1,"lng":2,"name":"ev"},{"lat":1,"lng":2}]}`,
		"gövdede fazla alan":    `{"points":[{"lat":1,"lng":2},{"lat":1,"lng":2}],"updated_at":"2026-11-19T09:00:00Z"}`,
		"dizi olmayan noktalar": `{"points":{"lat":1,"lng":2}}`,
		"bozuk JSON":            `{"points":[`,
	} {
		if w := as(h, "PUT", routePath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// The path is checked before core is asked.
	for _, path := range []string{
		"/v1/tayfas/x/events/" + eventID + "/components/track/route",
		"/v1/tayfas/" + tayfaID + "/events/x/components/track/route",
		"/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Track/route",
	} {
		if w := as(h, "GET", path, ""); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call.
	for _, m := range []string{"GET", "PUT"} {
		if w := do(h, m, routePath, `{"points":[]}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s: %d", m, w.Code)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestRouteComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_COMPONENT_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "component-not-found"},
		"ERROR_REASON_COMPONENT_DISABLED":  {codes.FailedPrecondition, http.StatusConflict, "component-disabled"},
		"ERROR_REASON_COMPONENT_FROZEN":    {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_PERMISSION_DENIED":   {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{routeComponentService: &fakeRoutes{err: coreErr(want.grpc, reason)}})
		if w := as(h, "PUT", routePath, `{"points":[{"lat":1,"lng":2},{"lat":3,"lng":4}]}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
