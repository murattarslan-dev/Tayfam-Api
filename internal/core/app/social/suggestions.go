package social

// Friend suggestions and the profiling settings (issue #208, ADR-0030).
//
// The daily friend-suggestions job (package jobs) keeps the friendship
// scores and writes the suggestions; this file is what the user does with
// them: list, dismiss, and the two settings. The score is never read here:
// no method returns it, the number of shared events or any reason.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Suggestion rules (ADR-0030).
const (
	// SuggestionListWindow: an open suggestion is listed this long.
	SuggestionListWindow = 30 * 24 * time.Hour
	// MaxSuggestionsListed bounds the list (at most one a week comes in).
	MaxSuggestionsListed = 10
)

// Suggestion is someone suggested to the caller. It has no score.
type Suggestion struct {
	UserID         id.ID
	DisplayName    string
	AvatarUploadID id.ID // zero: no avatar
	SuggestedAt    time.Time
}

// Settings are the caller's profiling settings.
type Settings struct {
	// ProfilingEnabled ("Profilleme"): scores are kept and suggestions made.
	ProfilingEnabled bool
	// HideFromSuggestions ("Beni önerme"): never suggested to others.
	HideFromSuggestions bool
	// DiscoverVisibleToFriends ("Keşfet'te arkadaşlarım görebilsin"):
	// friends may see the user's participation in Keşfet (ADR-0033 §4).
	DiscoverVisibleToFriends bool
	// NowFromFriends ("Şimdi bildirimi kimden", ADR-0038 §4): NowFromAll,
	// NowFromClose or NowFromOff.
	NowFromFriends string
}

// NowFromFriends values (social_settings.now_from_friends).
const (
	NowFromAll   = "all"
	NowFromClose = "close"
	NowFromOff   = "off"
)

// DefaultSettings apply while the user never changed them.
var DefaultSettings = Settings{ProfilingEnabled: true, DiscoverVisibleToFriends: true, NowFromFriends: NowFromAll}

// SettingsUpdate holds the settings to change; nil and empty fields stay.
type SettingsUpdate struct {
	ProfilingEnabled         *bool
	HideFromSuggestions      *bool
	DiscoverVisibleToFriends *bool
	NowFromFriends           string
}

// ListFriendSuggestions lists the caller's open suggestions of the last
// SuggestionListWindow, newest first. Every condition (friendship, block,
// report, the other's settings, a deleted or banned account) is checked
// again on every read.
func (s *Service) ListFriendSuggestions(ctx context.Context, caller id.ID) ([]Suggestion, error) {
	rows, err := s.q.ListOpenFriendSuggestions(ctx, store.ListOpenFriendSuggestionsParams{
		UserID: caller.PG(), Since: ts(s.now().Add(-SuggestionListWindow)), N: MaxSuggestionsListed,
	})
	if err != nil {
		return nil, fmt.Errorf("öneriler: %w", err)
	}
	out := make([]Suggestion, 0, len(rows))
	for _, r := range rows {
		sg := Suggestion{UserID: id.ID(r.SuggestedUserID.Bytes), DisplayName: r.DisplayName.String, SuggestedAt: r.CreatedAt.Time.UTC()}
		if r.AvatarUploadID.Valid {
			sg.AvatarUploadID = id.ID(r.AvatarUploadID.Bytes)
		}
		out = append(out, sg)
	}
	return out, nil
}

// DismissFriendSuggestion closes the caller's suggestion of other: the two
// are never suggested to each other again. Dismissing nothing is not an
// error and tells nothing.
func (s *Service) DismissFriendSuggestion(ctx context.Context, caller, other id.ID) error {
	if other == caller || other == (id.ID{}) {
		return apperr.Validation("user_id")
	}
	if _, err := s.q.DismissFriendSuggestion(ctx, store.DismissFriendSuggestionParams{
		UserID: caller.PG(), SuggestedUserID: other.PG(), Now: ts(s.now()),
	}); err != nil {
		return fmt.Errorf("öneri kapatma: %w", err)
	}
	return nil
}

// GetSocialSettings returns the caller's settings, or the defaults.
func (s *Service) GetSocialSettings(ctx context.Context, caller id.ID) (Settings, error) {
	r, err := s.q.GetSocialSettings(ctx, caller.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultSettings, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("sosyal ayarlar: %w", err)
	}
	return Settings{ProfilingEnabled: r.ProfilingEnabled, HideFromSuggestions: r.HideFromSuggestions,
		DiscoverVisibleToFriends: r.DiscoverVisibleToFriends, NowFromFriends: r.NowFromFriends}, nil
}

