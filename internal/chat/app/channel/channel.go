// Package channel runs tayfa channels and their encrypted messages
// (ChannelService, proto/tayfam/chat/v1/channels.proto; ADR-0005 "Kanal
// mesajları"; data model v14 "Sohbet", "Spam ve taciz", "Silinen içeriği
// geri alma", "Okunma bilgisi"; issue #81).
//
// Every method starts the same way: the caller comes from the verified
// token and core decides what the caller may do with the tayfa's channels
// (ChannelAccessService.AuthorizeChannelAction with the caller's own token,
// ADR-0009; core's single permission table, CLAUDE.md rule 5). A
// non-member gets apperr.ErrTayfaNotFound; a suspended, archived or
// deletion-pending tayfa makes channels read-only; hiding or restoring
// someone else's message also follows the role hierarchy against the
// author's current role. chat keeps no rule of its own. The channel is
// loaded by (tayfa_id, channel_id), so an ID from another tayfa is "not
// found" (IDOR); a staff channel is "not found" for plain members.
//
// Message bodies exist in plain text only in memory: they are encrypted
// with channelcrypt before they reach the database and are never written to
// logs, events, notifications or error messages. Events carry IDs only.
//
// Sending (Send) is a transport-free function: REST and the WebSocket
// (issue #82) call the same method, so membership, mute, archive and slow
// mode are checked in one place (ADR-0005 "Mesaj protokolü").
//
// Without a message key (CHAT_MESSAGE_KEYS unset; never in prod) every user
// method returns apperr.ErrChannelsUnavailable. Cleanup, PurgeTayfa and the
// purge job need no key.
package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// Limits (veri modeli v14 "Uzunluk sınırları", "Spam ve taciz"; the same
// CHECKs are in the database).
const (
	NameMax     = 40
	BodyMax     = 4000
	MaxSlowMode = time.Hour
	// MaxUnread caps unread counts; the client shows 100 as "99+".
	MaxUnread = 100
	// RestoreWindow is how long a moderation deletion can be undone; after
	// it the purge job deletes the message.
	RestoreWindow = 30 * 24 * time.Hour

	defaultPageSize = 50
	maxPageSize     = 100
	purgeBatch      = 1000
	purgeEvery      = time.Hour
)

// Visibilities (channels.visibility).
const (
	VisibilityMembers = "members"
	VisibilityStaff   = "staff"
)

// Event types. Payloads hold IDs only, never a name or a body.
const (
	EventMessageCreated  = "channel.message_created"
	EventMessageDeleted  = "channel.message_deleted"
	EventMessageRestored = "channel.message_restored"
	EventChannelCreated  = "channel.created"
	EventChannelUpdated  = "channel.updated"
)

// Config wires a Service.
type Config struct {
	Pool   *pgxpool.Pool
	Log    *slog.Logger
	Access Access
	// Audience answers about event channels and tayfa members (issue
	// #183). nil makes event and selected channels apperr.ErrUnavailable
	// (a local run without core).
	Audience Audience
	// SendLimit is the per-user message rate, shared with direct messages
	// and every transport (issue #101); required.
	SendLimit SendLimit
	// Cipher encrypts message bodies. nil turns the user methods off
	// (apperr.ErrChannelsUnavailable); only local and dev may run so.
	Cipher *channelcrypt.Cipher
	// Publisher hears about committed changes for live delivery
	// (WebSocket, issue #82); optional.
	Publisher Publisher
	Now       func() time.Time // optional, for tests
}

// SendLimit is the per-user message rate shared by every send path:
// channel and direct messages, REST and WebSocket (issue #101). Allow
// reports whether key (the user ID) may send one more message now.
type SendLimit interface {
	Allow(key string) bool
}

// Publisher is told about changes after they are committed, so that
// connected clients get them at once (ADR-0005 "Mesaj protokolü": the
// WebSocket carries live delivery only). Methods must not block.
type Publisher interface {
	// MessageCreated: a new message (not a retry of an existing one). m
	// carries the plain body; it goes only to the channel's subscribers.
	MessageCreated(tayfa id.ID, m Message)
	// MessageDeleted: a message was erased or hidden.
	MessageDeleted(tayfa, channel, message id.ID)
	// MessageRestored: a hidden message is visible again; m carries the body.
	MessageRestored(tayfa id.ID, m Message)
	// ChannelChanged: who may see the channel may have changed
	// (visibility), so subscriptions must be checked again.
	ChannelChanged(tayfa, channel id.ID)
}

