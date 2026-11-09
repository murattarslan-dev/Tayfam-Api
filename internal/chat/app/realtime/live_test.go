package realtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeChannels answers as core would: per user and channel, an error or
// allowed. It records the token each call carried.
type fakeChannels struct {
	mu     sync.Mutex
	deny   map[[2]id.ID]error // (user, channel) → error
	tokens []string
	sent   []channel.SendInput
	// onGet runs inside Get (to change things during a check).
	onGet func()
}

func newFakeChannels() *fakeChannels { return &fakeChannels{deny: map[[2]id.ID]error{}} }

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
	f.tokens = append(f.tokens, tokenOf(ctx))
	err := f.deny[[2]id.ID{caller, ch}]
	hook := f.onGet
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return channel.Channel{ID: ch, TayfaID: tayfa}, err
}

func (f *fakeChannels) Send(ctx context.Context, caller, tayfa, ch id.ID, in channel.SendInput) (channel.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, tokenOf(ctx))
	f.sent = append(f.sent, in)
	if err := f.deny[[2]id.ID{caller, ch}]; err != nil {
		return channel.Message{}, err
	}
	return channel.Message{ID: id.New(), ChannelID: ch, AuthorID: caller, Body: in.Body, CreatedAt: time.Now()}, nil
}

func (f *fakeChannels) lastToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[len(f.tokens)-1]
}

type liveEnv struct {
	hub  *Hub
	live *Live
	ch   *fakeChannels
}

