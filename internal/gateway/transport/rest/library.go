package rest

import (
	"context"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// Ortak kütüphane (issue #270; ADR-0040 §1). core decides everything; the
// gateway only keeps junk out early. Names and notes are content: they
// travel in bodies only and are never logged.

const (
	libraryPath     = "/v1/tayfas/{tayfa_id}/library"
	libraryItemPath = libraryPath + "/items/{library_item_id}"
	libraryLoanPath = libraryPath + "/loans/{library_loan_id}"
	// maxLibraryNameBytes and maxLibraryNoteBytes bound the texts before
	// core folds their whitespace (80 and 300 characters).
	maxLibraryNameBytes = 1 << 10
	maxLibraryNoteBytes = 4 << 10
)

// registerTayfaLibrary is the shared library's line in the registry
// (componentRoutes). Its routes are the tayfa's, not an event's.
func registerTayfaLibrary(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc("GET "+libraryPath+"/items", h.authed(h.listLibraryItems))
	mux.HandleFunc("POST "+libraryPath+"/items", h.authed(h.createLibraryItem))
	mux.HandleFunc("GET "+libraryItemPath, h.authed(h.getLibraryItem))
	mux.HandleFunc("PUT "+libraryItemPath, h.authed(h.updateLibraryItem))
	mux.HandleFunc("DELETE "+libraryItemPath, h.authed(h.withdrawLibraryItem))
	mux.HandleFunc("POST "+libraryItemPath+"/request", h.authed(h.requestLibraryLoan))
	mux.HandleFunc("DELETE "+libraryItemPath+"/request", h.authed(h.cancelLibraryRequest))
	mux.HandleFunc("GET "+libraryPath+"/loans", h.authed(h.listLibraryLoans))
	mux.HandleFunc("POST "+libraryLoanPath+"/approve", h.authed(h.approveLibraryLoan))
	mux.HandleFunc("POST "+libraryLoanPath+"/decline", h.authed(h.declineLibraryLoan))
	mux.HandleFunc("POST "+libraryLoanPath+"/hand-over", h.authed(h.handOverLibraryLoan))
	mux.HandleFunc("POST "+libraryLoanPath+"/return", h.authed(h.returnLibraryLoan))
}

func (h componentHandlers) library() corev1.TayfaLibraryServiceClient {
	return corev1.NewTayfaLibraryServiceClient(h.conn)
}

var (
	libraryKinds = newEnum(map[string]corev1.LibraryItemKind{
		"book": corev1.LibraryItemKind_LIBRARY_ITEM_KIND_BOOK, "camping_gear": corev1.LibraryItemKind_LIBRARY_ITEM_KIND_CAMPING_GEAR,
		"board_game": corev1.LibraryItemKind_LIBRARY_ITEM_KIND_BOARD_GAME, "tool": corev1.LibraryItemKind_LIBRARY_ITEM_KIND_TOOL,
		"other": corev1.LibraryItemKind_LIBRARY_ITEM_KIND_OTHER,
	})
	libraryStatuses = newEnum(map[string]corev1.LibraryItemStatus{
		"available": corev1.LibraryItemStatus_LIBRARY_ITEM_STATUS_AVAILABLE, "on_loan": corev1.LibraryItemStatus_LIBRARY_ITEM_STATUS_ON_LOAN,
		"withdrawn": corev1.LibraryItemStatus_LIBRARY_ITEM_STATUS_WITHDRAWN,
	})
	libraryStates = newEnum(map[string]corev1.LibraryLoanState{
		"requested": corev1.LibraryLoanState_LIBRARY_LOAN_STATE_REQUESTED, "approved": corev1.LibraryLoanState_LIBRARY_LOAN_STATE_APPROVED,
		"handed_over": corev1.LibraryLoanState_LIBRARY_LOAN_STATE_HANDED_OVER, "returned": corev1.LibraryLoanState_LIBRARY_LOAN_STATE_RETURNED,
		"declined": corev1.LibraryLoanState_LIBRARY_LOAN_STATE_DECLINED, "cancelled": corev1.LibraryLoanState_LIBRARY_LOAN_STATE_CANCELLED,
		"closed": corev1.LibraryLoanState_LIBRARY_LOAN_STATE_CLOSED,
	})
)

type libraryLoanJSON struct {
	ID             string     `json:"id"`
	ItemID         string     `json:"item_id"`
	ItemName       string     `json:"item_name"`
	ItemKind       string     `json:"item_kind"`
	OwnerUserID    string     `json:"owner_user_id,omitempty"`
	BorrowerUserID string     `json:"borrower_user_id,omitempty"`
	State          string     `json:"state"`
	RequestedAt    time.Time  `json:"requested_at"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
	HandedOverAt   *time.Time `json:"handed_over_at,omitempty"`
	DueAt          *time.Time `json:"due_at,omitempty"`
	OrphanedAt     *time.Time `json:"orphaned_at,omitempty"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	CallerIsOwner  bool       `json:"caller_is_owner"`
}

type libraryQueueEntryJSON struct {
	UserID string `json:"user_id"`
	Rank   int32  `json:"rank"`
}

type libraryItemJSON struct {
	ID              string                  `json:"id"`
	Kind            string                  `json:"kind"`
	Name            string                  `json:"name"`
	VocabularyID    string                  `json:"vocabulary_id,omitempty"`
	Note            string                  `json:"note,omitempty"`
	PhotoUploadID   string                  `json:"photo_upload_id,omitempty"`
	OwnerUserID     string                  `json:"owner_user_id,omitempty"`
	Status          string                  `json:"status"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
	QueueLength     int32                   `json:"queue_length"`
	CallerQueueRank int32                   `json:"caller_queue_rank,omitempty"`
	ActiveLoan      *libraryLoanJSON        `json:"active_loan,omitempty"`
	Queue           []libraryQueueEntryJSON `json:"queue,omitempty"`
}

type libraryItemListJSON struct {
	Items        []libraryItemJSON `json:"items"`
	CallerCanAdd bool              `json:"caller_can_add"`
}

type libraryLoanListJSON struct {
	Loans []libraryLoanJSON `json:"loans"`
}

func libraryLoanFrom(l *corev1.LibraryLoan) libraryLoanJSON {
	return libraryLoanJSON{
		ID: l.GetId(), ItemID: l.GetItemId(), ItemName: l.GetItemName(), ItemKind: libraryKinds.name(l.GetItemKind()),
		OwnerUserID: l.GetOwnerId(), BorrowerUserID: l.GetBorrowerId(), State: libraryStates.name(l.GetState()),
		RequestedAt: l.GetRequestTime().AsTime(), ApprovedAt: optTime(l.GetApproveTime()), HandedOverAt: optTime(l.GetHandOverTime()),
		DueAt: optTime(l.GetDueTime()), OrphanedAt: optTime(l.GetOrphanTime()), EndedAt: optTime(l.GetEndTime()),
		CallerIsOwner: l.GetCallerIsOwner(),
	}
}

func libraryItemFrom(it *corev1.LibraryItem) libraryItemJSON {
	out := libraryItemJSON{
		ID: it.GetId(), Kind: libraryKinds.name(it.GetKind()), Name: it.GetName(), VocabularyID: it.GetVocabularyId(),
		Note: it.GetNote(), PhotoUploadID: it.GetPhotoUploadId(), OwnerUserID: it.GetOwnerId(),
		Status: libraryStatuses.name(it.GetStatus()), CreatedAt: it.GetCreateTime().AsTime(), UpdatedAt: it.GetUpdateTime().AsTime(),
		QueueLength: it.GetQueueLength(), CallerQueueRank: it.GetCallerQueueRank(),
	}
	if l := it.GetActiveLoan(); l != nil {
		v := libraryLoanFrom(l)
		out.ActiveLoan = &v
	}
	for _, e := range it.GetQueue() {
		out.Queue = append(out.Queue, libraryQueueEntryJSON{UserID: e.GetUserId(), Rank: e.GetRank()})
	}
	return out
}

// libraryTexts checks what the gateway can of a name, a note and a photo.
func libraryTexts(name, note, photo string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= maxLibraryNameBytes && len(note) <= maxLibraryNoteBytes &&
		(photo == "" || validUUID(photo))
}

// pathLibrary reads and checks the path's tayfa and the named ID.
func pathLibrary(w http.ResponseWriter, r *http.Request, name string) (tayfaID, v string, ok bool) {
	if tayfaID, ok = pathUUID(w, r, "tayfa_id"); !ok {
		return "", "", false
	}
	if v, ok = pathUUID(w, r, name); !ok {
		return "", "", false
	}
	return tayfaID, v, true
}

func (h componentHandlers) listLibraryItems(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	q := r.URL.Query()
	kind, ok := libraryKinds.valueOrUnspecified(q.Get("kind"))
	if !ok {
		badRequest(w, r)
		return
	}
	var mine bool
	switch q.Get("mine") {
	case "", "false":
	case "true":
		mine = true
	default:
		badRequest(w, r)
		return
	}
	resp, err := h.library().ListLibraryItems(ctx, &corev1.ListLibraryItemsRequest{TayfaId: tf, Kind: kind, Mine: mine})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := libraryItemListJSON{Items: make([]libraryItemJSON, 0, len(resp.GetItems())), CallerCanAdd: resp.GetCallerCanAdd()}
	for _, it := range resp.GetItems() {
		out.Items = append(out.Items, libraryItemFrom(it))
	}
	writeJSON(w, http.StatusOK, out)
}

type libraryCreateBody struct {
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Note            string `json:"note"`
	PhotoUploadID   string `json:"photo_upload_id"`
	ClientRequestID string `json:"client_request_id"`
}

func (h componentHandlers) createLibraryItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b libraryCreateBody
	if !decode(w, r, &b) {
		return
	}
	kind, ok := libraryKinds.value(b.Kind)
	if !ok || !libraryTexts(b.Name, b.Note, b.PhotoUploadID) || !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.library().CreateLibraryItem(ctx, &corev1.CreateLibraryItemRequest{
		TayfaId: tf, Kind: kind, Name: b.Name, Note: b.Note, PhotoUploadId: b.PhotoUploadID, ClientRequestId: b.ClientRequestID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, libraryItemFrom(resp.GetItem()))
}

func (h componentHandlers) getLibraryItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, item, ok := pathLibrary(w, r, "library_item_id")
	if !ok {
		return
	}
	resp, err := h.library().GetLibraryItem(ctx, &corev1.GetLibraryItemRequest{TayfaId: tf, ItemId: item})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, libraryItemFrom(resp.GetItem()))
}

type libraryUpdateBody struct {
	Name          string `json:"name"`
	Note          string `json:"note"`
	PhotoUploadID string `json:"photo_upload_id"`
}

func (h componentHandlers) updateLibraryItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, item, ok := pathLibrary(w, r, "library_item_id")
	if !ok {
		return
	}
	var b libraryUpdateBody
	if !decode(w, r, &b) {
		return
	}
	if !libraryTexts(b.Name, b.Note, b.PhotoUploadID) {
		badRequest(w, r)
		return
	}
	resp, err := h.library().UpdateLibraryItem(ctx, &corev1.UpdateLibraryItemRequest{
		TayfaId: tf, ItemId: item, Name: b.Name, Note: b.Note, PhotoUploadId: b.PhotoUploadID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, libraryItemFrom(resp.GetItem()))
}

func (h componentHandlers) withdrawLibraryItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, item, ok := pathLibrary(w, r, "library_item_id")
	if !ok {
		return
	}
	if _, err := h.library().WithdrawLibraryItem(ctx, &corev1.WithdrawLibraryItemRequest{TayfaId: tf, ItemId: item}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h componentHandlers) requestLibraryLoan(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, item, ok := pathLibrary(w, r, "library_item_id")
	if !ok {
		return
	}
	resp, err := h.library().RequestLibraryLoan(ctx, &corev1.RequestLibraryLoanRequest{TayfaId: tf, ItemId: item})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, libraryItemFrom(resp.GetItem()))
}

func (h componentHandlers) cancelLibraryRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, item, ok := pathLibrary(w, r, "library_item_id")
	if !ok {
		return
	}
	resp, err := h.library().CancelLibraryRequest(ctx, &corev1.CancelLibraryRequestRequest{TayfaId: tf, ItemId: item})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, libraryItemFrom(resp.GetItem()))
}

func (h componentHandlers) listLibraryLoans(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.library().ListLibraryLoans(ctx, &corev1.ListLibraryLoansRequest{TayfaId: tf})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := libraryLoanListJSON{Loans: make([]libraryLoanJSON, 0, len(resp.GetLoans()))}
	for _, l := range resp.GetLoans() {
		out.Loans = append(out.Loans, libraryLoanFrom(l))
	}
	writeJSON(w, http.StatusOK, out)
}

// libraryLoanStep runs an owner's step and writes the loan.
func (h componentHandlers) libraryLoanStep(w http.ResponseWriter, r *http.Request, step func(tayfa, loan string) (*corev1.LibraryLoan, error)) {
	tf, loan, ok := pathLibrary(w, r, "library_loan_id")
	if !ok {
		return
	}
	l, err := step(tf, loan)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, libraryLoanFrom(l))
}

func (h componentHandlers) approveLibraryLoan(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	h.libraryLoanStep(w, r, func(tf, loan string) (*corev1.LibraryLoan, error) {
		resp, err := h.library().ApproveLibraryLoan(ctx, &corev1.ApproveLibraryLoanRequest{TayfaId: tf, LoanId: loan})
		return resp.GetLoan(), err
	})
}

func (h componentHandlers) declineLibraryLoan(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	h.libraryLoanStep(w, r, func(tf, loan string) (*corev1.LibraryLoan, error) {
		resp, err := h.library().DeclineLibraryLoan(ctx, &corev1.DeclineLibraryLoanRequest{TayfaId: tf, LoanId: loan})
		return resp.GetLoan(), err
	})
}

type libraryHandOverBody struct {
	DueAt *time.Time `json:"due_at"`
}

func (h componentHandlers) handOverLibraryLoan(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b libraryHandOverBody
	// The body is optional: no body, no return date.
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	req := &corev1.HandOverLibraryLoanRequest{}
	if b.DueAt != nil {
		req.DueTime = timestamppb.New(*b.DueAt)
	}
	h.libraryLoanStep(w, r, func(tf, loan string) (*corev1.LibraryLoan, error) {
		req.TayfaId, req.LoanId = tf, loan
		resp, err := h.library().HandOverLibraryLoan(ctx, req)
		return resp.GetLoan(), err
	})
}

func (h componentHandlers) returnLibraryLoan(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	h.libraryLoanStep(w, r, func(tf, loan string) (*corev1.LibraryLoan, error) {
		resp, err := h.library().ReturnLibraryLoan(ctx, &corev1.ReturnLibraryLoanRequest{TayfaId: tf, LoanId: loan})
		return resp.GetLoan(), err
	})
}
