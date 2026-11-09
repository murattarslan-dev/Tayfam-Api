// Package social holds friendships and blocks (data model v14 "Arkadaşlık
// ve Davet (v11)", "Mesaj İsteği Modeli (v7)"; ADR-0009: the social graph
// is core's, so direct-message rules need a single call; issue #71).
//
// Rules:
//
//   - A friend request is accepted by its recipient. A declined request is
//     deleted and its sender is not told. Sending a request to someone whose
//     request is already waiting for the caller accepts it.
//   - At most FriendRequestsPerDay requests per sender in 24 hours, counted
//     from the database (domain_events), so declined requests still count
//     and a restart resets nothing (ADR-0004).
//   - A block deletes the friendship and any waiting request between the
//     two in the same transaction. While a block exists in either direction
//     neither can send the other a request, and the other user looks like an
//     unknown one (apperr.ErrUserNotFound): nobody learns that they were
//     blocked.
//   - Friend lists, requests and blocks are only ever the caller's own.
//
// IsBlocked and Friends are the two-way checks other core packages use
// (direct invitations, the profile view). chat's DM rules will reach them
// through a core service method added with the DM work.
//
// Events carry IDs only: friend.requested (recipient in user_id; the daily
// limit counts them) and friend.accepted (the original requester in
// user_id), each followed by a notification.requested for its one
// recipient (friend_request, friend_accepted; not about a tayfa).
package social

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// FriendRequestsPerDay is the daily friend request limit per sender (v14).
const FriendRequestsPerDay = 20

// LimitWindow is the window of the daily limits.
const LimitWindow = 24 * time.Hour

// Outbox events. Payloads hold IDs only.
const (
	EventFriendRequested = "friend.requested"
	EventFriendAccepted  = "friend.accepted"
)

// Paging: page_size 0 means 50, above 100 means 100.
const (
	defaultPageSize = 50
	maxPageSize     = 100
)

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Now  func() time.Time // optional, for tests
}

// Service implements friendships and blocks.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	now  func() time.Time
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("social: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{pool: cfg.Pool, q: store.New(cfg.Pool), now: func() time.Time { return cfg.Now().UTC() }}, nil
}

// State is a friendship as the caller sees it.
type State string

// States.
const (
	// StateFriends: accepted.
	StateFriends State = "friends"
	// StateIncoming: the other user's request waits for the caller.
	StateIncoming State = "incoming"
	// StateOutgoing: the caller's request waits for the other user.
	StateOutgoing State = "outgoing"
)

// Friend is one row of the caller's friend or request list.
type Friend struct {
	UserID         id.ID
	DisplayName    string
	AvatarUploadID id.ID // zero: no avatar
	State          State
	RequestedAt    time.Time
	AcceptedAt     time.Time // zero unless friends
}

// Blocked is one row of the caller's block list.
type Blocked struct {
	UserID id.ID
	// DisplayName is empty when the user deleted their account; the block
	// still shows so it can be removed.
	DisplayName    string
	AvatarUploadID id.ID
	BlockedAt      time.Time
}

// Page is one page of a list.
type Page[T any] struct {
	Items []T
	// Next is the token of the next page; empty on the last page.
	Next string
}

// ---------------------------------------------------------------------------
// Two-way checks for other core packages

// Pair returns a and b in friendships order (user_low_id < user_high_id,
// the byte order Postgres compares uuids in).
func Pair(a, b id.ID) (low, high id.ID) {
	if bytes.Compare(a[:], b[:]) < 0 {
		return a, b
	}
	return b, a
}

// IsBlocked reports whether a blocked b or b blocked a. It is the one check
// every rule about two users goes through (requests, invitations, profile;
// later chat's DM rules).
func IsBlocked(ctx context.Context, q *store.Queries, a, b id.ID) (bool, error) {
	blocked, err := q.IsBlockedEitherWay(ctx, store.IsBlockedEitherWayParams{A: a.PG(), B: b.PG()})
	if err != nil {
		return false, fmt.Errorf("engel: %w", err)
	}
	return blocked, nil
}

// Friends reports whether a and b are friends (accepted).
func Friends(ctx context.Context, q *store.Queries, a, b id.ID) (bool, error) {
	st, err := relation(ctx, q, a, b)
	return st == StateFriends, err
}

// Relation returns the friendship between viewer and other as viewer sees
// it, or "" when there is none.
func Relation(ctx context.Context, q *store.Queries, viewer, other id.ID) (State, error) {
	return relation(ctx, q, viewer, other)
}

