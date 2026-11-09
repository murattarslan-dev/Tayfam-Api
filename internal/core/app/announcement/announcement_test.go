package announcement

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type env struct {
	svc    *Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	shift  time.Duration
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, coredb.Migrations())
	e := &env{pool: pool}
	now := func() time.Time { return time.Now().Add(e.shift) }
	var err error
	if e.svc, err = New(Config{Pool: pool, Now: now}); err != nil {
		t.Fatal(err)
	}
	if e.tayfas, err = tayfa.New(tayfa.Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
		t.Fatal(err)
	}
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func is(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: %v, %v bekleniyordu", what, err, want)
	}
}

// crew is a tayfa with one member of each kind.
type crew struct {
	tayfa                                id.ID
	admin, admin2, mod, mod2, member     id.ID
	mutedMod, pending, banned, nonMember id.ID
}

func (e *env) crew(t *testing.T) crew {
	t.Helper()
	ctx := context.Background()
	c := crew{admin: id.New(), nonMember: id.New()}
	v, err := e.tayfas.Create(ctx, c.admin, tayfa.CreateInput{Name: "Klasik Otomobil Tayfası", JoinPolicy: tayfa.JoinOpen, ClientRequestID: id.New().String()})
	must(t, err)
	c.tayfa = v.ID
	c.admin2 = e.member(t, c, authz.Admin)
	c.mod = e.member(t, c, authz.Moderator)
	c.mod2 = e.member(t, c, authz.Moderator)
	c.member = e.member(t, c, authz.Member)
	c.mutedMod = e.member(t, c, authz.Moderator)
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.MuteMembership(ctx, store.MuteMembershipParams{TayfaID: c.tayfa.PG(), UserID: c.mutedMod.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: c.admin.PG()})
		return err
	})
	c.pending = id.New()
	e.join(t, c.tayfa, c.pending, authz.StatusPending)
	c.banned = e.member(t, c, authz.Member)
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.BanMembership(ctx, store.BanMembershipParams{TayfaID: c.tayfa.PG(), UserID: c.banned.PG()})
		return err
	})
	return c
}

func (e *env) as(t *testing.T, actor id.ID, fn func(q *store.Queries) error) {
	t.Helper()
	must(t, txn.WithActor(context.Background(), e.pool, actor, func(_ pgx.Tx, q *store.Queries) error { return fn(q) }))
}

func (e *env) join(t *testing.T, tayfa, user id.ID, st authz.Status) {
	t.Helper()
	joined := pgtype.Timestamptz{}
	if st == authz.StatusActive {
		joined = ts(time.Now())
	}
	e.as(t, user, func(q *store.Queries) error {
		return q.CreateMembership(context.Background(), store.CreateMembershipParams{
			TayfaID: tayfa.PG(), UserID: user.PG(), Role: "member", Status: string(st), JoinedVia: "code", JoinedAt: joined,
		})
	})
}

func (e *env) member(t *testing.T, c crew, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
	e.join(t, c.tayfa, u, authz.StatusActive)
	e.setRole(t, c, u, role)
	return u
}

func (e *env) setRole(t *testing.T, c crew, u id.ID, role authz.Role) {
	t.Helper()
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.SetMembershipRole(context.Background(), store.SetMembershipRoleParams{TayfaID: c.tayfa.PG(), UserID: u.PG(), Role: role.String()})
		return err
	})
}

func (e *env) publish(t *testing.T, c crew, author id.ID, title string) View {
	t.Helper()
	v, err := e.svc.Create(context.Background(), author, c.tayfa, CreateInput{Title: title, Body: "Gövde", ClientRequestID: id.New().String()})
	must(t, err)
	return v
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func TestCreateWhoMayPublish(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		user id.ID
		want error
	}{
		{"yönetici", c.admin, nil},
		{"moderatör", c.mod, nil},
		{"üye", c.member, apperr.ErrPermissionDenied},
		{"susturulmuş moderatör", c.mutedMod, apperr.ErrMuted},
		{"onay bekleyen", c.pending, apperr.ErrTayfaNotFound},
		{"banlı", c.banned, apperr.ErrTayfaNotFound},
		{"üye değil", c.nonMember, apperr.ErrTayfaNotFound},
	} {
		_, err := e.svc.Create(ctx, tc.user, c.tayfa, CreateInput{Title: "Pazar konvoyu", ClientRequestID: id.New().String()})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}
	if n := e.count(t, "SELECT count(*) FROM announcements"); n != 2 {
		t.Fatalf("duyuru sayısı = %d", n)
	}
}

