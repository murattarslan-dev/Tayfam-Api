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

const libraryService = "tayfam.core.v1.TayfaLibraryService"

// fakeLibrary is core's TayfaLibraryService for the library tests.
type fakeLibrary struct {
	got   proto.Message
	token string
	err   error
	calls int
}

func (f *fakeLibrary) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func libraryLoanPB() *corev1.LibraryLoan {
	return &corev1.LibraryLoan{
		Id: entryID, ItemId: itemID, ItemName: "Çadır", ItemKind: corev1.LibraryItemKind_LIBRARY_ITEM_KIND_CAMPING_GEAR,
		OwnerId: userID, State: corev1.LibraryLoanState_LIBRARY_LOAN_STATE_HANDED_OVER,
		RequestTime: timestamppb.Now(), ApproveTime: timestamppb.Now(), HandOverTime: timestamppb.Now(), CallerIsOwner: true,
	}
}

func libraryItemPB() *corev1.LibraryItem {
	return &corev1.LibraryItem{
		Id: itemID, Kind: corev1.LibraryItemKind_LIBRARY_ITEM_KIND_CAMPING_GEAR, Name: "Çadır", Note: "Kazıklar\nkılıfında",
		OwnerId: userID, Status: corev1.LibraryItemStatus_LIBRARY_ITEM_STATUS_ON_LOAN,
		CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now(), QueueLength: 1,
		ActiveLoan: libraryLoanPB(), Queue: []*corev1.LibraryQueueEntry{{UserId: entryID, Rank: 1}},
	}
}

func (f *fakeLibrary) ListLibraryItems(ctx context.Context, in *corev1.ListLibraryItemsRequest, _ ...grpc.CallOption) (*corev1.ListLibraryItemsResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListLibraryItemsResponse{Items: []*corev1.LibraryItem{libraryItemPB()}, CallerCanAdd: true}, f.err
}

func (f *fakeLibrary) GetLibraryItem(ctx context.Context, in *corev1.GetLibraryItemRequest, _ ...grpc.CallOption) (*corev1.GetLibraryItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.GetLibraryItemResponse{Item: libraryItemPB()}, f.err
}

func (f *fakeLibrary) CreateLibraryItem(ctx context.Context, in *corev1.CreateLibraryItemRequest, _ ...grpc.CallOption) (*corev1.CreateLibraryItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.CreateLibraryItemResponse{Item: libraryItemPB()}, f.err
}

func (f *fakeLibrary) UpdateLibraryItem(ctx context.Context, in *corev1.UpdateLibraryItemRequest, _ ...grpc.CallOption) (*corev1.UpdateLibraryItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.UpdateLibraryItemResponse{Item: libraryItemPB()}, f.err
}

func (f *fakeLibrary) WithdrawLibraryItem(ctx context.Context, in *corev1.WithdrawLibraryItemRequest, _ ...grpc.CallOption) (*corev1.WithdrawLibraryItemResponse, error) {
	f.rec(ctx, in)
	return &corev1.WithdrawLibraryItemResponse{}, f.err
}

func (f *fakeLibrary) RequestLibraryLoan(ctx context.Context, in *corev1.RequestLibraryLoanRequest, _ ...grpc.CallOption) (*corev1.RequestLibraryLoanResponse, error) {
	f.rec(ctx, in)
	return &corev1.RequestLibraryLoanResponse{Item: libraryItemPB()}, f.err
}

func (f *fakeLibrary) CancelLibraryRequest(ctx context.Context, in *corev1.CancelLibraryRequestRequest, _ ...grpc.CallOption) (*corev1.CancelLibraryRequestResponse, error) {
	f.rec(ctx, in)
	return &corev1.CancelLibraryRequestResponse{Item: libraryItemPB()}, f.err
}

func (f *fakeLibrary) ListLibraryLoans(ctx context.Context, in *corev1.ListLibraryLoansRequest, _ ...grpc.CallOption) (*corev1.ListLibraryLoansResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListLibraryLoansResponse{Loans: []*corev1.LibraryLoan{libraryLoanPB()}}, f.err
}

func (f *fakeLibrary) ApproveLibraryLoan(ctx context.Context, in *corev1.ApproveLibraryLoanRequest, _ ...grpc.CallOption) (*corev1.ApproveLibraryLoanResponse, error) {
	f.rec(ctx, in)
	return &corev1.ApproveLibraryLoanResponse{Loan: libraryLoanPB()}, f.err
}

