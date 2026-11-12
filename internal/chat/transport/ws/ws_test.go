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
	"google.golang.org/grpc/metadata"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/realtime"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

// fakeVerifier knows the tokens issued by env.token.
type fakeVerifier struct {
	mu     sync.Mutex
	tokens map[string]auth.Claims
}

func (f *fakeVerifier) Verify(_ context.Context, token string) (auth.Claims, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.tokens[token]
	if !ok {
		return auth.Claims{}, auth.ErrInvalidToken
	}
	return c, nil
}

// fakeChannels stands in for channel.Service: per user and channel an
// error, else allowed. It records the token of each call.
type fakeChannels struct {
	mu     sync.Mutex
	deny   map[[2]id.ID]error
	tokens []string
	pub    *realtime.Live
}

func tokenOf(ctx context.Context) string {
	md, _ := metadata.FromOutgoingContext(ctx)
	v := md.Get("authorization")
	if len(v) == 0 {
		return ""
	}
	return strings.TrimPrefix(v[len(v)-1], "Bearer ")
}

func (f *fakeChannels) set(user, ch id.ID, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deny[[2]id.ID{user, ch}] = err
}

func (f *fakeChannels) Get(ctx context.Context, caller, tayfa, ch id.ID) (channel.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, tokenOf(ctx))
	return channel.Channel{ID: ch, TayfaID: tayfa}, f.deny[[2]id.ID{caller, ch}]
}

func (f *fakeChannels) Send(ctx context.Context, caller, tayfa, ch id.ID, in channel.SendInput) (channel.Message, error) {
	f.mu.Lock()
	f.tokens = append(f.tokens, tokenOf(ctx))
	err := f.deny[[2]id.ID{caller, ch}]
	f.mu.Unlock()
	if err != nil {
		return channel.Message{}, err
	}
	if in.Body == "" {
		return channel.Message{}, apperr.Validation("body")
	}
	m := channel.Message{ID: id.New(), ChannelID: ch, AuthorID: caller, Body: in.Body, CreatedAt: time.Now().UTC()}
	f.pub.MessageCreated(tayfa, m) // as channel.Service does after commit
	return m, nil
}

func (f *fakeChannels) lastToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[len(f.tokens)-1]
}

type env struct {
	srv  *httptest.Server
	ver  *fakeVerifier
	hub  *realtime.Hub
	live *realtime.Live
	ch   *fakeChannels
}

type who struct {
	user, session id.ID
	token         string
}

