package availability

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

type env struct {
	svc    *Service
	social *social.Service
	pool   *pgxpool.Pool
	admin  *pgxpool.Pool
	q      *store.Queries
	today  time.Time
}

// setup uses the real clock: the database trigger checks the day window
// with Postgres' own now().
func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	svc, err := New(Config{Pool: d.App})
	must(t, err)
	soc, err := social.New(social.Config{Pool: d.App})
	must(t, err)
	return &env{svc: svc, social: soc, pool: d.App, admin: d.Admin, q: store.New(d.App), today: Today(time.Now())}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) user(t *testing.T, name string) id.ID {
	t.Helper()
	u := id.New()
	_, err := e.pool.Exec(ctx, `INSERT INTO user_profiles (user_id, display_name, source_event_id) VALUES ($1, $2, $3)`, u.PG(), name, id.New().PG())
	must(t, err)
	return u
}

func (e *env) friends(t *testing.T, a, b id.ID) {
	t.Helper()
	_, err := e.social.SendFriendRequest(ctx, a, b)
	must(t, err)
	_, err = e.social.AcceptFriendRequest(ctx, b, a)
	must(t, err)
}

func (e *env) day(n int) string { return e.today.AddDate(0, 0, n).Format(DateLayout) }

func wantInvalid(t *testing.T, what string, err error, field string) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != field {
		t.Fatalf("%s: hata = %v, beklenen %s doğrulama hatası", what, err, field)
	}
}

