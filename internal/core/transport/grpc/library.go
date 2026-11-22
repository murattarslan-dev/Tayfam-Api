package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/library"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Library is what TayfaLibraryServer needs from the app layer
// (library.Service).
type Library interface {
	List(ctx context.Context, user, tayfa id.ID, kind string, mine bool) (library.List, error)
	Get(ctx context.Context, user, tayfa, item id.ID) (library.Item, error)
	Create(ctx context.Context, user, tayfa id.ID, in library.Input, clientRequestID string) (library.Item, error)
	Update(ctx context.Context, user, tayfa, item id.ID, in library.Input) (library.Item, error)
	Withdraw(ctx context.Context, user, tayfa, item id.ID) error
	Request(ctx context.Context, user, tayfa, item id.ID) (library.Item, error)
	Cancel(ctx context.Context, user, tayfa, item id.ID) (library.Item, error)
	Loans(ctx context.Context, user, tayfa id.ID) ([]library.Loan, error)
	Approve(ctx context.Context, user, tayfa, loan id.ID) (library.Loan, error)
	Decline(ctx context.Context, user, tayfa, loan id.ID) (library.Loan, error)
	HandOver(ctx context.Context, user, tayfa, loan id.ID, due time.Time) (library.Loan, error)
	Return(ctx context.Context, user, tayfa, loan id.ID) (library.Loan, error)
}

// TayfaLibraryServer implements corev1.TayfaLibraryServiceServer (issue
// #270; ADR-0040 §1). Every method needs a verified token (auth.Guard); the
// acting user is always the token's. Names and notes are never logged.
type TayfaLibraryServer struct {
	corev1.UnimplementedTayfaLibraryServiceServer
	app Library
	log *slog.Logger
}

// NewTayfaLibraryServer returns a TayfaLibraryServer.
func NewTayfaLibraryServer(app Library, log *slog.Logger) *TayfaLibraryServer {
	return &TayfaLibraryServer{app: app, log: log}
}

// registerTayfaLibrary is the shared library's line in the registry
// (componentRegistrars): the library is tayfa-level data on the framework.
func registerTayfaLibrary(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterTayfaLibraryServiceServer(srv, NewTayfaLibraryServer(library.New(fw), log))
	return nil
}

var libraryKinds = map[corev1.LibraryItemKind]string{
	corev1.LibraryItemKind_LIBRARY_ITEM_KIND_BOOK:         library.KindBook,
	corev1.LibraryItemKind_LIBRARY_ITEM_KIND_CAMPING_GEAR: library.KindCampingGear,
	corev1.LibraryItemKind_LIBRARY_ITEM_KIND_BOARD_GAME:   library.KindBoardGame,
	corev1.LibraryItemKind_LIBRARY_ITEM_KIND_TOOL:         library.KindTool,
	corev1.LibraryItemKind_LIBRARY_ITEM_KIND_OTHER:        library.KindOther,
}

var libraryStatuses = map[string]corev1.LibraryItemStatus{
	library.StatusAvailable: corev1.LibraryItemStatus_LIBRARY_ITEM_STATUS_AVAILABLE,
	library.StatusOnLoan:    corev1.LibraryItemStatus_LIBRARY_ITEM_STATUS_ON_LOAN,
	library.StatusWithdrawn: corev1.LibraryItemStatus_LIBRARY_ITEM_STATUS_WITHDRAWN,
}

var libraryStates = map[string]corev1.LibraryLoanState{
	library.StateRequested:  corev1.LibraryLoanState_LIBRARY_LOAN_STATE_REQUESTED,
	library.StateApproved:   corev1.LibraryLoanState_LIBRARY_LOAN_STATE_APPROVED,
	library.StateHandedOver: corev1.LibraryLoanState_LIBRARY_LOAN_STATE_HANDED_OVER,
	library.StateReturned:   corev1.LibraryLoanState_LIBRARY_LOAN_STATE_RETURNED,
	library.StateDeclined:   corev1.LibraryLoanState_LIBRARY_LOAN_STATE_DECLINED,
	library.StateCancelled:  corev1.LibraryLoanState_LIBRARY_LOAN_STATE_CANCELLED,
	library.StateClosed:     corev1.LibraryLoanState_LIBRARY_LOAN_STATE_CLOSED,
}

func libraryKindPB(kind string) corev1.LibraryItemKind {
	for k, v := range libraryKinds {
		if v == kind {
			return k
		}
	}
	return corev1.LibraryItemKind_LIBRARY_ITEM_KIND_UNSPECIFIED
}

