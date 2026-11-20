package instant

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Şimdi bildirimi to friends (issue #265; ADR-0038 §4).

func nowIn(title string, recipients ...id.ID) Input {
	in := input(title)
	in.Event.StartsAt, in.Event.EndsAt, in.Event.StartsNow = time.Time{}, time.Time{}, true
	in.NowRecipients = recipients
	return in
}

// score gives the friends a and b a friendship score.
func (e *env) score(t *testing.T, a, b id.ID, score float64) {
	t.Helper()
	low, high := social.Pair(a, b)
	e.exec(t, `INSERT INTO friend_scores (user_low_id, user_high_id, score, shared_count, last_shared_at, computed_at)
	    VALUES ($1, $2, $3, 3, now(), now())`, low.PG(), high.PG(), score)
}

// crowd gives u 20 friends whose scores are above any score of org.
func (e *env) crowd(t *testing.T, u id.ID) {
	t.Helper()
	for range 20 {
		f := e.friend(t, u)
		e.score(t, u, f, 5)
	}
}

// nowNotices returns the recipients of every now_started request, oldest
// first.
func (e *env) nowNotices(t *testing.T) [][]id.ID {
	t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT payload FROM domain_events WHERE type = $1 AND payload->>'type' = $2 ORDER BY id`,
		notify.EventType, notify.TypeNowStarted)
	must(t, err)
	defer rows.Close()
	var out [][]id.ID
	for rows.Next() {
		var raw []byte
		must(t, rows.Scan(&raw))
		var p struct {
			Recipients []string `json:"recipient_ids"`
			TargetType string   `json:"target_type"`
		}
		must(t, json.Unmarshal(raw, &p))
		if p.TargetType != notify.TargetEvent {
			t.Fatalf("hedef = %s", p.TargetType)
		}
		var to []id.ID
		for _, s := range p.Recipients {
			u, _ := id.Parse(s)
			to = append(to, u)
		}
		out = append(out, to)
	}
	return out
}

func TestNowBroadcast(t *testing.T) {
	e := setup(t)
	org := id.New()
	plain := e.friend(t, org)
	off := e.friend(t, org)
	e.exec(t, `INSERT INTO social_settings (user_id, now_from_friends) VALUES ($1, 'off')`, off.PG())
	// close: org must be in the recipient's first 20.
	closeIn := e.friend(t, org)
	e.exec(t, `INSERT INTO social_settings (user_id, now_from_friends) VALUES ($1, 'close')`, closeIn.PG())
	e.crowd(t, closeIn)
	e.score(t, org, closeIn, 9)
	closeOut := e.friend(t, org)
	e.exec(t, `INSERT INTO social_settings (user_id, now_from_friends) VALUES ($1, 'close')`, closeOut.PG())
	e.crowd(t, closeOut)
	// Without profiling there is no order: close acts like all.
	closeNoScore := e.friend(t, org)
	e.exec(t, `INSERT INTO social_settings (user_id, profiling_enabled, now_from_friends) VALUES ($1, false, 'close')`, closeNoScore.PG())
	e.crowd(t, closeNoScore)
	stranger := id.New()
	blocker, blocked := e.friend(t, org), e.friend(t, org)
	_, err := e.admin.Exec(ctx, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2), ($2, $3)`, blocker.PG(), org.PG(), blocked.PG())
	must(t, err)
	gone := e.friend(t, org)
	_, err = e.admin.Exec(ctx, `UPDATE user_profiles SET display_name = NULL, deleted_at = now() WHERE user_id = $1`, gone.PG())
	must(t, err)

	invitee := e.friend(t, org)
	in := nowIn("Batak", plain, off, closeIn, closeOut, closeNoScore, stranger, blocker, blocked, gone)
	in.Invitees = []id.ID{invitee}
	r, err := e.svc.Create(ctx, org, in)
	must(t, err)
	tf := r.Tayfa.ID

	got := e.nowNotices(t)
	want := []id.ID{plain, closeIn, closeNoScore}
	if len(got) != 1 || !sameSet(got[0], want) {
		t.Fatalf("şimdi bildirimi = %v, beklenen %v", got, want)
	}
	for _, u := range want {
		if m := e.membership(t, tf, u); m != "invited/member" {
			t.Errorf("davet = %s", m)
		}
	}
	for _, u := range []id.ID{off, closeOut, stranger, blocker, blocked, gone} {
		if n := e.count(t, `SELECT count(*) FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), u.PG()); n != 0 {
			t.Errorf("düşmesi gereken kişi davet edildi")
		}
	}
	// The direct invitee gets the ordinary invitation; only it counts
	// toward the daily direct invitation limit.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'tayfa.invited' AND actor_id = $1`, org.PG()); n != 1 {
		t.Errorf("tayfa.invited = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = $1 AND tayfa_id = $2`, "member.now_invited", tf.PG()); n != 3 {
		t.Errorf("denetim = %d", n)
	}
	if n := e.count(t, `SELECT sent FROM now_notification_counters WHERE scope = 'user' AND subject_id = $1`, org.PG()); n != 1 {
		t.Errorf("yayın sayacı = %d", n)
	}
	// Accepting joins at once: the inviter is the tayfa's admin.
	_, err = e.members.AcceptInvitation(ctx, plain, tf)
	must(t, err)
	if m := e.membership(t, tf, plain); m != "active/member" {
		t.Fatalf("kabul = %s", m)
	}

	// The second broadcast of the day, even with nobody left in it.
	_, err = e.events.Finish(ctx, org, tf, r.Event.ID)
	must(t, err)
	r2, err := e.svc.Create(ctx, org, nowIn("Okey", off))
	must(t, err)
	if got := e.nowNotices(t); len(got) != 1 {
		t.Fatalf("boş yayın bildirim üretti: %d", len(got))
	}
	_, err = e.events.Finish(ctx, org, r2.Tayfa.ID, r2.Event.ID)
	must(t, err)
	// The third is refused and leaves nothing behind.
	before := e.count(t, `SELECT count(*) FROM tayfas WHERE created_by = $1`, org.PG())
	_, err = e.svc.Create(ctx, org, nowIn("Tavla", plain))
	wantErr(t, "üçüncü yayın", err, apperr.ErrNowBroadcastLimit)
	if n := e.count(t, `SELECT count(*) FROM tayfas WHERE created_by = $1`, org.PG()); n != before {
		t.Fatalf("reddedilen yayın tayfa bıraktı")
	}
	// Without recipients it starts.
	_, err = e.svc.Create(ctx, org, nowIn("Tavla"))
	must(t, err)
	if n := e.count(t, `SELECT sent FROM now_notification_counters WHERE scope = 'user' AND subject_id = $1`, org.PG()); n != notify.NowBroadcastsPerDay {
		t.Errorf("yayın sayacı = %d", n)
	}
	// The database refuses a third too.
	if _, err := e.pool.Exec(ctx, `UPDATE now_notification_counters SET sent = 3 WHERE subject_id = $1`, org.PG()); err == nil {
		t.Fatal("yayın sayacı sınırı aşıldı")
	}
}

func TestNowBroadcastValidation(t *testing.T) {
	e := setup(t)
	org := id.New()
	f := e.friend(t, org)
	many := make([]id.ID, notify.MaxNowRecipients+1)
	for i := range many {
		many[i] = id.New()
	}
	planned := input("Yarın")
	planned.NowRecipients = []id.ID{f}
	both := nowIn("Batak", f)
	both.Invitees = []id.ID{f}
	for name, in := range map[string]Input{
		"planlı etkinlik": planned,
		"çok alıcı":       nowIn("Batak", many...),
		"kendisi":         nowIn("Batak", org),
		"tekrar":          nowIn("Batak", f, f),
		"sıfır":           nowIn("Batak", id.ID{}),
		"davetliyle aynı": both,
	} {
		_, err := e.svc.Create(ctx, org, in)
		wantInvalid(t, name, err)
	}
	if n := e.count(t, `SELECT count(*) FROM now_notification_counters`); n != 0 {
		t.Fatalf("reddedilen istek sayıldı: %d", n)
	}
}

func sameSet(got, want []id.ID) bool {
	if len(got) != len(want) {
		return false
	}
	in := map[id.ID]bool{}
	for _, u := range got {
		in[u] = true
	}
	for _, u := range want {
		if !in[u] {
			return false
		}
	}
	return true
}
