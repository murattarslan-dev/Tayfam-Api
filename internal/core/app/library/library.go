// Package library is the tayfa's shared library — ortak eşya (issue #270;
// ADR-0040 §1): books, camping gear, board games, tools that members lend
// to each other. It is a module (tayfa_modules "library"); its home widget
// "library" (package home) is the module's showcase. With the module off
// every method gets apperr.ErrModuleDisabled.
//
// Who may do what (the permission table, package authz):
//
//	read the items                   every active member (TayfaView)
//	add an item                      a member (LibraryItemAdd)
//	edit one's own item              its owner (LibraryItemEditOwn)
//	withdraw one's own item          its owner (LibraryItemWithdrawOwn)
//	remove anyone's item             moderators and admins
//	                                 (LibraryItemRemoveAny)
//	ask for an item, wait for it     a member but the owner (LibraryBorrow)
//	take back one's request          the borrower (LibraryBorrowCancel)
//	approve, decline, hand over,     the owner (LibraryLendOwn)
//	take back
//
// # Loans
//
// requested → approved → handed_over → returned, or declined (the owner,
// before handing over) or cancelled (the borrower before handing over, a
// withdrawn item, a member who left). One active loan per item; while there
// is one, a request takes a place in the item's queue and gets its loan
// request when the active loan ends (lending.Promote). The return date is
// optional, set when handing over, at most MaxLoanDays later; once it
// passes the borrower gets one in-app reminder (jobs). No money, deposit or
// fine can be expressed.
//
// The steps tell the other side (issue #321; ADR-0040 §1, revised
// 2026-11-23), in-app and by push, by IDs only: a new request and a queue
// place the owner, approving, declining and handing over the borrower
// (tell). Nobody hears across a block. Taking back, a request opened from
// the queue and requests ended by a withdrawal or a departure tell nobody.
//
// Only the two sides see a loan: members see an item's status (available,
// on_loan) and how many wait; the owner sees the active loan and the queue,
// the borrower their own loan and place.
//
// A member who leaves the tayfa (leaving, removal, ban, account deletion):
// see lending.Departed.
//
// Names and notes are content: they never reach a log, an audit record or
// an event; IDs do. Export lists the items one owns and one's loans and
// queue places (package export); ended loans are kept for a year.
package library

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/library/lending"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/vocabulary"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Limits (ADR-0040 §1; the same CHECKs are in the database).
const (
	// NameMax bounds an item's name, in characters.
	NameMax = 80
	// BookNameMax bounds a book's name: the tayfa vocabulary's bound.
	BookNameMax = vocabulary.MaxValueLength
	// NoteMax bounds an item's note.
	NoteMax = 300
	// MaxItemsPerOwner bounds the live items of one member in a tayfa.
	MaxItemsPerOwner = 100
	// MaxQueue bounds the members waiting for one item.
	MaxQueue = 20
	// MaxLoanDays bounds the return date: at most this many days after
	// handing over.
	MaxLoanDays = 90
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// BookNamespace is the tayfa vocabulary namespace of book names (ADR-0037
// §4).
const BookNamespace = "book"

// Item kinds (library_items.kind).
const (
	KindBook        = "book"
	KindCampingGear = "camping_gear"
	KindBoardGame   = "board_game"
	KindTool        = "tool"
	KindOther       = "other"
)

// Kinds are the item kinds, in their order in the contract.
var Kinds = []string{KindBook, KindCampingGear, KindBoardGame, KindTool, KindOther}

// Item statuses and loan states.
const (
	StatusAvailable = lending.StatusAvailable
	StatusOnLoan    = lending.StatusOnLoan
	StatusWithdrawn = lending.StatusWithdrawn

	StateRequested  = lending.StateRequested
	StateApproved   = lending.StateApproved
	StateHandedOver = lending.StateHandedOver
	StateReturned   = lending.StateReturned
	StateDeclined   = lending.StateDeclined
	StateCancelled  = lending.StateCancelled
	StateClosed     = lending.StateClosed
)

// Validation fields.
const (
	FieldKind            = "kind"
	FieldName            = "name"
	FieldNote            = "note"
	FieldPhotoUploadID   = "photo_upload_id"
	FieldItemID          = "item_id"
	FieldLoanID          = "loan_id"
	FieldDueTime         = "due_time"
	FieldClientRequestID = "client_request_id"
)

// Audit actions. The records name the item or loan by ID only.
const (
	AuditItemCreated   = "library.item_created"
	AuditItemUpdated   = "library.item_updated"
	AuditItemWithdrawn = "library.item_withdrawn"
	AuditItemRemoved   = "library.item_removed"
)

// targetType is the audit record's subject type (and the report target).
const targetType = "library_item"

// Loan is a loan as one of its sides sees it.
type Loan struct {
	ID       id.ID
	Item     id.ID
	ItemName string
	ItemKind string
	// Owner and Borrower are zero once the account is deleted.
	Owner    id.ID
	Borrower id.ID
	State    string
	// The zero time when not (yet) there.
	RequestedAt  time.Time
	ApprovedAt   time.Time
	HandedOverAt time.Time
	DueAt        time.Time
	OrphanedAt   time.Time
	EndedAt      time.Time
	// CallerIsOwner: the caller owns the item (else is the borrower).
	CallerIsOwner bool
}

// QueueEntry is a member waiting for an item; Rank 1 is next.
type QueueEntry struct {
	User id.ID
	Rank int32
}

// Item is an item of the library as the caller sees it.
type Item struct {
	ID            id.ID
	Kind          string
	Name          string
	VocabularyID  id.ID // a book's name in the vocabulary; zero otherwise
	Note          string
	PhotoUploadID id.ID
	Owner         id.ID
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	QueueLength   int32
	// CallerQueueRank is the caller's place in the queue, 0 when not
	// waiting.
	CallerQueueRank int32
	// ActiveLoan is the active loan, for its two sides only.
	ActiveLoan *Loan
	// Queue is the queue, for the owner and in Get only.
	Queue []QueueEntry
}

// Input is an item as its owner writes it. Kind is read on create only.
type Input struct {
	Kind          string
	Name          string
	Note          string
	PhotoUploadID id.ID // zero: none
}

// List is the library's items.
type List struct {
	Items []Item
	// CallerCanAdd reports whether the caller may add an item now.
	CallerCanAdd bool
}

// Service serves the library, built on the component framework's tayfa
// scope (component.Service.Tayfa).
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

func badRune(keep func(rune) bool) func(rune) bool {
	return func(r rune) bool {
		if keep(r) {
			return false
		}
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}
}

// CleanName returns an item's name as stored: one line, trimmed,
// whitespace (line breaks and tabs too) folded to single spaces. Refused:
// empty, invalid UTF-8, any other control or format character, more than
// max characters.
func CleanName(s string, maxLen int) (string, bool) {
	if len(s) > (maxLen+64)*maxRuneBytes || !utf8.ValidString(s) {
		return "", false
	}
	if strings.IndexFunc(s, badRune(func(r rune) bool { return r == '\n' || r == '\r' || r == '\t' })) >= 0 {
		return "", false
	}
	t := strings.Join(strings.Fields(s), " ")
	if t == "" || utf8.RuneCountInString(t) > maxLen {
		return "", false
	}
	return t, true
}

// CleanNote returns a note as stored: line breaks kept ("\r\n" and "\r"
// become "\n"), every line trimmed with its whitespace folded, empty lines
// at the start and the end dropped, at most one empty line in a row; ""
// for none. Refused: invalid UTF-8, any other control or format character,
// more than NoteMax characters.
func CleanNote(s string) (string, bool) {
	if len(s) > (NoteMax+64)*maxRuneBytes || !utf8.ValidString(s) {
		return "", false
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if strings.IndexFunc(s, badRune(func(r rune) bool { return r == '\n' || r == '\t' })) >= 0 {
		return "", false
	}
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" && (len(lines) == 0 || lines[len(lines)-1] == "") {
			continue
		}
		lines = append(lines, l)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	t := strings.Join(lines, "\n")
	if utf8.RuneCountInString(t) > NoteMax {
		return "", false
	}
	return t, true
}

// checkTerms is the one place a library item meets the abuse term check
// (ADR-0012). The term list and its check are not in core yet; when they
// come, the name and the note are checked here and nowhere else.
func checkTerms(...string) error { return nil }

// clean checks an input for kind (the item's kind); the error names the
// field.
func clean(kind string, in Input) (Input, error) {
	if !slices.Contains(Kinds, kind) {
		return Input{}, apperr.Validation(FieldKind)
	}
	maxLen := NameMax
	if kind == KindBook {
		maxLen = BookNameMax
	}
	name, ok := CleanName(in.Name, maxLen)
	if !ok {
		return Input{}, apperr.Validation(FieldName)
	}
	note, ok := CleanNote(in.Note)
	if !ok {
		return Input{}, apperr.Validation(FieldNote)
	}
	if err := checkTerms(name, note); err != nil {
		return Input{}, err
	}
	return Input{Kind: kind, Name: name, Note: note, PhotoUploadID: in.PhotoUploadID}, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func optional(v id.ID) pgtype.UUID {
	if v == (id.ID{}) {
		return pgtype.UUID{}
	}
	return v.PG()
}

func idOf(u pgtype.UUID) id.ID {
	if !u.Valid {
		return id.ID{}
	}
	return id.ID(u.Bytes)
}

func timeOf(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

// read runs fn for a reader: an active member (anyone else gets
// apperr.ErrTayfaNotFound), the module on, TayfaView.
func (s *Service) read(ctx context.Context, user, tayfa id.ID, fn func(sc *component.TayfaScope) error) error {
	return s.fw.Tayfa(ctx, user, tayfa, false, func(sc *component.TayfaScope) error {
		if err := access.RequireModule(ctx, sc.Q(), tayfa, access.ModuleLibrary); err != nil {
			return err
		}
		if err := sc.Authorize(authz.TayfaView); err != nil {
			return err
		}
		return fn(sc)
	})
}

// write runs fn in a transaction with the tayfa locked: an active member,
// the module on, not a read-only tayfa. fn asks for its own permission.
func (s *Service) write(ctx context.Context, user, tayfa id.ID, fn func(sc *component.TayfaScope) error) error {
	return s.fw.Tayfa(ctx, user, tayfa, true, func(sc *component.TayfaScope) error {
		if err := access.RequireModule(ctx, sc.Q(), tayfa, access.ModuleLibrary); err != nil {
			return err
		}
		return fn(sc)
	})
}

// loadItems reads the tayfa's live items (one with item, of an owner with
// owner, of a kind with kind) as user sees them; with queue the owner also
// gets the queue.
func loadItems(ctx context.Context, q *store.Queries, tayfa, user, item, owner id.ID, kind string, queue bool) ([]Item, error) {
	rows, err := q.ListLibraryItems(ctx, store.ListLibraryItemsParams{
		TayfaID: tayfa.PG(), ItemID: optional(item), Kind: text(kind), OwnerID: optional(owner),
	})
	if err != nil {
		return nil, fmt.Errorf("kütüphane: %w", err)
	}
	loans, err := q.ListActiveLibraryLoans(ctx, store.ListActiveLibraryLoansParams{TayfaID: tayfa.PG(), ItemID: optional(item)})
	if err != nil {
		return nil, fmt.Errorf("etkin ödünçler: %w", err)
	}
	active := make(map[id.ID]store.ListActiveLibraryLoansRow, len(loans))
	for _, l := range loans {
		active[id.ID(l.ItemID.Bytes)] = l
	}
	places, err := q.ListLibraryQueue(ctx, store.ListLibraryQueueParams{TayfaID: tayfa.PG(), ItemID: optional(item)})
	if err != nil {
		return nil, fmt.Errorf("sıra: %w", err)
	}
	queues := map[id.ID][]QueueEntry{}
	for _, p := range places {
		it := id.ID(p.ItemID.Bytes)
		queues[it] = append(queues[it], QueueEntry{User: id.ID(p.UserID.Bytes), Rank: p.Rank})
	}
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		it := Item{
			ID: id.ID(r.ID.Bytes), Kind: r.Kind, Name: r.ShownName, VocabularyID: idOf(r.VocabularyID),
			Note: r.Note.String, PhotoUploadID: idOf(r.PhotoUploadID), Owner: idOf(r.OwnerID), Status: r.Status,
			CreatedAt: timeOf(r.CreatedAt), UpdatedAt: timeOf(r.UpdatedAt), QueueLength: r.QueueLength,
		}
		isOwner := it.Owner == user
		for _, e := range queues[it.ID] {
			if e.User == user {
				it.CallerQueueRank = e.Rank
			}
		}
		if queue && isOwner {
			it.Queue = append([]QueueEntry{}, queues[it.ID]...)
		}
		if l, ok := active[it.ID]; ok && (isOwner || idOf(l.BorrowerID) == user) {
			loan, err := loadLoan(ctx, q, tayfa, user, id.ID(l.ID.Bytes))
			if err != nil {
				return nil, err
			}
			it.ActiveLoan = &loan
		}
		out = append(out, it)
	}
	return out, nil
}

// loadItem reads one live item as user sees it.
func loadItem(ctx context.Context, q *store.Queries, tayfa, user, item id.ID, queue bool) (Item, error) {
	items, err := loadItems(ctx, q, tayfa, user, item, id.ID{}, "", queue)
	if err != nil {
		return Item{}, err
	}
	if len(items) != 1 {
		return Item{}, apperr.ErrLibraryItemNotFound
	}
	return items[0], nil
}

func loanOf(r store.ListUserLibraryLoansRow, user id.ID) Loan {
	return Loan{
		ID: id.ID(r.ID.Bytes), Item: id.ID(r.ItemID.Bytes), ItemName: r.ItemName, ItemKind: r.ItemKind,
		Owner: idOf(r.OwnerID), Borrower: idOf(r.BorrowerID), State: r.State,
		RequestedAt: timeOf(r.RequestedAt), ApprovedAt: timeOf(r.ApprovedAt), HandedOverAt: timeOf(r.HandedOverAt),
		DueAt: timeOf(r.DueAt), OrphanedAt: timeOf(r.OrphanedAt), EndedAt: timeOf(r.EndedAt),
		CallerIsOwner: r.OwnerID.Valid && id.ID(r.OwnerID.Bytes) == user,
	}
}

// loadLoan reads a loan of which user is a side.
func loadLoan(ctx context.Context, q *store.Queries, tayfa, user, loan id.ID) (Loan, error) {
	rows, err := q.ListUserLibraryLoans(ctx, store.ListUserLibraryLoansParams{TayfaID: tayfa.PG(), UserID: user.PG(), LoanID: loan.PG()})
	if err != nil {
		return Loan{}, fmt.Errorf("ödünç: %w", err)
	}
	if len(rows) != 1 {
		return Loan{}, apperr.ErrLibraryLoanNotFound
	}
	return loanOf(rows[0], user), nil
}

// List returns the tayfa's live items, the newest first; kind ("" for
// every kind) and mine narrow it. Refused: an unknown kind (validation
// error on kind), anyone but an active member, the module off.
func (s *Service) List(ctx context.Context, user, tayfa id.ID, kind string, mine bool) (List, error) {
	if kind != "" && !slices.Contains(Kinds, kind) {
		return List{}, apperr.Validation(FieldKind)
	}
	var out List
	err := s.read(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		var owner id.ID
		if mine {
			owner = user
		}
		items, err := loadItems(ctx, sc.Q(), tayfa, user, id.ID{}, owner, kind, false)
		if err != nil {
			return err
		}
		out = List{Items: items, CallerCanAdd: sc.Authorize(authz.LibraryItemAdd) == nil && !sc.ReadOnly()}
		return nil
	})
	if err != nil {
		return List{}, err
	}
	return out, nil
}

// Get returns a live item; its owner also gets the queue. Refused: an item
// not in the library (apperr.ErrLibraryItemNotFound) and the refusals of
// every read.
func (s *Service) Get(ctx context.Context, user, tayfa, item id.ID) (Item, error) {
	var out Item
	err := s.read(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		var err error
		out, err = loadItem(ctx, sc.Q(), tayfa, user, item, true)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return out, nil
}

// bookName binds a book's name to the tayfa vocabulary and returns the
// value's ID and its shown name.
func bookName(ctx context.Context, sc *component.TayfaScope, name string) (id.ID, error) {
	return vocabulary.Ensure(ctx, sc.Q(), sc.Tayfa, sc.User, BookNamespace, name, BookNameMax, sc.Now, FieldName)
}

// attach attaches the owner's photo upload to the item, unless it already
// is (the column's trigger checks it again).
func attach(ctx context.Context, sc *component.TayfaScope, upload, item id.ID) error {
	if upload == (id.ID{}) {
		return nil
	}
	return media.Attach(ctx, sc.Q(), sc.User, upload, media.PurposeLibraryItem, sc.Tayfa,
		media.Target{Type: media.TargetLibraryItem, ID: item}, sc.Now)
}

// Create adds an item of the caller's. A repeated client request ID returns
// the item it created. Refused: a bad input (validation error on kind,
// name, note, client_request_id), a photo that is not the caller's ready
// library_item upload of the tayfa (the media errors), too many items
// (apperr.ErrLibraryItemLimit), LibraryItemAdd, a read-only tayfa.
func (s *Service) Create(ctx context.Context, user, tayfa id.ID, in Input, clientRequestID string) (Item, error) {
	crid, ok := id.Parse(clientRequestID)
	if !ok {
		return Item{}, apperr.Validation(FieldClientRequestID)
	}
	c, err := clean(in.Kind, in)
	if err != nil {
		return Item{}, err
	}
	var item id.ID
	err = s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.LibraryItemAdd); err != nil {
			return err
		}
		q := sc.Q()
		prev, err := q.GetLibraryItemByClientRequest(ctx, store.GetLibraryItemByClientRequestParams{TayfaID: tayfa.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			item = id.ID(prev.Bytes)
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("kalem: %w", err)
		}
		n, err := q.CountOwnerLibraryItems(ctx, store.CountOwnerLibraryItemsParams{TayfaID: tayfa.PG(), OwnerID: user.PG()})
		if err != nil {
			return fmt.Errorf("kalemler: %w", err)
		}
		if n >= MaxItemsPerOwner {
			return apperr.ErrLibraryItemLimit
		}
		item = id.New()
		p := store.CreateLibraryItemParams{
			ID: item.PG(), TayfaID: tayfa.PG(), OwnerID: user.PG(), Kind: c.Kind, Name: c.Name,
			Note: text(c.Note), ClientRequestID: crid.PG(), Now: ts(sc.Now),
		}
		if c.Kind == KindBook {
			v, err := bookName(ctx, sc, c.Name)
			if err != nil {
				return err
			}
			p.VocabularyID, p.VocabularyNamespace = v.PG(), text(BookNamespace)
		}
		if err := attach(ctx, sc, c.PhotoUploadID, item); err != nil {
			return err
		}
		p.PhotoUploadID = optional(c.PhotoUploadID)
		if err := q.CreateLibraryItem(ctx, p); err != nil {
			return fmt.Errorf("kalem ekleme: %w", err)
		}
		return sc.Audit(ctx, AuditItemCreated, txn.Target{Type: targetType, ID: item}, map[string]any{"kind": c.Kind})
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, user, tayfa, item)
}

// lockItem reads a live item of the tayfa for a change.
func lockItem(ctx context.Context, q *store.Queries, tayfa, item id.ID) (store.LibraryItem, error) {
	r, err := q.GetLibraryItemForUpdate(ctx, store.GetLibraryItemForUpdateParams{TayfaID: tayfa.PG(), ID: item.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return store.LibraryItem{}, apperr.ErrLibraryItemNotFound
	case err != nil:
		return store.LibraryItem{}, fmt.Errorf("kalem: %w", err)
	}
	if r.Status == StatusWithdrawn {
		return store.LibraryItem{}, apperr.ErrLibraryItemNotFound
	}
	return r, nil
}

func ownedBy(r store.LibraryItem, user id.ID) bool {
	return r.OwnerID.Valid && id.ID(r.OwnerID.Bytes) == user
}

// Update replaces the name, note and photo of the caller's item; its kind
// stays. A zero photo removes it (its file is queued for deletion).
// Refused: a bad input, an item that is not the caller's
// (apperr.ErrLibraryItemNotFound), the media errors, LibraryItemEditOwn, a
// read-only tayfa.
func (s *Service) Update(ctx context.Context, user, tayfa, item id.ID, in Input) (Item, error) {
	err := s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.LibraryItemEditOwn); err != nil {
			return err
		}
		q := sc.Q()
		r, err := lockItem(ctx, q, tayfa, item)
		if err != nil {
			return err
		}
		if !ownedBy(r, user) {
			return apperr.ErrLibraryItemNotFound
		}
		c, err := clean(r.Kind, in)
		if err != nil {
			return err
		}
		p := store.UpdateLibraryItemParams{
			Name: c.Name, Note: text(c.Note), PhotoUploadID: optional(c.PhotoUploadID), Now: ts(sc.Now),
			TayfaID: tayfa.PG(), ID: item.PG(),
		}
		if r.Kind == KindBook {
			v, err := bookName(ctx, sc, c.Name)
			if err != nil {
				return err
			}
			p.VocabularyID, p.VocabularyNamespace = v.PG(), text(BookNamespace)
		}
		if err := attach(ctx, sc, c.PhotoUploadID, item); err != nil {
			return err
		}
		n, err := q.UpdateLibraryItem(ctx, p)
		if err != nil {
			return fmt.Errorf("kalem düzenleme: %w", err)
		}
		if n != 1 {
			return apperr.ErrLibraryItemNotFound
		}
		return sc.Audit(ctx, AuditItemUpdated, txn.Target{Type: targetType, ID: item}, nil)
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, user, tayfa, item)
}

