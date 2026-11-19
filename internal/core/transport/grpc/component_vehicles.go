package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/vehicles"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Vehicles is what VehiclesComponentServer needs from the app layer
// (vehicles.Service).
type Vehicles interface {
	List(ctx context.Context, ref component.Ref) (vehicles.Vehicles, error)
	Set(ctx context.Context, ref component.Ref, in vehicles.Input) (vehicles.Vehicles, error)
	Remove(ctx context.Context, ref component.Ref, vehicle id.ID) (vehicles.Vehicles, error)
	RequestSeat(ctx context.Context, ref component.Ref, vehicle id.ID) (vehicles.Vehicles, error)
	ApproveSeat(ctx context.Context, ref component.Ref, vehicle, rider id.ID) (vehicles.Vehicles, error)
	ReleaseSeat(ctx context.Context, ref component.Ref, vehicle, rider id.ID) (vehicles.Vehicles, error)
}

// VehiclesComponentServer implements corev1.VehiclesComponentServiceServer
// (issue #240; ADR-0025 §4, ADR-0037 §3). Every method needs a verified
// token (auth.Guard); the acting user is always the token's. Plates are
// converted here and never logged.
type VehiclesComponentServer struct {
	corev1.UnimplementedVehiclesComponentServiceServer
	app Vehicles
	log *slog.Logger
}

// NewVehiclesComponentServer returns a VehiclesComponentServer.
func NewVehiclesComponentServer(app Vehicles, log *slog.Logger) *VehiclesComponentServer {
	return &VehiclesComponentServer{app: app, log: log}
}

// registerVehiclesComponent is the vehicles component's line in the
// registry (componentRegistrars).
func registerVehiclesComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterVehiclesComponentServiceServer(srv, NewVehiclesComponentServer(vehicles.New(fw), log))
	return nil
}

func userIDs(in []id.ID) []string {
	out := make([]string, 0, len(in))
	for _, u := range in {
		out = append(out, u.String())
	}
	return out
}

func eventVehiclesPB(v vehicles.Vehicles) *corev1.EventVehicles {
	out := &corev1.EventVehicles{
		Vehicles: make([]*corev1.EventVehicle, 0, len(v.Items)), PlateEnabled: v.PlateEnabled, SeatsEnabled: v.SeatsEnabled,
		CallerCanParticipate: v.CallerCanParticipate, CallerCanManage: v.CallerCanManage,
	}
	for _, it := range v.Items {
		out.Vehicles = append(out.Vehicles, &corev1.EventVehicle{
			Id: it.ID.String(), UserId: it.UserID.String(), Make: it.Make, Model: it.Model, ModelYear: it.ModelYear,
			Color: it.Color, Plate: it.Plate, Seats: it.Seats,
			RiderUserIds: userIDs(it.Riders), PendingUserIds: userIDs(it.Pending),
		})
	}
	return out
}

// vehicleRef reads the component and the vehicle of a request.
func vehicleRef(ctx context.Context, ref *corev1.ComponentRef, vehicleID string) (component.Ref, id.ID, error) {
	r, err := componentRef(ctx, ref)
	if err != nil {
		return component.Ref{}, id.ID{}, err
	}
	v, ok := id.Parse(vehicleID)
	if !ok {
		return component.Ref{}, id.ID{}, validation("vehicle_id")
	}
	return r, v, nil
}

// seatRef reads the component, the vehicle and the requester of a request.
func seatRef(ctx context.Context, ref *corev1.ComponentRef, vehicleID, userID string) (component.Ref, id.ID, id.ID, error) {
	r, v, err := vehicleRef(ctx, ref, vehicleID)
	if err != nil {
		return component.Ref{}, id.ID{}, id.ID{}, err
	}
	u, ok := id.Parse(userID)
	if !ok {
		return component.Ref{}, id.ID{}, id.ID{}, validation("user_id")
	}
	return r, v, u, nil
}

// ListVehicles implements corev1.VehiclesComponentServiceServer.
func (s *VehiclesComponentServer) ListVehicles(ctx context.Context, req *corev1.ListVehiclesRequest) (*corev1.ListVehiclesResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.List(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListVehiclesResponse{Vehicles: eventVehiclesPB(out)}, nil
}

// SetVehicle implements corev1.VehiclesComponentServiceServer.
func (s *VehiclesComponentServer) SetVehicle(ctx context.Context, req *corev1.SetVehicleRequest) (*corev1.SetVehicleResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if req.GetSeats() < 0 || req.GetSeats() > vehicles.MaxSeats {
		return nil, validation("seats")
	}
	out, err := s.app.Set(ctx, ref, vehicles.Input{
		Make: req.GetMake(), Model: req.GetModel(), ModelYear: req.GetModelYear(), Color: req.GetColor(),
		Plate: req.GetPlate(), Seats: req.GetSeats(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetVehicleResponse{Vehicles: eventVehiclesPB(out)}, nil
}

// RemoveVehicle implements corev1.VehiclesComponentServiceServer.
func (s *VehiclesComponentServer) RemoveVehicle(ctx context.Context, req *corev1.RemoveVehicleRequest) (*corev1.RemoveVehicleResponse, error) {
	ref, v, err := vehicleRef(ctx, req.GetRef(), req.GetVehicleId())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Remove(ctx, ref, v)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveVehicleResponse{Vehicles: eventVehiclesPB(out)}, nil
}

// RequestSeat implements corev1.VehiclesComponentServiceServer.
func (s *VehiclesComponentServer) RequestSeat(ctx context.Context, req *corev1.RequestSeatRequest) (*corev1.RequestSeatResponse, error) {
	ref, v, err := vehicleRef(ctx, req.GetRef(), req.GetVehicleId())
	if err != nil {
		return nil, err
	}
	out, err := s.app.RequestSeat(ctx, ref, v)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RequestSeatResponse{Vehicles: eventVehiclesPB(out)}, nil
}

// ApproveSeat implements corev1.VehiclesComponentServiceServer.
func (s *VehiclesComponentServer) ApproveSeat(ctx context.Context, req *corev1.ApproveSeatRequest) (*corev1.ApproveSeatResponse, error) {
	ref, v, rider, err := seatRef(ctx, req.GetRef(), req.GetVehicleId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	out, err := s.app.ApproveSeat(ctx, ref, v, rider)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ApproveSeatResponse{Vehicles: eventVehiclesPB(out)}, nil
}

// ReleaseSeat implements corev1.VehiclesComponentServiceServer.
func (s *VehiclesComponentServer) ReleaseSeat(ctx context.Context, req *corev1.ReleaseSeatRequest) (*corev1.ReleaseSeatResponse, error) {
	ref, v, rider, err := seatRef(ctx, req.GetRef(), req.GetVehicleId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	out, err := s.app.ReleaseSeat(ctx, ref, v, rider)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ReleaseSeatResponse{Vehicles: eventVehiclesPB(out)}, nil
}
