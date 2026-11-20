package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/schedule"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Schedule is what ScheduleComponentServer needs from the app layer
// (schedule.Service).
type Schedule interface {
	Get(ctx context.Context, ref component.Ref) (schedule.Schedule, error)
	Add(ctx context.Context, ref component.Ref, in schedule.Input) (schedule.Schedule, error)
	Update(ctx context.Context, ref component.Ref, item id.ID, in schedule.Input) (schedule.Schedule, error)
	Remove(ctx context.Context, ref component.Ref, item id.ID) (schedule.Schedule, error)
}

// ScheduleComponentServer implements corev1.ScheduleComponentServiceServer
// (issue #247; ADR-0025 §4). Every method needs a verified token
// (auth.Guard); the acting user is always the token's.
type ScheduleComponentServer struct {
	corev1.UnimplementedScheduleComponentServiceServer
	app Schedule
	log *slog.Logger
}

// NewScheduleComponentServer returns a ScheduleComponentServer.
func NewScheduleComponentServer(app Schedule, log *slog.Logger) *ScheduleComponentServer {
	return &ScheduleComponentServer{app: app, log: log}
}

// registerScheduleComponent is the schedule component's line in the
// registry (componentRegistrars).
func registerScheduleComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterScheduleComponentServiceServer(srv, NewScheduleComponentServer(schedule.New(fw), log))
	return nil
}

func schedulePB(s schedule.Schedule) *corev1.Schedule {
	out := &corev1.Schedule{
		Items:    make([]*corev1.ScheduleItem, 0, len(s.Items)),
		MaxItems: s.MaxItems, CallerCanEdit: s.CallerCanEdit,
	}
	for _, it := range s.Items {
		pb := &corev1.ScheduleItem{Id: it.ID.String(), StartTime: timestamppb.New(it.StartsAt), Title: it.Title, Note: it.Note}
		if !it.EndsAt.IsZero() {
			pb.EndTime = timestamppb.New(it.EndsAt)
		}
		out.Items = append(out.Items, pb)
	}
	return out
}

// scheduleTime reads an optional timestamp: zero when unset, ok false when
// set but invalid.
func scheduleTime(t *timestamppb.Timestamp) (time.Time, bool) {
	if t == nil {
		return time.Time{}, true
	}
	if t.CheckValid() != nil {
		return time.Time{}, false
	}
	at := t.AsTime()
	return at, !at.IsZero()
}

// scheduleInput reads an item's values; the app layer checks them.
func scheduleInput(in *corev1.ScheduleItemInput) (schedule.Input, error) {
	start, ok := scheduleTime(in.GetStartTime())
	if !ok || start.IsZero() {
		return schedule.Input{}, validation("start_time")
	}
	end, ok := scheduleTime(in.GetEndTime())
	if !ok {
		return schedule.Input{}, validation("end_time")
	}
	return schedule.Input{StartsAt: start, EndsAt: end, Title: in.GetTitle(), Note: in.GetNote()}, nil
}

// scheduleItemRef reads the component and the item of a request.
func scheduleItemRef(ctx context.Context, ref *corev1.ComponentRef, itemID string) (component.Ref, id.ID, error) {
	r, err := componentRef(ctx, ref)
	if err != nil {
		return component.Ref{}, id.ID{}, err
	}
	it, ok := id.Parse(itemID)
	if !ok {
		return component.Ref{}, id.ID{}, validation("item_id")
	}
	return r, it, nil
}

// GetSchedule implements corev1.ScheduleComponentServiceServer.
func (s *ScheduleComponentServer) GetSchedule(ctx context.Context, req *corev1.GetScheduleRequest) (*corev1.GetScheduleResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetScheduleResponse{Schedule: schedulePB(out)}, nil
}

// AddScheduleItem implements corev1.ScheduleComponentServiceServer.
func (s *ScheduleComponentServer) AddScheduleItem(ctx context.Context, req *corev1.AddScheduleItemRequest) (*corev1.AddScheduleItemResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	in, err := scheduleInput(req.GetItem())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Add(ctx, ref, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddScheduleItemResponse{Schedule: schedulePB(out)}, nil
}

// UpdateScheduleItem implements corev1.ScheduleComponentServiceServer.
func (s *ScheduleComponentServer) UpdateScheduleItem(ctx context.Context, req *corev1.UpdateScheduleItemRequest) (*corev1.UpdateScheduleItemResponse, error) {
	ref, item, err := scheduleItemRef(ctx, req.GetRef(), req.GetItemId())
	if err != nil {
		return nil, err
	}
	in, err := scheduleInput(req.GetItem())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Update(ctx, ref, item, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateScheduleItemResponse{Schedule: schedulePB(out)}, nil
}

// RemoveScheduleItem implements corev1.ScheduleComponentServiceServer.
func (s *ScheduleComponentServer) RemoveScheduleItem(ctx context.Context, req *corev1.RemoveScheduleItemRequest) (*corev1.RemoveScheduleItemResponse, error) {
	ref, item, err := scheduleItemRef(ctx, req.GetRef(), req.GetItemId())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Remove(ctx, ref, item)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveScheduleItemResponse{Schedule: schedulePB(out)}, nil
}