// Withdraw withdraws the caller's own item (LibraryItemWithdrawOwn; not
// while handed over: apperr.ErrLibraryItemOnLoan) or removes anyone's for
// a moderator (LibraryItemRemoveAny; a handed-over loan stays open for its
// owner to take back). See lending.Withdraw. Refused: an item not in the
// library (apperr.ErrLibraryItemNotFound), a read-only tayfa.
func (s *Service) Withdraw(ctx context.Context, user, tayfa, item id.ID) error {
	return s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		q := sc.Q()
		r, err := lockItem(ctx, q, tayfa, item)
		if err != nil {
			return err
		}
		action := AuditItemWithdrawn
		if ownedBy(r, user) {
			if err := sc.Authorize(authz.LibraryItemWithdrawOwn); err != nil {
				return err
			}
			if r.Status == StatusOnLoan {
				return apperr.ErrLibraryItemOnLoan
			}
		} else {
			if err := sc.Authorize(authz.LibraryItemRemoveAny); err != nil {
				return err
			}
			action = AuditItemRemoved
		}
		ok, err := lending.Withdraw(ctx, q, tayfa, item, user, sc.Now)
		if err != nil {
			return err
		}
		if !ok {
			return apperr.ErrLibraryItemNotFound
		}
		return sc.Audit(ctx, action, txn.Target{Type: targetType, ID: item}, nil)
	})
}

