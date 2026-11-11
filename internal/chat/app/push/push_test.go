package push_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push/fcm"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push/fcm/fcmtest"
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

type presence struct {
	mu     sync.Mutex
	online map[id.ID]bool
}

func (p *presence) Online(u id.ID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.online[u]
}

type allow struct{ deny bool }

func (a *allow) Allow(string) bool { return !a.deny }

// logBuf collects every log line of the service.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	pool  *pgxpool.Pool
	svc   *push.Service
	fcm   *fcmtest.Server
	pres  *presence
	limit *allow
	log   *logBuf
	now   time.Time
	tayfa id.ID
}

func setup(t *testing.T, enabled bool) *env {
	t.Helper()
	e := &env{
		pool: dbtest.New(t, "chat", chatdb.Migrations()), fcm: fcmtest.New(t),
		pres: &presence{online: map[id.ID]bool{}}, limit: &allow{}, log: &logBuf{},
		// Ahead of the database clock: jobs are due at their creation.
		now: time.Now().UTC().Add(time.Minute), tayfa: id.New(),
	}
	cfg := push.Config{
		Pool: e.pool, Log: slog.New(slog.NewJSONHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Presence: e.pres, Register: e.limit, Now: func() time.Time { return e.now },
	}
	if enabled {
		a, err := fcm.ParseServiceAccount(e.fcm.Account(t))
		must(t, err)
		c, err := fcm.New(fcm.Config{Account: a, Transport: e.fcm.Transport()})
		must(t, err)
		cfg.Sender = c
	}
	svc, err := push.New(cfg)
	must(t, err)
	e.svc = svc
	return e
}

func caller(user id.ID) push.Caller {
	return push.Caller{User: user, Session: id.New(), IssuedAt: time.Now().UTC().Add(-time.Minute)}
}

func (e *env) tokens(t *testing.T, user id.ID) []store.ListUserPushTokensRow {
	t.Helper()
	rows, err := store.New(e.pool).ListUserPushTokens(ctx, store.ListUserPushTokensParams{UserID: user.PG(), N: 100})
	must(t, err)
	return rows
}

// notify writes a notification as chat's consumers do.
func (e *env) notify(t *testing.T, r notification.Request) {
	t.Helper()
	if r.Event == (id.ID{}) {
		r.Event = id.New()
	}
	if r.Target == (id.ID{}) {
		r.Target = id.New()
	}
	must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return notification.Store(ctx, store.New(tx), r) }))
}

func (e *env) announce(t *testing.T, to ...id.ID) {
	t.Helper()
	e.notify(t, notification.Request{Type: notification.TypeAnnouncement, Tayfa: e.tayfa, Actor: id.New(), TargetType: notification.TargetAnnouncement, Recipients: to})
}

func (e *env) drain(t *testing.T) {
	t.Helper()
	for {
		n, err := e.svc.Drain(ctx)
		must(t, err)
		if n == 0 {
			return
		}
	}
}