func (f *fakeLibrary) DeclineLibraryLoan(ctx context.Context, in *corev1.DeclineLibraryLoanRequest, _ ...grpc.CallOption) (*corev1.DeclineLibraryLoanResponse, error) {
	f.rec(ctx, in)
	return &corev1.DeclineLibraryLoanResponse{Loan: libraryLoanPB()}, f.err
}

func (f *fakeLibrary) HandOverLibraryLoan(ctx context.Context, in *corev1.HandOverLibraryLoanRequest, _ ...grpc.CallOption) (*corev1.HandOverLibraryLoanResponse, error) {
	f.rec(ctx, in)
	return &corev1.HandOverLibraryLoanResponse{Loan: libraryLoanPB()}, f.err
}

func (f *fakeLibrary) ReturnLibraryLoan(ctx context.Context, in *corev1.ReturnLibraryLoanRequest, _ ...grpc.CallOption) (*corev1.ReturnLibraryLoanResponse, error) {
	f.rec(ctx, in)
	return &corev1.ReturnLibraryLoanResponse{Loan: libraryLoanPB()}, f.err
}

const (
	libraryBase     = "/v1/tayfas/" + tayfaID + "/library"
	libraryItemsURL = libraryBase + "/items"
	libraryItemURL  = libraryItemsURL + "/" + itemID
	libraryLoanURL  = libraryBase + "/loans/" + entryID
)