func libraryLoanPB(l library.Loan) *corev1.LibraryLoan {
	return &corev1.LibraryLoan{
		Id: l.ID.String(), ItemId: l.Item.String(), ItemName: l.ItemName, ItemKind: libraryKindPB(l.ItemKind),
		OwnerId: idString(l.Owner), BorrowerId: idString(l.Borrower), State: libraryStates[l.State],
		RequestTime: timestamppb.New(l.RequestedAt), ApproveTime: tsOrNil(l.ApprovedAt),
		HandOverTime: tsOrNil(l.HandedOverAt), DueTime: tsOrNil(l.DueAt), OrphanTime: tsOrNil(l.OrphanedAt),
		EndTime: tsOrNil(l.EndedAt), CallerIsOwner: l.CallerIsOwner,
	}
}

func libraryItemPB(it library.Item) *corev1.LibraryItem {
	out := &corev1.LibraryItem{
		Id: it.ID.String(), Kind: libraryKindPB(it.Kind), Name: it.Name, VocabularyId: idString(it.VocabularyID),
		Note: it.Note, PhotoUploadId: idString(it.PhotoUploadID), OwnerId: idString(it.Owner),
		Status: libraryStatuses[it.Status], CreateTime: timestamppb.New(it.CreatedAt), UpdateTime: timestamppb.New(it.UpdatedAt),
		QueueLength: it.QueueLength, CallerQueueRank: it.CallerQueueRank,
	}
	if it.ActiveLoan != nil {
		out.ActiveLoan = libraryLoanPB(*it.ActiveLoan)
	}
	for _, e := range it.Queue {
		out.Queue = append(out.Queue, &corev1.LibraryQueueEntry{UserId: e.User.String(), Rank: e.Rank})
	}
	return out
}

// libraryPhotoID parses an optional photo upload ID: "" is none.
func libraryPhotoID(s, field string) (id.ID, error) {
	if s == "" {
		return id.ID{}, nil
	}
	return parseID(s, field)
}

// ListLibraryItems implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) ListLibraryItems(ctx context.Context, req *corev1.ListLibraryItemsRequest) (*corev1.ListLibraryItemsResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	var kind string
	if req.GetKind() != corev1.LibraryItemKind_LIBRARY_ITEM_KIND_UNSPECIFIED {
		var ok bool
		if kind, ok = libraryKinds[req.GetKind()]; !ok {
			return nil, validation(library.FieldKind)
		}
	}
	l, err := s.app.List(ctx, user, t, kind, req.GetMine())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListLibraryItemsResponse{Items: make([]*corev1.LibraryItem, 0, len(l.Items)), CallerCanAdd: l.CallerCanAdd}
	for _, it := range l.Items {
		out.Items = append(out.Items, libraryItemPB(it))
	}
	return out, nil
}

// GetLibraryItem implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) GetLibraryItem(ctx context.Context, req *corev1.GetLibraryItemRequest) (*corev1.GetLibraryItemResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	item, err := parseID(req.GetItemId(), library.FieldItemID)
	if err != nil {
		return nil, err
	}
	it, err := s.app.Get(ctx, user, t, item)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetLibraryItemResponse{Item: libraryItemPB(it)}, nil
}

// CreateLibraryItem implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) CreateLibraryItem(ctx context.Context, req *corev1.CreateLibraryItemRequest) (*corev1.CreateLibraryItemResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	kind, ok := libraryKinds[req.GetKind()]
	if !ok {
		return nil, validation(library.FieldKind)
	}
	photo, err := libraryPhotoID(req.GetPhotoUploadId(), library.FieldPhotoUploadID)
	if err != nil {
		return nil, err
	}
	it, err := s.app.Create(ctx, user, t, library.Input{Kind: kind, Name: req.GetName(), Note: req.GetNote(), PhotoUploadID: photo}, req.GetClientRequestId())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateLibraryItemResponse{Item: libraryItemPB(it)}, nil
}

// UpdateLibraryItem implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) UpdateLibraryItem(ctx context.Context, req *corev1.UpdateLibraryItemRequest) (*corev1.UpdateLibraryItemResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	item, err := parseID(req.GetItemId(), library.FieldItemID)
	if err != nil {
		return nil, err
	}
	photo, err := libraryPhotoID(req.GetPhotoUploadId(), library.FieldPhotoUploadID)
	if err != nil {
		return nil, err
	}
	it, err := s.app.Update(ctx, user, t, item, library.Input{Name: req.GetName(), Note: req.GetNote(), PhotoUploadID: photo})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateLibraryItemResponse{Item: libraryItemPB(it)}, nil
}

// WithdrawLibraryItem implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) WithdrawLibraryItem(ctx context.Context, req *corev1.WithdrawLibraryItemRequest) (*corev1.WithdrawLibraryItemResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	item, err := parseID(req.GetItemId(), library.FieldItemID)
	if err != nil {
		return nil, err
	}
	if err := s.app.Withdraw(ctx, user, t, item); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.WithdrawLibraryItemResponse{}, nil
}

