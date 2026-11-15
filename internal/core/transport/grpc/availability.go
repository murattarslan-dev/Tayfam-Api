package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/availability"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Availabilities is what AvailabilityServer needs from the app layer
// (availability.Service).
type Availabilities interface {
	ListMine(ctx context.Context, caller id.ID) ([]availability.Day, error)
	Set(ctx context.Context, caller id.ID, date string, slots []availability.Slot, note string) (availability.Day, error)
	Delete(ctx context.Context, caller id.ID, date string) error
	ListUser(ctx context.Context, caller, target id.ID) ([]availability.Day, error)
}

// AvailabilityServer implements corev1.AvailabilityServiceServer ("Boşum",
// issue #207, ADR-0028). Every method needs a verified token (auth.Guard);
// the caller always comes from it. Notes, dates and slots are never logged.
type AvailabilityServer struct {
	corev1.UnimplementedAvailabilityServiceServer
	app Availabilities
	log *slog.Logger
}

// NewAvailabilityServer returns an AvailabilityServer.
func NewAvailabilityServer(app Availabilities, log *slog.Logger) *AvailabilityServer {
	return &AvailabilityServer{app: app, log: log}
}

var daySlots = map[availability.Slot]corev1.DaySlot{
	availability.SlotMorning:   corev1.DaySlot_DAY_SLOT_MORNING,
	availability.SlotAfternoon: corev1.DaySlot_DAY_SLOT_AFTERNOON,
	availability.SlotEvening:   corev1.DaySlot_DAY_SLOT_EVENING,
	availability.SlotAllDay:    corev1.DaySlot_DAY_SLOT_ALL_DAY,
}

var daySlotsFromPB = func() map[corev1.DaySlot]availability.Slot {
	out := make(map[corev1.DaySlot]availability.Slot, len(daySlots))
	for k, v := range daySlots {
		out[v] = k
	}
	return out
}()

// ListMyAvailability implements corev1.AvailabilityServiceServer.
func (s *AvailabilityServer) ListMyAvailability(ctx context.Context, _ *corev1.ListMyAvailabilityRequest) (*corev1.ListMyAvailabilityResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	days, err := s.app.ListMine(ctx, u)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListMyAvailabilityResponse{Days: daysPB(days)}, nil
}

// SetAvailability implements corev1.AvailabilityServiceServer.
func (s *AvailabilityServer) SetAvailability(ctx context.Context, req *corev1.SetAvailabilityRequest) (*corev1.SetAvailabilityResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	slots := make([]availability.Slot, 0, len(req.GetSlots()))
	for _, pb := range req.GetSlots() {
		sl, ok := daySlotsFromPB[pb]
		if !ok {
			return nil, validation("slots")
		}
		slots = append(slots, sl)
	}
	d, err := s.app.Set(ctx, u, req.GetDate(), slots, req.GetNote())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetAvailabilityResponse{Day: dayPB(d, true)}, nil
}

// DeleteAvailability implements corev1.AvailabilityServiceServer.
func (s *AvailabilityServer) DeleteAvailability(ctx context.Context, req *corev1.DeleteAvailabilityRequest) (*corev1.DeleteAvailabilityResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.app.Delete(ctx, u, req.GetDate()); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteAvailabilityResponse{}, nil
}

// ListUserAvailability implements corev1.AvailabilityServiceServer.
func (s *AvailabilityServer) ListUserAvailability(ctx context.Context, req *corev1.ListUserAvailabilityRequest) (*corev1.ListUserAvailabilityResponse, error) {
	u, target, err := userArgs(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	days, err := s.app.ListUser(ctx, u, target)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListUserAvailabilityResponse{Days: daysPB(days)}, nil
}

func daysPB(days []availability.Day) []*corev1.AvailabilityDay {
	out := make([]*corev1.AvailabilityDay, 0, len(days))
	for _, d := range days {
		out = append(out, dayPB(d, true))
	}
	return out
}

// dayPB converts a day; withNote false leaves the note and the update time
// out (the friend list badge).
func dayPB(d availability.Day, withNote bool) *corev1.AvailabilityDay {
	out := &corev1.AvailabilityDay{Date: d.Date.Format(availability.DateLayout), Slots: slotsPB(d.Slots)}
	if withNote {
		out.Note = d.Note
		out.UpdateTime = timestamppb.New(d.UpdatedAt)
	}
	return out
}

func slotsPB(in []availability.Slot) []corev1.DaySlot {
	out := make([]corev1.DaySlot, 0, len(in))
	for _, s := range in {
		if pb, ok := daySlots[s]; ok {
			out = append(out, pb)
		}
	}
	return out
}
