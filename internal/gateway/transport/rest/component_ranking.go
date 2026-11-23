package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The ranking component (kind "ranking", issue #253; ADR-0025 §4, §5;
// ADR-0037 §3). core decides everything; the gateway only keeps junk out
// early. Values are never logged.

const (
	// maxRankingBody fits {"value": …} with any number.
	maxRankingBody = 1 << 10
	// rankingMaxValue is ranking.MaxDuration in core, the widest bound of a
	// value entered by hand.
	rankingMaxValue = 1_000_000
)

// registerRankingComponent is the ranking component's line in the registry
// (componentRoutes).
func registerRankingComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "ranking"), h.authed(h.getRanking))
	mux.HandleFunc(componentPath("PUT", "ranking/entries/{user_id}"), h.authed(h.setRankingEntry))
	mux.HandleFunc(componentPath("DELETE", "ranking/entries/{user_id}"), h.authed(h.removeRankingEntry))
}

func (h componentHandlers) rankings() corev1.RankingComponentServiceClient {
	return corev1.NewRankingComponentServiceClient(h.conn)
}

type rankingEntryJSON struct {
	UserID    string     `json:"user_id"`
	Value     float64    `json:"value"`
	Rank      int32      `json:"rank"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type eventRankingJSON struct {
	Source         string             `json:"source,omitempty"`
	Metric         string             `json:"metric,omitempty"`
	Direction      string             `json:"direction"`
	Unit           string             `json:"unit,omitempty"`
	Decimals       int32              `json:"decimals"`
	Entries        []rankingEntryJSON `json:"entries"`
	Hidden         bool               `json:"hidden"`
	MeasureEnabled bool               `json:"measure_enabled"`
	CallerCanEdit  bool               `json:"caller_can_edit"`
}

func rankingMetricName(m corev1.RankingMetric) string {
	switch m {
	case corev1.RankingMetric_RANKING_METRIC_DURATION:
		return "duration"
	case corev1.RankingMetric_RANKING_METRIC_RANK:
		return "rank"
	}
	return ""
}

func rankingDirectionName(d corev1.RankingDirection) string {
	switch d {
	case corev1.RankingDirection_RANKING_DIRECTION_ASC:
		return "asc"
	case corev1.RankingDirection_RANKING_DIRECTION_DESC:
		return "desc"
	}
	return ""
}

func eventRankingFrom(r *corev1.EventRanking) eventRankingJSON {
	if r == nil {
		r = &corev1.EventRanking{}
	}
	out := eventRankingJSON{
		Source: r.GetSource(), Metric: rankingMetricName(r.GetMetric()), Direction: rankingDirectionName(r.GetDirection()),
		Unit: r.GetUnit(), Decimals: r.GetDecimals(), Hidden: r.GetHidden(), MeasureEnabled: r.GetMeasureEnabled(),
		CallerCanEdit: r.GetCallerCanEdit(), Entries: make([]rankingEntryJSON, 0, len(r.GetEntries())),
	}
	for _, e := range r.GetEntries() {
		out.Entries = append(out.Entries, rankingEntryJSON{
			UserID: e.GetUserId(), Value: e.GetValue(), Rank: e.GetRank(), UpdatedAt: optTime(e.GetUpdateTime()),
		})
	}
	return out
}

// rankingValueBody is the body of a value change. Value is a pointer so
// that a body without it is refused rather than read as 0.
type rankingValueBody struct {
	Value *float64 `json:"value"`
}

// rankingValue reads the body's value; on a bad one it answers
// invalid-request and ok is false. core checks the metric's own bounds.
func rankingValue(w http.ResponseWriter, r *http.Request) (v float64, ok bool) {
	var b rankingValueBody
	if !decodeLimit(w, r, &b, maxRankingBody) {
		return 0, false
	}
	if b.Value == nil || *b.Value <= 0 || *b.Value > rankingMaxValue {
		badRequest(w, r)
		return 0, false
	}
	return *b.Value, true
}

func (h componentHandlers) getRanking(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.rankings().GetRanking(ctx, &corev1.GetRankingRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventRankingFrom(resp.GetRanking()))
}

func (h componentHandlers) setRankingEntry(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	uid, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	v, ok := rankingValue(w, r)
	if !ok {
		return
	}
	resp, err := h.rankings().SetRankingEntry(ctx, &corev1.SetRankingEntryRequest{Ref: ref, UserId: uid, Value: v})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventRankingFrom(resp.GetRanking()))
}

func (h componentHandlers) removeRankingEntry(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	uid, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	resp, err := h.rankings().RemoveRankingEntry(ctx, &corev1.RemoveRankingEntryRequest{Ref: ref, UserId: uid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventRankingFrom(resp.GetRanking()))
}
