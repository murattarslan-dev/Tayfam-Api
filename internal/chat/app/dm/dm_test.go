package dm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("hata = %v, %v bekleniyordu", err, want)
	}
}

// fakeRules stands in for core's CheckDirectMessage. The rule table itself
// is tested in core (internal/core/app/directmessage); these tests cover
// chat's handling of every answer.
type fakeRules struct {
	mu    sync.Mutex
	v     map[[2]id.ID]Verdict // sender, recipient; missing: Denied
	err   error
	calls int
}

func (f *fakeRules) Check(_ context.Context, caller, recipient id.ID) (Verdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return Verdict{}, f.err
	}
	v, ok := f.v[[2]id.ID{caller, recipient}]
	if !ok {
		return Verdict{Decision: Denied}, nil
	}
	return v, nil
}

// set sets the answer in both directions.
func (f *fakeRules) set(a, b id.ID, v Verdict) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.v[[2]id.ID{a, b}], f.v[[2]id.ID{b, a}] = v, v
}

// recPub records what Service publishes.
type recPub struct {
	mu     sync.Mutex
	events []string
	bodies []string
	reads  []id.ID // receivers of read markers
	notes  []notification.Request
	// envelopes are the end-to-end deliveries (issue #127).
	envelopes []keys.Delivery
}

func (r *recPub) add(ev string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recPub) MessageCreated(_ [2]id.ID, m Message) {
	r.mu.Lock()
	r.bodies = append(r.bodies, m.Body)
	r.mu.Unlock()
	r.add("created")
}
func (r *recPub) MessageDeleted([2]id.ID, id.ID, id.ID) { r.add("deleted") }
func (r *recPub) ConversationAccepted([2]id.ID, id.ID)  { r.add("accepted") }
func (r *recPub) ConversationRead(to, _, _, _ id.ID) {
	r.mu.Lock()
	r.reads = append(r.reads, to)
	r.mu.Unlock()
	r.add("read")
}
func (r *recPub) Notified(n notification.Request) {
	r.mu.Lock()
	r.notes = append(r.notes, n)
	r.mu.Unlock()
	r.add("notified:" + n.Type)
}

func (r *recPub) Envelopes(ds []keys.Delivery) {
	r.mu.Lock()
	r.envelopes = append(r.envelopes, ds...)
	r.mu.Unlock()
	r.add("envelopes")
}

func (r *recPub) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

// fakeLimit stands in for the shared send rate: it records the keys and
// refuses while deny is set.
type fakeLimit struct {
	mu   sync.Mutex
	deny bool
	keys []string
}

func (f *fakeLimit) Allow(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	return !f.deny
}

type env struct {
	svc   *Service
	pool  *pgxpool.Pool
	rules *fakeRules
	limit *fakeLimit
	pub   *recPub
	now   time.Time
	mu    sync.Mutex
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, "chat", chatdb.Migrations())
	e := &env{pool: pool, rules: &fakeRules{v: map[[2]id.ID]Verdict{}}, limit: &fakeLimit{}, pub: &recPub{}, now: time.Now().UTC()}
	svc, err := New(Config{
		Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Rules: e.rules, SendLimit: e.limit,
		Cipher: channelcrypt.Ephemeral(), Publisher: e.pub,
		Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
	})
	must(t, err)
	e.svc = svc
	return e
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

func (e *env) send(t *testing.T, from, to id.ID, body string) (Message, Conversation, error) {
	t.Helper()
	return e.svc.Send(ctx, from, SendInput{Recipient: to, Body: body, ClientRequestID: id.New()})
}

