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

const scheduleComponentService = "tayfam.core.v1.ScheduleComponentService"

// fakeSchedule is core's ScheduleComponentService for the schedule tests.
type fakeSchedule struct {
	got   proto.Message
	token string
	out   *corev1.Schedule
	err   error
	calls int
}

func (f *fakeSchedule) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func (f *fakeSchedule) GetSchedule(ctx context.Context, in *corev1.GetScheduleRequest, _ ...grpc.CallOption) (*corev1.GetScheduleResponse, error) {
	f.rec(ctx, in)
	return &corev1.GetScheduleResponse{Schedule: f.out}, f.err
}

func (f *fakeSchedule) AddScheduleItem(ctx context.Context, in *corev1.AddScheduleItemRequest, _ ...grpc.CallOption) (*corev1.AddScheduleItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.AddScheduleItemResponse{Schedule: f.out}, f.err
}

func (f *fakeSchedule) UpdateScheduleItem(ctx context.Context, in *corev1.UpdateScheduleItemRequest, _ ...grpc.CallOption) (*corev1.UpdateScheduleItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.UpdateScheduleItemResponse{Schedule: f.out}, f.err
}

func (f *fakeSchedule) RemoveScheduleItem(ctx context.Context, in *corev1.RemoveScheduleItemRequest, _ ...grpc.CallOption) (*corev1.RemoveScheduleItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.RemoveScheduleItemResponse{Schedule: f.out}, f.err
}

const (
	schedulePath     = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/schedule/schedule"
	scheduleItems    = schedulePath + "/items"
	scheduleItemPath = scheduleItems + "/" + itemID
)

var scheduleStart = time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)

func testSchedule() *corev1.Schedule {
	return &corev1.Schedule{
		Items: []*corev1.ScheduleItem{
			{Id: itemID, StartTime: timestamppb.New(scheduleStart), EndTime: timestamppb.New(scheduleStart.Add(time.Hour)), Title: "Söyleşi", Note: "Salon B"},
			{Id: eventID, StartTime: timestamppb.New(scheduleStart.Add(2 * time.Hour)), Title: "Öğle yemeği"},
		},
		MaxItems: 30, CallerCanEdit: true,
	}
}

func TestScheduleComponentRoutes(t *testing.T) {
	f := &fakeSchedule{out: testSchedule()}
	h := componentRouter(fakeServices{scheduleComponentService: f})
	ref := &corev1.ComponentRef{TayfaId: tayfaID, EventId: eventID, ComponentKey: "schedule"}
	start, end := timestamppb.New(scheduleStart), timestamppb.New(scheduleStart.Add(time.Hour))
	full := `{"starts_at":"2026-11-22T10:00:00+03:00","ends_at":"2026-11-22T08:00:00Z","title":"Söyleşi","note":"Salon B"}`
	bare := `{"starts_at":"2026-11-22T07:00:00Z","title":"Söyleşi"}`

	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", schedulePath, "", http.StatusOK, &corev1.GetScheduleRequest{Ref: ref}},
		{"POST", scheduleItems, full, http.StatusCreated, &corev1.AddScheduleItemRequest{Ref: ref,
			Item: &corev1.ScheduleItemInput{StartTime: start, EndTime: end, Title: "Söyleşi", Note: "Salon B"}}},
		{"POST", scheduleItems, bare, http.StatusCreated, &corev1.AddScheduleItemRequest{Ref: ref,
			Item: &corev1.ScheduleItemInput{StartTime: start, Title: "Söyleşi"}}},
		{"PUT", scheduleItemPath, full, http.StatusOK, &corev1.UpdateScheduleItemRequest{Ref: ref, ItemId: itemID,
			Item: &corev1.ScheduleItemInput{StartTime: start, EndTime: end, Title: "Söyleşi", Note: "Salon B"}}},
		{"PUT", scheduleItemPath, `{"starts_at":"2026-11-22T07:00:00Z","ends_at":null,"title":"Söyleşi","note":null}`, http.StatusOK,
			&corev1.UpdateScheduleItemRequest{Ref: ref, ItemId: itemID, Item: &corev1.ScheduleItemInput{StartTime: start, Title: "Söyleşi"}}},
		{"DELETE", scheduleItemPath, "", http.StatusOK, &corev1.RemoveScheduleItemRequest{Ref: ref, ItemId: itemID}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s %s: core'a giden %v (%q), beklenen %v", tc.method, tc.path, f.got, f.token, tc.want)
		}
		got := decodeBody[scheduleJSON](t, w)
		if len(got.Items) != 2 || got.MaxItems != 30 || !got.CallerCanEdit {
			t.Fatalf("%s %s: yanıt %+v", tc.method, tc.path, got)
		}
		if it := got.Items[0]; it.ID != itemID || !it.StartsAt.Equal(scheduleStart) || it.EndsAt == nil ||
			!it.EndsAt.Equal(scheduleStart.Add(time.Hour)) || it.Title != "Söyleşi" || it.Note != "Salon B" {
			t.Fatalf("%s %s: madde %+v", tc.method, tc.path, it)
		}
	}

	// An item without an end or a note has neither key.
	w := as(h, "GET", schedulePath, "")
	if want := `{"id":"` + eventID + `","starts_at":"2026-11-22T09:00:00Z","title":"Öğle yemeği"}`; !strings.Contains(w.Body.String(), want) {
		t.Fatalf("bitişsiz madde: %s", w.Body)
	}
	// An empty programme: an empty array.
	f.out = &corev1.Schedule{MaxItems: 100}
	if w := as(h, "GET", schedulePath, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("boş program: %d %s", w.Code, w.Body)
	}
}

