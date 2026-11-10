// Package realtime runs chat's live delivery over WebSocket (ADR-0004
// "WebSocket kimlik doğrulaması", ADR-0005 "Bağlantı", "Kanal mesajları";
// issue #82).
//
// A connection is opened with the user's access token (the gateway passes
// it on; chat verifies it again) and lives at most until that token
// expires. The token is kept only in the connection's memory: every
// question to core about the connection's user (may they see this channel,
// may they post) is asked with that very token, through core's single
// permission table (AuthorizeChannelAction, CLAUDE.md rule 5). chat holds no
// rule of its own and no service-identity exception is used (ADR-0013).
//
// The Hub keeps the live connections by user, session and subscribed
// channel. Live (live.go) checks permissions, subscribes, sends through
// channel.Service.Send (the same path as REST) and delivers committed
// changes to subscribers. Everything is in memory: chat runs as a single
// replica (ADR-0005 "Ölçekleme").
package realtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// RevokedFor is how long an ended session (or user) is refused new
// connections: the longest an access token it may still hold stays valid
// (ADR-0004).
const RevokedFor = 15 * time.Minute

const sweepEvery = time.Minute

// CloseReason says why the server ends a connection; the transport turns it
// into a WebSocket close code.
type CloseReason int

// Close reasons.
const (
	// CloseSessionEnded: the session was revoked, the user sanctioned or
	// deleted. The client must not reconnect with the same session.
	CloseSessionEnded CloseReason = iota + 1
	// CloseShutdown: the process is stopping; the client reconnects.
	CloseShutdown
)

// Event is one server frame: a type and its data, which the transport
// encodes as JSON.
type Event struct {
	Type string
	Data any
}

// Conn is a live connection as the hub sees it. Neither method may block.
type Conn interface {
	Close(reason CloseReason)
	Deliver(e Event)
}

// Admission and subscription errors.
var (
	// ErrTooManyConnections: the user already has MaxPerUser connections.
	ErrTooManyConnections = errors.New("kullanıcının bağlantı sınırı dolu")
	// ErrServerFull: the process holds MaxTotal connections or is stopping.
	ErrServerFull = errors.New("bağlantı kapasitesi dolu")
	// ErrTooManySubscriptions: the connection already follows
	// MaxSubscriptions channels.
	ErrTooManySubscriptions = errors.New("abonelik sınırı dolu")
	// errChanged: the tayfa changed while a subscription was being checked;
	// the check is repeated.
	errChanged = errors.New("tayfa değişti")
)

// HubConfig limits a Hub.
type HubConfig struct {
	// MaxTotal bounds the connections of the process (memory).
	MaxTotal int
	// MaxPerUser bounds one user's connections (devices and tabs).
	MaxPerUser int
	// MaxSubscriptions bounds the channels one connection follows.
	MaxSubscriptions int
	Now              func() time.Time // optional, for tests
}

// Member is one admitted connection. Its token is the access token the
// connection was opened with; it never leaves memory.
type Member struct {
	hub     *Hub
	user    id.ID
	session id.ID
	token   string
	conn    Conn
	// subs maps a subscribed channel to its tayfa; guarded by hub.mu.
	subs map[id.ID]id.ID
	once sync.Once
}

// User is the connection's user.
func (m *Member) User() id.ID { return m.user }

// Release removes the connection and its subscriptions; call it once the
// connection is gone. Calling it again does nothing.
func (m *Member) Release() {
	m.once.Do(func() {
		h := m.hub
		h.mu.Lock()
		defer h.mu.Unlock()
		if remove(h.byUser, m.user, m) {
			remove(h.bySession, m.session, m)
			for ch := range m.subs {
				remove(h.byChannel, ch, m)
			}
			m.subs = nil
			h.total--
		}
	})
}

// sub is one subscription, for rechecks.
type sub struct {
	m       *Member
	tayfa   id.ID
	channel id.ID
}

