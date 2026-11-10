package rest

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/murattarslan-dev/tayfam-api/internal/gateway/app/clientversion"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
)

// clientHeader names the client on state-changing requests (ADR-0004,
// ADR-0017).
const clientHeader = "X-Tayfam-Client"

// requireClientHeader refuses state-changing requests without
// X-Tayfam-Client: android or ios, or web where the local web build is
// served (webClient; never in prod). No credential travels in a cookie, so
// a cross-site request carries none; the header stays as a second layer:
// a browser can add it only after a CORS preflight, which only a local
// development origin outside prod passes (ADR-0017).
func requireClientHeader(webClient bool) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				switch r.Header.Get(clientHeader) {
				case "android", "ios":
				case "web":
					if webClient {
						break
					}
					fallthrough
				default:
					httpx.WriteProblem(w, r, http.StatusBadRequest, "missing-client-header", "Bad Request", "")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// corsAllowedHeaders are the request headers a development origin may send.
var corsAllowedHeaders = strings.Join([]string{"Authorization", "Content-Type", clientHeader, clientversion.Header, httpx.RequestIDHeader}, ", ")

// cors answers the local web build of the app (ADR-0017): only the listed
// development origins (http://localhost:<port>; empty in prod) get CORS
// headers, never with credentials: the token travels in the Authorization
// header, there are no cookies. Any other origin gets no CORS header, so a
// browser cannot read the response, and its preflight is refused.
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
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", corsAllowedHeaders)
				h.Set("Access-Control-Max-Age", strconv.Itoa(600))
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.Set("Access-Control-Expose-Headers", httpx.RequestIDHeader)
			next.ServeHTTP(w, r)
		})
	}
}
