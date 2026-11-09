package dm

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Issue #101, decision 1: a declined sender cannot write, even as a friend,
// until the decliner takes them off the declined list. Only the decliner
// sees the list; the sender learns nothing of it.
func TestDeclinedList(t *testing.T) {
	e := setup(t)
	a, b, c := id.New(), id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	_, conv := e.mustSend(t, a, b, "merhaba")
	must(t, e.svc.Decline(ctx, b, conv.ID))
	e.pub.take()

	// Even as friends the sender cannot write before the removal.
	e.rules.set(a, b, Verdict{Decision: Active})
	_, _, err := e.send(t, a, b, "arkadaş olduk")
	is(t, err, apperr.ErrMessageRequestPending)

	list, next, err := e.svc.Declined(ctx, b, 0, "")
	must(t, err)
	if len(list) != 1 || next != "" || list[0].ConversationID != conv.ID || list[0].Sender != a || list[0].DeclinedAt.IsZero() {
		t.Fatalf("reddedilenler = %+v %q", list, next)
	}
	// The sender and anyone else see nothing.
	for _, u := range []id.ID{a, c} {
		if l, _, err := e.svc.Declined(ctx, u, 0, ""); err != nil || len(l) != 0 {
			t.Fatalf("%s listesi = %+v %v", u, l, err)
		}
	}
	// IDOR: a stranger cannot remove it; the sender cannot remove it
	// themselves.
	is(t, e.svc.RemoveDeclined(ctx, c, conv.ID), apperr.ErrConversationNotFound)
	is(t, e.svc.RemoveDeclined(ctx, c, id.New()), apperr.ErrConversationNotFound)
	is(t, e.svc.RemoveDeclined(ctx, a, conv.ID), apperr.ErrMessageRequestNotFound)
	if l, _, _ := e.svc.Declined(ctx, b, 0, ""); len(l) != 1 {
		t.Fatal("yetkisiz çıkarma listeyi değiştirdi")
	}

	must(t, e.svc.RemoveDeclined(ctx, b, conv.ID))
	// Nobody is told: no live event, no notice for the sender.
	if got := e.pub.take(); len(got) != 0 {
		t.Fatalf("çıkarma yayını: %v", got)
	}
	if n := e.count(t, `SELECT count(*) FROM notifications WHERE user_id = $1`, a.PG()); n != 0 {
		t.Fatalf("gönderene bildirim = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'dm.request_restored'`); n != 1 {
		t.Fatalf("olay = %d", n)
	}
	if l, _, _ := e.svc.Declined(ctx, b, 0, ""); len(l) != 0 {
		t.Fatalf("çıkarılan listede: %+v", l)
	}
	is(t, e.svc.RemoveDeclined(ctx, b, conv.ID), apperr.ErrMessageRequestNotFound)
	// It is a waiting request again: the recipient sees it, the sender's
	// view did not change.
	rv, err := e.svc.Get(ctx, b, conv.ID)
	must(t, err)
	if rv.Status != StatusRequestReceived {
		t.Fatalf("alıcı = %+v", rv)
	}
	if sv, _ := e.svc.Get(ctx, a, conv.ID); sv.Status != StatusRequestSent {
		t.Fatalf("gönderen = %+v", sv)
	}
	// Still one message for a requester without free writing...
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	_, _, err = e.send(t, a, b, "tekrar")
	is(t, err, apperr.ErrMessageRequestPending)
	// ...and a friend writes freely again.
	e.rules.set(a, b, Verdict{Decision: Active})
	if _, v := e.mustSend(t, a, b, "şimdi oldu"); v.Status != StatusActive || v.ID != conv.ID {
		t.Fatalf("yeniden yazma = %+v", v)
	}
	// An active conversation is not a declined request.
	is(t, e.svc.RemoveDeclined(ctx, b, conv.ID), apperr.ErrMessageRequestNotFound)
}

func TestDeclinedListPaging(t *testing.T) {
	e := setup(t)
	b := id.New()
	var senders []id.ID
	for i := range 3 {
		a := id.New()
		senders = append(senders, a)
		e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
		_, c := e.mustSend(t, a, b, "merhaba "+string(rune('a'+i)))
		must(t, e.svc.Decline(ctx, b, c.ID))
		e.advance(time.Second)
	}
	p1, next, err := e.svc.Declined(ctx, b, 2, "")
	must(t, err)
	if len(p1) != 2 || next == "" || p1[0].Sender != senders[2] || p1[1].Sender != senders[1] {
		t.Fatalf("sayfa 1 = %+v", p1)
	}
	p2, next, err := e.svc.Declined(ctx, b, 2, next)
	must(t, err)
	if len(p2) != 1 || next != "" || p2[0].Sender != senders[0] {
		t.Fatalf("sayfa 2 = %+v %q", p2, next)
	}
	_, _, err = e.svc.Declined(ctx, b, 0, "bozuk")
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != "page_token" {
		t.Fatalf("bozuk imleç: %v", err)
	}
}

// Issue #101, decision 2: one notice per conversation that carries the
// unread count; reading the conversation reads it, and the next message
// starts over.
func TestNoticeUnreadCount(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Active})
	_, c := e.mustSend(t, a, b, "1")
	e.mustSend(t, a, b, "2")
	m3, _ := e.mustSend(t, a, b, "3")
	unread := func() []int {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT unread_count FROM notifications WHERE user_id = $1 AND target_id = $2 AND read_at IS NULL ORDER BY id`, b.PG(), c.ID.PG())
		must(t, err)
		defer rows.Close()
		var out []int
		for rows.Next() {
			var n int
			must(t, rows.Scan(&n))
			out = append(out, n)
		}
		return out
	}
	if got := unread(); len(got) != 1 || got[0] != 3 {
		t.Fatalf("sayaç = %v", got)
	}
	// Each update reaches the recipient's connections with the count.
	var counts []int32
	for _, n := range e.pub.notes {
		counts = append(counts, n.Unread)
	}
	if len(counts) != 3 || counts[0] != 1 || counts[1] != 2 || counts[2] != 3 {
		t.Fatalf("yayın sayaçları = %v", counts)
	}
	// The recipient's own reply does not count as unread for them.
	e.mustSend(t, b, a, "cevap")
	if got := unread(); len(got) != 0 {
		t.Fatalf("yazınca okunmadı: %v", got)
	}
	must(t, e.svc.MarkRead(ctx, b, c.ID, m3.ID))
	e.mustSend(t, a, b, "4")
	if got := unread(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("sıfırlanan sayaç = %v", got)
	}
	// Read through the conversation: the notice is read.
	ms, _, err := e.svc.Messages(ctx, b, c.ID, ListInput{PageSize: 1})
	must(t, err)
	must(t, e.svc.MarkRead(ctx, b, c.ID, ms[0].ID))
	if got := unread(); len(got) != 0 {
		t.Fatalf("okununca kalan = %v", got)
	}
	// Notices are still without actor and content, and only ever one row
	// per conversation is unread.
	if n := e.count(t, `SELECT count(*) FROM notifications WHERE user_id = $1 AND (actor_id IS NOT NULL OR payload <> '{}')`, b.PG()); n != 0 {
		t.Fatalf("aktörlü/içerikli bildirim = %d", n)
	}
}

// The count is capped at 100 (the client shows "99+").
func TestNoticeUnreadCountCap(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Active})
	_, c := e.mustSend(t, a, b, "ilk")
	_, err := e.pool.Exec(ctx, `INSERT INTO dm_messages (id, conversation_id, sender_id, body_encrypted, key_version, client_request_id)
		SELECT gen_random_uuid(), $1, $2, decode(repeat('00', 40), 'hex'), 1, gen_random_uuid() FROM generate_series(1, 120)`, c.ID.PG(), a.PG())
	must(t, err)
	e.mustSend(t, a, b, "son")
	if n := e.count(t, `SELECT unread_count FROM notifications WHERE user_id = $1 AND read_at IS NULL`, b.PG()); n != notification.MaxDirectUnread {
		t.Fatalf("üst sınır = %d", n)
	}
}

// A request notice counts too and stays the conversation's one notice when
// the requester may later write freely.
func TestRequestNoticeCounts(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	e.mustSend(t, a, b, "merhaba")
	e.rules.set(a, b, Verdict{Decision: Active})
	e.mustSend(t, a, b, "arkadaş olduk")
	if n := e.count(t, `SELECT count(*) FROM notifications WHERE user_id = $1`, b.PG()); n != 1 {
		t.Fatalf("bildirim = %d", n)
	}
	if n := e.count(t, `SELECT unread_count FROM notifications WHERE user_id = $1 AND type = 'dm_request'`, b.PG()); n != 2 {
		t.Fatalf("istek sayacı = %d", n)
	}
	if got := e.pub.take(); strings.Join(got, ",") != "created,notified:dm_request,accepted,created,notified:dm_request" {
		t.Fatalf("yayın = %v", got)
	}
}

// Issue #101, decision 3: the per-user send rate is checked first, keyed by
// the caller, before core is asked or anything is written.
func TestSendRateLimited(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Active})
	e.mustSend(t, a, b, "1")
	e.limit.deny = true
	calls := e.rules.calls
	_, _, err := e.send(t, a, b, "2")
	is(t, err, apperr.ErrRateLimited)
	if e.rules.calls != calls {
		t.Fatal("hız sınırında core'a soruldu")
	}
	if n := e.count(t, `SELECT count(*) FROM dm_messages`); n != 1 {
		t.Fatalf("mesaj = %d", n)
	}
	// Validation comes first: a bad request is not counted.
	before := len(e.limit.keys)
	_, _, err = e.send(t, a, b, "")
	if !errors.As(err, new(*apperr.ValidationError)) || len(e.limit.keys) != before {
		t.Fatalf("geçersiz istek sayıldı: %v", err)
	}
	for _, k := range e.limit.keys {
		if k != a.String() {
			t.Fatalf("anahtar = %q", k)
		}
	}
}
