package discover

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

var ctx = context.Background()

type env struct {
	pool  *pgxpool.Pool
	admin *pgxpool.Pool
	base  time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	return &env{pool: d.App, admin: d.Admin, base: time.Now().UTC().Truncate(time.Microsecond)}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) svc(t *testing.T, enabled bool, d time.Duration) *Service {
	t.Helper()
	now := e.base.Add(d)
	s, err := New(Config{Pool: e.pool, Enabled: enabled, Now: func() time.Time { return now }})
	must(t, err)
	return s
}

func (e *env) members(t *testing.T, enabled bool) *membership.Service {
	t.Helper()
	now := e.base
	s, err := membership.New(membership.Config{
		Pool: e.pool, JoinLimiter: ratelimit.NewPer(100, time.Hour), DiscoverEnabled: enabled,
		Now: func() time.Time { return now },
	})
	must(t, err)
	return s
}

// exec runs sql as the superuser with triggers off (seeding).
func (e *env) seedExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	must(t, tx.Commit(ctx))
}

// exec runs sql as the superuser with triggers on.
func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.admin.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) one(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s pgtype.Text
	if err := e.admin.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s.String
}

// user writes a live profile copy.
func (e *env) user(t *testing.T, name, visibility string) id.ID {
	t.Helper()
	u := id.New()
	e.seedExec(t, `INSERT INTO user_profiles (user_id, display_name, source_event_id, activity_visibility) VALUES ($1, $2, $3, $4)`,
		u.PG(), name, id.New().PG(), visibility)
	return u
}

func (e *env) friends(t *testing.T, a, b id.ID) {
	t.Helper()
	lo, hi := a, b
	if strings.Compare(hi.String(), lo.String()) < 0 {
		lo, hi = hi, lo
	}
	e.seedExec(t, `INSERT INTO friendships (user_low_id, user_high_id, status, requested_by, accepted_at) VALUES ($1, $2, 'accepted', $1, now())`,
		lo.PG(), hi.PG())
}

func (e *env) member(t *testing.T, tf, u id.ID, role, status, via string) {
	t.Helper()
	var joined any
	if status == "active" {
		joined = e.base
	}
	e.seedExec(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		tf.PG(), u.PG(), role, status, via, joined)
}

// listedTayfa creates a tayfa through the tayfa service with admin as its
// creator, adds extra active members up to n, and makes it listable
// (listed + approved).
func (e *env) listedTayfa(t *testing.T, n int) (tf, admin id.ID) {
	t.Helper()
	admin = e.user(t, "Yönetici", "tayfa")
	ts, err := tayfa.New(tayfa.Config{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	v, err := ts.Create(ctx, admin, tayfa.CreateInput{Name: "Yürüyüşçüler " + id.New().String()[:8], ClientRequestID: id.New().String()})
	must(t, err)
	tf = v.ID
	for range n - 1 {
		e.member(t, tf, e.user(t, "Üye", "tayfa"), "member", "active", "code")
	}
	e.seedExec(t, `UPDATE tayfas SET visibility = 'listed' WHERE id = $1`, tf.PG())
	e.seedExec(t, `INSERT INTO tayfa_discover (tayfa_id, status, requested_at, requested_by, reviewed_at, status_changed_at)
	    VALUES ($1, 'approved', now(), $2, now(), now())`, tf.PG(), admin.PG())
	return tf, admin
}

type eventOpts struct {
	discoverable bool
	mode         string
	start, end   time.Duration
	status       string
}

func defaultEvent() eventOpts {
	return eventOpts{discoverable: true, mode: "in_person", start: 48 * time.Hour, end: 50 * time.Hour, status: "scheduled"}
}

func (e *env) event(t *testing.T, tf, organizer id.ID, o eventOpts) id.ID {
	t.Helper()
	ev := id.New()
	if o.mode == "online" {
		e.seedExec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, discoverable, status, created_by)
		    VALUES ($1, $2, 'general', 'online', 'Çevrim içi', $3, $4, 'https://example.com/x', $5, $6, $7)`,
			ev.PG(), tf.PG(), e.base.Add(o.start), e.base.Add(o.end), o.discoverable, o.status, organizer.PG())
	} else {
		e.seedExec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, description, starts_at, ends_at, meeting_point_name, meeting_lat, meeting_lng, city, district, discoverable, status, cancelled_at, cancel_reason, created_by)
		    VALUES ($1, $2, 'hike', 'in_person', 'Belgrad yürüyüşü', 'Gizli adres: X sokak', $3, $4, 'Bahçeköy girişi', 41.17, 28.98, 'İstanbul', 'Sarıyer', $5, $6,
		            CASE WHEN $6 = 'cancelled' THEN now() END, CASE WHEN $6 = 'cancelled' THEN 'iptal' END, $7)`,
			ev.PG(), tf.PG(), e.base.Add(o.start), e.base.Add(o.end), o.discoverable, o.status, organizer.PG())
	}
	return ev
}

