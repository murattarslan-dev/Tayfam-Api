package rest

import (
	"context"
	"net/http"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
)

// IntegrityDeps configures app integrity enforcement (ADR-0022 §6, issue
// #164).
type IntegrityDeps struct {
	// Enforce refuses access tokens without the integrity claim; off in the
	// off and report modes.
	Enforce bool
	// Verifier checks the token whose claim is read.
	Verifier TokenVerifier
}

// integrityExempt are the routes a session that has not passed integrity
// still reaches (ADR-0022 §6): signing in and out, registration and
// consents, its own sessions, deleting the account and the KVKK data
// export. A user who cannot pass must still be able to leave and to take
// their data. The list is fixed by a test against the router.
var integrityExempt = map[string]bool{
	"GET /v1/status":                                     true,
	"GET /v1/client-config":                              true,
	"POST /v1/auth/sign-in":                              true,
	"POST /v1/auth/refresh":                              true,
	"POST /v1/auth/sign-out":                             true,
	"GET /v1/registration/requirements":                  true,
	"POST /v1/registration":                              true,
	"GET /v1/me/consents":                                true,
	"POST /v1/me/consents":                               true,
	"GET /v1/me/sessions":                                true,
	"DELETE /v1/me/sessions/{session_id}":                true,
	"POST /v1/me/sessions/sign-out-others":               true,
	"DELETE /v1/me":                                      true,
	"POST /v1/me/data-exports":                           true,
	"GET /v1/me/data-exports":                            true,
	"GET /v1/me/data-exports/{data_export_id}":           true,
	"POST /v1/me/data-exports/{data_export_id}/download": true,
}

// requireIntegrity refuses, in enforce mode, a request whose valid access
// token lacks the integrity claim, unless its route is exempt. It runs in
// front of the router, so the WebSocket handshake is covered too. A request
// without a token, or with a token that does not verify, passes on: the
// route answers it as before (unauthenticated, or open).
func requireIntegrity(mux *http.ServeMux, d IntegrityDeps) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		if !d.Enforce || d.Verifier == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearer(r)
			if token == "" || len(token) > maxAccessTokenBytes {
				next.ServeHTTP(w, r)
				return
			}
			if _, pattern := mux.Handler(r); integrityExempt[pattern] {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
			defer cancel()
			c, err := d.Verifier.Verify(ctx, token)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			if !c.Integrity {
				httpx.WriteProblem(w, r, http.StatusForbidden, "integrity-required", "Forbidden", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
