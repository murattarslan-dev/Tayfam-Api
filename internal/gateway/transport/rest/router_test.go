package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStatusReportsUpstreams(t *testing.T) {
	h := NewRouter(Deps{
		Version: "test",
		Upstreams: []Upstream{
			{Name: "identity", Check: func(context.Context) error { return nil }},
			{Name: "core", Check: func(context.Context) error { return errors.New("dial tcp 10.0.0.5:8081: refused") }},
			{Name: "chat", Check: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
		},
		CheckTimeout: 50 * time.Millisecond,
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Errorf("internal error details leaked: %s", rec.Body.String())
	}
	var res StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"gateway": "ok", "identity": "ok", "core": "unavailable", "chat": "unavailable"}
	for k, v := range want {
		if res.Services[k] != v {
			t.Errorf("%s = %q, want %q", k, res.Services[k], v)
		}
	}
	if res.Status != "degraded" || res.Version != "test" {
		t.Errorf("unexpected response: %+v", res)
	}
}

func TestStatusAllOK(t *testing.T) {
	h := NewRouter(Deps{Upstreams: []Upstream{{Name: "core", Check: func(context.Context) error { return nil }}}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var res StatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Status != "ok" {
		t.Errorf("status = %q", res.Status)
	}
}

func TestUnknownRouteAndWrongMethod(t *testing.T) {
	h := NewRouter(Deps{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/yok", nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("unknown route: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/status", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("POST /v1/status should not succeed, got %d", rec.Code)
	}
}
