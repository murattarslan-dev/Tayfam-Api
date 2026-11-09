package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// pair returns a and b in conversation order (the byte order Postgres
// compares uuids in).
func pair(a, b id.ID) (low, high id.ID) {
	if bytes.Compare(a[:], b[:]) < 0 {
		return a, b
	}
	return b, a
}

func newConversation(t *testing.T, q *Queries, a, b id.ID, status string, requestedBy id.ID, last time.Time) id.ID {
	t.Helper()
	low, high := pair(a, b)
	c := id.New()
	// A declined conversation is a request first (declined_at, issue #101).
	p := CreateConversationParams{ID: c.PG(), UserLowID: low.PG(), UserHighID: high.PG(), Status: status, LastMessageAt: at(last)}
	if status == "declined" {
		p.Status = "request"
	}
	if requestedBy != (id.ID{}) {
		p.RequestedBy = requestedBy.PG()
	}
	if _, err := q.CreateConversation(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if status == "declined" {
		if _, err := q.DeclineConversation(context.Background(), DeclineConversationParams{ID: c.PG(), DeclinedAt: at(last)}); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func newDirectMessage(t *testing.T, q *Queries, conv, sender id.ID) id.ID {
	t.Helper()
	m := testMessageIDs.next()
	if _, err := q.CreateDirectMessage(context.Background(), CreateDirectMessageParams{
		ID: m.PG(), ConversationID: conv.PG(), SenderID: sender.PG(), BodyEncrypted: sealed(2), KeyVersion: int2(1), ClientRequestID: id.New().PG(),
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestConversationConstraints(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	a, b := id.New(), id.New()
	low, high := pair(a, b)
	now := time.Now()
	newConversation(t, q, a, b, "request", a, now)

	// The same pair again: nothing is created and the result is empty.
	_, err := q.CreateConversation(ctx, CreateConversationParams{ID: id.New().PG(), UserLowID: low.PG(), UserHighID: high.PG(), Status: "active", LastMessageAt: at(now)})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("aynı çift: %v", err)
	}
	for name, c := range map[string]struct {
		p          CreateConversationParams
		constraint string
	}{
		"ters sıra":     {CreateConversationParams{UserLowID: high.PG(), UserHighID: low.PG(), Status: "active"}, "dm_conversations_order"},
		"bilinmeyen":    {CreateConversationParams{UserLowID: id.New().PG(), UserHighID: id.New().PG(), Status: "open"}, "dm_conversations_status"},
		"istek açansız": {CreateConversationParams{UserLowID: low.PG(), UserHighID: id.New().PG(), Status: "request"}, "dm_conversations_request"},
		"üçüncü kişi":   {CreateConversationParams{UserLowID: low.PG(), UserHighID: id.New().PG(), Status: "request", RequestedBy: id.New().PG()}, "dm_conversations_requested_by"},
	} {
		c.p.ID, c.p.LastMessageAt = id.New().PG(), at(now)
		if c.p.UserLowID.Valid && c.p.UserHighID.Valid && c.constraint != "dm_conversations_order" {
			l, h := pair(id.ID(c.p.UserLowID.Bytes), id.ID(c.p.UserHighID.Bytes))
			c.p.UserLowID, c.p.UserHighID = l.PG(), h.PG()
			if (c.p.RequestedBy.Valid && c.constraint == "dm_conversations_request") || c.constraint == "dm_conversations_status" {
				c.p.RequestedBy = l.PG()
			}
		}
		if _, err := q.CreateConversation(ctx, c.p); !violates(err, c.constraint) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Declined: only an opened request, always with its decline time (issue
	// #101); no other state has one.
	x, y := pair(id.New(), id.New())
	for name, c := range map[string]struct {
		sql        string
		constraint string
	}{
		"reddedilmiş açansız":  {`INSERT INTO dm_conversations (id, user_low_id, user_high_id, status, declined_at, last_message_at) VALUES ($1, $2, $3, 'declined', now(), now())`, "dm_conversations_request"},
		"reddedilmiş zamansız": {`INSERT INTO dm_conversations (id, user_low_id, user_high_id, status, requested_by, last_message_at) VALUES ($1, $2, $3, 'declined', $2, now())`, "dm_conversations_declined_at"},
		"ret zamanlı istek":    {`INSERT INTO dm_conversations (id, user_low_id, user_high_id, status, requested_by, declined_at, last_message_at) VALUES ($1, $2, $3, 'request', $2, now(), now())`, "dm_conversations_declined_at"},
	} {
		if _, err := pool.Exec(ctx, c.sql, id.New().PG(), x.PG(), y.PG()); !violates(err, c.constraint) {
			t.Errorf("%s: %v", name, err)
		}
	}

	conv := newConversation(t, q, id.New(), id.New(), "active", id.ID{}, now)
	for name, c := range map[string]struct {
		sql        string
		constraint string
	}{
		"silinmemiş metinsiz":    {`INSERT INTO dm_messages (id, conversation_id, sender_id, client_request_id) VALUES ($1, $2, $3, $4)`, "dm_messages_body"},
		"e2e metinli":            {`INSERT INTO dm_messages (id, conversation_id, sender_id, client_request_id, encryption, body_encrypted, key_version) VALUES ($1, $2, $3, $4, 'e2e', '\x00000000000000000000000000000000000000000000000000000000ff', 1)`, "dm_messages_body"},
		"sunucu şifreli cihazlı": {`INSERT INTO dm_messages (id, conversation_id, sender_id, client_request_id, body_encrypted, key_version, sender_device_id) VALUES ($1, $2, $3, $4, '\x00000000000000000000000000000000000000000000000000000000ff', 1, $3)`, "dm_messages_device"},
		"kısa metin":             {`INSERT INTO dm_messages (id, conversation_id, sender_id, client_request_id, body_encrypted, key_version) VALUES ($1, $2, $3, $4, '\x00', 1)`, "dm_messages_body_len"},
		"sürümsüz":               {`INSERT INTO dm_messages (id, conversation_id, sender_id, client_request_id, body_encrypted) VALUES ($1, $2, $3, $4, '\x00000000000000000000000000000000000000000000000000000000ff')`, "dm_messages_key"},
	} {
		if _, err := pool.Exec(ctx, c.sql, id.New().PG(), conv.PG(), id.New().PG(), id.New().PG()); !violates(err, c.constraint) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// E2E: no body on the server, a sending device (the later stage).
	if _, err := pool.Exec(ctx, `INSERT INTO dm_messages (id, conversation_id, sender_id, client_request_id, encryption, sender_device_id) VALUES ($1, $2, $3, $4, 'e2e', $5)`,
		id.New().PG(), conv.PG(), id.New().PG(), id.New().PG(), id.New().PG()); err != nil {
		t.Fatalf("e2e mesaj: %v", err)
	}
}

func TestDirectMessageIdempotentAndErase(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	a, b := id.New(), id.New()
	conv := newConversation(t, q, a, b, "active", id.ID{}, time.Now())
	req := id.New()
	m := testMessageIDs.next()
	p := CreateDirectMessageParams{ID: m.PG(), ConversationID: conv.PG(), SenderID: a.PG(), BodyEncrypted: sealed(3), KeyVersion: int2(1), ClientRequestID: req.PG()}
	if _, err := q.CreateDirectMessage(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.ID = testMessageIDs.next().PG()
	if _, err := q.CreateDirectMessage(ctx, p); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("tekrar gönderim: %v", err)
	}
	r, err := q.GetDirectMessageByRequest(ctx, GetDirectMessageByRequestParams{SenderID: a.PG(), ClientRequestID: req.PG()})
	if err != nil || r.ID != m.PG() {
		t.Fatalf("istek kimliğiyle: %v %v", r.ID, err)
	}
	if n, _ := q.CountSenderMessages(ctx, CountSenderMessagesParams{ConversationID: conv.PG(), SenderID: a.PG()}); n != 1 {
		t.Fatalf("gönderen mesaj sayısı = %d", n)
	}
	// Only the sender erases; the body goes at once.
	if n, err := q.EraseDirectMessage(ctx, EraseDirectMessageParams{DeletedAt: at(time.Now()), ConversationID: conv.PG(), ID: m.PG(), SenderID: b.PG()}); err != nil || n != 0 {
		t.Fatalf("başkası sildi: %d %v", n, err)
	}
	if n, err := q.EraseDirectMessage(ctx, EraseDirectMessageParams{DeletedAt: at(time.Now()), ConversationID: conv.PG(), ID: m.PG(), SenderID: a.PG()}); err != nil || n != 1 {
		t.Fatalf("gönderen sildi: %d %v", n, err)
	}
	got, err := q.GetDirectMessage(ctx, GetDirectMessageParams{ConversationID: conv.PG(), ID: m.PG()})
	if err != nil || got.BodyEncrypted != nil || got.KeyVersion.Valid || !got.DeletedAt.Valid {
		t.Fatalf("silinen mesaj = %+v %v", got, err)
	}
	// Deleted messages still count against the one-message request.
	if n, _ := q.CountSenderMessages(ctx, CountSenderMessagesParams{ConversationID: conv.PG(), SenderID: a.PG()}); n != 1 {
		t.Fatalf("silinen sayılmadı: %d", n)
	}
}

func TestConversationListVisibilityUnreadAndPaging(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	me, x, y, z := id.New(), id.New(), id.New(), id.New()
	base := time.Now().UTC().Truncate(time.Microsecond)
	active := newConversation(t, q, me, x, "active", id.ID{}, base)
	declinedByMe := newConversation(t, q, me, y, "declined", y, base.Add(time.Minute))
	myDeclined := newConversation(t, q, me, z, "declined", me, base.Add(2*time.Minute))
	newConversation(t, q, x, y, "active", id.ID{}, base.Add(3*time.Minute)) // not mine

	m1 := newDirectMessage(t, q, active, x)
	newDirectMessage(t, q, active, me) // own messages are never unread
	m3 := newDirectMessage(t, q, active, x)
	newDirectMessage(t, q, declinedByMe, y)

	list := func(after *ListConversationsPageRow, n int32) []ListConversationsPageRow {
		t.Helper()
		p := ListConversationsPageParams{UserID: me.PG(), N: n}
		if after != nil {
			p.AfterAt, p.AfterID = after.LastMessageAt, after.ID
		}
		rows, err := q.ListConversationsPage(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := list(nil, 10)
	if len(rows) != 2 || rows[0].ID != myDeclined.PG() || rows[1].ID != active.PG() {
		t.Fatalf("liste = %+v", rows)
	}
	if rows[1].Unread != 2 || rows[1].LastReadMessageID.Valid {
		t.Fatalf("okunmamış = %d", rows[1].Unread)
	}
	if page := list(&rows[0], 1); len(page) != 1 || page[0].ID != active.PG() {
		t.Fatalf("ikinci sayfa = %+v", page)
	}

	// Read markers only move forward.
	mark := func(m id.ID) {
		t.Helper()
		if err := q.MarkConversationRead(ctx, MarkConversationReadParams{ConversationID: active.PG(), UserID: me.PG(), MessageID: m.PG(), ReadAt: at(time.Now())}); err != nil {
			t.Fatal(err)
		}
	}
	mark(m1)
	if r := list(nil, 10); r[1].Unread != 1 {
		t.Fatalf("m1 sonrası okunmamış = %d", r[1].Unread)
	}
	mark(m3)
	mark(m1)
	r, err := q.GetConversationRead(ctx, GetConversationReadParams{ConversationID: active.PG(), UserID: me.PG()})
	if err != nil || r.LastReadMessageID != m3.PG() {
		t.Fatalf("okundu geri gitti: %v %v", r.LastReadMessageID, err)
	}
	if r := list(nil, 10); r[1].Unread != 0 {
		t.Fatalf("hepsi okundu ama %d", r[1].Unread)
	}
	// The other party sees my read marker (read receipts).
	xs, err := q.ListConversationsPage(ctx, ListConversationsPageParams{UserID: x.PG(), N: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range xs {
		if c.ID == active.PG() && (c.PeerLastReadMessageID != m3.PG() || c.LastReadMessageID.Valid) {
			t.Fatalf("karşı tarafın okundu işareti = %v", c.PeerLastReadMessageID)
		}
	}

	// Only a party gets the conversation.
	if _, err := q.GetConversationForUser(ctx, GetConversationForUserParams{ID: active.PG(), UserID: y.PG()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("taraf olmayan: %v", err)
	}
}

func TestRequestCountAndStatus(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	me := id.New()
	now := time.Now()
	c1 := newConversation(t, q, me, id.New(), "request", me, now)
	newConversation(t, q, me, id.New(), "active", id.ID{}, now) // opened active: not a request
	other := id.New()
	newConversation(t, q, me, other, "request", other, now) // someone else's request

	count := func() int64 {
		n, err := q.CountRecentRequests(ctx, CountRecentRequestsParams{RequestedBy: me.PG(), Since: at(now.Add(-24 * time.Hour))})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("istek sayısı = %d", n)
	}
	if n, err := q.DeclineConversation(ctx, DeclineConversationParams{ID: c1.PG(), DeclinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}); err != nil || n != 1 {
		t.Fatalf("ret: %d %v", n, err)
	}
	if n := count(); n != 1 {
		t.Fatalf("reddedilen istek sınırdan düştü: %d", n)
	}
	if n, _ := q.DeclineConversation(ctx, DeclineConversationParams{ID: c1.PG(), DeclinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}); n != 0 {
		t.Fatal("ret iki kez")
	}
	if n, err := q.AcceptConversation(ctx, AcceptConversationParams{ID: c1.PG(), AcceptedAt: at(now)}); err != nil || n != 1 {
		t.Fatalf("kabul: %d %v", n, err)
	}
	if n, _ := q.AcceptConversation(ctx, AcceptConversationParams{ID: c1.PG(), AcceptedAt: at(now)}); n != 0 {
		t.Fatal("kabul iki kez")
	}
}

func TestDirectMessageCleanupAndPurge(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	gone, stays, other := id.New(), id.New(), id.New()
	now := time.Now()
	c := newConversation(t, q, gone, stays, "request", gone, now)
	both := newConversation(t, q, gone, other, "active", id.ID{}, now)
	m := newDirectMessage(t, q, c, gone)
	newDirectMessage(t, q, both, other)
	if err := q.MarkConversationRead(ctx, MarkConversationReadParams{ConversationID: c.PG(), UserID: gone.PG(), MessageID: m.PG(), ReadAt: at(now)}); err != nil {
		t.Fatal(err)
	}
	cleanup := func(u id.ID) {
		t.Helper()
		for _, f := range []func(context.Context, pgtype.UUID) error{
			q.ClearConversationLowSide, q.ClearConversationHighSide, q.ClearDirectMessageSender, q.DeleteUserConversationReads,
		} {
			if err := f(ctx, u.PG()); err != nil {
				t.Fatal(err)
			}
		}
		if err := q.DeleteAbandonedConversations(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cleanup(gone)
	var lowNull, highNull, reqNull bool
	if err := pool.QueryRow(ctx, `SELECT user_low_id IS NULL, user_high_id IS NULL, requested_by IS NULL FROM dm_conversations WHERE id = $1`, c.PG()).Scan(&lowNull, &highNull, &reqNull); err != nil {
		t.Fatal(err)
	}
	if lowNull == highNull || !reqNull {
		t.Fatalf("taraf temizlenmedi: %v %v %v", lowNull, highNull, reqNull)
	}
	got, err := q.GetDirectMessage(ctx, GetDirectMessageParams{ConversationID: c.PG(), ID: m.PG()})
	if err != nil || got.SenderID.Valid || got.BodyEncrypted == nil {
		t.Fatalf("mesaj: %+v %v", got, err)
	}
	if _, err := q.GetConversationRead(ctx, GetConversationReadParams{ConversationID: c.PG(), UserID: gone.PG()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("okunma kaydı kaldı: %v", err)
	}
	// The remaining party sees the conversation with "Silinmiş kullanıcı".
	if _, err := q.GetConversationForUser(ctx, GetConversationForUserParams{ID: c.PG(), UserID: stays.PG()}); err != nil {
		t.Fatal(err)
	}
	// When both parties are gone, the conversation and its messages go.
	cleanup(other)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dm_messages WHERE conversation_id = $1`, both.PG()).Scan(&n); err != nil || n != 0 {
		t.Fatalf("terk edilmiş konuşmanın mesajları: %d %v", n, err)
	}

	// Purge: only deleted messages older than the cutoff.
	old := newDirectMessage(t, q, c, stays)
	recent := newDirectMessage(t, q, c, stays)
	for msg, when := range map[id.ID]time.Time{old: now.Add(-31 * 24 * time.Hour), recent: now.Add(-time.Hour)} {
		if _, err := q.EraseDirectMessage(ctx, EraseDirectMessageParams{DeletedAt: at(when), ConversationID: c.PG(), ID: msg.PG(), SenderID: stays.PG()}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := q.PurgeDeletedDirectMessages(ctx, PurgeDeletedDirectMessagesParams{Before: at(now.Add(-30 * 24 * time.Hour)), N: 10}); err != nil || n != 1 {
		t.Fatalf("kalıcı silme: %d %v", n, err)
	}
	if _, err := q.GetDirectMessage(ctx, GetDirectMessageParams{ConversationID: c.PG(), ID: recent.PG()}); err != nil {
		t.Fatalf("yeni silinen gitti: %v", err)
	}
}

// The declined list belongs to the decliner only; only they can restore a
// request, and answering it clears the decline (issue #101).
func TestDeclinedRequests(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	me, x, y, stranger := id.New(), id.New(), id.New(), id.New()
	base := time.Now().UTC().Truncate(time.Microsecond)
	fromX := newConversation(t, q, me, x, "declined", x, base)
	fromY := newConversation(t, q, me, y, "declined", y, base.Add(time.Minute))
	newConversation(t, q, me, stranger, "declined", me, base.Add(2*time.Minute)) // my request, declined
	newConversation(t, q, x, y, "declined", x, base.Add(3*time.Minute))          // not mine

	list := func(u id.ID, n int32) []ListDeclinedRequestsRow {
		t.Helper()
		rows, err := q.ListDeclinedRequests(ctx, ListDeclinedRequestsParams{UserID: u.PG(), N: n})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := list(me, 10)
	if len(rows) != 2 || id.ID(rows[0].ID.Bytes) != fromY || id.ID(rows[1].ID.Bytes) != fromX || id.ID(rows[0].RequestedBy.Bytes) != y {
		t.Fatalf("liste = %+v", rows)
	}
	// x sent two declined requests and sees neither.
	if rows := list(x, 10); len(rows) != 0 {
		t.Fatalf("gönderenin listesi = %+v", rows)
	}
	page, err := q.ListDeclinedRequests(ctx, ListDeclinedRequestsParams{UserID: me.PG(), N: 10, AfterAt: rows[0].DeclinedAt, AfterID: rows[0].ID})
	if err != nil || len(page) != 1 || id.ID(page[0].ID.Bytes) != fromX {
		t.Fatalf("imleç = %+v %v", page, err)
	}

	// Only the decliner restores: not the sender, not a stranger.
	for _, u := range []id.ID{x, stranger} {
		if n, err := q.RestoreDeclinedRequest(ctx, RestoreDeclinedRequestParams{ID: fromX.PG(), UserID: u.PG()}); err != nil || n != 0 {
			t.Fatalf("%s geri aldı: %d %v", u, n, err)
		}
	}
	if n, err := q.RestoreDeclinedRequest(ctx, RestoreDeclinedRequestParams{ID: fromX.PG(), UserID: me.PG()}); err != nil || n != 1 {
		t.Fatalf("geri alma: %d %v", n, err)
	}
	var status string
	var declined pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `SELECT status, declined_at FROM dm_conversations WHERE id = $1`, fromX.PG()).Scan(&status, &declined); err != nil || status != "request" || declined.Valid {
		t.Fatalf("geri alınan = %s %v %v", status, declined, err)
	}
	if n, _ := q.RestoreDeclinedRequest(ctx, RestoreDeclinedRequestParams{ID: fromX.PG(), UserID: me.PG()}); n != 0 {
		t.Fatal("iki kez geri alındı")
	}
	// Writing to a declined request accepts it and clears the decline.
	if n, err := q.AcceptConversation(ctx, AcceptConversationParams{ID: fromY.PG(), AcceptedAt: at(base)}); err != nil || n != 1 {
		t.Fatalf("kabul: %d %v", n, err)
	}
	if rows := list(me, 10); len(rows) != 0 {
		t.Fatalf("kabulden sonra liste = %+v", rows)
	}
}