func TestToday(t *testing.T) {
	for _, tc := range []struct {
		utc  string
		want string
	}{
		// Istanbul is UTC+3: 20:59 UTC is still the same day, 21:00 UTC is
		// the next one.
		{"2026-11-15T20:59:59Z", "2026-11-15"},
		{"2026-11-15T21:00:00Z", "2026-11-16"},
		{"2026-12-31T22:30:00Z", "2027-01-01"},
	} {
		now, err := time.Parse(time.RFC3339, tc.utc)
		must(t, err)
		if got := Today(now).Format(DateLayout); got != tc.want {
			t.Errorf("Today(%s) = %s, beklenen %s", tc.utc, got, tc.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	for _, tc := range []struct {
		in   []Slot
		want []Slot
	}{
		{[]Slot{SlotEvening, SlotMorning}, []Slot{SlotMorning, SlotEvening}},
		{[]Slot{SlotAfternoon, SlotAfternoon}, []Slot{SlotAfternoon}},
		{[]Slot{SlotEvening, SlotAfternoon, SlotMorning}, []Slot{SlotMorning, SlotAfternoon, SlotEvening}},
		{[]Slot{SlotAllDay, SlotAllDay}, []Slot{SlotAllDay}},
	} {
		got, err := NormalizeSlots(tc.in)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("NormalizeSlots(%v) = %v %v", tc.in, got, err)
		}
	}
	for _, in := range [][]Slot{nil, {}, {"night"}, {SlotAllDay, SlotMorning}, {""}} {
		_, err := NormalizeSlots(in)
		wantInvalid(t, "dilim", err, "slots")
	}

	for in, want := range map[string]string{
		"  Kahve içelim mi?  ":              "Kahve içelim mi?",
		"   ":                               "",
		"":                                  "",
		strings.Repeat("ş", 60):             strings.Repeat("ş", 60),
		" " + strings.Repeat("İ", 60) + " ": strings.Repeat("İ", 60),
	} {
		got, err := NormalizeNote(in)
		if err != nil || got != want {
			t.Errorf("NormalizeNote(%q) = %q %v", in, got, err)
		}
	}
	for _, in := range []string{strings.Repeat("a", 61), "satır\nsonu", "sekme\tvar", "\x00", string([]byte{0xff})} {
		_, err := NormalizeNote(in)
		wantInvalid(t, "not", err, "note")
	}
}

// 30 days ahead, slot and note limits; writing replaces the day; deleting
// is for good.
func TestSetListDelete(t *testing.T) {
	e := setup(t)
	a := e.user(t, "Ayşe")

	d, err := e.svc.Set(ctx, a, e.day(0), []Slot{SlotEvening, SlotMorning}, "  Kahve?  ")
	if err != nil || d.Date.Format(DateLayout) != e.day(0) || !slices.Equal(d.Slots, []Slot{SlotMorning, SlotEvening}) ||
		d.Note != "Kahve?" || d.UpdatedAt.IsZero() {
		t.Fatalf("bugün: %+v %v", d, err)
	}
	_, err = e.svc.Set(ctx, a, e.day(MaxDaysAhead), []Slot{SlotAllDay}, "")
	must(t, err)

	_, err = e.svc.Set(ctx, a, e.day(MaxDaysAhead+1), []Slot{SlotAllDay}, "")
	wantInvalid(t, "31. gün", err, "date")
	_, err = e.svc.Set(ctx, a, e.day(-1), []Slot{SlotAllDay}, "")
	wantInvalid(t, "dün", err, "date")
	for _, bad := range []string{"", "2026-1-5", "2026-02-30", "15.11.2026", e.day(0) + "T00:00:00Z"} {
		_, err = e.svc.Set(ctx, a, bad, []Slot{SlotAllDay}, "")
		wantInvalid(t, "tarih "+bad, err, "date")
	}
	_, err = e.svc.Set(ctx, a, e.day(1), []Slot{SlotAllDay, SlotEvening}, "")
	wantInvalid(t, "tüm gün + akşam", err, "slots")
	_, err = e.svc.Set(ctx, a, e.day(1), []Slot{SlotMorning}, strings.Repeat("n", MaxNoteLength+1))
	wantInvalid(t, "uzun not", err, "note")

	// Replacing the day: the note goes when not sent again.
	d, err = e.svc.Set(ctx, a, e.day(0), []Slot{SlotAfternoon}, "")
	if err != nil || !slices.Equal(d.Slots, []Slot{SlotAfternoon}) || d.Note != "" {
		t.Fatalf("değiştirme: %+v %v", d, err)
	}

	days, err := e.svc.ListMine(ctx, a)
	if err != nil || len(days) != 2 || days[0].Date.Format(DateLayout) != e.day(0) || days[1].Date.Format(DateLayout) != e.day(MaxDaysAhead) {
		t.Fatalf("liste: %+v %v", days, err)
	}

	must(t, e.svc.Delete(ctx, a, e.day(0)))
	must(t, e.svc.Delete(ctx, a, e.day(0))) // nothing left: still fine
	wantInvalid(t, "silme tarihi", e.svc.Delete(ctx, a, "yarın"), "date")
	days, _ = e.svc.ListMine(ctx, a)
	if len(days) != 1 {
		t.Fatalf("silme sonrası: %+v", days)
	}
	var n int
	must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM user_availability WHERE user_id = $1`, a.PG()).Scan(&n))
	if n != 1 {
		t.Fatalf("kalıcı silinmedi: %d satır", n)
	}
}

// The acceptance criterion of issue #207: only accepted friends with no
// block either way see the days; everyone else gets the unknown user
// answer, whatever the reason.
func TestVisibility(t *testing.T) {
	e := setup(t)
	owner := e.user(t, "Sahip")
	_, err := e.svc.Set(ctx, owner, e.day(2), []Slot{SlotEvening}, "Maç?")
	must(t, err)

	friend := e.user(t, "Arkadaş")
	e.friends(t, friend, owner)
	stranger := e.user(t, "Yabancı")
	pending := e.user(t, "Bekleyen")
	_, err = e.social.SendFriendRequest(ctx, pending, owner)
	must(t, err)
	incoming := e.user(t, "İstek Alan")
	_, err = e.social.SendFriendRequest(ctx, owner, incoming)
	must(t, err)
	blockedByOwner := e.user(t, "Engellenen")
	e.friends(t, blockedByOwner, owner)
	must(t, e.social.BlockUser(ctx, owner, blockedByOwner))
	blocker := e.user(t, "Engelleyen")
	e.friends(t, blocker, owner)
	must(t, e.social.BlockUser(ctx, blocker, owner))
	unfriended := e.user(t, "Ayrılan")
	e.friends(t, unfriended, owner)
	must(t, e.social.RemoveFriendship(ctx, owner, unfriended))
	// A block lifted later does not bring the friendship back.
	unblocked := e.user(t, "Engeli Kalkan")
	e.friends(t, unblocked, owner)
	must(t, e.social.BlockUser(ctx, owner, unblocked))
	must(t, e.social.UnblockUser(ctx, owner, unblocked))

	for _, tc := range []struct {
		name   string
		viewer id.ID
		sees   bool
	}{
		{"kendisi", owner, true},
		{"arkadaş", friend, true},
		{"yabancı", stranger, false},
		{"isteği bekleyen", pending, false},
		{"istek gönderilen", incoming, false},
		{"sahibin engellediği", blockedByOwner, false},
		{"sahibi engelleyen", blocker, false},
		{"arkadaşlıktan çıkan", unfriended, false},
		{"engeli kalkan", unblocked, false},
	} {
		days, err := e.svc.ListUser(ctx, tc.viewer, owner)
		switch {
		case tc.sees && (err != nil || len(days) != 1 || days[0].Note != "Maç?"):
			t.Errorf("%s görmeli: %+v %v", tc.name, days, err)
		case !tc.sees && (!errors.Is(err, apperr.ErrUserNotFound) || days != nil):
			t.Errorf("%s görmemeli: %+v %v", tc.name, days, err)
		}
	}

	// An unknown user looks the same.
	_, err = e.svc.ListUser(ctx, friend, id.New())
	if !errors.Is(err, apperr.ErrUserNotFound) {
		t.Fatalf("bilinmeyen: %v", err)
	}
	_, err = e.svc.ListUser(ctx, friend, id.ID{})
	wantInvalid(t, "boş kimlik", err, "user_id")

	// A banned or deleted owner is hidden from friends; the rows stay until
	// the account goes (ban) or are deleted with it (user.deleted).
	_, err = e.admin.Exec(ctx, `INSERT INTO user_platform_bans (user_id, banned_at, event_id) VALUES ($1, now(), $2)`, owner.PG(), id.New().PG())
	must(t, err)
	if _, err := e.svc.ListUser(ctx, friend, owner); !errors.Is(err, apperr.ErrUserNotFound) {
		t.Fatalf("banlı sahip: %v", err)
	}
	if l, err := e.social.ListFriends(ctx, friend, 0, ""); err != nil || len(l.Items) != 0 {
		t.Fatalf("banlı sahip arkadaş listesinde: %+v %v", l, err)
	}
}

// The friend list badge: the nearest day from today on, only for accepted
// friends, never a past day, never the note.
func TestFriendListBadge(t *testing.T) {
	e := setup(t)
	me := e.user(t, "Ben")
	busy := e.user(t, "Dolu")
	free := e.user(t, "Boş")
	asker := e.user(t, "İsteyen")
	e.friends(t, me, busy)
	e.friends(t, me, free)
	_, err := e.svc.Set(ctx, asker, e.day(1), []Slot{SlotMorning}, "")
	must(t, err)
	_, err = e.social.SendFriendRequest(ctx, asker, me)
	must(t, err)

	_, err = e.svc.Set(ctx, free, e.day(5), []Slot{SlotAllDay}, "gizli not")
	must(t, err)
	_, err = e.svc.Set(ctx, free, e.day(3), []Slot{SlotEvening, SlotAfternoon}, "")
	must(t, err)
	// A past day (moved by the owner role; core_app cannot) never shows.
	_, err = e.svc.Set(ctx, busy, e.day(1), []Slot{SlotMorning}, "")
	must(t, err)
	_, err = e.admin.Exec(ctx, `UPDATE user_availability SET day = day - 3 WHERE user_id = $1`, busy.PG())
	must(t, err)

	l, err := e.social.ListFriends(ctx, me, 0, "")
	must(t, err)
	got := map[id.ID]social.NextAvailable{}
	for _, f := range l.Items {
		got[f.UserID] = f.NextAvailable
	}
	if len(got) != 2 {
		t.Fatalf("arkadaşlar: %+v", l.Items)
	}
	if n := got[free]; n.Date.Format(DateLayout) != e.day(3) || !slices.Equal(n.Slots, []string{"afternoon", "evening"}) {
		t.Fatalf("boş arkadaş rozeti: %+v", n)
	}
	if n := got[busy]; !n.Date.IsZero() {
		t.Fatalf("geçmiş gün rozette: %+v", n)
	}
	if days, err := e.svc.ListUser(ctx, me, busy); err != nil || len(days) != 0 {
		t.Fatalf("geçmiş gün listede: %+v %v", days, err)
	}
	// Requests carry no badge, even when the requester has a free day.
	in, err := e.social.ListFriendRequests(ctx, me, true, 0, "")
	if err != nil || len(in.Items) != 1 || !in.Items[0].NextAvailable.Date.IsZero() {
		t.Fatalf("istekte rozet: %+v %v", in, err)
	}
}

// The database refuses what Go refuses (second line, ADR-0011).
func TestDatabaseChecks(t *testing.T) {
	e := setup(t)
	u := e.user(t, "Kişi")
	insert := func(day string, slots []string, note any) error {
		_, err := e.pool.Exec(ctx, `INSERT INTO user_availability (id, user_id, day, slots, note) VALUES ($1, $2, $3::date, $4, $5)`,
			id.New().PG(), u.PG(), day, slots, note)
		return err
	}
	for name, tc := range map[string]struct {
		day   string
		slots []string
		note  any
	}{
		"32 gün ileri":       {e.day(32), []string{"morning"}, nil},
		"iki gün önce":       {e.day(-2), []string{"morning"}, nil},
		"boş dilim":          {e.day(1), []string{}, nil},
		"bilinmeyen dilim":   {e.day(1), []string{"night"}, nil},
		"tüm gün + sabah":    {e.day(1), []string{"all_day", "morning"}, nil},
		"tekrar eden dilim":  {e.day(1), []string{"morning", "morning"}, nil},
		"dört dilim":         {e.day(1), []string{"morning", "afternoon", "evening", "all_day"}, nil},
		"boş not":            {e.day(1), []string{"morning"}, ""},
		"uzun not":           {e.day(1), []string{"morning"}, strings.Repeat("a", 61)},
		"boşluklu not":       {e.day(1), []string{"morning"}, " not"},
		"kontrol karakterli": {e.day(1), []string{"morning"}, "a\nb"},
	} {
		if err := insert(tc.day, tc.slots, tc.note); err == nil {
			t.Errorf("%s kabul edildi", name)
		}
	}
	must(t, insert(e.day(1), []string{"morning", "evening"}, strings.Repeat("ç", 60)))
	if err := insert(e.day(1), []string{"evening"}, nil); err == nil {
		t.Error("aynı güne ikinci satır")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE user_availability SET user_id = $2 WHERE user_id = $1`, u.PG(), id.New().PG()); err == nil {
		t.Error("core_app user_id'yi değiştirdi")
	}
}
