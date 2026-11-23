package rest

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const drawComponentService = "tayfam.core.v1.DrawComponentService"

// fakeDraws is core's DrawComponentService for the draw tests.
type fakeDraws struct {
	got   proto.Message
	token string
	out   *corev1.Draw
	err   error
	calls int
}

func (f *fakeDraws) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func (f *fakeDraws) GetDraw(ctx context.Context, in *corev1.GetDrawRequest, _ ...grpc.CallOption) (*corev1.GetDrawResponse, error) {
	f.rec(ctx, in)
	return &corev1.GetDrawResponse{Draw: f.out}, f.err
}

func (f *fakeDraws) MakeDraw(ctx context.Context, in *corev1.MakeDrawRequest, _ ...grpc.CallOption) (*corev1.MakeDrawResponse, error) {
	f.rec(ctx, in)
	return &corev1.MakeDrawResponse{Draw: f.out}, f.err
}

const drawPath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/draw/draw"

var drawnAt = time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)

func testDraw() *corev1.Draw {
	return &corev1.Draw{
		Drawn: true, Subject: "participants", DrawnTime: timestamppb.New(drawnAt),
		Entries: []*corev1.DrawEntry{{Position: 1, UserId: userID}, {Position: 2}},
	}
}

func TestDrawComponentRoutes(t *testing.T) {
	f := &fakeDraws{out: testDraw()}
	h := componentRouter(fakeServices{drawComponentService: f})
	ref := &corev1.ComponentRef{TayfaId: tayfaID, EventId: eventID, ComponentKey: "draw"}

	for _, tc := range []struct {
		method, body string
		want         proto.Message
	}{
		{"GET", "", &corev1.GetDrawRequest{Ref: ref}},
		{"POST", "", &corev1.MakeDrawRequest{Ref: ref}},
		// A body is not read.
		{"POST", `{}`, &corev1.MakeDrawRequest{Ref: ref}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, drawPath, tc.body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.method, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s: core'a giden %v (%q), beklenen %v", tc.method, f.got, f.token, tc.want)
		}
		got := decodeBody[drawJSON](t, w)
		if !got.Drawn || got.Subject != "participants" || got.TeamsKey != "" || got.DrawnAt == nil || !got.DrawnAt.Equal(drawnAt) ||
			got.CallerCanDraw || len(got.Entries) != 2 || got.Entries[0].UserID != userID || got.Entries[0].Position != 1 {
			t.Fatalf("%s: yanıt %+v", tc.method, got)
		}
	}

	// An empty place has neither ID.
	w := as(h, "GET", drawPath, "")
	if want := `{"position":2}`; !strings.Contains(w.Body.String(), want) {
		t.Fatalf("boş yer: %s", w.Body)
	}
	// Before the draw: no time, an empty list.
	f.out = &corev1.Draw{Subject: "teams", TeamsKey: "teams", CallerCanDraw: true}
	w = as(h, "GET", drawPath, "")
	if b := w.Body.String(); w.Code != http.StatusOK || !strings.Contains(b, `"entries":[]`) || strings.Contains(b, "drawn_at") ||
		!strings.Contains(b, `"teams_key":"teams"`) || !strings.Contains(b, `"caller_can_draw":true`) {
		t.Fatalf("çekilmemiş: %d %s", w.Code, b)
	}
}

func TestDrawComponentRoutesValidateInput(t *testing.T) {
	f := &fakeDraws{out: &corev1.Draw{}}
	h := componentRouter(fakeServices{drawComponentService: f})
	// The path is checked before core is asked.
	for _, rt := range []struct{ method, path string }{
		{"GET", "/v1/tayfas/x/events/" + eventID + "/components/draw/draw"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/x/components/draw/draw"},
		{"POST", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Draw/draw"},
	} {
		if w := as(h, rt.method, rt.path, ""); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call; a draw without the client header is refused.
	for _, method := range []string{"GET", "POST"} {
		if w := do(h, method, drawPath, "", map[string]string{clientHeader: "android"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s: %d", method, w.Code)
		}
	}
	if w := do(h, "POST", drawPath, "", map[string]string{"Authorization": "Bearer T"}); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
		t.Fatalf("istemci başlıksız: %d %s", w.Code, w.Body)
	}
}

// core's refusals reach the client as their problem types.
func TestDrawComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_DRAW_ALREADY_MADE": {codes.FailedPrecondition, http.StatusConflict, "draw-already-made"},
		"ERROR_REASON_DRAW_TOO_FEW":      {codes.FailedPrecondition, http.StatusConflict, "draw-too-few"},
		"ERROR_REASON_COMPONENT_FROZEN":  {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_EVENT_LIMIT":       {codes.ResourceExhausted, http.StatusConflict, "event-limit"},
		"ERROR_REASON_PERMISSION_DENIED": {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{drawComponentService: &fakeDraws{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", drawPath, ""); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