func (e *env) rsvp(t *testing.T, ev, u id.ID, status, attendance string) {
	t.Helper()
	var method any
	if attendance == "attended" {
		method = "organizer"
	}
	e.seedExec(t, `INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method) VALUES ($1, $2, $3, $4, $5)`,
		ev.PG(), u.PG(), status, attendance, method)
}

// scenario is the base case of the listing table: viewer's friend Ayşe is
// an active member of a listable five-member tayfa and going to its
// discoverable in-person event in two days.
type scenario struct {
	viewer, friend, tf, admin, ev id.ID
}

func (e *env) scenario(t *testing.T) scenario {
	t.Helper()
	tf, admin := e.listedTayfa(t, 4)
	friend := e.user(t, "Ayşe", "tayfa")
	e.member(t, tf, friend, "member", "active", "code")
	viewer := e.user(t, "İzleyici", "tayfa")
	e.friends(t, viewer, friend)
	ev := e.event(t, tf, admin, defaultEvent())
	e.rsvp(t, ev, friend, "going", "unknown")
	return scenario{viewer: viewer, friend: friend, tf: tf, admin: admin, ev: ev}
}

func (e *env) feed(t *testing.T, viewer id.ID) []Item {
	t.Helper()
	items, err := e.svc(t, true, 0).Feed(ctx, viewer)
	must(t, err)
	return items
}

// TestFeatureDisabled: with DISCOVER_ENABLED off nothing is listed and the
// user-facing methods fail before reading anything; the tayfa setting
// works.
func TestFeatureDisabled(t *testing.T) {
	e := setup(t)
	sc := e.scenario(t)
	if items := e.feed(t, sc.viewer); len(items) != 1 {
		t.Fatalf("bayrak açıkken akış: %d", len(items))
	}
	off := e.svc(t, false, 0)
	items, err := off.Feed(ctx, sc.viewer)
	if !errors.Is(err, apperr.ErrFeatureDisabled) || items != nil {
		t.Fatalf("kapalı akış: %v %v", items, err)
	}
	if _, err := e.members(t, false).JoinFromDiscover(ctx, sc.viewer, sc.tf); !errors.Is(err, apperr.ErrFeatureDisabled) {
		t.Fatalf("kapalı katılım: %v", err)
	}
	if _, err := off.SetFeedback(ctx, sc.friend, sc.tf, sc.ev, AnswerOK); !errors.Is(err, apperr.ErrFeatureDisabled) {
		t.Fatalf("kapalı geri bildirim: %v", err)
	}
	if _, err := off.SetSafetyContact(ctx, sc.friend, sc.tf, sc.ev, sc.viewer, 0); !errors.Is(err, apperr.ErrFeatureDisabled) {
		t.Fatalf("kapalı güvenlik kişisi: %v", err)
	}
	if _, err := off.ListSafetyShares(ctx, sc.viewer); !errors.Is(err, apperr.ErrFeatureDisabled) {
		t.Fatalf("kapalı paylaşımlar: %v", err)
	}
	// The admins' setting does not depend on the flag.
	if st, err := off.GetTayfa(ctx, sc.admin, sc.tf); err != nil || st.State != StateListed {
		t.Fatalf("kapalıyken ayar: %+v %v", st, err)
	}
}

