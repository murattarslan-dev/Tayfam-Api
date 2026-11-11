// Package rest is the gateway's public HTTP API.
package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/gateway/app/clientversion"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/health"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
)

// Upstream is an internal service whose availability the gateway reports.
type Upstream struct {
	Name  string
	Check health.Check
}

// Deps are the router's dependencies.
type Deps struct {
	Version   string
	Upstreams []Upstream
	// CheckTimeout bounds each upstream check in /v1/status.
	CheckTimeout time.Duration
	// Auth enables sign-in and registration routes when its clients are set.
	Auth AuthDeps
	// Core enables the tayfa and membership routes when its clients and
	// verifier are set.
	Core CoreDeps
	// Chat enables the notification and channel routes when their clients
	// and the verifier are set.
	Chat ChatDeps
	// Client sets the supported client versions and the client-config
	// route's rate limit (issue #114).
	Client ClientDeps
	// DevOrigins are the local development origins
	// (http://localhost:<port>) the web build of the app may call the API
	// from, without credentials (CORS, ADR-0017). Always empty in prod.
	DevOrigins []string
	// WebClient serves the local web build of the app: X-Tayfam-Client
	// web, sign-in with platform web and the web version header. Off in
	// prod, the zero value (ADR-0017).
	WebClient bool
}

// NewRouter returns the public API handler.
func NewRouter(d Deps) http.Handler {
	if d.CheckTimeout <= 0 {
		d.CheckTimeout = 2 * time.Second
	}
	if !d.WebClient {
		// The web platform does not exist here: its version header is
		// refused as invalid and client-config does not list it.
		d.Client.Versions = d.Client.Versions.Without(clientversion.Web)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", statusHandler(d))
	registerClientConfig(mux, d.Client)
	if d.Auth.Auth != nil && d.Auth.Registration != nil {
		registerAuth(mux, d.Auth, d.WebClient)
	}
	if d.Core.Tayfas != nil && d.Core.Members != nil && d.Core.Verifier != nil {
		registerCore(mux, d.Core)
	}
	if d.Chat.Notifications != nil && d.Chat.Verifier != nil {
		registerNotifications(mux, d.Chat)
	}
	if d.Chat.Channels != nil && d.Chat.Verifier != nil {
		registerChannels(mux, chatHandlers{d: d.Chat})
	}
	if d.Chat.DirectMessages != nil && d.Chat.Verifier != nil {
		registerDirectMessages(mux, chatHandlers{d: d.Chat})
	}
	if d.Chat.E2E != nil && d.Chat.Verifier != nil {
		registerE2E(mux, chatHandlers{d: d.Chat})
	}
	if d.Chat.Push != nil && d.Chat.Verifier != nil {
		registerPush(mux, chatHandlers{d: d.Chat})
	}
	if d.Chat.RealtimeURL != nil && d.Chat.Verifier != nil {
		registerRealtime(mux, d.Chat)
	}
	mux.HandleFunc("/", httpx.NotFound)
	// cors runs first so that the local web build can read a refusal.
	return httpx.Chain(mux, cors(d.DevOrigins), requireSupportedClient(d.Client.Versions), requireClientHeader(d.WebClient))
}

// StatusResponse is the body of GET /v1/status.
type StatusResponse struct {
	Status    string            `json:"status"`
	Version   string            `json:"version"`
	Services  map[string]string `json:"services"`
	CheckedAt time.Time         `json:"checked_at"`
}

// statusHandler reports whether each internal service is reachable. Only
// "ok" or "unavailable" is exposed; error details stay internal.
func statusHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		results := make([]string, len(d.Upstreams))
		var wg sync.WaitGroup
		for i, u := range d.Upstreams {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), d.CheckTimeout)
				defer cancel()
				if err := u.Check(ctx); err != nil {
					results[i] = "unavailable"
				} else {
					results[i] = "ok"
				}
			}()
		}
		wg.Wait()

		res := StatusResponse{
			Status:    "ok",
			Version:   d.Version,
			Services:  map[string]string{"gateway": "ok"},
			CheckedAt: time.Now().UTC(),
		}
		for i, u := range d.Upstreams {
			res.Services[u.Name] = results[i]
			if results[i] != "ok" {
				res.Status = "degraded"
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(res)
	}
}