// Request asks for an item: a loan request when it has no active loan,
// otherwise a place at the end of its queue. Asking again changes nothing.
// Refused: an item not in the library (apperr.ErrLibraryItemNotFound), the
// caller's own item (apperr.ErrLibraryOwnItem), a full queue
// (apperr.ErrLibraryQueueFull), LibraryBorrow, a read-only tayfa.
func (s *Service) Request(ctx context.Context, user, tayfa, item id.ID) (Item, error) {
	err := s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.LibraryBorrow); err != nil {
			return err
		}
		q := sc.Q()
		r, err := lockItem(ctx, q, tayfa, item)
		if err != nil {
			return err
		}
		if ownedBy(r, user) {
			return apperr.ErrLibraryOwnItem
		}
		loan, err := q.GetActiveLibraryLoanForUpdate(ctx, item.PG())
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if err := q.CreateLibraryLoan(ctx, store.CreateLibraryLoanParams{
				ID: id.New().PG(), ItemID: item.PG(), TayfaID: tayfa.PG(), BorrowerID: user.PG(), Now: ts(sc.Now),
			}); err != nil {
				return fmt.Errorf("ödünç isteği: %w", err)
			}
			return tell(ctx, q, notify.TypeLibraryLoanRequested, tayfa, item, user, r.OwnerID)
		case err != nil:
			return fmt.Errorf("etkin ödünç: %w", err)
		}
		if loan.BorrowerID.Valid && id.ID(loan.BorrowerID.Bytes) == user {
			return nil // already the borrower
		}
		if _, err := q.GetLibraryQueueRank(ctx, store.GetLibraryQueueRankParams{ItemID: item.PG(), UserID: user.PG()}); err == nil {
			return nil // already waiting
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("sıra: %w", err)
		}
		n, err := q.CountLibraryQueue(ctx, item.PG())
		if err != nil {
			return fmt.Errorf("sıra: %w", err)
		}
		if n >= MaxQueue {
			return apperr.ErrLibraryQueueFull
		}
		if err := q.AddLibraryQueue(ctx, store.AddLibraryQueueParams{
			ID: id.New().PG(), ItemID: item.PG(), TayfaID: tayfa.PG(), UserID: user.PG(), Now: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("sıra: %w", err)
		}
		return tell(ctx, q, notify.TypeLibraryQueueJoined, tayfa, item, user, r.OwnerID)
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, user, tayfa, item)
}

