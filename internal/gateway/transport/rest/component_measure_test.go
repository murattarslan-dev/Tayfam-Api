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

const measureComponentService = "tayfam.core.v1.MeasureComponentService"

// fakeMeasures is core's MeasureComponentService for the measure tests.
type fakeMeasures struct {
	get         *corev1.GetMeasureRequest
	setTotal    *corev1.SetMeasureTotalRequest
	removeTotal *corev1.RemoveMeasureTotalRequest
	setEntry    *corev1.SetMeasureEntryRequest
	removeEntry *corev1.RemoveMeasureEntryRequest
	token       string
	out         *corev1.EventMeasure
	err         error
	calls       int
}

func (f *fakeMeasures) GetMeasure(ctx context.Context, in *corev1.GetMeasureRequest, _ ...grpc.CallOption) (*corev1.GetMeasureResponse, error) {
	f.get, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.GetMeasureResponse{Measure: f.out}, f.err
}

func (f *fakeMeasures) SetMeasureTotal(ctx context.Context, in *corev1.SetMeasureTotalRequest, _ ...grpc.CallOption) (*corev1.SetMeasureTotalResponse, error) {
	f.setTotal, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.SetMeasureTotalResponse{Measure: f.out}, f.err
}

func (f *fakeMeasures) RemoveMeasureTotal(ctx context.Context, in *corev1.RemoveMeasureTotalRequest, _ ...grpc.CallOption) (*corev1.RemoveMeasureTotalResponse, error) {
	f.removeTotal, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.RemoveMeasureTotalResponse{Measure: f.out}, f.err
}

func (f *fakeMeasures) SetMeasureEntry(ctx context.Context, in *corev1.SetMeasureEntryRequest, _ ...grpc.CallOption) (*corev1.SetMeasureEntryResponse, error) {
	f.setEntry, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.SetMeasureEntryResponse{Measure: f.out}, f.err
}

func (f *fakeMeasures) RemoveMeasureEntry(ctx context.Context, in *corev1.RemoveMeasureEntryRequest, _ ...grpc.CallOption) (*corev1.RemoveMeasureEntryResponse, error) {
	f.removeEntry, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.RemoveMeasureEntryResponse{Measure: f.out}, f.err
}

const (
	measurePath      = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/distance/measure"
	measureTotalPath = measurePath + "/total"
	measureEntryPath = measurePath + "/entries/" + userID
)

