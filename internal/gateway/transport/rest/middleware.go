package rest

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/murattarslan-dev/tayfam-api/internal/gateway/app/clientversion"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
)

var validClients = map[string]bool{"android": true, "ios": true, "web": true}

// requireClientHeader rejects state-changing requests without
// X-Tayfam-Client (ADR-0004). Browsers cannot add custom headers to
// cross-site form posts, so this blocks CSRF; a cross-origin fetch that adds
// it must first pass the CORS preflight.
func requireClientHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if !validClients[r.Header.Get(clientHeader)] {
				httpx.WriteProblem(w, r, http.StatusBadRequest, "missing-client-header", "Bad Request", "")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// cors allows credentialed requests from the listed origins only (the web
// app at tayfam.app). Other origins get no CORS headers, so browsers block
// them from reading responses.
func cors(origins []string) httpx.Middleware {
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			w.Header().Add("Vary", "Origin")
			if origin == "" || !allowed[origin] {
				if r.Method == http.MethodOptions && origin != "" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", strings.Join([]string{"Authorization", "Content-Type", clientHeader, clientversion.Header, httpx.RequestIDHeader}, ", "))
				h.Set("Access-Control-Max-Age", strconv.Itoa(600))
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.Set("Access-Control-Expose-Headers", httpx.RequestIDHeader)
			next.ServeHTTP(w, r)
		})
	}
}