// Cancel takes back the caller's request not yet handed over (the next in
// the queue gets the item) or place in the queue; with neither it changes
// nothing. Refused: an item not in the library
// (apperr.ErrLibraryItemNotFound), a handed-over loan
// (apperr.ErrLibraryLoanState), LibraryBorrowCancel, a read-only tayfa.
func (s *Service) Cancel(ctx context.Context, user, tayfa, item id.ID) (Item, error) {
	err := s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.LibraryBorrowCancel); err != nil {
			return err
		}
		q := sc.Q()
		if _, err := lockItem(ctx, q, tayfa, item); err != nil {
			return err
		}
		if _, err := q.DeleteLibraryQueuePlace(ctx, store.DeleteLibraryQueuePlaceParams{ItemID: item.PG(), UserID: user.PG()}); err != nil {
			return fmt.Errorf("sıra: %w", err)
		}
		loan, err := q.GetActiveLibraryLoanForUpdate(ctx, item.PG())
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("etkin ödünç: %w", err)
		}
		if !loan.BorrowerID.Valid || id.ID(loan.BorrowerID.Bytes) != user {
			return nil
		}
		if loan.State == StateHandedOver {
			return apperr.ErrLibraryLoanState
		}
		return end(ctx, q, loan, StateCancelled, sc.Now)
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, user, tayfa, item)
}