// TestFeedListing is the listing table: the base case is listed; every
// rule takes it out (ADR-0033 §3, §4).
func TestFeedListing(t *testing.T) {
	e := setup(t)
	for name, tc := range map[string]struct {
		change func(t *testing.T, sc scenario)
		listed bool
	}{
		"temel durum": {func(*testing.T, scenario) {}, true},
		"5 üyenin altı": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `DELETE FROM memberships WHERE tayfa_id = $1 AND role = 'member' AND user_id <> $2 AND user_id IN
			    (SELECT user_id FROM memberships WHERE tayfa_id = $1 AND role = 'member' AND user_id <> $2 LIMIT 1)`, sc.tf.PG(), sc.friend.PG())
		}, false},
		"panel onayı bekliyor": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE tayfa_discover SET status = 'pending' WHERE tayfa_id = $1`, sc.tf.PG())
		}, false},
		"başvuru yok": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `DELETE FROM tayfa_discover WHERE tayfa_id = $1`, sc.tf.PG())
		}, false},
		"reddedildi": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE tayfa_discover SET status = 'rejected' WHERE tayfa_id = $1`, sc.tf.PG())
		}, false},
		"düşürüldü": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE tayfa_discover SET status = 'dropped', drop_reason = 'member_report' WHERE tayfa_id = $1`, sc.tf.PG())
		}, false},
		"yönetici kapattı (gizli tayfa)": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE tayfas SET visibility = 'private' WHERE id = $1`, sc.tf.PG())
		}, false},
		"askıda": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE tayfas SET suspended_at = now(), suspended_reason = 'inceleme', suspended_by = $2 WHERE id = $1`, sc.tf.PG(), id.New().PG())
		}, false},
		"arşivlendi": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE tayfas SET status = 'archived', warned_at = now(), archived_at = now() WHERE id = $1`, sc.tf.PG())
		}, false},
		"etkinlik işaretsiz (özel adres)": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE events SET discoverable = false WHERE id = $1`, sc.ev.PG())
		}, false},
		"etkinlik iptal": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE events SET status = 'cancelled', cancelled_at = now(), cancel_reason = 'x', cancelled_by = $2 WHERE id = $1`, sc.ev.PG(), sc.admin.PG())
		}, false},
		"etkinlik silindi": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE events SET deleted_at = now(), deleted_by = $2 WHERE id = $1`, sc.ev.PG(), sc.admin.PG())
		}, false},
		"etkinlik bitti": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE events SET starts_at = $2, ends_at = $3 WHERE id = $1`, sc.ev.PG(), e.base.Add(-3*time.Hour), e.base.Add(-time.Hour))
		}, false},
		"30 günden sonra": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE events SET starts_at = $2, ends_at = $3 WHERE id = $1`, sc.ev.PG(), e.base.Add(31*24*time.Hour), e.base.Add(31*24*time.Hour+time.Hour))
		}, false},
		"süren etkinlikte katıldı": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE events SET starts_at = $2, ends_at = $3 WHERE id = $1`, sc.ev.PG(), e.base.Add(-time.Hour), e.base.Add(time.Hour))
			e.seedExec(t, `UPDATE event_rsvps SET status = 'maybe', attendance = 'attended', attendance_method = 'organizer' WHERE event_id = $1`, sc.ev.PG())
		}, true},
		"arkadaş belki dedi": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE event_rsvps SET status = 'maybe' WHERE event_id = $1`, sc.ev.PG())
		}, false},
		"arkadaşın geçmişi gizli": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE user_profiles SET activity_visibility = 'private' WHERE user_id = $1`, sc.friend.PG())
		}, false},
		"arkadaşın geçmişi herkese açık": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE user_profiles SET activity_visibility = 'public' WHERE user_id = $1`, sc.friend.PG())
		}, true},
		"arkadaşlık isteği bekliyor": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `UPDATE friendships SET status = 'pending', accepted_at = NULL`)
		}, false},
		"arkadaş engelledi": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, sc.friend.PG(), sc.viewer.PG())
		}, false},
		"arkadaş platformda banlı": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `INSERT INTO user_platform_bans (user_id, banned_at, event_id) VALUES ($1, now(), $2)`, sc.friend.PG(), id.New().PG())
		}, false},
		"arkadaş tayfadan ayrıldı": {func(t *testing.T, sc scenario) {
			e.seedExec(t, `DELETE FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, sc.tf.PG(), sc.friend.PG())
			e.member(t, sc.tf, e.user(t, "Yeni", "tayfa"), "member", "active", "code")
		}, false},
		"izleyici zaten üye": {func(t *testing.T, sc scenario) {
			e.member(t, sc.tf, sc.viewer, "member", "active", "code")
		}, false},
		"izleyicinin isteği bekliyor": {func(t *testing.T, sc scenario) {
			e.member(t, sc.tf, sc.viewer, "member", "pending", "code")
		}, false},
		"izleyici tayfada banlı": {func(t *testing.T, sc scenario) {
			e.member(t, sc.tf, sc.viewer, "member", "banned", "code")
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			sc := e.scenario(t)
			tc.change(t, sc)
			items := e.feed(t, sc.viewer)
			if got := len(items) == 1; got != tc.listed {
				t.Fatalf("listelendi mi = %v, beklenen %v (%d öğe)", got, tc.listed, len(items))
			}
			ok, err := store.New(e.pool).DiscoverTayfaVisible(ctx, store.DiscoverTayfaVisibleParams{
				Viewer: sc.viewer.PG(), Now: pgtype.Timestamptz{Time: e.base, Valid: true}, TayfaID: sc.tf.PG(),
			})
			must(t, err)
			if ok != tc.listed {
				t.Fatalf("katılım kuralı akışla aynı değil: %v", ok)
			}
		})
	}
}

// TestInstantTayfaNeverListed (ADR-0033 §12): until #209 adds the tayfa
// kind, an instant tayfa is a private tayfa; a private tayfa is never
// listed, even with an approved application. Whichever of #209 and #210
// merges second extends this test with the kind column.
func TestInstantTayfaNeverListed(t *testing.T) {
	e := setup(t)
	sc := e.scenario(t)
	e.seedExec(t, `UPDATE tayfas SET visibility = 'private' WHERE id = $1`, sc.tf.PG())
	if items := e.feed(t, sc.viewer); len(items) != 0 {
		t.Fatalf("gizli tayfa listelendi: %d", len(items))
	}
	var listable bool
	must(t, e.admin.QueryRow(ctx, `SELECT tayfa_discover_listable($1)`, sc.tf.PG()).Scan(&listable))
	if listable {
		t.Fatal("gizli tayfa listelenebilir sayıldı")
	}
}

