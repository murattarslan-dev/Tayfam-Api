package account

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/profilefields"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/refcode"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type env struct {
	svc  *Service
	pool *pgxpool.Pool
	now  time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, "identity", identitydb.Migrations())
	e := &env{pool: pool, now: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	svc, err := New(Config{Pool: pool, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	return e
}

// registered creates a registered user with a referral code.
func (e *env) registered(t *testing.T, name string) (id.ID, string) {
	t.Helper()
	u, code := id.New(), refcode.New()
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO users (id, display_name, birth_date_enc, registered_at, ref_code) VALUES ($1, $2, $3, now(), $4)`,
		u.PG(), name, bytes.Repeat([]byte{1}, 40), code); err != nil {
		t.Fatal(err)
	}
	return u, code
}

func TestFindByReferralCode(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	caller, _ := e.registered(t, "Arayan")
	owner, code := e.registered(t, "Kod Sahibi")

	f, err := e.svc.FindByReferralCode(ctx, caller, code)
	if err != nil || f.UserID != owner || f.DisplayName != "Kod Sahibi" || f.AvatarUploadID != (id.ID{}) {
		t.Fatalf("bulunan = %+v, %v", f, err)
	}
	// A deleted account is not found by its code.
	if _, err := e.pool.Exec(ctx, `UPDATE users SET deleted_at = now() WHERE id = $1`, owner.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.FindByReferralCode(ctx, caller, code); !errors.Is(err, ErrCodeNotFound) {
		t.Fatalf("silinmiş hesap: %v", err)
	}
	// An unregistered account cannot look anything up.
	pending := id.New()
	if _, err := e.pool.Exec(ctx, `INSERT INTO users (id, display_name) VALUES ($1, 'Kayıtsız')`, pending.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.FindByReferralCode(ctx, pending, code); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("kayıtsız: %v", err)
	}
}

// TestLookupLimit: 10 lookups per user per hour, found or not, malformed
// included; the hour slides; another user has their own limit.
func TestLookupLimit(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	caller, _ := e.registered(t, "Tarayıcı")
	other, _ := e.registered(t, "Başkası")
	_, code := e.registered(t, "Hedef")

	attempts := []struct {
		code string
		want error
	}{
		{code, nil}, {"TYF-ZZZZZZ", ErrCodeNotFound}, {"bozuk", ErrCodeNotFound}, {"", ErrCodeNotFound},
	}
	for i := range LookupsPerHour {
		a := attempts[i%len(attempts)]
		if _, err := e.svc.FindByReferralCode(ctx, caller, a.code); !errors.Is(err, a.want) && (a.want != nil || err != nil) {
			t.Fatalf("deneme %d: %v", i, err)
		}
		e.now = e.now.Add(time.Minute)
	}
	// The 11th, even with a valid code, is refused and not recorded.
	if _, err := e.svc.FindByReferralCode(ctx, caller, code); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("11. deneme: %v", err)
	}
	if _, err := e.svc.FindByReferralCode(ctx, other, code); err != nil {
		t.Fatalf("başka kullanıcının sınırı ayrı: %v", err)
	}
	// One hour after the first attempt, one slot opens again.
	e.now = e.now.Add(lookupWindow - 10*time.Minute + time.Second)
	if _, err := e.svc.FindByReferralCode(ctx, caller, code); err != nil {
		t.Fatalf("pencere kaydı: %v", err)
	}
	if _, err := e.svc.FindByReferralCode(ctx, caller, code); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("pencere içinde ikinci: %v", err)
	}
	// Old attempts are cleaned up as the caller searches again.
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM ref_code_lookups WHERE user_id = $1`, caller.PG()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != LookupsPerHour {
		t.Fatalf("saklanan deneme = %d", n)
	}
}

func TestLookupLimitUnderConcurrency(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	caller, _ := e.registered(t, "Tarayıcı")
	var wg sync.WaitGroup
	var mu sync.Mutex
	limited := 0
	for range 3 * LookupsPerHour {
		wg.Go(func() {
			_, err := e.svc.FindByReferralCode(ctx, caller, "TYF-ZZZZZZ")
			if errors.Is(err, ErrRateLimited) {
				mu.Lock()
				limited++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if limited != 2*LookupsPerHour {
		t.Fatalf("reddedilen = %d, %d bekleniyordu", limited, 2*LookupsPerHour)
	}
}

func TestRotateReferralCode(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	caller, _ := e.registered(t, "Arayan")
	owner, old := e.registered(t, "Kod Sahibi")

	code, err := e.svc.RotateReferralCode(ctx, owner)
	if err != nil || code == old {
		t.Fatalf("yeni kod = %q, %v", code, err)
	}
	if _, err := e.svc.FindByReferralCode(ctx, caller, old); !errors.Is(err, ErrCodeNotFound) {
		t.Fatalf("eski kod hâlâ çalışıyor: %v", err)
	}
	if f, err := e.svc.FindByReferralCode(ctx, caller, code); err != nil || f.UserID != owner {
		t.Fatalf("yeni kod: %+v %v", f, err)
	}
	a, err := e.svc.Get(ctx, owner)
	if err != nil || a.ReferralCode != code || a.ActivityVisibility != VisibilityTayfa || a.UserID != owner || a.DisplayName != "Kod Sahibi" {
		t.Fatalf("hesap = %+v %v", a, err)
	}
	if _, err := e.svc.RotateReferralCode(ctx, id.New()); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("bilinmeyen: %v", err)
	}
}

func TestSetActivityVisibility(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u, _ := e.registered(t, "Deniz")

	events := func() []string {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT payload::text FROM domain_events WHERE type = 'user.profile_updated' AND subject_id = $1 ORDER BY id`, u.PG())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}

	if _, err := e.svc.SetActivityVisibility(ctx, u, "herkes"); !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("geçersiz: %v", err)
	}
	// The default again: nothing is written.
	if v, err := e.svc.SetActivityVisibility(ctx, u, VisibilityTayfa); err != nil || v != VisibilityTayfa || len(events()) != 0 {
		t.Fatalf("aynı değer: %v %v %v", v, err, events())
	}
	if _, err := e.svc.SetActivityVisibility(ctx, u, VisibilityPrivate); err != nil {
		t.Fatal(err)
	}
	a, _ := e.svc.Get(ctx, u)
	if a.ActivityVisibility != VisibilityPrivate {
		t.Fatalf("görünürlük = %s", a.ActivityVisibility)
	}
	// core gets the whole public profile with the new setting.
	if got := events(); len(got) != 1 || got[0] != `{"display_name": "Deniz", "activity_visibility": "private"}` {
		t.Fatalf("olaylar = %q", got)
	}
	if _, err := e.svc.SetActivityVisibility(ctx, id.New(), VisibilityPublic); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("bilinmeyen: %v", err)
	}
}

type fakeMedia struct {
	err error
	got []id.ID
}

func (f *fakeMedia) AttachAvatar(_ context.Context, upload id.ID) error {
	f.got = append(f.got, upload)
	return f.err
}

// The avatar is stored only after core attached the upload, and reaches
// core and chat with user.profile_updated (issue #135).
func TestAvatar(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u, _ := e.registered(t, "Deniz")
	media := &fakeMedia{}
	svc, err := New(Config{Pool: e.pool, Media: media, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	events := func() []string {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT payload::text FROM domain_events WHERE type = 'user.profile_updated' AND subject_id = $1 ORDER BY id`, u.PG())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}

	// Without core: refused, nothing stored.
	if err := e.svc.SetAvatar(ctx, u, id.New()); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("core yok: %v", err)
	}
	// core refuses (another user's upload, not ready, ...): nothing stored.
	media.err = ErrUploadNotFound
	if err := svc.SetAvatar(ctx, u, id.New()); !errors.Is(err, ErrUploadNotFound) || len(events()) != 0 {
		t.Fatalf("core reddi: %v %v", err, events())
	}
	media.err = nil
	avatar := id.New()
	if err := svc.SetAvatar(ctx, u, avatar); err != nil {
		t.Fatal(err)
	}
	if a, _ := svc.Get(ctx, u); a.AvatarUploadID != avatar || media.got[len(media.got)-1] != avatar {
		t.Fatalf("avatar = %v", a.AvatarUploadID)
	}
	if got := events(); len(got) != 1 || got[0] != `{"display_name": "Deniz", "avatar_upload_id": "`+avatar.String()+`", "activity_visibility": "tayfa"}` {
		t.Fatalf("olaylar = %q", got)
	}
	// The same avatar again writes nothing.
	if err := svc.SetAvatar(ctx, u, avatar); err != nil || len(events()) != 1 {
		t.Fatalf("aynı avatar: %v %d", err, len(events()))
	}
	if err := svc.ClearAvatar(ctx, u); err != nil {
		t.Fatal(err)
	}
	if a, _ := svc.Get(ctx, u); a.AvatarUploadID != (id.ID{}) {
		t.Fatal("avatar kaldırılmalı")
	}
	if got := events(); len(got) != 2 || got[1] != `{"display_name": "Deniz", "activity_visibility": "tayfa"}` {
		t.Fatalf("olaylar = %q", got)
	}
	if err := svc.SetAvatar(ctx, id.New(), avatar); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("bilinmeyen: %v", err)
	}
}

func (e *env) profileEvents(t *testing.T, u id.ID) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT payload::text FROM domain_events WHERE type = 'user.profile_updated' AND subject_id = $1 ORDER BY id`, u.PG())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// TestUpdateDisplayName (issue #184): the rule of registration, the new
// name reaches core and chat, the old one is kept nowhere.
func TestUpdateDisplayName(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u, _ := e.registered(t, "Deniz")

	for name, in := range map[string]string{
		"boş":               "",
		"yalnız boşluk":     "   ",
		"41 karakter":       string(bytes.Repeat([]byte("a"), 41)),
		"kontrol karakteri": "Ece\u0007",
	} {
		if err := e.svc.Update(ctx, u, Changes{DisplayName: &in}); !errors.Is(err, ErrInvalidDisplayName) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An invalid field changes nothing, a valid one in the same call
	// included.
	bad, vis := "", VisibilityPrivate
	if err := e.svc.Update(ctx, u, Changes{DisplayName: &bad, ActivityVisibility: &vis}); !errors.Is(err, ErrInvalidDisplayName) {
		t.Fatal(err)
	}
	if a, _ := e.svc.Get(ctx, u); a.ActivityVisibility != VisibilityTayfa || len(e.profileEvents(t, u)) != 0 {
		t.Fatalf("geçersiz güncelleme bir şey değiştirdi: %+v", a)
	}

	// The current name again (after cleaning) writes nothing.
	same := "  Deniz "
	if err := e.svc.Update(ctx, u, Changes{DisplayName: &same}); err != nil || len(e.profileEvents(t, u)) != 0 {
		t.Fatalf("aynı ad: %v", err)
	}

	name := "  Deniz   Su "
	if err := e.svc.Update(ctx, u, Changes{DisplayName: &name, ActivityVisibility: &vis}); err != nil {
		t.Fatal(err)
	}
	a, _ := e.svc.Get(ctx, u)
	if a.DisplayName != "Deniz Su" || a.ActivityVisibility != VisibilityPrivate {
		t.Fatalf("hesap = %+v", a)
	}
	// One event carries both changes; the old name is nowhere.
	if got := e.profileEvents(t, u); len(got) != 1 || got[0] != `{"display_name": "Deniz Su", "activity_visibility": "private"}` {
		t.Fatalf("olaylar = %q", got)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM display_name_changes WHERE user_id = $1`, u.PG()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("değişiklik kaydı = %d %v", n, err)
	}

	if err := e.svc.Update(ctx, id.New(), Changes{DisplayName: &name}); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("bilinmeyen: %v", err)
	}
}

