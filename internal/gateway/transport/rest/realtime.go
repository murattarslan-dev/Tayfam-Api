package rest

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
)

// RealtimeClient is chat's RealtimeService as the gateway uses it (issue
// #82).
type RealtimeClient interface {
	CreateRealtimeTicket(ctx context.Context, in *chatv1.CreateRealtimeTicketRequest, opts ...grpc.CallOption) (*chatv1.CreateRealtimeTicketResponse, error)
}

// ticketLen is the length of chat's tickets (32 bytes, base64url without
// padding); anything else is refused before it reaches chat.
var ticketLen = base64.RawURLEncoding.EncodedLen(32)

type realtimeHandlers struct {
	d       ChatDeps
	origins map[string]bool
	proxy   *httputil.ReverseProxy
}

// registerRealtime adds the ticket route and the WebSocket route. The
// connection is not terminated here (ADR-0005): the gateway checks the
// Origin and the shape of the ticket and passes the request to chat's HTTP
// port unchanged; chat redeems the ticket and runs the connection. The
// gateway stays stateless.
func registerRealtime(mux *http.ServeMux, d ChatDeps, origins []string) {
	h := realtimeHandlers{d: d, origins: map[string]bool{}, proxy: newRealtimeProxy(d.RealtimeURL, d.Log)}
	for _, o := range origins {
		h.origins[o] = true
	}
	mux.HandleFunc("POST /v1/realtime/ticket", authenticated(d.Verifier, d.Log, h.createTicket))
	mux.HandleFunc("GET /v1/realtime", h.connect)
}

type ticketJSON struct {
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h realtimeHandlers) createTicket(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Realtime.CreateRealtimeTicket(ctx, &chatv1.CreateRealtimeTicketRequest{})
	if err != nil {
		writeUpstreamError(w, r, h.d.Log, "chat", err)
		return
	}
	writeJSON(w, http.StatusCreated, ticketJSON{Ticket: resp.GetTicket(), ExpiresAt: resp.GetExpireTime().AsTime()})
}

// connect checks what it can without state, then proxies to chat. chat
// checks the Origin again (ADR-0011).
func (h realtimeHandlers) connect(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Get("Origin"); o != "" && !h.origins[o] {
		httpx.WriteProblem(w, r, http.StatusForbidden, "forbidden-origin", "Forbidden", "")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	t := r.URL.Query().Get("ticket")
	if len(t) != ticketLen {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "invalid-ticket", "Unauthorized", "")
		return
	}
	if _, err := base64.RawURLEncoding.DecodeString(t); err != nil {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "invalid-ticket", "Unauthorized", "")
		return
	}
	h.proxy.ServeHTTP(w, r)
}

// newRealtimeProxy passes the upgrade to chat. Only what the WebSocket
// handshake needs goes on: credentials (the token cookie, Authorization)
// are dropped, chat authenticates with the ticket alone. Errors are logged
// without the request URL, which carries the ticket.
func newRealtimeProxy(target *url.URL, log *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			if id := httpx.RequestIDFrom(pr.In.Context()); id != "" {
				pr.Out.Header.Set(httpx.RequestIDHeader, id)
			}
		},
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: upstreamWait,
			MaxIdleConns:          4,
			IdleConnTimeout:       time.Minute,
		},
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			log.WarnContext(r.Context(), "chat gerçek zamanlı bağlantısına ulaşılamadı")
			httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
		},
	}
}
