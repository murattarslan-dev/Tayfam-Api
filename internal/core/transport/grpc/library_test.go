package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/library"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeLibrary struct {
	err         error
	method      string
	user, tayfa id.ID
	target      id.ID
	in          library.Input
	crid        string
	kind        string
	mine        bool
	due         time.Time
	item        library.Item
	loan        library.Loan
	calls       int
}

func (f *fakeLibrary) rec(method string, user, tayfa, target id.ID) {
	f.method, f.user, f.tayfa, f.target, f.calls = method, user, tayfa, target, f.calls+1
}

func (f *fakeLibrary) List(_ context.Context, user, tayfa id.ID, kind string, mine bool) (library.List, error) {
	f.rec("List", user, tayfa, id.ID{})
	f.kind, f.mine = kind, mine
	return library.List{Items: []library.Item{f.item}, CallerCanAdd: true}, f.err
}

func (f *fakeLibrary) Get(_ context.Context, user, tayfa, item id.ID) (library.Item, error) {
	f.rec("Get", user, tayfa, item)
	return f.item, f.err
}

func (f *fakeLibrary) Create(_ context.Context, user, tayfa id.ID, in library.Input, crid string) (library.Item, error) {
	f.rec("Create", user, tayfa, id.ID{})
	f.in, f.crid = in, crid
	return f.item, f.err
}

func (f *fakeLibrary) Update(_ context.Context, user, tayfa, item id.ID, in library.Input) (library.Item, error) {
	f.rec("Update", user, tayfa, item)
	f.in = in
	return f.item, f.err
}

func (f *fakeLibrary) Withdraw(_ context.Context, user, tayfa, item id.ID) error {
	f.rec("Withdraw", user, tayfa, item)
	return f.err
}

func (f *fakeLibrary) Request(_ context.Context, user, tayfa, item id.ID) (library.Item, error) {
	f.rec("Request", user, tayfa, item)
	return f.item, f.err
}

func (f *fakeLibrary) Cancel(_ context.Context, user, tayfa, item id.ID) (library.Item, error) {
	f.rec("Cancel", user, tayfa, item)
	return f.item, f.err
}

func (f *fakeLibrary) Loans(_ context.Context, user, tayfa id.ID) ([]library.Loan, error) {
	f.rec("Loans", user, tayfa, id.ID{})
	return []library.Loan{f.loan}, f.err
}

func (f *fakeLibrary) Approve(_ context.Context, user, tayfa, loan id.ID) (library.Loan, error) {
	f.rec("Approve", user, tayfa, loan)
	return f.loan, f.err
}

func (f *fakeLibrary) Decline(_ context.Context, user, tayfa, loan id.ID) (library.Loan, error) {
	f.rec("Decline", user, tayfa, loan)
	return f.loan, f.err
}

func (f *fakeLibrary) HandOver(_ context.Context, user, tayfa, loan id.ID, due time.Time) (library.Loan, error) {
	f.rec("HandOver", user, tayfa, loan)
	f.due = due
	return f.loan, f.err
}

func (f *fakeLibrary) Return(_ context.Context, user, tayfa, loan id.ID) (library.Loan, error) {
	f.rec("Return", user, tayfa, loan)
	return f.loan, f.err
}

