package rest

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// fakeServices is a connection to core for the component routes in tests:
// a gRPC service's full name ("tayfam.core.v1.RouteComponentService") to a
// fake with that service's client methods. A component's test builds a
// router with a fake of its own service (componentRouter); nothing else has
// to change for it.
type fakeServices map[string]any

// Invoke implements grpc.ClientConnInterface: it calls the fake's method
// named as the RPC, with ctx and the request.
func (s fakeServices) Invoke(ctx context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	service, name, ok := strings.Cut(strings.TrimPrefix(method, "/"), "/")
	fake, known := s[service]
	if !ok || !known {
		return status.Error(codes.Unimplemented, "test: servis yok: "+method)
	}
	m := reflect.ValueOf(fake).MethodByName(name)
	if !m.IsValid() {
		return status.Error(codes.Unimplemented, "test: metod yok: "+method)
	}
	out := m.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(args)})
	if err, _ := out[1].Interface().(error); err != nil {
		return err
	}
	proto.Merge(reply.(proto.Message), out[0].Interface().(proto.Message))
	return nil
}

// NewStream implements grpc.ClientConnInterface; no component streams.
func (s fakeServices) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("test: akış yok")
}

// componentRouter returns a router whose component routes call the given
// fakes; every other core route answers from a fresh fakeCore.
func componentRouter(services fakeServices) http.Handler {
	d := testCoreDeps(&fakeCore{}, 100)
	d.Components = services
	return coreRouterWith(d)
}

// --- A test component: the pattern of a component's routes ---

// probeService is a made-up component service with one method.
const probeService = "tayfam.core.v1.ProbeComponentService"

type fakeProbe struct {
	got   *corev1.ComponentRef
	token string
	err   error
}

// GetProbe answers with the reference it was called with.
func (f *fakeProbe) GetProbe(ctx context.Context, in *corev1.ComponentRef, _ ...grpc.CallOption) (*corev1.ComponentRef, error) {
	f.got, f.token = in, tokenFrom(ctx)
	return in, f.err
}

// registerProbeComponent is what a component's file adds: its routes under
// componentPath, the reference from componentRef, its own client from the
// shared connection.
func registerProbeComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "probe"), h.authed(func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		ref, ok := componentRef(w, r)
		if !ok {
			return
		}
		out := new(corev1.ComponentRef)
		if err := h.conn.Invoke(ctx, "/"+probeService+"/GetProbe", ref, out); err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"component_key": out.GetComponentKey()})
	}))
}

const probePath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/"

func TestComponentRoutePattern(t *testing.T) {
	probe := &fakeProbe{}
	d := testCoreDeps(&fakeCore{}, 100)
	d.Components = fakeServices{probeService: probe}
	mux := http.NewServeMux()
	registerProbeComponent(mux, componentHandlers{coreHandlers: coreHandlers{d: d}, conn: d.Components})

	w := as(mux, "GET", probePath+"gear/probe", "")
	if w.Code != http.StatusOK || decodeBody[map[string]string](t, w)["component_key"] != "gear" {
		t.Fatalf("bileşen ucu: %d %s", w.Code, w.Body)
	}
	if probe.got.GetTayfaId() != tayfaID || probe.got.GetEventId() != eventID || probe.got.GetComponentKey() != "gear" || probe.token != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", probe.got, probe.token)
	}
	// The path is checked before core is asked.
	probe.got = nil
	for _, path := range []string{
		"/v1/tayfas/x/events/" + eventID + "/components/gear/probe",
		"/v1/tayfas/" + tayfaID + "/events/x/components/gear/probe",
		probePath + "Gear/probe",
		probePath + "g/probe",
		probePath + "gear%20x/probe",
	} {
		if w := as(mux, "GET", path, ""); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" || probe.got != nil {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	// No token, no call.
	if w := do(mux, "GET", probePath+"gear/probe", "", map[string]string{clientHeader: "android"}); w.Code != http.StatusUnauthorized || probe.got != nil {
		t.Fatalf("token'sız: %d", w.Code)
	}
	// The framework's errors reach the client as their problem types.
	for reason, want := range map[string]struct {
		code int
		typ  string
	}{
		"ERROR_REASON_COMPONENT_NOT_FOUND": {http.StatusNotFound, "component-not-found"},
		"ERROR_REASON_COMPONENT_DISABLED":  {http.StatusConflict, "component-disabled"},
		"ERROR_REASON_COMPONENT_LOCKED":    {http.StatusConflict, "component-locked"},
		"ERROR_REASON_COMPONENT_FROZEN":    {http.StatusConflict, "component-frozen"},
	} {
		c := codes.FailedPrecondition
		if want.code == http.StatusNotFound {
			c = codes.NotFound
		}
		probe.err = coreErr(c, reason)
		if w := as(mux, "GET", probePath+"gear/probe", ""); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}

// Without the connection to core no component route is served.
func TestComponentRoutesNeedConnection(t *testing.T) {
	d := testCoreDeps(&fakeCore{}, 100)
	d.Components = nil
	h := coreRouterWith(d)
	if w := as(h, "GET", "/v1/tayfas/"+tayfaID+"/events/"+eventID+"/components", ""); w.Code != http.StatusNotFound {
		t.Fatalf("bağlantısız: %d", w.Code)
	}
	// A service the connection does not know is an upstream failure, not a
	// success.
	h = componentRouter(fakeServices{})
	if w := as(h, "GET", "/v1/tayfas/"+tayfaID+"/events/"+eventID+"/components", ""); w.Code < 500 {
		t.Fatalf("bilinmeyen servis: %d %s", w.Code, w.Body)
	}
}

// Every pattern in the registry is distinct: a duplicate would panic when
// the router is built.
func TestComponentRegistryBuilds(t *testing.T) {
	mux := http.NewServeMux()
	registerComponents(mux, coreHandlers{d: testCoreDeps(&fakeCore{}, 100)})
	if len(componentRoutes()) == 0 {
		t.Fatal("kayıt boş")
	}
}
