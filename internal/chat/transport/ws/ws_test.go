package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/realtime"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

// fakeTickets is a single-use ticket store.
type fakeTickets struct {
	mu sync.Mutex
	m  map[string]realtime.Identity
}

func (f *fakeTickets) add(who realtime.Identity) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := id.New().String() + "-" + id.New().String()
	f.m[v] = who
	return v
}

func (f *fakeTickets) Redeem(_ context.Context, v string) (realtime.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	who, ok := f.m[v]
	if !ok {
		return realtime.Identity{}, realtime.ErrInvalidTicket
	}
	delete(f.m, v)
	return who, nil
}

func (f *fakeTickets) left() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.m)
}

type env struct {
	srv     *httptest.Server
	tickets *fakeTickets
	hub     *realtime.Hub
}

func newEnv(t *testing.T, mod func(*Config)) *env {
	t.Helper()
	hub, err := realtime.NewHub(realtime.HubConfig{MaxTotal: 100, MaxPerUser: 2})
	if err != nil {
		t.Fatal(err)
	}
	tk := &fakeTickets{m: map[string]realtime.Identity{}}
	cfg := Config{
		Tickets: tk, Hub: hub, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		AllowedOrigins: []string{"https://tayfam.app"}, Frames: ratelimit.New(600, 100),
	}
	if mod != nil {
		mod(&cfg)
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{srv: srv, tickets: tk, hub: hub}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func (e *env) url(ticket string) string {
	return "ws" + strings.TrimPrefix(e.srv.URL, "http") + Path + "?ticket=" + ticket
}

// dial opens a connection and returns the handshake's HTTP status.
func (e *env) dial(t *testing.T, ticket, origin string) (*websocket.Conn, int, error) {
	t.Helper()
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	c, resp, err := websocket.Dial(testCtx(t), e.url(ticket), &websocket.DialOptions{HTTPHeader: hdr})
	code := 0
	if resp != nil {
		code = resp.StatusCode
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	return c, code, err
}

// connect opens a connection for a new user and reads the ready frame.
func (e *env) connect(t *testing.T, who realtime.Identity) *websocket.Conn {
	t.Helper()
	c, _, err := e.dial(t, e.tickets.add(who), "https://tayfam.app")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	if f := readFrame(t, c); f.Type != "ready" {
		t.Fatalf("ilk çerçeve: %+v", f)
	}
	return c
}

func readFrame(t *testing.T, c *websocket.Conn) frame {
	t.Helper()
	_, b, err := c.Read(testCtx(t))
	if err != nil {
		t.Fatalf("okuma: %v", err)
	}
	var f frame
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func errorCode(t *testing.T, f frame) string {
	t.Helper()
	var d errorData
	_ = json.Unmarshal(f.Data, &d)
	if f.Type != "error" {
		t.Fatalf("hata çerçevesi değil: %+v", f)
	}
	return d.Code
}

// closedWith reads until the server closes and returns the close code.
func closedWith(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx := testCtx(t)
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func newWho() realtime.Identity { return realtime.Identity{User: id.New(), Session: id.New()} }

func TestRefusedBeforeUpgrade(t *testing.T) {
	e := newEnv(t, nil)
	who := newWho()

	// A foreign web origin is refused, and the ticket is not used up.
	ticket := e.tickets.add(who)
	if _, resp, err := e.dial(t, ticket, "https://kotu.example"); err == nil || resp != http.StatusForbidden {
		t.Fatalf("yabancı origin: %v %v", resp, err)
	}
	if e.tickets.left() != 1 {
		t.Fatal("reddedilen istek bileti harcadı")
	}
	// A plain GET does not use it either.
	req, _ := http.NewRequestWithContext(testCtx(t), http.MethodGet, e.srv.URL+Path+"?ticket="+ticket, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || e.tickets.left() != 1 {
		t.Fatalf("upgrade yok: %d", resp.StatusCode)
	}
	// Native clients send no Origin.
	c, _, err := e.dial(t, ticket, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	// The ticket worked once; a second use is refused.
	if _, resp, err := e.dial(t, ticket, ""); err == nil || resp != http.StatusUnauthorized {
		t.Fatalf("bilet tekrar kullanıldı: %v %v", resp, err)
	}
	if _, resp, err := e.dial(t, "", ""); err == nil || resp != http.StatusUnauthorized {
		t.Fatalf("bilet yok: %v %v", resp, err)
	}
}

func TestTooManyConnections(t *testing.T) {
	e := newEnv(t, nil)
	who := newWho()
	e.connect(t, who)
	e.connect(t, who)
	if _, resp, err := e.dial(t, e.tickets.add(who), ""); err == nil || resp != http.StatusTooManyRequests {
		t.Fatalf("üçüncü bağlantı: %v %v", resp, err)
	}
}

func TestSessionEndClosesConnection(t *testing.T) {
	e := newEnv(t, nil)
	who, other := newWho(), newWho()
	c := e.connect(t, who)
	c2 := e.connect(t, other)
	e.hub.EndSession(who.Session)
	if code := closedWith(t, c); code != CloseSessionEnded {
		t.Fatalf("kapanış kodu %d", code)
	}
	// The other user's connection is untouched.
	if err := c2.Write(testCtx(t), websocket.MessageText, []byte(`{"type":"x","id":"1"}`)); err != nil {
		t.Fatal(err)
	}
	if f := readFrame(t, c2); errorCode(t, f) != "unsupported_type" {
		t.Fatalf("diğer bağlantı: %+v", f)
	}
	// The session cannot connect again.
	if _, resp, err := e.dial(t, e.tickets.add(who), ""); err == nil || resp != http.StatusUnauthorized {
		t.Fatalf("kapatılan oturum yeniden bağlandı: %v %v", resp, err)
	}
	waitConnections(t, e.hub, 1)
}

func TestUserEndClosesEveryConnection(t *testing.T) {
	e := newEnv(t, nil)
	u := id.New()
	c1 := e.connect(t, realtime.Identity{User: u, Session: id.New()})
	c2 := e.connect(t, realtime.Identity{User: u, Session: id.New()})
	e.hub.EndUser(u)
	if closedWith(t, c1) != CloseSessionEnded || closedWith(t, c2) != CloseSessionEnded {
		t.Fatal("bağlantılar kapanmadı")
	}
	waitConnections(t, e.hub, 0)
}

func waitConnections(t *testing.T, h *realtime.Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.Connections() != n {
		if time.Now().After(deadline) {
			t.Fatalf("bağlantı sayısı %d, beklenen %d", h.Connections(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFrames(t *testing.T) {
	e := newEnv(t, nil)
	c := e.connect(t, newWho())
	ctx := testCtx(t)
	for _, tc := range []struct{ in, id, code string }{
		{`{"type":"subscribe","id":"a1","data":{}}`, "a1", "unsupported_type"},
		{`not json`, "", "invalid_frame"},
		{`{"type":""}`, "", "invalid_frame"},
		{`{"type":"x","extra":1}`, "", "invalid_frame"},
		{`{"type":"x","id":"` + strings.Repeat("a", maxFrameID+1) + `"}`, "", "invalid_frame"},
	} {
		if err := c.Write(ctx, websocket.MessageText, []byte(tc.in)); err != nil {
			t.Fatal(err)
		}
		f := readFrame(t, c)
		if errorCode(t, f) != tc.code || f.ID != tc.id {
			t.Errorf("%s: %+v", tc.in, f)
		}
	}
}

func TestOversizedFrameCloses(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxFrameBytes = 1024 })
	c := e.connect(t, newWho())
	big := `{"type":"x","data":"` + strings.Repeat("a", 2048) + `"}`
	_ = c.Write(testCtx(t), websocket.MessageText, []byte(big))
	if code := closedWith(t, c); code != websocket.StatusMessageTooBig {
		t.Fatalf("kapanış kodu %d", code)
	}
	waitConnections(t, e.hub, 0)
}

func TestBinaryFrameCloses(t *testing.T) {
	e := newEnv(t, nil)
	c := e.connect(t, newWho())
	_ = c.Write(testCtx(t), websocket.MessageBinary, []byte{1, 2, 3})
	if code := closedWith(t, c); code != websocket.StatusUnsupportedData {
		t.Fatalf("kapanış kodu %d", code)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Frames = ratelimit.New(1, 2) })
	c := e.connect(t, newWho())
	ctx := testCtx(t)
	send := func() {
		t.Helper()
		if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"x"}`)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		send()
		if errorCode(t, readFrame(t, c)) != "unsupported_type" {
			t.Fatal("sınır içi çerçeve reddedildi")
		}
	}
	send()
	if code := errorCode(t, readFrame(t, c)); code != "rate_limited" {
		t.Fatalf("sınır aşımı: %s", code)
	}
	// Going on over the limit closes the connection.
	for range maxDenied {
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"x"}`))
	}
	if code := closedWith(t, c); code != websocket.StatusPolicyViolation {
		t.Fatalf("kapanış kodu %d", code)
	}
}

func TestUnansweredPingCloses(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.PingInterval, c.PongTimeout = 50*time.Millisecond, 50*time.Millisecond })
	c := e.connect(t, newWho())
	// The client stops reading, so it answers no ping.
	_ = c
	waitConnections(t, e.hub, 0)
}

func TestAnsweredPingKeepsConnection(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.PingInterval, c.PongTimeout = 20*time.Millisecond, time.Second })
	c := e.connect(t, newWho())
	// CloseRead keeps reading in the background, which answers the pings.
	ctx := c.CloseRead(context.Background())
	time.Sleep(300 * time.Millisecond)
	if ctx.Err() != nil || e.hub.Connections() != 1 {
		t.Fatal("ping'e cevap veren bağlantı kapandı")
	}
}

func TestLifetimeCloses(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxLifetime = 100 * time.Millisecond })
	c := e.connect(t, newWho())
	if code := closedWith(t, c); code != CloseReconnect {
		t.Fatalf("kapanış kodu %d", code)
	}
}

func TestShutdownCloses(t *testing.T) {
	e := newEnv(t, nil)
	c := e.connect(t, newWho())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- e.hub.Run(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if code := closedWith(t, c); code != websocket.StatusGoingAway {
		t.Fatalf("kapanış kodu %d", code)
	}
}

func TestFullSendBufferCloses(t *testing.T) {
	c := newConn(Config{SendBuffer: 1}, newWho())
	c.send([]byte("1"))
	c.send([]byte("2"))
	select {
	case r := <-c.end:
		if r.code != CloseSlowConsumer {
			t.Fatalf("kod %d", r.code)
		}
	default:
		t.Fatal("dolu tampon bağlantıyı kapatmadı")
	}
}