func relation(ctx context.Context, q *store.Queries, viewer, other id.ID) (State, error) {
	if viewer == other {
		return "", nil
	}
	low, high := Pair(viewer, other)
	f, err := q.GetFriendship(ctx, store.GetFriendshipParams{UserLowID: low.PG(), UserHighID: high.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("arkadaşlık: %w", err)
	}
	return stateOf(f.Status, id.ID(f.RequestedBy.Bytes), viewer), nil
}

func stateOf(status string, requestedBy, viewer id.ID) State {
	switch {
	case status == "accepted":
		return StateFriends
	case requestedBy == viewer:
		return StateOutgoing
	default:
		return StateIncoming
	}
}

// ---------------------------------------------------------------------------
// Friendships

// SendFriendRequest asks target to be the caller's friend. If target's
// request to the caller is already waiting, it is accepted instead.
func (s *Service) SendFriendRequest(ctx context.Context, caller, target id.ID) (Friend, error) {
	if target == caller || target == (id.ID{}) {
		return Friend{}, apperr.Validation("user_id")
	}
	var out Friend
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		p, err := s.reachable(ctx, q, caller, target)
		if err != nil {
			return err
		}
		low, high := Pair(caller, target)
		if err := q.LockFriendPair(ctx, store.LockFriendPairParams{UserLowID: low.PG(), UserHighID: high.PG()}); err != nil {
			return fmt.Errorf("çift kilidi: %w", err)
		}
		f, err := q.GetFriendship(ctx, store.GetFriendshipParams{UserLowID: low.PG(), UserHighID: high.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("arkadaşlık: %w", err)
		default:
			switch stateOf(f.Status, id.ID(f.RequestedBy.Bytes), caller) {
			case StateFriends:
				return apperr.ErrAlreadyFriends
			case StateOutgoing:
				return apperr.ErrFriendRequestPending
			case StateIncoming:
				out, err = s.accept(ctx, q, caller, target, p)
				return err
			}
		}

		if err := q.LockUserLimit(ctx, caller.PG()); err != nil {
			return fmt.Errorf("sınır kilidi: %w", err)
		}
		n, err := q.CountRecentEventsByActor(ctx, store.CountRecentEventsByActorParams{
			ActorID: caller.PG(), Type: EventFriendRequested, Since: ts(s.now().Add(-LimitWindow)),
		})
		if err != nil {
			return fmt.Errorf("istek sayısı: %w", err)
		}
		if n >= FriendRequestsPerDay {
			return apperr.ErrFriendRequestLimit
		}
		created, err := q.CreateFriendRequest(ctx, store.CreateFriendRequestParams{UserLowID: low.PG(), UserHighID: high.PG(), RequestedBy: caller.PG()})
		if err != nil {
			return fmt.Errorf("istek: %w", err)
		}
		if created != 1 {
			// The pair lock makes this unreachable; never report success
			// for a request that was not written.
			return apperr.ErrFriendRequestPending
		}
		out = friendOf(p, StateOutgoing, s.now(), time.Time{})
		if err := txn.Event(ctx, q, EventFriendRequested, id.ID{}, caller, txn.Target{Type: "user", ID: target},
			map[string]any{"user_id": target.String()}); err != nil {
			return err
		}
		// The recipient opens the requester's profile.
		return notify.Write(ctx, q, notify.Request{
			Type: notify.TypeFriendRequest, Actor: caller, TargetType: notify.TargetUser, Target: caller,
		}, []id.ID{target})
	})
	return out, err
}

// AcceptFriendRequest accepts requester's waiting request to the caller.
func (s *Service) AcceptFriendRequest(ctx context.Context, caller, requester id.ID) (Friend, error) {
	if requester == caller || requester == (id.ID{}) {
		return Friend{}, apperr.Validation("user_id")
	}
	var out Friend
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		p, err := s.reachable(ctx, q, caller, requester)
		if errors.Is(err, apperr.ErrUserNotFound) {
			return apperr.ErrFriendRequestNotFound
		}
		if err != nil {
			return err
		}
		low, high := Pair(caller, requester)
		if err := q.LockFriendPair(ctx, store.LockFriendPairParams{UserLowID: low.PG(), UserHighID: high.PG()}); err != nil {
			return fmt.Errorf("çift kilidi: %w", err)
		}
		out, err = s.accept(ctx, q, caller, requester, p)
		return err
	})
	return out, err
}

