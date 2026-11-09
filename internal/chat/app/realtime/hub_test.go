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

type fakeConn struct {
	mu      sync.Mutex
	reasons []CloseReason
}

func (c *fakeConn) Close(r CloseReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reasons = append(c.reasons, r)
}

func (c *fakeConn) closed() []CloseReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]CloseReason(nil), c.reasons...)
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
	h, err := NewHub(HubConfig{MaxTotal: total, MaxPerUser: perUser, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return h, c
}

func mustAdmit(t *testing.T, h *Hub, user, session id.ID, c Conn) func() {
	t.Helper()
	release, err := h.Admit(user, session, c)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestHubLimits(t *testing.T) {
	h, _ := newTestHub(t, 3, 2)
	u, v := id.New(), id.New()
	r1 := mustAdmit(t, h, u, id.New(), &fakeConn{})
	mustAdmit(t, h, u, id.New(), &fakeConn{})
	if _, err := h.Admit(u, id.New(), &fakeConn{}); !errors.Is(err, ErrTooManyConnections) {
		t.Fatalf("kullanıcı sınırı: %v", err)
	}
	mustAdmit(t, h, v, id.New(), &fakeConn{})
	if _, err := h.Admit(id.New(), id.New(), &fakeConn{}); !errors.Is(err, ErrServerFull) {
		t.Fatalf("toplam sınır: %v", err)
	}
	r1()
	r1() // releasing twice frees one place only
	if h.Connections() != 2 {
		t.Fatalf("bağlantı sayısı %d", h.Connections())
	}
	mustAdmit(t, h, u, id.New(), &fakeConn{})
	if _, err := h.Admit(id.New(), id.New(), &fakeConn{}); !errors.Is(err, ErrServerFull) {
		t.Fatalf("toplam sınır (ikinci): %v", err)
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

	h.EndSession(s1)
	if len(a.closed()) != 1 || a.closed()[0] != CloseSessionEnded || len(b.closed()) != 1 || len(c.closed()) != 0 {
		t.Fatalf("kapananlar: %v %v %v", a.closed(), b.closed(), c.closed())
	}
	// The ended session cannot connect again while its token may live.
	if _, err := h.Admit(u, s1, &fakeConn{}); !errors.Is(err, apperr.ErrSessionEnded) {
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
	h.EndUser(u)
	if len(a.closed()) != 1 || len(b.closed()) != 1 || len(c.closed()) != 0 {
		t.Fatalf("kapananlar: %v %v %v", a.closed(), b.closed(), c.closed())
	}
	if _, err := h.Admit(u, id.New(), &fakeConn{}); !errors.Is(err, apperr.ErrSessionEnded) {
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
	if _, err := h.Admit(id.New(), id.New(), &fakeConn{}); !errors.Is(err, ErrServerFull) {
		t.Fatalf("kapanırken kabul: %v", err)
	}
}