// nopPublisher is used when none is configured.
type nopPublisher struct{}

func (nopPublisher) MessageCreated(id.ID, Message)      {}
func (nopPublisher) MessageDeleted(id.ID, id.ID, id.ID) {}
func (nopPublisher) MessageRestored(id.ID, Message)     {}
func (nopPublisher) ChannelChanged(id.ID, id.ID)        {}

// Service implements ChannelService's business logic.
type Service struct {
	cfg  Config
	slow *slowMode
	ids  messageIDs
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Access == nil || cfg.SendLimit == nil {
		return nil, errors.New("channel: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Publisher == nil {
		cfg.Publisher = nopPublisher{}
	}
	return &Service{cfg: cfg, slow: newSlowMode()}, nil
}

// now is UTC with the database's microsecond precision.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// Channel is a channel as members see it.
type Channel struct {
	ID              id.ID
	TayfaID         id.ID
	Name            string
	Visibility      string
	SlowModeSeconds int32
	CreatedBy       id.ID // zero when the account was deleted
	ArchivedAt      time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// Unread is the caller's unread messages (at most MaxUnread); only in
	// List.
	Unread int32
	// LastRead is the caller's read marker; zero if none. Only in List.
	LastRead id.ID
	// Event channels only (issue #183).
	EventID id.ID
	// PlannedArchiveAt is when an event channel is archived; zero if not
	// known.
	PlannedArchiveAt time.Time
	// MemberCount is the audience of an event or selected channel.
	MemberCount int32
}

// ChannelInput is a new channel.
type ChannelInput struct {
	Name            string
	Visibility      string // empty: members
	SlowModeSeconds int32
	// Event is the event of an event channel; required there, only there.
	Event id.ID
	// Members are the first members of a selected channel besides the
	// opener; only there.
	Members []id.ID
}

// ChannelUpdate changes the fields that are not nil.
type ChannelUpdate struct {
	Name            *string
	Visibility      *string
	SlowModeSeconds *int32
}

// Message is a channel message as members see it.
type Message struct {
	ID        id.ID
	ChannelID id.ID
	AuthorID  id.ID // zero when the author's account was deleted
	// Body is empty for a deleted message ("Bu mesaj silindi").
	Body      string
	Deleted   bool
	CreatedAt time.Time
	// Restorable is set, for callers who may restore, on a moderation
	// deletion still within RestoreWindow.
	Restorable bool
}

// SendInput is a message to send.
type SendInput struct {
	Body string
	// ClientRequestID makes a retry return the first message (ADR-0005).
	ClientRequestID id.ID
}

// ListInput selects a page of messages. Without After the newest come
// first and PageToken continues towards older ones; with After the messages
// newer than it come oldest first (catching up after a reconnect) and
// PageToken continues towards newer ones.
type ListInput struct {
	PageSize  int32
	PageToken string
	After     id.ID
}

func invalid(field string) error { return apperr.Validation(field) }

// access asks core what the caller may do with the tayfa's channels.
func (s *Service) access(ctx context.Context, caller, tayfa id.ID) (Decision, error) {
	if s.cfg.Cipher == nil {
		return Decision{}, apperr.ErrChannelsUnavailable
	}
	d, err := s.cfg.Access.Channel(ctx, tayfa, caller, ActionView, id.ID{})
	if err != nil {
		return Decision{}, fmt.Errorf("kanal izni: %w", err)
	}
	d.caller = caller
	return d, nil
}

// chanRow is a channel row; every channel query returns these columns.
type chanRow = store.GetChannelRow

// visible returns nil if the caller may see the channel: staff channels
// need the role; event and selected channels need the caller in their
// audience, whatever the role (issue #183). An event channel's answer from
// core is returned, nil otherwise.
func (s *Service) visible(ctx context.Context, q *store.Queries, d Decision, row chanRow) (*EventAccess, error) {
	switch row.Visibility {
	case VisibilityMembers:
		return nil, nil
	case VisibilityStaff:
		if !d.Allowed(ActionViewStaff) {
			return nil, apperr.ErrChannelNotFound
		}
		return nil, nil
	case VisibilityEvent, VisibilitySelected:
		in, a, err := s.inAudience(ctx, q, d.caller, row)
		if err != nil {
			return nil, err
		}
		if !in {
			return nil, apperr.ErrChannelNotFound
		}
		return a, nil
	}
	// A value this version does not know: default "no".
	return nil, apperr.ErrChannelNotFound
}

func (s *Service) loadChannel(ctx context.Context, q *store.Queries, m Decision, tayfa, channel id.ID, forUpdate bool) (chanRow, error) {
	row, _, err := s.loadChannelAudience(ctx, q, m, tayfa, channel, forUpdate)
	return row, err
}

// loadChannelAudience is loadChannel that also returns core's answer for
// an event channel.
func (s *Service) loadChannelAudience(ctx context.Context, q *store.Queries, m Decision, tayfa, channel id.ID, forUpdate bool) (chanRow, *EventAccess, error) {
	var (
		row chanRow
		err error
	)
	if forUpdate {
		var r store.GetChannelForUpdateRow
		r, err = q.GetChannelForUpdate(ctx, store.GetChannelForUpdateParams{TayfaID: tayfa.PG(), ID: channel.PG()})
		row = chanRow(r)
	} else {
		row, err = q.GetChannel(ctx, store.GetChannelParams{TayfaID: tayfa.PG(), ID: channel.PG()})
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return chanRow{}, nil, apperr.ErrChannelNotFound
	case err != nil:
		return chanRow{}, nil, fmt.Errorf("kanal: %w", err)
	}
	a, err := s.visible(ctx, q, m, row)
	if err != nil {
		return chanRow{}, nil, err
	}
	return row, a, nil
}

// validName checks a channel name: 1–40 characters, valid UTF-8, no
// control characters, no leading, trailing or repeated spaces.
func validName(s string) bool {
	if s == "" || len(s) > NameMax*4 || !utf8.ValidString(s) || utf8.RuneCountInString(s) > NameMax ||
		strings.Join(strings.Fields(s), " ") != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// normalize is the Turkish-aware lower case the uniqueness check uses.
func normalize(s string) string { return strings.ToLowerSpecial(unicode.TurkishCase, s) }

// validBody checks a message body: valid UTF-8, 1–4000 characters, not
// only white space, no control characters other than tab and line breaks.
func validBody(s string) bool {
	if len(s) > BodyMax*4 || !utf8.ValidString(s) || utf8.RuneCountInString(s) > BodyMax || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return false
		}
	}
	return true
}

