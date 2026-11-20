package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/badge"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/stats"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// JobStats computes the tayfa and personal statistics and gives the
// threshold and first badges (issue #266; ADR-0026 §2, ADR-0039 §2).
const JobStats = "stats"

// DefaultStatsInterval: a frozen event, a deleted one or a vocabulary
// merge reaches the statistics within a quarter of an hour.
const DefaultStatsInterval = 15 * time.Minute

const (
	// statsMarkBatch bounds the events marked counted or dropped in one
	// transaction.
	statsMarkBatch = 500
	// statsTayfaBatch bounds the tayfas recomputed in one transaction.
	statsTayfaBatch = 10
)

// ComputeStats queues the tayfas of events that froze, and of counted
// events that were deleted or cancelled since, then recomputes every
// queued tayfa (package stats; the vocabulary consumer queues too). It
// returns how many events it marked plus how many tayfas it recomputed.
// Nothing is logged but the count.
func (s *Service) ComputeStats(ctx context.Context) (int, error) {
	now := s.now()
	marked, err := s.drain(ctx, JobStats, statsMarkBatch, func(q *store.Queries) (int, error) {
		counted, err := q.MarkCountedStatEvents(ctx, store.MarkCountedStatEventsParams{
			Now: ts(now), FrozenBefore: ts(now.Add(-component.FreezeAfter)), N: statsMarkBatch,
		})
		if err != nil {
			return 0, fmt.Errorf("donan etkinlikler: %w", err)
		}
		dropped, err := q.MarkDroppedStatEvents(ctx, store.MarkDroppedStatEventsParams{Now: ts(now), N: statsMarkBatch})
		if err != nil {
			return 0, fmt.Errorf("düşen etkinlikler: %w", err)
		}
		for _, t := range append(counted, dropped...) {
			if err := stats.Queue(ctx, q, id.ID(t.Bytes), now); err != nil {
				return 0, err
			}
		}
		return max(len(counted), len(dropped)), nil
	})
	if err != nil {
		return marked, err
	}
	computed, err := s.drain(ctx, JobStats, statsTayfaBatch, func(q *store.Queries) (int, error) {
		tayfas, err := q.ClaimTayfaStats(ctx, statsTayfaBatch)
		if err != nil {
			return 0, fmt.Errorf("istatistik kuyruğu: %w", err)
		}
		if len(tayfas) == 0 {
			return 0, nil
		}
		badges, err := badge.Usable(ctx, q)
		if err != nil {
			return 0, err
		}
		for _, t := range tayfas {
			if err := stats.Recompute(ctx, q, id.ID(t.Bytes), now, badges); err != nil {
				return 0, err
			}
			if err := q.DeleteTayfaStatQueue(ctx, t); err != nil {
				return 0, fmt.Errorf("istatistik kuyruğu: %w", err)
			}
		}
		return len(tayfas), nil
	})
	return marked + computed, err
}
