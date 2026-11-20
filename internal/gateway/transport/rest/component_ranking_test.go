package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const rankingComponentService = "tayfam.core.v1.RankingComponentService"

// fakeRankings is core's RankingComponentService for the ranking tests.
type fakeRankings struct {
	get    *corev1.GetRankingRequest
	set    *corev1.SetRankingEntryRequest
	remove *corev1.RemoveRankingEntryRequest
	token  string
	out    *corev1.EventRanking
	err    error
	calls  int
}

func (f *fakeRankings) GetRanking(ctx context.Context, in *corev1.GetRankingRequest, _ ...grpc.CallOption) (*corev1.GetRankingResponse, error) {
	f.get, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.GetRankingResponse{Ranking: f.out}, f.err
}

func (f *fakeRankings) SetRankingEntry(ctx context.Context, in *corev1.SetRankingEntryRequest, _ ...grpc.CallOption) (*corev1.SetRankingEntryResponse, error) {
	f.set, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.SetRankingEntryResponse{Ranking: f.out}, f.err
}

func (f *fakeRankings) RemoveRankingEntry(ctx context.Context, in *corev1.RemoveRankingEntryRequest, _ ...grpc.CallOption) (*corev1.RemoveRankingEntryResponse, error) {
	f.remove, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.RemoveRankingEntryResponse{Ranking: f.out}, f.err
}

const (
	rankingPath      = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/race/ranking"
	rankingEntryPath = rankingPath + "/entries/" + userID
)

func TestRankingComponentRoutes(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	f := &fakeRankings{out: &corev1.EventRanking{
		Metric: corev1.RankingMetric_RANKING_METRIC_DURATION, Direction: corev1.RankingDirection_RANKING_DIRECTION_ASC,
		Decimals: 2, MeasureEnabled: true, CallerCanEdit: true,
		Entries: []*corev1.RankingEntry{{UserId: userID, Value: 61.5, Rank: 1, UpdateTime: timestamppb.New(at)}},
	}}
	h := componentRouter(fakeServices{rankingComponentService: f})

	w := as(h, "GET", rankingPath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("okuma: %d %s", w.Code, w.Body)
	}
	if ref := f.get.GetRef(); ref.GetTayfaId() != tayfaID || ref.GetEventId() != eventID || ref.GetComponentKey() != "race" || f.token != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", f.get, f.token)
	}
	got := decodeBody[eventRankingJSON](t, w)
	if got.Source != "" || got.Metric != "duration" || got.Direction != "asc" || got.Decimals != 2 || !got.MeasureEnabled || !got.CallerCanEdit ||
		got.Hidden || len(got.Entries) != 1 || got.Entries[0].UserID != userID || got.Entries[0].Value != 61.5 || got.Entries[0].Rank != 1 ||
		got.Entries[0].UpdatedAt == nil || !got.Entries[0].UpdatedAt.Equal(at) {
		t.Fatalf("derece: %+v", got)
	}
	if body := w.Body.String(); strings.Contains(body, `"source"`) || strings.Contains(body, `"unit"`) {
		t.Fatalf("elle derecede kaynak ya da birim: %s", body)
	}

	if w := as(h, "PUT", rankingEntryPath, `{"value":59.25}`); w.Code != http.StatusOK || f.set.GetUserId() != userID || f.set.GetValue() != 59.25 ||
		f.set.GetRef().GetComponentKey() != "race" {
		t.Fatalf("yazma: %d %s %v", w.Code, w.Body, f.set)
	}
	if w := as(h, "DELETE", rankingEntryPath, ""); w.Code != http.StatusOK || f.remove.GetUserId() != userID {
		t.Fatalf("kaldırma: %d %s", w.Code, w.Body)
	}

	// A computed ranking that the caller may not see: the source, no metric,
	// empty entries.
	f.out = &corev1.EventRanking{Source: "net", Direction: corev1.RankingDirection_RANKING_DIRECTION_DESC, Unit: "net", Hidden: true, MeasureEnabled: true}
	w = as(h, "GET", rankingPath, "")
	out := decodeBody[eventRankingJSON](t, w)
	if w.Code != http.StatusOK || out.Source != "net" || out.Metric != "" || out.Direction != "desc" || out.Unit != "net" || !out.Hidden {
		t.Fatalf("ölçümden derece: %d %+v", w.Code, out)
	}
	if body := w.Body.String(); !strings.Contains(body, `"entries":[]`) || strings.Contains(body, `"metric"`) {
		t.Fatalf("yanıt: %s", body)
	}
}

func TestRankingComponentRoutesValidateInput(t *testing.T) {
	f := &fakeRankings{out: &corev1.EventRanking{}}
	h := componentRouter(fakeServices{rankingComponentService: f})

	for name, body := range map[string]string{
		"boş gövde":   `{}`,
		"null değer":  `{"value":null}`,
		"metin değer": `{"value":"3"}`,
		"sıfır":       `{"value":0}`,
		"eksi":        `{"value":-1}`,
		"çok büyük":   `{"value":1000000.01}`,
		"fazla alan":  `{"value":1,"rank":1}`,
		"bozuk JSON":  `{"value":`,
	} {
		if w := as(h, "PUT", rankingEntryPath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// The path is checked before core is asked.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/tayfas/x/events/" + eventID + "/components/race/ranking"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Race/ranking"},
		{"PUT", rankingPath + "/entries/x"},
		{"DELETE", rankingPath + "/entries/x"},
	} {
		if w := as(h, tc.method, tc.path, `{"value":1}`); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call.
	for _, tc := range []struct{ method, path string }{
		{"GET", rankingPath}, {"PUT", rankingEntryPath}, {"DELETE", rankingEntryPath},
	} {
		if w := do(h, tc.method, tc.path, `{"value":1}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestRankingComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_RANKING_NOT_STARTED":     {codes.FailedPrecondition, http.StatusConflict, "ranking-not-started"},
		"ERROR_REASON_RANKING_NOT_PARTICIPANT": {codes.FailedPrecondition, http.StatusConflict, "ranking-not-participant"},
		"ERROR_REASON_RANKING_FROM_MEASURE":    {codes.FailedPrecondition, http.StatusConflict, "ranking-from-measure"},
		"ERROR_REASON_COMPONENT_FROZEN":        {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_PERMISSION_DENIED":       {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{rankingComponentService: &fakeRankings{err: coreErr(want.grpc, reason)}})
		if w := as(h, "PUT", rankingEntryPath, `{"value":1}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
