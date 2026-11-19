package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The measure component (kind "measure", issue #245; ADR-0025 §4, §5). core
// decides everything; the gateway only keeps junk out early. Values are
// never logged.

const (
	// maxMeasureBody fits {"value": …} with any number.
	maxMeasureBody = 1 << 10
	// measureMaxValue is measure.MaxValue in core.
	measureMaxValue = 1_000_000_000
)

// registerMeasureComponent is the measure component's line in the registry
// (componentRoutes).
func registerMeasureComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "measure"), h.authed(h.getMeasure))
	mux.HandleFunc(componentPath("PUT", "measure/total"), h.authed(h.setMeasureTotal))
	mux.HandleFunc(componentPath("DELETE", "measure/total"), h.authed(h.removeMeasureTotal))
	mux.HandleFunc(componentPath("PUT", "measure/entries/{user_id}"), h.authed(h.setMeasureEntry))
	mux.HandleFunc(componentPath("DELETE", "measure/entries/{user_id}"), h.authed(h.removeMeasureEntry))
}

func (h componentHandlers) measures() corev1.MeasureComponentServiceClient {
	return corev1.NewMeasureComponentServiceClient(h.conn)
}

type measureEntryJSON struct {
	UserID    string     `json:"user_id"`
	Value     float64    `json:"value"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type eventMeasureJSON struct {
	Unit           string             `json:"unit"`
	Scope          string             `json:"scope"`
	EnteredBy      string             `json:"entered_by"`
	Min            float64            `json:"min"`
	Max            float64            `json:"max"`
	Decimals       int32              `json:"decimals"`
	Total          *float64           `json:"total,omitempty"`
	TotalUpdatedAt *time.Time         `json:"total_updated_at,omitempty"`
	Entries        []measureEntryJSON `json:"entries"`
	Suggested      *float64           `json:"suggested,omitempty"`
	CallerCanEdit  bool               `json:"caller_can_edit"`
}

func measureScopeName(s corev1.MeasureScope) string {
	switch s {
	case corev1.MeasureScope_MEASURE_SCOPE_TOTAL:
		return "total"
	case corev1.MeasureScope_MEASURE_SCOPE_PERSONAL:
		return "personal"
	}
	return ""
}

func measureEnteredByName(s corev1.MeasureEnteredBy) string {
	switch s {
	case corev1.MeasureEnteredBy_MEASURE_ENTERED_BY_SELF:
		return "self"
	case corev1.MeasureEnteredBy_MEASURE_ENTERED_BY_ORGANIZER:
		return "organizer"
	}
	return ""
}

func eventMeasureFrom(m *corev1.EventMeasure) eventMeasureJSON {
	if m == nil {
		m = &corev1.EventMeasure{}
	}
	out := eventMeasureJSON{
		Unit: m.GetUnit(), Scope: measureScopeName(m.GetScope()), EnteredBy: measureEnteredByName(m.GetEnteredBy()),
		Min: m.GetMin(), Max: m.GetMax(), Decimals: m.GetDecimals(),
		TotalUpdatedAt: optTime(m.GetTotalUpdateTime()), CallerCanEdit: m.GetCallerCanEdit(),
		Entries: make([]measureEntryJSON, 0, len(m.GetEntries())),
	}
	if m.Total != nil {
		v := m.GetTotal()
		out.Total = &v
	}
	if m.Suggested != nil {
		v := m.GetSuggested()
		out.Suggested = &v
	}
	for _, e := range m.GetEntries() {
		out.Entries = append(out.Entries, measureEntryJSON{UserID: e.GetUserId(), Value: e.GetValue(), UpdatedAt: optTime(e.GetUpdateTime())})
	}
	return out
}

// measureValueBody is the body of a value change. Value is a pointer so
// that a body without it is refused rather than read as 0.
type measureValueBody struct {
	Value *float64 `json:"value"`
}

// measureValue reads the body's value; on a bad one it answers
// invalid-request and ok is false.
func measureValue(w http.ResponseWriter, r *http.Request) (v float64, ok bool) {
	var b measureValueBody
	if !decodeLimit(w, r, &b, maxMeasureBody) {
		return 0, false
	}
	if b.Value == nil || *b.Value < -measureMaxValue || *b.Value > measureMaxValue {
		badRequest(w, r)
		return 0, false
	}
	return *b.Value, true
}

func (h componentHandlers) getMeasure(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.measures().GetMeasure(ctx, &corev1.GetMeasureRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventMeasureFrom(resp.GetMeasure()))
}

func (h componentHandlers) setMeasureTotal(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	v, ok := measureValue(w, r)
	if !ok {
		return
	}
	resp, err := h.measures().SetMeasureTotal(ctx, &corev1.SetMeasureTotalRequest{Ref: ref, Value: v})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventMeasureFrom(resp.GetMeasure()))
}

func (h componentHandlers) removeMeasureTotal(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.measures().RemoveMeasureTotal(ctx, &corev1.RemoveMeasureTotalRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventMeasureFrom(resp.GetMeasure()))
}

func (h componentHandlers) setMeasureEntry(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	uid, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	v, ok := measureValue(w, r)
	if !ok {
		return
	}
	resp, err := h.measures().SetMeasureEntry(ctx, &corev1.SetMeasureEntryRequest{Ref: ref, UserId: uid, Value: v})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventMeasureFrom(resp.GetMeasure()))
}

func (h componentHandlers) removeMeasureEntry(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	uid, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	resp, err := h.measures().RemoveMeasureEntry(ctx, &corev1.RemoveMeasureEntryRequest{Ref: ref, UserId: uid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventMeasureFrom(resp.GetMeasure()))
}