func TestCreateRecordsWithoutContent(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	in := CreateInput{Title: "  Gizli Başlık  ", Body: "\n Gizli gövde\tsatırı \n", ClientRequestID: id.New().String()}
	v, err := e.svc.Create(ctx, c.mod, c.tayfa, in)
	must(t, err)
	if v.Title != "Gizli Başlık" || v.Body != "Gizli gövde\tsatırı" || v.AuthorID != c.mod || v.Pinned || v.TayfaID != c.tayfa || v.CreatedAt.IsZero() {
		t.Fatalf("görünüm = %+v", v)
	}
	again, err := e.svc.Create(ctx, c.mod, c.tayfa, in)
	if err != nil || again.ID != v.ID {
		t.Fatalf("tekrar gönderim: %v %v", again.ID, err)
	}
	if n := e.count(t, "SELECT count(*) FROM announcements"); n != 1 {
		t.Fatalf("tekrar gönderim yeni duyuru açtı: %d", n)
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action = 'announcement.created' AND target_id = $1 AND actor_id = $2", v.ID.PG(), c.mod.PG()); n != 1 {
		t.Fatalf("denetim = %d", n)
	}
	var payload string
	must(t, e.pool.QueryRow(ctx, "SELECT payload::text FROM domain_events WHERE type = 'announcement.created' AND tayfa_id = $1 AND subject_id = $2", c.tayfa.PG(), v.ID.PG()).Scan(&payload))
	if !strings.Contains(payload, v.ID.String()) || strings.Contains(payload, "Gizli") {
		t.Fatalf("olay = %s", payload)
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%Gizli%'"); n != 0 {
		t.Fatal("içerik denetim kaydına yazıldı")
	}
	// The same request ID in another tayfa is refused.
	other := e.crew(t)
	e.join(t, other.tayfa, c.mod, authz.StatusActive)
	e.setRole(t, other, c.mod, authz.Moderator)
	var ve *apperr.ValidationError
	if _, err := e.svc.Create(ctx, c.mod, other.tayfa, in); !errors.As(err, &ve) || ve.Field != "client_request_id" {
		t.Fatalf("başka tayfada aynı istek: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	crid := id.New().String()
	for name, tc := range map[string]struct {
		in    CreateInput
		field string
	}{
		"boş başlık":       {CreateInput{Title: "   ", ClientRequestID: crid}, "title"},
		"uzun başlık":      {CreateInput{Title: strings.Repeat("ş", 121), ClientRequestID: crid}, "title"},
		"çok satırlı":      {CreateInput{Title: "a\nb", ClientRequestID: crid}, "title"},
		"kontrol karakter": {CreateInput{Title: "Başlık", Body: "a\x00b", ClientRequestID: crid}, "body"},
		"bozuk utf8":       {CreateInput{Title: "Ba\xffşlık", ClientRequestID: crid}, "title"},
		"uzun gövde":       {CreateInput{Title: "Başlık", Body: strings.Repeat("ğ", 5001), ClientRequestID: crid}, "body"},
		"istek kimliği":    {CreateInput{Title: "Başlık"}, "client_request_id"},
	} {
		var ve *apperr.ValidationError
		if _, err := e.svc.Create(context.Background(), c.admin, c.tayfa, tc.in); !errors.As(err, &ve) || ve.Field != tc.field {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Limits are inclusive.
	_, err := e.svc.Create(context.Background(), c.admin, c.tayfa, CreateInput{Title: strings.Repeat("ş", 120), Body: strings.Repeat("ğ", 5000), ClientRequestID: crid})
	must(t, err)
}

func TestEditHierarchy(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	byAdmin := e.publish(t, c, c.admin, "Yönetici duyurusu")
	byMod := e.publish(t, c, c.mod, "Moderatör duyurusu")
	edit := func(user id.ID, a View) error {
		_, err := e.svc.Update(ctx, user, c.tayfa, a.ID, UpdateInput{Fields: []string{FieldTitle}, Title: "Yeni başlık"})
		return err
	}
	for _, tc := range []struct {
		name string
		user id.ID
		a    View
		want error
	}{
		{"moderatör kendi", c.mod, byMod, nil},
		{"moderatör başka moderatörün", c.mod2, byMod, apperr.ErrRoleHierarchy},
		{"moderatör yöneticinin", c.mod, byAdmin, apperr.ErrRoleHierarchy},
		{"yönetici moderatörün", c.admin, byMod, nil},
		{"yönetici başka yöneticinin", c.admin2, byAdmin, apperr.ErrRoleHierarchy},
		{"yönetici kendi", c.admin, byAdmin, nil},
		{"üye", c.member, byMod, apperr.ErrPermissionDenied},
		{"susturulmuş moderatör", c.mutedMod, byMod, apperr.ErrMuted},
		{"üye değil", c.nonMember, byMod, apperr.ErrTayfaNotFound},
		{"bilinmeyen duyuru", c.admin, View{ID: id.New()}, apperr.ErrAnnouncementNotFound},
	} {
		if err := edit(tc.user, tc.a); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}

	// A demoted author can no longer edit; moderators now can.
	e.setRole(t, c, c.mod, authz.Member)
	is(t, "rolü düşen yazar", edit(c.mod, byMod), apperr.ErrPermissionDenied)
	must(t, edit(c.mod2, byMod))
	// An author who left: their announcement belongs to the tayfa.
	byMod2 := e.publish(t, c, c.mod2, "Ayrılanın duyurusu")
	e.as(t, c.mod2, func(q *store.Queries) error {
		_, err := q.DeleteMembership(ctx, store.DeleteMembershipParams{TayfaID: c.tayfa.PG(), UserID: c.mod2.PG()})
		return err
	})
	_, err := e.svc.Update(ctx, c.mutedMod, c.tayfa, byMod2.ID, UpdateInput{Fields: []string{FieldBody}, Body: "x"})
	is(t, "susturulmuş", err, apperr.ErrMuted)
	e.setRole(t, c, c.member, authz.Moderator)
	v, err := e.svc.Update(ctx, c.member, c.tayfa, byMod2.ID, UpdateInput{Fields: []string{FieldBody}, Body: " Yeni gövde "})
	if err != nil || v.Body != "Yeni gövde" || v.Title != "Ayrılanın duyurusu" {
		t.Fatalf("ayrılan yazar: %+v %v", v, err)
	}
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE type = 'announcement.updated' AND subject_id = $1 AND payload->>'change' = 'edited'", byMod2.ID.PG()); n != 1 {
		t.Fatalf("olay = %d", n)
	}
	// Same values: no record.
	_, err = e.svc.Update(ctx, c.member, c.tayfa, byMod2.ID, UpdateInput{Fields: []string{FieldBody}, Body: "Yeni gövde"})
	must(t, err)
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action = 'announcement.edited' AND target_id = $1", byMod2.ID.PG()); n != 1 {
		t.Fatalf("değişmeyen düzenleme kaydedildi: %d", n)
	}

	var ve *apperr.ValidationError
	for _, in := range []UpdateInput{{}, {Fields: []string{"pinned"}}, {Fields: []string{FieldTitle}, Title: ""}} {
		if _, err := e.svc.Update(ctx, c.admin, c.tayfa, byAdmin.ID, in); !errors.As(err, &ve) {
			t.Errorf("%+v: %v", in, err)
		}
	}
}

func TestDeleteAndRestore(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()

	// The author's own deletion empties the content; it cannot be restored.
	own := e.publish(t, c, c.mod, "Kendi duyurum")
	must(t, e.svc.Delete(ctx, c.mod, c.tayfa, own.ID))
	var title, body string
	must(t, e.pool.QueryRow(ctx, "SELECT title, body FROM announcements WHERE id = $1", own.ID.PG()).Scan(&title, &body))
	if title != "" || body != "" {
		t.Fatalf("içerik boşaltılmadı: %q %q", title, body)
	}
	_, err := e.svc.Get(ctx, c.member, c.tayfa, own.ID)
	is(t, "silinen okundu", err, apperr.ErrAnnouncementNotFound)
	_, err = e.svc.Restore(ctx, c.admin, c.tayfa, own.ID)
	is(t, "yazarın silmesi geri alındı", err, apperr.ErrAnnouncementNotFound)
	is(t, "iki kez silme", e.svc.Delete(ctx, c.mod, c.tayfa, own.ID), apperr.ErrAnnouncementNotFound)

	// A moderator cannot delete an admin's announcement; an admin can
	// delete a moderator's, which hides it.
	byAdmin := e.publish(t, c, c.admin, "Yönetici duyurusu")
	is(t, "moderatör yöneticininkini sildi", e.svc.Delete(ctx, c.mod, c.tayfa, byAdmin.ID), apperr.ErrRoleHierarchy)
	is(t, "üye sildi", e.svc.Delete(ctx, c.member, c.tayfa, byAdmin.ID), apperr.ErrPermissionDenied)
	byMod := e.publish(t, c, c.mod, "Moderatör duyurusu")
	must(t, e.svc.Delete(ctx, c.admin2, c.tayfa, byMod.ID))
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE type = 'announcement.deleted' AND subject_id = $1", byMod.ID.PG()); n != 1 {
		t.Fatalf("silme olayı = %d", n)
	}
	_, err = e.svc.Restore(ctx, c.mod2, c.tayfa, byMod.ID)
	is(t, "moderatör geri aldı", err, apperr.ErrPermissionDenied)
	v, err := e.svc.Restore(ctx, c.admin, c.tayfa, byMod.ID)
	if err != nil || v.Title != "Moderatör duyurusu" || v.Body != "Gövde" {
		t.Fatalf("geri alma: %+v %v", v, err)
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action = 'announcement.restored' AND target_id = $1 AND actor_id = $2", byMod.ID.PG(), c.admin.PG()); n != 1 {
		t.Fatalf("geri alma kaydı = %d", n)
	}
	// Restoring a visible announcement changes nothing.
	_, err = e.svc.Restore(ctx, c.admin, c.tayfa, byMod.ID)
	must(t, err)
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action = 'announcement.restored'"); n != 1 {
		t.Fatalf("görünen duyurunun geri alınması kaydedildi: %d", n)
	}
	// An admin cannot hide or restore another admin's announcement; the
	// author's current role counts.
	e.setRole(t, c, c.mod, authz.Admin)
	is(t, "yönetici yöneticininkini sildi", e.svc.Delete(ctx, c.admin, c.tayfa, byMod.ID), apperr.ErrRoleHierarchy)
	// Only the admin themself can step down (memberships trigger).
	e.as(t, c.mod, func(q *store.Queries) error {
		_, err := q.SetMembershipRole(ctx, store.SetMembershipRoleParams{TayfaID: c.tayfa.PG(), UserID: c.mod.PG(), Role: "moderator"})
		return err
	})
	must(t, e.svc.Delete(ctx, c.admin, c.tayfa, byMod.ID))
	e.setRole(t, c, c.mod, authz.Admin)
	_, err = e.svc.Restore(ctx, c.admin, c.tayfa, byMod.ID)
	is(t, "yönetici yöneticininkini geri aldı", err, apperr.ErrRoleHierarchy)
	_, err = e.svc.Restore(ctx, c.mod, c.tayfa, byMod.ID)
	must(t, err)

	// The restore window.
	late := e.publish(t, c, c.mod2, "Geç kalınan")
	inTime := e.publish(t, c, c.mod2, "Zamanında")
	must(t, e.svc.Delete(ctx, c.admin, c.tayfa, late.ID))
	must(t, e.svc.Delete(ctx, c.admin, c.tayfa, inTime.ID))
	e.shift = RestoreWindow + time.Minute
	_, err = e.svc.Restore(ctx, c.admin, c.tayfa, late.ID)
	is(t, "süre geçti", err, apperr.ErrAnnouncementNotFound)
	e.shift = RestoreWindow - time.Minute
	_, err = e.svc.Restore(ctx, c.admin, c.tayfa, inTime.ID)
	must(t, err)
	_, err = e.svc.Restore(ctx, c.admin, c.tayfa, id.New())
	is(t, "bilinmeyen", err, apperr.ErrAnnouncementNotFound)
}