// Hub keeps the process's live connections.
type Hub struct {
	cfg HubConfig

	mu        sync.Mutex
	closed    bool
	total     int
	byUser    map[id.ID]map[*Member]struct{}
	bySession map[id.ID]map[*Member]struct{}
	byChannel map[id.ID]map[*Member]struct{}
	// gen counts changes per tayfa (membership, state, channel
	// visibility). A subscription checked against an older count is checked
	// again, so a change that lands during the check is not missed.
	gen map[id.ID]uint64
	// ended sessions and users, until when.
	endedSessions map[id.ID]time.Time
	endedUsers    map[id.ID]time.Time
}

// NewHub returns a Hub.
func NewHub(cfg HubConfig) (*Hub, error) {
	if cfg.MaxTotal <= 0 || cfg.MaxPerUser <= 0 || cfg.MaxSubscriptions <= 0 {
		return nil, errors.New("realtime: eksik hub yapılandırması")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Hub{
		cfg: cfg, byUser: map[id.ID]map[*Member]struct{}{}, bySession: map[id.ID]map[*Member]struct{}{},
		byChannel: map[id.ID]map[*Member]struct{}{}, gen: map[id.ID]uint64{},
		endedSessions: map[id.ID]time.Time{}, endedUsers: map[id.ID]time.Time{},
	}, nil
}

// Admit registers a connection of user's session opened with token.
func (h *Hub) Admit(user, session id.ID, token string, c Conn) (*Member, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.endedLocked(user, session):
		return nil, apperr.ErrSessionEnded
	case h.closed || h.total >= h.cfg.MaxTotal:
		return nil, ErrServerFull
	case len(h.byUser[user]) >= h.cfg.MaxPerUser:
		return nil, ErrTooManyConnections
	}
	m := &Member{hub: h, user: user, session: session, token: token, conn: c, subs: map[id.ID]id.ID{}}
	add(h.byUser, user, m)
	add(h.bySession, session, m)
	h.total++
	return m, nil
}

func add(idx map[id.ID]map[*Member]struct{}, k id.ID, m *Member) {
	set := idx[k]
	if set == nil {
		set = map[*Member]struct{}{}
		idx[k] = set
	}
	set[m] = struct{}{}
}

func remove(idx map[id.ID]map[*Member]struct{}, k id.ID, m *Member) bool {
	set := idx[k]
	if _, ok := set[m]; !ok {
		return false
	}
	delete(set, m)
	if len(set) == 0 {
		delete(idx, k)
	}
	return true
}

// generation returns the tayfa's change count.
func (h *Hub) generation(tayfa id.ID) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gen[tayfa]
}

// bump records a change in the tayfa.
func (h *Hub) bump(tayfa id.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gen[tayfa]++
}

// subscribe adds a subscription checked at generation g. It fails with
// errChanged if the tayfa changed since. Subscribing again is a no-op.
func (h *Hub) subscribe(m *Member, tayfa, channel id.ID, g uint64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case m.subs == nil:
		return ErrServerFull // released meanwhile
	case h.gen[tayfa] != g:
		return errChanged
	}
	if t, ok := m.subs[channel]; ok && t == tayfa {
		return nil
	}
	if len(m.subs) >= h.cfg.MaxSubscriptions {
		return ErrTooManySubscriptions
	}
	m.subs[channel] = tayfa
	add(h.byChannel, channel, m)
	return nil
}

// unsubscribe removes a subscription; it reports whether there was one.
func (h *Hub) unsubscribe(m *Member, tayfa, channel id.ID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := m.subs[channel]; !ok || t != tayfa {
		return false
	}
	delete(m.subs, channel)
	remove(h.byChannel, channel, m)
	return true
}

// subscribed reports whether m follows the channel.
func (h *Hub) subscribed(m *Member, tayfa, channel id.ID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := m.subs[channel]
	return ok && t == tayfa
}

