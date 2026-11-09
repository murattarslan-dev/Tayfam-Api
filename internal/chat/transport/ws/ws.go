// Package ws is chat's WebSocket transport (ADR-0005 "Bağlantı", "Mesaj
// protokolü"; issue #82). It opens a connection for a one-time ticket,
// registers it with the hub, keeps it alive with pings and carries JSON
// frames { "type", "id", "data" } both ways. It converts and limits; the
// rules live in the app layer.
//
// Limits on every connection: the Origin header must be absent (native
// clients) or one of the allowed web origins; a frame is at most
// MaxFrameBytes (larger closes with 1009); incoming frames are rate limited
// per user; the server pings every PingInterval and closes a connection
// that does not answer within PongTimeout; a client that does not read its
// frames fast enough (send buffer full) is closed with 4002 and catches up
// over REST after reconnecting (ADR-0005); after MaxLifetime the server
// closes with 4000 and the client reconnects with a new ticket.
//
// Nothing a client sends is logged, and the request URL (it carries the
// ticket) is never logged either.
package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/realtime"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

// Path is the connection's route; the gateway passes GET /v1/realtime here
// unchanged.
const Path = "/v1/realtime"

// Close codes the server uses besides the standard ones (1001 going away
// on shutdown, 1003 binary frame, 1008 too many frames, 1009 frame too
// large). The reason text is the same machine-readable word.
const (
	// CloseReconnect (4000, "reconnect"): MaxLifetime reached; reconnect
	// with a new ticket.
	CloseReconnect websocket.StatusCode = 4000
	// CloseSessionEnded (4001, "session_ended"): the session was revoked
	// or the account sanctioned or deleted; do not reconnect.
	CloseSessionEnded websocket.StatusCode = 4001
	// CloseSlowConsumer (4002, "slow_consumer"): the client did not read
	// its frames; reconnect and catch up over REST.
	CloseSlowConsumer websocket.StatusCode = 4002
)

// Defaults for Config's zero values.
const (
	DefaultPingInterval  = 30 * time.Second // ADR-0005
	DefaultPongTimeout   = 10 * time.Second
	DefaultWriteTimeout  = 10 * time.Second
	DefaultSendBuffer    = 64
	DefaultMaxFrameBytes = 64 << 10
	DefaultMaxLifetime   = time.Hour
	// maxFrameID bounds a client frame's id, echoed in replies.
	maxFrameID = 64
	// maxDenied closes a connection that keeps sending over its rate.
	maxDenied = 10
)

// Tickets redeems tickets (realtime.Tickets).
type Tickets interface {
	Redeem(ctx context.Context, v string) (realtime.Identity, error)
}

// Hub admits connections (realtime.Hub).
type Hub interface {
	Admit(user, session id.ID, c realtime.Conn) (release func(), err error)
}

// Config wires a Handler.
type Config struct {
	Tickets Tickets
	Hub     Hub
	Log     *slog.Logger
	// AllowedOrigins are the web origins that may open a connection, such
	// as https://tayfam.app (the gateway's CORS list). A request without
	// Origin (native clients) is allowed.
	AllowedOrigins []string
	// Frames limits incoming frames per user.
	Frames *ratelimit.Limiter

	PingInterval  time.Duration
	PongTimeout   time.Duration
	WriteTimeout  time.Duration
	SendBuffer    int
	MaxFrameBytes int64
	MaxLifetime   time.Duration
}

// Handler serves GET /v1/realtime.
type Handler struct {
	cfg     Config
	origins map[string]bool
}

// New returns a Handler.
func New(cfg Config) (*Handler, error) {
	if cfg.Tickets == nil || cfg.Hub == nil || cfg.Log == nil || cfg.Frames == nil {
		return nil, errors.New("ws: eksik yapılandırma")
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = DefaultPingInterval
	}
	if cfg.PongTimeout <= 0 {
		cfg.PongTimeout = DefaultPongTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = DefaultWriteTimeout
	}
	if cfg.SendBuffer <= 0 {
		cfg.SendBuffer = DefaultSendBuffer
	}
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = DefaultMaxFrameBytes
	}
	if cfg.MaxLifetime <= 0 {
		cfg.MaxLifetime = DefaultMaxLifetime
	}
	h := &Handler{cfg: cfg, origins: map[string]bool{}}
	for _, o := range cfg.AllowedOrigins {
		h.origins[o] = true
	}
	return h, nil
}

// Register adds the route to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET "+Path, h)
	mux.HandleFunc("/", httpx.NotFound)
}

// AllowedOrigin reports whether a request with this Origin header may open
// a connection: none (a native client) or exactly one of origins.
func AllowedOrigin(origins map[string]bool, origin string) bool {
	return origin == "" || origins[origin]
}

// isUpgrade reports whether r asks for a WebSocket.
func isUpgrade(r *http.Request) bool {
	for v := range strings.SplitSeq(r.Header.Get("Upgrade"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), "websocket") {
			return true
		}
	}
	return false
}

