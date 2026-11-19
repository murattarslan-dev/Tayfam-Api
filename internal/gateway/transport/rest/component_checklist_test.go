package rest

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const checklistComponentService = "tayfam.core.v1.ChecklistComponentService"

// fakeChecklist is core's ChecklistComponentService for the list tests.
type fakeChecklist struct {
	got   proto.Message
	token string
	out   *corev1.Checklist
	err   error
	calls int
}

func (f *fakeChecklist) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func (f *fakeChecklist) ListChecklist(ctx context.Context, in *corev1.ListChecklistRequest, _ ...grpc.CallOption) (*corev1.ListChecklistResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListChecklistResponse{Checklist: f.out}, f.err
}

func (f *fakeChecklist) AddChecklistItem(ctx context.Context, in *corev1.AddChecklistItemRequest, _ ...grpc.CallOption) (*corev1.AddChecklistItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.AddChecklistItemResponse{Checklist: f.out}, f.err
}

func (f *fakeChecklist) RemoveChecklistItem(ctx context.Context, in *corev1.RemoveChecklistItemRequest, _ ...grpc.CallOption) (*corev1.RemoveChecklistItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.RemoveChecklistItemResponse{Checklist: f.out}, f.err
}

func (f *fakeChecklist) AssignChecklistItem(ctx context.Context, in *corev1.AssignChecklistItemRequest, _ ...grpc.CallOption) (*corev1.AssignChecklistItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.AssignChecklistItemResponse{Checklist: f.out}, f.err
}

const (
	checklistPath     = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/gear/items"
	checklistItemPath = checklistPath + "/" + itemID
)

func testChecklist() *corev1.Checklist {
	return &corev1.Checklist{
		Items: []*corev1.ChecklistItem{
			{Id: itemID, Name: "Çadır", Quantity: 2, AssignedUserId: userID},
			{Id: eventID, Name: "Ocak", Quantity: 1},
		},
		Label: "gear", AssignmentEnabled: true, QuantityEnabled: true, CallerCanTake: true,
	}
}

func TestChecklistComponentRoutes(t *testing.T) {
	f := &fakeChecklist{out: testChecklist()}
	h := componentRouter(fakeServices{checklistComponentService: f})
	ref := &corev1.ComponentRef{TayfaId: tayfaID, EventId: eventID, ComponentKey: "gear"}

	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", checklistPath, "", http.StatusOK, &corev1.ListChecklistRequest{Ref: ref}},
		{"POST", checklistPath, `{"name":"Çadır","quantity":2,"assigned_user_id":"` + userID + `"}`, http.StatusCreated,
			&corev1.AddChecklistItemRequest{Ref: ref, Name: "Çadır", Quantity: 2, AssignedUserId: userID}},
		{"POST", checklistPath, `{"name":"Ocak"}`, http.StatusCreated, &corev1.AddChecklistItemRequest{Ref: ref, Name: "Ocak"}},
		{"POST", checklistPath, `{"name":"Ocak","assigned_user_id":null}`, http.StatusCreated, &corev1.AddChecklistItemRequest{Ref: ref, Name: "Ocak"}},
		{"DELETE", checklistItemPath, "", http.StatusOK, &corev1.RemoveChecklistItemRequest{Ref: ref, ItemId: itemID}},
		{"PUT", checklistItemPath + "/assignee", `{"assigned_user_id":"` + userID + `"}`, http.StatusOK,
			&corev1.AssignChecklistItemRequest{Ref: ref, ItemId: itemID, AssignedUserId: userID}},
		{"PUT", checklistItemPath + "/assignee", `{"assigned_user_id":null}`, http.StatusOK, &corev1.AssignChecklistItemRequest{Ref: ref, ItemId: itemID}},
		{"PUT", checklistItemPath + "/assignee", `{}`, http.StatusOK, &corev1.AssignChecklistItemRequest{Ref: ref, ItemId: itemID}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s %s: core'a giden %v (%q), beklenen %v", tc.method, tc.path, f.got, f.token, tc.want)
		}
		got := decodeBody[checklistJSON](t, w)
		if len(got.Items) != 2 || got.Label != "gear" || !got.AssignmentEnabled || !got.QuantityEnabled || !got.CallerCanTake || got.CallerCanManage {
			t.Fatalf("%s %s: yanıt %+v", tc.method, tc.path, got)
		}
		if it := got.Items[0]; it.ID != itemID || it.Name != "Çadır" || it.Quantity != 2 || it.AssignedUserID != userID {
			t.Fatalf("%s %s: madde %+v", tc.method, tc.path, it)
		}
	}

	// A free item has no assignee key.
	w := as(h, "GET", checklistPath, "")
	if want := `{"id":"` + eventID + `","name":"Ocak","quantity":1}`; !strings.Contains(w.Body.String(), want) {
		t.Fatalf("boş madde: %s", w.Body)
	}
	// An empty list: an empty array.
	f.out = &corev1.Checklist{Label: "item"}
	if w := as(h, "GET", checklistPath, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("boş liste: %d %s", w.Code, w.Body)
	}
}