func TestMeasureComponentRoutes(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	total, suggested := 12.5, 0.0
	f := &fakeMeasures{out: &corev1.EventMeasure{
		Unit: "km", Scope: corev1.MeasureScope_MEASURE_SCOPE_TOTAL, EnteredBy: corev1.MeasureEnteredBy_MEASURE_ENTERED_BY_ORGANIZER,
		Max: 1000, Decimals: 2, Total: &total, TotalUpdateTime: timestamppb.New(at), Suggested: &suggested, CallerCanEdit: true,
	}}
	h := componentRouter(fakeServices{measureComponentService: f})

	w := as(h, "GET", measurePath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("okuma: %d %s", w.Code, w.Body)
	}
	if ref := f.get.GetRef(); ref.GetTayfaId() != tayfaID || ref.GetEventId() != eventID || ref.GetComponentKey() != "distance" || f.token != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", f.get, f.token)
	}
	got := decodeBody[eventMeasureJSON](t, w)
	if got.Unit != "km" || got.Scope != "total" || got.EnteredBy != "organizer" || got.Max != 1000 || got.Decimals != 2 ||
		got.Total == nil || *got.Total != 12.5 || got.TotalUpdatedAt == nil || !got.TotalUpdatedAt.Equal(at) || !got.CallerCanEdit {
		t.Fatalf("ölçüm: %+v", got)
	}
	// A suggestion of 0 is still a suggestion; entries are an empty array.
	if body := w.Body.String(); !strings.Contains(body, `"suggested":0`) || !strings.Contains(body, `"entries":[]`) || !strings.Contains(body, `"min":0`) {
		t.Fatalf("yanıt: %s", body)
	}

	if w := as(h, "PUT", measureTotalPath, `{"value":7.25}`); w.Code != http.StatusOK || f.setTotal.GetValue() != 7.25 || f.setTotal.GetRef().GetComponentKey() != "distance" {
		t.Fatalf("toplam yazma: %d %s %v", w.Code, w.Body, f.setTotal)
	}
	// Zero is a value, not a missing one.
	if w := as(h, "PUT", measureTotalPath, `{"value":0}`); w.Code != http.StatusOK || f.setTotal.GetValue() != 0 {
		t.Fatalf("sıfır: %d %s", w.Code, w.Body)
	}
	if w := as(h, "DELETE", measureTotalPath, ""); w.Code != http.StatusOK || f.removeTotal.GetRef().GetEventId() != eventID {
		t.Fatalf("toplam kaldırma: %d %s", w.Code, w.Body)
	}

	// A personal measure: no total, no suggestion, the entries.
	f.out = &corev1.EventMeasure{
		Unit: "goal", Scope: corev1.MeasureScope_MEASURE_SCOPE_PERSONAL, EnteredBy: corev1.MeasureEnteredBy_MEASURE_ENTERED_BY_SELF,
		Max: 50, Entries: []*corev1.MeasureEntry{{UserId: userID, Value: 2, UpdateTime: timestamppb.New(at)}},
	}
	w = as(h, "PUT", measureEntryPath, `{"value":2}`)
	if w.Code != http.StatusOK || f.setEntry.GetUserId() != userID || f.setEntry.GetValue() != 2 {
		t.Fatalf("kişisel yazma: %d %s %v", w.Code, w.Body, f.setEntry)
	}
	out := decodeBody[eventMeasureJSON](t, w)
	if out.Scope != "personal" || out.EnteredBy != "self" || out.Total != nil || out.Suggested != nil || len(out.Entries) != 1 ||
		out.Entries[0].UserID != userID || out.Entries[0].Value != 2 || out.Entries[0].UpdatedAt == nil {
		t.Fatalf("kişisel yanıt: %+v", out)
	}
	if body := w.Body.String(); strings.Contains(body, `"total"`) || strings.Contains(body, "suggested") {
		t.Fatalf("kişisel ölçümde toplam ya da öneri: %s", body)
	}
	if w := as(h, "DELETE", measureEntryPath, ""); w.Code != http.StatusOK || f.removeEntry.GetUserId() != userID {
		t.Fatalf("kişisel kaldırma: %d %s", w.Code, w.Body)
	}
}

func TestMeasureComponentRoutesValidateInput(t *testing.T) {
	f := &fakeMeasures{out: &corev1.EventMeasure{}}
	h := componentRouter(fakeServices{measureComponentService: f})

	for name, body := range map[string]string{
		"boş gövde":   `{}`,
		"null değer":  `{"value":null}`,
		"metin değer": `{"value":"3"}`,
		"çok büyük":   `{"value":1000000001}`,
		"çok küçük":   `{"value":-1000000001}`,
		"fazla alan":  `{"value":1,"user_id":"x"}`,
		"bozuk JSON":  `{"value":`,
	} {
		for _, path := range []string{measureTotalPath, measureEntryPath} {
			if w := as(h, "PUT", path, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
				t.Errorf("%s %s: %d %s", name, path, w.Code, w.Body)
			}
		}
	}
	// The path is checked before core is asked.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/tayfas/x/events/" + eventID + "/components/distance/measure"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Distance/measure"},
		{"PUT", measurePath + "/entries/x"},
		{"DELETE", measurePath + "/entries/x"},
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
		{"GET", measurePath}, {"PUT", measureTotalPath}, {"DELETE", measureTotalPath}, {"PUT", measureEntryPath}, {"DELETE", measureEntryPath},
	} {
		if w := do(h, tc.method, tc.path, `{"value":1}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestMeasureComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_MEASUREMENT_NOT_STARTED":     {codes.FailedPrecondition, http.StatusConflict, "measurement-not-started"},
		"ERROR_REASON_MEASUREMENT_NOT_PARTICIPANT": {codes.FailedPrecondition, http.StatusConflict, "measurement-not-participant"},
		"ERROR_REASON_MEASUREMENT_SCOPE":           {codes.FailedPrecondition, http.StatusConflict, "measurement-scope"},
		"ERROR_REASON_NOT_ATTENDED":                {codes.FailedPrecondition, http.StatusConflict, "not-attended"},
		"ERROR_REASON_COMPONENT_FROZEN":            {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_PERMISSION_DENIED":           {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{measureComponentService: &fakeMeasures{err: coreErr(want.grpc, reason)}})
		if w := as(h, "PUT", measureEntryPath, `{"value":1}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