// UpdateSocialSettings changes the given settings in one transaction.
// Turning profiling off deletes the caller's scores and withdraws the open
// suggestions to and of the caller; turning hide_from_suggestions on
// withdraws the open suggestions of the caller. Withdrawn rows stay, so
// switching a setting off and on resets neither the weekly limit nor the
// memory of who was already suggested (ADR-0030 §5). The transaction waits
// for a running score computation (shared lock), so no score written by it
// survives turning profiling off.
func (s *Service) UpdateSocialSettings(ctx context.Context, caller id.ID, u SettingsUpdate) (Settings, error) {
	if u.ProfilingEnabled == nil && u.HideFromSuggestions == nil && u.DiscoverVisibleToFriends == nil && u.NowFromFriends == "" {
		return Settings{}, apperr.Validation("settings")
	}
	nowFrom := pgtype.Text{}
	switch u.NowFromFriends {
	case "":
	case NowFromAll, NowFromClose, NowFromOff:
		nowFrom = pgtype.Text{String: u.NowFromFriends, Valid: true}
	default:
		return Settings{}, apperr.Validation("now_from_friends")
	}
	var out Settings
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.LockFriendScoresShared(ctx); err != nil {
			return fmt.Errorf("puan kilidi: %w", err)
		}
		now := ts(s.now())
		r, err := q.UpsertSocialSettings(ctx, store.UpsertSocialSettingsParams{
			UserID: caller.PG(), ProfilingEnabled: optBool(u.ProfilingEnabled),
			HideFromSuggestions: optBool(u.HideFromSuggestions), DiscoverVisibleToFriends: optBool(u.DiscoverVisibleToFriends),
			NowFromFriends: nowFrom, Now: now,
		})
		if err != nil {
			return fmt.Errorf("sosyal ayarlar: %w", err)
		}
		out = Settings{ProfilingEnabled: r.ProfilingEnabled, HideFromSuggestions: r.HideFromSuggestions,
			DiscoverVisibleToFriends: r.DiscoverVisibleToFriends, NowFromFriends: r.NowFromFriends}
		if !out.ProfilingEnabled {
			if err := q.DeleteUserFriendScores(ctx, caller.PG()); err != nil {
				return fmt.Errorf("puan silme: %w", err)
			}
			if err := q.WithdrawSuggestionsInvolving(ctx, store.WithdrawSuggestionsInvolvingParams{UserID: caller.PG(), Now: now}); err != nil {
				return fmt.Errorf("öneri geri çekme: %w", err)
			}
		}
		if out.HideFromSuggestions {
			if err := q.WithdrawSuggestionsOf(ctx, store.WithdrawSuggestionsOfParams{UserID: caller.PG(), Now: now}); err != nil {
				return fmt.Errorf("öneri geri çekme: %w", err)
			}
		}
		return nil
	})
	return out, err
}

// Sources of a permanent exclusion written in the actor's name
// (friend_suggestions.source; ADR-0030 §6).
const (
	excludeBlocked    = "blocked"
	excludeDeclined   = "declined"
	excludeUnfriended = "unfriended"
)

// excludePair runs in the transaction of a block, a declined friend request
// or an ended friendship: the pair's score goes, the other's open
// suggestion of actor is withdrawn, and actor's suggestion of other (or a
// new row with source) is dismissed for good, so the two are never
// suggested to each other again (ADR-0030 §6). It waits for a running
// score computation, like a settings change.
func (s *Service) excludePair(ctx context.Context, q *store.Queries, actor, other id.ID, source string) error {
	if err := q.LockFriendScoresShared(ctx); err != nil {
		return fmt.Errorf("puan kilidi: %w", err)
	}
	now := ts(s.now())
	low, high := Pair(actor, other)
	if err := q.DeleteFriendScore(ctx, store.DeleteFriendScoreParams{UserLowID: low.PG(), UserHighID: high.PG()}); err != nil {
		return fmt.Errorf("puan silme: %w", err)
	}
	if err := q.WithdrawSuggestionTo(ctx, store.WithdrawSuggestionToParams{UserID: other.PG(), SuggestedUserID: actor.PG(), Now: now}); err != nil {
		return fmt.Errorf("öneri geri çekme: %w", err)
	}
	if err := q.DismissSuggestionPair(ctx, store.DismissSuggestionPairParams{
		UserID: actor.PG(), SuggestedUserID: other.PG(), Now: now, Source: source,
	}); err != nil {
		return fmt.Errorf("öneri kapatma: %w", err)
	}
	return nil
}

func optBool(b *bool) pgtype.Bool {
	if b == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *b, Valid: true}
}