func TestChecklistComponentRoutesValidateInput(t *testing.T) {
	f := &fakeChecklist{out: &corev1.Checklist{}}
	h := componentRouter(fakeServices{checklistComponentService: f})

	for name, body := range map[string]string{
		"boş gövde":  `{}`,
		"boş ad":     `{"name":" "}`,
		"uzun ad":    `{"name":"` + strings.Repeat("ç", 61) + `"}`,
		"eksi adet":  `{"name":"Çadır","quantity":-1}`,
		"büyük adet": `{"name":"Çadır","quantity":1000}`,
		"metin adet": `{"name":"Çadır","quantity":"2"}`,
		"bozuk kişi": `{"name":"Çadır","assigned_user_id":"x"}`,
		"sayı kişi":  `{"name":"Çadır","assigned_user_id":5}`,
		"fazla alan": `{"name":"Çadır","price":10}`,
		"bozuk JSON": `{"name":`,
		"dizi gövde": `[]`,
		"null gövde": `null`,
	} {
		if w := as(h, "POST", checklistPath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	for name, body := range map[string]string{
		"bozuk kişi":      `{"assigned_user_id":"x"}`,
		"sayı kişi":       `{"assigned_user_id":5}`,
		"bilinmeyen alan": `{"user_id":"` + userID + `"}`,
	} {
		if w := as(h, "PUT", checklistItemPath+"/assignee", body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("atama %s: %d %s", name, w.Code, w.Body)
		}
	}
	// The path is checked before core is asked.
	for _, rt := range []struct{ method, path string }{
		{"GET", "/v1/tayfas/x/events/" + eventID + "/components/gear/items"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/x/components/gear/items"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Gear/items"},
		{"DELETE", checklistPath + "/x"},
		{"PUT", checklistPath + "/x/assignee"},
	} {
		if w := as(h, rt.method, rt.path, `{}`); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call; a write without the client header is refused.
	for _, rt := range []struct{ method, path string }{
		{"GET", checklistPath}, {"POST", checklistPath}, {"DELETE", checklistItemPath}, {"PUT", checklistItemPath + "/assignee"},
	} {
		if w := do(h, rt.method, rt.path, `{"name":"Çadır"}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", rt.method, rt.path, w.Code)
		}
		if rt.method == "GET" {
			continue
		}
		if w := do(h, rt.method, rt.path, `{"name":"Çadır"}`, map[string]string{"Authorization": "Bearer T"}); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
			t.Fatalf("istemci başlıksız %s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestChecklistComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_COMPONENT_NOT_FOUND":  {codes.NotFound, http.StatusNotFound, "component-not-found"},
		"ERROR_REASON_COMPONENT_FROZEN":     {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_EVENT_NOT_GOING":      {codes.FailedPrecondition, http.StatusConflict, "event-not-going"},
		"ERROR_REASON_EVENT_ENDED":          {codes.FailedPrecondition, http.StatusConflict, "event-ended"},
		"ERROR_REASON_EVENT_ITEM_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "event-item-not-found"},
		"ERROR_REASON_EVENT_ITEM_TAKEN":     {codes.Aborted, http.StatusConflict, "event-item-taken"},
		"ERROR_REASON_EVENT_LIMIT":          {codes.ResourceExhausted, http.StatusConflict, "event-limit"},
		"ERROR_REASON_PERMISSION_DENIED":    {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{checklistComponentService: &fakeChecklist{err: coreErr(want.grpc, reason)}})
		if w := as(h, "PUT", checklistItemPath+"/assignee", `{"assigned_user_id":"`+userID+`"}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
