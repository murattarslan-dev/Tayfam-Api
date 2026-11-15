package jobs

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Friendship score and friend suggestions (issue #208, ADR-0028).

// fakeMinors answers IsMinor from a map; users missing from it are not
// answered (as identity does for unknown users).
type fakeMinors struct {
	minor map[id.ID]bool
	err   error
	asked [][]id.ID
}

func (f *fakeMinors) IsMinor(_ context.Context, users []id.ID) (map[id.ID]bool, error) {
	f.asked = append(f.asked, users)
	if f.err != nil {
		return nil, f.err
	}
	out := map[id.ID]bool{}
	for _, u := range users {
		if m, ok := f.minor[u]; ok {
			out[u] = m
		}
	}
	return out, nil
}

// suggester returns a Service at base with minors.
func (e *env) suggester(t *testing.T, minors MinorChecker) *Service {
	t.Helper()
	now := e.base
	s, err := New(Config{Pool: e.pool, Log: e.log, Now: func() time.Time { return now }, Minors: minors})
	must(t, err)
	return s
}

// profile writes a live profile copy for u.
func (e *env) profile(t *testing.T, u id.ID) {
	t.Helper()
	e.exec(t, `INSERT INTO user_profiles (user_id, display_name, source_event_id) VALUES ($1, 'Kişi', $2)`, u.PG(), id.New().PG())
}

// attendTogether creates n events in tf that started age ago, with every
// user attended (check-in verified).
func (e *env) attendTogether(t *testing.T, tf id.ID, n int, age time.Duration, users ...id.ID) {
	t.Helper()
	for range n {
		ev := id.New()
		start := e.base.Add(-age)
		e.exec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, meeting_point_name, meeting_lat, meeting_lng, city, district)
		    VALUES ($1, $2, 'general', 'in_person', 'Buluşma', $3, $4, 'Meydan', 38.4, 27.1, 'İzmir', 'Konak')`,
			ev.PG(), tf.PG(), start, start.Add(2*time.Hour))
		for _, u := range users {
			e.exec(t, `INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method, checked_in_at)
			    VALUES ($1, $2, 'going', 'attended', 'location', $3)`, ev.PG(), u.PG(), start)
		}
	}
}

func (e *env) suggested(t *testing.T, to, who id.ID) bool {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM friend_suggestions WHERE user_id = $1 AND suggested_user_id = $2 AND source = 'suggested'`, to.PG(), who.PG()) == 1
}

func (e *env) socials(t *testing.T) *social.Service {
	t.Helper()
	now := e.base
	s, err := social.New(social.Config{Pool: e.pool, Now: func() time.Time { return now }})
	must(t, err)
	return s
}

