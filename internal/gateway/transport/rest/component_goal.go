package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The goal bar component (kind "goal", issue #246; ADR-0025 §4, §5). core
// decides everything; the gateway only keeps junk out early. Targets are
// never logged.

const (
	// maxGoalBody fits {"target": …} with any number.
	maxGoalBody = 1 << 10
	// goalMaxTarget is goal.MaxTarget in core.
	goalMaxTarget = 1_000_000_000
)

// registerGoalComponent is the goal component's line in the registry
// (componentRoutes).
func registerGoalComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "goal"), h.authed(h.getGoal))
	mux.HandleFunc(componentPath("PUT", "goal/target"), h.authed(h.setGoalTarget))
	mux.HandleFunc(componentPath("DELETE", "goal/target"), h.authed(h.removeGoalTarget))
}

func (h componentHandlers) goals() corev1.GoalComponentServiceClient {
	return corev1.NewGoalComponentServiceClient(h.conn)
}

type eventGoalJSON struct {
	MeasureKey      string     `json:"measure_key"`
	Unit            string     `json:"unit"`
	Decimals        int32      `json:"decimals"`
	Target          *float64   `json:"target,omitempty"`
	TargetSet       bool       `json:"target_set"`
	TargetUpdatedAt *time.Time `json:"target_updated_at,omitempty"`
	DefaultTarget   *float64   `json:"default_target,omitempty"`
	Current         *float64   `json:"current,omitempty"`
	ProgressHidden  bool       `json:"progress_hidden"`
	MeasureEnabled  bool       `json:"measure_enabled"`
	Reached         bool       `json:"reached"`
	CallerCanEdit   bool       `json:"caller_can_edit"`
}

func optFloat(set bool, v float64) *float64 {
	if !set {
		return nil
	}
	return &v
}

func eventGoalFrom(g *corev1.EventGoal) eventGoalJSON {
	if g == nil {
		g = &corev1.EventGoal{}
	}
	return eventGoalJSON{
		MeasureKey: g.GetMeasureKey(), Unit: g.GetUnit(), Decimals: g.GetDecimals(),
		Target: optFloat(g.Target != nil, g.GetTarget()), TargetSet: g.GetTargetSet(), TargetUpdatedAt: optTime(g.GetTargetUpdateTime()),
		DefaultTarget: optFloat(g.DefaultTarget != nil, g.GetDefaultTarget()),
		Current:       optFloat(g.Current != nil, g.GetCurrent()), ProgressHidden: g.GetProgressHidden(),
		MeasureEnabled: g.GetMeasureEnabled(), Reached: g.GetReached(), CallerCanEdit: g.GetCallerCanEdit(),
	}
}

// goalTargetBody is the body of a target change. Target is a pointer so
// that a body without it is refused rather than read as 0.
type goalTargetBody struct {
	Target *float64 `json:"target"`
}

func (h componentHandlers) getGoal(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.goals().GetGoal(ctx, &corev1.GetGoalRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventGoalFrom(resp.GetGoal()))
}

func (h componentHandlers) setGoalTarget(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b goalTargetBody
	if !decodeLimit(w, r, &b, maxGoalBody) {
		return
	}
	if b.Target == nil || *b.Target <= 0 || *b.Target > goalMaxTarget {
		badRequest(w, r)
		return
	}
	resp, err := h.goals().SetGoalTarget(ctx, &corev1.SetGoalTargetRequest{Ref: ref, Target: *b.Target})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventGoalFrom(resp.GetGoal()))
}

func (h componentHandlers) removeGoalTarget(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.goals().RemoveGoalTarget(ctx, &corev1.RemoveGoalTargetRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventGoalFrom(resp.GetGoal()))
}
