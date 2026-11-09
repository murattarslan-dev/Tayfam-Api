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
	d       ChatDeps
	origins map[string]bool
	proxy   *httputil.ReverseProxy
}

// registerRealtime adds the WebSocket route (issue #82; ADR-0005
// "Bağlantı", ADR-0014). The connection is not terminated here: the
// gateway checks the Origin and the user's access token (mobile:
// Authorization header; web: the tayfam_at cookie) and passes the upgrade
// to chat's HTTP port with that token in the Authorization header, never
// the cookie. chat verifies the token again and runs the connection. The
// gateway stays stateless.
func registerRealtime(mux *http.ServeMux, d ChatDeps, origins []string) {
	h := realtimeHandlers{d: d, origins: map[string]bool{}, proxy: newRealtimeProxy(d.RealtimeURL, d.Log)}
	for _, o := range origins {
		h.origins[o] = true
	}
	mux.HandleFunc("GET /v1/realtime", h.connect)
}

// connect checks what it can, then proxies to chat. A token from the web
// cookie needs an allowed Origin: browsers always send one with a
// WebSocket, so a cross-site page cannot ride on the cookie
// (cross-site WebSocket hijacking). chat checks the Origin again
// (ADR-0011).
func (h realtimeHandlers) connect(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" && !h.origins[origin] {
		httpx.WriteProblem(w, r, http.StatusForbidden, "forbidden-origin", "Forbidden", "")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	token, web := bearer(r)
	if token == "" || len(token) > maxAccessTokenBytes {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	}
	if web && origin == "" {
		httpx.WriteProblem(w, r, http.StatusForbidden, "forbidden-origin", "Forbidden", "")
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
// the verified access token, whichever way it came. The query string is
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
