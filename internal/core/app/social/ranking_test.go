package social

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Friend list ordered by the friendship score (issue #215, ADR-0030 §12).

func (e *env) befriend(t *testing.T, a, b id.ID) {
	t.Helper()
	_, err := e.svc.SendFriendRequest(ctx, b, a)
	must(t, err)
	_, err = e.svc.AcceptFriendRequest(ctx, a, b)
	must(t, err)
}

func (e *env) score(t *testing.T, a, b id.ID, score float64) {
	t.Helper()
	low, high := Pair(a, b)
	now := time.Now().UTC()
	_, err := e.pool.Exec(ctx, `INSERT INTO friend_scores (user_low_id, user_high_id, score, shared_count, last_shared_at, computed_at)
	    VALUES ($1, $2, $3, 3, $4, $4)`, low.PG(), high.PG(), score, now)
	must(t, err)
}

// profilingOff writes the setting directly: UpdateSocialSettings would also
// delete the scores, and these tests check that a score row left behind
// (the daily job racing the switch) still does not count.
func (e *env) profilingOff(t *testing.T, u id.ID) {
	t.Helper()
	_, err := e.pool.Exec(ctx, `INSERT INTO social_settings (user_id, profiling_enabled) VALUES ($1, false)`, u.PG())
	must(t, err)
}

// allFriends pages through ListFriends with the given page size and checks
// every page token on the way.
func (e *env) allFriends(t *testing.T, caller id.ID, size int32) []id.ID {
	t.Helper()
	var out []id.ID
	token := ""
	for range 50 {
		p, err := e.svc.ListFriends(ctx, caller, size, token)
		must(t, err)
		for _, f := range p.Items {
			out = append(out, f.UserID)
		}
		if p.Next == "" {
			return out
		}
		checkToken(t, p.Next, p.Items[len(p.Items)-1].UserID)
		token = p.Next
	}
	t.Fatal("sayfalama bitmedi")
	return nil
}

// checkToken pins that a page token holds only the list name and the last
// friend of the page: no score, rank or position.
func checkToken(t *testing.T, token string, last id.ID) {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(token)
	must(t, err)
	var fields map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	must(t, dec.Decode(&fields))
	if len(fields) != 2 || fields["m"] != "ListFriends" || fields["u"] != last.String() {
		t.Fatalf("imleç yalnızca liste adı ve son kişiyi taşımalı: %s", b)
	}
}

func byID(ids ...id.ID) []id.ID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b id.ID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

func TestListFriendsRankedByScore(t *testing.T) {
	e := setup(t)
	a := e.user(t, "Ayşe")
	f := make([]id.ID, 7)
	for i := range f {
		f[i] = e.user(t, "Arkadaş")
		e.befriend(t, a, f[i])
	}
	// f0 highest; f1 and f2 tie; f3 lowest scored; f4..f6 unscored.
	e.score(t, a, f[0], 3)
	e.score(t, a, f[1], 1.5)
	e.score(t, a, f[2], 1.5)
	e.score(t, a, f[3], 0.25)
	// A score with a non-friend and a pending request change nothing.
	stranger, pending := e.user(t, "Yabancı"), e.user(t, "Bekleyen")
	e.score(t, a, stranger, 9)
	_, err := e.svc.SendFriendRequest(ctx, pending, a)
	must(t, err)
	e.score(t, a, pending, 8)

	want := []id.ID{f[0]}
	want = append(want, byID(f[1], f[2])...)
	want = append(want, f[3])
	want = append(want, byID(f[4], f[5], f[6])...)
	for _, size := range []int32{1, 2, 3, 50} {
		if got := e.allFriends(t, a, size); !slices.Equal(got, want) {
			t.Fatalf("sayfa boyu %d: sıra %v, beklenen %v", size, got, want)
		}
	}
	// The other side sees the same pair score: a is f0's best friend.
	if got := e.allFriends(t, f[0], 50); len(got) != 1 || got[0] != a {
		t.Fatalf("f0'ın listesi %v", got)
	}
	// Requests keep their order by user ID; the score does not count there.
	other := e.user(t, "Diğer")
	_, err = e.svc.SendFriendRequest(ctx, other, a)
	must(t, err)
	reqs, err := e.svc.ListFriendRequests(ctx, a, true, 50, "")
	must(t, err)
	var gotReqs []id.ID
	for _, r := range reqs.Items {
		gotReqs = append(gotReqs, r.UserID)
	}
	if !slices.Equal(gotReqs, byID(pending, other)) {
		t.Fatalf("istekler %v", gotReqs)
	}
}

// TestListFriendsProfilingOff: when either side has profiling off the pair's
// score does not count; when the caller has it off the list is in the
// plain (user ID) order.
func TestListFriendsProfilingOff(t *testing.T) {
	t.Run("çağıran kapalı", func(t *testing.T) {
		e := setup(t)
		a := e.user(t, "Ayşe")
		f := []id.ID{e.user(t, "A"), e.user(t, "B"), e.user(t, "C")}
		for i, u := range f {
			e.befriend(t, a, u)
			e.score(t, a, u, float64(i+1))
		}
		e.profilingOff(t, a)
		for _, size := range []int32{1, 50} {
			if got := e.allFriends(t, a, size); !slices.Equal(got, byID(f...)) {
				t.Fatalf("sayfa boyu %d: %v", size, got)
			}
		}
	})
	t.Run("karşı taraf kapalı", func(t *testing.T) {
		e := setup(t)
		a := e.user(t, "Ayşe")
		hi, off, lo, none := e.user(t, "A"), e.user(t, "B"), e.user(t, "C"), e.user(t, "D")
		for _, u := range []id.ID{hi, off, lo, none} {
			e.befriend(t, a, u)
		}
		e.score(t, a, hi, 3)
		e.score(t, a, off, 2)
		e.score(t, a, lo, 1)
		e.profilingOff(t, off)
		want := append([]id.ID{hi, lo}, byID(off, none)...)
		for _, size := range []int32{1, 2, 50} {
			if got := e.allFriends(t, a, size); !slices.Equal(got, want) {
				t.Fatalf("sayfa boyu %d: %v, beklenen %v", size, got, want)
			}
		}
	})
}

// TestListFriendsForgedCursor: a page token naming someone who is not the
// caller's friend does not use that pair's score, so a token cannot be used
// to probe a score against the caller's friends.
func TestListFriendsForgedCursor(t *testing.T) {
	e := setup(t)
	a := e.user(t, "Ayşe")
	x := e.user(t, "Yabancı")
	e.score(t, a, x, 10)
	var scored []id.ID
	for i := range 4 {
		u := e.user(t, "Arkadaş")
		e.befriend(t, a, u)
		if i < 2 {
			e.score(t, a, u, float64(i+1))
			scored = append(scored, u)
		}
	}
	p, err := e.svc.ListFriends(ctx, a, 50, encodeCursor("ListFriends", x))
	must(t, err)
	for _, f := range p.Items {
		if slices.Contains(scored, f.UserID) {
			t.Fatal("uydurma imleç yabancının puanıyla konumlandı")
		}
		if bytes.Compare(f.UserID[:], x[:]) <= 0 {
			t.Fatalf("imleçten önceki kişi döndü: %v", f.UserID)
		}
	}
	// The same holds once a real friendship ends between two pages.
	e.befriend(t, a, x)
	p, err = e.svc.ListFriends(ctx, a, 1, "")
	must(t, err)
	if p.Items[0].UserID != x {
		t.Fatalf("en yüksek puanlı arkadaş ilk değil: %v", p.Items[0].UserID)
	}
	must(t, e.svc.RemoveFriendship(ctx, a, x))
	if n := e.count(t, `SELECT count(*) FROM friend_scores WHERE $1 IN (user_low_id, user_high_id) AND $2 IN (user_low_id, user_high_id)`, a.PG(), x.PG()); n != 0 {
		t.Fatalf("arkadaşlık bitince puan kaldı: %d", n)
	}
	e.score(t, a, x, 10) // a row left behind must still not count
	rest, err := e.svc.ListFriends(ctx, a, 50, p.Next)
	must(t, err)
	for _, f := range rest.Items {
		if slices.Contains(scored, f.UserID) {
			t.Fatal("biten arkadaşlığın puanı imleçte kullanıldı")
		}
	}
	checkToken(t, p.Next, x)
}
