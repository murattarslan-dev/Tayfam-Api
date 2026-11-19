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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/goal"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeGoals struct {
	err    error
	ref    component.Ref
	target float64
	out    goal.Goal
	calls  int
	last   string
}

func (f *fakeGoals) Get(_ context.Context, ref component.Ref) (goal.Goal, error) {
	f.ref, f.calls, f.last = ref, f.calls+1, "get"
	return f.out, f.err
}

func (f *fakeGoals) SetTarget(_ context.Context, ref component.Ref, target float64) (goal.Goal, error) {
	f.ref, f.target, f.calls, f.last = ref, target, f.calls+1, "set"
	return f.out, f.err
}

func (f *fakeGoals) RemoveTarget(_ context.Context, ref component.Ref) (goal.Goal, error) {
	f.ref, f.calls, f.last = ref, f.calls+1, "remove"
	return f.out, f.err
}

func TestGoalComponentMethods(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	target, def, cur := 150.0, 100.0, 0.0
	f := &fakeGoals{out: goal.Goal{
		Measure: "weight", Unit: "kg", Decimals: 1, Target: &target, TargetSet: true, TargetUpdatedAt: at,
		DefaultTarget: &def, Current: &cur, MeasureEnabled: true, CallerCanEdit: true,
	}}
	s := NewGoalComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "goal"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "goal"}

	got, err := s.GetGoal(ctx, &corev1.GetGoalRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	g := got.GetGoal()
	if f.ref != want || g.GetMeasureKey() != "weight" || g.GetUnit() != "kg" || g.GetDecimals() != 1 || g.Target == nil || g.GetTarget() != 150 ||
		!g.GetTargetSet() || !g.GetTargetUpdateTime().AsTime().Equal(at) || g.GetDefaultTarget() != 100 ||
		g.Current == nil || g.GetCurrent() != 0 || !g.GetMeasureEnabled() || g.GetProgressHidden() || g.GetReached() || !g.GetCallerCanEdit() {
		t.Fatalf("okuma: %v (%+v)", g, f.ref)
	}

	if _, err := s.SetGoalTarget(ctx, &corev1.SetGoalTargetRequest{Ref: ref, Target: 7.5}); err != nil || f.last != "set" || f.target != 7.5 || f.ref != want {
		t.Fatalf("hedef yazma: %v %+v", err, f)
	}

	// Without a target, hidden progress: the optional fields stay unset.
	f.out = goal.Goal{Measure: "net", Unit: "net", ProgressHidden: true, MeasureEnabled: true}
	rm, err := s.RemoveGoalTarget(ctx, &corev1.RemoveGoalTargetRequest{Ref: ref})
	if err != nil || f.last != "remove" {
		t.Fatalf("hedef kaldırma: %v %+v", err, f)
	}
	if g := rm.GetGoal(); g.Target != nil || g.DefaultTarget != nil || g.Current != nil || g.TargetUpdateTime != nil || !g.GetProgressHidden() {
		t.Fatalf("gizli ilerleme: %v", g)
	}

	// The app's errors keep their reasons.
	for err, reason := range map[error]corev1.ErrorReason{
		apperr.ErrComponentFrozen:   corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN,
		apperr.ErrComponentNotFound: corev1.ErrorReason_ERROR_REASON_COMPONENT_NOT_FOUND,
		apperr.ErrPermissionDenied:  corev1.ErrorReason_ERROR_REASON_PERMISSION_DENIED,
	} {
		f.err = err
		_, got := s.SetGoalTarget(ctx, &corev1.SetGoalTargetRequest{Ref: ref, Target: 1})
		if _, info := infoOf(t, got); info.GetReason() != reason.String() {
			t.Errorf("%v: %v", err, got)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestGoalComponentValidation(t *testing.T) {
	f := &fakeGoals{}
	s := NewGoalComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "goal"}

	for name, req := range map[string]*corev1.SetGoalTargetRequest{
		"başvurusuz":    {Target: 1},
		"bozuk anahtar": {Ref: &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "Goal"}, Target: 1},
		"bozuk tayfa":   {Ref: &corev1.ComponentRef{TayfaId: "x", EventId: ev, ComponentKey: "goal"}, Target: 1},
		"sıfır":         {Ref: ref},
		"eksi":          {Ref: ref, Target: -1},
		"çok büyük":     {Ref: ref, Target: goal.MaxTarget + 1},
		"NaN":           {Ref: ref, Target: math.NaN()},
		"sonsuz":        {Ref: ref, Target: math.Inf(1)},
	} {
		if _, err := s.SetGoalTarget(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.RemoveGoalTarget(ctx, &corev1.RemoveGoalTargetRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("başvurusuz kaldırma: %v", err)
	}
	if _, err := s.GetGoal(ctx, &corev1.GetGoalRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("başvurusuz okuma: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.SetGoalTarget(context.Background(), &corev1.SetGoalTargetRequest{Ref: ref, Target: 1}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the goal component.
func TestGoalComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.GoalComponentService"]
	if !ok || len(info.Methods) != 3 {
		t.Fatalf("hedef servisi: %v", info)
	}
}
