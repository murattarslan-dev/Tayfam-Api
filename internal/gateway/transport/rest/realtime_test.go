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
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
)

// testTicket has the shape of a chat ticket (32 bytes, base64url).
const testTicket = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (f *fakeChat) CreateRealtimeTicket(ctx context.Context, in *chatv1.CreateRealtimeTicketRequest, _ ...grpc.CallOption) (*chatv1.CreateRealtimeTicketResponse, error) {
	return &chatv1.CreateRealtimeTicketResponse{Ticket: testTicket, ExpireTime: timestamppb.New(t0)}, f.record(ctx, in)
}

func TestRealtimeTicket(t *testing.T) {
	f := &fakeChat{}
	h := chatRouter(f)
	w := as(h, "POST", "/v1/realtime/ticket", "")
	got, tok, _ := f.last()
	if w.Code != http.StatusCreated || !proto.Equal(got, &chatv1.CreateRealtimeTicketRequest{}) || tok != "USERTOKEN" {
		t.Fatalf("%d %s; chat'e giden %v", w.Code, w.Body, got)
	}
	b := decodeBody[ticketJSON](t, w)
	if b.Ticket != testTicket || !b.ExpiresAt.Equal(t0) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("gövde: %+v", b)
	}
	// Without a token nothing reaches chat.
	_, _, before := f.last()
	if w := do(h, "POST", "/v1/realtime/ticket", "", map[string]string{clientHeader: "web"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("token yok: %d", w.Code)
	}
	if _, _, after := f.last(); after != before {
		t.Fatal("token olmadan chat çağrıldı")
	}
	f.err = chatErr(codes.ResourceExhausted, "ERROR_REASON_TICKET_LIMIT")
	if w := as(h, "POST", "/v1/realtime/ticket", ""); w.Code != http.StatusTooManyRequests || problemType(t, w) != "rate-limited" {
		t.Fatalf("sınır: %d %s", w.Code, w.Body)
	}
}

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
	defer c.CloseNow()
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
	gw := httptest.NewServer(NewRouter(Deps{Version: "test", Chat: d, AllowedOrigins: []string{"https://tayfam.app"}}))
	t.Cleanup(gw.Close)
	return gw
}

func wsURL(base, query string) string {
	return "ws" + strings.TrimPrefix(base, "http") + "/v1/realtime" + query
}

func TestRealtimeProxiesUpgradeWithoutCredentials(t *testing.T) {
	up := &fakeRealtime{}
	gw := realtimeRouter(t, up)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Origin", "https://tayfam.app")
	hdr.Set("Cookie", "tayfam_at=TOKEN")
	hdr.Set("Authorization", "Bearer TOKEN")
	c, _, err := websocket.Dial(ctx, wsURL(gw.URL, "?ticket="+testTicket), &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, b, err := c.Read(ctx)
	if err != nil || string(b) != `{"type":"ready"}` {
		t.Fatalf("çerçeve: %q %v", b, err)
	}
	r := up.last()
	if r.URL.Path != "/v1/realtime" || r.URL.Query().Get("ticket") != testTicket || r.Header.Get("Origin") != "https://tayfam.app" {
		t.Fatalf("chat'e giden istek: %s %v", r.URL, r.Header)
	}
	if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
		t.Fatalf("kimlik bilgisi chat'e iletildi: %v", r.Header)
	}
}

func TestRealtimeRefusesBeforeChat(t *testing.T) {
	up := &fakeRealtime{}
	gw := realtimeRouter(t, up)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for name, tc := range map[string]struct {
		origin, query string
		code          int
		problem       string
	}{
		"yabancı origin": {"https://kotu.example", "?ticket=" + testTicket, http.StatusForbidden, "forbidden-origin"},
		"bilet yok":      {"", "", http.StatusUnauthorized, "invalid-ticket"},
		"bilet kısa":     {"", "?ticket=abc", http.StatusUnauthorized, "invalid-ticket"},
		"bilet biçimi":   {"", "?ticket=" + strings.Repeat("*", len(testTicket)), http.StatusUnauthorized, "invalid-ticket"},
	} {
		hdr := http.Header{}
		if tc.origin != "" {
			hdr.Set("Origin", tc.origin)
		}
		_, resp, err := websocket.Dial(ctx, wsURL(gw.URL, tc.query), &websocket.DialOptions{HTTPHeader: hdr})
		if err == nil || resp == nil || resp.StatusCode != tc.code {
			t.Errorf("%s: %v %v", name, resp, err)
			continue
		}
		_ = resp.Body.Close()
	}
	// A plain GET (no upgrade) is not passed on either.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, gw.URL+"/v1/realtime?ticket="+testTicket, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL(gw.URL, "?ticket="+testTicket), nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("%v %v", resp, err)
	}
	_ = resp.Body.Close()
}
