// Package realtime runs chat's real-time connections (ADR-0004
// "WebSocket kimlik doğrulaması", ADR-0005 "Bağlantı"; issue #82): the
// one-time tickets that open them and the hub that keeps them by user and
// session, so that a revoked session, a sanction or an account deletion
// closes them at once.
//
// A ticket is issued to the caller of a gRPC request, whose access token
// chat has verified itself (auth.Guard), and opens one WebSocket within
// TicketTTL. The access token never travels in a URL; the database keeps
// only the ticket's SHA-256, so reading it gives no way in. A connection
// lives on after the access token that bought its ticket expires
// (ADR-0004); it ends when the session ends.
//
// The WebSocket itself is transport (internal/chat/transport/ws); this
// package knows nothing about frames.
package realtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

const (
	// TicketTTL is how long a ticket opens a connection (ADR-0004).
	TicketTTL = 30 * time.Second
	// MaxLiveTickets bounds one user's unused tickets.
	MaxLiveTickets = 10
	// RevokedFor is how long an ended session (or user) is refused new
	// tickets and connections: the longest an access token it may still
	// hold stays valid (15 minutes, ADR-0004), plus a ticket's life.
	RevokedFor = 15*time.Minute + TicketTTL

	ticketBytes = 32
	sweepEvery  = time.Minute
)

// ErrInvalidTicket: the ticket is malformed, unknown, used or expired. The
// cases are indistinguishable on purpose.
var ErrInvalidTicket = errors.New("geçersiz bilet")

// Ticket is a newly issued ticket. Value is shown to the caller once and
// never stored.
type Ticket struct {
	Value     string
	ExpiresAt time.Time
}

// Identity is who a redeemed ticket belongs to.
type Identity struct {
	User    id.ID
	Session id.ID
}

// Ended tells whether a session was ended a short while ago (Hub.Ended).
type Ended interface {
	Ended(user, session id.ID) bool
}

// TicketsConfig wires Tickets.
type TicketsConfig struct {
	Pool  *pgxpool.Pool
	Log   *slog.Logger
	Ended Ended
	Now   func() time.Time // optional, for tests
}

// Tickets issues and redeems tickets.
type Tickets struct {
	cfg TicketsConfig
}

// NewTickets returns Tickets.
func NewTickets(cfg TicketsConfig) (*Tickets, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Ended == nil {
		return nil, errors.New("realtime: eksik bilet yapılandırması")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Tickets{cfg: cfg}, nil
}

// now is UTC with the database's microsecond precision.
func (t *Tickets) now() time.Time { return t.cfg.Now().UTC().Truncate(time.Microsecond) }

func ts(v time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: v, Valid: true} }

func hash(raw []byte) []byte {
	h := sha256.Sum256(raw)
	return h[:]
}

// Issue returns a new ticket for the caller's session. user and session
// come from the verified access token.
func (t *Tickets) Issue(ctx context.Context, user, session id.ID) (Ticket, error) {
	if t.cfg.Ended.Ended(user, session) {
		return Ticket{}, apperr.ErrSessionEnded
	}
	now := t.now()
	q := store.New(t.cfg.Pool)
	// Two requests at the same moment may both pass the count; the limit is
	// against piling up tickets, one over it is harmless.
	n, err := q.CountLiveRealtimeTickets(ctx, store.CountLiveRealtimeTicketsParams{UserID: user.PG(), Now: ts(now)})
	if err != nil {
		return Ticket{}, fmt.Errorf("biletler: %w", err)
	}
	if n >= MaxLiveTickets {
		return Ticket{}, apperr.ErrTicketLimit
	}
	raw := make([]byte, ticketBytes)
	if _, err := rand.Read(raw); err != nil {
		return Ticket{}, fmt.Errorf("bilet üretilemedi: %w", err)
	}
	exp := now.Add(TicketTTL)
	if err := q.CreateRealtimeTicket(ctx, store.CreateRealtimeTicketParams{
		ID: id.New().PG(), TokenHash: hash(raw), UserID: user.PG(), SessionID: session.PG(),
		CreatedAt: ts(now), ExpiresAt: ts(exp),
	}); err != nil {
		return Ticket{}, fmt.Errorf("bilet yazılamadı: %w", err)
	}
	return Ticket{Value: base64.RawURLEncoding.EncodeToString(raw), ExpiresAt: exp}, nil
}

