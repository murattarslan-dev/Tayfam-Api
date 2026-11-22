package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/library/lending"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// JobLibrary runs the library's passes (issue #270; ADR-0040 §1): the
// return date reminders, closing the loans left open after a side left,
// and the retention of the loan history and withdrawn items.
const JobLibrary = "library"

// DefaultLibraryInterval: the job runs every 15 minutes, so a reminder
// comes soon after the return date.
const DefaultLibraryInterval = 15 * time.Minute

// Library retention (ADR-0040 §1).
const (
	// LibraryLoanRetention: an ended loan is deleted a year after it ended
	// ("ödünç geçmişi 1 yıl tutulur").
	LibraryLoanRetention = 365 * 24 * time.Hour
	// LibraryItemRetention: a withdrawn item is deleted this long after it
	// was withdrawn, once no loan names it any more.
	LibraryItemRetention = 30 * 24 * time.Hour
)

const libraryBatch = 200

// RunLibrary sends the due reminders, closes the orphaned loans
// (lending.OrphanWindow) and purges old loans and withdrawn items. It
// returns how many rows it changed; nothing is logged but the count.
func (s *Service) RunLibrary(ctx context.Context) (int, error) {
	now := s.now()
	total := 0
	for _, step := range []struct {
		name string
		fn   func(q *store.Queries) (int, error)
	}{
		{"iade hatırlatması", func(q *store.Queries) (int, error) { return lending.RemindDue(ctx, q, now, libraryBatch) }},
		{"kapanmamış ödünç", func(q *store.Queries) (int, error) { return lending.CloseOrphaned(ctx, q, now, libraryBatch) }},
		{"ödünç geçmişi", func(q *store.Queries) (int, error) {
			n, err := q.PurgeLibraryLoans(ctx, store.PurgeLibraryLoansParams{EndedBefore: ts(now.Add(-LibraryLoanRetention)), N: libraryBatch})
			return int(n), err //nolint:gosec // at most one batch
		}},
		{"geri çekilmiş kalemler", func(q *store.Queries) (int, error) {
			n, err := q.PurgeLibraryItems(ctx, store.PurgeLibraryItemsParams{WithdrawnBefore: ts(now.Add(-LibraryItemRetention)), N: libraryBatch})
			return int(n), err //nolint:gosec // at most one batch
		}},
	} {
		n, err := s.drain(ctx, JobLibrary, libraryBatch, step.fn)
		total += n
		if err != nil {
			return total, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return total, nil
}
