// Package lending holds the library steps that do not run for a member's
// request (issue #270; ADR-0040 §1): withdrawing an item with its waiting
// requests, handing the item to the next in its queue, a member leaving the
// tayfa, and the background passes (the return date reminder, closing an
// orphaned loan). It sits under package library but imports only the store
// and the notification writer, so that the membership, account deletion,
// admin and jobs packages can call it without an import cycle.
//
// Every function runs in the caller's transaction (q). Names and notes are
// content: nothing here logs them or puts them in an event.
package lending

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Item statuses (library_items.status).
const (
	StatusAvailable = "available"
	StatusOnLoan    = "on_loan"
	StatusWithdrawn = "withdrawn"
)

// Loan states (library_loans.state).
const (
	StateRequested  = "requested"
	StateApproved   = "approved"
	StateHandedOver = "handed_over"
	StateReturned   = "returned"
	StateDeclined   = "declined"
	StateCancelled  = "cancelled"
	StateClosed     = "closed"
)

// OrphanWindow is how long a handed-over loan stays open after one side
// left the tayfa (ADR-0040 §1: "30 gün sonra kapanır").
const OrphanWindow = 30 * 24 * time.Hour

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func optional(v id.ID) pgtype.UUID {
	if v == (id.ID{}) {
		return pgtype.UUID{}
	}
	return v.PG()
}

// Promote opens a loan request for the first member in item's queue and
// takes them out of it; item must have no active loan and not be
// withdrawn (a withdrawn item has no queue). ok is false when nobody waits.
func Promote(ctx context.Context, q *store.Queries, item id.ID, now time.Time) (borrower id.ID, ok bool, err error) {
	b, err := q.PromoteLibraryQueue(ctx, store.PromoteLibraryQueueParams{ItemID: item.PG(), ID: id.New().PG(), Now: ts(now)})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return id.ID{}, false, nil
	case err != nil:
		return id.ID{}, false, fmt.Errorf("sıradaki: %w", err)
	}
	if !b.Valid {
		return id.ID{}, false, nil
	}
	return id.ID(b.Bytes), true, nil
}

// Withdraw withdraws the tayfa's item: its note and photo go (the photo
// file is queued for deletion), the requests not yet handed over are
// cancelled and the queue is emptied. A handed-over loan stays open for
// the owner to take back. by is who withdrew it (the owner, a moderator, a
// platform administrator); ok is false when the item is not in the tayfa
// or already withdrawn.
func Withdraw(ctx context.Context, q *store.Queries, tayfa, item, by id.ID, now time.Time) (ok bool, err error) {
	n, err := q.WithdrawLibraryItem(ctx, store.WithdrawLibraryItemParams{
		Now: ts(now), WithdrawnBy: optional(by), TayfaID: tayfa.PG(), ID: item.PG(),
	})
	if err != nil {
		return false, fmt.Errorf("kalem geri çekme: %w", err)
	}
	if n != 1 {
		return false, nil
	}
	if err := endWaiting(ctx, q, []pgtype.UUID{item.PG()}, now); err != nil {
		return false, err
	}
	return true, nil
}

// endWaiting empties the queues of items and cancels their loans that
// were not handed over.
func endWaiting(ctx context.Context, q *store.Queries, items []pgtype.UUID, now time.Time) error {
	if len(items) == 0 {
		return nil
	}
	if err := q.DeleteLibraryItemsQueue(ctx, items); err != nil {
		return fmt.Errorf("sıra: %w", err)
	}
	if err := q.CancelPendingLibraryLoans(ctx, store.CancelPendingLibraryLoansParams{Now: ts(now), ItemIds: items}); err != nil {
		return fmt.Errorf("bekleyen istekler: %w", err)
	}
	return nil
}

