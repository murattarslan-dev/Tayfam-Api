package social

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

type env struct {
	svc  *Service
	pool *pgxpool.Pool
	q    *store.Queries
	// admin is a superuser pool for fixtures core_app cannot write (moving
	// outbox events back in time, ADR-0015).
	admin *pgxpool.Pool
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	pool := d.App
	svc, err := New(Config{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, pool: pool, q: store.New(pool), admin: d.Admin}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// user creates a user with a profile copy, as identity's event would.
func (e *env) user(t *testing.T, name string) id.ID {
	t.Helper()
	u := id.New()
	_, err := e.pool.Exec(ctx, `INSERT INTO user_profiles (user_id, display_name, source_event_id) VALUES ($1, $2, $3)`, u.PG(), name, id.New().PG())
	must(t, err)
	return u
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: hata = %v, beklenen %v", what, err, want)
	}
}

func wantInvalid(t *testing.T, what string, err error) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("%s: hata = %v, beklenen doğrulama hatası", what, err)
	}
}

func TestFriendRequestFlow(t *testing.T) {
	e := setup(t)
	a, b := e.user(t, "Ayşe"), e.user(t, "Barış")

	f, err := e.svc.SendFriendRequest(ctx, a, b)
	if err != nil || f.State != StateOutgoing || f.UserID != b || f.DisplayName != "Barış" {
		t.Fatalf("istek: %+v %v", f, err)
	}
	_, err = e.svc.SendFriendRequest(ctx, a, b)
	wantErr(t, "tekrar istek", err, apperr.ErrFriendRequestPending)
	// The sender cannot accept their own request.
	_, err = e.svc.AcceptFriendRequest(ctx, a, b)
	wantErr(t, "kendi isteğini kabul", err, apperr.ErrFriendRequestNotFound)

	// Lists are the caller's own and split by direction.
	in, err := e.svc.ListFriendRequests(ctx, b, true, 0, "")
	if err != nil || len(in.Items) != 1 || in.Items[0].UserID != a || in.Items[0].State != StateIncoming {
		t.Fatalf("gelen: %+v %v", in, err)
	}
	out, _ := e.svc.ListFriendRequests(ctx, b, false, 0, "")
	if len(out.Items) != 0 {
		t.Fatalf("giden: %+v", out)
	}
	if r, _ := Relation(ctx, e.q, b, a); r != StateIncoming {
		t.Fatalf("ilişki = %q", r)
	}

	f, err = e.svc.AcceptFriendRequest(ctx, b, a)
	if err != nil || f.State != StateFriends || f.AcceptedAt.IsZero() {
		t.Fatalf("kabul: %+v %v", f, err)
	}
	_, err = e.svc.SendFriendRequest(ctx, b, a)
	wantErr(t, "arkadaşa istek", err, apperr.ErrAlreadyFriends)
	for _, u := range []id.ID{a, b} {
		l, err := e.svc.ListFriends(ctx, u, 0, "")
		if err != nil || len(l.Items) != 1 || l.Items[0].State != StateFriends {
			t.Fatalf("arkadaşlar: %+v %v", l, err)
		}
	}
	// A third user sees nothing of it.
	c := e.user(t, "Cem")
	if l, _ := e.svc.ListFriends(ctx, c, 0, ""); len(l.Items) != 0 {
		t.Fatalf("başkasının listesi: %+v", l)
	}

	// Events: requested (recipient b, actor a) and accepted (requester a,
	// actor b), IDs only.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'friend.requested' AND actor_id = $1 AND payload->>'user_id' = $2 AND tayfa_id IS NULL`, a.PG(), b.String()); n != 1 {
		t.Fatalf("istek olayı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'friend.accepted' AND actor_id = $1 AND payload = jsonb_build_object('user_id', $2::text)`, b.PG(), a.String()); n != 1 {
		t.Fatalf("kabul olayı = %d", n)
	}
	// One notification each, to the one person concerned, not about a tayfa;
	// the target is the other user's profile.
	for _, c := range []struct {
		typ              string
		actor, recipient id.ID
	}{{"friend_request", a, b}, {"friend_accepted", b, a}} {
		if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'notification.requested' AND actor_id = $1 AND tayfa_id IS NULL
			AND payload = jsonb_build_object('type', $2::text, 'target_type', 'user', 'target_id', $3::text, 'recipient_ids', jsonb_build_array($4::text))`,
			c.actor.PG(), c.typ, c.actor.String(), c.recipient.String()); n != 1 {
			t.Fatalf("%s bildirimi = %d", c.typ, n)
		}
	}

	// Unfriending removes the row on both sides; doing it again is fine.
	must(t, e.svc.RemoveFriendship(ctx, a, b))
	must(t, e.svc.RemoveFriendship(ctx, b, a))
	if ok, _ := Friends(ctx, e.q, a, b); ok {
		t.Fatal("arkadaşlık kaldı")
	}
}

func TestDeclinedRequestIsDeletedSilently(t *testing.T) {
	e := setup(t)
	a, b := e.user(t, "Ayşe"), e.user(t, "Barış")
	_, err := e.svc.SendFriendRequest(ctx, a, b)
	must(t, err)
	must(t, e.svc.RemoveFriendship(ctx, b, a))
	if n := e.count(t, `SELECT count(*) FROM friendships`); n != 0 {
		t.Fatalf("reddedilen istek kaldı: %d", n)
	}
	// No event tells the sender; only the original request (and its
	// notification to the recipient) is recorded.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type <> 'notification.requested'`); n != 1 {
		t.Fatalf("olay sayısı = %d", n)
	}
}