func newLiveEnv(t *testing.T) *liveEnv {
	t.Helper()
	h, _ := newTestHub(t, 100, 5)
	fc := newFakeChannels()
	l, err := NewLive(LiveConfig{Hub: h, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	l.Bind(fc)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return &liveEnv{hub: h, live: l, ch: fc}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("beklenen olmadı: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func unsubscribedReason(c *fakeConn) string {
	for _, e := range c.got() {
		if e.Type == EventUnsubscribed {
			return e.Data.(SubscriptionData).Reason
		}
	}
	return ""
}

// Subscribing asks core with the connection's own token; a refusal
// subscribes nothing.
func TestSubscribeAsksWithConnectionToken(t *testing.T) {
	e := newLiveEnv(t)
	u, tf, ch, staff := id.New(), id.New(), id.New(), id.New()
	m := mustAdmit(t, e.hub, u, id.New(), &fakeConn{})
	if err := e.live.Subscribe(ctx, m, tf, ch); err != nil {
		t.Fatal(err)
	}
	if got := e.ch.lastToken(); got != "TOKEN-"+u.String() {
		t.Fatalf("core'a giden token %q", got)
	}
	e.ch.set(u, staff, apperr.ErrChannelNotFound) // a staff channel for a plain member
	if err := e.live.Subscribe(ctx, m, tf, staff); !errors.Is(err, apperr.ErrChannelNotFound) {
		t.Fatalf("yetkisiz abonelik: %v", err)
	}
	e.ch.set(u, ch, apperr.ErrTayfaNotFound)
	other := id.New()
	e.ch.set(u, other, apperr.ErrTayfaNotFound)
	if err := e.live.Subscribe(ctx, m, id.New(), other); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("üye olunmayan tayfa: %v", err)
	}
	if e.hub.Subscriptions() != 1 {
		t.Fatalf("abonelik sayısı %d", e.hub.Subscriptions())
	}
}

// A change during the check repeats it, so the newer answer wins.
func TestSubscribeRepeatsWhenTayfaChanges(t *testing.T) {
	e := newLiveEnv(t)
	u, tf, ch := id.New(), id.New(), id.New()
	m := mustAdmit(t, e.hub, u, id.New(), &fakeConn{})
	calls := 0
	e.ch.onGet = func() {
		calls++
		if calls == 1 {
			// membership.changed lands while core answers the first time:
			// the user was demoted meanwhile.
			e.ch.set(u, ch, apperr.ErrChannelNotFound)
			e.live.MembershipChanged(tf, u)
		}
	}
	if err := e.live.Subscribe(ctx, m, tf, ch); !errors.Is(err, apperr.ErrChannelNotFound) {
		t.Fatalf("eski cevapla abone olundu: %v", err)
	}
	if e.hub.Subscriptions() != 0 {
		t.Fatal("abonelik kaldı")
	}
}

// A moderator demoted to member loses the staff channel subscription at
// once; the members channel stays.
func TestRoleDropCutsStaffSubscription(t *testing.T) {
	e := newLiveEnv(t)
	u, tf, staff, members := id.New(), id.New(), id.New(), id.New()
	c := &fakeConn{}
	m := mustAdmit(t, e.hub, u, id.New(), c)
	for _, ch := range []id.ID{staff, members} {
		if err := e.live.Subscribe(ctx, m, tf, ch); err != nil {
			t.Fatal(err)
		}
	}
	other := &fakeConn{}
	o := id.New()
	om := mustAdmit(t, e.hub, o, id.New(), other)
	if err := e.live.Subscribe(ctx, om, tf, staff); err != nil {
		t.Fatal(err)
	}

	e.ch.set(u, staff, apperr.ErrChannelNotFound) // core: no longer staff
	e.live.MembershipChanged(tf, u)
	waitFor(t, "staff aboneliği kesildi", func() bool { return !e.hub.subscribed(m, tf, staff) })
	if !e.hub.subscribed(m, tf, members) || !e.hub.subscribed(om, tf, staff) {
		t.Fatal("başka abonelik kesildi")
	}
	if r := unsubscribedReason(c); r != ReasonAccessLost {
		t.Fatalf("sebep %q", r)
	}
	if got := e.ch.lastToken(); got != "TOKEN-"+u.String() {
		t.Fatalf("yeniden denetimde token %q", got)
	}
	// Messages of the staff channel no longer reach the demoted user.
	e.live.MessageCreated(tf, channel.Message{ID: id.New(), ChannelID: staff, Body: "gizli"})
	for _, ev := range c.got() {
		if ev.Type == EventMessageCreated {
			t.Fatal("staff mesajı rolü düşen kullanıcıya gitti")
		}
	}
	if len(other.got()) != 1 || other.got()[0].Type != EventMessageCreated {
		t.Fatalf("yetkili abone mesajı almadı: %v", other.got())
	}
}

func TestLeavingTayfaCutsEverySubscriptionInIt(t *testing.T) {
	e := newLiveEnv(t)
	u, tf, tf2 := id.New(), id.New(), id.New()
	c1, c2, c3 := id.New(), id.New(), id.New()
	m := mustAdmit(t, e.hub, u, id.New(), &fakeConn{})
	m2 := mustAdmit(t, e.hub, u, id.New(), &fakeConn{}) // a second device
	for _, s := range []struct {
		m      *Member
		tf, ch id.ID
	}{{m, tf, c1}, {m, tf, c2}, {m2, tf, c1}, {m, tf2, c3}} {
		if err := e.live.Subscribe(ctx, s.m, s.tf, s.ch); err != nil {
			t.Fatal(err)
		}
	}
	for _, ch := range []id.ID{c1, c2} {
		e.ch.set(u, ch, apperr.ErrTayfaNotFound) // left or banned
	}
	e.live.MembershipChanged(tf, u)
	waitFor(t, "tayfa abonelikleri kesildi", func() bool { return e.hub.Subscriptions() == 1 })
	if !e.hub.subscribed(m, tf2, c3) {
		t.Fatal("başka tayfanın aboneliği kesildi")
	}
}

func TestFailedRecheckCuts(t *testing.T) {
	e := newLiveEnv(t)
	u, tf, ch := id.New(), id.New(), id.New()
	c := &fakeConn{}
	m := mustAdmit(t, e.hub, u, id.New(), c)
	if err := e.live.Subscribe(ctx, m, tf, ch); err != nil {
		t.Fatal(err)
	}
	e.ch.set(u, ch, apperr.ErrUnavailable)
	e.live.TayfaChanged(tf)
	waitFor(t, "abonelik kesildi", func() bool { return e.hub.Subscriptions() == 0 })
	if r := unsubscribedReason(c); r != ReasonRecheckFailed {
		t.Fatalf("sebep %q", r)
	}
}

func TestChannelMadeStaffCutsPlainMembers(t *testing.T) {
	e := newLiveEnv(t)
	tf, ch := id.New(), id.New()
	member, mod := id.New(), id.New()
	mm := mustAdmit(t, e.hub, member, id.New(), &fakeConn{})
	mo := mustAdmit(t, e.hub, mod, id.New(), &fakeConn{})
	for _, m := range []*Member{mm, mo} {
		if err := e.live.Subscribe(ctx, m, tf, ch); err != nil {
			t.Fatal(err)
		}
	}
	e.ch.set(member, ch, apperr.ErrChannelNotFound)
	e.live.ChannelChanged(tf, ch)
	waitFor(t, "üye aboneliği kesildi", func() bool { return !e.hub.subscribed(mm, tf, ch) })
	if !e.hub.subscribed(mo, tf, ch) {
		t.Fatal("moderatör aboneliği kesildi")
	}
}

func TestTayfaPurgedCutsWithoutAsking(t *testing.T) {
	e := newLiveEnv(t)
	tf, ch := id.New(), id.New()
	c := &fakeConn{}
	m := mustAdmit(t, e.hub, id.New(), id.New(), c)
	if err := e.live.Subscribe(ctx, m, tf, ch); err != nil {
		t.Fatal(err)
	}
	e.live.TayfaPurged(tf)
	if e.hub.Subscriptions() != 0 || unsubscribedReason(c) != ReasonAccessLost {
		t.Fatal("silinen tayfanın aboneliği kaldı")
	}
}

func TestSendGoesThroughChannelServiceWithToken(t *testing.T) {
	e := newLiveEnv(t)
	u, tf, ch := id.New(), id.New(), id.New()
	m := mustAdmit(t, e.hub, u, id.New(), &fakeConn{})
	req := id.New()
	msg, err := e.live.Send(ctx, m, tf, ch, channel.SendInput{Body: "merhaba", ClientRequestID: req})
	if err != nil || msg.Body != "merhaba" {
		t.Fatalf("%+v %v", msg, err)
	}
	if e.ch.lastToken() != "TOKEN-"+u.String() || e.ch.sent[0].ClientRequestID != req {
		t.Fatal("gönderim kullanıcının token'ıyla Send'den geçmedi")
	}
	e.ch.set(u, ch, apperr.ErrMuted)
	if _, err := e.live.Send(ctx, m, tf, ch, channel.SendInput{Body: "x", ClientRequestID: id.New()}); !errors.Is(err, apperr.ErrMuted) {
		t.Fatalf("susturulmuş: %v", err)
	}
}

func TestPublishReachesOnlySubscribers(t *testing.T) {
	e := newLiveEnv(t)
	tf, ch := id.New(), id.New()
	sub, other := &fakeConn{}, &fakeConn{}
	m := mustAdmit(t, e.hub, id.New(), id.New(), sub)
	mustAdmit(t, e.hub, id.New(), id.New(), other)
	if err := e.live.Subscribe(ctx, m, tf, ch); err != nil {
		t.Fatal(err)
	}
	author, msgID := id.New(), id.New()
	e.live.MessageCreated(tf, channel.Message{ID: msgID, ChannelID: ch, AuthorID: author, Body: "selam"})
	e.live.MessageDeleted(tf, ch, msgID)
	e.live.MessageRestored(tf, channel.Message{ID: msgID, ChannelID: ch, Body: "selam"})
	got := sub.got()
	if len(got) != 3 || got[0].Type != EventMessageCreated || got[1].Type != EventMessageDeleted || got[2].Type != EventMessageRestored {
		t.Fatalf("aboneye gelenler: %+v", got)
	}
	if d := got[0].Data.(MessageData); d.Body != "selam" || d.AuthorID != author.String() || d.TayfaID != tf.String() {
		t.Fatalf("mesaj: %+v", d)
	}
	if len(other.got()) != 0 {
		t.Fatal("abone olmayana mesaj gitti")
	}
}

func TestNotifiedReachesRecipients(t *testing.T) {
	e := newLiveEnv(t)
	r1, r2 := id.New(), id.New()
	a1, a2, b, x := &fakeConn{}, &fakeConn{}, &fakeConn{}, &fakeConn{}
	mustAdmit(t, e.hub, r1, id.New(), a1)
	mustAdmit(t, e.hub, r1, id.New(), a2)
	mustAdmit(t, e.hub, r2, id.New(), b)
	mustAdmit(t, e.hub, id.New(), id.New(), x)
	tf, target := id.New(), id.New()
	e.live.Notified(notification.Request{Tayfa: tf, Type: notification.TypeAnnouncement, TargetType: notification.TargetAnnouncement, Target: target, Recipients: []id.ID{r1, r2}})
	for _, c := range []*fakeConn{a1, a2, b} {
		got := c.got()
		if len(got) != 1 || got[0].Type != EventNotification {
			t.Fatalf("alıcı: %+v", got)
		}
		if d := got[0].Data.(NotificationData); d.TargetID != target.String() || d.TayfaID != tf.String() || d.Type != notification.TypeAnnouncement {
			t.Fatalf("bildirim: %+v", d)
		}
	}
	if len(x.got()) != 0 {
		t.Fatal("alıcı olmayana bildirim gitti")
	}
}
