package channel

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Event and selected-member channels (issue #183, ADR-0024). Their
// audience is narrower than the tayfa and staff are not in it by role:
//
//   - VisibilityEvent: the opener, the event's organizer and members going
//     to the event. Who goes is core's data, asked with the caller's own
//     token every time (Audience); chat keeps no copy of attendance.
//   - VisibilitySelected: the opener and the members they chose, kept in
//     channel_members (the opener included).
//
// Someone outside the audience gets apperr.ErrChannelNotFound, as for a
// staff channel. Entering later shows the earlier messages; leaving or
// being removed shows none. An event channel is archived ArchiveAfter the
// event's end or cancellation and is read-only from then on.

// Visibilities of narrower audiences (channels.visibility).
const (
	VisibilityEvent    = "event"
	VisibilitySelected = "selected"
)

// ArchiveAfter is how long after its event's end (or cancellation) an event
// channel is archived.
const ArchiveAfter = 7 * 24 * time.Hour

// MaxMembersBatch bounds the users of one request and of one question to
// core.
const MaxMembersBatch = 100

// EventRef is an event channel as core is asked about it.
type EventRef struct {
	Event  id.ID
	Opener id.ID // zero when the account was deleted
}

// EventAccess is core's answer for the caller and one event channel.
type EventAccess struct {
	Member      bool
	CanOpen     bool
	End         time.Time
	Cancelled   bool
	MemberCount int32
}

// Audience asks core about event channels and tayfa members
// (ChannelAccessService, issue #183). caller must be the caller of the
// current request: the call carries their own token, and core answers
// about the token's user. A caller who is not an active member gets
// apperr.ErrTayfaNotFound.
type Audience interface {
	// EventChannels answers for each ref (at most MaxMembersBatch); events
	// the caller cannot see are missing from the map.
	EventChannels(ctx context.Context, caller, tayfa id.ID, refs []EventRef) (map[EventRef]EventAccess, error)
	// EventMembers pages through an event channel's audience;
	// apperr.ErrEventNotFound for an event the caller cannot see.
	EventMembers(ctx context.Context, caller, tayfa id.ID, ref EventRef, size int32, token string) ([]id.ID, string, error)
	// ActiveMembers returns which of users are active members.
	ActiveMembers(ctx context.Context, caller, tayfa id.ID, users []id.ID) ([]id.ID, error)
}

func refOf(r chanRow) EventRef {
	ref := EventRef{Event: id.ID(r.EventID.Bytes)}
	if r.CreatedBy.Valid {
		ref.Opener = id.ID(r.CreatedBy.Bytes)
	}
	return ref
}

// archivedNow reports whether the channel is archived at now: archived by
// hand or by the job, or past its planned archive time (the job may not
// have run yet).
func archivedNow(r chanRow, now time.Time) bool {
	return r.ArchivedAt.Valid || (r.ArchiveAt.Valid && !now.Before(r.ArchiveAt.Time))
}

// inAudience reports whether caller is in a narrower channel's audience and,
// for an event channel, returns core's answer (nil when core does not show
// the caller the event).
func (s *Service) inAudience(ctx context.Context, q *store.Queries, caller id.ID, r chanRow) (bool, *EventAccess, error) {
	switch r.Visibility {
	case VisibilitySelected:
		ok, err := q.IsChannelMember(ctx, store.IsChannelMemberParams{ChannelID: r.ID, UserID: caller.PG()})
		if err != nil {
			return false, nil, fmt.Errorf("kanal üyeliği: %w", err)
		}
		return ok, nil, nil
	case VisibilityEvent:
		ref := refOf(r)
		if s.cfg.Audience == nil {
			return false, nil, apperr.ErrUnavailable
		}
		got, err := s.cfg.Audience.EventChannels(ctx, caller, id.ID(r.TayfaID.Bytes), []EventRef{ref})
		if err != nil {
			return false, nil, fmt.Errorf("etkinlik kanalı: %w", err)
		}
		a, ok := got[ref]
		if !ok {
			// The event is gone from the caller's view: the opener stays in.
			return caller == ref.Opener, nil, nil
		}
		return a.Member || caller == ref.Opener, &a, nil
	}
	return true, nil, nil
}

// MemberPage is a page of a channel's audience.
type MemberPage struct {
	Users []id.ID
	Next  string
}

// Members lists the audience of an event or selected channel the caller is
// in, ordered by user ID.
func (s *Service) Members(ctx context.Context, caller, tayfa, channel id.ID, size int32, token string) (MemberPage, error) {
	if size < 0 {
		return MemberPage{}, invalid("page_size")
	}
	if size == 0 {
		size = defaultPageSize
	}
	size = min(size, maxPageSize)
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return MemberPage{}, err
	}
	if err := authorize(m, ActionView); err != nil {
		return MemberPage{}, err
	}
	q := store.New(s.cfg.Pool)
	row, err := s.loadChannel(ctx, q, m, tayfa, channel, false)
	if err != nil {
		return MemberPage{}, err
	}
	switch row.Visibility {
	case VisibilityEvent:
		if s.cfg.Audience == nil {
			return MemberPage{}, apperr.ErrUnavailable
		}
		users, next, err := s.cfg.Audience.EventMembers(ctx, caller, tayfa, refOf(row), size, token)
		if err != nil {
			return MemberPage{}, err
		}
		return MemberPage{Users: users, Next: next}, nil
	case VisibilitySelected:
	default:
		return MemberPage{}, invalid("channel_id")
	}
	after := id.ID{}
	if token != "" {
		b, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(b) != len(after) {
			return MemberPage{}, invalid("page_token")
		}
		after = id.ID(b)
	}
	rows, err := q.ListChannelMembers(ctx, store.ListChannelMembersParams{ChannelID: row.ID, After: after.PG(), N: size + 1})
	if err != nil {
		return MemberPage{}, fmt.Errorf("kanal üyeleri: %w", err)
	}
	out := MemberPage{Users: make([]id.ID, 0, len(rows))}
	for _, u := range rows {
		out.Users = append(out.Users, id.ID(u.Bytes))
	}
	if len(out.Users) > int(size) {
		out.Users = out.Users[:size]
		last := out.Users[len(out.Users)-1]
		out.Next = base64.RawURLEncoding.EncodeToString(last[:])
	}
	return out, nil
}