// TestDisplayNameLimit: 3 changes in 24 hours; the window slides; setting
// the current name does not count; another user has their own limit.
func TestDisplayNameLimit(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u, _ := e.registered(t, "Ad0")
	other, _ := e.registered(t, "Başka")
	set := func(user id.ID, s string) error { return e.svc.Update(ctx, user, Changes{DisplayName: &s}) }

	start := e.now
	for i, n := range []string{"Ad1", "Ad2", "Ad3"} {
		e.now = start.Add(time.Duration(i) * time.Hour)
		if err := set(u, n); err != nil {
			t.Fatalf("%d. değişiklik: %v", i+1, err)
		}
	}
	if err := set(u, "Ad3"); err != nil {
		t.Fatalf("aynı ad sınıra takılmamalı: %v", err)
	}
	if err := set(u, "Ad4"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("4. değişiklik: %v", err)
	}
	if a, _ := e.svc.Get(ctx, u); a.DisplayName != "Ad3" {
		t.Fatalf("reddedilen ad yazıldı: %s", a.DisplayName)
	}
	if err := set(other, "Başka2"); err != nil {
		t.Fatalf("başka kullanıcı: %v", err)
	}
	// 24 hours after the first change, one more is allowed.
	e.now = start.Add(24*time.Hour + time.Second)
	if err := set(u, "Ad4"); err != nil {
		t.Fatalf("pencere kaydı: %v", err)
	}
	if err := set(u, "Ad5"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("pencere içinde 4.: %v", err)
	}
}