// Departed applies a member's leaving (ADR-0040 §1 "Üye ayrılınca":
// leaving, removal, ban, account deletion) in the tayfa, or in every tayfa
// when tayfa is zero (account deletion):
//
//   - their items are withdrawn, with the requests not yet handed over and
//     the queues;
//   - their own requests not yet handed over are cancelled (the next in the
//     queue gets the item) and their queue places go;
//   - a handed-over loan on either side stays open (orphaned) and both
//     sides get the TypeLibraryLoanOrphaned warning; it closes after
//     OrphanWindow unless the owner takes the item back first.
//
// warnSelf leaves the departing member among the warned (false for a
// deleted account, which has nobody to warn).
func Departed(ctx context.Context, q *store.Queries, tayfa, user id.ID, now time.Time, warnSelf bool) error {
	tf := optional(tayfa)
	items, err := q.WithdrawUserLibraryItems(ctx, store.WithdrawUserLibraryItemsParams{Now: ts(now), UserID: user.PG(), TayfaID: tf})
	if err != nil {
		return fmt.Errorf("kalemler: %w", err)
	}
	if err := endWaiting(ctx, q, items, now); err != nil {
		return err
	}
	if err := q.DeleteUserLibraryQueuePlaces(ctx, store.DeleteUserLibraryQueuePlacesParams{UserID: user.PG(), TayfaID: tf}); err != nil {
		return fmt.Errorf("sıra yerleri: %w", err)
	}
	freed, err := q.CancelUserLibraryRequests(ctx, store.CancelUserLibraryRequestsParams{Now: ts(now), UserID: user.PG(), TayfaID: tf})
	if err != nil {
		return fmt.Errorf("istekler: %w", err)
	}
	for _, it := range freed {
		if _, _, err := Promote(ctx, q, id.ID(it.Bytes), now); err != nil {
			return err
		}
	}
	orphaned, err := q.OrphanUserLibraryLoans(ctx, store.OrphanUserLibraryLoansParams{Now: ts(now), UserID: user.PG(), TayfaID: tf})
	if err != nil {
		return fmt.Errorf("açık ödünçler: %w", err)
	}
	for _, l := range orphaned {
		var to []id.ID
		for _, u := range []pgtype.UUID{l.OwnerID, l.BorrowerID} {
			if !u.Valid {
				continue
			}
			if v := id.ID(u.Bytes); v != user || warnSelf {
				to = append(to, v)
			}
		}
		if err := notify.Write(ctx, q, notify.Request{
			Type: notify.TypeLibraryLoanOrphaned, Tayfa: id.ID(l.TayfaID.Bytes),
			TargetType: notify.TargetTayfa, Target: id.ID(l.TayfaID.Bytes),
		}, to); err != nil {
			return err
		}
	}
	return nil
}

// RemindDue marks at most n handed-over loans whose return date passed and
// sends each borrower who is still an active member the one
// TypeLibraryDue reminder. It returns how many loans it marked.
func RemindDue(ctx context.Context, q *store.Queries, now time.Time, n int32) (int, error) {
	rows, err := q.MarkDueLibraryLoans(ctx, store.MarkDueLibraryLoansParams{Now: ts(now), N: n})
	if err != nil {
		return 0, fmt.Errorf("iade hatırlatması: %w", err)
	}
	for _, r := range rows {
		if !r.BorrowerID.Valid {
			continue
		}
		tayfa := id.ID(r.TayfaID.Bytes)
		to, err := notify.Only(ctx, q, tayfa, []id.ID{id.ID(r.BorrowerID.Bytes)}, id.ID{})
		if err != nil {
			return 0, fmt.Errorf("iade hatırlatması: %w", err)
		}
		if err := notify.Write(ctx, q, notify.Request{
			Type: notify.TypeLibraryDue, Tayfa: tayfa, TargetType: notify.TargetTayfa, Target: tayfa,
		}, to); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// CloseOrphaned closes at most n loans orphaned OrphanWindow ago: the
// item, unless withdrawn, is available again and the next in its queue
// gets it. It returns how many loans it closed.
func CloseOrphaned(ctx context.Context, q *store.Queries, now time.Time, n int32) (int, error) {
	items, err := q.CloseOrphanedLibraryLoans(ctx, store.CloseOrphanedLibraryLoansParams{
		Now: ts(now), OrphanedBefore: ts(now.Add(-OrphanWindow)), N: n,
	})
	if err != nil {
		return 0, fmt.Errorf("kapanmamış ödünçler: %w", err)
	}
	for _, it := range items {
		if err := Freed(ctx, q, id.ID(it.Bytes), true, now); err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

// Freed follows the end of item's active loan: back from a borrower
// (wasOut) the item is available again unless withdrawn, and the next in
// its queue gets a loan request.
func Freed(ctx context.Context, q *store.Queries, item id.ID, wasOut bool, now time.Time) error {
	if wasOut {
		if err := q.SetLibraryItemStatus(ctx, store.SetLibraryItemStatusParams{Status: StatusAvailable, Now: ts(now), ID: item.PG()}); err != nil {
			return fmt.Errorf("kalem durumu: %w", err)
		}
	}
	_, _, err := Promote(ctx, q, item, now)
	return err
}