// TestFeedItem: facts, visible friends only, no location, no score.
func TestFeedItem(t *testing.T) {
	e := setup(t)
	sc := e.scenario(t)
	// A second visible friend and a hidden one at the same event.
	can := e.user(t, "Can", "public")
	e.member(t, sc.tf, can, "member", "active", "code")
	e.friends(t, sc.viewer, can)
	e.rsvp(t, sc.ev, can, "going", "unknown")
	hidden := e.user(t, "Gizli", "private")
	e.member(t, sc.tf, hidden, "member", "active", "code")
	e.friends(t, sc.viewer, hidden)
	e.rsvp(t, sc.ev, hidden, "going", "unknown")
	// Facts: one frozen event with two verified attendances; a recent one
	// does not count yet.
	old := e.event(t, sc.tf, sc.admin, eventOpts{discoverable: false, mode: "in_person", start: -10 * 24 * time.Hour, end: -10*24*time.Hour + time.Hour, status: "scheduled"})
	e.rsvp(t, old, sc.friend, "going", "attended")
	e.rsvp(t, old, can, "going", "attended")
	recent := e.event(t, sc.tf, sc.admin, eventOpts{discoverable: false, mode: "in_person", start: -2 * 24 * time.Hour, end: -2*24*time.Hour + time.Hour, status: "scheduled"})
	e.rsvp(t, recent, sc.friend, "going", "attended")

	items := e.feed(t, sc.viewer)
	if len(items) != 1 {
		t.Fatalf("öğe: %d", len(items))
	}
	it := items[0]
	if it.FriendCount != 2 || len(it.Friends) != 2 || it.EventCount != 1 || it.AttendanceCount != 2 {
		t.Fatalf("öğe: %+v", it)
	}
	for _, f := range it.Friends {
		if f.ID == hidden {
			t.Fatal("geçmişi gizli arkadaş gösterildi")
		}
	}
	if it.City != "İstanbul" || it.District != "Sarıyer" || it.EventID != sc.ev || it.TayfaID != sc.tf {
		t.Fatalf("öğe alanları: %+v", it)
	}
	// The item type has no place for a score, coordinates, a meeting point
	// or the description.
	typ := reflect.TypeOf(Item{})
	for i := range typ.NumField() {
		n := strings.ToLower(typ.Field(i).Name)
		for _, banned := range []string{"score", "rank", "lat", "lng", "meeting", "description"} {
			if strings.Contains(n, banned) && n != "tayfadescription" {
				t.Errorf("Item alanı %s", typ.Field(i).Name)
			}
		}
	}
}

// TestFeedRanking: the friendship score orders the feed while both sides
// profile; with the viewer's profiling off, the number of friends decides.
func TestFeedRanking(t *testing.T) {
	e := setup(t)
	sc := e.scenario(t) // event 1: Ayşe, in two days
	// Event 2 in another tayfa, three days ahead, with Bora and Cem.
	tf2, admin2 := e.listedTayfa(t, 3)
	bora, cem := e.user(t, "Bora", "tayfa"), e.user(t, "Cem", "tayfa")
	for _, u := range []id.ID{bora, cem} {
		e.member(t, tf2, u, "member", "active", "code")
		e.friends(t, sc.viewer, u)
	}
	o := defaultEvent()
	o.start, o.end = 72*time.Hour, 74*time.Hour
	ev2 := e.event(t, tf2, admin2, o)
	e.rsvp(t, ev2, bora, "going", "unknown")
	e.rsvp(t, ev2, cem, "going", "unknown")

	order := func() []id.ID {
		var out []id.ID
		for _, it := range e.feed(t, sc.viewer) {
			out = append(out, it.EventID)
		}
		return out
	}
	// No scores: more friends first.
	if got := order(); len(got) != 2 || got[0] != ev2 {
		t.Fatalf("puansız sıra: %v", got)
	}
	// A strong score with Ayşe puts event 1 first.
	score := func(a, b id.ID, s float64) {
		lo, hi := a, b
		if strings.Compare(hi.String(), lo.String()) < 0 {
			lo, hi = hi, lo
		}
		e.seedExec(t, `INSERT INTO friend_scores (user_low_id, user_high_id, score, shared_count, last_shared_at, computed_at) VALUES ($1, $2, $3, 5, now(), now())`,
			lo.PG(), hi.PG(), s)
	}
	score(sc.viewer, sc.friend, 9)
	if got := order(); got[0] != sc.ev {
		t.Fatalf("puanlı sıra: %v", got)
	}
	// The viewer turns profiling off: the score is not used.
	e.seedExec(t, `INSERT INTO social_settings (user_id, profiling_enabled) VALUES ($1, false)`, sc.viewer.PG())
	if got := order(); got[0] != ev2 {
		t.Fatalf("izleyicinin profillemesi kapalı: %v", got)
	}
	// The friend's profiling off has the same effect.
	e.seedExec(t, `DELETE FROM social_settings WHERE user_id = $1`, sc.viewer.PG())
	e.seedExec(t, `INSERT INTO social_settings (user_id, profiling_enabled) VALUES ($1, false)`, sc.friend.PG())
	if got := order(); got[0] != ev2 {
		t.Fatalf("arkadaşın profillemesi kapalı: %v", got)
	}
}

