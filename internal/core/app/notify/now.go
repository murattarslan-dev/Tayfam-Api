package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/localday"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Şimdi bildirimi (issue #265; ADR-0038 §4). A now event's notification
// is TypeNowStarted about the event, with its organizer as actor. Its
// limits are counted per Europe/Istanbul day in now_notification_counters,
// in the transaction that creates the event; the counters hold numbers
// only, never who was told.
const (
	// NowPerTayfaDay: a tayfa's now events notify its members at most this
	// often a day; later ones open without a notification.
	NowPerTayfaDay = 3
	// NowBroadcastsPerDay: a user sends at most this many now broadcasts to
	// friends a day.
	NowBroadcastsPerDay = 2
	// MaxNowRecipients bounds the friends of one broadcast.
	MaxNowRecipients = 30
	// NowCounterRetention: the counters are deleted after two days.
	NowCounterRetention = 2 * 24 * time.Hour
)

// Counter scopes (now_notification_counters.scope).
const (
	nowScopeTayfa = "tayfa"
	nowScopeUser  = "user"
)

// NowMembers returns who hears about a now event of the tayfa organized
// by actor: the active members but the actor and anyone blocked with the
// actor either way. When the tayfa already had NowPerTayfaDay notifying now
// events on now's day it returns none: the event opens without a
// notification. No recipients, nothing counted.
func NowMembers(ctx context.Context, q *store.Queries, tayfa, actor id.ID, now time.Time) ([]id.ID, error) {
	to, err := ids(q.ListNotifyNowMembers(ctx, store.ListNotifyNowMembersParams{TayfaID: tayfa.PG(), ActorID: actor.PG()}))
	if err != nil || len(to) == 0 {
		return nil, err
	}
	ok, err := takeNow(ctx, q, nowScopeTayfa, tayfa, NowPerTayfaDay, now)
	if err != nil || !ok {
		return nil, err
	}
	return to, nil
}

// TakeNowBroadcast counts a now broadcast of sender on now's day; false:
// the sender already sent NowBroadcastsPerDay.
func TakeNowBroadcast(ctx context.Context, q *store.Queries, sender id.ID, now time.Time) (bool, error) {
	return takeNow(ctx, q, nowScopeUser, sender, NowBroadcastsPerDay, now)
}

// NowFriends returns those of users who may hear about sender's now event:
// accepted friends, no block either way, live accounts, and whose
// now_from_friends setting allows sender. The others are left out without
// a trace.
func NowFriends(ctx context.Context, q *store.Queries, sender id.ID, users []id.ID) ([]id.ID, error) {
	if len(users) == 0 {
		return nil, nil
	}
	in := make([]pgtype.UUID, 0, len(users))
	for _, u := range users {
		in = append(in, u.PG())
	}
	return ids(q.FilterNowFriendRecipients(ctx, store.FilterNowFriendRecipientsParams{SenderID: sender.PG(), UserIds: in}))
}

func takeNow(ctx context.Context, q *store.Queries, scope string, subject id.ID, limit int32, now time.Time) (bool, error) {
	_, err := q.TakeNowCounter(ctx, store.TakeNowCounterParams{
		Scope: scope, SubjectID: subject.PG(), Day: pgtype.Date{Time: localday.Today(now), Valid: true}, MaxSent: limit,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("şimdi sayacı: %w", err)
	}
	return true, nil
}
