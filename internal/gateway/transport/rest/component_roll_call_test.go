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

const rollCallComponentService = "tayfam.core.v1.RollCallComponentService"

// fakeRollCalls is core's RollCallComponentService for the roll call tests.
type fakeRollCalls struct {
	get    *corev1.GetRollCallRequest
	plan   *corev1.SetExpectedReturnRequest
	start  *corev1.StartRollCallRequest
	back   *corev1.ReturnRollCallRequest
	token  string
	out    *corev1.EventRollCall
	err    error
	calls  int
	method string
}

func (f *fakeRollCalls) seen(ctx context.Context, method string) {
	f.token, f.method, f.calls = tokenFrom(ctx), method, f.calls+1
}

func (f *fakeRollCalls) GetRollCall(ctx context.Context, in *corev1.GetRollCallRequest, _ ...grpc.CallOption) (*corev1.GetRollCallResponse, error) {
	f.get = in
	f.seen(ctx, "get")
	return &corev1.GetRollCallResponse{RollCall: f.out}, f.err
}

func (f *fakeRollCalls) SetExpectedReturn(ctx context.Context, in *corev1.SetExpectedReturnRequest, _ ...grpc.CallOption) (*corev1.SetExpectedReturnResponse, error) {
	f.plan = in
	f.seen(ctx, "plan")
	return &corev1.SetExpectedReturnResponse{RollCall: f.out}, f.err
}

func (f *fakeRollCalls) StartRollCall(ctx context.Context, in *corev1.StartRollCallRequest, _ ...grpc.CallOption) (*corev1.StartRollCallResponse, error) {
	f.start = in
	f.seen(ctx, "start")
	return &corev1.StartRollCallResponse{RollCall: f.out}, f.err
}

func (f *fakeRollCalls) ReturnRollCall(ctx context.Context, in *corev1.ReturnRollCallRequest, _ ...grpc.CallOption) (*corev1.ReturnRollCallResponse, error) {
	f.back = in
	f.seen(ctx, "return")
	return &corev1.ReturnRollCallResponse{RollCall: f.out}, f.err
}

// rollCallOther is another participant.
const rollCallOther = "01930000-0000-7000-8000-0000000000ab"

const rollCallPath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/roll_call/roll-call"

func TestRollCallComponentRoutes(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	f := &fakeRollCalls{out: &corev1.EventRollCall{
		Alarm: true, ReturnGraceMinutes: 30, ExpectedReturnTime: timestamppb.New(at.Add(6 * time.Hour)),
		Mine: &corev1.RollCallEntry{UserId: userID, StartTime: timestamppb.New(at)},
		Entries: []*corev1.RollCallEntry{
			{UserId: userID, StartTime: timestamppb.New(at)},
			{UserId: rollCallOther, StartTime: timestamppb.New(at), ReturnTime: timestamppb.New(at.Add(time.Hour))},
		},
		CallerCanManage: true, CallerCanEdit: true,
	}}
	h := componentRouter(fakeServices{rollCallComponentService: f})

	w := as(h, "GET", rollCallPath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("okuma: %d %s", w.Code, w.Body)
	}
	if ref := f.get.GetRef(); ref.GetTayfaId() != tayfaID || ref.GetEventId() != eventID || ref.GetComponentKey() != "roll_call" || f.token != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", f.get, f.token)
	}
	got := decodeBody[eventRollCallJSON](t, w)
	if !got.Alarm || got.ReturnGraceMinutes != 30 || got.ExpectedReturnAt == nil || !got.ExpectedReturnAt.Equal(at.Add(6*time.Hour)) ||
		got.Mine == nil || got.Mine.UserID != userID || got.Mine.ReturnedAt != nil || len(got.Entries) != 2 ||
		got.Entries[1].ReturnedAt == nil || !got.Entries[1].ReturnedAt.Equal(at.Add(time.Hour)) || !got.CallerCanManage || !got.CallerCanEdit {
		t.Fatalf("yoklama: %+v", got)
	}

	// Times travel as given; no time is "now" (start, return) or "no plan".
	for _, tc := range []struct {
		method, path, body, call string
		at                       func() *timestamppb.Timestamp
		want                     *timestamppb.Timestamp
	}{
		{"PUT", "/expected-return", `{"expected_return_at":"2026-11-19T15:00:00Z"}`, "plan", func() *timestamppb.Timestamp { return f.plan.GetExpectedReturnTime() }, timestamppb.New(at.Add(6 * time.Hour))},
		{"PUT", "/expected-return", `{"expected_return_at":null}`, "plan", func() *timestamppb.Timestamp { return f.plan.GetExpectedReturnTime() }, nil},
		{"PUT", "/expected-return", `{}`, "plan", func() *timestamppb.Timestamp { return f.plan.GetExpectedReturnTime() }, nil},
		{"POST", "/start", `{"started_at":"2026-11-19T09:00:00Z"}`, "start", func() *timestamppb.Timestamp { return f.start.GetStartTime() }, timestamppb.New(at)},
		{"POST", "/start", `{}`, "start", func() *timestamppb.Timestamp { return f.start.GetStartTime() }, nil},
		{"POST", "/return", `{"returned_at":"2026-11-19T09:00:00Z"}`, "return", func() *timestamppb.Timestamp { return f.back.GetReturnTime() }, timestamppb.New(at)},
		{"POST", "/return", `{}`, "return", func() *timestamppb.Timestamp { return f.back.GetReturnTime() }, nil},
	} {
		w := as(h, tc.method, rollCallPath+tc.path, tc.body)
		if w.Code != http.StatusOK || f.method != tc.call {
			t.Fatalf("%s %s: %d %s", tc.path, tc.body, w.Code, w.Body)
		}
		if g := tc.at(); (g == nil) != (tc.want == nil) || (g != nil && !g.AsTime().Equal(tc.want.AsTime())) {
			t.Errorf("%s %s: core'a giden zaman %v", tc.path, tc.body, g)
		}
		if out := decodeBody[eventRollCallJSON](t, w); len(out.Entries) != 2 {
			t.Errorf("%s yanıtı: %+v", tc.path, out)
		}
	}

	// Without a plan or a record the answer leaves them out; entries is an
	// empty array.
	f.out = &corev1.EventRollCall{ReturnGraceMinutes: 30}
	w = as(h, "GET", rollCallPath, "")
	if b := w.Body.String(); w.Code != http.StatusOK || strings.Contains(b, "expected_return_at") || strings.Contains(b, "mine") || !strings.Contains(b, `"entries":[]`) {
		t.Fatalf("plansız: %d %s", w.Code, b)
	}
}