// TestTayfaSetting: admins turn Keşfet on (five members, into the queue)
// and off; the state never says why a tayfa was taken out.
func TestTayfaSetting(t *testing.T) {
	e := setup(t)
	s := e.svc(t, false, 0)
	admin := e.user(t, "Yönetici", "tayfa")
	ts, err := tayfa.New(tayfa.Config{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	v, err := ts.Create(ctx, admin, tayfa.CreateInput{Name: "Kitap Kulübü", ClientRequestID: id.New().String()})
	must(t, err)
	tf := v.ID
	plain := e.user(t, "Üye", "tayfa")
	e.member(t, tf, plain, "member", "active", "code")

	if st, err := s.GetTayfa(ctx, admin, tf); err != nil || st != (TayfaState{State: StateOff}) {
		t.Fatalf("başlangıç: %+v %v", st, err)
	}
	if _, err := s.SetTayfa(ctx, plain, tf, true); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("üye açtı: %v", err)
	}
	if _, err := s.GetTayfa(ctx, e.user(t, "Yabancı", "tayfa"), tf); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("yabancı okudu: %v", err)
	}
	if _, err := s.SetTayfa(ctx, admin, tf, true); !errors.Is(err, apperr.ErrDiscoverTooFewMembers) {
		t.Fatalf("2 üyeyle açıldı: %v", err)
	}
	for range 3 {
		e.member(t, tf, e.user(t, "Üye", "tayfa"), "member", "active", "code")
	}
	st, err := s.SetTayfa(ctx, admin, tf, true)
	if err != nil || st != (TayfaState{State: StatePending, Enabled: true}) {
		t.Fatalf("açma: %+v %v", st, err)
	}
	if n := e.one(t, `SELECT count(*)::text FROM audit_logs WHERE tayfa_id = $1 AND action = 'tayfa.discover_enabled'`, tf.PG()); n != "1" {
		t.Fatalf("denetim kaydı: %s", n)
	}
	e.exec(t, `UPDATE tayfa_discover SET status = 'approved', reviewed_at = now() WHERE tayfa_id = $1`, tf.PG())
	if st, _ := s.GetTayfa(ctx, admin, tf); st.State != StateListed {
		t.Fatalf("onaylı: %+v", st)
	}
	// Turning it off and on keeps the approval.
	if st, _ := s.SetTayfa(ctx, admin, tf, false); st != (TayfaState{State: StateOff}) {
		t.Fatalf("kapatma: %+v", st)
	}
	if st, _ := s.SetTayfa(ctx, admin, tf, true); st.State != StateListed {
		t.Fatalf("yeniden açma: %+v", st)
	}
	// Dropped: under review, and turning it off and on does not escape it.
	e.exec(t, `UPDATE tayfa_discover SET status = 'dropped', drop_reason = 'member_report' WHERE tayfa_id = $1`, tf.PG())
	_, _ = s.SetTayfa(ctx, admin, tf, false)
	if st, _ := s.SetTayfa(ctx, admin, tf, true); st.State != StateUnderReview {
		t.Fatalf("düşürülmüş: %+v", st)
	}
	// Rejected: turning it on asks again.
	e.exec(t, `UPDATE tayfa_discover SET status = 'rejected', drop_reason = NULL, reviewed_at = now() WHERE tayfa_id = $1`, tf.PG())
	if st, _ := s.GetTayfa(ctx, admin, tf); st.State != StateRejected {
		t.Fatalf("ret: %+v", st)
	}
	_, _ = s.SetTayfa(ctx, admin, tf, false)
	if st, _ := s.SetTayfa(ctx, admin, tf, true); st.State != StatePending {
		t.Fatalf("retten sonra yeniden başvuru: %+v", st)
	}
}

// TestRereviewOnMetadataChange: the platform approved metadata; changed
// metadata goes back to review.
func TestRereviewOnMetadataChange(t *testing.T) {
	e := setup(t)
	sc := e.scenario(t)
	e.exec(t, `UPDATE tayfas SET last_activity_at = now() WHERE id = $1`, sc.tf.PG())
	if st := e.one(t, `SELECT status FROM tayfa_discover WHERE tayfa_id = $1`, sc.tf.PG()); st != "approved" {
		t.Fatalf("ilgisiz değişiklik: %s", st)
	}
	e.exec(t, `UPDATE tayfas SET description = 'Yeni açıklama' WHERE id = $1`, sc.tf.PG())
	if st := e.one(t, `SELECT status FROM tayfa_discover WHERE tayfa_id = $1`, sc.tf.PG()); st != "pending" {
		t.Fatalf("açıklama değişti: %s", st)
	}
	if items := e.feed(t, sc.viewer); len(items) != 0 {
		t.Fatal("yeniden incelemedeki tayfa listelendi")
	}
}

