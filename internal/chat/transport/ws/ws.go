// Package ws is chat's WebSocket transport (ADR-0005 "Bağlantı", "Mesaj
// protokolü"; issue #82). It opens a connection for a verified access
// token, registers it with the hub and carries JSON frames
// { "type", "id", "data" } both ways. It converts and limits; the rules
// live in the app layer (realtime.Live, channel.Service).
//
// Authentication: the gateway passes the user's access token in the
// Authorization header, as the mobile client sent it; chat verifies the
// signature itself (CLAUDE.md rule 8) and keeps
// the token only in the connection's memory, to ask core with it. The
// connection closes when the token expires (4003); the client reconnects
// with a fresh one.
//
// Limits on every connection: a request with an Origin header is refused
// (only the mobile apps connect, and they send none; browsers always do;
// ADR-0017); a frame is at most
// MaxFrameBytes (larger closes with 1009); incoming frames are rate limited
// per user (Frames; "rate_limited"), and sending a message also counts
// against the per-user message rate the app layer shares with REST
// (issue #101; an error frame "rate_limited" for that frame, the
// connection stays open); the server pings every PingInterval and closes a
// connection that does not answer within PongTimeout; a client that does not read its
// frames fast enough (send buffer full) is closed with 4002 and catches up
// over REST after reconnecting (ADR-0005).
//
// Nothing a client sends is logged, message bodies included, and neither is
// the token.
package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/realtime"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

// Path is the connection's route; the gateway passes GET /v1/realtime here.
const Path = "/v1/realtime"

// Close codes the server uses besides the standard ones (1001 going away
// on shutdown, 1003 binary frame, 1008 too many frames, 1009 frame too
// large). The reason text is the same machine-readable word.
const (
	// CloseSessionEnded (4001, "session_ended"): the session was revoked
	// or the account sanctioned or deleted; sign in again.
	CloseSessionEnded websocket.StatusCode = 4001
	// CloseSlowConsumer (4002, "slow_consumer"): the client did not read
	// its frames; reconnect and catch up over REST.
	CloseSlowConsumer websocket.StatusCode = 4002
	// CloseTokenExpired (4003, "token_expired"): the access token the
	// connection was opened with expired; refresh it and reconnect.
	CloseTokenExpired websocket.StatusCode = 4003
)

// Defaults for Config's zero values.
const (
	DefaultPingInterval  = 30 * time.Second // ADR-0005
	DefaultPongTimeout   = 10 * time.Second
	DefaultWriteTimeout  = 10 * time.Second
	DefaultSendBuffer    = 64
	DefaultMaxFrameBytes = 64 << 10
	DefaultCallTimeout   = 10 * time.Second
	// maxFrameID bounds a client frame's id, echoed in replies.
	maxFrameID = 64
	// maxDenied closes a connection that keeps sending over its rate.
	maxDenied = 10
	// maxTokenBytes bounds the access token read from the header.
	maxTokenBytes = 4096
	// inbox holds client frames waiting for their answer; a full inbox
	// answers "busy".
	inbox = 8
)

// Verifier checks an access token (auth.Verifier).
type Verifier interface {
	Verify(ctx context.Context, token string) (auth.Claims, error)
}

// Hub admits connections (realtime.Hub).
type Hub interface {
	Admit(user, session id.ID, token string, c realtime.Conn) (*realtime.Member, error)
}

// Live is the app layer of a connection (realtime.Live).
type Live interface {
	Subscribe(ctx context.Context, m *realtime.Member, tayfa, ch id.ID) error
	Unsubscribe(m *realtime.Member, tayfa, ch id.ID) bool
	Send(ctx context.Context, m *realtime.Member, tayfa, ch id.ID, in channel.SendInput) (channel.Message, error)
	SendDirect(ctx context.Context, m *realtime.Member, in dm.SendInput) (dm.Message, dm.Conversation, error)
	AckEnvelopes(ctx context.Context, m *realtime.Member, ids []id.ID) error
}

// Config wires a Handler.
type Config struct {
	Verifier Verifier
	Hub      Hub
	Live     Live
	Log      *slog.Logger
	// Frames limits incoming frames per user.
	Frames *ratelimit.Limiter
	Now    func() time.Time // optional, for tests

	PingInterval  time.Duration
	PongTimeout   time.Duration
	WriteTimeout  time.Duration
	CallTimeout   time.Duration
	SendBuffer    int
	MaxFrameBytes int64
}

// Handler serves GET /v1/realtime.
type Handler struct {
	cfg Config
}

