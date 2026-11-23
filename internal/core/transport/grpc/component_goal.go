package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/goal"
)

// Goals is what GoalComponentServer needs from the app layer
// (goal.Service).
type Goals interface {
	Get(ctx context.Context, ref component.Ref) (goal.Goal, error)
	SetTarget(ctx context.Context, ref component.Ref, target float64) (goal.Goal, error)
	RemoveTarget(ctx context.Context, ref component.Ref) (goal.Goal, error)
}

// GoalComponentServer implements corev1.GoalComponentServiceServer (issue
// #246; ADR-0025 §4, §5). Every method needs a verified token (auth.Guard);
// the acting user is always the token's. Targets are never logged.
type GoalComponentServer struct {
	corev1.UnimplementedGoalComponentServiceServer
	app Goals
	log *slog.Logger
}

// NewGoalComponentServer returns a GoalComponentServer.
func NewGoalComponentServer(app Goals, log *slog.Logger) *GoalComponentServer {
	return &GoalComponentServer{app: app, log: log}
}

// registerGoalComponent is the goal component's line in the registry
// (componentRegistrars).
func registerGoalComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterGoalComponentServiceServer(srv, NewGoalComponentServer(goal.New(fw), log))
	return nil
}

func eventGoalPB(g goal.Goal) *corev1.EventGoal {
	out := &corev1.EventGoal{
		MeasureKey: g.Measure, Unit: g.Unit, Decimals: g.Decimals,
		Target: g.Target, TargetSet: g.TargetSet, DefaultTarget: g.DefaultTarget,
		Current: g.Current, ProgressHidden: g.ProgressHidden, MeasureEnabled: g.MeasureEnabled,
		Reached: g.Reached, CallerCanEdit: g.CallerCanEdit,
	}
	if g.TargetSet && !g.TargetUpdatedAt.IsZero() {
		out.TargetUpdateTime = timestamppb.New(g.TargetUpdatedAt)
	}
	return out
}

// GetGoal implements corev1.GoalComponentServiceServer.
func (s *GoalComponentServer) GetGoal(ctx context.Context, req *corev1.GetGoalRequest) (*corev1.GetGoalResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	g, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetGoalResponse{Goal: eventGoalPB(g)}, nil
}

// SetGoalTarget implements corev1.GoalComponentServiceServer.
func (s *GoalComponentServer) SetGoalTarget(ctx context.Context, req *corev1.SetGoalTargetRequest) (*corev1.SetGoalTargetResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if !finite(req.GetTarget()) || req.GetTarget() <= 0 || req.GetTarget() > goal.MaxTarget {
		return nil, validation("target")
	}
	g, err := s.app.SetTarget(ctx, ref, req.GetTarget())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetGoalTargetResponse{Goal: eventGoalPB(g)}, nil
}

// RemoveGoalTarget implements corev1.GoalComponentServiceServer.
func (s *GoalComponentServer) RemoveGoalTarget(ctx context.Context, req *corev1.RemoveGoalTargetRequest) (*corev1.RemoveGoalTargetResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	g, err := s.app.RemoveTarget(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveGoalTargetResponse{Goal: eventGoalPB(g)}, nil
}
