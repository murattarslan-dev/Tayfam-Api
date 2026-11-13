package channelaccess

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Event and selected-member channels (issue #183, ADR-0024). chat keeps
// the selected members; everything that depends on core's data (who goes
// to an event, who organizes it, who is an active member) is answered here
// with the caller's own token. chat keeps no copy of attendance.

// MaxBatch bounds the channels and users of one question.
const MaxBatch = 100

// rsvpGoing and eventCancelled are core's stored values (event_rsvps.status,
// events.status).
const (
	rsvpGoing      = "going"
	eventCancelled = "cancelled"
)

// EventChannel is one event channel chat asks about.
type EventChannel struct {
	Event  id.ID
	Opener id.ID // zero when the account was deleted
}

// EventAccess is the answer for the caller and one event channel.
type EventAccess struct {
	EventChannel
	// Member: the caller answered going, organizes the event or opened
	// the channel.
	Member bool
	// CanOpen: the caller may open a channel for the event.
	CanOpen bool
	// End is the event's end, or the moment it was cancelled.
	End       time.Time
	Cancelled bool
	// MemberCount is the audience: going members, the organizer and the
	// opener, active members each counted once.
	MemberCount int32
}

// EventChannels answers for caller and each channel. Events caller cannot
// see in tayfa (unknown, another tayfa's, events module off) are left out.
func (s *Service) EventChannels(ctx context.Context, caller, tayfa id.ID, chans []EventChannel) ([]EventAccess, error) {
	if len(chans) == 0 || len(chans) > MaxBatch {
		return nil, apperr.Validation("channels")
	}
	t, m, err := access.Load(ctx, s.q, tayfa, caller, false)
	if err != nil {
		return nil, err
	}
	if err := access.RequireModule(ctx, s.q, tayfa, access.ModuleEvents); errors.Is(err, apperr.ErrModuleDisabled) {
		return []EventAccess{}, nil
	} else if err != nil {
		return nil, err
	}
	events := make([]pgtype.UUID, 0, len(chans))
	openers := make([]pgtype.UUID, 0, len(chans))
	for _, c := range chans {
		if c.Event == (id.ID{}) {
			return nil, apperr.Validation("event_id")
		}
		events = append(events, c.Event.PG())
		if c.Opener != (id.ID{}) {
			openers = append(openers, c.Opener.PG())
		}
	}
	rows, err := s.q.ChannelEvents(ctx, store.ChannelEventsParams{CallerID: caller.PG(), TayfaID: tayfa.PG(), EventIds: events})
	if err != nil {
		return nil, fmt.Errorf("etkinlikler: %w", err)
	}
	counts, err := s.q.EventAudienceCounts(ctx, store.EventAudienceCountsParams{EventIds: events, TayfaID: tayfa.PG()})
	if err != nil {
		return nil, fmt.Errorf("kitle: %w", err)
	}
	going, err := s.q.GoingAmong(ctx, store.GoingAmongParams{EventIds: events, UserIds: openers})
	if err != nil {
		return nil, fmt.Errorf("kanalı açanlar: %w", err)
	}
	activeOpeners, err := s.q.FilterActiveMembers(ctx, store.FilterActiveMembersParams{TayfaID: tayfa.PG(), UserIds: openers})
	if err != nil {
		return nil, fmt.Errorf("kanalı açanlar: %w", err)
	}
	byEvent := make(map[id.ID]store.ChannelEventsRow, len(rows))
	for _, r := range rows {
		byEvent[id.ID(r.ID.Bytes)] = r
	}
	count := make(map[id.ID]int32, len(counts))
	for _, c := range counts {
		count[id.ID(c.EventID.Bytes)] = c.MemberCount
	}
	goingPair := make(map[[2]id.ID]bool, len(going))
	for _, g := range going {
		goingPair[[2]id.ID{id.ID(g.EventID.Bytes), id.ID(g.UserID.Bytes)}] = true
	}
	active := make(map[id.ID]bool, len(activeOpeners))
	for _, u := range activeOpeners {
		active[id.ID(u.Bytes)] = true
	}

	now := s.cfg.Now().UTC()
	staffOpen := decide(m, authz.ChannelManage, now, access.ReadOnly(t)) == DenialNone
	settings := access.Settings(t)
	out := make([]EventAccess, 0, len(chans))
	for _, c := range chans {
		r, ok := byEvent[c.Event]
		if !ok {
			continue
		}
		organizer := id.ID{}
		if r.CreatedBy.Valid {
			organizer = id.ID(r.CreatedBy.Bytes)
		}
		a := EventAccess{EventChannel: c, End: r.EndsAt.Time.UTC(), Cancelled: r.Status == eventCancelled, MemberCount: count[c.Event]}
		if a.Cancelled && r.CancelledAt.Valid {
			a.End = r.CancelledAt.Time.UTC()
		}
		// The opener counts once, if still an active member and not
		// already in as going or organizing.
		if c.Opener != (id.ID{}) && active[c.Opener] && c.Opener != organizer && !goingPair[[2]id.ID{c.Event, c.Opener}] {
			a.MemberCount++
		}
		a.Member = r.CallerStatus == rsvpGoing || caller == organizer || caller == c.Opener
		// Whoever may manage the event (caller_can_manage in the event
		// view) may open its channel, as moderators and admins may.
		a.CanOpen = staffOpen || (!access.ReadOnly(t) &&
			(access.AuthorizeIn(m, authz.EventEditAny, now, settings) == nil ||
				(caller == organizer && access.AuthorizeIn(m, authz.EventEditOwn, now, settings) == nil)))
		out = append(out, a)
	}
	return out, nil
}

// EventAudience lists an event channel's audience after the cursor, ordered
// by user ID. An event caller cannot see is apperr.ErrEventNotFound.
func (s *Service) EventAudience(ctx context.Context, caller, tayfa id.ID, ch EventChannel, size int32, token string) ([]id.ID, string, error) {
	if size < 0 {
		return nil, "", apperr.Validation("page_size")
	}
	if size == 0 {
		size = 50
	}
	size = min(size, MaxBatch)
	after, err := decodeAfter(token)
	if err != nil {
		return nil, "", err
	}
	got, err := s.EventChannels(ctx, caller, tayfa, []EventChannel{ch})
	if err != nil {
		return nil, "", err
	}
	if len(got) != 1 {
		return nil, "", apperr.ErrEventNotFound
	}
	rows, err := s.q.ListEventAudience(ctx, store.ListEventAudienceParams{
		TayfaID: tayfa.PG(), EventID: ch.Event.PG(), After: after.PG(), PageSize: size + 1,
	})
	if err != nil {
		return nil, "", fmt.Errorf("kitle: %w", err)
	}
	users := make([]id.ID, 0, len(rows)+1)
	for _, r := range rows {
		users = append(users, id.ID(r.Bytes))
	}
	// The opener, if an active member, joins the page where they belong.
	if o := ch.Opener; o != (id.ID{}) && idLess(after, o) && !slices.Contains(users, o) {
		act, err := s.q.FilterActiveMembers(ctx, store.FilterActiveMembersParams{TayfaID: tayfa.PG(), UserIds: []pgtype.UUID{o.PG()}})
		if err != nil {
			return nil, "", fmt.Errorf("kanalı açan: %w", err)
		}
		if len(act) == 1 {
			users = append(users, o)
			slices.SortFunc(users, compareID)
		}
	}
	next := ""
	if len(users) > int(size) {
		users = users[:size]
		next = base64.RawURLEncoding.EncodeToString(users[len(users)-1][:])
	}
	return users, next, nil
}

// ActiveMembers returns which of users are active members of tayfa, in the
// order given, without repeats. The caller must be an active member.
func (s *Service) ActiveMembers(ctx context.Context, caller, tayfa id.ID, users []id.ID) ([]id.ID, error) {
	if len(users) > MaxBatch {
		return nil, apperr.Validation("user_ids")
	}
	if _, _, err := access.Load(ctx, s.q, tayfa, caller, false); err != nil {
		return nil, err
	}
	ids := make([]pgtype.UUID, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.PG())
	}
	rows, err := s.q.FilterActiveMembers(ctx, store.FilterActiveMembersParams{TayfaID: tayfa.PG(), UserIds: ids})
	if err != nil {
		return nil, fmt.Errorf("üyeler: %w", err)
	}
	ok := make(map[id.ID]bool, len(rows))
	for _, r := range rows {
		ok[id.ID(r.Bytes)] = true
	}
	out := make([]id.ID, 0, len(rows))
	for _, u := range users {
		if ok[u] && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out, nil
}

func decodeAfter(token string) (id.ID, error) {
	if token == "" {
		return id.ID{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != len(id.ID{}) {
		return id.ID{}, apperr.Validation("page_token")
	}
	return id.ID(b), nil
}

func compareID(a, b id.ID) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func idLess(a, b id.ID) bool { return compareID(a, b) < 0 }