// New returns a Handler.
func New(cfg Config) (*Handler, error) {
	if cfg.Verifier == nil || cfg.Hub == nil || cfg.Live == nil || cfg.Log == nil || cfg.Frames == nil {
		return nil, errors.New("ws: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
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
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultCallTimeout
	}
	if cfg.SendBuffer <= 0 {
		cfg.SendBuffer = DefaultSendBuffer
	}
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = DefaultMaxFrameBytes
	}
	return &Handler{cfg: cfg}, nil
}

// Register adds the route to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET "+Path, h)
	mux.HandleFunc("/", httpx.NotFound)
}

func isUpgrade(r *http.Request) bool {
	for v := range strings.SplitSeq(r.Header.Get("Upgrade"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), "websocket") {
			return true
		}
	}
	return false
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[len("Bearer "):])
}

// ServeHTTP checks the Origin and the token, then upgrades. Any Origin is
// refused: no browser may open a connection, whatever credentials it
// carries (ADR-0017). The gateway refuses it too (ADR-0011).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Origin")) > 0 {
		httpx.WriteProblem(w, r, http.StatusForbidden, "forbidden-origin", "Forbidden", "")
		return
	}
	if !isUpgrade(r) {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	token := bearer(r)
	if token == "" || len(token) > maxTokenBytes {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	}
	claims, err := h.cfg.Verifier.Verify(r.Context(), token)
	switch {
	case errors.Is(err, auth.ErrInvalidToken):
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	case err != nil:
		h.cfg.Log.WarnContext(r.Context(), "token doğrulanamadı: anahtar alınamadı")
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
		return
	}
	user, okU := id.Parse(claims.Subject)
	session, okS := id.Parse(claims.SessionID)
	if !okU || !okS {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	}
	c := newConn(h.cfg, claims.ExpiresAt)
	m, err := h.cfg.Hub.Admit(user, session, token, c)
	switch {
	case errors.Is(err, apperr.ErrSessionEnded):
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	case errors.Is(err, realtime.ErrTooManyConnections):
		httpx.WriteProblem(w, r, http.StatusTooManyRequests, "rate-limited", "Too Many Requests", "")
		return
	case err != nil:
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
		return
	}
	defer m.Release()
	c.m = m
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// No OriginPatterns: the library refuses a cross-origin request
		// too, should one get past the check above.
		// No permessage-deflate: compression of attacker-influenced frames
		// next to message bodies (CRIME/BREACH) and its memory cost are not
		// worth it for small JSON frames.
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

type closeReq struct {
	code   websocket.StatusCode
	reason string
}

// conn is one connection. Close, Deliver and send may be called from any
// goroutine.
type conn struct {
	cfg     Config
	m       *realtime.Member
	expires time.Time
	out     chan []byte
	in      chan frame
	// end receives the first close request; later ones are dropped.
	end chan closeReq
}

func newConn(cfg Config, expires time.Time) *conn {
	return &conn{
		cfg: cfg, expires: expires, out: make(chan []byte, cfg.SendBuffer),
		in: make(chan frame, inbox), end: make(chan closeReq, 1),
	}
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

// Deliver implements realtime.Conn.
func (c *conn) Deliver(e realtime.Event) { c.sendFrame(e.Type, "", e.Data) }

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
	// Field names the invalid field of invalid_request.
	Field string `json:"field,omitempty"`
	// RetryAfterSeconds comes with slow_mode.
	RetryAfterSeconds int64 `json:"retry_after_seconds,omitempty"`
}

type readyData struct {
	HeartbeatSeconds int       `json:"heartbeat_seconds"`
	ExpiresAt        time.Time `json:"expires_at"`
}

func (c *conn) fail(frameID, code string) { c.sendFrame("error", frameID, errorData{Code: code}) }

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
	wg.Go(func() { c.work(ctx) })

	c.sendFrame("ready", "", readyData{HeartbeatSeconds: int(c.cfg.PingInterval.Seconds()), ExpiresAt: c.expires.UTC()})
	c.readLoop(ctx, ws)
	cancel()
	wg.Wait()
	_ = ws.CloseNow()
}

// readLoop reads frames and hands them to work, so that pongs and close
// frames keep being read while core answers.
func (c *conn) readLoop(ctx context.Context, ws *websocket.Conn) {
	denied := 0
	key := c.m.User().String()
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
			c.fail("", "rate_limited")
			continue
		}
		denied = 0
		f, ok := decodeFrame(data)
		if !ok {
			c.fail("", "invalid_frame")
			continue
		}
		select {
		case c.in <- f:
		default:
			c.fail(f.ID, "busy")
		}
	}
}

