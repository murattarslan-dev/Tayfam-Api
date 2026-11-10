package dm

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// MayWrite reports, with Send's rules but without writing, whether caller
// could send recipient a message now (keys.Contacts; issue #127): core
// does not refuse, and the conversation's state allows it (a waiting or
// declined request of the caller does not; a new request needs room in
// the caller's daily limit). The end-to-end pre-key bundles are guarded by
// it, so that strangers cannot use up a user's one-time pre-keys
// (ADR-0018 item 2). ctx must carry caller's token.
func (s *Service) MayWrite(ctx context.Context, caller, recipient id.ID) error {
	if recipient == (id.ID{}) || recipient == caller {
		return invalid("user_id")
	}
	v, err := s.cfg.Rules.Check(ctx, caller, recipient)
	if err != nil {
		return fmt.Errorf("birebir mesaj kuralı: %w", err)
	}
	switch v.Decision {
	case Active, Request:
	default:
		return apperr.ErrDirectMessageNotAllowed
	}
	q := store.New(s.cfg.Pool)
	low, high := pair(caller, recipient)
	c, err := q.GetConversationByPair(ctx, store.GetConversationByPairParams{UserLowID: low.PG(), UserHighID: high.PG()})
	switch {
	case err == nil:
		mine := uuidOf(c.RequestedBy) == caller
		switch {
		case c.Status == stateRequest && mine && v.Decision != Active, c.Status == stateDeclined && mine:
			return apperr.ErrMessageRequestPending
		}
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("konuşma: %w", err)
	}
	if v.Decision != Request {
		return nil
	}
	limit := v.RequestLimit
	if limit < 1 || limit > maxRequestLimit {
		limit = fallbackRequestLimit
	}
	n, err := q.CountRecentRequests(ctx, store.CountRecentRequestsParams{RequestedBy: caller.PG(), Since: ts(s.now().Add(-RequestWindow))})
	if err != nil {
		return fmt.Errorf("istek sayısı: %w", err)
	}
	if n >= int64(limit) {
		return apperr.ErrMessageRequestLimit
	}
	return nil
}

// HasConversation reports whether caller sees a conversation with other
// (keys.Contacts): any state but a request caller declined.
func (s *Service) HasConversation(ctx context.Context, caller, other id.ID) (bool, error) {
	if other == (id.ID{}) || other == caller {
		return false, nil
	}
	low, high := pair(caller, other)
	c, err := store.New(s.cfg.Pool).GetConversationByPair(ctx, store.GetConversationByPairParams{UserLowID: low.PG(), UserHighID: high.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("konuşma: %w", err)
	}
	_, ok := statusFor(c.Status, c.RequestedBy, caller)
	return ok, nil
}