// selectedForChange loads a selected channel whose members the caller, its
// opener, may change: not muted, the tayfa writable, the channel not
// archived.
func (s *Service) selectedForChange(ctx context.Context, q *store.Queries, m Decision, caller, tayfa, channel id.ID) (chanRow, error) {
	row, err := s.loadChannel(ctx, q, m, tayfa, channel, true)
	if err != nil {
		return chanRow{}, err
	}
	if row.Visibility != VisibilitySelected {
		return chanRow{}, invalid("channel_id")
	}
	if !row.CreatedBy.Valid || id.ID(row.CreatedBy.Bytes) != caller {
		return chanRow{}, apperr.ErrPermissionDenied
	}
	// A change, like a message: muted callers and read-only tayfas cannot.
	if err := authorize(m, ActionPost); err != nil {
		return chanRow{}, err
	}
	if archivedNow(row, s.now()) {
		return chanRow{}, apperr.ErrChannelArchived
	}
	return row, nil
}

// checkActive returns apperr.ErrNotTayfaMember unless core confirms every
// user is an active member of the tayfa.
func (s *Service) checkActive(ctx context.Context, caller, tayfa id.ID, users []id.ID) error {
	if len(users) == 0 {
		return nil
	}
	if s.cfg.Audience == nil {
		return apperr.ErrUnavailable
	}
	active, err := s.cfg.Audience.ActiveMembers(ctx, caller, tayfa, users)
	if err != nil {
		return fmt.Errorf("tayfa üyeleri: %w", err)
	}
	for _, u := range users {
		if !slices.Contains(active, u) {
			return apperr.ErrNotTayfaMember
		}
	}
	return nil
}

