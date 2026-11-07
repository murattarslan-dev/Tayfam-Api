package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe for the logger and the test to share.
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

func lookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRunServesAndShutsDownGracefully(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuffer{}
	started := make(chan map[string]string, 1)
	done := make(chan error, 1)

	env := map[string]string{"OPS_ADDR": "127.0.0.1:0", "PUBLIC_ADDR": "127.0.0.1:0"}
	go func() {
		done <- Run(ctx, "deneme", lookup(env), out, func(rt *Runtime) error {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /merhaba", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("selam"))
			})
			rt.AddHTTPServer("public", rt.Config.String("PUBLIC_ADDR", ""), mux)
			return nil
		}, started)
	}()

	var addrs map[string]string
	select {
	case addrs = <-started:
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("servers did not start")
	}

	if code, body := get(t, "http://"+addrs["public"]+"/merhaba"); code != 200 || body != "selam" {
		t.Errorf("public server: %d %q", code, body)
	}
	if code, body := get(t, "http://"+addrs["ops"]+"/readyz"); code != 200 || !strings.Contains(body, `"ok"`) {
		t.Errorf("readyz: %d %s", code, body)
	}
	if code, _ := get(t, "http://"+addrs["ops"]+"/livez"); code != 200 {
		t.Errorf("livez: %d", code)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on clean shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	logs := out.String()
	if !strings.Contains(logs, `"service":"deneme"`) || !strings.Contains(logs, "servis durdu") {
		t.Errorf("unexpected logs: %s", logs)
	}
	if strings.Contains(logs, "/readyz") {
		t.Errorf("probe requests should not be access-logged: %s", logs)
	}
}

func TestRunRejectsBadConfigBeforeListening(t *testing.T) {
	err := Run(context.Background(), "deneme", lookup(map[string]string{"TAYFAM_ENV": "staging"}), io.Discard,
		func(rt *Runtime) error {
			rt.Config.Required("DATABASE_URL")
			return nil
		}, nil)
	if err == nil {
		t.Fatal("expected configuration error")
	}
	for _, want := range []string{"TAYFAM_ENV", "DATABASE_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

func TestRunReportsSetupError(t *testing.T) {
	err := Run(context.Background(), "deneme", lookup(map[string]string{"OPS_ADDR": "127.0.0.1:0"}), io.Discard,
		func(*Runtime) error { return errors.New("bağımlılık yok") }, nil)
	if err == nil || !strings.Contains(err.Error(), "bağımlılık yok") {
		t.Fatalf("expected setup error, got %v", err)
	}
}

func TestRunFailsOnPortConflict(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()

	err = Run(context.Background(), "deneme", lookup(map[string]string{"OPS_ADDR": "127.0.0.1:0"}), io.Discard,
		func(rt *Runtime) error {
			rt.AddHTTPServer("public", busy.Addr().String(), http.NewServeMux())
			return nil
		}, nil)
	if err == nil || !strings.Contains(err.Error(), "dinleyemiyor") {
		t.Fatalf("expected port conflict error, got %v", err)
	}
}
