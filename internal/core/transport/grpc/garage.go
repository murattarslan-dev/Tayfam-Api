package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/garage"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Garage is what MemberGarageServer needs from the app layer
// (garage.Service).
type Garage interface {
	Mine(ctx context.Context, user id.ID) (garage.Mine, error)
	Create(ctx context.Context, user id.ID, in garage.Input, clientRequestID string) (garage.Vehicle, error)
	Update(ctx context.Context, user, vehicle id.ID, in garage.Input) (garage.Vehicle, error)
	Delete(ctx context.Context, user, vehicle id.ID) error
	List(ctx context.Context, user, tayfa id.ID, f garage.Filter) (garage.Page, error)
	SetVisibility(ctx context.Context, user, tayfa id.ID, visible bool) (bool, error)
	Remove(ctx context.Context, user, tayfa, vehicle id.ID) error
}

// MemberGarageServer implements corev1.MemberGarageServiceServer (issue
// #272; ADR-0040 §4). Every method needs a verified token (auth.Guard); the
// acting user is always the token's. Texts are never logged.
type MemberGarageServer struct {
	corev1.UnimplementedMemberGarageServiceServer
	app Garage
	log *slog.Logger
}

// NewMemberGarageServer returns a MemberGarageServer.
func NewMemberGarageServer(app Garage, log *slog.Logger) *MemberGarageServer {
	return &MemberGarageServer{app: app, log: log}
}

// registerMemberGarage is the member garage's line in the registry
// (componentRegistrars): the garage is the person's data on the framework.
func registerMemberGarage(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterMemberGarageServiceServer(srv, NewMemberGarageServer(garage.New(fw), log))
	return nil
}

func garageVehiclePB(v garage.Vehicle) *corev1.GarageVehicle {
	return &corev1.GarageVehicle{
		Id: v.ID.String(), OwnerId: v.Owner.String(), Make: v.Make, Model: v.Model, ModelYear: v.ModelYear,
		Color: v.Color, Note: v.Note, PhotoUploadIds: idStrings(v.Photos),
		CreateTime: timestamppb.New(v.CreatedAt), UpdateTime: timestamppb.New(v.UpdatedAt),
		CallerCanEdit: v.CallerCanEdit, CallerCanRemove: v.CallerCanRemove,
	}
}

// garageInput parses a vehicle's input; how many photos is the app's to
// judge within the bound.
func garageInput(in *corev1.GarageVehicleInput) (garage.Input, error) {
	raw := in.GetPhotoUploadIds()
	if len(raw) > garage.MaxPhotos {
		return garage.Input{}, validation(garage.FieldPhotoIDs)
	}
	photos := make([]id.ID, 0, len(raw))
	for _, r := range raw {
		p, err := parseID(r, garage.FieldPhotoIDs)
		if err != nil {
			return garage.Input{}, err
		}
		photos = append(photos, p)
	}
	return garage.Input{
		Make: in.GetMake(), Model: in.GetModel(), ModelYear: in.GetModelYear(), Color: in.GetColor(), Note: in.GetNote(),
		Photos: photos,
	}, nil
}

// ListMyGarageVehicles implements corev1.MemberGarageServiceServer.
func (s *MemberGarageServer) ListMyGarageVehicles(ctx context.Context, _ *corev1.ListMyGarageVehiclesRequest) (*corev1.ListMyGarageVehiclesResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.app.Mine(ctx, user)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListMyGarageVehiclesResponse{Vehicles: make([]*corev1.GarageVehicle, 0, len(m.Vehicles)), CallerCanAdd: m.CallerCanAdd}
	for _, v := range m.Vehicles {
		out.Vehicles = append(out.Vehicles, garageVehiclePB(v))
	}
	return out, nil
}

// CreateGarageVehicle implements corev1.MemberGarageServiceServer.
func (s *MemberGarageServer) CreateGarageVehicle(ctx context.Context, req *corev1.CreateGarageVehicleRequest) (*corev1.CreateGarageVehicleResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	in, err := garageInput(req.GetVehicle())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Create(ctx, user, in, req.GetClientRequestId())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateGarageVehicleResponse{Vehicle: garageVehiclePB(v)}, nil
}

// UpdateGarageVehicle implements corev1.MemberGarageServiceServer.
func (s *MemberGarageServer) UpdateGarageVehicle(ctx context.Context, req *corev1.UpdateGarageVehicleRequest) (*corev1.UpdateGarageVehicleResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	vehicle, err := parseID(req.GetVehicleId(), garage.FieldVehicleID)
	if err != nil {
		return nil, err
	}
	in, err := garageInput(req.GetVehicle())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Update(ctx, user, vehicle, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateGarageVehicleResponse{Vehicle: garageVehiclePB(v)}, nil
}

// DeleteGarageVehicle implements corev1.MemberGarageServiceServer.
func (s *MemberGarageServer) DeleteGarageVehicle(ctx context.Context, req *corev1.DeleteGarageVehicleRequest) (*corev1.DeleteGarageVehicleResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	vehicle, err := parseID(req.GetVehicleId(), garage.FieldVehicleID)
	if err != nil {
		return nil, err
	}
	if err := s.app.Delete(ctx, user, vehicle); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteGarageVehicleResponse{}, nil
}

// ListTayfaGarage implements corev1.MemberGarageServiceServer.
func (s *MemberGarageServer) ListTayfaGarage(ctx context.Context, req *corev1.ListTayfaGarageRequest) (*corev1.ListTayfaGarageResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	f := garage.Filter{PageSize: req.GetPageSize(), PageToken: req.GetPageToken()}
	if o := req.GetOwnerId(); o != "" {
		if f.Owner, err = parseID(o, garage.FieldOwnerID); err != nil {
			return nil, err
		}
	}
	p, err := s.app.List(ctx, user, t, f)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListTayfaGarageResponse{
		Vehicles: make([]*corev1.GarageVehicle, 0, len(p.Vehicles)), NextPageToken: p.NextPageToken, CallerVisible: p.CallerVisible,
	}
	for _, v := range p.Vehicles {
		out.Vehicles = append(out.Vehicles, garageVehiclePB(v))
	}
	return out, nil
}

// SetGarageVisibility implements corev1.MemberGarageServiceServer.
func (s *MemberGarageServer) SetGarageVisibility(ctx context.Context, req *corev1.SetGarageVisibilityRequest) (*corev1.SetGarageVisibilityResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	visible, err := s.app.SetVisibility(ctx, user, t, req.GetVisible())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetGarageVisibilityResponse{Visible: visible}, nil
}

// RemoveTayfaGarageVehicle implements corev1.MemberGarageServiceServer.
func (s *MemberGarageServer) RemoveTayfaGarageVehicle(ctx context.Context, req *corev1.RemoveTayfaGarageVehicleRequest) (*corev1.RemoveTayfaGarageVehicleResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	vehicle, err := parseID(req.GetVehicleId(), garage.FieldVehicleID)
	if err != nil {
		return nil, err
	}
	if err := s.app.Remove(ctx, user, t, vehicle); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveTayfaGarageVehicleResponse{}, nil
}