func TestScheduleComponentRoutesValidateInput(t *testing.T) {
	f := &fakeSchedule{out: &corev1.Schedule{}}
	h := componentRouter(fakeServices{scheduleComponentService: f})
	for name, body := range map[string]string{
		"boş gövde":       `{}`,
		"saatsiz":         `{"title":"Varış"}`,
		"null saat":       `{"starts_at":null,"title":"Varış"}`,
		"bozuk saat":      `{"starts_at":"yarın","title":"Varış"}`,
		"bitiş önce":      `{"starts_at":"2026-11-22T07:00:00Z","ends_at":"2026-11-22T06:00:00Z","title":"Varış"}`,
		"bitiş aynı":      `{"starts_at":"2026-11-22T07:00:00Z","ends_at":"2026-11-22T07:00:00Z","title":"Varış"}`,
		"başlıksız":       `{"starts_at":"2026-11-22T07:00:00Z"}`,
		"boş başlık":      `{"starts_at":"2026-11-22T07:00:00Z","title":" "}`,
		"uzun başlık":     `{"starts_at":"2026-11-22T07:00:00Z","title":"` + strings.Repeat("ç", 121) + `"}`,
		"uzun not":        `{"starts_at":"2026-11-22T07:00:00Z","title":"Varış","note":"` + strings.Repeat("ş", 501) + `"}`,
		"sayı başlık":     `{"starts_at":"2026-11-22T07:00:00Z","title":5}`,
		"bilinmeyen alan": `{"starts_at":"2026-11-22T07:00:00Z","title":"Varış","position":1}`,
		"bozuk JSON":      `{"title":`,
		"dizi gövde":      `[]`,
		"null gövde":      `null`,
	} {
		if w := as(h, "POST", scheduleItems, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("ekleme %s: %d %s", name, w.Code, w.Body)
		}
		if w := as(h, "PUT", scheduleItemPath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("düzenleme %s: %d %s", name, w.Code, w.Body)
		}
	}
	// The path is checked before core is asked.
	valid := `{"starts_at":"2026-11-22T07:00:00Z","title":"Varış"}`
	for _, rt := range []struct{ method, path string }{
		{"GET", "/v1/tayfas/x/events/" + eventID + "/components/schedule/schedule"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/x/components/schedule/schedule"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Schedule/schedule"},
		{"PUT", scheduleItems + "/x"},
		{"DELETE", scheduleItems + "/x"},
	} {
		if w := as(h, rt.method, rt.path, valid); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call; a write without the client header is refused.
	for _, rt := range []struct{ method, path string }{
		{"GET", schedulePath}, {"POST", scheduleItems}, {"PUT", scheduleItemPath}, {"DELETE", scheduleItemPath},
	} {
		if w := do(h, rt.method, rt.path, valid, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", rt.method, rt.path, w.Code)
		}
		if rt.method == "GET" {
			continue
		}
		if w := do(h, rt.method, rt.path, valid, map[string]string{"Authorization": "Bearer T"}); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
			t.Fatalf("istemci başlıksız %s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestScheduleComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_COMPONENT_NOT_FOUND":  {codes.NotFound, http.StatusNotFound, "component-not-found"},
		"ERROR_REASON_COMPONENT_DISABLED":   {codes.FailedPrecondition, http.StatusConflict, "component-disabled"},
		"ERROR_REASON_COMPONENT_FROZEN":     {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_EVENT_ITEM_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "event-item-not-found"},
		"ERROR_REASON_EVENT_LIMIT":          {codes.ResourceExhausted, http.StatusConflict, "event-limit"},
		"ERROR_REASON_PERMISSION_DENIED":    {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{scheduleComponentService: &fakeSchedule{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", scheduleItems, `{"starts_at":"2026-11-22T07:00:00Z","title":"Varış"}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