func decodeFrame(data []byte) (frame, bool) {
	var f frame
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil || f.Type == "" {
		return frame{}, false
	}
	if len(f.ID) > maxFrameID || !utf8.ValidString(f.ID) || strings.ContainsFunc(f.ID, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return frame{}, false
	}
	return f, true
}

func (c *conn) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-c.in:
			cctx, cancel := context.WithTimeout(ctx, c.cfg.CallTimeout)
			c.handle(cctx, f)
			cancel()
		}
	}
}

// Client frame data.
type channelRef struct {
	TayfaID   string `json:"tayfa_id"`
	ChannelID string `json:"channel_id"`
}

type sendData struct {
	TayfaID         string `json:"tayfa_id"`
	ChannelID       string `json:"channel_id"`
	Body            string `json:"body"`
	ClientRequestID string `json:"client_request_id"`
}

type dmSendData struct {
	RecipientUserID string `json:"recipient_user_id"`
	Body            string `json:"body"`
	ClientRequestID string `json:"client_request_id"`
}

// envelopeAckData acknowledges end-to-end envelopes of the connection's
// device (issue #127).
type envelopeAckData struct {
	EnvelopeIDs []string `json:"envelope_ids"`
}

// dmSentData answers dm.send: the message and the conversation's status as
// the sender sees it.
type dmSentData struct {
	realtime.DMData
	Status string `json:"conversation_status"`
}

// dmStatuses are the conversation statuses in frames (the REST names).
var dmStatuses = map[dm.Status]string{
	dm.StatusActive:          "active",
	dm.StatusRequestSent:     "request_sent",
	dm.StatusRequestReceived: "request_received",
}