// ValidTicketFormat reports whether v can be a ticket at all, without a
// database round trip.
func ValidTicketFormat(v string) bool {
	if len(v) != base64.RawURLEncoding.EncodedLen(ticketBytes) {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(v)
	return err == nil
}

// Redeem uses up a ticket and returns whose it is. A ticket works once; a
// malformed, unknown, used or expired one, or one of a session that ended
// meanwhile, is ErrInvalidTicket.
func (t *Tickets) Redeem(ctx context.Context, v string) (Identity, error) {
	if !ValidTicketFormat(v) {
		return Identity{}, ErrInvalidTicket
	}
	raw, _ := base64.RawURLEncoding.DecodeString(v)
	r, err := store.New(t.cfg.Pool).RedeemRealtimeTicket(ctx, store.RedeemRealtimeTicketParams{TokenHash: hash(raw), Now: ts(t.now())})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Identity{}, ErrInvalidTicket
	case err != nil:
		return Identity{}, fmt.Errorf("bilet: %w", err)
	}
	who := Identity{User: id.ID(r.UserID.Bytes), Session: id.ID(r.SessionID.Bytes)}
	if t.cfg.Ended.Ended(who.User, who.Session) {
		return Identity{}, ErrInvalidTicket
	}
	return who, nil
}

// Purge deletes expired tickets and returns how many.
func (t *Tickets) Purge(ctx context.Context) (int64, error) {
	n, err := store.New(t.cfg.Pool).DeleteExpiredRealtimeTickets(ctx, ts(t.now()))
	if err != nil {
		return 0, fmt.Errorf("süresi dolan biletler: %w", err)
	}
	return n, nil
}

// RunPurge runs Purge every minute until ctx is done. Running it in more
// than one pod is harmless.
func (t *Tickets) RunPurge(ctx context.Context) error {
	tk := time.NewTicker(sweepEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
			if _, err := t.Purge(ctx); err != nil && ctx.Err() == nil {
				t.cfg.Log.WarnContext(ctx, "süresi dolan biletler silinemedi", slog.String("hata", err.Error()))
			}
		}
	}
}

// Cleanup removes a deleted user's tickets inside the deletion's
// transaction (userevents.Cleanup).
func Cleanup(ctx context.Context, q *store.Queries, user id.ID) error {
	if err := q.DeleteUserRealtimeTickets(ctx, user.PG()); err != nil {
		return fmt.Errorf("bilet temizliği: %w", err)
	}
	return nil
}

// Sessions ends sessions in chat: their connections close at once and
// their unused tickets are removed (session.revoked, user.sanctioned,
// user.deleted). Both methods are idempotent.
type Sessions struct {
	Pool *pgxpool.Pool
	Hub  *Hub
}

// EndSession ends one session.
func (s Sessions) EndSession(ctx context.Context, session id.ID) error {
	// Memory first: the connections close even if the database is down.
	s.Hub.EndSession(session)
	if err := store.New(s.Pool).DeleteSessionRealtimeTickets(ctx, session.PG()); err != nil {
		return fmt.Errorf("oturum biletleri: %w", err)
	}
	return nil
}

// EndUser ends every session of user.
func (s Sessions) EndUser(ctx context.Context, user id.ID) error {
	s.Hub.EndUser(user)
	if err := store.New(s.Pool).DeleteUserRealtimeTickets(ctx, user.PG()); err != nil {
		return fmt.Errorf("kullanıcı biletleri: %w", err)
	}
	return nil
}
