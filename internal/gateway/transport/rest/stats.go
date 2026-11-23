package rest

import (
	"context"
	"net/http"
	"strconv"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The tayfa statistics (issue #266; ADR-0026). core decides everything;
// the gateway only keeps junk out early. A reader gets only their own
// contributions (issue #322): the deprecated contributions list is always
// empty, whatever core sends, and stays only for clients built before.

const statsPath = "/v1/tayfas/{tayfa_id}/stats"

// registerTayfaStats is the tayfa statistics' line in the registry
// (componentRoutes). Its route is the tayfa's, not an event's.
func registerTayfaStats(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc("GET "+statsPath, h.authed(h.getTayfaStats))
}

func (h componentHandlers) stats() corev1.TayfaStatsServiceClient {
	return corev1.NewTayfaStatsServiceClient(h.conn)
}

type tayfaStatJSON struct {
	MetricKey  string  `json:"metric_key"`
	Source     string  `json:"source"`
	Value      float64 `json:"value"`
	EventCount int32   `json:"event_count"`
}

type tayfaStatsJSON struct {
	// Year is absent for all time.
	Year             *int32          `json:"year,omitempty"`
	Years            []int32         `json:"years"`
	Totals           []tayfaStatJSON `json:"totals"`
	OwnContributions []tayfaStatJSON `json:"own_contributions"`
	// Contributions is deprecated and always empty (issue #322).
	Contributions []struct{} `json:"contributions"`
	ComputedAt    *time.Time `json:"computed_at,omitempty"`
}

// statsYear reads the optional year query: absent for all time (0), else
// a year 2000–2999.
func statsYear(s string) (int32, bool) {
	if s == "" {
		return 0, true
	}
	if len(s) != 4 {
		return 0, false
	}
	y, err := strconv.ParseInt(s, 10, 32)
	if err != nil || y < 2000 || y > 2999 {
		return 0, false
	}
	return int32(y), true //nolint:gosec // parsed with bitSize 32 and bounded
}

func (h componentHandlers) getTayfaStats(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	year, ok := statsYear(r.URL.Query().Get("year"))
	if !ok {
		badRequest(w, r)
		return
	}
	resp, err := h.stats().GetTayfaStats(ctx, &corev1.GetTayfaStatsRequest{TayfaId: tf, Year: year})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := tayfaStatsJSON{
		Years:            append([]int32{}, resp.GetYears()...),
		Totals:           make([]tayfaStatJSON, 0, len(resp.GetTotals())),
		OwnContributions: make([]tayfaStatJSON, 0, len(resp.GetOwnContributions())),
		Contributions:    []struct{}{},
		ComputedAt:       optTime(resp.GetComputeTime()),
	}
	if y := resp.GetYear(); y != 0 {
		out.Year = &y
	}
	for _, s := range resp.GetTotals() {
		out.Totals = append(out.Totals, tayfaStatJSON{MetricKey: s.GetMetricKey(), Source: s.GetSource(), Value: s.GetValue(), EventCount: s.GetEventCount()})
	}
	for _, s := range resp.GetOwnContributions() {
		out.OwnContributions = append(out.OwnContributions, tayfaStatJSON{MetricKey: s.GetMetricKey(), Source: s.GetSource(), Value: s.GetValue(), EventCount: s.GetEventCount()})
	}
	writeJSON(w, http.StatusOK, out)
}