func cleanUsers(users []id.ID, field string) ([]id.ID, error) {
	if len(users) > MaxMembersBatch {
		return nil, invalid(field)
	}
	out := make([]id.ID, 0, len(users))
	for _, u := range users {
		if u == (id.ID{}) {
			return nil, invalid(field)
		}
		if !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out, nil
}

// AddMembers adds active tayfa members to a selected channel. Only its
// opener; users already in change nothing.
func (s *Service) AddMembers(ctx context.Context, caller, tayfa, channel id.ID, users []id.ID) (Channel, error) {
	users, err := cleanUsers(users, "user_ids")
	if err != nil {
		return Channel{}, err
	}
	if len(users) == 0 {
		return Channel{}, invalid("user_ids")
	}
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return Channel{}, err
	}
	if err := authorize(m, ActionView); err != nil {
		return Channel{}, err
	}
	// Checked before the transaction: no lock is held while core answers.
	if _, err := s.selectedForChange(ctx, store.New(s.cfg.Pool), m, caller, tayfa, channel); err != nil {
		return Channel{}, err
	}
	if err := s.checkActive(ctx, caller, tayfa, users); err != nil {
		return Channel{}, err
	}
	var c Channel
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := s.selectedForChange(ctx, q, m, caller, tayfa, channel)
		if err != nil {
			return err
		}
		for _, u := range users {
			if err := q.AddChannelMember(ctx, store.AddChannelMemberParams{ChannelID: row.ID, UserID: u.PG()}); err != nil {
				return fmt.Errorf("kanal üyesi: %w", err)
			}
		}
		c = channelView(row, s.now())
		return record(ctx, tx, EventChannelUpdated, caller, "channel", channel, map[string]any{"tayfa_id": tayfa.String(), "change": "members_added"})
	})
	if err != nil {
		return Channel{}, err
	}
	return s.withCount(ctx, c)
}

// RemoveMember takes user out of a selected channel. Only its opener, who
// cannot remove themself. The removed user's subscriptions are checked
// again at once (Publisher.ChannelChanged); nobody is notified.
func (s *Service) RemoveMember(ctx context.Context, caller, tayfa, channel, user id.ID) error {
	if user == (id.ID{}) {
		return invalid("user_id")
	}
	if user == caller {
		return invalid("user_id")
	}
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return err
	}
	if err := authorize(m, ActionView); err != nil {
		return err
	}
	removed := false
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := s.selectedForChange(ctx, q, m, caller, tayfa, channel)
		if err != nil {
			return err
		}
		n, err := q.RemoveChannelMember(ctx, store.RemoveChannelMemberParams{ChannelID: row.ID, UserID: user.PG()})
		if err != nil {
			return fmt.Errorf("kanal üyesi: %w", err)
		}
		if n == 0 {
			return nil
		}
		removed = true
		return record(ctx, tx, EventChannelUpdated, caller, "channel", channel, map[string]any{"tayfa_id": tayfa.String(), "change": "member_removed"})
	})
	if err != nil {
		return err
	}
	if removed {
		s.cfg.Publisher.ChannelChanged(tayfa, channel)
	}
	return nil
}

// withCount fills the member count of a selected channel.
func (s *Service) withCount(ctx context.Context, c Channel) (Channel, error) {
	if c.Visibility != VisibilitySelected {
		return c, nil
	}
	counts, err := store.New(s.cfg.Pool).CountChannelMembers(ctx, []pgtype.UUID{c.ID.PG()})
	if err != nil {
		return Channel{}, fmt.Errorf("kanal üyeleri: %w", err)
	}
	for _, r := range counts {
		c.MemberCount = r.MemberCount
	}
	return c, nil
}

// Membership and event changes from core (package coreevents).

// LeaveTayfa takes user out of every selected channel of tayfa: they left,
// were removed or banned. Applying it again changes nothing.
func LeaveTayfa(ctx context.Context, q *store.Queries, tayfa, user id.ID) error {
	if err := q.RemoveUserFromTayfaChannels(ctx, store.RemoveUserFromTayfaChannelsParams{TayfaID: tayfa.PG(), UserID: user.PG()}); err != nil {
		return fmt.Errorf("kanal üyelikleri: %w", err)
	}
	return nil
}

// EventEnded moves the planned archive time of event's open channel to end
// + ArchiveAfter, unless chat already has newer information (source is
// when core learned it; events may arrive out of order).
func EventEnded(ctx context.Context, q *store.Queries, tayfa, event id.ID, end, source time.Time) error {
	if err := q.SetEventChannelArchiveAt(ctx, store.SetEventChannelArchiveAtParams{
		TayfaID: tayfa.PG(), EventID: event.PG(), ArchiveAt: ts(end.Add(ArchiveAfter)), SourceAt: ts(source),
	}); err != nil {
		return fmt.Errorf("kanal arşiv zamanı: %w", err)
	}
	return nil
}

// ArchiveDue archives the event channels whose time has come.
func (s *Service) ArchiveDue(ctx context.Context) (int64, error) {
	n, err := store.New(s.cfg.Pool).ArchiveDueEventChannels(ctx, ts(s.now()))
	if err != nil {
		return 0, fmt.Errorf("etkinlik kanalı arşivi: %w", err)
	}
	return n, nil
}