func TestTayfaLibraryMethods(t *testing.T) {
	t0 := time.Date(2026, time.November, 21, 9, 0, 0, 0, time.UTC)
	owner, borrower, waiter := id.New(), id.New(), id.New()
	loan := library.Loan{
		ID: id.New(), Item: id.New(), ItemName: "Çadır", ItemKind: library.KindCampingGear, Owner: owner, Borrower: borrower,
		State: library.StateHandedOver, RequestedAt: t0, ApprovedAt: t0, HandedOverAt: t0, DueAt: t0.Add(48 * time.Hour), CallerIsOwner: true,
	}
	f := &fakeLibrary{
		item: library.Item{
			ID: loan.Item, Kind: library.KindCampingGear, Name: "Çadır", Owner: owner, Status: library.StatusOnLoan,
			CreatedAt: t0, UpdatedAt: t0, QueueLength: 1, ActiveLoan: &loan, Queue: []library.QueueEntry{{User: waiter, Rank: 1}},
		},
		loan: loan,
	}
	s := NewTayfaLibraryServer(f, discard)
	user, tf := id.New(), id.New()
	ctx := callerCtx(user)

	list, err := s.ListLibraryItems(ctx, &corev1.ListLibraryItemsRequest{TayfaId: tf.String(), Kind: corev1.LibraryItemKind_LIBRARY_ITEM_KIND_CAMPING_GEAR, Mine: true})
	if err != nil || f.method != "List" || f.user != user || f.tayfa != tf || f.kind != library.KindCampingGear || !f.mine || !list.GetCallerCanAdd() {
		t.Fatalf("liste: %v %+v", err, f)
	}
	it := list.GetItems()[0]
	if it.GetStatus() != corev1.LibraryItemStatus_LIBRARY_ITEM_STATUS_ON_LOAN || it.GetKind() != corev1.LibraryItemKind_LIBRARY_ITEM_KIND_CAMPING_GEAR ||
		it.GetVocabularyId() != "" || it.GetPhotoUploadId() != "" || it.GetQueue()[0].GetUserId() != waiter.String() ||
		it.GetActiveLoan().GetState() != corev1.LibraryLoanState_LIBRARY_LOAN_STATE_HANDED_OVER || it.GetActiveLoan().GetEndTime() != nil ||
		it.GetActiveLoan().GetDueTime() == nil || it.GetActiveLoan().GetBorrowerId() != borrower.String() {
		t.Fatalf("kalem: %v", it)
	}
	if _, err := s.ListLibraryItems(ctx, &corev1.ListLibraryItemsRequest{TayfaId: tf.String()}); err != nil || f.kind != "" || f.mine {
		t.Fatalf("tüm liste: %v %+v", err, f)
	}

	photo, crid := id.New(), id.New().String()
	if _, err := s.CreateLibraryItem(ctx, &corev1.CreateLibraryItemRequest{
		TayfaId: tf.String(), Kind: corev1.LibraryItemKind_LIBRARY_ITEM_KIND_BOOK, Name: "Simyacı", Note: "not",
		PhotoUploadId: photo.String(), ClientRequestId: crid,
	}); err != nil || f.method != "Create" || f.in != (library.Input{Kind: library.KindBook, Name: "Simyacı", Note: "not", PhotoUploadID: photo}) || f.crid != crid {
		t.Fatalf("ekleme: %v %+v", err, f)
	}
	if _, err := s.UpdateLibraryItem(ctx, &corev1.UpdateLibraryItemRequest{TayfaId: tf.String(), ItemId: loan.Item.String(), Name: "Yeni"}); err != nil ||
		f.method != "Update" || f.target != loan.Item || f.in != (library.Input{Name: "Yeni"}) {
		t.Fatalf("düzenleme: %v %+v", err, f)
	}
	for name, call := range map[string]func() error{
		"Get": func() error {
			_, err := s.GetLibraryItem(ctx, &corev1.GetLibraryItemRequest{TayfaId: tf.String(), ItemId: loan.Item.String()})
			return err
		},
		"Withdraw": func() error {
			_, err := s.WithdrawLibraryItem(ctx, &corev1.WithdrawLibraryItemRequest{TayfaId: tf.String(), ItemId: loan.Item.String()})
			return err
		},
		"Request": func() error {
			_, err := s.RequestLibraryLoan(ctx, &corev1.RequestLibraryLoanRequest{TayfaId: tf.String(), ItemId: loan.Item.String()})
			return err
		},
		"Cancel": func() error {
			_, err := s.CancelLibraryRequest(ctx, &corev1.CancelLibraryRequestRequest{TayfaId: tf.String(), ItemId: loan.Item.String()})
			return err
		},
	} {
		if err := call(); err != nil || f.method != name || f.target != loan.Item {
			t.Errorf("%s: %v %+v", name, err, f)
		}
	}
	for name, call := range map[string]func() error{
		"Approve": func() error {
			_, err := s.ApproveLibraryLoan(ctx, &corev1.ApproveLibraryLoanRequest{TayfaId: tf.String(), LoanId: loan.ID.String()})
			return err
		},
		"Decline": func() error {
			_, err := s.DeclineLibraryLoan(ctx, &corev1.DeclineLibraryLoanRequest{TayfaId: tf.String(), LoanId: loan.ID.String()})
			return err
		},
		"Return": func() error {
			_, err := s.ReturnLibraryLoan(ctx, &corev1.ReturnLibraryLoanRequest{TayfaId: tf.String(), LoanId: loan.ID.String()})
			return err
		},
	} {
		if err := call(); err != nil || f.method != name || f.target != loan.ID {
			t.Errorf("%s: %v %+v", name, err, f)
		}
	}
	due := t0.Add(72 * time.Hour)
	out, err := s.HandOverLibraryLoan(ctx, &corev1.HandOverLibraryLoanRequest{TayfaId: tf.String(), LoanId: loan.ID.String(), DueTime: timestamppb.New(due)})
	if err != nil || f.method != "HandOver" || !f.due.Equal(due) || !out.GetLoan().GetCallerIsOwner() {
		t.Fatalf("teslim: %v %+v", err, f)
	}
	if _, err := s.HandOverLibraryLoan(ctx, &corev1.HandOverLibraryLoanRequest{TayfaId: tf.String(), LoanId: loan.ID.String()}); err != nil || !f.due.IsZero() {
		t.Fatalf("tarihsiz teslim: %v %+v", err, f)
	}
	loans, err := s.ListLibraryLoans(ctx, &corev1.ListLibraryLoansRequest{TayfaId: tf.String()})
	if err != nil || f.method != "Loans" || len(loans.GetLoans()) != 1 || loans.GetLoans()[0].GetItemName() != "Çadır" {
		t.Fatalf("ödünçler: %v %v", loans, err)
	}
}