// end ends an active loan in state and lets the item go on: back from a
// borrower it is available again, and the next in the queue gets it.
func end(ctx context.Context, q *store.Queries, loan store.LibraryLoan, state string, now time.Time) error {
	from := []string{StateRequested, StateApproved}
	if state == StateReturned {
		from = []string{StateHandedOver}
	}
	n, err := q.EndLibraryLoan(ctx, store.EndLibraryLoanParams{State: state, Now: ts(now), ID: loan.ID, FromStates: from})
	if err != nil {
		return fmt.Errorf("ödünç: %w", err)
	}
	if n != 1 {
		return apperr.ErrLibraryLoanState
	}
	return lending.Freed(ctx, q, id.ID(loan.ItemID.Bytes), state == StateReturned, now)
}

// tell writes the loan step notification typ about item, taken by actor,
// for the other side to (issue #321; ADR-0040 §1, revised 2026-11-23):
// only while to is an active member and neither has blocked the other;
// otherwise nobody hears about it, without a trace. It names the item by
// ID only. It runs last in the step's transaction (notify.Write).
func tell(ctx context.Context, q *store.Queries, typ string, tayfa, item, actor id.ID, to pgtype.UUID) error {
	if !to.Valid {
		return nil // the account was deleted
	}
	rcpt, err := notify.Unblocked(ctx, q, tayfa, []id.ID{id.ID(to.Bytes)}, actor)
	if err != nil {
		return err
	}
	return notify.Write(ctx, q, notify.Request{
		Type: typ, Tayfa: tayfa, Actor: actor, TargetType: notify.TargetLibraryItem, Target: item,
	}, rcpt)
}

