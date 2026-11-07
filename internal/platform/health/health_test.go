package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReadyAllChecksPass(t *testing.T) {
	h := New(time.Second)
	h.Add("db", func(context.Context) error { return nil })
	h.Add("cache", func(context.Context) error { return nil })

	res, ok := h.Ready(context.Background())
	if !ok || res.Status != "ok" || res.Checks["db"] != "ok" || res.Checks["cache"] != "ok" {
		t.Fatalf("expected ready: %+v", res)
	}
}

func TestReadyReportsFailuresTimeoutsAndPanics(t *testing.T) {
	h := New(50 * time.Millisecond)
	h.Add("ok", func(context.Context) error { return nil })
	h.Add("down", func(context.Context) error { return errors.New("bağlantı yok") })
	h.Add("slow", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	h.Add("panics", func(context.Context) error { panic("boom") })

	res, ok := h.Ready(context.Background())
	if ok || res.Status != "unavailable" {
		t.Fatalf("expected unavailable: %+v", res)
	}
	if res.Checks["ok"] != "ok" {
		t.Errorf("passing check should be ok: %+v", res.Checks)
	}
	for _, n := range []string{"down", "slow", "panics"} {
		if res.Checks[n] == "ok" || res.Checks[n] == "" {
			t.Errorf("check %s should report an error: %+v", n, res.Checks)
		}
	}
}

func TestDrainingFailsReadiness(t *testing.T) {
	h := New(time.Second)
	h.SetDraining()
	if res, ok := h.Ready(context.Background()); ok || res.Status != "draining" {
		t.Fatalf("expected draining: %+v", res)
	}
}

func TestEndpoints(t *testing.T) {
	h := New(time.Second)
	fail := true
	h.Add("db", func(context.Context) error {
		if fail {
			return errors.New("kapalı")
		}
		return nil
	})
	mux := http.NewServeMux()
	h.Register(mux)

	get := func(path string) (int, Result) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		var res Result
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("%s: invalid JSON: %v", path, err)
		}
		return rec.Code, res
	}

	if code, _ := get("/livez"); code != http.StatusOK {
		t.Errorf("livez should always be 200, got %d", code)
	}
	if code, _ := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("readyz should be 503 while a check fails, got %d", code)
	}
	fail = false
	if code, res := get("/readyz"); code != http.StatusOK || res.Status != "ok" {
		t.Errorf("readyz should be 200 once checks pass, got %d %+v", code, res)
	}
}

func TestHTTPCheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()

	check := HTTPCheck(srv.Client(), srv.URL)
	if err := check(context.Background()); err != nil {
		t.Fatalf("expected success: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := check(context.Background()); err == nil {
		t.Fatal("expected failure on 503")
	}
	srv.Close()
	if err := check(context.Background()); err == nil {
		t.Fatal("expected failure when server is down")
	}
}
