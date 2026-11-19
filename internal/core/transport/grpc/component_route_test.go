package grpc

import (
	"context"
	"math"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/route"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeRoutes struct {
	err   error
	ref   component.Ref
	pts   []route.Point
	out   route.Route
	calls int
}

func (f *fakeRoutes) Get(_ context.Context, ref component.Ref) (route.Route, error) {
	f.ref, f.calls = ref, f.calls+1
	return f.out, f.err
}

func (f *fakeRoutes) Set(_ context.Context, ref component.Ref, points []route.Point) (route.Route, error) {
	f.ref, f.pts, f.calls = ref, points, f.calls+1
	return f.out, f.err
}

func TestRouteComponentMethods(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	f := &fakeRoutes{out: route.Route{Points: []route.Point{{Lat: 38.5, Lng: 27.25}, {Lat: 38.75, Lng: 27.5}}, UpdatedAt: at, CallerCanEdit: true}}
	s := NewRouteComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "track"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "track"}

	got, err := s.GetRoute(ctx, &corev1.GetRouteRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if r := got.GetRoute(); f.ref != want || len(r.GetPoints()) != 2 || r.GetPoints()[1].GetLat() != 38.75 || r.GetPoints()[1].GetLng() != 27.5 ||
		!r.GetUpdateTime().AsTime().Equal(at) || !r.GetCallerCanEdit() {
		t.Fatalf("okuma: %v (%+v)", r, f.ref)
	}

	f.ref = component.Ref{}
	set, err := s.SetRoute(ctx, &corev1.SetRouteRequest{Ref: ref, Points: []*corev1.LatLng{{Lat: 1, Lng: 2}, {Lat: -90, Lng: 180}}})
	if err != nil {
		t.Fatal(err)
	}
	if f.ref != want || len(f.pts) != 2 || f.pts[0] != (route.Point{Lat: 1, Lng: 2}) || f.pts[1] != (route.Point{Lat: -90, Lng: 180}) || len(set.GetRoute().GetPoints()) != 2 {
		t.Fatalf("yazma: %+v %+v", f.ref, f.pts)
	}
	// No points removes the route; without a route there is no time.
	f.out = route.Route{Points: []route.Point{}}
	set, err = s.SetRoute(ctx, &corev1.SetRouteRequest{Ref: ref})
	if err != nil || f.pts == nil || len(f.pts) != 0 || set.GetRoute().GetUpdateTime() != nil || len(set.GetRoute().GetPoints()) != 0 || set.GetRoute().GetCallerCanEdit() {
		t.Fatalf("kaldırma: %+v %v %v", f.pts, set, err)
	}

	// The app's errors keep their reasons.
	f.err = apperr.ErrComponentFrozen
	if _, err := s.SetRoute(ctx, &corev1.SetRouteRequest{Ref: ref}); codeOf(t, err) != codes.FailedPrecondition {
		t.Fatalf("donmuş bileşen: %v", err)
	}
	f.err = apperr.ErrComponentNotFound
	if _, err := s.GetRoute(ctx, &corev1.GetRouteRequest{Ref: ref}); codeOf(t, err) != codes.NotFound {
		t.Fatalf("olmayan bileşen: %v", err)
	}
}

// Junk is refused before the app layer is asked.
func TestRouteComponentValidation(t *testing.T) {
	f := &fakeRoutes{}
	s := NewRouteComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "route"}
	two := []*corev1.LatLng{{Lat: 1, Lng: 2}, {Lat: 3, Lng: 4}}
	many := make([]*corev1.LatLng, route.MaxPoints+1)
	for i := range many {
		many[i] = &corev1.LatLng{Lat: 1, Lng: 2}
	}

	for name, req := range map[string]*corev1.SetRouteRequest{
		"başvurusuz":         {Points: two},
		"bozuk tayfa":        {Ref: &corev1.ComponentRef{TayfaId: "x", EventId: ev, ComponentKey: "route"}, Points: two},
		"bozuk etkinlik":     {Ref: &corev1.ComponentRef{TayfaId: tf, EventId: "x", ComponentKey: "route"}, Points: two},
		"bozuk anahtar":      {Ref: &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "Route"}, Points: two},
		"501 nokta":          {Ref: ref, Points: many},
		"eksik nokta":        {Ref: ref, Points: []*corev1.LatLng{{Lat: 1, Lng: 2}, nil}},
		"NaN enlem":          {Ref: ref, Points: []*corev1.LatLng{{Lat: math.NaN(), Lng: 2}, {Lat: 1, Lng: 2}}},
		"sonsuz boylam":      {Ref: ref, Points: []*corev1.LatLng{{Lat: 1, Lng: math.Inf(-1)}, {Lat: 1, Lng: 2}}},
		"enlem aralık dışı":  {Ref: ref, Points: []*corev1.LatLng{{Lat: 90.5, Lng: 2}, {Lat: 1, Lng: 2}}},
		"boylam aralık dışı": {Ref: ref, Points: []*corev1.LatLng{{Lat: 1, Lng: 180.5}, {Lat: 1, Lng: 2}}},
	} {
		if _, err := s.SetRoute(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.GetRoute(ctx, &corev1.GetRouteRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("başvurusuz okuma: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.SetRoute(context.Background(), &corev1.SetRouteRequest{Ref: ref, Points: two}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the route component.
func TestRouteComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.RouteComponentService"]
	if !ok || len(info.Methods) != 2 {
		t.Fatalf("rota servisi: %v", info)
	}
}
