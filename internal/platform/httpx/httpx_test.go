package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDKeepsValidAndReplacesInvalid(t *testing.T) {
	var seen string
	h := RequestID()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "abcdef12-3456")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen != "abcdef12-3456" || rec.Header().Get(RequestIDHeader) != seen {
		t.Errorf("valid incoming ID should be kept: %q", seen)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "<script>")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen == "<script>" || len(seen) != 32 {
		t.Errorf("invalid incoming ID should be replaced: %q", seen)
	}
}

func TestAccessLogUsesRoutePatternNotURL(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tayfas/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := Chain(mux, RequestID(), AccessLog(log))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tayfas/gizli-id?token=secret", nil))

	out := buf.String()
	if strings.Contains(out, "gizli-id") || strings.Contains(out, "secret") {
		t.Errorf("raw URL leaked into access log: %s", out)
	}
	var rec2 map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec2); err != nil {
		t.Fatal(err)
	}
	if rec2["route"] != "GET /v1/tayfas/{id}" || rec2["status"] != float64(http.StatusTeapot) {
		t.Errorf("unexpected log record: %v", rec2)
	}
}

func TestRecoverWritesProblem(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("beklenmeyen")
	}), RequestID(), Recover(log))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content type = %q", ct)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != ProblemTypeBase+"internal" || p.RequestID == "" || strings.Contains(rec.Body.String(), "beklenmeyen") {
		t.Errorf("problem response leaks or is incomplete: %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "beklenmeyen") {
		t.Error("panic value should be logged server-side")
	}
}

func TestNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	NotFound(rec, httptest.NewRequest(http.MethodGet, "/yok", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not-found") {
		t.Errorf("unexpected: %d %s", rec.Code, rec.Body.String())
	}
}