// channelView is a channel as members see it at now: an event channel past
// its planned archive time shows as archived then.
func channelView(r chanRow, now time.Time) Channel {
	c := Channel{
		ID: id.ID(r.ID.Bytes), TayfaID: id.ID(r.TayfaID.Bytes), Name: r.Name, Visibility: r.Visibility,
		SlowModeSeconds: r.SlowModeSeconds, CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
	if r.CreatedBy.Valid {
		c.CreatedBy = id.ID(r.CreatedBy.Bytes)
	}
	if r.EventID.Valid {
		c.EventID = id.ID(r.EventID.Bytes)
	}
	if r.ArchiveAt.Valid {
		c.PlannedArchiveAt = r.ArchiveAt.Time.UTC()
	}
	switch {
	case r.ArchivedAt.Valid:
		c.ArchivedAt = r.ArchivedAt.Time.UTC()
	case archivedNow(r, now):
		c.ArchivedAt = c.PlannedArchiveAt
	}
	return c
}

// withEvent fills what core said about an event channel.
func withEvent(c Channel, a *EventAccess) Channel {
	if a != nil {
		c.MemberCount = a.MemberCount
	}
	return c
}

// List returns the tayfa's channels the caller may see, with the caller's
// unread counts. Archived channels come last. Event channels are kept only
// if core puts the caller in their audience (asked in batches of
// MaxMembersBatch); selected channels only if the caller was added.
func (s *Service) List(ctx context.Context, caller, tayfa id.ID) ([]Channel, error) {
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return nil, err
	}
	if err := authorize(m, ActionView); err != nil {
		return nil, err
	}
	q := store.New(s.cfg.Pool)
	rows, err := q.ListChannels(ctx, store.ListChannelsParams{
		UserID: caller.PG(), MaxUnread: MaxUnread, TayfaID: tayfa.PG(), IncludeStaff: m.Allowed(ActionViewStaff),
	})
	if err != nil {
		return nil, fmt.Errorf("kanallar: %w", err)
	}
	now := s.now()
	var refs []EventRef
	var selected []pgtype.UUID
	for _, r := range rows {
		switch r.Visibility {
		case VisibilityEvent:
			refs = append(refs, refOf(listRow(r)))
		case VisibilitySelected:
			selected = append(selected, r.ID)
		}
	}
	events := map[EventRef]EventAccess{}
	if len(refs) > 0 {
		if s.cfg.Audience == nil {
			return nil, apperr.ErrUnavailable
		}
		for chunk := range slices.Chunk(refs, MaxMembersBatch) {
			got, err := s.cfg.Audience.EventChannels(ctx, caller, tayfa, chunk)
			if err != nil {
				return nil, fmt.Errorf("etkinlik kanalları: %w", err)
			}
			maps.Copy(events, got)
		}
	}
	counts := map[id.ID]int32{}
	if len(selected) > 0 {
		cs, err := q.CountChannelMembers(ctx, selected)
		if err != nil {
			return nil, fmt.Errorf("kanal üyeleri: %w", err)
		}
		for _, c := range cs {
			counts[id.ID(c.ChannelID.Bytes)] = c.MemberCount
		}
	}
	out := make([]Channel, 0, len(rows))
	for _, r := range rows {
		row := listRow(r)
		c := channelView(row, now)
		switch r.Visibility {
		case VisibilityEvent:
			ref := refOf(row)
			a, ok := events[ref]
			if (!ok || !a.Member) && caller != ref.Opener {
				continue
			}
			if ok {
				c = withEvent(c, &a)
			}
		case VisibilitySelected:
			c.MemberCount = counts[c.ID]
		}
		c.Unread = int32(min(r.UnreadCount, MaxUnread)) //nolint:gosec // at most MaxUnread
		if r.LastReadMessageID.Valid {
			c.LastRead = id.ID(r.LastReadMessageID.Bytes)
		}
		out = append(out, c)
	}
	return out, nil
}

