package grpc

import (
	"context"
	"log/slog"
	"math"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/route"
)

// Routes is what RouteComponentServer needs from the app layer
// (route.Service).
type Routes interface {
	Get(ctx context.Context, ref component.Ref) (route.Route, error)
	Set(ctx context.Context, ref component.Ref, points []route.Point) (route.Route, error)
}

// RouteComponentServer implements corev1.RouteComponentServiceServer (issue
// #239; ADR-0025 §4). Every method needs a verified token (auth.Guard); the
// acting user is always the token's. Points are never logged.
type RouteComponentServer struct {
	corev1.UnimplementedRouteComponentServiceServer
	app Routes
	log *slog.Logger
}

// NewRouteComponentServer returns a RouteComponentServer.
func NewRouteComponentServer(app Routes, log *slog.Logger) *RouteComponentServer {
	return &RouteComponentServer{app: app, log: log}
}

// registerRouteComponent is the route component's line in the registry
// (componentRegistrars).
func registerRouteComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterRouteComponentServiceServer(srv, NewRouteComponentServer(route.New(fw), log))
	return nil
}

func eventRoutePB(r route.Route) *corev1.EventRoute {
	out := &corev1.EventRoute{Points: make([]*corev1.LatLng, 0, len(r.Points)), CallerCanEdit: r.CallerCanEdit}
	for _, p := range r.Points {
		out.Points = append(out.Points, &corev1.LatLng{Lat: p.Lat, Lng: p.Lng})
	}
	if !r.UpdatedAt.IsZero() {
		out.UpdateTime = timestamppb.New(r.UpdatedAt)
	}
	return out
}

// GetRoute implements corev1.RouteComponentServiceServer.
func (s *RouteComponentServer) GetRoute(ctx context.Context, req *corev1.GetRouteRequest) (*corev1.GetRouteResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	r, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetRouteResponse{Route: eventRoutePB(r)}, nil
}

// SetRoute implements corev1.RouteComponentServiceServer.
func (s *RouteComponentServer) SetRoute(ctx context.Context, req *corev1.SetRouteRequest) (*corev1.SetRouteResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if len(req.GetPoints()) > route.MaxPoints {
		return nil, validation("points")
	}
	pts := make([]route.Point, 0, len(req.GetPoints()))
	for _, p := range req.GetPoints() {
		// A missing point is not the point (0, 0).
		if p == nil || math.IsInf(p.GetLat(), 0) || math.IsInf(p.GetLng(), 0) {
			return nil, validation("points")
		}
		pt := route.Point{Lat: p.GetLat(), Lng: p.GetLng()}
		if !pt.Valid() {
			return nil, validation("points")
		}
		pts = append(pts, pt)
	}
	r, err := s.app.Set(ctx, ref, pts)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetRouteResponse{Route: eventRoutePB(r)}, nil
}
