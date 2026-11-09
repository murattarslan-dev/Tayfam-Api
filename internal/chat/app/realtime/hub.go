package realtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

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

// Conn is a live connection as the hub sees it.
type Conn interface {
	// Close ends the connection with reason. It must not block.
	Close(reason CloseReason)
}

// Admission errors.
var (
	// ErrTooManyConnections: the user already has MaxPerUser connections.
	ErrTooManyConnections = errors.New("kullanıcının bağlantı sınırı dolu")
	// ErrServerFull: the process holds MaxTotal connections or is stopping.
	ErrServerFull = errors.New("bağlantı kapasitesi dolu")
)

// HubConfig limits a Hub.
type HubConfig struct {
	// MaxTotal bounds the connections of the process (memory).
	MaxTotal int
	// MaxPerUser bounds one user's connections (devices and tabs).
	MaxPerUser int
	Now        func() time.Time // optional, for tests
}

// member is one admitted connection.
type member struct {
	user, session id.ID
	conn          Conn
}

// Hub keeps the process's live connections by user and session, closes them
// when a session ends and remembers ended sessions for RevokedFor, so a
// still valid access token cannot open a new connection for them.
//
// Everything is in memory: chat runs as a single replica (ADR-0005
// "Ölçekleme"). A restart closes every connection anyway; the list of ended
// sessions is lost with it, which leaves at most RevokedFor in which a
// stolen, still valid access token of a revoked session could open a
// connection again. A REST call with the same token is possible in that
// window too (ADR-0004).
type Hub struct {
	cfg HubConfig

	mu        sync.Mutex
	closed    bool
	total     int
	byUser    map[id.ID]map[*member]struct{}
	bySession map[id.ID]map[*member]struct{}
	// ended sessions and users, until when.
	endedSessions map[id.ID]time.Time
	endedUsers    map[id.ID]time.Time
}

// NewHub returns a Hub.
func NewHub(cfg HubConfig) (*Hub, error) {
	if cfg.MaxTotal <= 0 || cfg.MaxPerUser <= 0 {
		return nil, errors.New("realtime: eksik hub yapılandırması")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Hub{
		cfg: cfg, byUser: map[id.ID]map[*member]struct{}{}, bySession: map[id.ID]map[*member]struct{}{},
		endedSessions: map[id.ID]time.Time{}, endedUsers: map[id.ID]time.Time{},
	}, nil
}

// Admit registers c for user and session. The returned function removes it
// again; call it once the connection is gone.
func (h *Hub) Admit(user, session id.ID, c Conn) (release func(), err error) {
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
	m := &member{user: user, session: session, conn: c}
	add(h.byUser, user, m)
	add(h.bySession, session, m)
	h.total++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if remove(h.byUser, user, m) {
				remove(h.bySession, session, m)
				h.total--
			}
		})
	}, nil
}

func add(idx map[id.ID]map[*member]struct{}, k id.ID, m *member) {
	set := idx[k]
	if set == nil {
		set = map[*member]struct{}{}
		idx[k] = set
	}
	set[m] = struct{}{}
}

func remove(idx map[id.ID]map[*member]struct{}, k id.ID, m *member) bool {
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
// RevokedFor (session.revoked).
func (h *Hub) EndSession(session id.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.endedSessions[session] = h.cfg.Now().Add(RevokedFor)
	h.closeLocked(h.bySession[session], CloseSessionEnded)
}

// EndUser closes every connection of the user and refuses them new ones for
// RevokedFor (user.sanctioned, user.deleted).
func (h *Hub) EndUser(user id.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.endedUsers[user] = h.cfg.Now().Add(RevokedFor)
	h.closeLocked(h.byUser[user], CloseSessionEnded)
}

// closeLocked asks the connections to close; each one releases itself when
// its handler returns.
func (h *Hub) closeLocked(set map[*member]struct{}, reason CloseReason) {
	for m := range set {
		m.conn.Close(reason)
	}
}

// Connections returns how many connections are open (for tests and
// metrics).
func (h *Hub) Connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.total
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

// Run sweeps the ended sessions every sweepEvery until ctx is done, then
// closes every connection (CloseShutdown) and admits no more. It is the
// service's worker; workers stop before the servers (service.Runtime), so
// clients are told to reconnect before the listener closes.
func (h *Hub) Run(ctx context.Context) error {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			h.closed = true
			for _, set := range h.byUser {
				h.closeLocked(set, CloseShutdown)
			}
			h.mu.Unlock()
			return nil
		case <-t.C:
			h.sweep()
		}
	}
}