func TestFriendSuggestionRules(t *testing.T) {
	adult, minor := false, true
	cases := []struct {
		name string
		// ageA, ageB: identity's answer; nil means no answer.
		ageA, ageB *bool
		shared     int
		age        time.Duration
		identity   error
		// prepare runs before the job.
		prepare func(t *testing.T, e *env, tf, a, b id.ID)
		want    bool
	}{
		{name: "iki yetişkin, 3 ortak katılım", ageA: &adult, ageB: &adult, shared: 3, want: true},
		{name: "iki genç, 3 ortak katılım", ageA: &minor, ageB: &minor, shared: 3, want: true},
		{name: "18 altı ile 18 üstü", ageA: &minor, ageB: &adult, shared: 5, want: false},
		{name: "18 üstü ile 18 altı", ageA: &adult, ageB: &minor, shared: 5, want: false},
		{name: "adayın yaşı bilinmiyor", ageA: &adult, shared: 3, want: false},
		{name: "alıcının yaşı bilinmiyor", ageB: &adult, shared: 3, want: false},
		{name: "identity'ye ulaşılamıyor", ageA: &adult, ageB: &adult, shared: 3, identity: errors.New("yok"), want: false},
		{name: "yalnızca 2 ortak katılım", ageA: &adult, ageB: &adult, shared: 2, want: false},
		{name: "12 aydan eski katılım sayılmaz", ageA: &adult, ageB: &adult, shared: 3, age: 370 * day, want: false},
		{name: "engelli", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, _, a, b id.ID) { must(t, e.socials(t).BlockUser(ctx, b, a)) }},
		{name: "engel kaldırılsa da önerilmez", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, _, a, b id.ID) {
				must(t, e.socials(t).BlockUser(ctx, a, b))
				must(t, e.socials(t).UnblockUser(ctx, a, b))
			}},
		{name: "başka birini engellemek haftalık sınırı tüketmez", ageA: &adult, ageB: &adult, shared: 3, want: true,
			prepare: func(t *testing.T, e *env, _, a, _ id.ID) {
				c := id.New()
				e.profile(t, c)
				must(t, e.socials(t).BlockUser(ctx, a, c))
			}},
		{name: "zaten arkadaş", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, _, a, b id.ID) {
				_, err := e.socials(t).SendFriendRequest(ctx, a, b)
				must(t, err)
			}},
		{name: "reddedilmiş istek", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, _, a, b id.ID) {
				_, err := e.socials(t).SendFriendRequest(ctx, b, a)
				must(t, err)
				must(t, e.socials(t).RemoveFriendship(ctx, a, b))
			}},
		{name: "şikâyet edilmiş", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, tf, a, b id.ID) {
				e.exec(t, `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, subject_user_id, reason)
				    VALUES ($1, $2, $3, 'user', $4, $4, 'spam')`, id.New().PG(), tf.PG(), b.PG(), a.PG())
			}},
		{name: "şikâyet etmiş", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, tf, a, b id.ID) {
				e.exec(t, `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, subject_user_id, reason)
				    VALUES ($1, $2, $3, 'user', $4, $4, 'spam')`, id.New().PG(), tf.PG(), a.PG(), b.PG())
			}},
		{name: "karşı taraf Beni önerme dedi", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, _, _, b id.ID) {
				on := true
				_, err := e.socials(t).UpdateSocialSettings(ctx, b, social.SettingsUpdate{HideFromSuggestions: &on})
				must(t, err)
			}},
		{name: "alıcının profillemesi kapalı", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, _, a, _ id.ID) {
				off := false
				_, err := e.socials(t).UpdateSocialSettings(ctx, a, social.SettingsUpdate{ProfilingEnabled: &off})
				must(t, err)
			}},
		{name: "karşı taraf daha önce kapatmış", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, _, a, b id.ID) {
				e.exec(t, `INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at, dismissed_at) VALUES ($1, $2, $3, $3)`,
					b.PG(), a.PG(), e.base.Add(-30*day))
			}},
		{name: "bu hafta öneri almış", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, _, a, _ id.ID) {
				e.exec(t, `INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at) VALUES ($1, $2, $3)`,
					a.PG(), id.New().PG(), e.base.Add(-6*day))
			}},
		{name: "geçen hafta öneri almış", ageA: &adult, ageB: &adult, shared: 3, want: true,
			prepare: func(t *testing.T, e *env, _, a, _ id.ID) {
				e.exec(t, `INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at) VALUES ($1, $2, $3)`,
					a.PG(), id.New().PG(), e.base.Add(-8*day))
			}},
		{name: "iptal edilmiş etkinlik sayılmaz", ageA: &adult, ageB: &adult, shared: 3, want: false,
			prepare: func(t *testing.T, e *env, _, _, _ id.ID) {
				e.exec(t, `UPDATE events SET status = 'cancelled', cancelled_at = $1`, e.base)
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			tf, _ := e.newTayfa(t)
			a, b := id.New(), id.New()
			e.profile(t, a)
			e.profile(t, b)
			age := c.age
			if age == 0 {
				age = 10 * day
			}
			e.attendTogether(t, tf, c.shared, age, a, b)
			if c.prepare != nil {
				c.prepare(t, e, tf, a, b)
			}
			m := &fakeMinors{minor: map[id.ID]bool{}, err: c.identity}
			if c.ageA != nil {
				m.minor[a] = *c.ageA
			}
			if c.ageB != nil {
				m.minor[b] = *c.ageB
			}
			_, err := e.suggester(t, m).SuggestFriends(ctx)
			must(t, err)
			if got := e.suggested(t, a, b); got != c.want {
				t.Fatalf("a'ya b önerildi mi: %v, beklenen %v", got, c.want)
			}
			for _, asked := range m.asked {
				if len(asked) > 10 {
					t.Fatalf("identity'ye %d kimlik soruldu", len(asked))
				}
			}
			notices := e.notices(t, notify.TypeFriendSuggestion)
			if c.want {
				if got := e.noticeFor(t, notify.TypeFriendSuggestion, b); len(got) != 1 || got[0] != a.String() {
					t.Fatalf("bildirim: %v", got)
				}
				for _, n := range notices {
					for _, k := range []string{"score", "shared_count", "reason"} {
						if _, ok := n.payload[k]; ok {
							t.Fatalf("bildirimde %s var: %v", k, n.payload)
						}
					}
				}
			} else if len(e.noticeFor(t, notify.TypeFriendSuggestion, b)) != 0 {
				t.Fatal("öneri yokken bildirim gitti")
			}
		})
	}
}

