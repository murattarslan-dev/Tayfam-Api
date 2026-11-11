package store

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func tsAt(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func putToken(t *testing.T, q *Queries, user, session id.ID, token string, at time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	if err := q.DeletePushTokenElsewhere(ctx, DeletePushTokenElsewhereParams{Token: token, SessionID: session.PG()}); err != nil {
		t.Fatal(err)
	}
	n, err := q.UpsertPushToken(ctx, UpsertPushTokenParams{
		SessionID: session.PG(), UserID: user.PG(), Platform: "android", Token: token, Now: tsAt(at),
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func tokensOf(t *testing.T, q *Queries, user id.ID) []ListUserPushTokensRow {
	t.Helper()
	rows, err := q.ListUserPushTokens(context.Background(), ListUserPushTokensParams{UserID: user.PG(), N: 100})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestPushTokenIsPerSession(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	u, other := id.New(), id.New()
	s1, s2, s3 := id.New(), id.New(), id.New()
	now := time.Now().UTC()

	putToken(t, q, u, s1, "tok:A", now)
	putToken(t, q, u, s1, "tok:B", now.Add(time.Second)) // the session refreshed its token
	putToken(t, q, u, s2, "tok:C", now)
	got := tokensOf(t, q, u)
	if len(got) != 2 || got[0].Token != "tok:B" {
		t.Fatalf("token'lar = %+v", got)
	}
	// The same device signs in with another account: the old session loses
	// the token.
	putToken(t, q, other, s3, "tok:C", now.Add(2*time.Second))
	if got := tokensOf(t, q, u); len(got) != 1 || got[0].SessionID != s1.PG() {
		t.Fatalf("eski oturumda kaldı: %+v", got)
	}
	// A session cannot be moved to another user.
	if n := putToken(t, q, other, s1, "tok:D", now); n != 0 {
		t.Fatalf("başka kullanıcının oturumu yazıldı: %d", n)
	}
	if got := tokensOf(t, q, u); len(got) != 1 || got[0].Token != "tok:B" {
		t.Fatalf("token değişti: %+v", got)
	}
	// An invalid token is removed only while the session still holds it.
	if n, err := q.DeleteInvalidPushToken(ctx, DeleteInvalidPushTokenParams{SessionID: s1.PG(), Token: "tok:A"}); err != nil || n != 0 {
		t.Fatalf("eski token silindi: %d %v", n, err)
	}
	if n, err := q.DeleteInvalidPushToken(ctx, DeleteInvalidPushTokenParams{SessionID: s1.PG(), Token: "tok:B"}); err != nil || n != 1 {
		t.Fatalf("geçersiz token: %d %v", n, err)
	}
	if n, err := q.DeleteUserPushTokens(ctx, other.PG()); err != nil || n != 1 {
		t.Fatalf("kullanıcının token'ları: %d %v", n, err)
	}
}

func TestPushTokenLimitsAndRetention(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	u := id.New()
	now := time.Now().UTC()
	sessions := make([]id.ID, 12)
	for i := range sessions {
		sessions[i] = id.New()
		putToken(t, q, u, sessions[i], "tok:"+sessions[i].String(), now.Add(time.Duration(i)*time.Minute))
	}
	if n, err := q.TrimUserPushTokens(ctx, TrimUserPushTokensParams{UserID: u.PG(), Keep: 10}); err != nil || n != 2 {
		t.Fatalf("budama: %d %v", n, err)
	}
	if got := tokensOf(t, q, u); len(got) != 10 || got[0].SessionID != sessions[11].PG() {
		t.Fatalf("en yeniler kalmalı: %d", len(got))
	}
	if n, err := q.PurgeStalePushTokens(ctx, tsAt(now.Add(5*time.Minute))); err != nil || n != 3 {
		t.Fatalf("saklama: %d %v", n, err)
	}
	// The database refuses what Go would refuse too.
	for name, tok := range map[string]string{
		"boş": "", "boşluk": "a b", "satır sonu": "a\nb", "uzun": strings.Repeat("a", 1025), "tırnak": `a"b`,
	} {
		_, err := pool.Exec(ctx, `INSERT INTO push_tokens (session_id, user_id, platform, token) VALUES ($1, $2, 'ios', $3)`,
			id.New().PG(), u.PG(), tok)
		if err == nil {
			t.Errorf("%s token kabul edildi", name)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO push_tokens (session_id, user_id, platform, token) VALUES ($1, $2, 'web', 'x')`,
		id.New().PG(), u.PG()); err == nil {
		t.Error("web platformu kabul edildi")
	}
}

func notify(t *testing.T, q *Queries, user id.ID, typ, target string, unread int32) (event, notificationID id.ID) {
	t.Helper()
	event, notificationID = id.New(), id.New()
	tayfa := pgtype.UUID{}
	if target != "dm_conversation" {
		tayfa = id.New().PG()
	}
	u := pgtype.Int4{}
	if unread > 0 {
		u = pgtype.Int4{Int32: unread, Valid: true}
	}
	if _, err := q.CreateNotifications(context.Background(), CreateNotificationsParams{
		TayfaID: tayfa, Type: typ, TargetType: target, TargetID: id.New().PG(), Payload: []byte(`{}`),
		SourceEventID: event.PG(), Ids: []pgtype.UUID{notificationID.PG()}, UserIds: []pgtype.UUID{user.PG()}, UnreadCount: u,
	}); err != nil {
		t.Fatal(err)
	}
	return event, notificationID
}

func queued(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM push_queue`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Every notification row written gets its push job in the same
// transaction; a repeated event adds neither a row nor a job (ADR-0019).
func TestNotificationEnqueuesPush(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	u := id.New()

	// Rolled back: no notification, no job.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	notify(t, New(tx), u, "announcement", "announcement", 0)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n := queued(t, pool); n != 0 {
		t.Fatalf("geri alınan kayıt için iş: %d", n)
	}

	ev, nid := notify(t, q, u, "announcement", "announcement", 0)
	if n := queued(t, pool); n != 1 {
		t.Fatalf("iş yok: %d", n)
	}
	// The same event again (at least once delivery) adds nothing, even
	// after the job is done.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(q.DeletePushJob(ctx, nid.PG()))
	_, err = q.CreateNotifications(ctx, CreateNotificationsParams{
		TayfaID: id.New().PG(), Type: "announcement", TargetType: "announcement", TargetID: id.New().PG(), Payload: []byte(`{}`),
		SourceEventID: ev.PG(), Ids: []pgtype.UUID{id.New().PG()}, UserIds: []pgtype.UUID{u.PG()},
	})
	must(err)
	if n := queued(t, pool); n != 0 {
		t.Fatalf("tekrar eden olay için iş: %d", n)
	}

	// A direct message notice: each new message (its unread count update)
	// is a push; a waiting job absorbs the next one.
	_, dm := notify(t, q, u, "dm_message", "dm_conversation", 1)
	conv := pgtype.UUID{}
	must(pool.QueryRow(ctx, `SELECT target_id FROM notifications WHERE id = $1`, dm.PG()).Scan(&conv))
	update := func() {
		t.Helper()
		_, err := q.UpdateConversationNotificationUnread(ctx, UpdateConversationNotificationUnreadParams{
			UnreadCount: pgtype.Int4{Int32: 2, Valid: true}, UserID: u.PG(), ConversationID: conv,
		})
		must(err)
	}
	update()
	if n := queued(t, pool); n != 1 {
		t.Fatalf("birleşmeli: %d", n)
	}
	must(q.DeletePushJob(ctx, dm.PG()))
	update()
	if n := queued(t, pool); n != 1 {
		t.Fatalf("yeni mesaj push'u: %d", n)
	}
	// Reading does not queue anything; deleting the notification removes
	// its job.
	must(q.DeletePushJob(ctx, dm.PG()))
	must(q.MarkConversationNotificationsRead(ctx, MarkConversationNotificationsReadParams{ReadAt: tsAt(time.Now()), UserID: u.PG(), ConversationID: conv}))
	if n := queued(t, pool); n != 0 {
		t.Fatalf("okuma iş açtı: %d", n)
	}
	notify(t, q, u, "announcement", "announcement", 0)
	must(q.DeleteUserNotifications(ctx, u.PG()))
	if n := queued(t, pool); n != 0 {
		t.Fatalf("silinen bildirimin işi kaldı: %d", n)
	}
}

func TestPushJobsLeaseRetryAndPurge(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	u := id.New()
	now := time.Now().UTC().Add(time.Minute) // jobs default to now()
	for range 6 {
		notify(t, q, u, "announcement", "announcement", 0)
	}
	// Two workers at once never get the same job.
	var mu sync.Mutex
	seen := map[pgtype.UUID]int{}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				rows, err := New(tx).ClaimPushJobs(ctx, ClaimPushJobsParams{Now: tsAt(now), LeaseUntil: tsAt(now.Add(2 * time.Minute)), N: 4})
				mu.Lock()
				defer mu.Unlock()
				for _, r := range rows {
					seen[r.NotificationID]++
					if r.Attempts != 1 || r.UserID != u.PG() || r.Type != "announcement" || r.ReadAt.Valid {
						t.Errorf("iş = %+v", r)
					}
				}
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(seen) != 6 {
		t.Fatalf("kiralanan = %d", len(seen))
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("%v iki kez kiralandı", k)
		}
	}
	// Leased: invisible until the lease ends.
	rows, err := q.ClaimPushJobs(ctx, ClaimPushJobsParams{Now: tsAt(now.Add(time.Minute)), LeaseUntil: tsAt(now.Add(3 * time.Minute)), N: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("kira: %d %v", len(rows), err)
	}
	rows, err = q.ClaimPushJobs(ctx, ClaimPushJobsParams{Now: tsAt(now.Add(2 * time.Minute)), LeaseUntil: tsAt(now.Add(4 * time.Minute)), N: 1})
	if err != nil || len(rows) != 1 || rows[0].Attempts != 2 {
		t.Fatalf("kira bitti: %+v %v", rows, err)
	}
	// Retry only one session, later.
	s := id.New()
	if err := q.RetryPushJob(ctx, RetryPushJobParams{NotificationID: rows[0].NotificationID, NextAttemptAt: tsAt(now.Add(time.Hour)), SessionIds: []pgtype.UUID{s.PG()}}); err != nil {
		t.Fatal(err)
	}
	rows, err = q.ClaimPushJobs(ctx, ClaimPushJobsParams{Now: tsAt(now.Add(time.Hour)), LeaseUntil: tsAt(now.Add(2 * time.Hour)), N: 10})
	if err != nil || len(rows) != 6 {
		t.Fatalf("yeniden deneme: %d %v", len(rows), err)
	}
	retried := 0
	for _, r := range rows {
		if len(r.SessionIds) == 1 && r.SessionIds[0] == s.PG() {
			retried++
		}
	}
	if retried != 1 {
		t.Fatalf("oturum listesi: %d", retried)
	}
	if n, err := q.PurgeOldPushJobs(ctx, tsAt(now)); err != nil || n != 6 {
		t.Fatalf("eski işler: %d %v", n, err)
	}
}

// The advisory lock serialises a registration with a session ending.
func TestLockPushKey(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	key := id.New()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := New(tx).LockPushKey(ctx, key.PG()); err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := pool.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('push:' || $1::uuid::text, 0))`, key.PG()).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("kilit alınmamış")
	}
}