func TestPinAndList(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	var all []View
	for _, title := range []string{"bir", "iki", "üç", "dört", "beş"} {
		all = append(all, e.publish(t, c, c.admin, title))
	}
	// Pinning is not bound to the author's role: a moderator may pin an
	// admin's announcement.
	v, err := e.svc.SetPinned(ctx, c.mod, c.tayfa, all[1].ID, true)
	if err != nil || !v.Pinned {
		t.Fatalf("sabitleme: %+v %v", v, err)
	}
	_, err = e.svc.SetPinned(ctx, c.mod, c.tayfa, all[1].ID, true)
	must(t, err)
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action = 'announcement.pinned'"); n != 1 {
		t.Fatalf("sabitleme kaydı = %d", n)
	}
	_, err = e.svc.SetPinned(ctx, c.member, c.tayfa, all[0].ID, true)
	is(t, "üye sabitledi", err, apperr.ErrPermissionDenied)
	_, err = e.svc.SetPinned(ctx, c.mutedMod, c.tayfa, all[0].ID, true)
	is(t, "susturulmuş sabitledi", err, apperr.ErrMuted)
	must(t, e.svc.Delete(ctx, c.admin, c.tayfa, all[3].ID))

	// Every active member reads, also while muted.
	want := []id.ID{all[1].ID, all[4].ID, all[2].ID, all[0].ID}
	for _, reader := range []id.ID{c.member, c.mutedMod} {
		var got []id.ID
		token := ""
		for range 10 {
			page, next, err := e.svc.List(ctx, reader, c.tayfa, 2, token)
			must(t, err)
			for _, a := range page {
				got = append(got, a.ID)
			}
			if next == "" {
				break
			}
			token = next
		}
		if len(got) != len(want) {
			t.Fatalf("liste = %v", got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("sıra: %v, %v bekleniyordu", got, want)
			}
		}
	}
	g, err := e.svc.Get(ctx, c.member, c.tayfa, all[1].ID)
	if err != nil || !g.Pinned || g.Title != "iki" {
		t.Fatalf("Get: %+v %v", g, err)
	}
	for _, u := range []id.ID{c.pending, c.banned, c.nonMember} {
		_, _, err := e.svc.List(ctx, u, c.tayfa, 0, "")
		is(t, "üye olmayan listeledi", err, apperr.ErrTayfaNotFound)
		_, err = e.svc.Get(ctx, u, c.tayfa, all[1].ID)
		is(t, "üye olmayan okudu", err, apperr.ErrTayfaNotFound)
	}

	// Page tokens only work for the tayfa they were made for.
	_, next, err := e.svc.List(ctx, c.member, c.tayfa, 1, "")
	must(t, err)
	other := e.crew(t)
	var ve *apperr.ValidationError
	for _, bad := range []string{next[:len(next)-2], "!!", strings.Repeat("a", 600)} {
		if _, _, err := e.svc.List(ctx, c.member, c.tayfa, 1, bad); !errors.As(err, &ve) || ve.Field != "page_token" {
			t.Errorf("bozuk token %q: %v", bad, err)
		}
	}
	if _, _, err := e.svc.List(ctx, other.admin, other.tayfa, 1, next); !errors.As(err, &ve) {
		t.Errorf("başka tayfanın token'ı: %v", err)
	}
	if _, _, err := e.svc.List(ctx, c.member, c.tayfa, -1, ""); !errors.As(err, &ve) || ve.Field != "page_size" {
		t.Errorf("negatif sayfa: %v", err)
	}
}