func decodeData(raw json.RawMessage, v any) bool {
	if len(raw) == 0 {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil
}

func (r channelRef) ids() (id.ID, id.ID, string) {
	tayfa, ok := id.Parse(r.TayfaID)
	if !ok {
		return id.ID{}, id.ID{}, "tayfa_id"
	}
	ch, ok := id.Parse(r.ChannelID)
	if !ok {
		return id.ID{}, id.ID{}, "channel_id"
	}
	return tayfa, ch, ""
}

func (c *conn) invalid(frameID, field string) {
	c.sendFrame("error", frameID, errorData{Code: "invalid_request", Field: field})
}

// handle answers one client frame:
//
//	subscribe   { tayfa_id, channel_id }  → subscribed
//	unsubscribe { tayfa_id, channel_id }  → unsubscribed (reason requested)
//	send        { tayfa_id, channel_id, body, client_request_id } → sent
//	dm.send     { recipient_user_id, body, client_request_id }     → dm.sent
//	dm.envelope_ack { envelope_ids }                                → dm.envelope_acked
//
// End-to-end messages are sent over REST only (their envelopes exceed a
// frame); the WebSocket delivers them (dm.envelope) and takes the
// acknowledgements (ADR-0018 item 3).
func (c *conn) handle(ctx context.Context, f frame) {
	switch f.Type {
	case "subscribe", "unsubscribe":
		var d channelRef
		if !decodeData(f.Data, &d) {
			c.invalid(f.ID, "data")
			return
		}
		tayfa, ch, bad := d.ids()
		if bad != "" {
			c.invalid(f.ID, bad)
			return
		}
		ref := realtime.SubscriptionData{TayfaID: tayfa.String(), ChannelID: ch.String()}
		if f.Type == "unsubscribe" {
			c.cfg.Live.Unsubscribe(c.m, tayfa, ch)
			ref.Reason = realtime.ReasonRequested
			c.sendFrame(realtime.EventUnsubscribed, f.ID, ref)
			return
		}
		if err := c.cfg.Live.Subscribe(ctx, c.m, tayfa, ch); err != nil {
			c.appError(ctx, f.ID, err)
			return
		}
		c.sendFrame("subscribed", f.ID, ref)
	case "send":
		var d sendData
		if !decodeData(f.Data, &d) {
			c.invalid(f.ID, "data")
			return
		}
		tayfa, ch, bad := channelRef{TayfaID: d.TayfaID, ChannelID: d.ChannelID}.ids()
		if bad != "" {
			c.invalid(f.ID, bad)
			return
		}
		req, ok := id.Parse(d.ClientRequestID)
		if !ok {
			c.invalid(f.ID, "client_request_id")
			return
		}
		msg, err := c.cfg.Live.Send(ctx, c.m, tayfa, ch, channel.SendInput{Body: d.Body, ClientRequestID: req})
		if err != nil {
			c.appError(ctx, f.ID, err)
			return
		}
		c.sendFrame("sent", f.ID, realtime.MessageFrom(tayfa, msg))
	case "dm.send":
		var d dmSendData
		if !decodeData(f.Data, &d) {
			c.invalid(f.ID, "data")
			return
		}
		recipient, ok := id.Parse(d.RecipientUserID)
		if !ok {
			c.invalid(f.ID, "recipient_user_id")
			return
		}
		req, ok := id.Parse(d.ClientRequestID)
		if !ok {
			c.invalid(f.ID, "client_request_id")
			return
		}
		msg, conv, err := c.cfg.Live.SendDirect(ctx, c.m, dm.SendInput{Recipient: recipient, Body: d.Body, ClientRequestID: req})
		if err != nil {
			c.appError(ctx, f.ID, err)
			return
		}
		c.sendFrame("dm.sent", f.ID, dmSentData{DMData: realtime.DMFrom(msg), Status: dmStatuses[conv.Status]})
	case "dm.envelope_ack":
		var d envelopeAckData
		if !decodeData(f.Data, &d) {
			c.invalid(f.ID, "data")
			return
		}
		list := make([]id.ID, 0, len(d.EnvelopeIDs))
		for _, v := range d.EnvelopeIDs {
			e, ok := id.Parse(v)
			if !ok {
				c.invalid(f.ID, "envelope_ids")
				return
			}
			list = append(list, e)
		}
		if err := c.cfg.Live.AckEnvelopes(ctx, c.m, list); err != nil {
			c.appError(ctx, f.ID, err)
			return
		}
		c.sendFrame("dm.envelope_acked", f.ID, envelopeAckData{EnvelopeIDs: d.EnvelopeIDs})
	default:
		c.fail(f.ID, "unsupported_type")
	}
}

// appError answers an app error with its machine-readable code; the same
// names as the REST problem types, with underscores. Unknown errors are
// logged (never with the frame's content) and answered "internal".
func (c *conn) appError(ctx context.Context, frameID string, err error) {
	var v *apperr.ValidationError
	if errors.As(err, &v) {
		c.invalid(frameID, v.Field)
		return
	}
	var slow *apperr.SlowModeError
	if errors.As(err, &slow) {
		secs := max(int64(math.Ceil(slow.RetryAfter.Seconds())), 1)
		c.sendFrame("error", frameID, errorData{Code: "slow_mode", RetryAfterSeconds: secs})
		return
	}
	for _, e := range []struct {
		err  error
		code string
	}{
		{apperr.ErrTayfaNotFound, "tayfa_not_found"},
		{apperr.ErrChannelNotFound, "channel_not_found"},
		{apperr.ErrPermissionDenied, "permission_denied"},
		{apperr.ErrMuted, "muted"},
		{apperr.ErrChannelArchived, "channel_archived"},
		{apperr.ErrTayfaReadOnly, "tayfa_read_only"},
		{realtime.ErrTooManySubscriptions, "too_many_subscriptions"},
		{apperr.ErrUnavailable, "unavailable"},
		{apperr.ErrChannelsUnavailable, "unavailable"},
		{apperr.ErrConversationNotFound, "conversation_not_found"},
		{apperr.ErrDirectMessageNotAllowed, "direct_message_not_allowed"},
		{apperr.ErrMessageRequestPending, "message_request_pending"},
		{apperr.ErrMessageRequestLimit, "message_request_limit"},
		{apperr.ErrRateLimited, "rate_limited"},
		{apperr.ErrDirectMessagesUnavailable, "unavailable"},
		{apperr.ErrE2EDeviceNotRegistered, "e2e_device_not_registered"},
		{apperr.ErrE2ERequired, "e2e_required"},
		{context.DeadlineExceeded, "unavailable"},
	} {
		if errors.Is(err, e.err) {
			c.fail(frameID, e.code)
			return
		}
	}
	if !errors.Is(err, context.Canceled) {
		c.cfg.Log.ErrorContext(ctx, "gerçek zamanlı istek başarısız", slog.String("hata", err.Error()))
	}
	c.fail(frameID, "internal")
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

// closeLoop ends the connection on the first close request or when the
// access token expires. The close handshake needs the read loop running,
// so it happens here and the read loop ends with it.
func (c *conn) closeLoop(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn) {
	exp := time.NewTimer(max(c.expires.Sub(c.cfg.Now()), 0))
	defer exp.Stop()
	var req closeReq
	select {
	case <-ctx.Done():
		return
	case req = <-c.end:
	case <-exp.C:
		req = closeReq{code: CloseTokenExpired, reason: "token_expired"}
	}
	_ = ws.Close(req.code, req.reason)
	cancel()
}
