package social

import (
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Friend suggestions and settings (issue #208, ADR-0028).

func (e *env) suggest(t *testing.T, to, who id.ID, at time.Time) {
	t.Helper()
	_, err := e.pool.Exec(ctx, `INSERT INTO friend_suggestions (user_id, suggested_user_id, created_at) VALUES ($1, $2, $3)`, to.PG(), who.PG(), at)
	must(t, err)
}

func (e *env) listed(t *testing.T, caller id.ID) []id.ID {
	t.Helper()
	items, err := e.svc.ListFriendSuggestions(ctx, caller)
	must(t, err)
	out := make([]id.ID, 0, len(items))
	for _, s := range items {
		out = append(out, s.UserID)
	}
	return out
}

func TestListFriendSuggestionsFilters(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name    string
		created time.Duration
		prepare func(t *testing.T, e *env, a, b id.ID)
		want    bool
	}{
		{name: "açık öneri", want: true},
		{name: "30 günden eski", created: 31 * 24 * time.Hour},
		{name: "kapatılmış", prepare: func(t *testing.T, e *env, a, b id.ID) { must(t, e.svc.DismissFriendSuggestion(ctx, a, b)) }},
		{name: "istek gönderilmiş", prepare: func(t *testing.T, e *env, a, b id.ID) {
			_, err := e.svc.SendFriendRequest(ctx, a, b)
			must(t, err)
		}},
		{name: "karşı taraf engellemiş", prepare: func(t *testing.T, e *env, a, b id.ID) { must(t, e.svc.BlockUser(ctx, b, a)) }},
		{name: "şikâyet", prepare: func(t *testing.T, e *env, a, b id.ID) {
			// A report from the profile: no tayfa, platform only (issue #187).
			_, err := e.pool.Exec(ctx, `INSERT INTO reports (id, reporter_id, target_type, target_id, subject_user_id, reason,
			    platform_only, escalated_to_platform_at, escalation_reason, platform_status)
			    VALUES ($1, $2, 'user', $3, $3, 'spam', true, now(), 'user_target', 'open')`, id.New().PG(), b.PG(), a.PG())
			must(t, err)
		}},
		{name: "karşı taraf Beni önerme dedi", prepare: func(t *testing.T, e *env, _, b id.ID) {
			on := true
			_, err := e.svc.UpdateSocialSettings(ctx, b, SettingsUpdate{HideFromSuggestions: &on})
			must(t, err)
		}},
		{name: "karşı tarafın hesabı silinmiş", prepare: func(t *testing.T, e *env, _, b id.ID) {
			_, err := e.pool.Exec(ctx, `UPDATE user_profiles SET deleted_at = now(), display_name = NULL WHERE user_id = $1`, b.PG())
			must(t, err)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			a, b := e.user(t, "Ayşe"), e.user(t, "Ece")
			e.suggest(t, a, b, now.Add(-c.created))
			if c.prepare != nil {
				c.prepare(t, e, a, b)
			}
			got := e.listed(t, a)
			if (len(got) == 1 && got[0] == b) != c.want || len(got) > 1 {
				t.Fatalf("liste %v, öneri bekleniyor mu: %v", got, c.want)
			}
		})
	}
}

func TestDismissFriendSuggestion(t *testing.T) {
	e := setup(t)
	a, b := e.user(t, "Ayşe"), e.user(t, "Ece")
	wantInvalid(t, "kendisi", e.svc.DismissFriendSuggestion(ctx, a, a))
	// Nothing to dismiss: success, no row written.
	must(t, e.svc.DismissFriendSuggestion(ctx, a, b))
	if n := e.count(t, `SELECT count(*) FROM friend_suggestions`); n != 0 {
		t.Fatalf("öneri yokken %d satır", n)
	}
	e.suggest(t, a, b, time.Now().UTC())
	must(t, e.svc.DismissFriendSuggestion(ctx, a, b))
	must(t, e.svc.DismissFriendSuggestion(ctx, a, b))
	if n := e.count(t, `SELECT count(*) FROM friend_suggestions WHERE dismissed_at IS NOT NULL`); n != 1 {
		t.Fatalf("kapatılmış %d", n)
	}
	// Another user's suggestion is untouched.
	c := e.user(t, "Can")
	e.suggest(t, c, b, time.Now().UTC())
	must(t, e.svc.DismissFriendSuggestion(ctx, a, c))
	if len(e.listed(t, c)) != 1 {
		t.Fatal("başkasının önerisi kapandı")
	}
}

func TestSocialSettings(t *testing.T) {
	e := setup(t)
	a := e.user(t, "Ayşe")
	st, err := e.svc.GetSocialSettings(ctx, a)
	must(t, err)
	if st != DefaultSettings || !st.ProfilingEnabled || st.HideFromSuggestions {
		t.Fatalf("varsayılan: %+v", st)
	}
	_, err = e.svc.UpdateSocialSettings(ctx, a, SettingsUpdate{})
	wantInvalid(t, "boş güncelleme", err)

	on := true
	st, err = e.svc.UpdateSocialSettings(ctx, a, SettingsUpdate{HideFromSuggestions: &on})
	must(t, err)
	if !st.ProfilingEnabled || !st.HideFromSuggestions {
		t.Fatalf("beni önerme: %+v", st)
	}
	off := false
	st, err = e.svc.UpdateSocialSettings(ctx, a, SettingsUpdate{ProfilingEnabled: &off})
	must(t, err)
	if st.ProfilingEnabled || !st.HideFromSuggestions {
		t.Fatalf("verilmeyen alan değişti: %+v", st)
	}
	got, err := e.svc.GetSocialSettings(ctx, a)
	must(t, err)
	if got != st {
		t.Fatalf("okunan %+v, yazılan %+v", got, st)
	}
}

// TestHideFromSuggestionsDeletesOpenOnes: turning "Beni önerme" on removes
// the open suggestions of the user, not the ones made to them.
func TestHideFromSuggestionsDeletesOpenOnes(t *testing.T) {
	e := setup(t)
	a, b, c := e.user(t, "Ayşe"), e.user(t, "Ece"), e.user(t, "Can")
	now := time.Now().UTC()
	e.suggest(t, b, a, now)
	e.suggest(t, a, c, now)
	on := true
	_, err := e.svc.UpdateSocialSettings(ctx, a, SettingsUpdate{HideFromSuggestions: &on})
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM friend_suggestions WHERE suggested_user_id = $1`, a.PG()); n != 0 {
		t.Fatalf("a hâlâ %d kişiye önerili", n)
	}
	if len(e.listed(t, a)) != 1 {
		t.Fatal("a'nın aldığı öneri silinmemeli")
	}
}

// TestBlockForgetsPair: a block deletes the pair's score and open
// suggestions and keeps the pair from being suggested again.
func TestBlockForgetsPair(t *testing.T) {
	e := setup(t)
	a, b := e.user(t, "Ayşe"), e.user(t, "Ece")
	now := time.Now().UTC()
	low, high := Pair(a, b)
	_, err := e.pool.Exec(ctx, `INSERT INTO friend_scores (user_low_id, user_high_id, score, shared_count, last_shared_at, computed_at)
	    VALUES ($1, $2, 2.5, 3, $3, $3)`, low.PG(), high.PG(), now)
	must(t, err)
	e.suggest(t, a, b, now.Add(-time.Hour))
	e.suggest(t, b, a, now.Add(-time.Hour))
	must(t, e.svc.BlockUser(ctx, a, b))
	must(t, e.svc.BlockUser(ctx, a, b)) // again: no change, no error
	if n := e.count(t, `SELECT count(*) FROM friend_scores`); n != 0 {
		t.Fatalf("engelde %d puan kaldı", n)
	}
	if n := e.count(t, `SELECT count(*) FROM friend_suggestions WHERE dismissed_at IS NULL`); n != 0 {
		t.Fatalf("engelde %d açık öneri kaldı", n)
	}
	if n := e.count(t, `SELECT count(*) FROM friend_suggestions WHERE user_id = $1 AND suggested_user_id = $2 AND source = 'blocked'`, a.PG(), b.PG()); n != 1 {
		t.Fatal("engelde kapatma kaydı yok")
	}
	must(t, e.svc.UnblockUser(ctx, a, b))
	if len(e.listed(t, a)) != 0 || len(e.listed(t, b)) != 0 {
		t.Fatal("engel kalkınca öneri geri geldi")
	}
}
