package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeEvents answers as core would: per user token and event, an error or
// allowed. It records the token each question carried.
type fakeEvents struct {
	mu     sync.Mutex
	deny   map[string]error // token + event → error
	tokens []string
	// onCheck runs inside a check (to change things during it).
	onCheck func()
}

func newFakeEvents() *fakeEvents { return &fakeEvents{deny: map[string]error{}} }

func (f *fakeEvents) set(m *Member, event id.ID, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deny[m.token+event.String()] = err
}

func (f *fakeEvents) CheckEventAccess(ctx context.Context, _, event id.ID) error {
	f.mu.Lock()
	tok := tokenOf(ctx)
	f.tokens = append(f.tokens, tok)
	err := f.deny[tok+event.String()]
	hook := f.onCheck
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (f *fakeEvents) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tokens...)
}

func newWatchEnv(t *testing.T) (*liveEnv, *fakeEvents) {
	t.Helper()
	e := newLiveEnv(t)
	fe := newFakeEvents()
	e.live.BindEvents(fe)
	return e, fe
}

func framesOf(c *fakeConn, typ string) []Event {
	var out []Event
	for _, e := range c.got() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func unwatchedReason(c *fakeConn) string {
	for _, e := range framesOf(c, EventUnwatched) {
		return e.Data.(WatchData).Reason
	}
	return ""
}

// The frame reaches only connections that watch the event and that core
// allowed, asked with the connection's own token (issue #238, ADR-0025
// §6). It carries the IDs and nothing else.
func TestComponentChangedReachesOnlyAllowedWatchers(t *testing.T) {
	e, fe := newWatchEnv(t)
	tf, ev, other := id.New(), id.New(), id.New()
	member, outsider, bystander := id.New(), id.New(), id.New()
	cm, co, cb := &fakeConn{}, &fakeConn{}, &fakeConn{}
	mm := mustAdmit(t, e.hub, member, id.New(), cm)
	mo := mustAdmit(t, e.hub, outsider, id.New(), co)
	mustAdmit(t, e.hub, bystander, id.New(), cb) // connected, watches nothing

	if err := e.live.Watch(ctx, mm, tf, ev); err != nil {
		t.Fatal(err)
	}
	if got := fe.asked(); len(got) != 1 || got[0] != mm.token {
		t.Fatalf("core'a bağlantının token'ıyla sorulmadı: %v", got)
	}
	// core refuses the outsider: no watch.
	fe.set(mo, ev, apperr.ErrTayfaNotFound)
	if err := e.live.Watch(ctx, mo, tf, ev); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("erişimsiz izleme: %v", err)
	}
	if e.hub.Watches() != 1 {
		t.Fatalf("izleme = %d", e.hub.Watches())
	}

	e.live.ComponentChanged(tf, ev, "scoreboard")
	got := framesOf(cm, EventComponentChanged)
	if len(got) != 1 {
		t.Fatalf("izleyen = %v", cm.got())
	}
	if d := got[0].Data.(ComponentChangedData); d != (ComponentChangedData{TayfaID: tf.String(), EventID: ev.String(), ComponentKey: "scoreboard"}) {
		t.Fatalf("çerçeve = %+v", d)
	}
	if len(co.got()) != 0 || len(cb.got()) != 0 {
		t.Fatalf("erişimi olmayan bağlantıya çerçeve gitti: %v %v", co.got(), cb.got())
	}
	// Another event, and the same event ID under another tayfa: nothing.
	e.live.ComponentChanged(tf, other, "scoreboard")
	e.live.ComponentChanged(id.New(), ev, "scoreboard")
	if len(framesOf(cm, EventComponentChanged)) != 1 {
		t.Fatalf("başka etkinliğin çerçevesi geldi: %v", cm.got())
	}

	// After unwatching nothing arrives; unwatching again reports false.
	if !e.live.Unwatch(mm, tf, ev) || e.live.Unwatch(mm, tf, ev) {
		t.Fatal("izlemeyi bırakma")
	}
	e.live.ComponentChanged(tf, ev, "draw")
	if len(framesOf(cm, EventComponentChanged)) != 1 {
		t.Fatal("bırakılan izlemeye çerçeve geldi")
	}
}

// Without core's answer nobody watches: the default is no.
func TestWatchNeedsCore(t *testing.T) {
	e := newLiveEnv(t) // no EventAccess bound
	m := mustAdmit(t, e.hub, id.New(), id.New(), &fakeConn{})
	if err := e.live.Watch(ctx, m, id.New(), id.New()); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("core'suz izleme: %v", err)
	}
	e2, fe := newWatchEnv(t)
	m2 := mustAdmit(t, e2.hub, id.New(), id.New(), &fakeConn{})
	ev := id.New()
	fe.set(m2, ev, apperr.ErrUnavailable)
	if err := e2.live.Watch(ctx, m2, id.New(), ev); !errors.Is(err, apperr.ErrUnavailable) || e2.hub.Watches() != 0 {
		t.Fatalf("core yanıt vermezken izleme: %v", err)
	}
}