func (e *env) queued(t *testing.T) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM push_queue`).Scan(&n))
	return n
}

func TestRegister(t *testing.T) {
	e := setup(t, false)
	u := id.New()
	c := caller(u)
	for name, tc := range map[string]struct{ platform, token, field string }{
		"platform":   {"web", "tok:a", "platform"},
		"boş token":  {"android", "", "token"},
		"boşluk":     {"android", "a b", "token"},
		"uzun token": {"ios", strings.Repeat("a", push.MaxTokenBytes+1), "token"},
	} {
		var v *apperr.ValidationError
		if err := e.svc.Register(ctx, c, tc.platform, tc.token); !errors.As(err, &v) || v.Field != tc.field {
			t.Errorf("%s: %v", name, err)
		}
	}
	must(t, e.svc.Register(ctx, c, push.PlatformAndroid, "tok:a"))
	must(t, e.svc.Register(ctx, c, push.PlatformIOS, "tok:b"))
	if got := e.tokens(t, u); len(got) != 1 || got[0].Token != "tok:b" || got[0].Platform != push.PlatformIOS {
		t.Fatalf("token'lar = %+v", got)
	}
	// Another user's session (a stolen sid cannot be reused across users).
	if err := e.svc.Register(ctx, push.Caller{User: id.New(), Session: c.Session, IssuedAt: c.IssuedAt}, "android", "tok:c"); !errors.Is(err, apperr.ErrSessionEnded) {
		t.Fatalf("başkasının oturumu: %v", err)
	}
	e.limit.deny = true
	if err := e.svc.Register(ctx, c, push.PlatformAndroid, "tok:a"); !errors.Is(err, apperr.ErrRateLimited) {
		t.Fatalf("hız sınırı: %v", err)
	}
	e.limit.deny = false
	must(t, e.svc.Unregister(ctx, c))
	must(t, e.svc.Unregister(ctx, c))
	if got := e.tokens(t, u); len(got) != 0 {
		t.Fatalf("çıkışta kaldı: %+v", got)
	}
	// At most ten per user.
	for range push.MaxTokensPerUser + 2 {
		must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:"+id.New().String()))
	}
	if got := e.tokens(t, u); len(got) != push.MaxTokensPerUser {
		t.Fatalf("sınır: %d", len(got))
	}
}

// A session that ended loses its token and cannot register one while its
// access token is still valid; a sanction and an account deletion remove
// every token (ADR-0019 item 1).
func TestEndedSessions(t *testing.T) {
	e := setup(t, false)
	u := id.New()
	c1, c2 := caller(u), caller(u)
	must(t, e.svc.Register(ctx, c1, push.PlatformAndroid, "tok:1"))
	must(t, e.svc.Register(ctx, c2, push.PlatformAndroid, "tok:2"))

	must(t, e.svc.SessionEnded(ctx, c1.Session))
	must(t, e.svc.SessionEnded(ctx, c1.Session)) // repeated event
	if got := e.tokens(t, u); len(got) != 1 || got[0].Token != "tok:2" {
		t.Fatalf("oturum bitti: %+v", got)
	}
	if err := e.svc.Register(ctx, c1, push.PlatformAndroid, "tok:1"); !errors.Is(err, apperr.ErrSessionEnded) {
		t.Fatalf("biten oturum kaydetti: %v", err)
	}

	must(t, e.svc.UserSanctioned(ctx, u, e.now))
	if got := e.tokens(t, u); len(got) != 0 {
		t.Fatalf("yaptırım: %+v", got)
	}
	if err := e.svc.Register(ctx, c2, push.PlatformAndroid, "tok:2"); !errors.Is(err, apperr.ErrSessionEnded) {
		t.Fatalf("yaptırımdan önceki token kaydetti: %v", err)
	}
	// A token issued after the sanction (the user signed in again) may.
	later := push.Caller{User: u, Session: id.New(), IssuedAt: e.now.Add(time.Second)}
	must(t, e.svc.Register(ctx, later, push.PlatformAndroid, "tok:3"))

	must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return push.Cleanup(ctx, store.New(tx), u) }))
	if got := e.tokens(t, u); len(got) != 0 {
		t.Fatalf("hesap silme: %+v", got)
	}
}

// A connected user gets no push (the notification arrives live); one who
// is not connected does (ADR-0005).
func TestPushOnlyWhenNotConnected(t *testing.T) {
	e := setup(t, true)
	on, off := id.New(), id.New()
	must(t, e.svc.Register(ctx, caller(on), push.PlatformAndroid, "tok:on"))
	must(t, e.svc.Register(ctx, caller(off), push.PlatformIOS, "tok:off"))
	e.pres.online[on] = true
	e.announce(t, on, off)
	e.drain(t)
	sent := e.fcm.Sent()
	if len(sent) != 1 || sent[0]["token"] != "tok:off" {
		t.Fatalf("gönderilen = %v", sent)
	}
	if n := e.queued(t); n != 0 {
		t.Fatalf("kuyrukta kaldı: %d", n)
	}
	// hike_overdue is a safety alert: it is pushed while connected too
	// (ADR-0019 item 2, platform owner 2026-11-11), and nothing else is.
	for _, typ := range []string{notification.TypeHikeOverdue, notification.TypeEventCancelled} {
		r := notification.Request{Type: typ, Tayfa: e.tayfa, TargetType: notification.TargetEvent, Recipients: []id.ID{on}}
		if typ == notification.TypeHikeOverdue {
			r.Subject = id.New()
		}
		e.notify(t, r)
	}
	e.drain(t)
	sent = e.fcm.Sent()
	if len(sent) != 2 || sent[1]["token"] != "tok:on" || sent[1]["data"].(map[string]any)["type"] != notification.TypeHikeOverdue {
		t.Fatalf("bağlıyken yalnızca yürüyüş alarmı gitmeli: %v", sent)
	}
}

// Preferences stop the push, never the list; hike_overdue goes through
// any preference (v14).
func TestPreferences(t *testing.T) {
	type pref struct {
		level string
		until time.Duration // 0: no time limit; negative: in the past
	}
	for name, tc := range map[string]struct {
		typ  string
		pref *pref
		want bool
	}{
		"tercih yok":                  {notification.TypeAnnouncement, nil, true},
		"all":                         {notification.TypeAnnouncement, &pref{"all", 0}, true},
		"none":                        {notification.TypeAnnouncement, &pref{"none", 0}, false},
		"none, süresi geçmiş":         {notification.TypeAnnouncement, &pref{"none", -time.Hour}, true},
		"none, süresi var":            {notification.TypeEventCreated, &pref{"none", time.Hour}, false},
		"mentions, duyuru":            {notification.TypeAnnouncement, &pref{"mentions", 0}, false},
		"mentions, cevap":             {notification.TypeAnswerReceived, &pref{"mentions", 0}, true},
		"mentions, yedekten asil":     {notification.TypeRSVPPromoted, &pref{"mentions", time.Hour}, true},
		"none, yürüyüş alarmı":        {notification.TypeHikeOverdue, &pref{"none", 0}, true},
		"mentions, yürüyüş alarmı":    {notification.TypeHikeOverdue, &pref{"mentions", time.Hour}, true},
		"none, DM":                    {notification.TypeDMMessage, &pref{"none", 0}, false},
		"none, DM isteği, süre geçti": {notification.TypeDMRequest, &pref{"none", -time.Minute}, true},
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t, true)
			u := id.New()
			must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:u"))
			r := notification.Request{Type: tc.typ, Tayfa: e.tayfa, Target: id.New(), Recipients: []id.ID{u}}
			scopeType, scopeID := "tayfa", e.tayfa
			switch tc.typ {
			case notification.TypeAnnouncement:
				r.TargetType, r.Actor = notification.TargetAnnouncement, id.New()
			case notification.TypeEventCreated, notification.TypeRSVPPromoted:
				r.TargetType = notification.TargetEvent
			case notification.TypeHikeOverdue:
				r.TargetType, r.Subject = notification.TargetEvent, id.New()
			case notification.TypeAnswerReceived:
				r.TargetType, r.Actor, r.Question = notification.TargetAnswer, id.New(), id.New()
			case notification.TypeDMMessage, notification.TypeDMRequest:
				r.TargetType, r.Tayfa, r.Unread = notification.TargetDMConversation, id.ID{}, 1
				scopeType, scopeID = "dm_conversation", r.Target
			}
			if tc.pref != nil {
				var until any
				if tc.pref.until != 0 {
					until = e.now.Add(tc.pref.until)
				}
				_, err := e.pool.Exec(ctx, `INSERT INTO notification_preferences (user_id, scope_type, scope_id, level, muted_until) VALUES ($1, $2, $3, $4, $5)`,
					u.PG(), scopeType, scopeID.PG(), tc.pref.level, until)
				must(t, err)
			}
			e.notify(t, r)
			e.drain(t)
			if got := len(e.fcm.Sent()) == 1; got != tc.want {
				t.Fatalf("push gitti mi = %v, beklenen %v", got, tc.want)
			}
			// The list is complete whatever the preference.
			var n int
			must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1`, u.PG()).Scan(&n))
			if n != 1 {
				t.Fatalf("listede %d bildirim", n)
			}
		})
	}
}

