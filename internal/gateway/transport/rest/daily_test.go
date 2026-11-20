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
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const dailyService = "tayfam.core.v1.TayfaDailyService"

// fakeDaily is core's TayfaDailyService for the daily content tests.
type fakeDaily struct {
	got   proto.Message
	token string
	err   error
	calls int
	empty bool
}

func (f *fakeDaily) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func dailyItemPB() *corev1.DailyItem {
	year := int32(2026)
	return &corev1.DailyItem{
		Id: itemID, Body: "Söz\nikinci satır", Attribution: "Atatürk", Position: 2,
		Pin: &corev1.DailyPin{Month: 11, Day: 10, Year: &year}, CreatedBy: userID,
		CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now(),
	}
}

func (f *fakeDaily) GetDailyToday(ctx context.Context, in *corev1.GetDailyTodayRequest, _ ...grpc.CallOption) (*corev1.GetDailyTodayResponse, error) {
	f.rec(ctx, in)
	if f.empty {
		return &corev1.GetDailyTodayResponse{Date: "2026-11-11"}, f.err
	}
	return &corev1.GetDailyTodayResponse{Item: dailyItemPB(), Pinned: true, Date: "2026-11-10"}, f.err
}

func (f *fakeDaily) ListDailyItems(ctx context.Context, in *corev1.ListDailyItemsRequest, _ ...grpc.CallOption) (*corev1.ListDailyItemsResponse, error) {
	f.rec(ctx, in)
	deleted := dailyItemPB()
	deleted.Pin, deleted.CreatedBy, deleted.Attribution, deleted.DeleteTime = nil, "", "", timestamppb.Now()
	return &corev1.ListDailyItemsResponse{Items: []*corev1.DailyItem{dailyItemPB(), deleted}, CallerCanManage: true}, f.err
}

func (f *fakeDaily) CreateDailyItem(ctx context.Context, in *corev1.CreateDailyItemRequest, _ ...grpc.CallOption) (*corev1.CreateDailyItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.CreateDailyItemResponse{Item: dailyItemPB(), PinConflict: true}, f.err
}

func (f *fakeDaily) UpdateDailyItem(ctx context.Context, in *corev1.UpdateDailyItemRequest, _ ...grpc.CallOption) (*corev1.UpdateDailyItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.UpdateDailyItemResponse{Item: dailyItemPB()}, f.err
}

func (f *fakeDaily) DeleteDailyItem(ctx context.Context, in *corev1.DeleteDailyItemRequest, _ ...grpc.CallOption) (*corev1.DeleteDailyItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.DeleteDailyItemResponse{}, f.err
}

func (f *fakeDaily) RestoreDailyItem(ctx context.Context, in *corev1.RestoreDailyItemRequest, _ ...grpc.CallOption) (*corev1.RestoreDailyItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.RestoreDailyItemResponse{Item: dailyItemPB()}, f.err
}

func (f *fakeDaily) ReorderDailyItems(ctx context.Context, in *corev1.ReorderDailyItemsRequest, _ ...grpc.CallOption) (*corev1.ReorderDailyItemsResponse, error) {
	f.rec(ctx, in)
	return &corev1.ReorderDailyItemsResponse{Items: []*corev1.DailyItem{dailyItemPB()}}, f.err
}

const (
	dailyBase     = "/v1/tayfas/" + tayfaID + "/daily"
	dailyItemsURL = dailyBase + "/items"
	dailyItemURL  = dailyItemsURL + "/" + itemID
)

