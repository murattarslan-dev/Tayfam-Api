// Package dm runs one-to-one conversations, plain version
// (DirectMessageService, proto/tayfam/chat/v1/direct_messages.proto; issue
// #95; ADR-0005 "Direkt mesajlar", "Sonuçlar"; data model v14 "Mesaj İsteği
// Modeli (v7)", "Okunma bilgisi").
//
// Who may write to whom is core's decision, asked for every message with
// the caller's own token (Rules: DirectMessageService.CheckDirectMessage;
// ADR-0009, ADR-0013). chat keeps only the conversation's state:
//
//   - active: both write freely;
//   - request: opened by a sender core answered "request" for; the sender
//     may send only that one message. The recipient accepts by replying or
//     with Accept; Decline makes it declined;
//   - declined: the sender is never told, still sees the request as sent
//     and cannot write again, even as a friend, until the recipient takes
//     it off their declined list (Declined, RemoveDeclined; issue #101):
//     then it is a waiting request again. The recipient no longer sees it
//     among conversations. If the recipient writes later, it becomes
//     active.
//
// A sender opens at most core's daily limit (5 or 20) of requests in 24
// hours, counted from the database so a restart resets nothing; declined
// requests still count. A block closes every conversation: core refuses the
// next message in either direction.
//
// Send is the single send path: REST (gRPC) and the WebSocket call it, so
// the rule, the request state and the limits are checked in one place
// (ADR-0005 "Mesaj protokolü", v7). The per-user send rate (Config.SendLimit)
// is shared with channel messages (issue #101).
//
// The recipient has one notice per conversation; it carries their unread
// count, updated with every message until the conversation is read.
//
// Message bodies exist in plain text only in memory: they are encrypted with
// the channel message key (channelcrypt.EncryptDirect, AAD message +
// conversation + table) before they reach the database and are never
// written to logs, events, notifications or error messages. Events and
// notifications carry IDs only; a direct message notice does not even name
// the sender (ADR-0005 "Bildirimler").
//
// Without a message key (CHAT_MESSAGE_KEYS unset; never in prod) every user
// method returns apperr.ErrDirectMessagesUnavailable. Cleanup and the purge
// job need no key.
package dm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// Limits (v14; the same CHECKs are in the database).
const (
	BodyMax = 4000
	// MaxUnread caps unread counts; the client shows 100 as "99+".
	MaxUnread = 100
	// RequestWindow is the window of the daily request limit.
	RequestWindow = 24 * time.Hour
	// PurgeAfter is how long a deleted message stays as "Bu mesaj silindi"
	// before the purge job deletes it (v14: 30 days).
	PurgeAfter = 30 * 24 * time.Hour
	// fallbackRequestLimit is used when core's limit is missing or out of
	// range: the stricter of the two.
	fallbackRequestLimit = 5
	maxRequestLimit      = 20

	defaultPageSize = 50
	maxPageSize     = 100
	purgeBatch      = 1000
	purgeEvery      = time.Hour
)

// Event types. Payloads hold IDs only.
const (
	EventMessageCreated  = "dm.message_created"
	EventMessageDeleted  = "dm.message_deleted"
	EventRequestAccepted = "dm.request_accepted"
	EventRequestDeclined = "dm.request_declined"
	// EventRequestRestored: the decliner took the request off their
	// declined list (issue #101).
	EventRequestRestored = "dm.request_restored"
)

// Conversation states in the database.
const (
	stateRequest  = "request"
	stateActive   = "active"
	stateDeclined = "declined"
)

// Status is a conversation as the caller sees it.
type Status int

// Statuses (direct_messages.proto ConversationStatus).
const (
	StatusActive Status = iota + 1
	StatusRequestSent
	StatusRequestReceived
)

// Decision is core's answer to "may the caller write to this user?".
type Decision int

// Decisions (core's DirectMessageDecision).
const (
	Denied Decision = iota + 1
	Request
	Active
)

// Verdict is core's answer: the decision and, with Request, the caller's
// daily request limit.
type Verdict struct {
	Decision     Decision
	RequestLimit int
}

// Rules asks core whether caller may write to recipient. caller must be
// the user of the token ctx carries: core reads the sender from it.
type Rules interface {
	Check(ctx context.Context, caller, recipient id.ID) (Verdict, error)
}

