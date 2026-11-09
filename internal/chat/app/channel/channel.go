// Package channel runs tayfa channels and their encrypted messages
// (ChannelService, proto/tayfam/chat/v1/channels.proto; ADR-0005 "Kanal
// mesajları"; data model v14 "Sohbet", "Spam ve taciz", "Silinen içeriği
// geri alma", "Okunma bilgisi"; issue #81).
//
// Every method starts the same way: the caller comes from the verified
// token, core says whether the caller is an active member of the tayfa and
// with which role (GetMembership with the caller's own token, ADR-0009); a
// non-member gets apperr.ErrTayfaNotFound. The channel is loaded by
// (tayfa_id, channel_id), so an ID from another tayfa is "not found"
// (IDOR); a staff channel is "not found" for plain members. Then the
// permission table (authz.go) decides; it defaults to "no".
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

// Memberships answers who the caller is in a tayfa (core GetMembership).
type Memberships interface {
	Member(ctx context.Context, tayfa, user id.ID) (Member, error)
}

// Config wires a Service.
type Config struct {
	Pool    *pgxpool.Pool
	Log     *slog.Logger
	Members Memberships
	// Cipher encrypts message bodies. nil turns the user methods off
	// (apperr.ErrChannelsUnavailable); only local and dev may run so.
	Cipher *channelcrypt.Cipher
	Now    func() time.Time // optional, for tests
}

