// Package rest is the gateway's public HTTP API.
package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

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
	// AllowedOrigins are the web origins allowed to call the API with
	// credentials (CORS), such as https://tayfam.app.
	AllowedOrigins []string
}

// NewRouter returns the public API handler.
func NewRouter(d Deps) http.Handler {
	if d.CheckTimeout <= 0 {
		d.CheckTimeout = 2 * time.Second
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", statusHandler(d))
	if d.Auth.Auth != nil && d.Auth.Registration != nil {
		registerAuth(mux, d.Auth)
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
	if d.Chat.RealtimeURL != nil && d.Chat.Verifier != nil {
		registerRealtime(mux, d.Chat, d.AllowedOrigins)
	}
	mux.HandleFunc("/", httpx.NotFound)
	return httpx.Chain(mux, cors(d.AllowedOrigins), requireClientHeader)
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
