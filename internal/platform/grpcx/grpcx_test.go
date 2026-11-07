package grpcx

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// panickyHealth is a health service whose Check always panics.
type panickyHealth struct {
	healthpb.UnimplementedHealthServer
}

func (panickyHealth) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	panic("beklenmeyen")
}

func serve(t *testing.T, srv *grpc.Server) *grpc.ClientConn {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)

	conn, err := NewClient(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestHealthFollowsLifecycle(t *testing.T) {
	srv := NewServer(slog.New(slog.DiscardHandler))
	h := RegisterHealth(srv)
	conn := serve(t, srv)
	check := HealthCheck(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A new health server reports SERVING for "" by default; the runtime sets
	// it explicitly after start. Draining must turn it off.
	h.SetServing()
	if err := check(ctx); err != nil {
		t.Fatalf("expected SERVING: %v", err)
	}
	h.SetDraining()
	if err := check(ctx); err == nil {
		t.Fatal("expected NOT_SERVING after draining")
	}
}

func TestPanicBecomesInternalAndIsLogged(t *testing.T) {
	out := &syncBuffer{}
	srv := NewServer(slog.New(slog.NewJSONHandler(out, nil)))
	healthpb.RegisterHealthServer(srv, panickyHealth{})
	conn := serve(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
	if strings.Contains(err.Error(), "beklenmeyen") {
		t.Errorf("panic value leaked to the caller: %v", err)
	}
	if !strings.Contains(out.String(), "beklenmeyen") {
		t.Errorf("panic should be logged server-side: %s", out.String())
	}
}

func TestIsHealth(t *testing.T) {
	cases := map[string]bool{
		"/grpc.health.v1.Health/Check":         true,
		"/grpc.health.v1.Health/Watch":         true,
		"/grpc.health.v1.Health/":              false,
		"/tayfam.core.v1.CoreService/GetTayfa": false,
	}
	for method, want := range cases {
		if got := isHealth(method); got != want {
			t.Errorf("isHealth(%q) = %v, want %v", method, got, want)
		}
	}
}