func (e *env) mustSend(t *testing.T, from, to id.ID, body string) (Message, Conversation) {
	t.Helper()
	m, c, err := e.send(t, from, to, body)
	must(t, err)
	return m, c
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) notices(t *testing.T, user id.ID, typ string) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM notifications WHERE user_id = $1 AND type = $2 AND read_at IS NULL`, user.PG(), typ)
}

func TestSendActive(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Active})
	m, c := e.mustSend(t, a, b, "selam, gizli metin")
	if m.Body != "selam, gizli metin" || m.SenderID != a || c.Status != StatusActive || c.Other != b || c.LastRead != m.ID || c.Unread != 0 {
		t.Fatalf("gönderim = %+v %+v", m, c)
	}
	if got := e.pub.take(); strings.Join(got, ",") != "created,notified:dm_message" {
		t.Fatalf("yayın = %v", got)
	}
	// The recipient sees one unread message and one notice without actor.
	cs, _, err := e.svc.List(ctx, b, 0, "")
	must(t, err)
	if len(cs) != 1 || cs[0].Unread != 1 || cs[0].Other != a || cs[0].Status != StatusActive {
		t.Fatalf("alıcının listesi = %+v", cs)
	}
	if n := e.count(t, `SELECT count(*) FROM notifications WHERE user_id = $1 AND type = 'dm_message' AND actor_id IS NULL AND tayfa_id IS NULL AND target_type = 'dm_conversation' AND target_id = $2 AND payload = '{}'`, b.PG(), c.ID.PG()); n != 1 {
		t.Fatalf("bildirim = %d", n)
	}
	ms, _, err := e.svc.Messages(ctx, b, c.ID, ListInput{})
	must(t, err)
	if len(ms) != 1 || ms[0].Body != "selam, gizli metin" {
		t.Fatalf("mesajlar = %+v", ms)
	}
	// Nobody else reaches the conversation.
	_, err = e.svc.Get(ctx, id.New(), c.ID)
	is(t, err, apperr.ErrConversationNotFound)
	_, _, err = e.svc.Messages(ctx, id.New(), c.ID, ListInput{})
	is(t, err, apperr.ErrConversationNotFound)
}

// The body is never stored, logged as an event or notified in plain text.
func TestBodyStaysEncrypted(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	const secret = "ÇokGizliİçerik-42"
	_, c := e.mustSend(t, a, b, secret)
	must(t, e.svc.Decline(ctx, b, c.ID))
	e.rules.set(a, b, Verdict{Decision: Active})
	for _, sql := range []string{
		`SELECT count(*) FROM dm_messages WHERE position(convert_to($1, 'UTF8') in body_encrypted) > 0`,
		`SELECT count(*) FROM domain_events WHERE payload::text LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM notifications WHERE payload::text LIKE '%' || $1 || '%'`,
	} {
		if n := e.count(t, sql, secret); n != 0 {
			t.Errorf("%s: %d", sql, n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type LIKE 'dm.%'`); n != 2 {
		t.Fatalf("olaylar = %d", n)
	}
}

