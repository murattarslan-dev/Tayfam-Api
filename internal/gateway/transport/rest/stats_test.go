package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const statsService = "tayfam.core.v1.TayfaStatsService"

// fakeStats is core's TayfaStatsService for the statistics tests.
type fakeStats struct {
	got   *corev1.GetTayfaStatsRequest
	token string
	resp  *corev1.GetTayfaStatsResponse
	err   error
	calls int
}

func (f *fakeStats) GetTayfaStats(ctx context.Context, in *corev1.GetTayfaStatsRequest, _ ...grpc.CallOption) (*corev1.GetTayfaStatsResponse, error) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	if f.resp == nil {
		return &corev1.GetTayfaStatsResponse{}, f.err
	}
	return f.resp, f.err
}

const tayfaStatsPath = "/v1/tayfas/" + tayfaID + "/stats"

func TestTayfaStatsRoute(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	f := &fakeStats{resp: &corev1.GetTayfaStatsResponse{
		Year: 2025, Years: []int32{2026, 2025},
		Totals:           []*corev1.TayfaStat{{MetricKey: "events", Source: "common", Value: 4, EventCount: 4}},
		OwnContributions: []*corev1.TayfaStat{{MetricKey: "hike.distance", Source: "measure", Value: 12.5, EventCount: 2}},
		// Even if core sent someone's contribution, it would not go out
		// (issue #322).
		Contributions: []*corev1.TayfaStatContribution{{UserId: userID, MetricKey: "hike.distance", Value: 12.5, EventCount: 2}}, //nolint:staticcheck // the deprecated field must not go out
		ComputeTime:   timestamppb.New(at),
	}}
	h := componentRouter(fakeServices{statsService: f})

	w := as(h, "GET", tayfaStatsPath+"?year=2025", "")
	if w.Code != http.StatusOK || !proto.Equal(f.got, &corev1.GetTayfaStatsRequest{TayfaId: tayfaID, Year: 2025}) || f.token != "USERTOKEN" {
		t.Fatalf("istek: %d %v %q %s", w.Code, f.got, f.token, w.Body)
	}
	v := decodeBody[tayfaStatsJSON](t, w)
	if v.Year == nil || *v.Year != 2025 || len(v.Years) != 2 || v.ComputedAt == nil || !v.ComputedAt.Equal(at) {
		t.Fatalf("yanıt: %+v", v)
	}
	if len(v.Totals) != 1 || v.Totals[0] != (tayfaStatJSON{MetricKey: "events", Source: "common", Value: 4, EventCount: 4}) {
		t.Fatalf("toplamlar: %+v", v.Totals)
	}
	if len(v.OwnContributions) != 1 || v.OwnContributions[0] != (tayfaStatJSON{MetricKey: "hike.distance", Source: "measure", Value: 12.5, EventCount: 2}) {
		t.Fatalf("kendi katkıları: %+v", v.OwnContributions)
	}
	if body := w.Body.String(); !strings.Contains(body, `"contributions":[]`) || strings.Contains(body, userID) || strings.Contains(body, "user_id") {
		t.Fatalf("başkasının katkısı yanıtta: %s", body)
	}

	// All time: no year, empty lists, no time.
	f.resp = nil
	w = as(h, "GET", tayfaStatsPath, "")
	if w.Code != http.StatusOK || f.got.GetYear() != 0 {
		t.Fatalf("tüm zamanlar: %d %v", w.Code, f.got)
	}
	if body := w.Body.String(); strings.Contains(body, `"year"`) || strings.Contains(body, "computed_at") ||
		!strings.Contains(body, `"years":[]`) || !strings.Contains(body, `"totals":[]`) || !strings.Contains(body, `"own_contributions":[]`) ||
		!strings.Contains(body, `"contributions":[]`) {
		t.Fatalf("boş yanıt: %s", body)
	}

	calls := f.calls
	for _, q := range []string{"?year=1999", "?year=3000", "?year=abc", "?year=02025", "?year=-2025", "?year=+202"} {
		if w := as(h, "GET", tayfaStatsPath+q, ""); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", q, w.Code, w.Body)
		}
	}
	if w := as(h, "GET", "/v1/tayfas/x/stats", ""); w.Code != http.StatusBadRequest {
		t.Errorf("geçersiz tayfa: %d", w.Code)
	}
	if f.calls != calls {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls-calls)
	}
	if w := do(h, "GET", tayfaStatsPath, "", map[string]string{clientHeader: "android"}); w.Code != http.StatusUnauthorized || f.calls != calls {
		t.Fatalf("token'sız: %d", w.Code)
	}

	h = componentRouter(fakeServices{statsService: &fakeStats{err: coreErr(codes.NotFound, "ERROR_REASON_TAYFA_NOT_FOUND")}})
	if w := as(h, "GET", tayfaStatsPath, ""); w.Code != http.StatusNotFound || problemType(t, w) != "tayfa-not-found" {
		t.Fatalf("üye olmayan: %d %s", w.Code, w.Body)
	}
}