// RequestLibraryLoan implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) RequestLibraryLoan(ctx context.Context, req *corev1.RequestLibraryLoanRequest) (*corev1.RequestLibraryLoanResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	item, err := parseID(req.GetItemId(), library.FieldItemID)
	if err != nil {
		return nil, err
	}
	it, err := s.app.Request(ctx, user, t, item)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RequestLibraryLoanResponse{Item: libraryItemPB(it)}, nil
}

// CancelLibraryRequest implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) CancelLibraryRequest(ctx context.Context, req *corev1.CancelLibraryRequestRequest) (*corev1.CancelLibraryRequestResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	item, err := parseID(req.GetItemId(), library.FieldItemID)
	if err != nil {
		return nil, err
	}
	it, err := s.app.Cancel(ctx, user, t, item)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CancelLibraryRequestResponse{Item: libraryItemPB(it)}, nil
}

// ListLibraryLoans implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) ListLibraryLoans(ctx context.Context, req *corev1.ListLibraryLoansRequest) (*corev1.ListLibraryLoansResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	loans, err := s.app.Loans(ctx, user, t)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListLibraryLoansResponse{Loans: make([]*corev1.LibraryLoan, 0, len(loans))}
	for _, l := range loans {
		out.Loans = append(out.Loans, libraryLoanPB(l))
	}
	return out, nil
}

// lendStep runs an owner's step on a loan.
func (s *TayfaLibraryServer) lendStep(ctx context.Context, tayfa, loan string, step func(user, t, l id.ID) (library.Loan, error)) (*corev1.LibraryLoan, error) {
	user, t, _, err := scope(ctx, tayfa, nil)
	if err != nil {
		return nil, err
	}
	l, err := parseID(loan, library.FieldLoanID)
	if err != nil {
		return nil, err
	}
	out, err := step(user, t, l)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return libraryLoanPB(out), nil
}

// ApproveLibraryLoan implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) ApproveLibraryLoan(ctx context.Context, req *corev1.ApproveLibraryLoanRequest) (*corev1.ApproveLibraryLoanResponse, error) {
	l, err := s.lendStep(ctx, req.GetTayfaId(), req.GetLoanId(), func(user, t, loan id.ID) (library.Loan, error) {
		return s.app.Approve(ctx, user, t, loan)
	})
	if err != nil {
		return nil, err
	}
	return &corev1.ApproveLibraryLoanResponse{Loan: l}, nil
}

// DeclineLibraryLoan implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) DeclineLibraryLoan(ctx context.Context, req *corev1.DeclineLibraryLoanRequest) (*corev1.DeclineLibraryLoanResponse, error) {
	l, err := s.lendStep(ctx, req.GetTayfaId(), req.GetLoanId(), func(user, t, loan id.ID) (library.Loan, error) {
		return s.app.Decline(ctx, user, t, loan)
	})
	if err != nil {
		return nil, err
	}
	return &corev1.DeclineLibraryLoanResponse{Loan: l}, nil
}

// HandOverLibraryLoan implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) HandOverLibraryLoan(ctx context.Context, req *corev1.HandOverLibraryLoanRequest) (*corev1.HandOverLibraryLoanResponse, error) {
	var due time.Time
	if d := req.GetDueTime(); d != nil {
		if err := d.CheckValid(); err != nil {
			return nil, validation(library.FieldDueTime)
		}
		due = d.AsTime()
		if due.IsZero() {
			return nil, validation(library.FieldDueTime)
		}
	}
	l, err := s.lendStep(ctx, req.GetTayfaId(), req.GetLoanId(), func(user, t, loan id.ID) (library.Loan, error) {
		return s.app.HandOver(ctx, user, t, loan, due)
	})
	if err != nil {
		return nil, err
	}
	return &corev1.HandOverLibraryLoanResponse{Loan: l}, nil
}

// ReturnLibraryLoan implements corev1.TayfaLibraryServiceServer.
func (s *TayfaLibraryServer) ReturnLibraryLoan(ctx context.Context, req *corev1.ReturnLibraryLoanRequest) (*corev1.ReturnLibraryLoanResponse, error) {
	l, err := s.lendStep(ctx, req.GetTayfaId(), req.GetLoanId(), func(user, t, loan id.ID) (library.Loan, error) {
		return s.app.Return(ctx, user, t, loan)
	})
	if err != nil {
		return nil, err
	}
	return &corev1.ReturnLibraryLoanResponse{Loan: l}, nil
}