func listRow(r store.ListChannelsRow) chanRow {
	return chanRow{
		ID: r.ID, TayfaID: r.TayfaID, Name: r.Name, Visibility: r.Visibility, SlowModeSeconds: r.SlowModeSeconds,
		CreatedBy: r.CreatedBy, ArchivedAt: r.ArchivedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		EventID: r.EventID, ArchiveAt: r.ArchiveAt,
	}
}

// Get returns one channel the caller may see.
func (s *Service) Get(ctx context.Context, caller, tayfa, channel id.ID) (Channel, error) {
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return Channel{}, err
	}
	if err := authorize(m, ActionView); err != nil {
		return Channel{}, err
	}
	row, a, err := s.loadChannelAudience(ctx, store.New(s.cfg.Pool), m, tayfa, channel, false)
	if err != nil {
		return Channel{}, err
	}
	return s.withCount(ctx, withEvent(channelView(row, s.now()), a))
}

func validVisibility(v string) bool {
	return v == VisibilityMembers || v == VisibilityStaff || v == VisibilityEvent || v == VisibilitySelected
}

// narrow reports whether v is an audience narrower than the tayfa: those
// cannot be changed to or from (issue #183).
func narrow(v string) bool { return v == VisibilityEvent || v == VisibilitySelected }

// Create opens a channel. Members and staff channels, and selected-member
// channels (issue #183): moderators and admins only. Event channels: also
// whoever may manage the event (core's answer), one open channel per
// event, archived ArchiveAfter the event's end.
func (s *Service) Create(ctx context.Context, caller, tayfa id.ID, in ChannelInput) (Channel, error) {
	if in.Visibility == "" {
		in.Visibility = VisibilityMembers
	}
	members, err := cleanUsers(in.Members, "member_user_ids")
	if err != nil {
		return Channel{}, err
	}
	members = slices.DeleteFunc(members, func(u id.ID) bool { return u == caller })
	switch {
	case !validName(in.Name):
		return Channel{}, invalid("name")
	case !validVisibility(in.Visibility):
		return Channel{}, invalid("visibility")
	case in.SlowModeSeconds < 0 || time.Duration(in.SlowModeSeconds)*time.Second > MaxSlowMode:
		return Channel{}, invalid("slow_mode_seconds")
	case (in.Visibility == VisibilityEvent) != (in.Event != id.ID{}):
		return Channel{}, invalid("event_id")
	case in.Visibility != VisibilitySelected && len(in.Members) > 0:
		return Channel{}, invalid("member_user_ids")
	}
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return Channel{}, err
	}
	p := store.CreateChannelParams{
		ID: id.New().PG(), TayfaID: tayfa.PG(), Name: in.Name, NameNormalized: normalize(in.Name),
		Visibility: in.Visibility, SlowModeSeconds: in.SlowModeSeconds, CreatedBy: caller.PG(),
	}
	var ev *EventAccess
	if in.Visibility == VisibilityEvent {
		// core decides who may open it; asked before the transaction.
		if err := authorize(m, ActionView); err != nil {
			return Channel{}, err
		}
		if s.cfg.Audience == nil {
			return Channel{}, apperr.ErrUnavailable
		}
		ref := EventRef{Event: in.Event, Opener: caller}
		asked := s.now()
		got, err := s.cfg.Audience.EventChannels(ctx, caller, tayfa, []EventRef{ref})
		if err != nil {
			return Channel{}, fmt.Errorf("etkinlik: %w", err)
		}
		a, ok := got[ref]
		switch {
		case !ok:
			return Channel{}, apperr.ErrEventNotFound
		case !a.CanOpen:
			// Why (role, mute, read-only) is what the general answer says.
			if err := authorize(m, ActionManage); err != nil {
				return Channel{}, err
			}
			return Channel{}, apperr.ErrPermissionDenied
		}
		archiveAt := a.End.Add(ArchiveAfter)
		if !asked.Before(archiveAt) {
			return Channel{}, apperr.ErrEventEnded
		}
		p.EventID, p.ArchiveAt, p.ArchiveSourceAt = in.Event.PG(), ts(archiveAt), ts(asked)
		ev = &a
	} else {
		if err := authorize(m, ActionManage); err != nil {
			return Channel{}, err
		}
		if err := s.checkActive(ctx, caller, tayfa, members); err != nil {
			return Channel{}, err
		}
	}
	var c Channel
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := q.CreateChannel(ctx, p)
		if err != nil {
			return nameErr(err)
		}
		c = withEvent(channelView(chanRow(r), s.now()), ev)
		if in.Visibility == VisibilitySelected {
			for _, u := range append([]id.ID{caller}, members...) {
				if err := q.AddChannelMember(ctx, store.AddChannelMemberParams{ChannelID: r.ID, UserID: u.PG()}); err != nil {
					return fmt.Errorf("kanal üyesi: %w", err)
				}
			}
			c.MemberCount = int32(len(members) + 1) //nolint:gosec // at most MaxMembersBatch+1
		}
		return record(ctx, tx, EventChannelCreated, caller, "channel", c.ID, map[string]any{"tayfa_id": tayfa.String()})
	})
	if err != nil {
		return Channel{}, err
	}
	return c, nil
}

