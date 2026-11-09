package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func tsAt(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func ticketHash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func TestRealtimeTicketIsSingleUse(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user, session := id.New(), id.New()
	create := func(name string, created time.Time) {
		t.Helper()
		if err := q.CreateRealtimeTicket(ctx, CreateRealtimeTicketParams{
			ID: id.New().PG(), TokenHash: ticketHash(name), UserID: user.PG(), SessionID: session.PG(),
			CreatedAt: tsAt(created), ExpiresAt: tsAt(created.Add(30 * time.Second)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	create("a", now)
	create("eski", now.Add(-time.Minute))

	if n, err := q.CountLiveRealtimeTickets(ctx, CountLiveRealtimeTicketsParams{UserID: user.PG(), Now: tsAt(now)}); err != nil || n != 1 {
		t.Fatalf("canlı bilet: %d %v", n, err)
	}
	r, err := q.RedeemRealtimeTicket(ctx, RedeemRealtimeTicketParams{TokenHash: ticketHash("a"), Now: tsAt(now)})
	if err != nil || id.ID(r.UserID.Bytes) != user || id.ID(r.SessionID.Bytes) != session {
		t.Fatalf("bilet: %+v %v", r, err)
	}
	// Used once: gone.
	if _, err := q.RedeemRealtimeTicket(ctx, RedeemRealtimeTicketParams{TokenHash: ticketHash("a"), Now: tsAt(now)}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ikinci kullanım: %v", err)
	}
	// Expired: refused, and removed as well.
	if _, err := q.RedeemRealtimeTicket(ctx, RedeemRealtimeTicketParams{TokenHash: ticketHash("eski"), Now: tsAt(now)}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("süresi dolan: %v", err)
	}
	if n, _ := q.CountLiveRealtimeTickets(ctx, CountLiveRealtimeTicketsParams{UserID: user.PG(), Now: tsAt(now.Add(-time.Hour))}); n != 0 {
		t.Fatalf("kalan bilet: %d", n)
	}
}

func TestRealtimeTicketConstraintsAndCleanup(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	user, session := id.New(), id.New()
	for name, p := range map[string]CreateRealtimeTicketParams{
		"kısa özet": {TokenHash: []byte("x"), CreatedAt: tsAt(now), ExpiresAt: tsAt(now.Add(time.Second))},
		"uzun ömür": {TokenHash: ticketHash("u"), CreatedAt: tsAt(now), ExpiresAt: tsAt(now.Add(time.Hour))},
		"geçmiş":    {TokenHash: ticketHash("g"), CreatedAt: tsAt(now), ExpiresAt: tsAt(now)},
	} {
		p.ID, p.UserID, p.SessionID = id.New().PG(), user.PG(), session.PG()
		if err := q.CreateRealtimeTicket(ctx, p); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	add := func(name string, u, s id.ID, created time.Time) {
		t.Helper()
		if err := q.CreateRealtimeTicket(ctx, CreateRealtimeTicketParams{
			ID: id.New().PG(), TokenHash: ticketHash(name), UserID: u.PG(), SessionID: s.PG(),
			CreatedAt: tsAt(created), ExpiresAt: tsAt(created.Add(30 * time.Second)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	other := id.New()
	add("s1", user, session, now)
	add("s2", user, id.New(), now)
	add("o", other, id.New(), now)
	add("eski", other, id.New(), now.Add(-time.Hour))
	if n, err := q.DeleteExpiredRealtimeTickets(ctx, tsAt(now)); err != nil || n != 1 {
		t.Fatalf("süresi dolan: %d %v", n, err)
	}
	if err := q.DeleteSessionRealtimeTickets(ctx, session.PG()); err != nil {
		t.Fatal(err)
	}
	if n, _ := q.CountLiveRealtimeTickets(ctx, CountLiveRealtimeTicketsParams{UserID: user.PG(), Now: tsAt(now)}); n != 1 {
		t.Fatalf("oturum silindikten sonra: %d", n)
	}
	if err := q.DeleteUserRealtimeTickets(ctx, user.PG()); err != nil {
		t.Fatal(err)
	}
	if n, _ := q.CountLiveRealtimeTickets(ctx, CountLiveRealtimeTicketsParams{UserID: user.PG(), Now: tsAt(now)}); n != 0 {
		t.Fatalf("kullanıcı silindikten sonra: %d", n)
	}
	if n, _ := q.CountLiveRealtimeTickets(ctx, CountLiveRealtimeTicketsParams{UserID: other.PG(), Now: tsAt(now)}); n != 1 {
		t.Fatalf("başkasının bileti: %d", n)
	}
}