// report files a report in tf about target (an event of it, or the tayfa
// itself when target is zero, which goes to the platform at once).
func (e *env) report(t *testing.T, tf, reporter, target id.ID) {
	t.Helper()
	if target == (id.ID{}) {
		e.exec(t, `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, reason, platform_only,
		        escalated_to_platform_at, escalation_reason, platform_status)
		    VALUES ($1, $2, $3, 'tayfa', $2, 'other', true, now(), 'tayfa_target', 'open')`, id.New().PG(), tf.PG(), reporter.PG())
		return
	}
	e.exec(t, `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, reason) VALUES ($1, $2, $3, 'event', $4, 'fake_event')`,
		id.New().PG(), tf.PG(), reporter.PG(), target.PG())
}

// TestDropOnDiscoverMemberReport: a report by a member who came from
// Keşfet drops the tayfa; other members' reports do not.
func TestDropOnDiscoverMemberReport(t *testing.T) {
	e := setup(t)
	sc := e.scenario(t)
	e.report(t, sc.tf, sc.friend, sc.ev) // joined by code
	e.report(t, sc.tf, sc.friend, id.ID{})
	if st := e.one(t, `SELECT status FROM tayfa_discover WHERE tayfa_id = $1`, sc.tf.PG()); st != "approved" {
		t.Fatalf("kodla gelen üyenin şikâyeti düşürdü: %s", st)
	}
	newcomer := e.user(t, "Yeni", "tayfa")
	e.member(t, sc.tf, newcomer, "member", "active", "discover")
	e.report(t, sc.tf, newcomer, sc.ev) // any report in the tayfa, not only about it
	if st := e.one(t, `SELECT status || '/' || drop_reason FROM tayfa_discover WHERE tayfa_id = $1`, sc.tf.PG()); st != "dropped/member_report" {
		t.Fatalf("Keşfet üyesinin şikâyeti: %s", st)
	}
	if items := e.feed(t, sc.viewer); len(items) != 0 {
		t.Fatal("düşürülen tayfa listelendi")
	}
	if st, _ := e.svc(t, true, 0).GetTayfa(ctx, sc.admin, sc.tf); st.State != StateUnderReview {
		t.Fatalf("yöneticinin gördüğü: %+v", st)
	}
}

// TestJoinFromDiscover: only a tayfa in the caller's feed, always a pending
// request labelled discover; the trigger refuses anything else.
func TestJoinFromDiscover(t *testing.T) {
	e := setup(t)
	sc := e.scenario(t)
	e.exec(t, `UPDATE tayfas SET join_policy = 'open' WHERE id = $1`, sc.tf.PG())
	m := e.members(t, true)

	stranger := e.user(t, "Yabancı", "tayfa")
	if _, err := m.JoinFromDiscover(ctx, stranger, sc.tf); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("akışında olmayan katıldı: %v", err)
	}
	if _, err := m.JoinFromDiscover(ctx, sc.viewer, id.New()); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("bilinmeyen tayfa: %v", err)
	}
	got, err := m.JoinFromDiscover(ctx, sc.viewer, sc.tf)
	if err != nil || got.Status != "pending" {
		t.Fatalf("katılım: %+v %v", got, err)
	}
	if via := e.one(t, `SELECT status || '/' || joined_via FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, sc.tf.PG(), sc.viewer.PG()); via != "pending/discover" {
		t.Fatalf("üyelik: %s", via)
	}
	if _, err := m.JoinFromDiscover(ctx, sc.viewer, sc.tf); !errors.Is(err, apperr.ErrAlreadyPending) {
		t.Fatalf("ikinci istek: %v", err)
	}
	// Staff see the label, members do not.
	page, err := m.ListPending(ctx, sc.admin, sc.tf, 0, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].JoinedVia != "discover" {
		t.Fatalf("bekleyenler: %+v %v", page, err)
	}
	_, err = m.ApproveMember(ctx, sc.admin, sc.tf, sc.viewer)
	must(t, err)
	staff, err := m.GetMember(ctx, sc.admin, sc.tf, sc.viewer)
	if err != nil || !staff.FromDiscover {
		t.Fatalf("yönetici etiketi görmedi: %+v %v", staff, err)
	}
	other, err := m.GetMember(ctx, sc.friend, sc.tf, sc.viewer)
	if err != nil || other.FromDiscover {
		t.Fatalf("üye etiketi gördü: %+v %v", other, err)
	}

	// The trigger: a discover membership is pending and only into a
	// listable tayfa, whatever the caller.
	u := e.user(t, "Doğrudan", "tayfa")
	for _, tc := range []struct{ status string }{{"active"}} {
		_, err := e.pool.Exec(ctx, `SELECT set_config('app.actor_id', $1, false)`, u.String())
		must(t, err)
		_, err = e.pool.Exec(ctx, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at) VALUES ($1, $2, 'member', $3, 'discover', now())`,
			sc.tf.PG(), u.PG(), tc.status)
		if err == nil {
			t.Fatalf("trigger %s Keşfet üyeliğine izin verdi", tc.status)
		}
	}
	e.exec(t, `UPDATE tayfas SET visibility = 'private' WHERE id = $1`, sc.tf.PG())
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT set_config('app.actor_id', $1, true)`, u.String())
	must(t, err)
	if _, err := tx.Exec(ctx, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via) VALUES ($1, $2, 'member', 'pending', 'discover')`,
		sc.tf.PG(), u.PG()); err == nil {
		t.Fatal("trigger gizli tayfaya Keşfet isteğine izin verdi")
	}
}

