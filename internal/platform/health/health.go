// Package health serves the liveness and readiness endpoints Kubernetes probes.
//
// Liveness answers whether the process is alive. Readiness answers whether it
// should receive traffic: it runs every registered check and turns false as
// soon as shutdown starts, so the load balancer drains the pod first.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Check reports whether a dependency is usable.
type Check func(ctx context.Context) error

// Health holds readiness checks and the draining flag.
type Health struct {
	timeout  time.Duration
	draining atomic.Bool

	mu     sync.RWMutex
	checks map[string]Check
}

// New returns a Health whose checks each run with the given timeout.
func New(timeout time.Duration) *Health {
	return &Health{timeout: timeout, checks: map[string]Check{}}
}

// Add registers a readiness check under name.
func (h *Health) Add(name string, c Check) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = c
}

// SetDraining makes readiness fail; called when shutdown starts.
func (h *Health) SetDraining() { h.draining.Store(true) }

// Result is the readiness report.
type Result struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// Ready runs all checks concurrently and reports whether every one passed.
func (h *Health) Ready(ctx context.Context) (Result, bool) {
	if h.draining.Load() {
		return Result{Status: "draining"}, false
	}

	h.mu.RLock()
	names := make([]string, 0, len(h.checks))
	for n := range h.checks {
		names = append(names, n)
	}
	sort.Strings(names)
	checks := make([]Check, len(names))
	for i, n := range names {
		checks[i] = h.checks[n]
	}
	h.mu.RUnlock()

	errs := make([]error, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, h.timeout)
			defer cancel()
			errs[i] = run(cctx, c)
		}()
	}
	wg.Wait()

	res := Result{Status: "ok", Checks: make(map[string]string, len(names))}
	ok := true
	for i, n := range names {
		if errs[i] != nil {
			ok = false
			res.Checks[n] = errs[i].Error()
		} else {
			res.Checks[n] = "ok"
		}
	}
	if !ok {
		res.Status = "unavailable"
	}
	return res, ok
}

// run calls c, converting a panic into an error so one bad check cannot crash the probe.
func run(ctx context.Context, c Check) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("check panicked: %v", r)
		}
	}()
	return c(ctx)
}

// Register mounts GET /livez and GET /readyz on mux.
func (h *Health) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, Result{Status: "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		res, ok := h.Ready(r.Context())
		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, res)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