func TestLibraryRoutes(t *testing.T) {
	f := &fakeLibrary{}
	h := componentRouter(fakeServices{libraryService: f})
	due := time.Date(2026, time.December, 1, 9, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", libraryItemsURL, "", http.StatusOK, &corev1.ListLibraryItemsRequest{TayfaId: tayfaID}},
		{"GET", libraryItemsURL + "?kind=book&mine=true", "", http.StatusOK,
			&corev1.ListLibraryItemsRequest{TayfaId: tayfaID, Kind: corev1.LibraryItemKind_LIBRARY_ITEM_KIND_BOOK, Mine: true}},
		{"POST", libraryItemsURL, `{"kind":"tool","name":"Matkap","note":"Uçlarıyla","photo_upload_id":"` + entryID + `","client_request_id":"` + itemID + `"}`, http.StatusCreated,
			&corev1.CreateLibraryItemRequest{TayfaId: tayfaID, Kind: corev1.LibraryItemKind_LIBRARY_ITEM_KIND_TOOL, Name: "Matkap", Note: "Uçlarıyla",
				PhotoUploadId: entryID, ClientRequestId: itemID}},
		{"GET", libraryItemURL, "", http.StatusOK, &corev1.GetLibraryItemRequest{TayfaId: tayfaID, ItemId: itemID}},
		{"PUT", libraryItemURL, `{"name":"Darbeli matkap"}`, http.StatusOK,
			&corev1.UpdateLibraryItemRequest{TayfaId: tayfaID, ItemId: itemID, Name: "Darbeli matkap"}},
		{"DELETE", libraryItemURL, "", http.StatusNoContent, &corev1.WithdrawLibraryItemRequest{TayfaId: tayfaID, ItemId: itemID}},
		{"POST", libraryItemURL + "/request", "", http.StatusOK, &corev1.RequestLibraryLoanRequest{TayfaId: tayfaID, ItemId: itemID}},
		{"DELETE", libraryItemURL + "/request", "", http.StatusOK, &corev1.CancelLibraryRequestRequest{TayfaId: tayfaID, ItemId: itemID}},
		{"GET", libraryBase + "/loans", "", http.StatusOK, &corev1.ListLibraryLoansRequest{TayfaId: tayfaID}},
		{"POST", libraryLoanURL + "/approve", "", http.StatusOK, &corev1.ApproveLibraryLoanRequest{TayfaId: tayfaID, LoanId: entryID}},
		{"POST", libraryLoanURL + "/decline", "", http.StatusOK, &corev1.DeclineLibraryLoanRequest{TayfaId: tayfaID, LoanId: entryID}},
		{"POST", libraryLoanURL + "/hand-over", "", http.StatusOK, &corev1.HandOverLibraryLoanRequest{TayfaId: tayfaID, LoanId: entryID}},
		{"POST", libraryLoanURL + "/hand-over", `{"due_at":"2026-12-01T09:00:00Z"}`, http.StatusOK,
			&corev1.HandOverLibraryLoanRequest{TayfaId: tayfaID, LoanId: entryID, DueTime: timestamppb.New(due)}},
		{"POST", libraryLoanURL + "/return", "", http.StatusOK, &corev1.ReturnLibraryLoanRequest{TayfaId: tayfaID, LoanId: entryID}},
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

	it := decodeBody[libraryItemJSON](t, as(h, "GET", libraryItemURL, ""))
	if it.Kind != "camping_gear" || it.Status != "on_loan" || it.Note != "Kazıklar\nkılıfında" || it.VocabularyID != "" ||
		it.ActiveLoan == nil || it.ActiveLoan.State != "handed_over" || it.ActiveLoan.BorrowerUserID != "" || it.ActiveLoan.DueAt != nil ||
		len(it.Queue) != 1 || it.Queue[0].Rank != 1 || it.CallerQueueRank != 0 {
		t.Fatalf("kalem: %+v", it)
	}
	w := as(h, "GET", libraryItemsURL, "")
	if l := decodeBody[libraryItemListJSON](t, w); len(l.Items) != 1 || !l.CallerCanAdd || strings.Contains(w.Body.String(), `"caller_queue_rank"`) {
		t.Fatalf("liste: %s", w.Body)
	}
	loans := decodeBody[libraryLoanListJSON](t, as(h, "GET", libraryBase+"/loans", ""))
	if len(loans.Loans) != 1 || loans.Loans[0].ItemKind != "camping_gear" || !loans.Loans[0].CallerIsOwner || loans.Loans[0].EndedAt != nil {
		t.Fatalf("ödünçler: %+v", loans)
	}
}

func TestLibraryRoutesValidateInput(t *testing.T) {
	f := &fakeLibrary{}
	h := componentRouter(fakeServices{libraryService: f})
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/v1/tayfas/x/library/items", ""},
		{"GET", libraryItemsURL + "?kind=weapon", ""},
		{"GET", libraryItemsURL + "?mine=evet", ""},
		{"POST", libraryItemsURL, `{"kind":"tool","name":"x"}`},
		{"POST", libraryItemsURL, `{"kind":"weapon","name":"x","client_request_id":"` + itemID + `"}`},
		{"POST", libraryItemsURL, `{"kind":"tool","name":"  ","client_request_id":"` + itemID + `"}`},
		{"POST", libraryItemsURL, `{"kind":"tool","name":"x","photo_upload_id":"x","client_request_id":"` + itemID + `"}`},
		{"POST", libraryItemsURL, `{"kind":"tool","name":"` + strings.Repeat("a", 1025) + `","client_request_id":"` + itemID + `"}`},
		{"POST", libraryItemsURL, `{"kind":"tool","name":"x","note":"` + strings.Repeat("a", 4097) + `","client_request_id":"` + itemID + `"}`},
		{"POST", libraryItemsURL, `{"kind":"tool","name":"x","client_request_id":"` + itemID + `","status":"on_loan"}`},
		{"PUT", libraryItemsURL + "/x", `{"name":"x"}`},
		{"PUT", libraryItemURL, `{"name":"x","kind":"book"}`},
		{"DELETE", libraryItemsURL + "/x", ""},
		{"POST", libraryItemsURL + "/x/request", ""},
		{"POST", libraryBase + "/loans/x/approve", ""},
		{"POST", libraryLoanURL + "/hand-over", `{"due_at":"yarın"}`},
	} {
		if w := as(h, rt.method, rt.path, rt.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	for _, rt := range []struct{ method, path string }{
		{"GET", libraryItemsURL}, {"POST", libraryItemsURL}, {"DELETE", libraryItemURL}, {"POST", libraryLoanURL + "/return"},
	} {
		if w := do(h, rt.method, rt.path, `{}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", rt.method, rt.path, w.Code)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestLibraryErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_LIBRARY_ITEM_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "library-item-not-found"},
		"ERROR_REASON_LIBRARY_LOAN_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "library-loan-not-found"},
		"ERROR_REASON_LIBRARY_OWN_ITEM":       {codes.FailedPrecondition, http.StatusConflict, "library-own-item"},
		"ERROR_REASON_LIBRARY_LOAN_STATE":     {codes.FailedPrecondition, http.StatusConflict, "library-loan-state"},
		"ERROR_REASON_LIBRARY_ITEM_ON_LOAN":   {codes.FailedPrecondition, http.StatusConflict, "library-item-on-loan"},
		"ERROR_REASON_LIBRARY_ITEM_LIMIT":     {codes.FailedPrecondition, http.StatusConflict, "library-item-limit"},
		"ERROR_REASON_LIBRARY_QUEUE_FULL":     {codes.FailedPrecondition, http.StatusConflict, "library-queue-full"},
		"ERROR_REASON_MODULE_DISABLED":        {codes.FailedPrecondition, http.StatusConflict, "module-disabled"},
	} {
		h := componentRouter(fakeServices{libraryService: &fakeLibrary{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", libraryItemURL+"/request", ""); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