// Publisher is told about committed changes so that connected clients get
// them at once (ADR-0005: the WebSocket carries live delivery only).
// Methods must not block.
type Publisher interface {
	// MessageCreated: a new message (not a retry), with its plain body, for
	// the two parties' connections.
	MessageCreated(parties [2]id.ID, m Message)
	// MessageDeleted: a message was erased.
	MessageDeleted(parties [2]id.ID, conversation, message id.ID)
	// ConversationAccepted: a request became active.
	ConversationAccepted(parties [2]id.ID, conversation id.ID)
	// ConversationRead: reader's marker moved; for the other party.
	ConversationRead(to, reader, conversation, message id.ID)
	// Notified: notifications were written for their recipients.
	Notified(r notification.Request)
}

type nopPublisher struct{}

func (nopPublisher) MessageCreated([2]id.ID, Message)            {}
func (nopPublisher) MessageDeleted([2]id.ID, id.ID, id.ID)       {}
func (nopPublisher) ConversationAccepted([2]id.ID, id.ID)        {}
func (nopPublisher) ConversationRead(id.ID, id.ID, id.ID, id.ID) {}
func (nopPublisher) Notified(notification.Request)               {}

// SendLimit is the per-user message rate shared by every send path:
// channel and direct messages, REST and WebSocket (issue #101). Allow
// reports whether key (the user ID) may send one more message now.
type SendLimit interface {
	Allow(key string) bool
}

// Config wires a Service.
type Config struct {
	Pool  *pgxpool.Pool
	Log   *slog.Logger
	Rules Rules
	// SendLimit is the per-user send rate (required: there is no unlimited
	// sending).
	SendLimit SendLimit
	// Cipher encrypts message bodies. nil turns the user methods off
	// (apperr.ErrDirectMessagesUnavailable); only local and dev may run so.
	Cipher    *channelcrypt.Cipher
	Publisher Publisher        // optional
	Now       func() time.Time // optional, for tests
}