// TestFeedback: attendees of discoverable in-person events, in the window;
// a serious answer drops the tayfa.
func TestFeedback(t *testing.T) {
	e := setup(t)
	sc := e.scenario(t)
	o := defaultEvent()
	o.start, o.end = -3*time.Hour, -time.Hour
	past := e.event(t, sc.tf, sc.admin, o)
	e.rsvp(t, past, sc.friend, "going", "attended")
	notThere := e.user(t, "Gelmeyen", "tayfa")
	e.member(t, sc.tf, notThere, "member", "active", "code")
	e.rsvp(t, past, notThere, "going", "no_show")
	s := e.svc(t, true, 0)

	if _, err := s.SetFeedback(ctx, sc.friend, sc.tf, past, "harika"); err == nil {
		t.Fatal("geçersiz cevap kabul edildi")
	}
	if _, err := s.SetFeedback(ctx, notThere, sc.tf, past, AnswerOK); !errors.Is(err, apperr.ErrNotAttended) {
		t.Fatalf("katılmayan: %v", err)
	}
	if _, err := s.SetFeedback(ctx, sc.friend, sc.tf, sc.ev, AnswerOK); !errors.Is(err, apperr.ErrFeedbackClosed) {
		t.Fatalf("bitmemiş etkinlik: %v", err)
	}
	if _, err := e.svc(t, true, 8*24*time.Hour).SetFeedback(ctx, sc.friend, sc.tf, past, AnswerOK); !errors.Is(err, apperr.ErrFeedbackClosed) {
		t.Fatalf("7 günden sonra: %v", err)
	}
	o.discoverable = false
	plain := e.event(t, sc.tf, sc.admin, o)
	e.rsvp(t, plain, sc.friend, "going", "attended")
	if _, err := s.SetFeedback(ctx, sc.friend, sc.tf, plain, AnswerOK); !errors.Is(err, apperr.ErrFeedbackClosed) {
		t.Fatalf("Keşfet'e uygun olmayan etkinlik: %v", err)
	}

	f, err := s.SetFeedback(ctx, sc.friend, sc.tf, past, AnswerNotOK)
	if err != nil || f.Answer != AnswerNotOK {
		t.Fatalf("cevap: %+v %v", f, err)
	}
	if st := e.one(t, `SELECT status FROM tayfa_discover WHERE tayfa_id = $1`, sc.tf.PG()); st != "approved" {
		t.Fatalf("not_ok düşürdü: %s", st)
	}
	if got, _ := s.GetFeedback(ctx, sc.friend, sc.tf, past); got.Answer != AnswerNotOK {
		t.Fatalf("okuma: %+v", got)
	}
	if got, _ := s.GetFeedback(ctx, sc.admin, sc.tf, past); got.Answer != "" {
		t.Fatalf("başkasının cevabı göründü: %+v", got)
	}
	if _, err := s.SetFeedback(ctx, sc.friend, sc.tf, past, AnswerSerious); err != nil {
		t.Fatal(err)
	}
	if st := e.one(t, `SELECT status || '/' || drop_reason FROM tayfa_discover WHERE tayfa_id = $1`, sc.tf.PG()); st != "dropped/negative_feedback" {
		t.Fatalf("ciddi cevap: %s", st)
	}
}

