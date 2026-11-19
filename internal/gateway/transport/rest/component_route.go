package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The route component (kind "route", issue #239; ADR-0025 §4). core decides
// everything; the gateway only keeps junk out early. Coordinates travel in
// bodies only and are never logged (ADR-0008).

const (
	// maxRouteBody fits routeMaxPoints points written with full float
	// precision.
	maxRouteBody = 64 << 10
	// routeMaxPoints is route.MaxPoints in core.
	routeMaxPoints = 500
)

// registerRouteComponent is the route component's line in the registry
// (componentRoutes).
func registerRouteComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "route"), h.authed(h.getRoute))
	mux.HandleFunc(componentPath("PUT", "route"), h.authed(h.setRoute))
}

func (h componentHandlers) routes() corev1.RouteComponentServiceClient {
	return corev1.NewRouteComponentServiceClient(h.conn)
}

type eventRouteJSON struct {
	Points        []latLngJSON `json:"points"`
	UpdatedAt     *time.Time   `json:"updated_at,omitempty"`
	CallerCanEdit bool         `json:"caller_can_edit"`
}

func eventRouteFrom(r *corev1.EventRoute) eventRouteJSON {
	out := eventRouteJSON{
		Points: make([]latLngJSON, 0, len(r.GetPoints())), UpdatedAt: optTime(r.GetUpdateTime()), CallerCanEdit: r.GetCallerCanEdit(),
	}
	for _, p := range r.GetPoints() {
		out.Points = append(out.Points, latLngJSON{Lat: p.GetLat(), Lng: p.GetLng()})
	}
	return out
}

func (h componentHandlers) getRoute(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.routes().GetRoute(ctx, &corev1.GetRouteRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventRouteFrom(resp.GetRoute()))
}

// eventRouteBody is the body of a route change. Points is a pointer so that
// a body without it is refused rather than read as "remove the route".
type eventRouteBody struct {
	Points *[]latLngJSON `json:"points"`
}

func (h componentHandlers) setRoute(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b eventRouteBody
	if !decodeLimit(w, r, &b, maxRouteBody) {
		return
	}
	if b.Points == nil || len(*b.Points) == 1 || len(*b.Points) > routeMaxPoints {
		badRequest(w, r)
		return
	}
	req := &corev1.SetRouteRequest{Ref: ref, Points: make([]*corev1.LatLng, 0, len(*b.Points))}
	for _, p := range *b.Points {
		if !p.valid() {
			badRequest(w, r)
			return
		}
		req.Points = append(req.Points, &corev1.LatLng{Lat: p.Lat, Lng: p.Lng})
	}
	resp, err := h.routes().SetRoute(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventRouteFrom(resp.GetRoute()))
}
