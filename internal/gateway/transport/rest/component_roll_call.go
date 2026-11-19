package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The roll call and overdue alarm component (kind "roll_call", issue #242;
// ADR-0025 §4, §9). core decides everything; the gateway only keeps junk out
// early. No location is taken (ADR-0008).

// registerRollCallComponent is the roll call component's line in the
// registry (componentRoutes).
func registerRollCallComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "roll-call"), h.authed(h.getRollCall))
	mux.HandleFunc(componentPath("PUT", "roll-call/expected-return"), h.authed(h.setRollCallExpectedReturn))
	mux.HandleFunc(componentPath("POST", "roll-call/start"), h.authed(h.startRollCall))
	mux.HandleFunc(componentPath("POST", "roll-call/return"), h.authed(h.returnRollCall))
}

func (h componentHandlers) rollCalls() corev1.RollCallComponentServiceClient {
	return corev1.NewRollCallComponentServiceClient(h.conn)
}

type rollCallEntryJSON struct {
	UserID     string     `json:"user_id"`
	StartedAt  time.Time  `json:"started_at"`
	ReturnedAt *time.Time `json:"returned_at,omitempty"`
}

type eventRollCallJSON struct {
	Alarm              bool                `json:"alarm"`
	ReturnGraceMinutes int32               `json:"return_grace_minutes"`
	ExpectedReturnAt   *time.Time          `json:"expected_return_at,omitempty"`
	Mine               *rollCallEntryJSON  `json:"mine,omitempty"`
	Entries            []rollCallEntryJSON `json:"entries"`
	CallerCanManage    bool                `json:"caller_can_manage"`
	CallerCanEdit      bool                `json:"caller_can_edit"`
}

func rollCallEntryFrom(e *corev1.RollCallEntry) rollCallEntryJSON {
	return rollCallEntryJSON{UserID: e.GetUserId(), StartedAt: e.GetStartTime().AsTime(), ReturnedAt: optTime(e.GetReturnTime())}
}

func eventRollCallFrom(r *corev1.EventRollCall) eventRollCallJSON {
	out := eventRollCallJSON{
		Alarm: r.GetAlarm(), ReturnGraceMinutes: r.GetReturnGraceMinutes(), ExpectedReturnAt: optTime(r.GetExpectedReturnTime()),
		Entries:         make([]rollCallEntryJSON, 0, len(r.GetEntries())),
		CallerCanManage: r.GetCallerCanManage(), CallerCanEdit: r.GetCallerCanEdit(),
	}
	if r.GetMine() != nil {
		m := rollCallEntryFrom(r.GetMine())
		out.Mine = &m
	}
	for _, e := range r.GetEntries() {
		out.Entries = append(out.Entries, rollCallEntryFrom(e))
	}
	return out
}

func (h componentHandlers) getRollCall(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.rollCalls().GetRollCall(ctx, &corev1.GetRollCallRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventRollCallFrom(resp.GetRollCall()))
}

// rollCallPlanBody is the body of a plan change: a time sets the plan; no
// expected_return_at (or null) removes it.
type rollCallPlanBody struct {
	ExpectedReturnAt *time.Time `json:"expected_return_at"`
}

func (h componentHandlers) setRollCallExpectedReturn(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b rollCallPlanBody
	if !decode(w, r, &b) {
		return
	}
	resp, err := h.rollCalls().SetExpectedReturn(ctx, &corev1.SetExpectedReturnRequest{Ref: ref, ExpectedReturnTime: optTimestamp(b.ExpectedReturnAt)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventRollCallFrom(resp.GetRollCall()))
}

type rollCallStartBody struct {
	StartedAt *time.Time `json:"started_at"`
}

func (h componentHandlers) startRollCall(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b rollCallStartBody
	if !decode(w, r, &b) {
		return
	}
	resp, err := h.rollCalls().StartRollCall(ctx, &corev1.StartRollCallRequest{Ref: ref, StartTime: optTimestamp(b.StartedAt)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventRollCallFrom(resp.GetRollCall()))
}

type rollCallReturnBody struct {
	ReturnedAt *time.Time `json:"returned_at"`
}

func (h componentHandlers) returnRollCall(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b rollCallReturnBody
	if !decode(w, r, &b) {
		return
	}
	resp, err := h.rollCalls().ReturnRollCall(ctx, &corev1.ReturnRollCallRequest{Ref: ref, ReturnTime: optTimestamp(b.ReturnedAt)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventRollCallFrom(resp.GetRollCall()))
}