// nameErr maps the name uniqueness violation.
func nameErr(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "channels_name_unique" {
		return apperr.ErrChannelNameTaken
	}
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "channels_event_open" {
		return apperr.ErrEventChannelExists
	}
	return fmt.Errorf("kanal yazılamadı: %w", err)
}

// Update changes a channel's name, visibility or slow mode. Moderators and
// admins only. An update that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, caller, tayfa, channel id.ID, in ChannelUpdate) (Channel, error) {
	switch {
	case in.Name == nil && in.Visibility == nil && in.SlowModeSeconds == nil:
		return Channel{}, invalid("update_mask")
	case in.Name != nil && !validName(*in.Name):
		return Channel{}, invalid("name")
	case in.Visibility != nil && !validVisibility(*in.Visibility):
		return Channel{}, invalid("visibility")
	case in.SlowModeSeconds != nil && (*in.SlowModeSeconds < 0 || time.Duration(*in.SlowModeSeconds)*time.Second > MaxSlowMode):
		return Channel{}, invalid("slow_mode_seconds")
	}
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return Channel{}, err
	}
	if err := authorize(m, ActionManage); err != nil {
		return Channel{}, err
	}
	var (
		c          Channel
		visibility bool
	)
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := s.loadChannel(ctx, q, m, tayfa, channel, true)
		if err != nil {
			return err
		}
		next := row
		if in.Name != nil {
			next.Name = *in.Name
		}
		if in.Visibility != nil {
			next.Visibility = *in.Visibility
		}
		// An event or selected channel keeps its audience (issue #183).
		if next.Visibility != row.Visibility && (narrow(next.Visibility) || narrow(row.Visibility)) {
			return invalid("visibility")
		}
		if in.SlowModeSeconds != nil {
			next.SlowModeSeconds = *in.SlowModeSeconds
		}
		if next == row {
			c = channelView(row, s.now())
			return nil
		}
		r, err := q.UpdateChannel(ctx, store.UpdateChannelParams{
			Name: next.Name, NameNormalized: normalize(next.Name), Visibility: next.Visibility,
			SlowModeSeconds: next.SlowModeSeconds, TayfaID: tayfa.PG(), ID: channel.PG(),
		})
		if err != nil {
			return nameErr(err)
		}
		c = channelView(chanRow(r), s.now())
		visibility = next.Visibility != row.Visibility
		return record(ctx, tx, EventChannelUpdated, caller, "channel", channel, map[string]any{"tayfa_id": tayfa.String(), "change": "edited"})
	})
	if err != nil {
		return Channel{}, err
	}
	if visibility {
		s.cfg.Publisher.ChannelChanged(tayfa, channel)
	}
	return c, nil
}