// A direct message push says only that there is something new: no
// sender, no content, no count (ADR-0005, ADR-0019 item 4).
func TestDirectMessagePushHasNoContent(t *testing.T) {
	e := setup(t, true)
	u, conv := id.New(), id.New()
	must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:a"))
	must(t, e.svc.Register(ctx, caller(u), push.PlatformIOS, "tok:i"))
	e.notify(t, notification.Request{Type: notification.TypeDMRequest, TargetType: notification.TargetDMConversation, Target: conv, Recipients: []id.ID{u}, Unread: 3})
	e.drain(t)
	sent := e.fcm.Sent()
	if len(sent) != 2 {
		t.Fatalf("gönderilen = %d", len(sent))
	}
	for _, m := range sent {
		data, _ := m["data"].(map[string]any)
		if len(data) != 4 || data["type"] != "dm_request" || data["target_type"] != "dm_conversation" || data["target_id"] != conv.String() || data["notification_id"] == nil {
			t.Fatalf("data = %v", data)
		}
		for k := range m {
			if k != "token" && k != "data" && k != "android" && k != "apns" {
				t.Fatalf("beklenmeyen alan %q", k)
			}
		}
	}
	a := sent[0]
	if a["token"] != "tok:a" {
		a = sent[1]
	}
	n := a["android"].(map[string]any)["notification"].(map[string]any)
	if n["title_loc_key"] != "push_dm_request_title" || n["body_loc_key"] != "push_dm_request_body" || n["tag"] != conv.String() ||
		n["channel_id"] != push.ChannelMessages || len(n) != 4 {
		t.Fatalf("android bildirimi = %v", n)
	}
}