// Loans returns the caller's loans in the tayfa, as borrower and as owner:
// the active ones first, then the latest (at most 200). Refused: the
// refusals of every read.
func (s *Service) Loans(ctx context.Context, user, tayfa id.ID) ([]Loan, error) {
	var out []Loan
	err := s.read(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		rows, err := sc.Q().ListUserLibraryLoans(ctx, store.ListUserLibraryLoansParams{TayfaID: tayfa.PG(), UserID: user.PG()})
		if err != nil {
			return fmt.Errorf("ödünçler: %w", err)
		}
		out = make([]Loan, 0, len(rows))
		for _, r := range rows {
			out = append(out, loanOf(r, user))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Owner's steps.
const (
	stepApprove  = "approve"
	stepDecline  = "decline"
	stepHandOver = "hand_over"
	stepReturn   = "return"
)

// lend runs an owner's step on a loan of the caller's item. Refused: a loan
// that is not of the caller's item (apperr.ErrLibraryLoanNotFound), a state
// the step does not follow (apperr.ErrLibraryLoanState), a bad return date
// (validation error on due_time), LibraryLendOwn, a read-only tayfa.
func (s *Service) lend(ctx context.Context, user, tayfa, loan id.ID, step string, due time.Time) (Loan, error) {
	err := s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.LibraryLendOwn); err != nil {
			return err
		}
		q := sc.Q()
		l, err := q.GetLibraryLoanForUpdate(ctx, store.GetLibraryLoanForUpdateParams{TayfaID: tayfa.PG(), ID: loan.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrLibraryLoanNotFound
		case err != nil:
			return fmt.Errorf("ödünç: %w", err)
		}
		// The item is read (and locked) even when withdrawn: its owner
		// still takes a handed-over item back.
		it, err := q.GetLibraryItemForUpdate(ctx, store.GetLibraryItemForUpdateParams{TayfaID: tayfa.PG(), ID: l.ItemID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrLibraryLoanNotFound
		case err != nil:
			return fmt.Errorf("kalem: %w", err)
		}
		if !ownedBy(it, user) {
			return apperr.ErrLibraryLoanNotFound
		}
		switch step {
		case stepApprove:
			n, err := q.ApproveLibraryLoan(ctx, store.ApproveLibraryLoanParams{Now: ts(sc.Now), ID: l.ID})
			if err != nil {
				return fmt.Errorf("onay: %w", err)
			}
			if n != 1 {
				return apperr.ErrLibraryLoanState
			}
			return tell(ctx, q, notify.TypeLibraryLoanApproved, tayfa, id.ID(l.ItemID.Bytes), user, l.BorrowerID)
		case stepDecline:
			if err := end(ctx, q, l, StateDeclined, sc.Now); err != nil {
				return err
			}
			return tell(ctx, q, notify.TypeLibraryLoanDeclined, tayfa, id.ID(l.ItemID.Bytes), user, l.BorrowerID)
		case stepHandOver:
			var dueAt pgtype.Timestamptz
			if !due.IsZero() {
				if !due.After(sc.Now) || due.After(sc.Now.Add(MaxLoanDays*24*time.Hour)) {
					return apperr.Validation(FieldDueTime)
				}
				dueAt = ts(due.UTC())
			}
			n, err := q.HandOverLibraryLoan(ctx, store.HandOverLibraryLoanParams{Now: ts(sc.Now), DueAt: dueAt, ID: l.ID})
			if err != nil {
				return fmt.Errorf("teslim: %w", err)
			}
			if n != 1 {
				return apperr.ErrLibraryLoanState
			}
			if err := q.SetLibraryItemStatus(ctx, store.SetLibraryItemStatusParams{Status: StatusOnLoan, Now: ts(sc.Now), ID: l.ItemID}); err != nil {
				return fmt.Errorf("kalem durumu: %w", err)
			}
			return tell(ctx, q, notify.TypeLibraryLoanHandedOver, tayfa, id.ID(l.ItemID.Bytes), user, l.BorrowerID)
		case stepReturn:
			return end(ctx, q, l, StateReturned, sc.Now)
		default:
			return errors.New("bilinmeyen adım")
		}
	})
	if err != nil {
		return Loan{}, err
	}
	var out Loan
	err = s.read(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		var err error
		out, err = loadLoan(ctx, sc.Q(), tayfa, user, loan)
		return err
	})
	return out, err
}

// Approve approves a requested loan of the caller's item.
func (s *Service) Approve(ctx context.Context, user, tayfa, loan id.ID) (Loan, error) {
	return s.lend(ctx, user, tayfa, loan, stepApprove, time.Time{})
}

// Decline declines a requested or approved loan of the caller's item; the
// next in the queue gets the item.
func (s *Service) Decline(ctx context.Context, user, tayfa, loan id.ID) (Loan, error) {
	return s.lend(ctx, user, tayfa, loan, stepDecline, time.Time{})
}

// HandOver records that the caller handed their item over (the loan must
// be approved), with an optional return date (zero: none) after now and at
// most MaxLoanDays away.
func (s *Service) HandOver(ctx context.Context, user, tayfa, loan id.ID, due time.Time) (Loan, error) {
	return s.lend(ctx, user, tayfa, loan, stepHandOver, due)
}

// Return records that the caller took their item back (the loan must be
// handed over); the item is available again unless withdrawn and the next
// in the queue gets it.
func (s *Service) Return(ctx context.Context, user, tayfa, loan id.ID) (Loan, error) {
	return s.lend(ctx, user, tayfa, loan, stepReturn, time.Time{})
}