// callAll calls every method as user on announcement a of tayfa.
func (e *env) callAll(user, tayfaID, a id.ID) map[string]error {
	ctx := context.Background()
	out := map[string]error{}
	_, out["Create"] = e.svc.Create(ctx, user, tayfaID, CreateInput{Title: "Yeni", ClientRequestID: id.New().String()})
	_, out["Get"] = e.svc.Get(ctx, user, tayfaID, a)
	_, _, out["List"] = e.svc.List(ctx, user, tayfaID, 0, "")
	_, out["Update"] = e.svc.Update(ctx, user, tayfaID, a, UpdateInput{Fields: []string{FieldTitle}, Title: "Değişti"})
	_, out["SetPinned"] = e.svc.SetPinned(ctx, user, tayfaID, a, true)
	_, out["Restore"] = e.svc.Restore(ctx, user, tayfaID, a)
	out["Delete"] = e.svc.Delete(ctx, user, tayfaID, a)
	return out
}

func TestModuleDisabled(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	a := e.publish(t, c, c.admin, "Duyuru")
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "announcements", false))
	for method, err := range e.callAll(c.admin, c.tayfa, a.ID) {
		is(t, method, err, apperr.ErrModuleDisabled)
	}
	for method, err := range e.callAll(c.member, c.tayfa, a.ID) {
		is(t, "üye "+method, err, apperr.ErrModuleDisabled)
	}
	// Non-members still learn nothing.
	for method, err := range e.callAll(c.nonMember, c.tayfa, a.ID) {
		is(t, "üye olmayan "+method, err, apperr.ErrTayfaNotFound)
	}
	if n := e.count(t, "SELECT count(*) FROM announcements"); n != 1 {
		t.Fatal("kapalı modülde yazıldı")
	}
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "announcements", true))
	_, err := e.svc.Get(ctx, c.member, c.tayfa, a.ID)
	must(t, err)
}