func TestRollCallComponentRoutesValidateInput(t *testing.T) {
	f := &fakeRollCalls{out: &corev1.EventRollCall{}}
	h := componentRouter(fakeServices{rollCallComponentService: f})
	for name, tc := range map[string]struct{ method, path, body string }{
		"bozuk beklenen dönüş": {"PUT", "/expected-return", `{"expected_return_at":"yarın"}`},
		"sayı beklenen dönüş":  {"PUT", "/expected-return", `{"expected_return_at":5}`},
		"planda fazla alan":    {"PUT", "/expected-return", `{"expected_return_at":null,"return_grace_minutes":10}`},
		"bozuk çıkış":          {"POST", "/start", `{"started_at":"dün"}`},
		"çıkışta konum":        {"POST", "/start", `{"location":{"lat":1,"lng":2}}`},
		"bozuk dönüş":          {"POST", "/return", `{"returned_at":"az önce"}`},
		"bozuk JSON":           {"POST", "/return", `{`},
	} {
		if w := as(h, tc.method, rollCallPath+tc.path, tc.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// The path is checked before core is asked.
	for _, path := range []string{
		"/v1/tayfas/x/events/" + eventID + "/components/roll_call/roll-call",
		"/v1/tayfas/" + tayfaID + "/events/x/components/roll_call/roll-call",
		"/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Roll/roll-call",
	} {
		if w := as(h, "GET", path, ""); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// Writes need the client header; no token, no call.
	for _, p := range []string{"/start", "/return"} {
		w := do(h, "POST", rollCallPath+p, `{}`, map[string]string{"Authorization": "Bearer T", "Content-Type": "application/json"})
		if w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
			t.Errorf("başlıksız %s: %d", p, w.Code)
		}
		if w := do(h, "POST", rollCallPath+p, `{}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Errorf("token'sız %s: %d", p, w.Code)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestRollCallComponentErrors(t *testing.T) {
	for _, tc := range []struct {
		path, reason string
		grpc         codes.Code
		code         int
		typ          string
	}{
		{"/return", "ERROR_REASON_HIKE_NOT_STARTED", codes.FailedPrecondition, http.StatusConflict, "hike-not-started"},
		{"/return", "ERROR_REASON_TAYFA_READ_ONLY", codes.FailedPrecondition, http.StatusConflict, "tayfa-read-only"},
		{"/start", "ERROR_REASON_CHECK_IN_CLOSED", codes.FailedPrecondition, http.StatusConflict, "check-in-closed"},
		{"/start", "ERROR_REASON_EVENT_NOT_GOING", codes.FailedPrecondition, http.StatusConflict, "event-not-going"},
		{"/expected-return", "ERROR_REASON_COMPONENT_FROZEN", codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		{"/expected-return", "ERROR_REASON_PERMISSION_DENIED", codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{rollCallComponentService: &fakeRollCalls{err: coreErr(tc.grpc, tc.reason)}})
		method := "POST"
		if tc.path == "/expected-return" {
			method = "PUT"
		}
		if w := as(h, method, rollCallPath+tc.path, `{}`); w.Code != tc.code || problemType(t, w) != tc.typ {
			t.Errorf("%s %s: %d %s", tc.path, tc.reason, w.Code, w.Body)
		}
	}
}