func TestFriendSuggestionWeeklyLimitAndOrder(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	a, close, far := id.New(), id.New(), id.New()
	for _, u := range []id.ID{a, close, far} {
		e.profile(t, u)
	}
	// a attended 5 events with close and 3 with far: close comes first.
	e.attendTogether(t, tf, 3, 20*day, a, close, far)
	e.attendTogether(t, tf, 2, 20*day, a, close)
	m := &fakeMinors{minor: map[id.ID]bool{a: false, close: false, far: false}}

	_, err := e.suggester(t, m).SuggestFriends(ctx)
	must(t, err)
	if !e.suggested(t, a, close) || e.suggested(t, a, far) {
		t.Fatal("ilk öneri en yüksek puanlı aday olmalı, tek öneri")
	}
	if n := e.count(t, `SELECT count(*) FROM friend_suggestions WHERE user_id = $1`, a.PG()); n != 1 {
		t.Fatalf("a'ya %d öneri", n)
	}
	// The next day: still within the week, nothing new.
	e.base = e.base.Add(day)
	_, err = e.suggester(t, m).SuggestFriends(ctx)
	must(t, err)
	if e.suggested(t, a, far) {
		t.Fatal("haftalık sınır aşıldı")
	}
	// A week later the next candidate comes; close is not suggested again.
	e.base = e.base.Add(7 * day)
	_, err = e.suggester(t, m).SuggestFriends(ctx)
	must(t, err)
	if !e.suggested(t, a, far) {
		t.Fatal("ikinci hafta öneri gelmedi")
	}
	if n := e.count(t, `SELECT count(*) FROM friend_suggestions WHERE user_id = $1`, a.PG()); n != 2 {
		t.Fatalf("a'ya %d öneri", n)
	}
}

func TestFriendScoreDecayAndProfilingOff(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	a, b, c := id.New(), id.New(), id.New()
	for _, u := range []id.ID{a, b, c} {
		e.profile(t, u)
	}
	e.attendTogether(t, tf, 1, 0, a, b)
	e.attendTogether(t, tf, 1, 180*day, a, b)
	e.attendTogether(t, tf, 1, 10*day, a, c)
	_, err := e.suggester(t, nil).SuggestFriends(ctx)
	must(t, err)

	low, high := social.Pair(a, b)
	var score float64
	var shared int
	must(t, e.pool.QueryRow(ctx, `SELECT score, shared_count FROM friend_scores WHERE user_low_id = $1 AND user_high_id = $2`,
		low.PG(), high.PG()).Scan(&score, &shared))
	if shared != 2 || math.Abs(score-1.5) > 0.01 {
		t.Fatalf("puan %v, ortak %d; 1,5 ve 2 bekleniyordu", score, shared)
	}
	// No identity: scores are kept but nothing is suggested.
	if n := e.count(t, `SELECT count(*) FROM friend_suggestions`); n != 0 {
		t.Fatalf("identity'siz %d öneri", n)
	}

	// An open suggestion to a and one of a; turning profiling off deletes
	// a's scores and both, and the next run leaves a out.
	e.exec(t, `INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at) VALUES ($1, $2, $3), ($4, $1, $3)`,
		a.PG(), b.PG(), e.base, c.PG())
	e.exec(t, `INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at, dismissed_at) VALUES ($1, $2, $3, $3)`,
		a.PG(), id.New().PG(), e.base)
	off := false
	st, err := e.socials(t).UpdateSocialSettings(ctx, a, social.SettingsUpdate{ProfilingEnabled: &off})
	must(t, err)
	if st.ProfilingEnabled || st.HideFromSuggestions {
		t.Fatalf("ayarlar: %+v", st)
	}
	if n := e.count(t, `SELECT count(*) FROM friend_scores WHERE user_low_id = $1 OR user_high_id = $1`, a.PG()); n != 0 {
		t.Fatalf("profilleme kapalıyken %d puan", n)
	}
	if n := e.count(t, `SELECT count(*) FROM friend_suggestions WHERE (user_id = $1 OR suggested_user_id = $1) AND dismissed_at IS NULL`, a.PG()); n != 0 {
		t.Fatalf("profilleme kapalıyken %d açık öneri", n)
	}
	if n := e.count(t, `SELECT count(*) FROM friend_suggestions WHERE user_id = $1 AND dismissed_at IS NOT NULL`, a.PG()); n != 1 {
		t.Fatal("kişinin kendi kapatması silinmemeli")
	}
	_, err = e.suggester(t, nil).SuggestFriends(ctx)
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM friend_scores WHERE user_low_id = $1 OR user_high_id = $1`, a.PG()); n != 0 {
		t.Fatalf("yeniden hesapta %d puan", n)
	}
	if n := e.count(t, `SELECT count(*) FROM friend_scores`); n != 0 {
		t.Fatalf("b ile c'nin ortak katılımı yok; %d puan", n)
	}
}

func TestOldOpenSuggestionsExpire(t *testing.T) {
	e := setup(t)
	a, b, c := id.New(), id.New(), id.New()
	e.exec(t, `INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at) VALUES ($1, $2, $3)`, a.PG(), b.PG(), e.base.Add(-366*day))
	e.exec(t, `INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at, dismissed_at) VALUES ($1, $2, $3, $3)`, a.PG(), c.PG(), e.base.Add(-400*day))
	_, err := e.suggester(t, nil).SuggestFriends(ctx)
	must(t, err)
	if e.suggested(t, a, b) || !e.suggested(t, a, c) {
		t.Fatal("eski açık öneri silinmeli, kapatılmış kalmalı")
	}
}
