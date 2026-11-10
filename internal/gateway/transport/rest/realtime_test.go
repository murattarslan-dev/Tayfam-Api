package rest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeRealtime is chat's WebSocket port: it records the handshake request
// and sends one frame.
type fakeRealtime struct {
	mu  sync.Mutex
	req *http.Request
}

func (s *fakeRealtime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.req = r.Clone(context.Background())
	s.mu.Unlock()
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = c.CloseNow() }()
	_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"ready"}`))
	_, _, _ = c.Read(r.Context())
}

func (s *fakeRealtime) last() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.req
}

func realtimeRouter(t *testing.T, upstream http.Handler) *httptest.Server {
	t.Helper()
	chat := httptest.NewServer(upstream)
	t.Cleanup(chat.Close)
	u, _ := url.Parse(chat.URL)
	d := testChatDeps(&fakeChat{})
	d.RealtimeURL = u
	d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	// A dev gateway: even the local web build's origin may not connect.
	gw := httptest.NewServer(NewRouter(Deps{Version: "test", Chat: d, DevOrigins: []string{devOrigin}, WebClient: true}))
	t.Cleanup(gw.Close)
	return gw
}

func wsURL(base, query string) string {
	return "ws" + strings.TrimPrefix(base, "http") + "/v1/realtime" + query
}

func dialRealtime(t *testing.T, gw *httptest.Server, query string, hdr http.Header) (*websocket.Conn, int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, resp, err := websocket.Dial(ctx, wsURL(gw.URL, query), &websocket.DialOptions{HTTPHeader: hdr})
	code := 0
	if resp != nil {
		code = resp.StatusCode
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	return c, code, err
}

func readReady(t *testing.T, c *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, b, err := c.Read(ctx); err != nil || string(b) != `{"type":"ready"}` {
		t.Fatalf("çerçeve: %q %v", b, err)
	}
}

// Mobile: the token in the Authorization header reaches chat in the same
// header; nothing else of the client's credentials does.
func TestRealtimeMobileToken(t *testing.T) {
	up := &fakeRealtime{}
	gw := realtimeRouter(t, up)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer USERTOKEN")
	hdr.Set("Cookie", "tayfam_at=BASKA; other=1")
	c, _, err := dialRealtime(t, gw, "?ticket=eski", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	readReady(t, c)
	r := up.last()
	if r.URL.Path != "/v1/realtime" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer USERTOKEN" || r.Header.Get("Cookie") != "" {
		t.Fatalf("chat'e giden istek: %s %v", r.URL, r.Header)
	}
}

func TestRealtimeRefusesBeforeChat(t *testing.T) {
	up := &fakeRealtime{}
	gw := realtimeRouter(t, up)
	h := func(kv ...string) http.Header {
		hdr := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			hdr.Set(kv[i], kv[i+1])
		}
		return hdr
	}
	for name, tc := range map[string]struct {
		hdr  http.Header
		code int
	}{
		// Any Origin is a browser; only the mobile apps connect (ADR-0017).
		"yabancı origin":     {h("Origin", "https://kotu.example", "Authorization", "Bearer USERTOKEN"), http.StatusForbidden},
		"geliştirme kaynağı": {h("Origin", devOrigin, "Authorization", "Bearer USERTOKEN"), http.StatusForbidden},
		"eski web adresi":    {h("Origin", "https://tayfam.app", "Authorization", "Bearer USERTOKEN"), http.StatusForbidden},
		"çerezle origin'li":  {h("Origin", "https://tayfam.app", "Cookie", "tayfam_at=USERTOKEN"), http.StatusForbidden},
		"yalnızca çerez":     {h("Cookie", "tayfam_at=USERTOKEN"), http.StatusUnauthorized},
		"token yok":          {h(), http.StatusUnauthorized},
		"geçersiz token":     {h("Authorization", "Bearer BAD"), http.StatusUnauthorized},
		"anahtar alınamadı":  {h("Authorization", "Bearer DOWN"), http.StatusServiceUnavailable},
		"çok uzun token":     {h("Authorization", "Bearer "+strings.Repeat("a", maxAccessTokenBytes+1)), http.StatusUnauthorized},
	} {
		if _, code, err := dialRealtime(t, gw, "", tc.hdr); err == nil || code != tc.code {
			t.Errorf("%s: %d %v", name, code, err)
		}
	}
	// A plain GET (no upgrade) is not passed on either.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, gw.URL+"/v1/realtime", nil)
	req.Header.Set("Authorization", "Bearer USERTOKEN")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("upgrade yok: %d", resp.StatusCode)
	}
	if up.last() != nil {
		t.Fatal("reddedilen istek chat'e ulaştı")
	}
}

func TestRealtimeChatDown(t *testing.T) {
	d := testChatDeps(&fakeChat{})
	d.RealtimeURL = &url.URL{Scheme: "http", Host: "127.0.0.1:1"}
	gw := httptest.NewServer(NewRouter(Deps{Version: "test", Chat: d}))
	defer gw.Close()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer USERTOKEN")
	if _, code, err := dialRealtime(t, gw, "", hdr); err == nil || code != http.StatusServiceUnavailable {
		t.Fatalf("%d %v", code, err)
	}
}
