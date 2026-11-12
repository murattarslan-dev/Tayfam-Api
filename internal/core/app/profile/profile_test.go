package profile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var (
	ctx = context.Background()
	now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
)

type env struct {
	svc  *Service
	pool *pgxpool.Pool
	// admin is a superuser pool for fixtures core_app cannot write
	// (seeding with triggers off, ADR-0015).
	admin *pgxpool.Pool
	seq   int
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	pool := d.App
	svc, err := New(Config{Pool: pool, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, pool: pool, admin: d.Admin}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seed runs setup SQL with triggers off.
func (e *env) seed(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, sql, args...)
	must(t, err)
	must(t, tx.Commit(ctx))
}

func (e *env) user(t *testing.T, vis Visibility) id.ID {
	t.Helper()
	u := id.New()
	e.seed(t, `INSERT INTO user_profiles (user_id, display_name, source_event_id, activity_visibility) VALUES ($1, 'Kişi', $2, $3)`, u.PG(), id.New().PG(), string(vis))
	return u
}

func (e *env) tayfa(t *testing.T, name string, members ...id.ID) id.ID {
	t.Helper()
	e.seq++
	tf := id.New()
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	b := []byte("TEST")
	for n := e.seq; len(b) < 8; n /= len(alphabet) {
		b = append(b, alphabet[n%len(alphabet)])
	}
	code := string(b)
	e.seed(t, `INSERT INTO tayfas (id, name, name_normalized, join_code, created_by) VALUES ($1, $2, $2, $3, $4)`, tf.PG(), name, code, members[0].PG())
	for _, m := range members {
		e.seed(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at) VALUES ($1, $2, 'member', 'active', 'code', now())`, tf.PG(), m.PG())
	}
	return tf
}

func (e *env) record(t *testing.T, u, tf id.ID, at time.Time) {
	t.Helper()
	e.seed(t, `INSERT INTO user_activity_records (id, user_id, tayfa_id, event_type, mode, event_title, city, district, started_at, duration_minutes, attendance_method)
		VALUES ($1, $2, $3, 'hike', 'in_person', 'Gizli Rota', 'Bursa', 'Nilüfer', $4, 240, 'location')`, id.New().PG(), u.PG(), tf.PG(), at)
}

func (e *env) friends(t *testing.T, a, b id.ID) {
	t.Helper()
	low, high := social.Pair(a, b)
	e.seed(t, `INSERT INTO friendships (user_low_id, user_high_id, status, requested_by, accepted_at) VALUES ($1, $2, 'accepted', $1, now())`, low.PG(), high.PG())
}

func TestAccessTable(t *testing.T) {
	type res struct{ visible, delayed bool }
	for _, tc := range []struct {
		v    Visibility
		a    Audience
		want res
	}{
		{VisibilityPrivate, AudienceSelf, res{true, false}},
		{VisibilityPrivate, AudienceFriend, res{false, false}},
		{VisibilityPrivate, AudienceMutualTayfa, res{false, false}},
		{VisibilityPrivate, AudienceOther, res{false, false}},
		{VisibilityTayfa, AudienceSelf, res{true, false}},
		{VisibilityTayfa, AudienceFriend, res{true, false}},
		{VisibilityTayfa, AudienceMutualTayfa, res{true, true}},
		{VisibilityTayfa, AudienceOther, res{false, false}},
		{VisibilityPublic, AudienceSelf, res{true, false}},
		{VisibilityPublic, AudienceFriend, res{true, false}},
		{VisibilityPublic, AudienceMutualTayfa, res{true, true}},
		{VisibilityPublic, AudienceOther, res{true, true}},
		{"bilinmeyen", AudienceFriend, res{false, false}},
		{"", AudienceOther, res{false, false}},
	} {
		v, d := Access(tc.v, tc.a)
		if (res{v, d}) != tc.want {
			t.Errorf("%s/%d: %v %v", tc.v, tc.a, v, d)
		}
	}
}

// TestVisibilityMatrix is the acceptance table of issue #71: every setting
// × friend / shared tayfa / other × a record older and newer than 14 days.
func TestVisibilityMatrix(t *testing.T) {
	old, recent := now.Add(-Delay-time.Hour), now.Add(-Delay+time.Hour)
	type want struct{ old, recent bool }
	for _, vis := range []Visibility{VisibilityPrivate, VisibilityTayfa, VisibilityPublic} {
		e := setup(t)
		subject := e.user(t, vis)
		friend, mate, other := e.user(t, VisibilityTayfa), e.user(t, VisibilityTayfa), e.user(t, VisibilityTayfa)
		e.friends(t, subject, friend)
		tf := e.tayfa(t, "Ortak Tayfa", subject, mate)
		e.record(t, subject, tf, old)
		e.record(t, subject, tf, recent)

		expect := map[Visibility]map[string]want{
			VisibilityPrivate: {"self": {true, true}, "friend": {false, false}, "mate": {false, false}, "other": {false, false}},
			VisibilityTayfa:   {"self": {true, true}, "friend": {true, true}, "mate": {true, false}, "other": {false, false}},
			VisibilityPublic:  {"self": {true, true}, "friend": {true, true}, "mate": {true, false}, "other": {true, false}},
		}[vis]
		for name, viewer := range map[string]id.ID{"self": subject, "friend": friend, "mate": mate, "other": other} {
			p, err := e.svc.ListActivity(ctx, viewer, subject, 0, "")
			must(t, err)
			got := want{}
			for _, a := range p.Items {
				switch {
				case a.StartedAt.Equal(old):
					got.old = true
				case a.StartedAt.Equal(recent):
					got.recent = true
				}
			}
			if got != expect[name] {
				t.Errorf("%s / %s: %+v, beklenen %+v", vis, name, got, expect[name])
			}
			prof, err := e.svc.GetProfile(ctx, viewer, subject)
			must(t, err)
			if prof.ActivityVisible != (expect[name].old || expect[name].recent) {
				t.Errorf("%s / %s: activity_visible = %v", vis, name, prof.ActivityVisible)
			}
		}
	}
}

// TestTayfaNameOnlyForMembers: a friend who is not in the record's tayfa
// sees type, date and city only; a member sees the rest.
func TestTayfaNameOnlyForMembers(t *testing.T) {
	e := setup(t)
	subject := e.user(t, VisibilityPublic)
	friend, mate := e.user(t, VisibilityTayfa), e.user(t, VisibilityTayfa)
	e.friends(t, subject, friend)
	secret := e.tayfa(t, "Gizli Tayfa", subject, mate)
	e.record(t, subject, secret, now.Add(-30*24*time.Hour))

	p, err := e.svc.ListActivity(ctx, friend, subject, 0, "")
	must(t, err)
	a := p.Items[0]
	if a.Member || a.TayfaName != "" || a.TayfaID != (id.ID{}) || a.EventTitle != "" || a.DurationMinutes != 0 {
		t.Fatalf("üye olmayana tayfa bilgisi sızdı: %+v", a)
	}
	if a.EventType != "hike" || a.City != "Bursa" || a.District != "Nilüfer" || a.StartedAt.IsZero() {
		t.Fatalf("kaba bilgi eksik: %+v", a)
	}
	p, err = e.svc.ListActivity(ctx, mate, subject, 0, "")
	must(t, err)
	if a := p.Items[0]; !a.Member || a.TayfaName != "Gizli Tayfa" || a.TayfaID != secret || a.EventTitle != "Gizli Rota" || a.DurationMinutes != 240 {
		t.Fatalf("üyeye tayfa bilgisi: %+v", a)
	}
	// A member who left no longer sees it.
	e.seed(t, `DELETE FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, secret.PG(), mate.PG())
	p, err = e.svc.ListActivity(ctx, mate, subject, 0, "")
	must(t, err)
	if len(p.Items) != 1 || p.Items[0].Member {
		t.Fatalf("ayrılan üye: %+v", p.Items)
	}
}

func TestMutualTayfasAndProfile(t *testing.T) {
	e := setup(t)
	subject, viewer := e.user(t, VisibilityTayfa), e.user(t, VisibilityTayfa)
	shared := e.tayfa(t, "Ortak", subject, viewer)
	e.tayfa(t, "Yalnız Onun", subject)
	e.tayfa(t, "Yalnız Benim", viewer)
	// A pending membership does not count as shared.
	pendingTf := e.tayfa(t, "Bekleyen", subject)
	e.seed(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via) VALUES ($1, $2, 'member', 'pending', 'code')`, pendingTf.PG(), viewer.PG())

	p, err := e.svc.GetProfile(ctx, viewer, subject)
	must(t, err)
	if len(p.MutualTayfas) != 1 || p.MutualTayfas[0].ID != shared || p.MutualTayfas[0].Name != "Ortak" || p.Self || p.ActivityVisibility != "" || p.Friend != "" {
		t.Fatalf("profil = %+v", p)
	}
	_, err = e.pool.Exec(ctx, `UPDATE tayfas SET deleted_at = now() WHERE id = $1`, shared.PG())
	must(t, err)
	p, _ = e.svc.GetProfile(ctx, viewer, subject)
	if len(p.MutualTayfas) != 0 {
		t.Fatalf("silinmiş tayfa: %+v", p.MutualTayfas)
	}

	self, err := e.svc.GetProfile(ctx, subject, subject)
	if err != nil || !self.Self || self.ActivityVisibility != VisibilityTayfa || len(self.MutualTayfas) != 0 {
		t.Fatalf("kendi profili: %+v %v", self, err)
	}
	// A friend request shows from both sides.
	low, high := social.Pair(viewer, subject)
	e.seed(t, `INSERT INTO friendships (user_low_id, user_high_id, status, requested_by) VALUES ($1, $2, 'pending', $3)`, low.PG(), high.PG(), viewer.PG())
	if p, _ := e.svc.GetProfile(ctx, viewer, subject); p.Friend != social.StateOutgoing {
		t.Fatalf("giden istek: %q", p.Friend)
	}
	if p, _ := e.svc.GetProfile(ctx, subject, viewer); p.Friend != social.StateIncoming {
		t.Fatalf("gelen istek: %q", p.Friend)
	}
}

// TestBlockHidesProfile: either direction; the same answer as a user who
// does not exist.
func TestBlockHidesProfile(t *testing.T) {
	e := setup(t)
	a, b := e.user(t, VisibilityPublic), e.user(t, VisibilityPublic)
	e.friends(t, a, b)
	e.seed(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, b.PG(), a.PG())
	for _, pair := range [][2]id.ID{{a, b}, {b, a}} {
		if _, err := e.svc.GetProfile(ctx, pair[0], pair[1]); !errors.Is(err, apperr.ErrUserNotFound) {
			t.Fatalf("profil: %v", err)
		}
		if _, err := e.svc.ListActivity(ctx, pair[0], pair[1], 0, ""); !errors.Is(err, apperr.ErrUserNotFound) {
			t.Fatalf("geçmiş: %v", err)
		}
	}
	for _, u := range []id.ID{id.New(), {}} {
		_, err := e.svc.GetProfile(ctx, a, u)
		var v *apperr.ValidationError
		if !errors.Is(err, apperr.ErrUserNotFound) && !errors.As(err, &v) {
			t.Fatalf("bilinmeyen: %v", err)
		}
	}
	gone := e.user(t, VisibilityPublic)
	_, err := e.pool.Exec(ctx, `UPDATE user_profiles SET display_name = NULL, deleted_at = now() WHERE user_id = $1`, gone.PG())
	must(t, err)
	if _, err := e.svc.GetProfile(ctx, a, gone); !errors.Is(err, apperr.ErrUserNotFound) {
		t.Fatalf("silinmiş: %v", err)
	}
}

func TestActivityPaging(t *testing.T) {
	e := setup(t)
	u := e.user(t, VisibilityPublic)
	tf := e.tayfa(t, "Tayfa", u)
	for i := range 5 {
		e.record(t, u, tf, now.Add(-time.Duration(30+i)*24*time.Hour))
	}
	var all []time.Time
	token := ""
	for range 4 {
		p, err := e.svc.ListActivity(ctx, u, u, 2, token)
		must(t, err)
		for _, a := range p.Items {
			all = append(all, a.StartedAt)
		}
		if token = p.Next; token == "" {
			break
		}
	}
	if len(all) != 5 {
		t.Fatalf("kayıt = %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if !all[i].Before(all[i-1]) {
			t.Fatal("sıra yeniden eskiye değil")
		}
	}
	// A token of one profile is refused on another.
	p, _ := e.svc.ListActivity(ctx, u, u, 2, "")
	other := e.user(t, VisibilityPublic)
	_, err := e.svc.ListActivity(ctx, other, other, 2, p.Next)
	var v *apperr.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("başka profilin imleci: %v", err)
	}
}

// TestProfileNameWithoutRelation: the name and avatar come back for anyone
// not blocked, with no shared tayfa or friendship. Notifications carry IDs
// only (a friend request, a direct invite), and the client takes the name
// from here (issue #150).
func TestProfileNameWithoutRelation(t *testing.T) {
	e := setup(t)
	stranger, viewer := e.user(t, VisibilityPrivate), e.user(t, VisibilityPrivate)
	p, err := e.svc.GetProfile(ctx, viewer, stranger)
	if err != nil || p.DisplayName != "Kişi" || p.UserID != stranger || p.Self || p.Friend != "" ||
		len(p.MutualTayfas) != 0 || p.ActivityVisible {
		t.Fatalf("profil = %+v %v", p, err)
	}
}