func TestDisplayNameLimitUnderConcurrency(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u, _ := e.registered(t, "Ad")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := "Ad " + string(rune('A'+i))
			if err := e.svc.Update(ctx, u, Changes{DisplayName: &s}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != DisplayNameChangesPerDay {
		t.Fatalf("başarılı değişiklik = %d", ok)
	}
}

// TestUpdateBirthdayVisibility (issue #185): the default is day and month;
// the setting changes no profile copy (the date never leaves identity
// with user.profile_updated).
func TestUpdateBirthdayVisibility(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u, _ := e.registered(t, "Deniz")
	if a, err := e.svc.Get(ctx, u); err != nil || a.BirthdayVisibility != BirthdayDayMonth {
		t.Fatalf("varsayılan = %+v %v", a, err)
	}
	bad := BirthdayVisibility("public")
	if err := e.svc.Update(ctx, u, Changes{BirthdayVisibility: &bad}); !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("geçersiz: %v", err)
	}
	for _, v := range []BirthdayVisibility{BirthdayFull, BirthdayHidden, BirthdayDayMonth} {
		if err := e.svc.Update(ctx, u, Changes{BirthdayVisibility: &v}); err != nil {
			t.Fatal(err)
		}
		if a, _ := e.svc.Get(ctx, u); a.BirthdayVisibility != v {
			t.Fatalf("görünürlük = %s, istenen %s", a.BirthdayVisibility, v)
		}
	}
	if got := e.profileEvents(t, u); len(got) != 0 {
		t.Fatalf("profil olayı yazıldı: %q", got)
	}
}

