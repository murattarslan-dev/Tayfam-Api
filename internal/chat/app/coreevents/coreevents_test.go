package coreevents

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/userevents"
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

func setup(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.New(t, chatdb.Migrations())
	svc, err := New(Config{Pool: pool})
	must(t, err)
	return svc, pool
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func announcement(tf, actor id.ID, to ...id.ID) Event {
	return Event{ID: id.New(), Tayfa: tf, Notification: &notification.Request{
		Type: notification.TypeAnnouncement, Actor: actor, TargetType: notification.TargetAnnouncement, Target: id.New(), Recipients: to,
	}}
}

// The same event twice gives one notification per recipient; a recipient
// whose account is deleted gets none (issue #64).
func TestNotificationRequestIsIdempotent(t *testing.T) {
	svc, pool := setup(t)
	tf, actor, a, b, gone := id.New(), id.New(), id.New(), id.New(), id.New()
	must(t, store.New(pool).TombstoneUserProfile(ctx, store.TombstoneUserProfileParams{UserID: gone.PG(), SourceEventID: id.New().PG()}))

	e := announcement(tf, actor, a, b, gone)
	must(t, svc.Apply(ctx, e))
	must(t, svc.Apply(ctx, e))
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE source_event_id = $1`, e.ID.PG()); n != 2 {
		t.Fatalf("%d bildirim", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE user_id = $1`, gone.PG()); n != 0 {
		t.Fatal("silinmiş hesaba bildirim yazıldı")
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE actor_id = $1 AND tayfa_id = $2 AND type = 'announcement'`, actor.PG(), tf.PG()); n != 2 {
		t.Fatalf("aktör/tayfa: %d", n)
	}
}

func TestPurgeRemovesTayfaNotifications(t *testing.T) {
	svc, pool := setup(t)
	tf, other, u := id.New(), id.New(), id.New()
	must(t, svc.Apply(ctx, announcement(tf, id.New(), u)))
	must(t, svc.Apply(ctx, announcement(other, id.New(), u)))
	must(t, store.New(pool).UpsertNotificationPreference(ctx, store.UpsertNotificationPreferenceParams{UserID: u.PG(), ScopeType: "tayfa", ScopeID: tf.PG(), Level: "none"}))
	// One channel in each tayfa; only the purged tayfa's goes.
	for _, tayfa := range []id.ID{tf, other} {
		_, err := store.New(pool).CreateChannel(ctx, store.CreateChannelParams{
			ID: id.New().PG(), TayfaID: tayfa.PG(), Name: "genel", NameNormalized: "genel", Visibility: "members",
		})
		must(t, err)
	}

	for range 2 {
		must(t, svc.Apply(ctx, Event{ID: id.New(), Tayfa: tf, Purged: true}))
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE user_id = $1`, u.PG()); n != 1 {
		t.Fatalf("%d bildirim kaldı", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM notification_preferences`); n != 0 {
		t.Fatal("tercih kaldı")
	}
	if n := count(t, pool, `SELECT count(*) FROM channels WHERE tayfa_id = $1`, tf.PG()); n != 0 {
		t.Fatal("kanal kaldı")
	}
	if n := count(t, pool, `SELECT count(*) FROM channels WHERE tayfa_id = $1`, other.PG()); n != 1 {
		t.Fatal("başka tayfanın kanalı silindi")
	}
}

func TestValidation(t *testing.T) {
	svc, _ := setup(t)
	tf, actor, u := id.New(), id.New(), id.New()
	many := make([]id.ID, notification.MaxRecipients+1)
	for i := range many {
		many[i] = id.New()
	}
	req := func(f func(r *notification.Request)) Event {
		e := announcement(tf, actor, u)
		f(e.Notification)
		return e
	}
	for name, e := range map[string]Event{
		"kimliksiz":      {Tayfa: tf, Purged: true},
		"tayfasız":       {ID: id.New(), Purged: true},
		"ikisi":          {ID: id.New(), Tayfa: tf, Purged: true, Notification: &notification.Request{}},
		"hiçbiri":        {ID: id.New(), Tayfa: tf},
		"alıcısız":       req(func(r *notification.Request) { r.Recipients = nil }),
		"çok alıcı":      req(func(r *notification.Request) { r.Recipients = many }),
		"aktör alıcı":    req(func(r *notification.Request) { r.Recipients = []id.ID{actor} }),
		"bilinmeyen tür": req(func(r *notification.Request) { r.Type = "poke" }),
		"yanlış hedef":   req(func(r *notification.Request) { r.TargetType = notification.TargetEvent }),
		"sorusuz cevap": req(func(r *notification.Request) {
			r.Type, r.TargetType = notification.TypeAnswerReceived, notification.TargetAnswer
		}),
		"yürüyüşçüsüz alarm": req(func(r *notification.Request) {
			r.Type, r.TargetType = notification.TypeHikeOverdue, notification.TargetEvent
		}),
		"nedensiz uyarı": req(func(r *notification.Request) {
			r.Type, r.TargetType = notification.TypeTayfaWarned, notification.TargetTayfa
		}),
		"duyuruda neden": req(func(r *notification.Request) { r.Reason = notification.ReasonInactive }),
		"tayfasız duyuru": {ID: id.New(), Notification: &notification.Request{
			Type: notification.TypeAnnouncement, TargetType: notification.TargetAnnouncement, Target: id.New(), Recipients: []id.ID{u},
		}},
		"tayfalı arkadaşlık": req(func(r *notification.Request) {
			r.Type, r.TargetType = notification.TypeFriendRequest, notification.TargetUser
		}),
		// Direct message notices are chat's own; core cannot ask for them.
		"core'dan DM bildirimi": {ID: id.New(), Notification: &notification.Request{
			Type: notification.TypeDMMessage, TargetType: notification.TargetDMConversation, Target: id.New(), Recipients: []id.ID{u},
		}},
		"arkadaşlık hedefi": {ID: id.New(), Notification: &notification.Request{
			Type: notification.TypeFriendAccepted, TargetType: notification.TargetTayfa, Target: id.New(), Recipients: []id.ID{u},
		}},
	} {
		var v *apperr.ValidationError
		if err := svc.Apply(ctx, e); !errors.As(err, &v) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Account deletion removes the user's notifications and clears them as an
// actor (ADR-0009); later requests skip them.
func TestAccountDeletionCleansNotifications(t *testing.T) {
	svc, pool := setup(t)
	tf, u, other := id.New(), id.New(), id.New()
	must(t, svc.Apply(ctx, announcement(tf, other, u)))
	must(t, svc.Apply(ctx, announcement(tf, u, other)))
	users, err := userevents.New(userevents.Config{Pool: pool, Cleanups: []userevents.Cleanup{notification.Cleanup}})
	must(t, err)
	must(t, users.Apply(ctx, userevents.Event{ID: id.New(), User: u, Deleted: true}))

	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE user_id = $1 OR actor_id = $1`, u.PG()); n != 0 {
		t.Fatalf("silinen hesabın %d izi kaldı", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE user_id = $1`, other.PG()); n != 1 {
		t.Fatal("başkasının bildirimi silindi")
	}
	must(t, svc.Apply(ctx, announcement(tf, other, u)))
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE user_id = $1`, u.PG()); n != 0 {
		t.Fatal("silinen hesaba yeni bildirim yazıldı")
	}
}

// Friend notifications are stored without a tayfa; an invitation names its
// tayfa (issue #71).
func TestSocialNotifications(t *testing.T) {
	svc, pool := setup(t)
	requester, friend, tf := id.New(), id.New(), id.New()
	for _, e := range []Event{
		{ID: id.New(), Notification: &notification.Request{Type: notification.TypeFriendRequest, Actor: requester,
			TargetType: notification.TargetUser, Target: requester, Recipients: []id.ID{friend}}},
		{ID: id.New(), Notification: &notification.Request{Type: notification.TypeFriendAccepted, Actor: friend,
			TargetType: notification.TargetUser, Target: friend, Recipients: []id.ID{requester}}},
		{ID: id.New(), Tayfa: tf, Notification: &notification.Request{Type: notification.TypeTayfaInvitation, Actor: requester,
			TargetType: notification.TargetTayfa, Target: tf, Recipients: []id.ID{friend}}},
	} {
		must(t, svc.Apply(ctx, e))
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE tayfa_id IS NULL AND target_type = 'user' AND type IN ('friend_request', 'friend_accepted')`); n != 2 {
		t.Fatalf("arkadaşlık bildirimleri = %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE user_id = $1 AND tayfa_id = $2 AND type = 'tayfa_invitation'`, friend.PG(), tf.PG()); n != 1 {
		t.Fatalf("davet bildirimi = %d", n)
	}
}

type fakeCache struct {
	forgot      [][2]id.ID
	forgotTayfa []id.ID
}

func (f *fakeCache) Forget(tayfa, user id.ID) { f.forgot = append(f.forgot, [2]id.ID{tayfa, user}) }
func (f *fakeCache) ForgetTayfa(tayfa id.ID)  { f.forgotTayfa = append(f.forgotTayfa, tayfa) }

// Membership and tayfa state changes only clear the channel permission
// cache; nothing is stored (chat keeps no copy of memberships).
func TestStateChangesClearCache(t *testing.T) {
	pool := dbtest.New(t, chatdb.Migrations())
	cache := &fakeCache{}
	svc, err := New(Config{Pool: pool, Access: cache})
	must(t, err)
	tf, u := id.New(), id.New()
	must(t, svc.Apply(ctx, Event{ID: id.New(), Tayfa: tf, MemberChanged: u}))
	must(t, svc.Apply(ctx, Event{ID: id.New(), Tayfa: tf, StatusChanged: true}))
	must(t, svc.Apply(ctx, Event{ID: id.New(), Tayfa: tf, Purged: true}))
	if len(cache.forgot) != 1 || cache.forgot[0] != [2]id.ID{tf, u} || len(cache.forgotTayfa) != 2 {
		t.Fatalf("önbellek = %+v", cache)
	}
	if n := count(t, pool, `SELECT count(*) FROM domain_events`); n != 0 {
		t.Fatal("olay yazıldı")
	}
	for name, e := range map[string]Event{
		"tayfasız üyelik": {ID: id.New(), MemberChanged: u},
		"tayfasız durum":  {ID: id.New(), StatusChanged: true},
		"iki tür":         {ID: id.New(), Tayfa: tf, MemberChanged: u, StatusChanged: true},
	} {
		var v *apperr.ValidationError
		if err := svc.Apply(ctx, e); !errors.As(err, &v) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// fakeLive records what reaches the real-time layer, and whether the cache
// had been cleared by then.
type fakeLive struct {
	cache  *fakeCache
	calls  []string
	notice []notification.Request
}

func (f *fakeLive) MembershipChanged(tayfa, user id.ID) {
	f.calls = append(f.calls, "member")
	if len(f.cache.forgot) == 0 {
		f.calls = append(f.calls, "ÖNBELLEK TEMİZLENMEDEN")
	}
}
func (f *fakeLive) TayfaChanged(id.ID) { f.calls = append(f.calls, "tayfa") }
func (f *fakeLive) TayfaPurged(id.ID)  { f.calls = append(f.calls, "purged") }
func (f *fakeLive) Notified(r notification.Request) {
	f.calls = append(f.calls, "notified")
	f.notice = append(f.notice, r)
}

// Membership and tayfa changes reach the live layer after the cache is
// cleared, so its rechecks ask core afresh; notifications after they are
// stored (issue #82).
func TestLiveHearsChanges(t *testing.T) {
	pool := dbtest.New(t, chatdb.Migrations())
	cache := &fakeCache{}
	live := &fakeLive{cache: cache}
	svc, err := New(Config{Pool: pool, Access: cache, Live: live})
	must(t, err)
	tf, u, actor := id.New(), id.New(), id.New()
	must(t, svc.Apply(ctx, Event{ID: id.New(), Tayfa: tf, MemberChanged: u}))
	must(t, svc.Apply(ctx, Event{ID: id.New(), Tayfa: tf, StatusChanged: true}))
	must(t, svc.Apply(ctx, Event{ID: id.New(), Tayfa: tf, Purged: true}))
	e := announcement(tf, actor, u)
	must(t, svc.Apply(ctx, e))
	want := []string{"member", "tayfa", "purged", "notified"}
	if len(live.calls) != len(want) {
		t.Fatalf("canlı katman = %v", live.calls)
	}
	for i := range want {
		if live.calls[i] != want[i] {
			t.Fatalf("canlı katman = %v", live.calls)
		}
	}
	if r := live.notice[0]; r.Event != e.ID || r.Tayfa != tf || len(r.Recipients) != 1 || r.Recipients[0] != u {
		t.Fatalf("bildirim = %+v", r)
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE source_event_id = $1`, e.ID.PG()); n != 1 {
		t.Fatal("bildirim yazılmadan iletildi")
	}
	// An invalid notification reaches nobody.
	bad := announcement(tf, actor)
	if err := svc.Apply(ctx, bad); err == nil {
		t.Fatal("alıcısız bildirim kabul edildi")
	}
	if len(live.calls) != len(want) {
		t.Fatalf("geçersiz olay iletildi: %v", live.calls)
	}
}