// A member who leaves the tayfa (or is banned) loses the watch at once,
// on every device; watches in other tayfas stay.
func TestLeavingTayfaCutsWatches(t *testing.T) {
	e, fe := newWatchEnv(t)
	u, tf, tf2 := id.New(), id.New(), id.New()
	ev, ev2 := id.New(), id.New()
	c1, c2 := &fakeConn{}, &fakeConn{}
	m1 := mustAdmit(t, e.hub, u, id.New(), c1)
	m2 := mustAdmit(t, e.hub, u, id.New(), c2)
	stay := &fakeConn{}
	ms := mustAdmit(t, e.hub, id.New(), id.New(), stay)
	for _, w := range []struct {
		m      *Member
		tf, ev id.ID
	}{{m1, tf, ev}, {m2, tf, ev}, {m1, tf2, ev2}, {ms, tf, ev}} {
		if err := e.live.Watch(ctx, w.m, w.tf, w.ev); err != nil {
			t.Fatal(err)
		}
	}
	fe.set(m1, ev, apperr.ErrTayfaNotFound)
	fe.set(m2, ev, apperr.ErrTayfaNotFound)
	e.live.MembershipChanged(tf, u)
	waitFor(t, "izlemeler kesildi", func() bool { return e.hub.Watches() == 2 })
	if unwatchedReason(c1) != ReasonAccessLost || unwatchedReason(c2) != ReasonAccessLost {
		t.Fatalf("kesilme nedeni: %q %q", unwatchedReason(c1), unwatchedReason(c2))
	}
	if !e.hub.watching(m1, tf2, ev2) || !e.hub.watching(ms, tf, ev) {
		t.Fatal("başka tayfanın ya da başka üyenin izlemesi kesildi")
	}
	e.live.ComponentChanged(tf, ev, "scoreboard")
	if len(framesOf(c1, EventComponentChanged)) != 0 || len(framesOf(c2, EventComponentChanged)) != 0 {
		t.Fatal("ayrılan üyeye çerçeve gitti")
	}
	if len(framesOf(stay, EventComponentChanged)) != 1 {
		t.Fatal("kalan üyeye çerçeve gitmedi")
	}
}

// A recheck core cannot answer cuts the watch (the client may watch
// again); a purged tayfa cuts without asking.
func TestWatchRecheckFailureAndPurge(t *testing.T) {
	e, fe := newWatchEnv(t)
	u, tf, ev := id.New(), id.New(), id.New()
	c := &fakeConn{}
	m := mustAdmit(t, e.hub, u, id.New(), c)
	if err := e.live.Watch(ctx, m, tf, ev); err != nil {
		t.Fatal(err)
	}
	fe.set(m, ev, apperr.ErrUnavailable)
	e.live.TayfaChanged(tf)
	waitFor(t, "izleme kesildi", func() bool { return e.hub.Watches() == 0 })
	if unwatchedReason(c) != ReasonRecheckFailed {
		t.Fatalf("neden = %q", unwatchedReason(c))
	}

	fe.set(m, ev, nil)
	c2 := &fakeConn{}
	m2 := mustAdmit(t, e.hub, id.New(), id.New(), c2)
	if err := e.live.Watch(ctx, m2, tf, ev); err != nil {
		t.Fatal(err)
	}
	asked := len(fe.asked())
	e.live.TayfaPurged(tf)
	if e.hub.Watches() != 0 || unwatchedReason(c2) != ReasonAccessLost || len(fe.asked()) != asked {
		t.Fatalf("silinen tayfa: izleme=%d neden=%q", e.hub.Watches(), unwatchedReason(c2))
	}
}

// A change in the tayfa during the check repeats the question, so a
// member removed meanwhile is not left watching.
func TestWatchRepeatsWhenTayfaChanges(t *testing.T) {
	e, fe := newWatchEnv(t)
	u, tf, ev := id.New(), id.New(), id.New()
	m := mustAdmit(t, e.hub, u, id.New(), &fakeConn{})
	var once sync.Once
	fe.mu.Lock()
	fe.onCheck = func() {
		once.Do(func() {
			fe.set(m, ev, apperr.ErrTayfaNotFound)
			e.hub.bump(tf)
		})
	}
	fe.mu.Unlock()
	if err := e.live.Watch(ctx, m, tf, ev); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("değişen tayfada izleme: %v", err)
	}
	if e.hub.Watches() != 0 || len(fe.asked()) != 2 {
		t.Fatalf("izleme=%d soru=%d", e.hub.Watches(), len(fe.asked()))
	}
}

// Watches are bounded per connection and go with the connection.
func TestWatchLimitAndRelease(t *testing.T) {
	h, err := NewHub(HubConfig{MaxTotal: 10, MaxPerUser: 2, MaxSubscriptions: 3, MaxWatches: 2})
	if err != nil {
		t.Fatal(err)
	}
	tf := id.New()
	m := mustAdmit(t, h, id.New(), id.New(), &fakeConn{})
	a, b := id.New(), id.New()
	for _, ev := range []id.ID{a, b, a} { // watching again is a no-op
		if err := h.watch(m, tf, ev, h.generation(tf)); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.watch(m, tf, id.New(), h.generation(tf)); !errors.Is(err, ErrTooManyWatches) {
		t.Fatalf("sınır: %v", err)
	}
	if h.Watches() != 2 {
		t.Fatalf("izleme = %d", h.Watches())
	}
	m.Release()
	if h.Watches() != 0 {
		t.Fatal("bağlantı gidince izleme kaldı")
	}
	if err := h.watch(m, tf, a, h.generation(tf)); !errors.Is(err, ErrServerFull) {
		t.Fatalf("bırakılmış bağlantıda izleme: %v", err)
	}
	// The default bound when none is configured.
	d, err := NewHub(HubConfig{MaxTotal: 1, MaxPerUser: 1, MaxSubscriptions: 1})
	if err != nil || d.cfg.MaxWatches != DefaultMaxWatches {
		t.Fatalf("varsayılan sınır: %v %v", d, err)
	}
}