func (s *Service) accept(ctx context.Context, q *store.Queries, caller, requester id.ID, p store.GetLiveUserProfileRow) (Friend, error) {
	low, high := Pair(caller, requester)
	n, err := q.AcceptFriendRequest(ctx, store.AcceptFriendRequestParams{UserLowID: low.PG(), UserHighID: high.PG(), Accepter: caller.PG()})
	if err != nil {
		return Friend{}, fmt.Errorf("kabul: %w", err)
	}
	if n != 1 {
		return Friend{}, apperr.ErrFriendRequestNotFound
	}
	f, err := q.GetFriendship(ctx, store.GetFriendshipParams{UserLowID: low.PG(), UserHighID: high.PG()})
	if err != nil {
		return Friend{}, fmt.Errorf("arkadaşlık: %w", err)
	}
	out := friendOf(p, StateFriends, f.CreatedAt.Time, f.AcceptedAt.Time)
	if err := txn.Event(ctx, q, EventFriendAccepted, id.ID{}, caller, txn.Target{Type: "user", ID: requester},
		map[string]any{"user_id": requester.String()}); err != nil {
		return Friend{}, err
	}
	// The requester opens the new friend's profile.
	return out, notify.Write(ctx, q, notify.Request{
		Type: notify.TypeFriendAccepted, Actor: caller, TargetType: notify.TargetUser, Target: caller,
	}, []id.ID{requester})
}

// RemoveFriendship ends a friendship, withdraws the caller's request or
// declines other's request: whatever there is between the two is deleted.
// Nobody is told; a declined sender keeps seeing nothing. Removing nothing
// is not an error.
func (s *Service) RemoveFriendship(ctx context.Context, caller, other id.ID) error {
	if other == caller || other == (id.ID{}) {
		return apperr.Validation("user_id")
	}
	low, high := Pair(caller, other)
	if _, err := s.q.DeleteFriendship(ctx, store.DeleteFriendshipParams{UserLowID: low.PG(), UserHighID: high.PG()}); err != nil {
		return fmt.Errorf("arkadaşlık silme: %w", err)
	}
	return nil
}

// ListFriends lists the caller's friends.
func (s *Service) ListFriends(ctx context.Context, caller id.ID, size int32, token string) (Page[Friend], error) {
	return s.listFriendships(ctx, caller, "accepted", "", "ListFriends", size, token)
}

// ListFriendRequests lists requests waiting for the caller (incoming) or
// sent by the caller (outgoing).
func (s *Service) ListFriendRequests(ctx context.Context, caller id.ID, incoming bool, size int32, token string) (Page[Friend], error) {
	dir := "outgoing"
	if incoming {
		dir = "incoming"
	}
	return s.listFriendships(ctx, caller, "pending", dir, "ListFriendRequests:"+dir, size, token)
}

func (s *Service) listFriendships(ctx context.Context, caller id.ID, status, dir, method string, size int32, token string) (Page[Friend], error) {
	size, err := pageSize(size)
	if err != nil {
		return Page[Friend]{}, err
	}
	after, err := decodeCursor(token, method)
	if err != nil {
		return Page[Friend]{}, err
	}
	rows, err := s.q.ListFriendshipsPage(ctx, store.ListFriendshipsPageParams{
		UserID: caller.PG(), Status: status, Direction: dir, AfterUserID: after.PG(), PageSize: size + 1,
	})
	if err != nil {
		return Page[Friend]{}, fmt.Errorf("arkadaşlar: %w", err)
	}
	var out Page[Friend]
	for i, r := range rows {
		if i == int(size) {
			out.Next = encodeCursor(method, id.ID(rows[i-1].OtherID.Bytes))
			break
		}
		f := Friend{
			UserID: id.ID(r.OtherID.Bytes), DisplayName: r.DisplayName.String,
			State:       stateOf(r.Status, id.ID(r.RequestedBy.Bytes), caller),
			RequestedAt: r.CreatedAt.Time.UTC(),
		}
		if r.AvatarUploadID.Valid {
			f.AvatarUploadID = id.ID(r.AvatarUploadID.Bytes)
		}
		if r.AcceptedAt.Valid {
			f.AcceptedAt = r.AcceptedAt.Time.UTC()
		}
		out.Items = append(out.Items, f)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Blocks

// BlockUser blocks target: the friendship or waiting request between the two
// goes in the same transaction. Blocking again changes nothing.
func (s *Service) BlockUser(ctx context.Context, caller, target id.ID) error {
	if target == caller || target == (id.ID{}) {
		return apperr.Validation("user_id")
	}
	return txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		// Only a live user can be blocked; whether target blocked the caller
		// does not matter (and is not told).
		if _, err := q.GetLiveUserProfile(ctx, target.PG()); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.ErrUserNotFound
			}
			return fmt.Errorf("profil: %w", err)
		}
		low, high := Pair(caller, target)
		if err := q.LockFriendPair(ctx, store.LockFriendPairParams{UserLowID: low.PG(), UserHighID: high.PG()}); err != nil {
			return fmt.Errorf("çift kilidi: %w", err)
		}
		if _, err := q.DeleteFriendship(ctx, store.DeleteFriendshipParams{UserLowID: low.PG(), UserHighID: high.PG()}); err != nil {
			return fmt.Errorf("arkadaşlık silme: %w", err)
		}
		if _, err := q.CreateBlock(ctx, store.CreateBlockParams{BlockerID: caller.PG(), BlockedID: target.PG()}); err != nil {
			return fmt.Errorf("engel: %w", err)
		}
		return nil
	})
}

