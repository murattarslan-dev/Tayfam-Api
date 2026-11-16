package social

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Notification resolutions (issue #192): how a friend request or a tayfa
// invitation the caller received stands now. chat asks for one page of
// notifications at a time; the answer comes from the current request or
// membership row and, once that is gone, from the recorded outcome. Only
// the recipient's own requests are read; who withdrew is never said.

// MaxResolutionRefs bounds one question.
const MaxResolutionRefs = 100

// RequestKind is what a ref names.
type RequestKind int

// Request kinds.
const (
	KindFriendRequest   RequestKind = iota + 1 // ref: the requester
	KindTayfaInvitation                        // ref: the tayfa
	// KindFriendSuggestion: ref: the suggested user (issue #208, ADR-0030).
	KindFriendSuggestion
)

// Resolution is how a request stands.
type Resolution string

// Resolutions.
const (
	ResolutionPending   Resolution = "pending"
	ResolutionAccepted  Resolution = "accepted"
	ResolutionDeclined  Resolution = "declined"
	ResolutionWithdrawn Resolution = "withdrawn"
	ResolutionExpired   Resolution = "expired"
)

// RequestRef is one request the caller received.
type RequestRef struct {
	Kind RequestKind
	ID   id.ID
}

// Resolutions answers for each ref, in order, with two queries in all.
func (s *Service) Resolutions(ctx context.Context, caller id.ID, refs []RequestRef) ([]Resolution, error) {
	if len(refs) == 0 || len(refs) > MaxResolutionRefs {
		return nil, apperr.Validation("refs")
	}
	var requesters, tayfas, suggested []pgtype.UUID
	for _, r := range refs {
		switch {
		case r.ID == (id.ID{}):
			return nil, apperr.Validation("refs.id")
		case r.Kind == KindFriendRequest:
			requesters = append(requesters, r.ID.PG())
		case r.Kind == KindTayfaInvitation:
			tayfas = append(tayfas, r.ID.PG())
		case r.Kind == KindFriendSuggestion:
			suggested = append(suggested, r.ID.PG())
		default:
			return nil, apperr.Validation("refs.kind")
		}
	}
	friend := map[id.ID]Resolution{}
	if len(requesters) > 0 {
		rows, err := s.q.FriendRequestStates(ctx, store.FriendRequestStatesParams{RequesterIds: requesters, RecipientID: caller.PG()})
		if err != nil {
			return nil, fmt.Errorf("istek durumu: %w", err)
		}
		for _, r := range rows {
			a := id.ID(r.RequesterID.Bytes)
			switch {
			case r.FriendshipStatus.String == "pending" && id.ID(r.RequestedBy.Bytes) == a:
				friend[a] = ResolutionPending
			case r.FriendshipStatus.String == "accepted":
				friend[a] = ResolutionAccepted
			case r.Outcome.Valid:
				friend[a] = Resolution(r.Outcome.String)
			default:
				friend[a] = ResolutionExpired
			}
		}
	}
	invite := map[id.ID]Resolution{}
	if len(tayfas) > 0 {
		rows, err := s.q.InvitationStates(ctx, store.InvitationStatesParams{TayfaIds: tayfas, UserID: caller.PG()})
		if err != nil {
			return nil, fmt.Errorf("davet durumu: %w", err)
		}
		for _, r := range rows {
			t := id.ID(r.TayfaID.Bytes)
			switch st := r.MembershipStatus.String; {
			case st == "invited":
				invite[t] = ResolutionPending
			case r.Outcome.Valid:
				invite[t] = Resolution(r.Outcome.String)
			case st == "active" || st == "pending":
				// Joined, or asked to join, some other way.
				invite[t] = ResolutionAccepted
			default:
				invite[t] = ResolutionExpired
			}
		}
	}
	suggestion, err := s.suggestionResolutions(ctx, caller, suggested)
	if err != nil {
		return nil, err
	}
	out := make([]Resolution, 0, len(refs))
	for _, r := range refs {
		m := friend
		switch r.Kind {
		case KindTayfaInvitation:
			m = invite
		case KindFriendSuggestion:
			m = suggestion
		}
		v, ok := m[r.ID]
		if !ok {
			v = ResolutionExpired
		}
		out = append(out, v)
	}
	return out, nil
}

// suggestionResolutions answers for friend_suggestion notifications: a
// suggestion still shown is pending; one that became a friendship or a
// request is accepted; one the caller dismissed is declined; anything else
// (withdrawn, excluded by a block, report or setting, the other account
// gone) is expired, so the client drops the notification without learning
// why (ADR-0030 §8).
func (s *Service) suggestionResolutions(ctx context.Context, caller id.ID, suggested []pgtype.UUID) (map[id.ID]Resolution, error) {
	out := map[id.ID]Resolution{}
	if len(suggested) == 0 {
		return out, nil
	}
	rows, err := s.q.FriendSuggestionStates(ctx, store.FriendSuggestionStatesParams{UserID: caller.PG(), SuggestedIds: suggested})
	if err != nil {
		return nil, fmt.Errorf("öneri durumu: %w", err)
	}
	since := s.now().Add(-SuggestionListWindow)
	for _, r := range rows {
		u := id.ID(r.SuggestedID.Bytes)
		switch {
		case r.HasFriendship:
			out[u] = ResolutionAccepted
		case r.Source.Valid && r.Source.String == "suggested" && r.DismissedAt.Valid:
			out[u] = ResolutionDeclined
		case r.Allowed && !r.WithdrawnAt.Valid && !r.DismissedAt.Valid && r.CreatedAt.Valid && r.CreatedAt.Time.After(since):
			out[u] = ResolutionPending
		default:
			out[u] = ResolutionExpired
		}
	}
	return out, nil
}
