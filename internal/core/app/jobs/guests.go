package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// JobGuestNicknames clears the nicknames of guest players (issue #252,
// ADR-0037 §6).
const JobGuestNicknames = "guest-nicknames"

// DefaultGuestNicknamesInterval: the job runs daily.
const DefaultGuestNicknamesInterval = 24 * time.Hour

// GuestNicknameRetention: a guest's nickname is cleared this long after the
// event ended (ADR-0037 §6).
const GuestNicknameRetention = 30 * 24 * time.Hour

const guestBatch = 500

// ClearGuestNicknames clears the nicknames of the guest players of events
// that ended GuestNicknameRetention ago and returns how many. The guest
// stays ("Guest"): its seats, scores and draw places keep their meaning.
// Nothing is logged but the count.
func (s *Service) ClearGuestNicknames(ctx context.Context) (int, error) {
	before := ts(s.now().Add(-GuestNicknameRetention))
	return s.drain(ctx, JobGuestNicknames, guestBatch, func(q *store.Queries) (int, error) {
		n, err := q.ClearOldGuestNicknames(ctx, store.ClearOldGuestNicknamesParams{EndedBefore: before, N: guestBatch})
		if err != nil {
			return 0, fmt.Errorf("misafir takma adı temizliği: %w", err)
		}
		return int(n), nil //nolint:gosec // at most one batch
	})
}