// TestProfileCompletion (issue #186): fields are saved and cleaned, an
// invalid one changes nothing, declined answers a field, the form is due
// until every field is answered, a snooze holds it back 7 days, and a
// newer field list voids the snooze.
func TestProfileCompletion(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u, _ := e.registered(t, "Deniz")
	pending := func() bool {
		t.Helper()
		a, err := e.svc.Get(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		return a.ProfileCompletionPending
	}
	a, _ := e.svc.Get(ctx, u)
	if !a.ProfileCompletionPending || a.AboutVisibility != profilefields.VisibleFriendsAndMutual {
		t.Fatalf("yeni hesap: %+v", a)
	}

	team, url := "  Bursa   spor ", []profilefields.SocialAccount{{Platform: "instagram", Username: "https://evil.example/x"}}
	if err := e.svc.Update(ctx, u, Changes{Profile: profilefields.Update{Team: &team, SocialAccounts: &url}}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("URL: %v", err)
	}
	if a, _ := e.svc.Get(ctx, u); a.Profile.Team != "" {
		t.Fatal("geçersiz istek takımı yazdı")
	}

	social := []profilefields.SocialAccount{{Platform: "x", Username: "@deniz"}}
	interests := []string{"camp"}
	vis := profilefields.VisibleFriends
	if err := e.svc.Update(ctx, u, Changes{
		Profile:         profilefields.Update{Team: &team, SocialAccounts: &social, Interests: &interests},
		AboutVisibility: &vis,
	}); err != nil {
		t.Fatal(err)
	}
	a, _ = e.svc.Get(ctx, u)
	if a.Profile.Team != "Bursa spor" || a.Profile.SocialAccounts[0].Username != "deniz" || a.AboutVisibility != profilefields.VisibleFriends || !a.ProfileCompletionPending {
		t.Fatalf("kayıt: %+v", a)
	}
	// Profile fields reach no other service.
	if got := e.profileEvents(t, u); len(got) != 0 {
		t.Fatalf("profil olayı yazıldı: %q", got)
	}

	// Snooze: 7 days.
	if err := e.svc.SnoozeProfilePrompt(ctx, u); err != nil {
		t.Fatal(err)
	}
	if pending() {
		t.Fatal("ertelemeden sonra form istendi")
	}
	start := e.now
	e.now = start.Add(profilefields.SnoozeFor - time.Second)
	if pending() {
		t.Fatal("7 gün dolmadan form istendi")
	}
	e.now = start.Add(profilefields.SnoozeFor)
	if !pending() {
		t.Fatal("7 gün sonra form istenmedi")
	}
	// A snooze under an older field list does not hold.
	e.now = start
	if err := e.svc.SnoozeProfilePrompt(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE profile_details SET prompt_fields_version = $2 WHERE user_id = $1`, u.PG(), profilefields.FieldsVersion-1); err != nil {
		t.Fatal(err)
	}
	if !pending() {
		t.Fatal("eski sürümdeki erteleme formu tuttu")
	}

	// Declining the rest answers every field.
	declined := []string{"about", "occupation", "languages"}
	if err := e.svc.Update(ctx, u, Changes{Profile: profilefields.Update{Declined: &declined}}); err != nil {
		t.Fatal(err)
	}
	if pending() {
		t.Fatal("bütün alanlar cevaplandı ama form istendi")
	}
	bad := profilefields.AboutVisibility("public")
	if err := e.svc.Update(ctx, u, Changes{AboutVisibility: &bad}); !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("geçersiz görünürlük: %v", err)
	}
}
