package jobs

import (
	"context"
	"fmt"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// purgeStep hard deletes one batch of one content table.
type purgeStep struct {
	name string
	run  func(ctx context.Context, q *store.Queries, p store.PurgeAnnouncementsParams) (int64, error)
}

// purgeSteps: questions before answers, so a deleted question's answers go
// with it in one statement; events last.
func purgeSteps() []purgeStep {
	return []purgeStep{
		{"duyurular", func(ctx context.Context, q *store.Queries, p store.PurgeAnnouncementsParams) (int64, error) {
			return q.PurgeAnnouncements(ctx, p)
		}},
		{"sorular", func(ctx context.Context, q *store.Queries, p store.PurgeAnnouncementsParams) (int64, error) {
			return q.PurgeQuestions(ctx, store.PurgeQuestionsParams(p))
		}},
		{"cevaplar", func(ctx context.Context, q *store.Queries, p store.PurgeAnnouncementsParams) (int64, error) {
			return q.PurgeAnswers(ctx, store.PurgeAnswersParams(p))
		}},
		{"anketler", func(ctx context.Context, q *store.Queries, p store.PurgeAnnouncementsParams) (int64, error) {
			return q.PurgePolls(ctx, store.PurgePollsParams(p))
		}},
		{"av kayıtları", func(ctx context.Context, q *store.Queries, p store.PurgeAnnouncementsParams) (int64, error) {
			return q.PurgeFishingCatches(ctx, store.PurgeFishingCatchesParams(p))
		}},
		{"etkinlikler", func(ctx context.Context, q *store.Queries, p store.PurgeAnnouncementsParams) (int64, error) {
			return q.PurgeEvents(ctx, store.PurgeEventsParams(p))
		}},
	}
}

// Purge hard deletes what was soft deleted RetentionPeriod ago (v14
// "Saklama süreleri", CLAUDE.md "Silme") and returns how many rows it
// deleted (tayfas and content rows; cascaded rows are not counted).
//
// Tayfas go one per transaction: the row's foreign keys take everything
// in it along (ON DELETE CASCADE), the memberships and audit_logs triggers
// allow that once the tayfa row is gone (migration 00001), and
// tayfa.purged tells chat to delete the tayfa's channels for good
// (ADR-0009). Then announcements, questions (with their answers), answers,
// polls, fishing catches and events go in batches of contentBatch.
// Activity records stay (SET NULL).
func (s *Service) Purge(ctx context.Context) (int, error) {
	before := ts(s.now().Add(-RetentionPeriod))
	total, err := s.drain(ctx, JobPurge, purgeTayfaBatch, func(q *store.Queries) (int, error) {
		tayfas, err := q.ListPurgeableTayfas(ctx, store.ListPurgeableTayfasParams{DeletedBefore: before, N: purgeTayfaBatch})
		if err != nil {
			return 0, fmt.Errorf("silinecek tayfalar: %w", err)
		}
		purged := 0
		for _, t := range tayfas {
			n, err := q.PurgeTayfa(ctx, store.PurgeTayfaParams{ID: t, DeletedBefore: before})
			if err != nil {
				return 0, fmt.Errorf("tayfa %s kalıcı silme: %w", id.ID(t.Bytes), err)
			}
			if n == 0 {
				continue
			}
			purged++
			tf := id.ID(t.Bytes)
			// domain_events.tayfa_id is not a foreign key: the event outlives
			// the tayfa row.
			if err := txn.Event(ctx, q, EventTayfaPurged, tf, id.ID{}, txn.Target{Type: "tayfa", ID: tf},
				map[string]any{"tayfa_id": tf.String()}); err != nil {
				return 0, err
			}
		}
		return purged, nil
	})
	if err != nil {
		return total, err
	}
	for _, step := range purgeSteps() {
		n, err := s.drain(ctx, JobPurge, contentBatch, func(q *store.Queries) (int, error) {
			n, err := step.run(ctx, q, store.PurgeAnnouncementsParams{DeletedBefore: before, N: contentBatch})
			if err != nil {
				return 0, fmt.Errorf("%s kalıcı silme: %w", step.name, err)
			}
			return int(n), nil //nolint:gosec // at most one batch
		})
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// ClearPlates clears the plates of convoy vehicles whose event ended
// PlateRetention ago (v14 "Konvoy aracı": plates are personal data) and
// returns how many.
func (s *Service) ClearPlates(ctx context.Context) (int, error) {
	before := ts(s.now().Add(-PlateRetention))
	return s.drain(ctx, JobPlates, plateBatch, func(q *store.Queries) (int, error) {
		n, err := q.ClearOldPlates(ctx, store.ClearOldPlatesParams{EndedBefore: before, N: plateBatch})
		if err != nil {
			return 0, fmt.Errorf("plaka temizliği: %w", err)
		}
		return int(n), nil //nolint:gosec // at most one batch
	})
}