func TestRequestFlow(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	_, c := e.mustSend(t, a, b, "merhaba, tanışalım mı?")
	if c.Status != StatusRequestSent {
		t.Fatalf("gönderen durumu = %v", c.Status)
	}
	if got := e.pub.take(); strings.Join(got, ",") != "created,notified:dm_request" {
		t.Fatalf("yayın = %v", got)
	}
	// One message until the recipient answers.
	_, _, err := e.send(t, a, b, "ikinci")
	is(t, err, apperr.ErrMessageRequestPending)
	// The requester cannot accept their own request.
	_, err = e.svc.Accept(ctx, a, c.ID)
	is(t, err, apperr.ErrMessageRequestNotFound)

	rc, err := e.svc.Get(ctx, b, c.ID)
	must(t, err)
	if rc.Status != StatusRequestReceived || rc.Unread != 1 {
		t.Fatalf("alıcı = %+v", rc)
	}
	// Reading the request does not tell the requester.
	ms, _, err := e.svc.Messages(ctx, b, c.ID, ListInput{})
	must(t, err)
	must(t, e.svc.MarkRead(ctx, b, c.ID, ms[0].ID))
	if v, _ := e.svc.Get(ctx, a, c.ID); v.PeerLastRead != (id.ID{}) {
		t.Fatal("istekte okundu bilgisi gitti")
	}
	if got := e.pub.take(); len(got) != 0 {
		t.Fatalf("istekte okundu yayını: %v", got)
	}
	if n := e.notices(t, b, "dm_request"); n != 0 {
		t.Fatalf("okununca istek bildirimi okunmadı: %d", n)
	}

	// The reply accepts.
	_, rc = e.mustSend(t, b, a, "olur")
	if rc.Status != StatusActive {
		t.Fatalf("cevap sonrası = %v", rc.Status)
	}
	if got := e.pub.take(); strings.Join(got, ",") != "accepted,created,notified:dm_message" {
		t.Fatalf("yayın = %v", got)
	}
	e.mustSend(t, a, b, "harika")
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'dm.request_accepted'`); n != 1 {
		t.Fatalf("kabul olayı = %d", n)
	}
	// Read receipts in an active conversation.
	ms, _, err = e.svc.Messages(ctx, b, c.ID, ListInput{})
	must(t, err)
	must(t, e.svc.MarkRead(ctx, b, c.ID, ms[0].ID))
	if v, _ := e.svc.Get(ctx, a, c.ID); v.PeerLastRead != ms[0].ID {
		t.Fatalf("okundu bilgisi = %v", v.PeerLastRead)
	}
	e.pub.mu.Lock()
	reads := e.pub.reads
	e.pub.mu.Unlock()
	if len(reads) != 1 || reads[0] != a {
		t.Fatalf("okundu yayını = %v", reads)
	}
}

func TestAcceptExplicitly(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	_, c := e.mustSend(t, a, b, "merhaba")
	e.pub.take()
	v, err := e.svc.Accept(ctx, b, c.ID)
	must(t, err)
	if v.Status != StatusActive {
		t.Fatalf("kabul = %+v", v)
	}
	if got := e.pub.take(); strings.Join(got, ",") != "accepted" {
		t.Fatalf("yayın = %v", got)
	}
	// Again: nothing changes.
	if _, err := e.svc.Accept(ctx, b, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.pub.take(); len(got) != 0 {
		t.Fatalf("ikinci kabul yayını: %v", got)
	}
	err = e.svc.Decline(ctx, b, c.ID)
	is(t, err, apperr.ErrMessageRequestNotFound)
	e.mustSend(t, a, b, "teşekkürler")
}

func TestDecline(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	_, c := e.mustSend(t, a, b, "merhaba")
	is(t, e.svc.Decline(ctx, a, c.ID), apperr.ErrMessageRequestNotFound)
	must(t, e.svc.Decline(ctx, b, c.ID))
	if n := e.notices(t, b, "dm_request"); n != 0 {
		t.Fatalf("reddedilen isteğin bildirimi okunmadı: %d", n)
	}
	// The sender is never told: same answer as a waiting request, and the
	// conversation still looks sent.
	_, _, err := e.send(t, a, b, "neden cevap yok")
	is(t, err, apperr.ErrMessageRequestPending)
	v, err := e.svc.Get(ctx, a, c.ID)
	must(t, err)
	if v.Status != StatusRequestSent {
		t.Fatalf("gönderen = %v", v.Status)
	}
	// Becoming able to write freely does not undo a decline.
	e.rules.set(a, b, Verdict{Decision: Active})
	_, _, err = e.send(t, a, b, "arkadaş olduk")
	is(t, err, apperr.ErrMessageRequestPending)
	// For the recipient it is gone.
	_, err = e.svc.Get(ctx, b, c.ID)
	is(t, err, apperr.ErrConversationNotFound)
	_, _, err = e.svc.Messages(ctx, b, c.ID, ListInput{})
	is(t, err, apperr.ErrConversationNotFound)
	if cs, _, _ := e.svc.List(ctx, b, 0, ""); len(cs) != 0 {
		t.Fatalf("alıcı listesi = %+v", cs)
	}
	is(t, e.svc.Decline(ctx, b, c.ID), apperr.ErrConversationNotFound)
	// Writing later reopens it as active.
	_, rc := e.mustSend(t, b, a, "kusura bakma, şimdi gördüm")
	if rc.Status != StatusActive || rc.ID != c.ID {
		t.Fatalf("yeniden açılış = %+v", rc)
	}
	e.mustSend(t, a, b, "sorun değil")
}

// A requester who may now write freely (for example they became friends)
// is no longer held to one message.
func TestRequestUpgradedByActiveVerdict(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	_, c := e.mustSend(t, a, b, "merhaba")
	e.rules.set(a, b, Verdict{Decision: Active})
	_, v := e.mustSend(t, a, b, "artık arkadaşız")
	if v.Status != StatusActive || v.ID != c.ID {
		t.Fatalf("durum = %+v", v)
	}
}

func TestDenied(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	_, _, err := e.send(t, a, b, "merhaba")
	is(t, err, apperr.ErrDirectMessageNotAllowed)
	// A block closes an active conversation too.
	e.rules.set(a, b, Verdict{Decision: Active})
	_, c := e.mustSend(t, a, b, "merhaba")
	e.rules.set(a, b, Verdict{Decision: Denied})
	_, _, err = e.send(t, b, a, "cevap")
	is(t, err, apperr.ErrDirectMessageNotAllowed)
	// An unknown answer is a refusal.
	e.rules.set(a, b, Verdict{})
	_, _, err = e.send(t, a, b, "?")
	is(t, err, apperr.ErrDirectMessageNotAllowed)
	if n := e.count(t, `SELECT count(*) FROM dm_messages WHERE conversation_id = $1`, c.ID.PG()); n != 1 {
		t.Fatalf("mesaj sayısı = %d", n)
	}
	// One's own message can still be deleted after a block.
	ms, _, err := e.svc.Messages(ctx, a, c.ID, ListInput{})
	must(t, err)
	must(t, e.svc.Delete(ctx, a, c.ID, ms[0].ID))
	// Core unreachable: unavailable, nothing written.
	e.rules.err = apperr.ErrUnavailable
	_, _, err = e.send(t, a, b, "?")
	is(t, err, apperr.ErrUnavailable)
}

func TestRequestLimit(t *testing.T) {
	e := setup(t)
	a := id.New()
	for i := range 5 {
		b := id.New()
		e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 5})
		_, c := e.mustSend(t, a, b, "merhaba")
		if i == 0 {
			must(t, e.svc.Decline(ctx, b, c.ID))
		}
	}
	// A declined request still counts; an active conversation does not.
	b := id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 5})
	_, _, err := e.send(t, a, b, "merhaba")
	is(t, err, apperr.ErrMessageRequestLimit)
	if n := e.count(t, `SELECT count(*) FROM dm_conversations WHERE requested_by = $1`, a.PG()); n != 5 {
		t.Fatalf("istek sayısı = %d", n)
	}
	friend := id.New()
	e.rules.set(a, friend, Verdict{Decision: Active})
	e.mustSend(t, a, friend, "selam")
	// Core's higher limit for long-standing members.
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	e.mustSend(t, a, b, "merhaba")
	// A missing or out-of-range limit falls back to the strict one.
	c := id.New()
	e.rules.set(a, c, Verdict{Decision: Request, RequestLimit: 0})
	_, _, err = e.send(t, a, c, "merhaba")
	is(t, err, apperr.ErrMessageRequestLimit)
	// The window is 24 hours.
	e.advance(RequestWindow + time.Minute)
	e.mustSend(t, a, c, "merhaba")
}

func TestRequestLimitUnderConcurrency(t *testing.T) {
	e := setup(t)
	a := id.New()
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		b := id.New()
		e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 5})
		wg.Go(func() {
			_, _, err := e.send(t, a, b, "merhaba")
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, apperr.ErrMessageRequestLimit):
			t.Fatal(err)
		}
	}
	if ok != 5 {
		t.Fatalf("%d istek açıldı, 5 bekleniyordu", ok)
	}
}

func TestRetryAndValidation(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	req := id.New()
	m1, _, err := e.svc.Send(ctx, a, SendInput{Recipient: b, Body: "merhaba", ClientRequestID: req})
	must(t, err)
	calls := e.rules.calls
	e.pub.take()
	// A retry returns the first message without asking core again and is
	// not held to the one-message request rule.
	m2, c, err := e.svc.Send(ctx, a, SendInput{Recipient: b, Body: "merhaba", ClientRequestID: req})
	must(t, err)
	if m2.ID != m1.ID || m2.Body != "merhaba" || c.Status != StatusRequestSent || e.rules.calls != calls {
		t.Fatalf("tekrar = %+v %+v", m2, c)
	}
	if got := e.pub.take(); len(got) != 0 {
		t.Fatalf("tekrar yayınlandı: %v", got)
	}
	// The same request ID towards someone else.
	other := id.New()
	e.rules.set(a, other, Verdict{Decision: Active})
	_, _, err = e.svc.Send(ctx, a, SendInput{Recipient: other, Body: "x", ClientRequestID: req})
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != "client_request_id" {
		t.Fatalf("başka alıcı: %v", err)
	}
	for name, in := range map[string]SendInput{
		"recipient_user_id": {Recipient: a, Body: "x", ClientRequestID: id.New()},
		"body":              {Recipient: b, Body: "  \n ", ClientRequestID: id.New()},
		"client_request_id": {Recipient: b, Body: "x"},
	} {
		_, _, err := e.svc.Send(ctx, a, in)
		if !errors.As(err, &v) || v.Field != name {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, body := range map[string]string{
		"çok uzun": strings.Repeat("ş", BodyMax+1),
		"kontrol":  "a\x00b",
		"geçersiz": string([]byte{0xff}),
		"boş":      "",
	} {
		if _, _, err := e.send(t, a, other, body); !errors.As(err, &v) || v.Field != "body" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := e.send(t, a, other, strings.Repeat("ş", BodyMax)); err != nil {
		t.Fatalf("4000 karakter: %v", err)
	}
}

func TestDeleteAndPurge(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Active})
	m, c := e.mustSend(t, a, b, "yanlışlıkla")
	e.pub.take()
	is(t, e.svc.Delete(ctx, b, c.ID, m.ID), apperr.ErrMessageNotFound)
	is(t, e.svc.Delete(ctx, a, c.ID, id.New()), apperr.ErrMessageNotFound)
	is(t, e.svc.Delete(ctx, id.New(), c.ID, m.ID), apperr.ErrConversationNotFound)
	must(t, e.svc.Delete(ctx, a, c.ID, m.ID))
	must(t, e.svc.Delete(ctx, a, c.ID, m.ID))
	if got := e.pub.take(); strings.Join(got, ",") != "deleted" {
		t.Fatalf("yayın = %v", got)
	}
	ms, _, err := e.svc.Messages(ctx, b, c.ID, ListInput{})
	must(t, err)
	if len(ms) != 1 || !ms[0].Deleted || ms[0].Body != "" {
		t.Fatalf("silinen = %+v", ms)
	}
	if n := e.count(t, `SELECT count(*) FROM dm_messages WHERE body_encrypted IS NOT NULL`); n != 0 {
		t.Fatal("metin silinmedi")
	}
	if cs, _, _ := e.svc.List(ctx, b, 0, ""); cs[0].Unread != 0 {
		t.Fatalf("silinen okunmamış sayıldı: %d", cs[0].Unread)
	}
	e.advance(PurgeAfter + time.Hour)
	n, err := e.svc.PurgeDeleted(ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("kalıcı silinen = %d", n)
	}
}

func TestNoticeDedupe(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Active})
	_, c := e.mustSend(t, a, b, "1")
	e.mustSend(t, a, b, "2")
	m3, _ := e.mustSend(t, a, b, "3")
	if n := e.notices(t, b, "dm_message"); n != 1 {
		t.Fatalf("okunmamış bildirim = %d", n)
	}
	must(t, e.svc.MarkRead(ctx, b, c.ID, m3.ID))
	if n := e.notices(t, b, "dm_message"); n != 0 {
		t.Fatalf("okununca kalan bildirim = %d", n)
	}
	e.mustSend(t, a, b, "4")
	if n := e.notices(t, b, "dm_message"); n != 1 {
		t.Fatalf("yeni bildirim = %d", n)
	}
	// The sender's own messages never notify the sender.
	if n := e.count(t, `SELECT count(*) FROM notifications WHERE user_id = $1`, a.PG()); n != 0 {
		t.Fatalf("gönderene bildirim = %d", n)
	}
	is(t, e.svc.MarkRead(ctx, b, c.ID, id.New()), apperr.ErrMessageNotFound)
}

func TestPaging(t *testing.T) {
	e := setup(t)
	me := id.New()
	var others []id.ID
	for range 3 {
		o := id.New()
		others = append(others, o)
		e.rules.set(me, o, Verdict{Decision: Active})
		e.mustSend(t, me, o, "selam")
		e.advance(time.Second)
	}
	var seen []id.ID
	token := ""
	for {
		cs, next, err := e.svc.List(ctx, me, 2, token)
		must(t, err)
		for _, c := range cs {
			seen = append(seen, c.Other)
		}
		if next == "" {
			break
		}
		token = next
	}
	if len(seen) != 3 || seen[0] != others[2] || seen[2] != others[0] {
		t.Fatalf("sıra = %v", seen)
	}
	_, _, err := e.svc.List(ctx, me, 0, "bozuk!")
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != "page_token" {
		t.Fatalf("bozuk imleç: %v", err)
	}

	o := others[0]
	var ids []id.ID
	var conv id.ID
	for range 5 {
		m, c := e.mustSend(t, me, o, "m")
		ids, conv = append(ids, m.ID), c.ID
	}
	page, next, err := e.svc.Messages(ctx, o, conv, ListInput{PageSize: 2})
	must(t, err)
	if len(page) != 2 || page[0].ID != ids[4] || next == "" {
		t.Fatalf("ilk sayfa = %+v", page)
	}
	after, _, err := e.svc.Messages(ctx, o, conv, ListInput{After: ids[2]})
	must(t, err)
	if len(after) != 2 || after[0].ID != ids[3] {
		t.Fatalf("sonrası = %+v", after)
	}
}

func TestUnavailableWithoutKey(t *testing.T) {
	pool := dbtest.New(t, "chat", chatdb.Migrations())
	svc, err := New(Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Rules: &fakeRules{}, SendLimit: &fakeLimit{}})
	must(t, err)
	a, b, c := id.New(), id.New(), id.New()
	_, _, err = svc.Send(ctx, a, SendInput{Recipient: b, Body: "x", ClientRequestID: id.New()})
	is(t, err, apperr.ErrDirectMessagesUnavailable)
	_, _, err = svc.List(ctx, a, 0, "")
	is(t, err, apperr.ErrDirectMessagesUnavailable)
	_, err = svc.Get(ctx, a, c)
	is(t, err, apperr.ErrDirectMessagesUnavailable)
	_, _, err = svc.Messages(ctx, a, c, ListInput{})
	is(t, err, apperr.ErrDirectMessagesUnavailable)
	is(t, svc.Delete(ctx, a, c, b), apperr.ErrDirectMessagesUnavailable)
	is(t, svc.MarkRead(ctx, a, c, b), apperr.ErrDirectMessagesUnavailable)
	_, err = svc.Accept(ctx, a, c)
	is(t, err, apperr.ErrDirectMessagesUnavailable)
	is(t, svc.Decline(ctx, a, c), apperr.ErrDirectMessagesUnavailable)
}

func TestCleanup(t *testing.T) {
	e := setup(t)
	gone, stays := id.New(), id.New()
	e.rules.set(gone, stays, Verdict{Decision: Active})
	m, c := e.mustSend(t, gone, stays, "veda")
	must(t, e.svc.MarkRead(ctx, stays, c.ID, m.ID))
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	must(t, Cleanup(ctx, store.New(tx), gone))
	must(t, tx.Commit(ctx))
	v, err := e.svc.Get(ctx, stays, c.ID)
	must(t, err)
	if v.Other != (id.ID{}) {
		t.Fatalf("silinen taraf = %v", v.Other)
	}
	ms, _, err := e.svc.Messages(ctx, stays, c.ID, ListInput{})
	must(t, err)
	if len(ms) != 1 || ms[0].SenderID != (id.ID{}) || ms[0].Body != "veda" {
		t.Fatalf("mesaj = %+v", ms)
	}
	if n := e.count(t, `SELECT count(*) FROM dm_conversation_reads WHERE user_id = $1`, gone.PG()); n != 0 {
		t.Fatal("okunma kaydı kaldı")
	}
}

// core's report check: parties only, IDs only, deleted and declined count,
// one's own message and a deleted sender do not.
func TestVerifyForReport(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 20})
	m, c := e.mustSend(t, a, b, "taciz")
	conv, sender, err := e.svc.VerifyForReport(ctx, b, m.ID)
	if err != nil || conv != c.ID || sender != a {
		t.Fatalf("doğrulama = %v %v %v", conv, sender, err)
	}
	// The recipient declined and the sender deleted: still reportable.
	must(t, e.svc.Decline(ctx, b, c.ID))
	must(t, e.svc.Delete(ctx, a, c.ID, m.ID))
	if _, s, err := e.svc.VerifyForReport(ctx, b, m.ID); err != nil || s != a {
		t.Fatalf("silinmiş ve reddedilmiş: %v %v", s, err)
	}
	for name, who := range map[string]id.ID{"kendi mesajı": a, "taraf değil": id.New()} {
		if _, _, err := e.svc.VerifyForReport(ctx, who, m.ID); !errors.Is(err, apperr.ErrMessageNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := e.svc.VerifyForReport(ctx, b, id.New()); !errors.Is(err, apperr.ErrMessageNotFound) {
		t.Errorf("bilinmeyen mesaj: %v", err)
	}
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	must(t, Cleanup(ctx, store.New(tx), a))
	must(t, tx.Commit(ctx))
	if _, _, err := e.svc.VerifyForReport(ctx, b, m.ID); !errors.Is(err, apperr.ErrMessageNotFound) {
		t.Errorf("silinmiş gönderen: %v", err)
	}
	// It works without the message key.
	keyless, err := New(Config{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Rules: e.rules, SendLimit: e.limit})
	must(t, err)
	x, y := id.New(), id.New()
	e.rules.set(x, y, Verdict{Decision: Active})
	m2, _ := e.mustSend(t, x, y, "x")
	if _, s, err := keyless.VerifyForReport(ctx, y, m2.ID); err != nil || s != x {
		t.Fatalf("anahtarsız: %v %v", s, err)
	}
}