// SetArchived archives a channel (read-only: no new messages) or brings it
// back. Moderators and admins only. Unarchiving fails with
// ErrChannelNameTaken if a channel with the same name was opened meanwhile.
func (s *Service) SetArchived(ctx context.Context, caller, tayfa, channel id.ID, archived bool) (Channel, error) {
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return Channel{}, err
	}
	if err := authorize(m, ActionManage); err != nil {
		return Channel{}, err
	}
	var c Channel
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := s.loadChannel(ctx, q, m, tayfa, channel, true)
		if err != nil {
			return err
		}
		if row.ArchivedAt.Valid == archived {
			c = channelView(row, s.now())
			return nil
		}
		// An event channel past its planned archive time stays archived
		// (issue #183).
		if !archived && row.ArchiveAt.Valid && !s.now().Before(row.ArchiveAt.Time) {
			return apperr.ErrEventEnded
		}
		at := pgtype.Timestamptz{}
		change := "unarchived"
		if archived {
			at, change = ts(s.now()), "archived"
		}
		r, err := q.SetChannelArchived(ctx, store.SetChannelArchivedParams{ArchivedAt: at, TayfaID: tayfa.PG(), ID: channel.PG()})
		if err != nil {
			return nameErr(err)
		}
		c = channelView(chanRow(r), s.now())
		return record(ctx, tx, EventChannelUpdated, caller, "channel", channel, map[string]any{"tayfa_id": tayfa.String(), "change": change})
	})
	if err != nil {
		return Channel{}, err
	}
	return c, nil
}

// record writes an outbox event (IDs only) as the last statement of tx.
func record(ctx context.Context, tx pgx.Tx, typ string, actor id.ID, subjectType string, subject id.ID, payload map[string]any) error {
	if _, err := outbox.Write(ctx, tx, outbox.NewEvent{Type: typ, Actor: actor, SubjectType: subjectType, SubjectID: subject, Payload: payload}); err != nil {
		return fmt.Errorf("olay: %w", err)
	}
	return nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
