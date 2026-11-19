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

const goalComponentService = "tayfam.core.v1.GoalComponentService"

// fakeGoals is core's GoalComponentService for the goal tests.
type fakeGoals struct {
	get    *corev1.GetGoalRequest
	set    *corev1.SetGoalTargetRequest
	remove *corev1.RemoveGoalTargetRequest
	token  string
	out    *corev1.EventGoal
	err    error
	calls  int
}

func (f *fakeGoals) GetGoal(ctx context.Context, in *corev1.GetGoalRequest, _ ...grpc.CallOption) (*corev1.GetGoalResponse, error) {
	f.get, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.GetGoalResponse{Goal: f.out}, f.err
}

func (f *fakeGoals) SetGoalTarget(ctx context.Context, in *corev1.SetGoalTargetRequest, _ ...grpc.CallOption) (*corev1.SetGoalTargetResponse, error) {
	f.set, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.SetGoalTargetResponse{Goal: f.out}, f.err
}

func (f *fakeGoals) RemoveGoalTarget(ctx context.Context, in *corev1.RemoveGoalTargetRequest, _ ...grpc.CallOption) (*corev1.RemoveGoalTargetResponse, error) {
	f.remove, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.RemoveGoalTargetResponse{Goal: f.out}, f.err
}

const (
	goalPath       = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/goal/goal"
	goalTargetPath = goalPath + "/target"
)

func TestGoalComponentRoutes(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	target, def, cur := 150.0, 100.0, 0.0
	f := &fakeGoals{out: &corev1.EventGoal{
		MeasureKey: "weight", Unit: "kg", Decimals: 1, Target: &target, TargetSet: true, TargetUpdateTime: timestamppb.New(at),
		DefaultTarget: &def, Current: &cur, MeasureEnabled: true, CallerCanEdit: true,
	}}
	h := componentRouter(fakeServices{goalComponentService: f})

	w := as(h, "GET", goalPath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("okuma: %d %s", w.Code, w.Body)
	}
	if ref := f.get.GetRef(); ref.GetTayfaId() != tayfaID || ref.GetEventId() != eventID || ref.GetComponentKey() != "goal" || f.token != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", f.get, f.token)
	}
	got := decodeBody[eventGoalJSON](t, w)
	if got.MeasureKey != "weight" || got.Unit != "kg" || got.Decimals != 1 || got.Target == nil || *got.Target != 150 || !got.TargetSet ||
		got.TargetUpdatedAt == nil || !got.TargetUpdatedAt.Equal(at) || got.DefaultTarget == nil || *got.DefaultTarget != 100 ||
		!got.MeasureEnabled || got.ProgressHidden || got.Reached || !got.CallerCanEdit {
		t.Fatalf("hedef: %+v", got)
	}
	// A progress of 0 is still progress.
	if body := w.Body.String(); !strings.Contains(body, `"current":0`) {
		t.Fatalf("yanıt: %s", body)
	}

	if w := as(h, "PUT", goalTargetPath, `{"target":7.5}`); w.Code != http.StatusOK || f.set.GetTarget() != 7.5 || f.set.GetRef().GetComponentKey() != "goal" {
		t.Fatalf("hedef yazma: %d %s %v", w.Code, w.Body, f.set)
	}

	// Without a target and with hidden progress the fields are absent.
	f.out = &corev1.EventGoal{MeasureKey: "net", Unit: "net", ProgressHidden: true, MeasureEnabled: true}
	w = as(h, "DELETE", goalTargetPath, "")
	if w.Code != http.StatusOK || f.remove.GetRef().GetEventId() != eventID {
		t.Fatalf("hedef kaldırma: %d %s", w.Code, w.Body)
	}
	for _, field := range []string{`"target"`, `"default_target"`, `"current"`, `"target_updated_at"`} {
		if strings.Contains(w.Body.String(), field) {
			t.Fatalf("gizli ilerlemede %s: %s", field, w.Body)
		}
	}
	if out := decodeBody[eventGoalJSON](t, w); !out.ProgressHidden {
		t.Fatalf("gizli ilerleme: %+v", out)
	}
}

func TestGoalComponentRoutesValidateInput(t *testing.T) {
	f := &fakeGoals{out: &corev1.EventGoal{}}
	h := componentRouter(fakeServices{goalComponentService: f})

	for name, body := range map[string]string{
		"boş gövde":   `{}`,
		"null hedef":  `{"target":null}`,
		"metin hedef": `{"target":"3"}`,
		"sıfır":       `{"target":0}`,
		"eksi":        `{"target":-1}`,
		"çok büyük":   `{"target":1000000001}`,
		"fazla alan":  `{"target":1,"value":2}`,
		"bozuk JSON":  `{"target":`,
	} {
		if w := as(h, "PUT", goalTargetPath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// The path is checked before core is asked.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/tayfas/x/events/" + eventID + "/components/goal/goal"},
		{"PUT", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Goal/goal/target"},
	} {
		if w := as(h, tc.method, tc.path, `{"target":1}`); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call.
	for _, tc := range []struct{ method, path string }{{"GET", goalPath}, {"PUT", goalTargetPath}, {"DELETE", goalTargetPath}} {
		if w := do(h, tc.method, tc.path, `{"target":1}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestGoalComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_COMPONENT_FROZEN":  {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_PERMISSION_DENIED": {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{goalComponentService: &fakeGoals{err: coreErr(want.grpc, reason)}})
		if w := as(h, "PUT", goalTargetPath, `{"target":1}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