func TestReadOnlyTayfa(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	a := e.publish(t, c, c.admin, "Duyuru")
	hidden := e.publish(t, c, c.mod, "Gizlenen")
	must(t, e.svc.Delete(ctx, c.admin, c.tayfa, hidden.ID))
	_, err := e.pool.Exec(ctx, "UPDATE tayfas SET suspended_at = now(), suspended_reason = 'kural ihlali' WHERE id = $1", c.tayfa.PG())
	must(t, err)
	for method, err := range e.callAll(c.admin, c.tayfa, a.ID) {
		switch method {
		case "Get", "List":
			must(t, err)
		default:
			is(t, method, err, apperr.ErrTayfaReadOnly)
		}
	}
	_, err = e.svc.Restore(ctx, c.admin, c.tayfa, hidden.ID)
	is(t, "salt okunurda geri alma", err, apperr.ErrTayfaReadOnly)
}

// TestOtherTayfasAnnouncement: an admin of one tayfa cannot reach another
// tayfa's announcement through their own tayfa's ID (IDOR).
func TestOtherTayfasAnnouncement(t *testing.T) {
	e := setup(t)
	victim, attacker := e.crew(t), e.crew(t)
	a := e.publish(t, victim, victim.admin, "Hedef")
	ctx := context.Background()
	must(t, e.svc.Delete(ctx, victim.admin2, victim.tayfa, e.publish(t, victim, victim.mod, "Gizlenen").ID))
	for method, err := range e.callAll(attacker.admin, attacker.tayfa, a.ID) {
		if method == "Create" || method == "List" {
			must(t, err)
			continue
		}
		is(t, method, err, apperr.ErrAnnouncementNotFound)
	}
	for method, err := range e.callAll(attacker.admin, victim.tayfa, a.ID) {
		is(t, method, err, apperr.ErrTayfaNotFound)
	}
	v, err := e.svc.Get(ctx, victim.member, victim.tayfa, a.ID)
	if err != nil || v.Title != "Hedef" || v.Pinned {
		t.Fatalf("hedef değişti: %+v %v", v, err)
	}
	page, _, err := e.svc.List(ctx, attacker.admin, attacker.tayfa, 0, "")
	must(t, err)
	for _, p := range page {
		if p.TayfaID != attacker.tayfa {
			t.Fatal("başka tayfanın duyurusu listelendi")
		}
	}
}