// TestSafetyContact: a going participant chooses one friend; the share is
// visible to that friend alone, without coordinates.
func TestSafetyContact(t *testing.T) {
	e := setup(t)
	sc := e.scenario(t)
	s := e.svc(t, true, 0)
	contact := e.user(t, "Anne", "tayfa")
	e.friends(t, sc.friend, contact)
	notFriend := e.user(t, "Yabancı", "tayfa")

	if _, err := s.SetSafetyContact(ctx, sc.friend, sc.tf, sc.ev, notFriend, 0); !errors.Is(err, apperr.ErrNotFriends) {
		t.Fatalf("arkadaş olmayan: %v", err)
	}
	if _, err := s.SetSafetyContact(ctx, sc.friend, sc.tf, sc.ev, sc.friend, 0); !errors.Is(err, apperr.ErrNotFriends) {
		t.Fatalf("kendisi: %v", err)
	}
	if _, err := s.SetSafetyContact(ctx, sc.friend, sc.tf, sc.ev, contact, 10); err == nil {
		t.Fatal("15 dakikadan kısa tolerans kabul edildi")
	}
	if _, err := s.SetSafetyContact(ctx, sc.admin, sc.tf, sc.ev, contact, 0); !errors.Is(err, apperr.ErrEventNotGoing) {
		t.Fatalf("gitmeyen: %v", err)
	}
	c, err := s.SetSafetyContact(ctx, sc.friend, sc.tf, sc.ev, contact, 0)
	if err != nil || c.Contact.ID != contact || c.Contact.DisplayName != "Anne" || c.GraceMinutes != DefaultGrace {
		t.Fatalf("seçim: %+v %v", c, err)
	}
	shares, err := s.ListSafetyShares(ctx, contact)
	if err != nil || len(shares) != 1 || shares[0].MeetingPointName != "Bahçeköy girişi" || shares[0].Participant.ID != sc.friend {
		t.Fatalf("paylaşımlar: %+v %v", shares, err)
	}
	for _, u := range []id.ID{sc.viewer, notFriend, sc.admin} {
		if other, _ := s.ListSafetyShares(ctx, u); len(other) != 0 {
			t.Fatalf("başkası paylaşımı gördü: %+v", other)
		}
	}
	// The share type has no coordinates.
	typ := reflect.TypeOf(Share{})
	for i := range typ.NumField() {
		if n := strings.ToLower(typ.Field(i).Name); strings.Contains(n, "lat") || strings.Contains(n, "lng") {
			t.Errorf("Share alanı %s", typ.Field(i).Name)
		}
	}
	got, err := s.MarkReturned(ctx, sc.friend, sc.tf, sc.ev)
	if err != nil || got.ReturnedAt.IsZero() {
		t.Fatalf("döndüm: %+v %v", got, err)
	}
	if _, err := s.MarkReturned(ctx, sc.viewer, sc.tf, sc.ev); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("üye olmayan döndüm dedi: %v", err)
	}
	must(t, s.ClearSafetyContact(ctx, sc.friend, sc.tf, sc.ev))
	if _, err := s.MarkReturned(ctx, sc.friend, sc.tf, sc.ev); !errors.Is(err, apperr.ErrSafetyContactNotFound) {
		t.Fatalf("seçilmeden döndüm: %v", err)
	}
	// An online event, or one that ended, takes no safety contact.
	o := defaultEvent()
	o.mode = "online"
	online := e.event(t, sc.tf, sc.admin, o)
	e.rsvp(t, online, sc.friend, "going", "unknown")
	if _, err := s.SetSafetyContact(ctx, sc.friend, sc.tf, online, contact, 0); !errors.Is(err, apperr.ErrSafetyContactClosed) {
		t.Fatalf("çevrim içi: %v", err)
	}
	if _, err := e.svc(t, true, 51*time.Hour).SetSafetyContact(ctx, sc.friend, sc.tf, sc.ev, contact, 0); !errors.Is(err, apperr.ErrSafetyContactClosed) {
		t.Fatalf("bitmiş: %v", err)
	}
}

// TestExportAndAccountDeletion: export holds one's own rows; account
// deletion removes feedback and safety contacts both ways.
func TestExportAndAccountDeletion(t *testing.T) {
	e := setup(t)
	sc := e.scenario(t)
	contact := e.user(t, "Anne", "tayfa")
	e.friends(t, sc.friend, contact)
	_, err := e.svc(t, true, 0).SetSafetyContact(ctx, sc.friend, sc.tf, sc.ev, contact, 30)
	must(t, err)
	q := store.New(e.pool)
	rows, err := q.ExportDiscover(ctx, store.ExportDiscoverParams{UserID: sc.friend.PG(), After: id.ID{}.PG(), N: 10})
	if err != nil || len(rows) != 1 || !strings.Contains(string(rows[0].Data), contact.String()) {
		t.Fatalf("dışa aktarma: %v %v", rows, err)
	}
	if rows, _ := q.ExportDiscover(ctx, store.ExportDiscoverParams{UserID: contact.PG(), After: id.ID{}.PG(), N: 10}); len(rows) != 0 {
		t.Fatalf("güvenlik kişisinin dışa aktarımında başkasının verisi: %v", rows)
	}
	must(t, q.DeleteUserSafetyContacts(ctx, contact.PG()))
	if n := e.one(t, `SELECT count(*)::text FROM event_safety_contacts`); n != "0" {
		t.Fatalf("hesap silmede güvenlik kişisi kaldı: %s", n)
	}
}