func TestDailyRoutes(t *testing.T) {
	f := &fakeDaily{}
	h := componentRouter(fakeServices{dailyService: f})
	year := int32(2027)

	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", dailyBase + "/today", "", http.StatusOK, &corev1.GetDailyTodayRequest{TayfaId: tayfaID}},
		{"GET", dailyItemsURL, "", http.StatusOK, &corev1.ListDailyItemsRequest{TayfaId: tayfaID}},
		{"GET", dailyItemsURL + "?deleted=true", "", http.StatusOK, &corev1.ListDailyItemsRequest{TayfaId: tayfaID, Deleted: true}},
		{"POST", dailyItemsURL, `{"body":"Söz","attribution":"Atatürk","pin":{"month":11,"day":10,"year":2027},"client_request_id":"` + itemID + `"}`, http.StatusCreated,
			&corev1.CreateDailyItemRequest{TayfaId: tayfaID, ClientRequestId: itemID, Item: &corev1.DailyItemInput{
				Body: "Söz", Attribution: "Atatürk", Pin: &corev1.DailyPin{Month: 11, Day: 10, Year: &year},
			}}},
		{"PUT", dailyItemURL, `{"body":"Dize\nikinci"}`, http.StatusOK,
			&corev1.UpdateDailyItemRequest{TayfaId: tayfaID, ItemId: itemID, Item: &corev1.DailyItemInput{Body: "Dize\nikinci"}}},
		{"DELETE", dailyItemURL, "", http.StatusNoContent, &corev1.DeleteDailyItemRequest{TayfaId: tayfaID, ItemId: itemID}},
		{"POST", dailyItemURL + "/restore", "", http.StatusOK, &corev1.RestoreDailyItemRequest{TayfaId: tayfaID, ItemId: itemID}},
		{"PUT", dailyBase + "/order", `{"item_ids":["` + itemID + `","` + entryID + `"]}`, http.StatusOK,
			&corev1.ReorderDailyItemsRequest{TayfaId: tayfaID, ItemIds: []string{itemID, entryID}}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s %s: core'a giden %v (%q), beklenen %v", tc.method, tc.path, f.got, f.token, tc.want)
		}
	}

	w := as(h, "GET", dailyBase+"/today", "")
	today := decodeBody[dailyTodayJSON](t, w)
	if today.Item == nil || !today.Pinned || today.Date != "2026-11-10" || today.Item.Body != "Söz\nikinci satır" ||
		today.Item.Pin == nil || today.Item.Pin.Year == nil || *today.Item.Pin.Year != 2026 || today.Item.CreatedByUserID != userID ||
		today.Item.DeletedAt != nil {
		t.Fatalf("bugün: %+v", today)
	}
	f.empty = true
	if w := as(h, "GET", dailyBase+"/today", ""); strings.Contains(w.Body.String(), `"item"`) {
		t.Fatalf("boş gün: %s", w.Body)
	}
	l := decodeBody[dailyListJSON](t, as(h, "GET", dailyItemsURL, ""))
	if len(l.Items) != 2 || !l.CallerCanManage || l.Items[1].Pin != nil || l.Items[1].DeletedAt == nil || l.Items[1].CreatedByUserID != "" {
		t.Fatalf("liste: %+v", l)
	}
	saved := decodeBody[dailySavedJSON](t, as(h, "POST", dailyItemsURL, `{"body":"x","client_request_id":"`+itemID+`"}`))
	if !saved.PinConflict || saved.Item.ID != itemID {
		t.Fatalf("ekleme: %+v", saved)
	}
}

func TestDailyRoutesValidateInput(t *testing.T) {
	f := &fakeDaily{}
	h := componentRouter(fakeServices{dailyService: f})
	many := make([]string, 1001)
	for i := range many {
		many[i] = `"` + itemID + `"`
	}
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/v1/tayfas/x/daily/today", ""},
		{"GET", dailyItemsURL + "?deleted=evet", ""},
		{"POST", dailyItemsURL, `{"body":"x"}`},
		{"POST", dailyItemsURL, `{"body":"x","client_request_id":"x"}`},
		{"POST", dailyItemsURL, `{"body":"  ","client_request_id":"` + itemID + `"}`},
		{"POST", dailyItemsURL, `{"body":"` + strings.Repeat("a", 4097) + `","client_request_id":"` + itemID + `"}`},
		{"POST", dailyItemsURL, `{"body":"x","client_request_id":"` + itemID + `","position":3}`},
		{"PUT", dailyItemsURL + "/x", `{"body":"x"}`},
		{"PUT", dailyItemURL, `{"body":"x","pin":{"month":"kasım"}}`},
		{"DELETE", dailyItemsURL + "/x", ""},
		{"POST", dailyItemsURL + "/x/restore", ""},
		{"PUT", dailyBase + "/order", `{}`},
		{"PUT", dailyBase + "/order", `{"item_ids":["x"]}`},
		{"PUT", dailyBase + "/order", `{"item_ids":[` + strings.Join(many, ",") + `]}`},
	} {
		if w := as(h, rt.method, rt.path, rt.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call; a write without the client header is refused.
	for _, rt := range []struct{ method, path string }{
		{"GET", dailyBase + "/today"}, {"POST", dailyItemsURL}, {"DELETE", dailyItemURL}, {"PUT", dailyBase + "/order"},
	} {
		if w := do(h, rt.method, rt.path, `{}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", rt.method, rt.path, w.Code)
		}
		if rt.method == "GET" {
			continue
		}
		if w := do(h, rt.method, rt.path, `{}`, map[string]string{"Authorization": "Bearer T"}); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
			t.Fatalf("istemci başlıksız %s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestDailyErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_DAILY_ITEM_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "daily-item-not-found"},
		"ERROR_REASON_DAILY_ITEM_LIMIT":     {codes.FailedPrecondition, http.StatusConflict, "daily-item-limit"},
		"ERROR_REASON_MODULE_DISABLED":      {codes.FailedPrecondition, http.StatusConflict, "module-disabled"},
		"ERROR_REASON_PERMISSION_DENIED":    {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{dailyService: &fakeDaily{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", dailyItemURL+"/restore", ""); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