func TestCrossingRequestsBecomeFriendship(t *testing.T) {
	e := setup(t)
	a, b := e.user(t, "Ayşe"), e.user(t, "Barış")
	_, err := e.svc.SendFriendRequest(ctx, a, b)
	must(t, err)
	f, err := e.svc.SendFriendRequest(ctx, b, a)
	if err != nil || f.State != StateFriends {
		t.Fatalf("karşılıklı istek: %+v %v", f, err)
	}
}

func TestFriendRequestValidation(t *testing.T) {
	e := setup(t)
	a := e.user(t, "Ayşe")
	_, err := e.svc.SendFriendRequest(ctx, a, a)
	wantInvalid(t, "kendine", err)
	_, err = e.svc.SendFriendRequest(ctx, a, id.ID{})
	wantInvalid(t, "boş", err)
	_, err = e.svc.SendFriendRequest(ctx, a, id.New())
	wantErr(t, "bilinmeyen", err, apperr.ErrUserNotFound)
	gone := e.user(t, "Silinen")
	_, err = e.pool.Exec(ctx, `UPDATE user_profiles SET display_name = NULL, deleted_at = now() WHERE user_id = $1`, gone.PG())
	must(t, err)
	_, err = e.svc.SendFriendRequest(ctx, a, gone)
	wantErr(t, "silinmiş", err, apperr.ErrUserNotFound)
	_, err = e.svc.ListFriends(ctx, a, -1, "")
	wantInvalid(t, "sayfa", err)
	_, err = e.svc.ListFriends(ctx, a, 0, "bozuk")
	wantInvalid(t, "imleç", err)
}

// TestFriendRequestDailyLimit: 20 a day, counted from the database, so
// declined (deleted) requests still count; older than 24 hours do not.
func TestFriendRequestDailyLimit(t *testing.T) {
	e := setup(t)
	a := e.user(t, "Ayşe")
	for i := range FriendRequestsPerDay {
		b := e.user(t, "Kişi")
		_, err := e.svc.SendFriendRequest(ctx, a, b)
		must(t, err)
		if i%2 == 0 {
			must(t, e.svc.RemoveFriendship(ctx, b, a)) // declined
		}
	}
	_, err := e.svc.SendFriendRequest(ctx, a, e.user(t, "Fazla"))
	wantErr(t, "21. istek", err, apperr.ErrFriendRequestLimit)

	// Accepting a request someone else sent is not a request of one's own.
	c := e.user(t, "Cem")
	_, err = e.svc.SendFriendRequest(ctx, c, a)
	must(t, err)
	if _, err := e.svc.SendFriendRequest(ctx, a, c); err != nil {
		t.Fatalf("gelen isteği karşılık vererek kabul: %v", err)
	}

	// A day later the window has moved.
	_, err = e.admin.Exec(ctx, `UPDATE domain_events SET occurred_at = occurred_at - interval '25 hours' WHERE actor_id = $1`, a.PG())
	must(t, err)
	if _, err := e.svc.SendFriendRequest(ctx, a, e.user(t, "Ertesi gün")); err != nil {
		t.Fatalf("ertesi gün: %v", err)
	}
}