// Other pushes carry IDs only.
func TestPushCarriesIDs(t *testing.T) {
	e := setup(t, true)
	u, hiker, ev := id.New(), id.New(), id.New()
	must(t, e.svc.Register(ctx, caller(u), push.PlatformIOS, "tok:i"))
	e.notify(t, notification.Request{Type: notification.TypeHikeOverdue, Tayfa: e.tayfa, TargetType: notification.TargetEvent, Target: ev, Subject: hiker, Recipients: []id.ID{u}})
	e.drain(t)
	sent := e.fcm.Sent()
	if len(sent) != 1 {
		t.Fatalf("gönderilen = %d", len(sent))
	}
	data := sent[0]["data"].(map[string]any)
	if data["type"] != "hike_overdue" || data["tayfa_id"] != e.tayfa.String() || data["subject_user_id"] != hiker.String() || data["target_id"] != ev.String() || len(data) != 6 {
		t.Fatalf("data = %v", data)
	}
	aps := sent[0]["apns"].(map[string]any)["payload"].(map[string]any)["aps"].(map[string]any)
	if aps["interruption-level"] != "time-sensitive" || aps["alert"].(map[string]any)["loc-key"] != "push_hike_overdue_body" {
		t.Fatalf("aps = %v", aps)
	}
}

// FCM says the token is gone: it is removed (fake FCM server).
func TestInvalidTokenIsRemoved(t *testing.T) {
	for name, r := range map[string]fcmtest.Response{
		"UNREGISTERED": fcmtest.Unregistered, "INVALID_ARGUMENT token": fcmtest.BadToken, "SENDER_ID_MISMATCH": fcmtest.Mismatch,
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t, true)
			u := id.New()
			must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:gone"))
			must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:ok"))
			e.fcm.Answer("tok:gone", r)
			e.announce(t, u)
			e.drain(t)
			if got := e.tokens(t, u); len(got) != 1 || got[0].Token != "tok:ok" {
				t.Fatalf("token'lar = %+v", got)
			}
			if n := e.queued(t); n != 0 {
				t.Fatalf("iş kaldı: %d", n)
			}
		})
	}
	// A broken payload is not the token's fault.
	e := setup(t, true)
	u := id.New()
	must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:a"))
	e.fcm.Answer("tok:a", fcmtest.BadPayload)
	e.announce(t, u)
	e.drain(t)
	if got := e.tokens(t, u); len(got) != 1 || e.queued(t) != 0 {
		t.Fatalf("içerik hatası token sildi ya da yeniden denendi: %+v %d", got, e.queued(t))
	}
}

// A transient failure is retried with backoff, for the failed token only,
// at most MaxAttempts times.
func TestTransientFailureIsRetried(t *testing.T) {
	e := setup(t, true)
	u := id.New()
	must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:flaky"))
	must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:ok"))
	e.fcm.Answer("tok:flaky", fcmtest.Unavailable, fcmtest.Unavailable)
	e.announce(t, u)
	e.drain(t)
	if n := len(e.fcm.Sent()); n != 2 || e.queued(t) != 1 {
		t.Fatalf("ilk deneme: %d gönderim, %d iş", n, e.queued(t))
	}
	// Retry-After (120 s) is longer than the first backoff (30 s).
	e.now = e.now.Add(90 * time.Second)
	e.drain(t)
	if n := len(e.fcm.Sent()); n != 2 {
		t.Fatalf("erken denendi: %d", n)
	}
	e.now = e.now.Add(time.Minute)
	e.drain(t)
	sent := e.fcm.Sent()
	if len(sent) != 3 || sent[2]["token"] != "tok:flaky" {
		t.Fatalf("yalnızca başarısız token yeniden denenmeli: %v", sent)
	}
	e.now = e.now.Add(time.Hour)
	e.drain(t)
	if len(e.fcm.Sent()) != 4 || e.queued(t) != 0 {
		t.Fatalf("üçüncü deneme: %d gönderim, %d iş", len(e.fcm.Sent()), e.queued(t))
	}

	// Never more than MaxAttempts.
	e.fcm.Answer("tok:ok", fcmtest.Quota, fcmtest.Quota, fcmtest.Quota, fcmtest.Quota, fcmtest.Quota, fcmtest.Quota, fcmtest.Quota)
	e.fcm.Answer("tok:flaky", fcmtest.Quota, fcmtest.Quota, fcmtest.Quota, fcmtest.Quota, fcmtest.Quota, fcmtest.Quota, fcmtest.Quota)
	before := len(e.fcm.Sent())
	e.announce(t, u)
	for range 10 {
		e.drain(t)
		e.now = e.now.Add(time.Hour)
	}
	if got := len(e.fcm.Sent()) - before; got != 2*push.MaxAttempts || e.queued(t) != 0 {
		t.Fatalf("deneme sayısı = %d, iş %d", got, e.queued(t))
	}
}