// UnblockUser removes the caller's block of target. The friendship does not
// come back. Removing a block that is not there is not an error.
func (s *Service) UnblockUser(ctx context.Context, caller, target id.ID) error {
	if target == caller || target == (id.ID{}) {
		return apperr.Validation("user_id")
	}
	if _, err := s.q.DeleteBlock(ctx, store.DeleteBlockParams{BlockerID: caller.PG(), BlockedID: target.PG()}); err != nil {
		return fmt.Errorf("engel kaldırma: %w", err)
	}
	return nil
}

// ListBlockedUsers lists the users the caller blocked. Who blocked the
// caller is never listed.
func (s *Service) ListBlockedUsers(ctx context.Context, caller id.ID, size int32, token string) (Page[Blocked], error) {
	const method = "ListBlockedUsers"
	size, err := pageSize(size)
	if err != nil {
		return Page[Blocked]{}, err
	}
	after, err := decodeCursor(token, method)
	if err != nil {
		return Page[Blocked]{}, err
	}
	rows, err := s.q.ListBlocksPage(ctx, store.ListBlocksPageParams{BlockerID: caller.PG(), AfterUserID: after.PG(), PageSize: size + 1})
	if err != nil {
		return Page[Blocked]{}, fmt.Errorf("engeller: %w", err)
	}
	var out Page[Blocked]
	for i, r := range rows {
		if i == int(size) {
			out.Next = encodeCursor(method, id.ID(rows[i-1].BlockedID.Bytes))
			break
		}
		b := Blocked{UserID: id.ID(r.BlockedID.Bytes), DisplayName: r.DisplayName.String, BlockedAt: r.CreatedAt.Time.UTC()}
		if r.AvatarUploadID.Valid {
			b.AvatarUploadID = id.ID(r.AvatarUploadID.Bytes)
		}
		out.Items = append(out.Items, b)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Helpers

// reachable returns target's live profile when there is no block between
// the two; otherwise target looks unknown.
func (s *Service) reachable(ctx context.Context, q *store.Queries, caller, target id.ID) (store.GetLiveUserProfileRow, error) {
	p, err := q.GetLiveUserProfile(ctx, target.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GetLiveUserProfileRow{}, apperr.ErrUserNotFound
	}
	if err != nil {
		return store.GetLiveUserProfileRow{}, fmt.Errorf("profil: %w", err)
	}
	blocked, err := IsBlocked(ctx, q, caller, target)
	if err != nil {
		return store.GetLiveUserProfileRow{}, err
	}
	if blocked {
		return store.GetLiveUserProfileRow{}, apperr.ErrUserNotFound
	}
	return p, nil
}

func friendOf(p store.GetLiveUserProfileRow, st State, requested, accepted time.Time) Friend {
	f := Friend{UserID: id.ID(p.UserID.Bytes), DisplayName: p.DisplayName.String, State: st,
		RequestedAt: requested.UTC()}
	if !accepted.IsZero() {
		f.AcceptedAt = accepted.UTC()
	}
	if p.AvatarUploadID.Valid {
		f.AvatarUploadID = id.ID(p.AvatarUploadID.Bytes)
	}
	return f
}

func pageSize(n int32) (int32, error) {
	switch {
	case n < 0:
		return 0, apperr.Validation("page_size")
	case n == 0:
		return defaultPageSize, nil
	case n > maxPageSize:
		return maxPageSize, nil
	}
	return n, nil
}

// cursor is the position after the last row of a page: the other user's
// ID. It names its list, so a token only works for the list it came from;
// it carries nothing the caller could not already see.
type cursor struct {
	Method string `json:"m"`
	User   string `json:"u"`
}

func encodeCursor(method string, after id.ID) string {
	b, _ := json.Marshal(cursor{Method: method, User: after.String()}) // plain struct; cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor returns the user to list after; the zero ID for the first
// page.
func decodeCursor(token, method string) (id.ID, error) {
	if token == "" {
		return id.ID{}, nil
	}
	bad := apperr.Validation("page_token")
	if len(token) > 512 {
		return id.ID{}, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil || c.Method != method {
		return id.ID{}, bad
	}
	u, ok := id.Parse(c.User)
	if !ok {
		return id.ID{}, bad
	}
	return u, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
