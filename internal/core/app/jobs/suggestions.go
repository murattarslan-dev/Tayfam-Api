package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Friendship score and friend suggestions (issue #208, ADR-0028).
const (
	// ScoreWindow: shared attendance older than this does not count.
	ScoreWindow = 365 * 24 * time.Hour
	// ScoreHalfLife: a shared attendance this old weighs half.
	ScoreHalfLife = 180 * 24 * time.Hour
	// MinSharedForSuggestion: at least this many verified shared
	// attendances.
	MinSharedForSuggestion = 3
	// SuggestionInterval: at most one suggestion per recipient in this
	// long.
	SuggestionInterval = 7 * 24 * time.Hour
	// CandidatesPerRecipient: candidates tried per recipient and run (by
	// score); the age check may rule some out.
	CandidatesPerRecipient = 5
	// OpenSuggestionRetention: an open (not dismissed) suggestion row is
	// deleted after this long; dismissed rows stay until the account is
	// deleted.
	OpenSuggestionRetention = 365 * 24 * time.Hour
	// DefaultFriendSuggestionsInterval: the job runs daily.
	DefaultFriendSuggestionsInterval = 24 * time.Hour
	// candidateBatch bounds the candidate rows read at once.
	candidateBatch = 500
)

// MinorChecker answers whether users are under 18 (identity's
// MinorService.IsMinor, package identityminor). Users it cannot answer for
// are missing from the map. The answer is used once and never stored,
// returned or logged.
type MinorChecker interface {
	IsMinor(ctx context.Context, users []id.ID) (map[id.ID]bool, error)
}

// SuggestFriends recomputes every friendship score, then suggests at most
// one friend to each recipient who had none for SuggestionInterval, and
// deletes old open suggestions. It returns the score rows written plus the
// suggestions made plus the rows deleted.
//
// The scores are rewritten in one transaction: a pair with a verified
// shared attendance in the last ScoreWindow weighs 0.5^(age/ScoreHalfLife)
// per event (ADR-0028 §2). A candidate is suggested only when identity
// answers for both and both are on the same side of 18; without an answer
// there is no suggestion (the safe side).
func (s *Service) SuggestFriends(ctx context.Context) (int, error) {
	now := s.now()
	scores, err := s.batch(ctx, JobFriendSuggestions, func(q *store.Queries) (int, error) {
		if err := q.DeleteAllFriendScores(ctx); err != nil {
			return 0, fmt.Errorf("puan silme: %w", err)
		}
		n, err := q.InsertFriendScores(ctx, store.InsertFriendScoresParams{
			Now: ts(now), Since: ts(now.Add(-ScoreWindow)), HalfLifeSeconds: ScoreHalfLife.Seconds(),
		})
		if err != nil {
			return 0, fmt.Errorf("puan hesaplama: %w", err)
		}
		return int(n), nil //nolint:gosec // row count
	})
	if errors.Is(err, errLocked) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	made, err := s.makeSuggestions(ctx, now)
	if err != nil {
		return scores + made, err
	}
	expired, err := s.drain(ctx, JobFriendSuggestions, retentionBatch, func(q *store.Queries) (int, error) {
		n, err := q.DeleteOldOpenFriendSuggestions(ctx, ts(now.Add(-OpenSuggestionRetention)))
		if err != nil {
			return 0, fmt.Errorf("eski öneriler: %w", err)
		}
		return int(n), nil //nolint:gosec // row count
	})
	return scores + made + expired, err
}

// makeSuggestions walks the candidates recipient by recipient.
func (s *Service) makeSuggestions(ctx context.Context, now time.Time) (int, error) {
	q := store.New(s.cfg.Pool)
	after := pgtype.UUID{Valid: true} // the nil UUID: before every recipient
	made := 0
	for range maxBatches {
		if err := ctx.Err(); err != nil {
			return made, err
		}
		rows, err := q.ListSuggestionCandidates(ctx, store.ListSuggestionCandidatesParams{
			MinShared: MinSharedForSuggestion, AfterUser: after, WeekAgo: ts(now.Add(-SuggestionInterval)),
			PerUser: CandidatesPerRecipient, N: candidateBatch,
		})
		if err != nil {
			return made, fmt.Errorf("öneri adayları: %w", err)
		}
		for start := 0; start < len(rows); {
			end := start
			for end < len(rows) && rows[end].RecipientID == rows[start].RecipientID {
				end++
			}
			recipient := id.ID(rows[start].RecipientID.Bytes)
			candidates := make([]id.ID, 0, end-start)
			for _, r := range rows[start:end] {
				candidates = append(candidates, id.ID(r.CandidateID.Bytes))
			}
			ok, err := s.suggestOne(ctx, now, recipient, candidates)
			if err != nil {
				return made, err
			}
			if ok {
				made++
			}
			start = end
		}
		if len(rows) < candidateBatch {
			break
		}
		after = rows[len(rows)-1].RecipientID
	}
	return made, nil
}

// suggestOne asks identity about the recipient and the candidates and
// suggests the first candidate on the recipient's side of 18. It reports
// whether a suggestion was written.
func (s *Service) suggestOne(ctx context.Context, now time.Time, recipient id.ID, candidates []id.ID) (bool, error) {
	pick, ok := s.sameAgeSide(ctx, recipient, candidates)
	if !ok {
		return false, nil
	}
	wrote := false
	_, err := s.batch(ctx, JobFriendSuggestions, func(q *store.Queries) (int, error) {
		if err := q.LockSuggestionRecipient(ctx, recipient.PG()); err != nil {
			return 0, fmt.Errorf("öneri kilidi: %w", err)
		}
		n, err := q.CreateFriendSuggestion(ctx, store.CreateFriendSuggestionParams{
			UserID: recipient.PG(), SuggestedUserID: pick.PG(), Now: ts(now), WeekAgo: ts(now.Add(-SuggestionInterval)),
		})
		if err != nil {
			return 0, fmt.Errorf("öneri: %w", err)
		}
		if n != 1 {
			return 0, nil
		}
		wrote = true
		// The recipient opens the suggested user's profile; no actor, no
		// reason, no score.
		return 1, notify.Write(ctx, q, notify.Request{
			Type: notify.TypeFriendSuggestion, TargetType: notify.TargetUser, Target: pick,
		}, []id.ID{recipient})
	})
	if errors.Is(err, errLocked) {
		return false, nil
	}
	return wrote, err
}

// sameAgeSide returns the first candidate identity answered for that is
// on the recipient's side of 18. Without an answer for the recipient, or
// when identity cannot be asked, there is none. Nothing of the answer is
// kept or logged.
func (s *Service) sameAgeSide(ctx context.Context, recipient id.ID, candidates []id.ID) (id.ID, bool) {
	if s.cfg.Minors == nil || len(candidates) == 0 {
		return id.ID{}, false
	}
	ask := append([]id.ID{recipient}, candidates...)
	answers, err := s.cfg.Minors.IsMinor(ctx, ask)
	if err != nil {
		return id.ID{}, false
	}
	side, ok := answers[recipient]
	if !ok {
		return id.ID{}, false
	}
	for _, c := range candidates {
		if m, ok := answers[c]; ok && m == side {
			return c, true
		}
	}
	return id.ID{}, false
}
