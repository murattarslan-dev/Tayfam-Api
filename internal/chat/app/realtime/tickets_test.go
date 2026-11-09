package realtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func newTickets(t *testing.T) (*Tickets, *Hub, *clock, Sessions) {
	t.Helper()
	pool := dbtest.New(t, chatdb.Migrations())
	h, clk := newTestHub(t, 100, 5)
	tk, err := NewTickets(TicketsConfig{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Ended: h, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	return tk, h, clk, Sessions{Pool: pool, Hub: h}
}

func TestTicketIssueAndRedeemOnce(t *testing.T) {
	tk, _, clk, _ := newTickets(t)
	u, s := id.New(), id.New()
	tick, err := tk.Issue(ctx, u, s)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidTicketFormat(tick.Value) || !tick.ExpiresAt.Equal(clk.now().Add(TicketTTL)) {
		t.Fatalf("bilet: %+v", tick)
	}
	who, err := tk.Redeem(ctx, tick.Value)
	if err != nil || who != (Identity{User: u, Session: s}) {
		t.Fatalf("kullanım: %+v %v", who, err)
	}
	if _, err := tk.Redeem(ctx, tick.Value); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("tekrar kullanım: %v", err)
	}
	for name, v := range map[string]string{
		"boş": "", "kısa": "abc", "biçim": strings.Repeat("*", len(tick.Value)), "bilinmeyen": strings.Repeat("A", len(tick.Value)),
	} {
		if _, err := tk.Redeem(ctx, v); !errors.Is(err, ErrInvalidTicket) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTicketExpires(t *testing.T) {
	tk, _, clk, _ := newTickets(t)
	tick, err := tk.Issue(ctx, id.New(), id.New())
	if err != nil {
		t.Fatal(err)
	}
	clk.add(TicketTTL)
	if _, err := tk.Redeem(ctx, tick.Value); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("süresi dolan bilet: %v", err)
	}
	// Purge removes the expired ones.
	if _, err := tk.Issue(ctx, id.New(), id.New()); err != nil {
		t.Fatal(err)
	}
	clk.add(time.Minute)
	if n, err := tk.Purge(ctx); err != nil || n != 1 {
		t.Fatalf("temizlik: %d %v", n, err)
	}
}

func TestTicketLimit(t *testing.T) {
	tk, _, clk, _ := newTickets(t)
	u := id.New()
	for range MaxLiveTickets {
		if _, err := tk.Issue(ctx, u, id.New()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tk.Issue(ctx, u, id.New()); !errors.Is(err, apperr.ErrTicketLimit) {
		t.Fatalf("sınır: %v", err)
	}
	if _, err := tk.Issue(ctx, id.New(), id.New()); err != nil {
		t.Fatalf("başka kullanıcı: %v", err)
	}
	clk.add(TicketTTL)
	if _, err := tk.Issue(ctx, u, id.New()); err != nil {
		t.Fatalf("süre dolunca: %v", err)
	}
}

func TestEndedSessionGetsNoTicketAndLosesUnusedOnes(t *testing.T) {
	tk, h, _, sessions := newTickets(t)
	u, s := id.New(), id.New()
	unused, err := tk.Issue(ctx, u, s)
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeConn{}
	if _, err := h.Admit(u, s, c); err != nil {
		t.Fatal(err)
	}
	if err := sessions.EndSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	if r := c.closed(); len(r) != 1 || r[0] != CloseSessionEnded {
		t.Fatalf("bağlantı kapanmadı: %v", r)
	}
	if _, err := tk.Redeem(ctx, unused.Value); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("kullanılmamış bilet: %v", err)
	}
	if _, err := tk.Issue(ctx, u, s); !errors.Is(err, apperr.ErrSessionEnded) {
		t.Fatalf("yeni bilet: %v", err)
	}
	// Another session of the user still works.
	if _, err := tk.Issue(ctx, u, id.New()); err != nil {
		t.Fatal(err)
	}
	if err := sessions.EndUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Issue(ctx, u, id.New()); !errors.Is(err, apperr.ErrSessionEnded) {
		t.Fatalf("yaptırım sonrası: %v", err)
	}
}

func TestRedeemRefusesTicketOfSessionEndedAfterIssue(t *testing.T) {
	tk, h, _, _ := newTickets(t)
	u, s := id.New(), id.New()
	tick, err := tk.Issue(ctx, u, s)
	if err != nil {
		t.Fatal(err)
	}
	h.EndSession(s) // memory only, as if the database delete had failed
	if _, err := tk.Redeem(ctx, tick.Value); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("kapatılan oturumun bileti: %v", err)
	}
}

func TestCleanupRemovesUserTickets(t *testing.T) {
	pool := dbtest.New(t, chatdb.Migrations())
	h, clk := newTestHub(t, 10, 5)
	tk, err := NewTickets(TicketsConfig{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Ended: h, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	u := id.New()
	tick, err := tk.Issue(ctx, u, id.New())
	if err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(ctx, store.New(pool), u); err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Redeem(ctx, tick.Value); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("silinen kullanıcının bileti: %v", err)
	}
}
