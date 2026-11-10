package rest

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
)

type realtimeTokenKey struct{}

type realtimeHandlers struct {
	d     ChatDeps
	proxy *httputil.ReverseProxy
}

// registerRealtime adds the WebSocket route (issue #82; ADR-0005
// "Bağlantı", ADR-0014, ADR-0017). The connection is not terminated here:
// the gateway checks the mobile client's access token (Authorization
// header) and passes the upgrade to chat's HTTP port with that token in
// the Authorization header. chat verifies the token again and runs the
// connection. The gateway stays stateless.
func registerRealtime(mux *http.ServeMux, d ChatDeps) {
	h := realtimeHandlers{d: d, proxy: newRealtimeProxy(d.RealtimeURL, d.Log)}
	mux.HandleFunc("GET /v1/realtime", h.connect)
}

// connect checks what it can, then proxies to chat. Only the mobile apps
// connect, and they send no Origin; browsers always do, so a request with
// an Origin is refused whatever it carries (ADR-0017). chat refuses it
// again (ADR-0011).
func (h realtimeHandlers) connect(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Origin")) > 0 {
		httpx.WriteProblem(w, r, http.StatusForbidden, "forbidden-origin", "Forbidden", "")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	token := bearer(r)
	if token == "" || len(token) > maxAccessTokenBytes {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	_, err := h.d.Verifier.Verify(ctx, token)
	cancel()
	switch {
	case errors.Is(err, auth.ErrInvalidToken):
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	case err != nil:
		h.d.Log.WarnContext(r.Context(), "token doğrulanamadı: anahtar alınamadı")
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
		return
	}
	h.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), realtimeTokenKey{}, token)))
}

// newRealtimeProxy passes the upgrade to chat. Only what the handshake
// needs goes on: cookies are dropped and the Authorization header is set to
// the verified access token. The query string is
// dropped (the route takes none). Errors are logged without the request.
func newRealtimeProxy(target *url.URL, log *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.RawQuery = ""
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			if t, ok := pr.In.Context().Value(realtimeTokenKey{}).(string); ok {
				pr.Out.Header.Set("Authorization", "Bearer "+t)
			}
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
