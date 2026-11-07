package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"
)

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first one listed is the outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type requestIDKey struct{}

// RequestIDHeader carries the request ID to and from clients.
const RequestIDHeader = "X-Request-Id"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// RequestIDFrom returns the request ID stored in ctx, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// RequestID keeps a well-formed incoming X-Request-Id or generates a new one,
// stores it in the context and echoes it in the response.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !validRequestID.MatchString(id) {
				id = newRequestID()
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
		})
	}
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// statusRecorder captures the response status for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status, s.wrote = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog logs one line per request. It logs the matched route pattern,
// not the raw URL, so path parameters and query strings (which may carry IDs
// or tokens) never reach the log.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			log.LogAttrs(r.Context(), slog.LevelInfo, "http",
				slog.String("method", r.Method),
				slog.String("route", route),
				slog.Int("status", rec.status),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", RequestIDFrom(r.Context())),
			)
		})
	}
}

// Recover turns a panic in a handler into a 500 problem response and logs the stack.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(v)
					}
					log.LogAttrs(r.Context(), slog.LevelError, "panic",
						slog.Any("panic", v),
						slog.String("stack", string(debug.Stack())),
						slog.String("request_id", RequestIDFrom(r.Context())),
					)
					WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal Server Error", "")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