// subsWhere returns the subscriptions in tayfa that match.
func (h *Hub) subsWhere(tayfa id.ID, match func(m *Member, channel id.ID) bool) []sub {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []sub
	for _, set := range h.byUser {
		for m := range set {
			for ch, t := range m.subs {
				if t == tayfa && match(m, ch) {
					out = append(out, sub{m: m, tayfa: t, channel: ch})
				}
			}
		}
	}
	return out
}

// userSubs returns user's subscriptions in tayfa.
func (h *Hub) userSubs(tayfa, user id.ID) []sub {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []sub
	for m := range h.byUser[user] {
		for ch, t := range m.subs {
			if t == tayfa {
				out = append(out, sub{m: m, tayfa: t, channel: ch})
			}
		}
	}
	return out
}

// channelSubs returns the subscriptions of one channel.
func (h *Hub) channelSubs(tayfa, channel id.ID) []sub {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []sub
	for m := range h.byChannel[channel] {
		if m.subs[channel] == tayfa {
			out = append(out, sub{m: m, tayfa: tayfa, channel: channel})
		}
	}
	return out
}

// publish delivers e to the channel's subscribers.
func (h *Hub) publish(tayfa, channel id.ID, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for m := range h.byChannel[channel] {
		if m.subs[channel] == tayfa {
			m.conn.Deliver(e)
		}
	}
}

// sendToUser delivers e to every connection of user.
func (h *Hub) sendToUser(user id.ID, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for m := range h.byUser[user] {
		m.conn.Deliver(e)
	}
}

// sendToSession delivers e to every connection of session (one device,
// ADR-0018: device = session).
func (h *Hub) sendToSession(session id.ID, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for m := range h.bySession[session] {
		m.conn.Deliver(e)
	}
}

// Ended reports whether the session (or the user) was ended less than
// RevokedFor ago.
func (h *Hub) Ended(user, session id.ID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.endedLocked(user, session)
}

func (h *Hub) endedLocked(user, session id.ID) bool {
	now := h.cfg.Now()
	if t, ok := h.endedSessions[session]; ok && now.Before(t) {
		return true
	}
	t, ok := h.endedUsers[user]
	return ok && now.Before(t)
}

// EndSession closes the session's connections and refuses it new ones for
// RevokedFor (session.revoked). It never fails; the error is for
// userevents.Sessions.
func (h *Hub) EndSession(_ context.Context, session id.ID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.endedSessions[session] = h.cfg.Now().Add(RevokedFor)
	for m := range h.bySession[session] {
		m.conn.Close(CloseSessionEnded)
	}
	return nil
}

// EndUser closes every connection of the user and refuses them new ones for
// RevokedFor (user.sanctioned, user.deleted).
func (h *Hub) EndUser(_ context.Context, user id.ID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.endedUsers[user] = h.cfg.Now().Add(RevokedFor)
	for m := range h.byUser[user] {
		m.conn.Close(CloseSessionEnded)
	}
	return nil
}

// Connections returns how many connections are open.
func (h *Hub) Connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.total
}

// Subscriptions returns how many subscriptions the connections hold.
func (h *Hub) Subscriptions() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, set := range h.byChannel {
		n += len(set)
	}
	return n
}

// sweep forgets ended sessions whose access tokens have expired.
func (h *Hub) sweep() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.cfg.Now()
	for _, m := range []map[id.ID]time.Time{h.endedSessions, h.endedUsers} {
		for k, t := range m {
			if !now.Before(t) {
				delete(m, k)
			}
		}
	}
}

// Run sweeps the ended sessions every minute until ctx is done, then closes
// every connection (CloseShutdown) and admits no more. Workers stop before
// the servers (service.Runtime), so clients are told to reconnect first.
func (h *Hub) Run(ctx context.Context) error {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			h.closed = true
			for _, set := range h.byUser {
				for m := range set {
					m.conn.Close(CloseShutdown)
				}
			}
			h.mu.Unlock()
			return nil
		case <-t.C:
			h.sweep()
		}
	}
}
