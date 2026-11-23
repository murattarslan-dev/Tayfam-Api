package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/guest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// GuestPlayers is what GuestPlayerComponentServer needs from the app layer
// (guest.Service).
type GuestPlayers interface {
	Get(ctx context.Context, ref component.Ref) (guest.Guests, error)
	Add(ctx context.Context, ref component.Ref, in guest.Input) (guest.Guests, error)
	Remove(ctx context.Context, ref component.Ref, g id.ID) (guest.Guests, error)
}

// GuestPlayerComponentServer implements
// corev1.GuestPlayerComponentServiceServer (issue #252; ADR-0037 §6). Every
// method needs a verified token (auth.Guard); the acting user is always the
// token's. Nicknames are never logged.
type GuestPlayerComponentServer struct {
	corev1.UnimplementedGuestPlayerComponentServiceServer
	app GuestPlayers
	log *slog.Logger
}

// NewGuestPlayerComponentServer returns a GuestPlayerComponentServer.
func NewGuestPlayerComponentServer(app GuestPlayers, log *slog.Logger) *GuestPlayerComponentServer {
	return &GuestPlayerComponentServer{app: app, log: log}
}

// registerGuestPlayers is the guest players' line in the registry
// (componentRegistrars).
func registerGuestPlayers(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterGuestPlayerComponentServiceServer(srv, NewGuestPlayerComponentServer(guest.New(fw), log))
	return nil
}

func guestPlayersPB(g guest.Guests) *corev1.GuestPlayers {
	out := &corev1.GuestPlayers{
		Guests: make([]*corev1.GuestPlayer, 0, len(g.Guests)), MaxGuests: g.MaxGuests,
		CallerCanAdd: g.CallerCanAdd, CallerCanRemove: g.CallerCanRemove,
	}
	for _, v := range g.Guests {
		out.Guests = append(out.Guests, &corev1.GuestPlayer{Id: v.ID.String(), Nickname: v.Nickname, CreateTime: timestamppb.New(v.CreatedAt)})
	}
	return out
}

// ListGuestPlayers implements corev1.GuestPlayerComponentServiceServer.
func (s *GuestPlayerComponentServer) ListGuestPlayers(ctx context.Context, req *corev1.ListGuestPlayersRequest) (*corev1.ListGuestPlayersResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListGuestPlayersResponse{Guests: guestPlayersPB(out)}, nil
}

// AddGuestPlayer implements corev1.GuestPlayerComponentServiceServer.
func (s *GuestPlayerComponentServer) AddGuestPlayer(ctx context.Context, req *corev1.AddGuestPlayerRequest) (*corev1.AddGuestPlayerResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	in := guest.Input{Nickname: req.GetNickname()}
	if t := req.GetTableId(); t != "" {
		var ok bool
		if in.Table, ok = id.Parse(t); !ok {
			return nil, validation("table_id")
		}
	}
	out, err := s.app.Add(ctx, ref, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddGuestPlayerResponse{Guests: guestPlayersPB(out)}, nil
}

// RemoveGuestPlayer implements corev1.GuestPlayerComponentServiceServer.
func (s *GuestPlayerComponentServer) RemoveGuestPlayer(ctx context.Context, req *corev1.RemoveGuestPlayerRequest) (*corev1.RemoveGuestPlayerResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	g, ok := id.Parse(req.GetGuestId())
	if !ok {
		return nil, validation("guest_id")
	}
	out, err := s.app.Remove(ctx, ref, g)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveGuestPlayerResponse{Guests: guestPlayersPB(out)}, nil
}
