package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/badge"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// JobChampionBadges gives the champion badges of events whose celebration
// froze (issue #258; ADR-0039 §2).
const JobChampionBadges = "champion-badges"

// DefaultChampionBadgesInterval: a badge comes at most an hour after the
// celebration froze.
const DefaultChampionBadgesInterval = time.Hour

// badgeEventBatch bounds the events looked at in one transaction.
const badgeEventBatch = 50

// AwardChampionBadges looks at every event whose celebration froze (it
// ended component.FreezeAfter ago, was not cancelled or deleted) once,
// gives its announced winners the active champion badges that fit, and
// returns how many events it looked at. The threshold and first badges
// are the statistics job's (issue #266). Nothing is logged but the count.
func (s *Service) AwardChampionBadges(ctx context.Context) (int, error) {
	now := s.now()
	return s.drain(ctx, JobChampionBadges, badgeEventBatch, func(q *store.Queries) (int, error) {
		rows, err := q.ListChampionBadgeEvents(ctx, store.ListChampionBadgeEventsParams{
			FrozenBefore: ts(now.Add(-component.FreezeAfter)), N: badgeEventBatch,
		})
		if err != nil {
			return 0, fmt.Errorf("şampiyonluk rozeti etkinlikleri: %w", err)
		}
		if len(rows) == 0 {
			return 0, nil
		}
		badges, err := badge.Usable(ctx, q)
		if err != nil {
			return 0, err
		}
		for _, r := range rows {
			ev := badge.Event{ID: id.ID(r.ID.Bytes), Tayfa: id.ID(r.TayfaID.Bytes), Type: r.Type, Version: r.TypeVersion}
			if _, err := badge.AwardChampions(ctx, q, ev, badges, now); err != nil {
				return 0, err
			}
		}
		return len(rows), nil
	})
}
