package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/rollcall"
)

// RollCalls is what RollCallComponentServer needs from the app layer
// (rollcall.Service).
type RollCalls interface {
	Get(ctx context.Context, ref component.Ref) (rollcall.RollCall, error)
	SetExpectedReturn(ctx context.Context, ref component.Ref, at time.Time) (rollcall.RollCall, error)
	Start(ctx context.Context, ref component.Ref, startedAt time.Time) (rollcall.RollCall, error)
	Return(ctx context.Context, ref component.Ref, returnedAt time.Time) (rollcall.RollCall, error)
}

// RollCallComponentServer implements corev1.RollCallComponentServiceServer
// (issue #242; ADR-0025 §4). Every method needs a verified token
// (auth.Guard); the acting user is always the token's. No location is
// taken.
type RollCallComponentServer struct {
	corev1.UnimplementedRollCallComponentServiceServer
	app RollCalls
	log *slog.Logger
}

// NewRollCallComponentServer returns a RollCallComponentServer.
func NewRollCallComponentServer(app RollCalls, log *slog.Logger) *RollCallComponentServer {
	return &RollCallComponentServer{app: app, log: log}
}

// registerRollCallComponent is the roll call component's line in the
// registry (componentRegistrars).
func registerRollCallComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterRollCallComponentServiceServer(srv, NewRollCallComponentServer(rollcall.New(fw), log))
	return nil
}

func rollCallEntryPB(e rollcall.Entry) *corev1.RollCallEntry {
	out := &corev1.RollCallEntry{UserId: e.User.String(), StartTime: timestamppb.New(e.StartedAt)}
	if !e.ReturnedAt.IsZero() {
		out.ReturnTime = timestamppb.New(e.ReturnedAt)
	}
	return out
}

func eventRollCallPB(r rollcall.RollCall) *corev1.EventRollCall {
	out := &corev1.EventRollCall{
		Alarm: r.Alarm, ReturnGraceMinutes: r.ReturnGraceMinutes,
		Entries:         make([]*corev1.RollCallEntry, 0, len(r.Entries)),
		CallerCanManage: r.CallerCanManage, CallerCanEdit: r.CallerCanEdit,
	}
	if !r.ExpectedReturnAt.IsZero() {
		out.ExpectedReturnTime = timestamppb.New(r.ExpectedReturnAt)
	}
	if r.Mine != nil {
		out.Mine = rollCallEntryPB(*r.Mine)
	}
	for _, e := range r.Entries {
		out.Entries = append(out.Entries, rollCallEntryPB(e))
	}
	return out
}

// rollCallTime reads an optional timestamp: zero when unset, ok false when set
// but invalid.
func rollCallTime(t *timestamppb.Timestamp) (time.Time, bool) {
	if t == nil {
		return time.Time{}, true
	}
	if t.CheckValid() != nil {
		return time.Time{}, false
	}
	at := t.AsTime()
	return at, !at.IsZero()
}

// GetRollCall implements corev1.RollCallComponentServiceServer.
func (s *RollCallComponentServer) GetRollCall(ctx context.Context, req *corev1.GetRollCallRequest) (*corev1.GetRollCallResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	r, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetRollCallResponse{RollCall: eventRollCallPB(r)}, nil
}

// SetExpectedReturn implements corev1.RollCallComponentServiceServer.
func (s *RollCallComponentServer) SetExpectedReturn(ctx context.Context, req *corev1.SetExpectedReturnRequest) (*corev1.SetExpectedReturnResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	at, ok := rollCallTime(req.GetExpectedReturnTime())
	if !ok {
		return nil, validation("expected_return_time")
	}
	r, err := s.app.SetExpectedReturn(ctx, ref, at)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetExpectedReturnResponse{RollCall: eventRollCallPB(r)}, nil
}

// StartRollCall implements corev1.RollCallComponentServiceServer.
func (s *RollCallComponentServer) StartRollCall(ctx context.Context, req *corev1.StartRollCallRequest) (*corev1.StartRollCallResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	at, ok := rollCallTime(req.GetStartTime())
	if !ok {
		return nil, validation("start_time")
	}
	r, err := s.app.Start(ctx, ref, at)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.StartRollCallResponse{RollCall: eventRollCallPB(r)}, nil
}

// ReturnRollCall implements corev1.RollCallComponentServiceServer.
func (s *RollCallComponentServer) ReturnRollCall(ctx context.Context, req *corev1.ReturnRollCallRequest) (*corev1.ReturnRollCallResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	at, ok := rollCallTime(req.GetReturnTime())
	if !ok {
		return nil, validation("return_time")
	}
	r, err := s.app.Return(ctx, ref, at)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ReturnRollCallResponse{RollCall: eventRollCallPB(r)}, nil
}
