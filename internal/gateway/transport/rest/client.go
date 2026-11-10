package rest

import (
	"encoding/json"
	"net/http"

	"github.com/murattarslan-dev/tayfam-api/internal/gateway/app/clientversion"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

// ClientDeps configure the supported client versions (issue #114,
// ADR-0016).
type ClientDeps struct {
	// Versions holds each platform's minimum and latest build; the zero
	// Policy refuses nothing.
	Versions clientversion.Policy
	// ConfigLimit bounds GET /v1/client-config per client IP. Nil uses a
	// default; the open route is never unlimited.
	ConfigLimit         *ratelimit.Limiter
	TrustCFConnectingIP bool
}

// versionExempt lists the paths never refused for an old build: the
// client must be able to learn that it is outdated, and health checks
// carry no header.
func versionExempt(path string) bool {
	return path == "/v1/status" || path == "/v1/client-config"
}

// requireSupportedClient refuses requests from builds below their
// platform's minimum with client-outdated (426), REST and the WebSocket
// handshake alike: it runs before any route, so before the upgrade and
// before the token check. A request without the header passes (ADR-0016).
// The check is about keeping broken or unsafe builds out of use, not
// security: any client can claim any version.
func requireSupportedClient(p clientversion.Policy) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions || versionExempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			switch p.Check(r.Header.Values(clientversion.Header)) {
			case clientversion.Outdated:
				httpx.WriteProblem(w, r, http.StatusUpgradeRequired, "client-outdated", "Upgrade Required", "")
				return
			case clientversion.Invalid:
				httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-client-version", "Bad Request", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientConfigResponse is the body of GET /v1/client-config.
type ClientConfigResponse struct {
	Platforms map[clientversion.Platform]PlatformVersions `json:"platforms"`
}

// PlatformVersions is one platform's supported range; a field is left out
// when it is not configured.
type PlatformVersions struct {
	MinSupportedVersion string `json:"min_supported_version,omitempty"`
	LatestVersion       string `json:"latest_version,omitempty"`
}

// registerClientConfig adds GET /v1/client-config, which takes no token
// and lists the served platforms only (no web in prod, ADR-0017):
// clients call it at start-up, before signing in. It returns only the
// configured version numbers, the same for everyone; it reads nothing
// per request but the client IP for its rate limit.
func registerClientConfig(mux *http.ServeMux, d ClientDeps) {
	res := ClientConfigResponse{Platforms: map[clientversion.Platform]PlatformVersions{}}
	for _, p := range d.Versions.Served() {
		r := d.Versions.Range(p)
		res.Platforms[p] = PlatformVersions{MinSupportedVersion: r.Min.String(), LatestVersion: r.Latest.String()}
	}
	body, err := json.Marshal(res)
	if err != nil {
		panic(err) // strings only; cannot fail
	}
	limit := d.ConfigLimit
	if limit == nil {
		// One call per app start; loose for many users behind one CGNAT
		// address (ADR-0004).
		limit = ratelimit.New(60, 30)
	}
	mux.HandleFunc("GET /v1/client-config", func(w http.ResponseWriter, r *http.Request) {
		if !limit.Allow(clientIP(r, d.TrustCFConnectingIP)) {
			httpx.WriteProblem(w, r, http.StatusTooManyRequests, "rate-limited", "Too Many Requests", "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// No personal data; a short cache keeps a changed minimum close.
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write(append(body, '\n'))
	})
}