func TestBackoff(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		after   time.Duration
		want    time.Duration
	}{
		{1, 0, 30 * time.Second}, {2, 0, time.Minute}, {3, 0, 2 * time.Minute}, {4, 0, 4 * time.Minute},
		{10, 0, 30 * time.Minute}, {1, 5 * time.Minute, 5 * time.Minute}, {1, 3 * time.Hour, time.Hour},
	} {
		if got := push.Backoff(tc.attempt, tc.after); got != tc.want {
			t.Errorf("Backoff(%d, %v) = %v, beklenen %v", tc.attempt, tc.after, got, tc.want)
		}
	}
}

// Without FCM configuration the service starts, keeps tokens, sends
// nothing, and the queue empties by retention.
func TestDisabled(t *testing.T) {
	e := setup(t, false)
	if e.svc.Enabled() {
		t.Fatal("yapılandırmasız push açık")
	}
	u := id.New()
	must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:a"))
	e.announce(t, u)
	if n, err := e.svc.Drain(ctx); err != nil || n != 0 {
		t.Fatalf("kapalıyken gönderim: %d %v", n, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	must(t, e.svc.Run(cctx))
	e.now = e.now.Add(push.MaxAge + time.Minute)
	r, err := e.svc.Retain(ctx)
	must(t, err)
	if r.Jobs != 1 || e.queued(t) != 0 || r.Tokens != 0 {
		t.Fatalf("saklama = %+v", r)
	}
	e.now = e.now.Add(push.TokenRetention)
	r, err = e.svc.Retain(ctx)
	must(t, err)
	if r.Tokens != 1 {
		t.Fatalf("60 gün: %+v", r)
	}
}

// Read before the push went out: nothing is sent.
func TestReadNotificationIsNotPushed(t *testing.T) {
	e := setup(t, true)
	u := id.New()
	must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:a"))
	e.announce(t, u)
	_, err := e.pool.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE user_id = $1`, u.PG())
	must(t, err)
	e.drain(t)
	if len(e.fcm.Sent()) != 0 || e.queued(t) != 0 {
		t.Fatal("okunmuş bildirim gönderildi")
	}
}

// No push token, FCM access token or key reaches a log, whatever happens
// (ADR-0019 item 10).
func TestNothingSecretIsLogged(t *testing.T) {
	e := setup(t, true)
	u := id.New()
	toks := []string{"tok:SECRETa", "tok:SECRETb", "tok:SECRETc", "tok:SECRETd", "tok:SECRETe"}
	for _, tok := range toks {
		must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, tok))
	}
	e.fcm.Answer(toks[0], fcmtest.Unregistered)
	e.fcm.Answer(toks[1], fcmtest.BadPayload)
	e.fcm.Answer(toks[2], fcmtest.Unavailable)
	e.fcm.Answer(toks[3], fcmtest.APNsAuth)
	e.fcm.RejectAccessTokens()
	e.announce(t, u)
	for range 4 {
		e.drain(t)
		e.now = e.now.Add(time.Hour)
	}
	_, err := e.svc.Retain(ctx)
	must(t, err)
	logs := e.log.String()
	if !strings.Contains(logs, "push reddedildi") {
		t.Fatalf("beklenen log yok: %s", logs)
	}
	for _, bad := range []string{"SECRET", "fake-access", "PRIVATE KEY", "BEGIN", "@" + fcmtest.Project} {
		if strings.Contains(logs, bad) {
			t.Fatalf("log %q içeriyor:\n%s", bad, logs)
		}
	}
}

// partyOf stands in for dm's party check of DM muting.
type partyOf map[[2]id.ID]bool

func (p partyOf) SeesConversation(_ context.Context, conversation, user id.ID) (bool, error) {
	return p[[2]id.ID{conversation, user}], nil
}

type noMembers struct{}

func (noMembers) IsActiveMember(context.Context, id.ID, id.ID) (bool, error) { return false, nil }

// A conversation muted through the user endpoint (issue #138) gets no
// push, only that conversation, only for that user, and only until
// mute_until; its notices stay in the list (ADR-0005, ADR-0019 item 3).
func TestDirectConversationMute(t *testing.T) {
	e := setup(t, true)
	u, peer, muted, open := id.New(), id.New(), id.New(), id.New()
	prefs, err := notification.New(notification.Config{
		Pool: e.pool, Log: slog.New(slog.NewJSONHandler(e.log, nil)), Members: noMembers{},
		Conversations: partyOf{{muted, u}: true, {open, u}: true, {muted, peer}: true},
	})
	must(t, err)
	must(t, e.svc.Register(ctx, caller(u), push.PlatformAndroid, "tok:u"))
	must(t, e.svc.Register(ctx, caller(peer), push.PlatformAndroid, "tok:peer"))
	_, err = prefs.SetConversationPreference(ctx, u, muted, notification.LevelNone, time.Time{})
	must(t, err)

	dm := func(to, conv id.ID, typ string) {
		t.Helper()
		e.notify(t, notification.Request{Type: typ, TargetType: notification.TargetDMConversation, Target: conv, Recipients: []id.ID{to}, Unread: 1})
	}
	sentTo := func() []string {
		t.Helper()
		var out []string
		for _, m := range e.fcm.Sent() {
			out = append(out, m["token"].(string)+"/"+m["data"].(map[string]any)["target_id"].(string))
		}
		return out
	}
	dm(u, muted, notification.TypeDMMessage)
	dm(u, muted, notification.TypeDMRequest)
	dm(u, open, notification.TypeDMMessage)
	dm(peer, muted, notification.TypeDMMessage) // the other party's own preference
	e.drain(t)
	got := sentTo()
	slices.Sort(got)
	want := []string{"tok:peer/" + muted.String(), "tok:u/" + open.String()}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("gönderilen = %v, beklenen %v", got, want)
	}
	var n int
	must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND target_id = $2`, u.PG(), muted.PG()).Scan(&n))
	if n != 2 {
		t.Fatalf("sessizdeki konuşmanın bildirimleri listede %d", n)
	}
	if n := e.queued(t); n != 0 {
		t.Fatalf("kuyrukta kaldı: %d", n)
	}

	// Timed: muted until mute_until, then pushed again.
	_, err = prefs.SetConversationPreference(ctx, u, muted, notification.LevelNone, time.Now().UTC().Add(time.Hour))
	must(t, err)
	dm(u, muted, notification.TypeDMMessage)
	e.drain(t)
	if len(e.fcm.Sent()) != 2 {
		t.Fatalf("süreli sessizde push gitti: %v", sentTo())
	}
	e.now = e.now.Add(2 * time.Hour)
	dm(u, muted, notification.TypeDMMessage)
	e.drain(t)
	if got := sentTo(); len(got) != 3 || got[2] != "tok:u/"+muted.String() {
		t.Fatalf("süre bitince push gitmedi: %v", got)
	}

	// Back to all through the endpoint: pushed again.
	_, err = prefs.SetConversationPreference(ctx, u, muted, notification.LevelNone, time.Time{})
	must(t, err)
	_, err = prefs.SetConversationPreference(ctx, u, muted, notification.LevelAll, time.Time{})
	must(t, err)
	dm(u, muted, notification.TypeDMMessage)
	e.drain(t)
	if got := sentTo(); len(got) != 4 || got[3] != "tok:u/"+muted.String() {
		t.Fatalf("varsayılana dönünce push gitmedi: %v", got)
	}
	// Nothing personal in the logs: no conversation ID, no token.
	if l := e.log.String(); strings.Contains(l, muted.String()) || strings.Contains(l, "tok:") {
		t.Fatalf("log kişisel veri taşıyor: %s", l)
	}
}