// ServeHTTP checks the Origin and the ticket, then upgrades. The cheap
// checks come first, so a cross-site page or a plain GET cannot use up a
// ticket.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !AllowedOrigin(h.origins, r.Header.Get("Origin")) {
		httpx.WriteProblem(w, r, http.StatusForbidden, "forbidden-origin", "Forbidden", "")
		return
	}
	if !isUpgrade(r) {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	who, err := h.cfg.Tickets.Redeem(r.Context(), r.URL.Query().Get("ticket"))
	switch {
	case errors.Is(err, realtime.ErrInvalidTicket):
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "invalid-ticket", "Unauthorized", "")
		return
	case err != nil:
		h.cfg.Log.WarnContext(r.Context(), "bilet doğrulanamadı", slog.String("hata", err.Error()))
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
		return
	}
	c := newConn(h.cfg, who)
	release, err := h.cfg.Hub.Admit(who.User, who.Session, c)
	switch {
	case errors.Is(err, apperr.ErrSessionEnded):
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "invalid-ticket", "Unauthorized", "")
		return
	case errors.Is(err, realtime.ErrTooManyConnections):
		httpx.WriteProblem(w, r, http.StatusTooManyRequests, "rate-limited", "Too Many Requests", "")
		return
	case err != nil:
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
		return
	}
	defer release()
	patterns := make([]string, 0, len(h.origins))
	for o := range h.origins {
		patterns = append(patterns, o)
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Checked above as well; the library checks again.
		OriginPatterns: patterns,
		// No permessage-deflate: compression of attacker-influenced frames
		// next to secrets (CRIME/BREACH) and its memory cost are not worth
		// it for small JSON frames.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return // Accept has answered
	}
	c.run(ws)
}

// frame is the JSON envelope of every frame (ADR-0005).
type frame struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// closeReq is a request to end the connection.
type closeReq struct {
	code   websocket.StatusCode
	reason string
}

// conn is one connection. Close and send may be called from any goroutine.
type conn struct {
	cfg Config
	who realtime.Identity
	out chan []byte
	// end receives the first close request; later ones are dropped.
	end chan closeReq
}

func newConn(cfg Config, who realtime.Identity) *conn {
	return &conn{cfg: cfg, who: who, out: make(chan []byte, cfg.SendBuffer), end: make(chan closeReq, 1)}
}

// Close implements realtime.Conn.
func (c *conn) Close(reason realtime.CloseReason) {
	switch reason {
	case realtime.CloseSessionEnded:
		c.requestClose(CloseSessionEnded, "session_ended")
	default:
		c.requestClose(websocket.StatusGoingAway, "shutdown")
	}
}

func (c *conn) requestClose(code websocket.StatusCode, reason string) {
	select {
	case c.end <- closeReq{code: code, reason: reason}:
	default:
	}
}

// send queues a frame; a full buffer closes the connection (ADR-0005).
func (c *conn) send(f []byte) {
	select {
	case c.out <- f:
	default:
		c.requestClose(CloseSlowConsumer, "slow_consumer")
	}
}

func (c *conn) sendFrame(typ, frameID string, data any) {
	f := frame{Type: typ, ID: frameID}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return
		}
		f.Data = b
	}
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	c.send(b)
}

type errorData struct {
	Code string `json:"code"`
}

type readyData struct {
	HeartbeatSeconds int `json:"heartbeat_seconds"`
}

// run serves the connection until it ends. The request's context is not
// used: after the upgrade it belongs to the hijacked HTTP request.
func (c *conn) run(ws *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws.SetReadLimit(c.cfg.MaxFrameBytes)

	var wg sync.WaitGroup
	wg.Go(func() { c.writeLoop(ctx, cancel, ws) })
	wg.Go(func() { c.pingLoop(ctx, cancel, ws) })
	wg.Go(func() { c.closeLoop(ctx, cancel, ws) })

	c.sendFrame("ready", "", readyData{HeartbeatSeconds: int(c.cfg.PingInterval.Seconds())})
	c.readLoop(ctx, ws)
	cancel()
	wg.Wait()
	_ = ws.CloseNow()
}

func (c *conn) readLoop(ctx context.Context, ws *websocket.Conn) {
	denied := 0
	key := c.who.User.String()
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			c.requestClose(websocket.StatusUnsupportedData, "binary_frame")
			continue
		}
		if !c.cfg.Frames.Allow(key) {
			if denied++; denied >= maxDenied {
				c.requestClose(websocket.StatusPolicyViolation, "rate_limited")
				continue
			}
			c.sendFrame("error", "", errorData{Code: "rate_limited"})
			continue
		}
		denied = 0
		c.handle(data)
	}
}

// handle answers one client frame. No client frame type is served yet;
// channel subscriptions and sending come with the next step of issue #82.
func (c *conn) handle(data []byte) {
	var f frame
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil || f.Type == "" {
		c.sendFrame("error", "", errorData{Code: "invalid_frame"})
		return
	}
	if len(f.ID) > maxFrameID || !utf8.ValidString(f.ID) || strings.ContainsFunc(f.ID, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		c.sendFrame("error", "", errorData{Code: "invalid_frame"})
		return
	}
	c.sendFrame("error", f.ID, errorData{Code: "unsupported_type"})
}

func (c *conn) writeLoop(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.out:
			wctx, wcancel := context.WithTimeout(ctx, c.cfg.WriteTimeout)
			err := ws.Write(wctx, websocket.MessageText, b)
			wcancel()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

// pingLoop pings every PingInterval; no pong within PongTimeout ends the
// connection (ADR-0005: "yanıt vermeyen bağlantı kapatılır").
func (c *conn) pingLoop(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn) {
	t := time.NewTicker(c.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, pcancel := context.WithTimeout(ctx, c.cfg.PongTimeout)
			err := ws.Ping(pctx)
			pcancel()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

// closeLoop ends the connection on the first close request or when its
// lifetime is over. The close handshake needs the read loop running, so it
// happens here and the read loop ends with it.
func (c *conn) closeLoop(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn) {
	life := time.NewTimer(c.cfg.MaxLifetime)
	defer life.Stop()
	var req closeReq
	select {
	case <-ctx.Done():
		return
	case req = <-c.end:
	case <-life.C:
		req = closeReq{code: CloseReconnect, reason: "reconnect"}
	}
	_ = ws.Close(req.code, req.reason)
	cancel()
}