func newEnv(t *testing.T, mod func(*Config)) *env {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub, err := realtime.NewHub(realtime.HubConfig{MaxTotal: 100, MaxPerUser: 2, MaxSubscriptions: 10})
	if err != nil {
		t.Fatal(err)
	}
	live, err := realtime.NewLive(realtime.LiveConfig{Hub: hub, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeChannels{deny: map[[2]id.ID]error{}, pub: live}
	live.Bind(fc)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = live.Run(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	ver := &fakeVerifier{tokens: map[string]auth.Claims{}}
	cfg := Config{
		Verifier: ver, Hub: hub, Live: live, Log: log,
		Frames: ratelimit.New(600, 100),
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
	return &env{srv: srv, ver: ver, hub: hub, live: live, ch: fc}
}

// token issues a token for a new session of user, valid for ttl.
func (e *env) token(user id.ID, ttl time.Duration) who {
	e.ver.mu.Lock()
	defer e.ver.mu.Unlock()
	w := who{user: user, session: id.New(), token: "tok-" + id.New().String()}
	e.ver.tokens[w.token] = auth.Claims{Subject: user.String(), SessionID: w.session.String(), ExpiresAt: time.Now().Add(ttl)}
	return w
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func (e *env) url() string { return "ws" + strings.TrimPrefix(e.srv.URL, "http") + Path }

// dial opens a connection and returns the handshake's HTTP status.
func (e *env) dial(t *testing.T, token, origin string) (*websocket.Conn, int, error) {
	t.Helper()
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}
	c, resp, err := websocket.Dial(testCtx(t), e.url(), &websocket.DialOptions{HTTPHeader: hdr})
	code := 0
	if resp != nil {
		code = resp.StatusCode
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	return c, code, err
}

// connect opens a connection with w's token and reads the ready frame.
func (e *env) connect(t *testing.T, w who) *websocket.Conn {
	t.Helper()
	c, _, err := e.dial(t, w.token, "")
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

func write(t *testing.T, c *websocket.Conn, s string) {
	t.Helper()
	if err := c.Write(testCtx(t), websocket.MessageText, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

func errorOf(t *testing.T, f frame) errorData {
	t.Helper()
	if f.Type != "error" {
		t.Fatalf("hata çerçevesi değil: %+v %s", f, f.Data)
	}
	var d errorData
	_ = json.Unmarshal(f.Data, &d)
	return d
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

func ref(tf, ch id.ID) string {
	return `{"tayfa_id":"` + tf.String() + `","channel_id":"` + ch.String() + `"}`
}

func TestRefusedBeforeUpgrade(t *testing.T) {
	e := newEnv(t, nil)
	w := e.token(id.New(), time.Hour)
	// Any Origin is refused, the app's former web address and localhost
	// included: only the mobile apps connect (ADR-0017).
	for _, origin := range []string{"https://kotu.example", "https://tayfam.app", "http://localhost:3000"} {
		if _, code, err := e.dial(t, w.token, origin); err == nil || code != http.StatusForbidden {
			t.Fatalf("origin %s: %d %v", origin, code, err)
		}
	}
	if _, code, err := e.dial(t, "", ""); err == nil || code != http.StatusUnauthorized {
		t.Fatalf("token yok: %d %v", code, err)
	}
	if _, code, err := e.dial(t, "sahte", ""); err == nil || code != http.StatusUnauthorized {
		t.Fatalf("geçersiz token: %d %v", code, err)
	}
	req, _ := http.NewRequestWithContext(testCtx(t), http.MethodGet, e.srv.URL+Path, nil)
	req.Header.Set("Authorization", "Bearer "+w.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("upgrade yok: %d", resp.StatusCode)
	}
	// A native client: no Origin, the token in the header.
	c, _, err := e.dial(t, w.token, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	f := readFrame(t, c)
	var d readyData
	_ = json.Unmarshal(f.Data, &d)
	if f.Type != "ready" || d.HeartbeatSeconds != 30 || d.ExpiresAt.IsZero() {
		t.Fatalf("ready: %+v %s", f, f.Data)
	}
}

func TestTooManyConnections(t *testing.T) {
	e := newEnv(t, nil)
	u := id.New()
	e.connect(t, e.token(u, time.Hour))
	e.connect(t, e.token(u, time.Hour))
	if _, code, err := e.dial(t, e.token(u, time.Hour).token, ""); err == nil || code != http.StatusTooManyRequests {
		t.Fatalf("üçüncü bağlantı: %d %v", code, err)
	}
}

// The connection ends when its access token expires.
func TestTokenExpiryCloses(t *testing.T) {
	e := newEnv(t, nil)
	c := e.connect(t, e.token(id.New(), 200*time.Millisecond))
	if code := closedWith(t, c); code != CloseTokenExpired {
		t.Fatalf("kapanış kodu %d", code)
	}
	waitConnections(t, e.hub, 0)
}

func TestSessionEndClosesConnection(t *testing.T) {
	e := newEnv(t, nil)
	w, other := e.token(id.New(), time.Hour), e.token(id.New(), time.Hour)
	c := e.connect(t, w)
	c2 := e.connect(t, other)
	_ = e.hub.EndSession(context.Background(), w.session)
	if code := closedWith(t, c); code != CloseSessionEnded {
		t.Fatalf("kapanış kodu %d", code)
	}
	write(t, c2, `{"type":"x","id":"1"}`)
	if d := errorOf(t, readFrame(t, c2)); d.Code != "unsupported_type" {
		t.Fatalf("diğer bağlantı: %+v", d)
	}
	// The session cannot connect again, even with its still valid token.
	if _, code, err := e.dial(t, w.token, ""); err == nil || code != http.StatusUnauthorized {
		t.Fatalf("kapatılan oturum yeniden bağlandı: %d %v", code, err)
	}
	waitConnections(t, e.hub, 1)
}

func TestSubscribeAndReceive(t *testing.T) {
	e := newEnv(t, nil)
	tf, ch := id.New(), id.New()
	a, b := e.token(id.New(), time.Hour), e.token(id.New(), time.Hour)
	ca, cb := e.connect(t, a), e.connect(t, b)

	write(t, ca, `{"type":"subscribe","id":"s1","data":`+ref(tf, ch)+`}`)
	if f := readFrame(t, ca); f.Type != "subscribed" || f.ID != "s1" {
		t.Fatalf("abonelik: %+v %s", f, f.Data)
	}
	if e.ch.lastToken() != a.token {
		t.Fatal("abonelik core'a bağlantının token'ıyla sorulmadı")
	}
	// b sends without being subscribed; a receives it, b gets "sent".
	write(t, cb, `{"type":"send","id":"m1","data":{"tayfa_id":"`+tf.String()+`","channel_id":"`+ch.String()+`","body":"merhaba","client_request_id":"`+id.New().String()+`"}}`)
	f := readFrame(t, cb)
	var sent realtime.MessageData
	_ = json.Unmarshal(f.Data, &sent)
	if f.Type != "sent" || f.ID != "m1" || sent.Body != "merhaba" || sent.AuthorID != b.user.String() {
		t.Fatalf("gönderim: %+v %s", f, f.Data)
	}
	if e.ch.lastToken() != b.token {
		t.Fatal("gönderim bağlantının token'ıyla yapılmadı")
	}
	f = readFrame(t, ca)
	var got realtime.MessageData
	_ = json.Unmarshal(f.Data, &got)
	if f.Type != realtime.EventMessageCreated || got.ID != sent.ID || got.Body != "merhaba" {
		t.Fatalf("abone: %+v %s", f, f.Data)
	}

	write(t, ca, `{"type":"unsubscribe","id":"u1","data":`+ref(tf, ch)+`}`)
	if f := readFrame(t, ca); f.Type != realtime.EventUnsubscribed || f.ID != "u1" {
		t.Fatalf("çıkış: %+v", f)
	}
	if e.hub.Subscriptions() != 0 {
		t.Fatal("abonelik kaldı")
	}
}

func TestUnauthorizedSubscriptionRefused(t *testing.T) {
	e := newEnv(t, nil)
	tf, staff := id.New(), id.New()
	w := e.token(id.New(), time.Hour)
	c := e.connect(t, w)
	e.ch.set(w.user, staff, apperr.ErrChannelNotFound)
	write(t, c, `{"type":"subscribe","id":"s","data":`+ref(tf, staff)+`}`)
	if d := errorOf(t, readFrame(t, c)); d.Code != "channel_not_found" {
		t.Fatalf("staff kanalı: %+v", d)
	}
	other := id.New()
	e.ch.set(w.user, other, apperr.ErrTayfaNotFound)
	write(t, c, `{"type":"subscribe","id":"s","data":`+ref(id.New(), other)+`}`)
	if d := errorOf(t, readFrame(t, c)); d.Code != "tayfa_not_found" {
		t.Fatalf("üye olunmayan tayfa: %+v", d)
	}
	if e.hub.Subscriptions() != 0 {
		t.Fatal("yetkisiz abonelik eklendi")
	}
	// The channel's messages do not reach the refused user.
	e.live.MessageCreated(tf, channel.Message{ID: id.New(), ChannelID: staff, Body: "gizli"})
	write(t, c, `{"type":"x"}`)
	if d := errorOf(t, readFrame(t, c)); d.Code != "unsupported_type" {
		t.Fatalf("beklenmeyen çerçeve: %+v", d)
	}
}

// A moderator demoted to member: the staff subscription ends at once.
func TestRoleDropEndsStaffSubscription(t *testing.T) {
	e := newEnv(t, nil)
	tf, staff := id.New(), id.New()
	w := e.token(id.New(), time.Hour)
	c := e.connect(t, w)
	write(t, c, `{"type":"subscribe","data":`+ref(tf, staff)+`}`)
	if f := readFrame(t, c); f.Type != "subscribed" {
		t.Fatalf("%+v", f)
	}
	e.ch.set(w.user, staff, apperr.ErrChannelNotFound)
	e.live.MembershipChanged(tf, w.user)
	f := readFrame(t, c)
	var d realtime.SubscriptionData
	_ = json.Unmarshal(f.Data, &d)
	if f.Type != realtime.EventUnsubscribed || d.Reason != realtime.ReasonAccessLost || d.ChannelID != staff.String() {
		t.Fatalf("kesilme: %+v %s", f, f.Data)
	}
	if e.ch.lastToken() != w.token {
		t.Fatal("yeniden denetim bağlantının token'ıyla yapılmadı")
	}
}

func TestSendErrors(t *testing.T) {
	e := newEnv(t, nil)
	tf, ch := id.New(), id.New()
	w := e.token(id.New(), time.Hour)
	c := e.connect(t, w)
	send := func(body, req string) errorData {
		t.Helper()
		write(t, c, `{"type":"send","id":"m","data":{"tayfa_id":"`+tf.String()+`","channel_id":"`+ch.String()+`","body":"`+body+`","client_request_id":"`+req+`"}}`)
		return errorOf(t, readFrame(t, c))
	}
	if d := send("x", "kötü"); d.Code != "invalid_request" || d.Field != "client_request_id" {
		t.Fatalf("istek kimliği: %+v", d)
	}
	if d := send("", id.New().String()); d.Code != "invalid_request" || d.Field != "body" {
		t.Fatalf("boş gövde: %+v", d)
	}
	e.ch.set(w.user, ch, apperr.ErrMuted)
	if d := send("x", id.New().String()); d.Code != "muted" {
		t.Fatalf("susturma: %+v", d)
	}
	e.ch.set(w.user, ch, &apperr.SlowModeError{RetryAfter: 1500 * time.Millisecond})
	if d := send("x", id.New().String()); d.Code != "slow_mode" || d.RetryAfterSeconds != 2 {
		t.Fatalf("yavaş mod: %+v", d)
	}
	e.ch.set(w.user, ch, apperr.ErrTayfaReadOnly)
	if d := send("x", id.New().String()); d.Code != "tayfa_read_only" {
		t.Fatalf("salt okunur: %+v", d)
	}
	// The per-user message rate (issue #101; shared with REST, checked in
	// the app layer) answers the frame and keeps the connection open.
	e.ch.set(w.user, ch, apperr.ErrRateLimited)
	if d := send("x", id.New().String()); d.Code != "rate_limited" {
		t.Fatalf("hız sınırı: %+v", d)
	}
	e.ch.set(w.user, ch, nil)
	write(t, c, `{"type":"send","id":"m2","data":{"tayfa_id":"`+tf.String()+`","channel_id":"`+ch.String()+`","body":"x","client_request_id":"`+id.New().String()+`"}}`)
	if f := readFrame(t, c); f.Type == "error" {
		t.Fatalf("bağlantı hız sınırından sonra çalışmıyor: %+v", f)
	}
}

func TestFrames(t *testing.T) {
	e := newEnv(t, nil)
	c := e.connect(t, e.token(id.New(), time.Hour))
	for _, tc := range []struct{ in, id, code string }{
		{`{"type":"zap","id":"a1"}`, "a1", "unsupported_type"},
		{`not json`, "", "invalid_frame"},
		{`{"type":""}`, "", "invalid_frame"},
		{`{"type":"x","extra":1}`, "", "invalid_frame"},
		{`{"type":"x","id":"` + strings.Repeat("a", maxFrameID+1) + `"}`, "", "invalid_frame"},
		{`{"type":"subscribe","id":"b"}`, "b", "invalid_request"},
		{`{"type":"subscribe","id":"c","data":{"tayfa_id":"x","channel_id":"y"}}`, "c", "invalid_request"},
		{`{"type":"subscribe","id":"d","data":{"tayfa_id":"` + id.New().String() + `","channel_id":"y","z":1}}`, "d", "invalid_request"},
	} {
		write(t, c, tc.in)
		f := readFrame(t, c)
		if d := errorOf(t, f); d.Code != tc.code || f.ID != tc.id {
			t.Errorf("%s: %+v %+v", tc.in, f, d)
		}
	}
}

func TestOversizedFrameCloses(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxFrameBytes = 1024 })
	c := e.connect(t, e.token(id.New(), time.Hour))
	_ = c.Write(testCtx(t), websocket.MessageText, []byte(`{"type":"x","data":"`+strings.Repeat("a", 2048)+`"}`))
	if code := closedWith(t, c); code != websocket.StatusMessageTooBig {
		t.Fatalf("kapanış kodu %d", code)
	}
	waitConnections(t, e.hub, 0)
}

func TestBinaryFrameCloses(t *testing.T) {
	e := newEnv(t, nil)
	c := e.connect(t, e.token(id.New(), time.Hour))
	_ = c.Write(testCtx(t), websocket.MessageBinary, []byte{1, 2, 3})
	if code := closedWith(t, c); code != websocket.StatusUnsupportedData {
		t.Fatalf("kapanış kodu %d", code)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Frames = ratelimit.New(1, 2) })
	c := e.connect(t, e.token(id.New(), time.Hour))
	for range 2 {
		write(t, c, `{"type":"x"}`)
		if errorOf(t, readFrame(t, c)).Code != "unsupported_type" {
			t.Fatal("sınır içi çerçeve reddedildi")
		}
	}
	write(t, c, `{"type":"x"}`)
	if code := errorOf(t, readFrame(t, c)).Code; code != "rate_limited" {
		t.Fatalf("sınır aşımı: %s", code)
	}
	for range maxDenied {
		_ = c.Write(testCtx(t), websocket.MessageText, []byte(`{"type":"x"}`))
	}
	if code := closedWith(t, c); code != websocket.StatusPolicyViolation {
		t.Fatalf("kapanış kodu %d", code)
	}
}

func TestUnansweredPingCloses(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.PingInterval, c.PongTimeout = 50*time.Millisecond, 50*time.Millisecond })
	e.connect(t, e.token(id.New(), time.Hour))
	// The client stops reading, so it answers no ping.
	waitConnections(t, e.hub, 0)
}

func TestAnsweredPingKeepsConnection(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.PingInterval, c.PongTimeout = 20*time.Millisecond, time.Second })
	c := e.connect(t, e.token(id.New(), time.Hour))
	// CloseRead keeps reading in the background, which answers the pings.
	ctx := c.CloseRead(context.Background())
	time.Sleep(300 * time.Millisecond)
	if ctx.Err() != nil || e.hub.Connections() != 1 {
		t.Fatal("ping'e cevap veren bağlantı kapandı")
	}
}

func TestShutdownCloses(t *testing.T) {
	e := newEnv(t, nil)
	c := e.connect(t, e.token(id.New(), time.Hour))
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
	c := newConn(Config{SendBuffer: 1}, time.Now().Add(time.Hour))
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

// A direct message needs no subscription: both parties' connections get
// it, the sender's other device too; strangers do not. Sending is REST
// only (issue #151): the plain dm.send frame is an unknown type.
func TestDirectMessage(t *testing.T) {
	e := newEnv(t, nil)
	a, b, x := e.token(id.New(), time.Hour), e.token(id.New(), time.Hour), e.token(id.New(), time.Hour)
	ca, cb, cx := e.connect(t, a), e.connect(t, b), e.connect(t, x)
	ca2 := e.connect(t, e.token(a.user, time.Hour))

	write(t, ca, `{"type":"dm.send","id":"d1","data":{"recipient_user_id":"`+b.user.String()+`","body":"selam","client_request_id":"`+id.New().String()+`"}}`)
	if d := errorOf(t, readFrame(t, ca)); d.Code != "unsupported_type" {
		t.Fatalf("dm.send: %+v", d)
	}

	// dm.Service publishes after a REST send.
	m := dm.Message{ID: id.New(), ConversationID: id.New(), SenderID: a.user, Encryption: dm.EncryptionE2E, CreatedAt: time.Now().UTC()}
	e.live.Direct().MessageCreated([2]id.ID{a.user, b.user}, m)
	for name, c := range map[string]*websocket.Conn{"gönderen": ca, "alıcı": cb, "gönderenin diğer cihazı": ca2} {
		f := readFrame(t, c)
		var got realtime.DMData
		_ = json.Unmarshal(f.Data, &got)
		if f.Type != realtime.EventDMCreated || got.ID != m.ID.String() || got.ConversationID != m.ConversationID.String() ||
			got.SenderID != a.user.String() || got.Encryption != "e2e" || strings.Contains(string(f.Data), "body") {
			t.Fatalf("%s: %+v %s", name, f, f.Data)
		}
	}
	// A stranger gets nothing: the next frame it reads is its own answer.
	write(t, cx, `{"type":"nope","id":"z"}`)
	if f := readFrame(t, cx); f.Type != "error" || f.ID != "z" {
		t.Fatalf("yabancı: %+v %s", f, f.Data)
	}
}
