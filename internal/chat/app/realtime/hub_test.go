package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

type fakeConn struct {
	mu      sync.Mutex
	reasons []CloseReason
	events  []Event
}

func (c *fakeConn) Close(r CloseReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reasons = append(c.reasons, r)
}

func (c *fakeConn) Deliver(e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *fakeConn) closed() []CloseReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]CloseReason(nil), c.reasons...)
}

func (c *fakeConn) got() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.events...)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestHub(t *testing.T, total, perUser int) (*Hub, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	h, err := NewHub(HubConfig{MaxTotal: total, MaxPerUser: perUser, MaxSubscriptions: 3, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return h, c
}

func mustAdmit(t *testing.T, h *Hub, user, session id.ID, c Conn) *Member {
	t.Helper()
	m, err := h.Admit(user, session, "TOKEN-"+user.String(), c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHubLimits(t *testing.T) {
	h, _ := newTestHub(t, 3, 2)
	u, v := id.New(), id.New()
	m1 := mustAdmit(t, h, u, id.New(), &fakeConn{})
	mustAdmit(t, h, u, id.New(), &fakeConn{})
	if _, err := h.Admit(u, id.New(), "T", &fakeConn{}); !errors.Is(err, ErrTooManyConnections) {
		t.Fatalf("kullanıcı sınırı: %v", err)
	}
	mustAdmit(t, h, v, id.New(), &fakeConn{})
	if _, err := h.Admit(id.New(), id.New(), "T", &fakeConn{}); !errors.Is(err, ErrServerFull) {
		t.Fatalf("toplam sınır: %v", err)
	}
	m1.Release()
	m1.Release() // releasing twice frees one place only
	if h.Connections() != 2 {
		t.Fatalf("bağlantı sayısı %d", h.Connections())
	}
	mustAdmit(t, h, u, id.New(), &fakeConn{})
	if _, err := h.Admit(id.New(), id.New(), "T", &fakeConn{}); !errors.Is(err, ErrServerFull) {
		t.Fatalf("toplam sınır (ikinci): %v", err)
	}
}

func TestHubSubscriptions(t *testing.T) {
	h, _ := newTestHub(t, 10, 5)
	tf := id.New()
	a, b := &fakeConn{}, &fakeConn{}
	ma := mustAdmit(t, h, id.New(), id.New(), a)
	mb := mustAdmit(t, h, id.New(), id.New(), b)
	c1, c2, c3, c4 := id.New(), id.New(), id.New(), id.New()
	for _, ch := range []id.ID{c1, c2, c3} {
		if err := h.subscribe(ma, tf, ch, h.generation(tf)); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.subscribe(ma, tf, c1, h.generation(tf)); err != nil {
		t.Fatalf("aynı abonelik tekrar: %v", err)
	}
	if err := h.subscribe(ma, tf, c4, h.generation(tf)); !errors.Is(err, ErrTooManySubscriptions) {
		t.Fatalf("abonelik sınırı: %v", err)
	}
	// A change between the check and the subscription is not missed.
	g := h.generation(tf)
	h.bump(tf)
	if err := h.subscribe(mb, tf, c1, g); !errors.Is(err, errChanged) {
		t.Fatalf("değişiklik sonrası abonelik: %v", err)
	}
	// Another tayfa's ID with the same channel ID gets nothing.
	if err := h.subscribe(mb, id.New(), c1, 0); err != nil {
		t.Fatal(err)
	}
	h.publish(tf, c1, Event{Type: "x"})
	if len(a.got()) != 1 || len(b.got()) != 0 {
		t.Fatalf("yayılım: %v %v", a.got(), b.got())
	}
	if !h.unsubscribe(ma, tf, c1) || h.unsubscribe(ma, tf, c1) {
		t.Fatal("abonelikten çıkma")
	}
	h.publish(tf, c1, Event{Type: "x"})
	if len(a.got()) != 1 {
		t.Fatal("çıkılan kanaldan çerçeve geldi")
	}
	ma.Release()
	if h.Subscriptions() != 1 {
		t.Fatalf("kapanan bağlantının abonelikleri kaldı: %d", h.Subscriptions())
	}
	if err := h.subscribe(ma, tf, c1, h.generation(tf)); err == nil {
		t.Fatal("kapanan bağlantıya abonelik eklendi")
	}
}

func TestHubEndSessionClosesOnlyThatSession(t *testing.T) {
	h, clk := newTestHub(t, 10, 5)
	u := id.New()
	s1, s2 := id.New(), id.New()
	a, b, c := &fakeConn{}, &fakeConn{}, &fakeConn{}
	mustAdmit(t, h, u, s1, a)
	mustAdmit(t, h, u, s1, b)
	mustAdmit(t, h, u, s2, c)

	if err := h.EndSession(ctx, s1); err != nil {
		t.Fatal(err)
	}
	if len(a.closed()) != 1 || a.closed()[0] != CloseSessionEnded || len(b.closed()) != 1 || len(c.closed()) != 0 {
		t.Fatalf("kapananlar: %v %v %v", a.closed(), b.closed(), c.closed())
	}
	// The ended session cannot connect again while its token may live.
	if _, err := h.Admit(u, s1, "T", &fakeConn{}); !errors.Is(err, apperr.ErrSessionEnded) {
		t.Fatalf("kapatılan oturum: %v", err)
	}
	if !h.Ended(u, s1) || h.Ended(u, s2) {
		t.Fatal("Ended yanlış")
	}
	clk.add(RevokedFor)
	h.sweep()
	if h.Ended(u, s1) {
		t.Fatal("süre dolunca oturum hâlâ kapalı")
	}
}

func TestHubEndUserClosesEverySession(t *testing.T) {
	h, _ := newTestHub(t, 10, 5)
	u, other := id.New(), id.New()
	a, b, c := &fakeConn{}, &fakeConn{}, &fakeConn{}
	mustAdmit(t, h, u, id.New(), a)
	mustAdmit(t, h, u, id.New(), b)
	mustAdmit(t, h, other, id.New(), c)
	if err := h.EndUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if len(a.closed()) != 1 || len(b.closed()) != 1 || len(c.closed()) != 0 {
		t.Fatalf("kapananlar: %v %v %v", a.closed(), b.closed(), c.closed())
	}
	if _, err := h.Admit(u, id.New(), "T", &fakeConn{}); !errors.Is(err, apperr.ErrSessionEnded) {
		t.Fatalf("yaptırımlı kullanıcı yeni oturumla: %v", err)
	}
}

func TestHubRunClosesEverythingOnShutdown(t *testing.T) {
	h, _ := newTestHub(t, 10, 5)
	a := &fakeConn{}
	mustAdmit(t, h, id.New(), id.New(), a)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- h.Run(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r := a.closed(); len(r) != 1 || r[0] != CloseShutdown {
		t.Fatalf("kapanış: %v", r)
	}
	if _, err := h.Admit(id.New(), id.New(), "T", &fakeConn{}); !errors.Is(err, ErrServerFull) {
		t.Fatalf("kapanırken kabul: %v", err)
	}
}

func TestHubOnline(t *testing.T) {
	h, _ := newTestHub(t, 10, 5)
	u := id.New()
	if h.Online(u) {
		t.Fatal("bağlantısız kullanıcı çevrimiçi")
	}
	m := mustAdmit(t, h, u, id.New(), &fakeConn{})
	if !h.Online(u) || h.Online(id.New()) {
		t.Fatal("çevrimiçi bilgisi yanlış")
	}
	m.Release()
	if h.Online(u) {
		t.Fatal("bağlantı kapandı, hâlâ çevrimiçi")
	}
}