func TestTayfaLibraryValidation(t *testing.T) {
	f := &fakeLibrary{}
	s := NewTayfaLibraryServer(f, discard)
	ctx := callerCtx(id.New())
	tf := id.New().String()
	for name, call := range map[string]func() error{
		"tayfa": func() error {
			_, err := s.ListLibraryItems(ctx, &corev1.ListLibraryItemsRequest{TayfaId: "x"})
			return err
		},
		"kind": func() error {
			_, err := s.ListLibraryItems(ctx, &corev1.ListLibraryItemsRequest{TayfaId: tf, Kind: corev1.LibraryItemKind(99)})
			return err
		},
		"create kind": func() error {
			_, err := s.CreateLibraryItem(ctx, &corev1.CreateLibraryItemRequest{TayfaId: tf, Name: "x", ClientRequestId: id.New().String()})
			return err
		},
		"photo": func() error {
			_, err := s.CreateLibraryItem(ctx, &corev1.CreateLibraryItemRequest{TayfaId: tf, Kind: corev1.LibraryItemKind_LIBRARY_ITEM_KIND_TOOL, Name: "x", PhotoUploadId: "x"})
			return err
		},
		"item_id": func() error {
			_, err := s.WithdrawLibraryItem(ctx, &corev1.WithdrawLibraryItemRequest{TayfaId: tf, ItemId: "x"})
			return err
		},
		"loan_id": func() error {
			_, err := s.ApproveLibraryLoan(ctx, &corev1.ApproveLibraryLoanRequest{TayfaId: tf, LoanId: "x"})
			return err
		},
		"due_time": func() error {
			_, err := s.HandOverLibraryLoan(ctx, &corev1.HandOverLibraryLoanRequest{TayfaId: tf, LoanId: id.New().String(), DueTime: &timestamppb.Timestamp{Seconds: -1 << 62}})
			return err
		},
	} {
		if err := call(); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek uygulamaya ulaştı: %d", f.calls)
	}
	for err, want := range map[error]codes.Code{
		apperr.ErrLibraryItemNotFound: codes.NotFound,
		apperr.ErrLibraryLoanState:    codes.FailedPrecondition,
		apperr.ErrLibraryQueueFull:    codes.FailedPrecondition,
	} {
		f.err = err
		if _, err := s.RequestLibraryLoan(ctx, &corev1.RequestLibraryLoanRequest{TayfaId: tf, ItemId: id.New().String()}); codeOf(t, err) != want {
			t.Errorf("%v: %v", f.err, err)
		}
	}
}