// Service implements DirectMessageService's business logic.
type Service struct {
	cfg Config
	ids messageIDs
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Rules == nil || cfg.SendLimit == nil {
		return nil, errors.New("dm: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Publisher == nil {
		cfg.Publisher = nopPublisher{}
	}
	return &Service{cfg: cfg}, nil
}

// now is UTC with the database's microsecond precision.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// Conversation is a conversation as the caller sees it.
type Conversation struct {
	ID     id.ID
	Other  id.ID // zero when the other party's account was deleted
	Status Status
	Unread int32
	// LastRead is the caller's read marker; zero if none.
	LastRead id.ID
	// PeerLastRead is the other party's marker, only in active
	// conversations.
	PeerLastRead  id.ID
	LastMessageAt time.Time
	CreatedAt     time.Time
}

// Message is a direct message.
type Message struct {
	ID             id.ID
	ConversationID id.ID
	SenderID       id.ID // zero when the sender's account was deleted
	// Body is empty for a deleted message ("Bu mesaj silindi").
	Body      string
	Deleted   bool
	CreatedAt time.Time
}

// SendInput is a message to send.
type SendInput struct {
	Recipient id.ID
	Body      string
	// ClientRequestID makes a retry return the first message (ADR-0005).
	ClientRequestID id.ID
}

// ListInput selects a page of messages, as in channel.ListInput.
type ListInput struct {
	PageSize  int32
	PageToken string
	After     id.ID
}

func invalid(field string) error { return apperr.Validation(field) }

func (s *Service) ready() error {
	if s.cfg.Cipher == nil {
		return apperr.ErrDirectMessagesUnavailable
	}
	return nil
}

// validBody checks a message body as channel messages are checked: valid
// UTF-8, 1–4000 characters, not only white space, no control characters
// other than tab and line breaks.
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

// pair returns a and b in conversation order (user_low_id < user_high_id,
// the byte order Postgres compares uuids in).
func pair(a, b id.ID) (low, high id.ID) {
	if bytes.Compare(a[:], b[:]) < 0 {
		return a, b
	}
	return b, a
}

func uuidOf(u pgtype.UUID) id.ID {
	if !u.Valid {
		return id.ID{}
	}
	return id.ID(u.Bytes)
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// convRow is a conversation row; the conversation queries return these
// columns.
type convRow = store.GetConversationForUserRow

// viewRow is a conversation with the caller's counters.
type viewRow = store.GetConversationViewRow

// msgRow is a message row; every message query returns these columns.
type msgRow = store.GetDirectMessageRow

// other is the party of r that is not user (zero if deleted).
func other(lowID, highID pgtype.UUID, user id.ID) id.ID {
	if uuidOf(lowID) == user {
		return uuidOf(highID)
	}
	return uuidOf(lowID)
}

// statusFor is r's state as user sees it; ok is false when user declined
// it (gone for them).
func statusFor(state string, requestedBy pgtype.UUID, user id.ID) (Status, bool) {
	mine := uuidOf(requestedBy) == user
	switch state {
	case stateActive:
		return StatusActive, true
	case stateRequest:
		if mine {
			return StatusRequestSent, true
		}
		return StatusRequestReceived, true
	case stateDeclined:
		if mine {
			return StatusRequestSent, true
		}
	}
	return 0, false
}

func viewOf(r viewRow, user id.ID) (Conversation, bool) {
	st, ok := statusFor(r.Status, r.RequestedBy, user)
	if !ok {
		return Conversation{}, false
	}
	c := Conversation{
		ID: uuidOf(r.ID), Other: other(r.UserLowID, r.UserHighID, user), Status: st, Unread: r.Unread,
		LastRead: uuidOf(r.LastReadMessageID), LastMessageAt: r.LastMessageAt.Time.UTC(), CreatedAt: r.CreatedAt.Time.UTC(),
	}
	if st == StatusActive {
		c.PeerLastRead = uuidOf(r.PeerLastReadMessageID)
	}
	return c, true
}

// view loads conversation for user as they see it.
func (s *Service) view(ctx context.Context, q *store.Queries, user, conversation id.ID) (Conversation, error) {
	r, err := q.GetConversationView(ctx, store.GetConversationViewParams{UserID: user.PG(), ID: conversation.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Conversation{}, apperr.ErrConversationNotFound
	case err != nil:
		return Conversation{}, fmt.Errorf("konuşma: %w", err)
	}
	c, ok := viewOf(r, user)
	if !ok {
		return Conversation{}, apperr.ErrConversationNotFound
	}
	return c, nil
}

// load returns the conversation row if user is a party who still sees it.
func load(ctx context.Context, q *store.Queries, user, conversation id.ID) (convRow, error) {
	r, err := q.GetConversationForUser(ctx, store.GetConversationForUserParams{ID: conversation.PG(), UserID: user.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return convRow{}, apperr.ErrConversationNotFound
	case err != nil:
		return convRow{}, fmt.Errorf("konuşma: %w", err)
	}
	if _, ok := statusFor(r.Status, r.RequestedBy, user); !ok {
		return convRow{}, apperr.ErrConversationNotFound
	}
	return r, nil
}

func parties(r convRow) [2]id.ID { return [2]id.ID{uuidOf(r.UserLowID), uuidOf(r.UserHighID)} }

func record(ctx context.Context, tx pgx.Tx, typ string, actor id.ID, subjectType string, subject id.ID, payload map[string]any) error {
	var p any // a nil map is not "no payload"
	if payload != nil {
		p = payload
	}
	if _, err := outbox.Write(ctx, tx, outbox.NewEvent{Type: typ, Actor: actor, SubjectType: subjectType, SubjectID: subject, Payload: p}); err != nil {
		return fmt.Errorf("olay: %w", err)
	}
	return nil
}

// messageOf converts a row; a body that does not decrypt is an error (wrong
// key or a tampered row), never something to show.
func (s *Service) messageOf(r msgRow) (Message, error) {
	m := Message{ID: uuidOf(r.ID), ConversationID: uuidOf(r.ConversationID), SenderID: uuidOf(r.SenderID), CreatedAt: r.CreatedAt.Time.UTC()}
	if r.DeletedAt.Valid || r.BodyEncrypted == nil {
		m.Deleted = r.DeletedAt.Valid
		return m, nil
	}
	body, err := s.cfg.Cipher.DecryptDirect(m.ID, m.ConversationID, channelcrypt.Sealed{Version: r.KeyVersion.Int16, Data: r.BodyEncrypted})
	if err != nil {
		return Message{}, fmt.Errorf("mesaj %s: %w", m.ID, err)
	}
	m.Body = body
	return m, nil
}

// messageIDs hands out strictly increasing message IDs in this process
// (paging and unread counts are by ID; chat runs as one replica, ADR-0005).
type messageIDs struct {
	mu   sync.Mutex
	last id.ID
}

func (g *messageIDs) next() id.ID {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.last = id.After(g.last)
	return g.last
}