// Service implements ChannelService's business logic.
type Service struct {
	cfg  Config
	slow *slowMode
	ids  messageIDs
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Members == nil {
		return nil, errors.New("channel: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
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
}

// ChannelInput is a new channel.
type ChannelInput struct {
	Name            string
	Visibility      string // empty: members
	SlowModeSeconds int32
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

// access loads the caller's membership and checks it allows a in tayfa.
func (s *Service) access(ctx context.Context, caller, tayfa id.ID) (Member, error) {
	if s.cfg.Cipher == nil {
		return Member{}, apperr.ErrChannelsUnavailable
	}
	m, err := s.cfg.Members.Member(ctx, tayfa, caller)
	if err != nil {
		return Member{}, fmt.Errorf("üyelik: %w", err)
	}
	if !m.Active {
		return Member{}, apperr.ErrTayfaNotFound
	}
	return m, nil
}

// authorize turns a refusal into the error the caller sees: a muted member
// whose role would allow a gets ErrMuted.
func authorize(m Member, a Action, now time.Time) error {
	switch {
	case Allowed(m, a, now):
		return nil
	case roleAllows(m, a):
		return apperr.ErrMuted
	}
	return apperr.ErrPermissionDenied
}

// visible returns the channel if the member may see it.
func visible(m Member, row chanRow, now time.Time) error {
	if row.Visibility == VisibilityStaff && !Allowed(m, ActionViewStaff, now) {
		return apperr.ErrChannelNotFound
	}
	return nil
}

// chanRow is a channel row; every channel query returns these columns.
type chanRow = store.GetChannelRow

func (s *Service) loadChannel(ctx context.Context, q *store.Queries, m Member, tayfa, channel id.ID, forUpdate bool) (chanRow, error) {
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
		return chanRow{}, apperr.ErrChannelNotFound
	case err != nil:
		return chanRow{}, fmt.Errorf("kanal: %w", err)
	}
	if err := visible(m, row, s.now()); err != nil {
		return chanRow{}, err
	}
	return row, nil
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

func channelView(r chanRow) Channel {
	c := Channel{
		ID: id.ID(r.ID.Bytes), TayfaID: id.ID(r.TayfaID.Bytes), Name: r.Name, Visibility: r.Visibility,
		SlowModeSeconds: r.SlowModeSeconds, CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
	if r.CreatedBy.Valid {
		c.CreatedBy = id.ID(r.CreatedBy.Bytes)
	}
	if r.ArchivedAt.Valid {
		c.ArchivedAt = r.ArchivedAt.Time.UTC()
	}
	return c
}

// List returns the tayfa's channels the caller may see, with the caller's
// unread counts. Archived channels come last.
func (s *Service) List(ctx context.Context, caller, tayfa id.ID) ([]Channel, error) {
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return nil, err
	}
	if err := authorize(m, ActionView, s.now()); err != nil {
		return nil, err
	}
	rows, err := store.New(s.cfg.Pool).ListChannels(ctx, store.ListChannelsParams{
		UserID: caller.PG(), MaxUnread: MaxUnread, TayfaID: tayfa.PG(), IncludeStaff: Allowed(m, ActionViewStaff, s.now()),
	})
	if err != nil {
		return nil, fmt.Errorf("kanallar: %w", err)
	}
	out := make([]Channel, 0, len(rows))
	for _, r := range rows {
		c := channelView(chanRow{
			ID: r.ID, TayfaID: r.TayfaID, Name: r.Name, Visibility: r.Visibility, SlowModeSeconds: r.SlowModeSeconds,
			CreatedBy: r.CreatedBy, ArchivedAt: r.ArchivedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
		c.Unread = int32(min(r.UnreadCount, MaxUnread)) //nolint:gosec // at most MaxUnread
		if r.LastReadMessageID.Valid {
			c.LastRead = id.ID(r.LastReadMessageID.Bytes)
		}
		out = append(out, c)
	}
	return out, nil
}

// Get returns one channel the caller may see.
func (s *Service) Get(ctx context.Context, caller, tayfa, channel id.ID) (Channel, error) {
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return Channel{}, err
	}
	if err := authorize(m, ActionView, s.now()); err != nil {
		return Channel{}, err
	}
	row, err := s.loadChannel(ctx, store.New(s.cfg.Pool), m, tayfa, channel, false)
	if err != nil {
		return Channel{}, err
	}
	return channelView(row), nil
}

func validVisibility(v string) bool { return v == VisibilityMembers || v == VisibilityStaff }

// Create opens a channel. Moderators and admins only.
func (s *Service) Create(ctx context.Context, caller, tayfa id.ID, in ChannelInput) (Channel, error) {
	if in.Visibility == "" {
		in.Visibility = VisibilityMembers
	}
	switch {
	case !validName(in.Name):
		return Channel{}, invalid("name")
	case !validVisibility(in.Visibility):
		return Channel{}, invalid("visibility")
	case in.SlowModeSeconds < 0 || time.Duration(in.SlowModeSeconds)*time.Second > MaxSlowMode:
		return Channel{}, invalid("slow_mode_seconds")
	}
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return Channel{}, err
	}
	if err := authorize(m, ActionManage, s.now()); err != nil {
		return Channel{}, err
	}
	var c Channel
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		r, err := store.New(tx).CreateChannel(ctx, store.CreateChannelParams{
			ID: id.New().PG(), TayfaID: tayfa.PG(), Name: in.Name, NameNormalized: normalize(in.Name),
			Visibility: in.Visibility, SlowModeSeconds: in.SlowModeSeconds, CreatedBy: caller.PG(),
		})
		if err != nil {
			return nameErr(err)
		}
		c = channelView(chanRow(r))
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
	if err := authorize(m, ActionManage, s.now()); err != nil {
		return Channel{}, err
	}
	var c Channel
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
		if in.SlowModeSeconds != nil {
			next.SlowModeSeconds = *in.SlowModeSeconds
		}
		if next == row {
			c = channelView(row)
			return nil
		}
		r, err := q.UpdateChannel(ctx, store.UpdateChannelParams{
			Name: next.Name, NameNormalized: normalize(next.Name), Visibility: next.Visibility,
			SlowModeSeconds: next.SlowModeSeconds, TayfaID: tayfa.PG(), ID: channel.PG(),
		})
		if err != nil {
			return nameErr(err)
		}
		c = channelView(chanRow(r))
		return record(ctx, tx, EventChannelUpdated, caller, "channel", channel, map[string]any{"tayfa_id": tayfa.String(), "change": "edited"})
	})
	if err != nil {
		return Channel{}, err
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
	if err := authorize(m, ActionManage, s.now()); err != nil {
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
			c = channelView(row)
			return nil
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
		c = channelView(chanRow(r))
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