func TestFriendRequestLimitUnderConcurrency(t *testing.T) {
	e := setup(t)
	a := e.user(t, "Ayşe")
	targets := make([]id.ID, FriendRequestsPerDay+10)
	for i := range targets {
		targets[i] = e.user(t, "Kişi")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	limited := 0
	for _, b := range targets {
		wg.Go(func() {
			if _, err := e.svc.SendFriendRequest(ctx, a, b); errors.Is(err, apperr.ErrFriendRequestLimit) {
				mu.Lock()
				limited++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if limited != 10 || e.count(t, `SELECT count(*) FROM friendships`) != FriendRequestsPerDay {
		t.Fatalf("reddedilen = %d", limited)
	}
}

// TestBlock: a block deletes the friendship and waiting requests; while it
// exists neither side can send a request and the other looks unknown.
func TestBlock(t *testing.T) {
	e := setup(t)
	a, b, c := e.user(t, "Ayşe"), e.user(t, "Barış"), e.user(t, "Cem")
	_, err := e.svc.SendFriendRequest(ctx, a, b)
	must(t, err)
	_, err = e.svc.AcceptFriendRequest(ctx, b, a)
	must(t, err)
	_, err = e.svc.SendFriendRequest(ctx, c, a)
	must(t, err)

	must(t, e.svc.BlockUser(ctx, a, b))
	must(t, e.svc.BlockUser(ctx, a, b)) // again: no change
	must(t, e.svc.BlockUser(ctx, a, c))
	if n := e.count(t, `SELECT count(*) FROM friendships`); n != 0 {
		t.Fatalf("engel arkadaşlığı/isteği silmedi: %d", n)
	}
	for _, pair := range [][2]id.ID{{a, b}, {b, a}, {c, a}} {
		if blocked, err := IsBlocked(ctx, e.q, pair[0], pair[1]); err != nil || !blocked {
			t.Fatalf("iki yönlü kontrol: %v %v", blocked, err)
		}
		_, err := e.svc.SendFriendRequest(ctx, pair[0], pair[1])
		wantErr(t, "engelli istek", err, apperr.ErrUserNotFound)
	}
	_, err = e.svc.AcceptFriendRequest(ctx, a, c)
	wantErr(t, "engel sonrası kabul", err, apperr.ErrFriendRequestNotFound)
	// The blocked user may block back; that says nothing either.
	must(t, e.svc.BlockUser(ctx, b, a))

	l, err := e.svc.ListBlockedUsers(ctx, a, 0, "")
	if err != nil || len(l.Items) != 2 {
		t.Fatalf("engellenenler: %+v %v", l, err)
	}
	// Who blocked the caller is never listed.
	if l, _ := e.svc.ListBlockedUsers(ctx, c, 0, ""); len(l.Items) != 0 {
		t.Fatalf("engelleyen listelendi: %+v", l)
	}

	// Unblocking does not bring the friendship back; one remaining block
	// still separates a and b.
	must(t, e.svc.UnblockUser(ctx, a, b))
	must(t, e.svc.UnblockUser(ctx, a, b))
	if ok, _ := Friends(ctx, e.q, a, b); ok {
		t.Fatal("arkadaşlık geri geldi")
	}
	_, err = e.svc.SendFriendRequest(ctx, a, b)
	wantErr(t, "karşı engel", err, apperr.ErrUserNotFound)
	must(t, e.svc.UnblockUser(ctx, b, a))
	if _, err := e.svc.SendFriendRequest(ctx, a, b); err != nil {
		t.Fatalf("engeller kalkınca: %v", err)
	}

	err = e.svc.BlockUser(ctx, a, a)
	wantInvalid(t, "kendini engelleme", err)
	err = e.svc.BlockUser(ctx, a, id.New())
	wantErr(t, "bilinmeyeni engelleme", err, apperr.ErrUserNotFound)
}

func TestListPaging(t *testing.T) {
	e := setup(t)
	a := e.user(t, "Ayşe")
	for range 5 {
		b := e.user(t, "Kişi")
		_, err := e.svc.SendFriendRequest(ctx, b, a)
		must(t, err)
		_, err = e.svc.AcceptFriendRequest(ctx, a, b)
		must(t, err)
	}
	seen := map[id.ID]bool{}
	token := ""
	for range 3 {
		p, err := e.svc.ListFriends(ctx, a, 2, token)
		must(t, err)
		for _, f := range p.Items {
			seen[f.UserID] = true
		}
		token = p.Next
		if token == "" {
			break
		}
	}
	if len(seen) != 5 || token != "" {
		t.Fatalf("sayfalar: %d %q", len(seen), token)
	}
	// A token of one list is not accepted by another.
	p, _ := e.svc.ListFriends(ctx, a, 2, "")
	_, err := e.svc.ListBlockedUsers(ctx, a, 2, p.Next)
	wantInvalid(t, "başka listenin imleci", err)
}

func TestPairOrderMatchesDatabase(t *testing.T) {
	e := setup(t)
	for range 50 {
		a, b := id.New(), id.New()
		low, high := Pair(a, b)
		var less bool
		must(t, e.pool.QueryRow(ctx, `SELECT $1::uuid < $2::uuid`, low.PG(), high.PG()).Scan(&less))
		if !less && low != high {
			t.Fatalf("sıra uyuşmuyor: %s %s", low, high)
		}
	}
}
