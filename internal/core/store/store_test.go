package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// admins maps each test's runtime pool (core_app) to the superuser pool of
// the same database, for fixtures the service cannot create itself.
var admins sync.Map

func setup(t *testing.T) (*pgxpool.Pool, *Queries) {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	admins.Store(d.App, d.Admin)
	t.Cleanup(func() { admins.Delete(d.App) })
	return d.App, New(d.App)
}

// adminOf returns the superuser pool of pool's database. Only fixtures and
// trigger tests that model a caller with more privileges than core_app use
// it.
func adminOf(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	a, ok := admins.Load(pool)
	if !ok {
		t.Fatal("yönetici bağlantısı yok")
	}
	return a.(*pgxpool.Pool)
}

var noActor id.ID

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func constraintOf(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

const codeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

func joinCode(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b[:])
}

// as runs fn in a transaction with app.actor_id set to actor (none for the
// zero ID) and commits it.
func as(t *testing.T, pool *pgxpool.Pool, actor id.ID, fn func(q *Queries) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := New(tx)
	if actor != noActor {
		if err := q.SetActor(ctx, actor.String()); err != nil {
			t.Fatal(err)
		}
	}
	if err := fn(q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// asJob runs fn as a user-less system job.
func asJob(t *testing.T, pool *pgxpool.Pool, job string, fn func(q *Queries) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := New(tx)
	if err := q.SetSystemJob(ctx, job); err != nil {
		return err
	}
	if err := fn(q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type tayfaOpts struct {
	open bool
}

// newTayfa creates a tayfa the way the app does: the tayfa row, then the
// creator's admin membership with the creator as actor.
func newTayfa(t *testing.T, pool *pgxpool.Pool, creator id.ID, o tayfaOpts) id.ID {
	t.Helper()
	ctx := context.Background()
	tid := id.New()
	policy := "approval"
	if o.open {
		policy = "open"
	}
	err := as(t, pool, creator, func(q *Queries) error {
		if err := q.CreateTayfa(ctx, CreateTayfaParams{
			ID: tid.PG(), Name: "Klasik Otomobil İzmir", NameNormalized: "klasik otomobil izmir",
			JoinCode: joinCode(t), JoinPolicy: policy, CreatedBy: creator.PG(),
		}); err != nil {
			return err
		}
		return q.CreateMembership(ctx, CreateMembershipParams{
			TayfaID: tid.PG(), UserID: creator.PG(), Role: "admin", Status: "active",
			JoinedVia: "creator", JoinedAt: ts(time.Now()),
		})
	})
	if err != nil {
		t.Fatalf("tayfa oluşturulamadı: %v", err)
	}
	return tid
}

// seed writes a membership directly with triggers disabled, to set up a
// state for a test. It uses the superuser pool; core_app cannot do this.
func seed(t *testing.T, pool *pgxpool.Pool, tayfa, user id.ID, role, status string) {
	t.Helper()
	ctx := context.Background()
	tx, err := adminOf(t, pool).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	var joined pgtype.Timestamptz
	if status == "active" {
		joined = ts(time.Now())
	}
	if err := New(tx).CreateMembership(ctx, CreateMembershipParams{
		TayfaID: tayfa.PG(), UserID: user.PG(), Role: role, Status: status,
		JoinedVia: "code", JoinedAt: joined,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func membership(t *testing.T, q *Queries, tayfa, user id.ID) (GetMembershipRow, bool) {
	t.Helper()
	m, err := q.GetMembership(context.Background(), GetMembershipParams{TayfaID: tayfa.PG(), UserID: user.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return m, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return m, true
}

func wantCode(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("izin verilmeliydi: %v", err)
		}
		return
	}
	if got := SQLState(err); got != want {
		t.Fatalf("hata kodu = %q (err=%v), %q bekleniyordu", got, err, want)
	}
}

// mustAffect turns "no row matched" into an error so a silently skipped
// update cannot pass as allowed.
func mustAffect(n int64, err error) error {
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("satır etkilenmedi")
	}
	return nil
}

func TestMigrationsRevokePublicAccess(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	var canConnect, canCreate bool
	err := pool.QueryRow(ctx, `
		SELECT has_database_privilege('public', current_database(), 'CONNECT'),
		       has_schema_privilege('public', 'public', 'CREATE')`).Scan(&canConnect, &canCreate)
	if err != nil {
		t.Fatal(err)
	}
	if canConnect || canCreate {
		t.Fatalf("PUBLIC yetkisi kalmış: connect=%v create=%v", canConnect, canCreate)
	}
}

// Every actor role × target role × operation. The expected result is
// written out; changing the hierarchy must change this table.
func TestRoleHierarchy(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()

	type op string
	const (
		toAdmin  op = "rol→admin"
		toMod    op = "rol→moderator"
		toMember op = "rol→member"
		ban      op = "ban"
		mute     op = "sustur"
		remove   op = "çıkar"
	)
	const deny = CodeRoleHierarchy
	cases := []struct {
		actor, target string
		op            op
		want          string
	}{
		{"admin", "admin", toAdmin, deny},
		{"admin", "admin", toMod, deny},
		{"admin", "admin", toMember, deny},
		{"admin", "admin", ban, deny},
		{"admin", "admin", mute, deny},
		{"admin", "admin", remove, deny},
		{"admin", "moderator", toAdmin, ""},
		{"admin", "moderator", toMod, ""},
		{"admin", "moderator", toMember, ""},
		{"admin", "moderator", ban, ""},
		{"admin", "moderator", mute, ""},
		{"admin", "moderator", remove, ""},
		{"admin", "member", toAdmin, ""},
		{"admin", "member", toMod, ""},
		{"admin", "member", toMember, ""},
		{"admin", "member", ban, ""},
		{"admin", "member", mute, ""},
		{"admin", "member", remove, ""},

		{"moderator", "admin", toAdmin, deny},
		{"moderator", "admin", toMod, deny},
		{"moderator", "admin", toMember, deny},
		{"moderator", "admin", ban, deny},
		{"moderator", "admin", mute, deny},
		{"moderator", "admin", remove, deny},
		{"moderator", "moderator", toAdmin, deny},
		{"moderator", "moderator", toMod, deny},
		{"moderator", "moderator", toMember, deny},
		{"moderator", "moderator", ban, deny},
		{"moderator", "moderator", mute, deny},
		{"moderator", "moderator", remove, deny},
		{"moderator", "member", toAdmin, deny},
		{"moderator", "member", toMod, ""},
		{"moderator", "member", toMember, ""},
		{"moderator", "member", ban, ""},
		{"moderator", "member", mute, ""},
		{"moderator", "member", remove, ""},

		{"member", "admin", toAdmin, deny},
		{"member", "admin", toMod, deny},
		{"member", "admin", toMember, deny},
		{"member", "admin", ban, deny},
		{"member", "admin", mute, deny},
		{"member", "admin", remove, deny},
		{"member", "moderator", toAdmin, deny},
		{"member", "moderator", toMod, deny},
		{"member", "moderator", toMember, deny},
		{"member", "moderator", ban, deny},
		{"member", "moderator", mute, deny},
		{"member", "moderator", remove, deny},
		{"member", "member", toAdmin, deny},
		{"member", "member", toMod, deny},
		{"member", "member", toMember, deny},
		{"member", "member", ban, deny},
		{"member", "member", mute, deny},
		{"member", "member", remove, deny},

		// Tayfada aktif üye olmayan aktörler hiçbir şeye dokunamaz.
		{"pending", "member", ban, deny},
		{"pending", "member", remove, deny},
		{"banned", "member", toMod, deny},
		{"banned", "member", remove, deny},
		{"invited", "member", mute, deny},
		{"non-member", "member", ban, deny},
		{"non-member", "member", remove, deny},
		{"other-tayfa-admin", "member", ban, deny},
		{"other-tayfa-admin", "member", toMod, deny},
		{"other-tayfa-admin", "member", remove, deny},
	}

	run := func(q *Queries, tayfa, actor, target id.ID, o op) error {
		k := func(role string) error {
			return mustAffect(q.SetMembershipRole(ctx, SetMembershipRoleParams{TayfaID: tayfa.PG(), UserID: target.PG(), Role: role}))
		}
		switch o {
		case toAdmin:
			return k("admin")
		case toMod:
			return k("moderator")
		case toMember:
			return k("member")
		case ban:
			return mustAffect(q.BanMembership(ctx, BanMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()}))
		case mute:
			return mustAffect(q.MuteMembership(ctx, MuteMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: actor.PG()}))
		case remove:
			return mustAffect(q.DeleteMembership(ctx, DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()}))
		}
		panic(o)
	}

	for _, tc := range cases {
		t.Run(tc.actor+"→"+tc.target+" "+string(tc.op), func(t *testing.T) {
			// Kurucu admin her tayfada kalır; son yönetici kuralı bu testi
			// etkilemez.
			creator, actor, target := id.New(), id.New(), id.New()
			tayfa := newTayfa(t, pool, creator, tayfaOpts{})
			seed(t, pool, tayfa, target, tc.target, "active")
			switch tc.actor {
			case "non-member":
			case "other-tayfa-admin":
				newTayfa(t, pool, actor, tayfaOpts{})
			case "pending", "invited":
				seed(t, pool, tayfa, actor, "member", tc.actor)
			case "banned":
				seed(t, pool, tayfa, actor, "admin", "banned")
			default:
				seed(t, pool, tayfa, actor, tc.actor, "active")
			}
			err := as(t, pool, actor, func(q *Queries) error { return run(q, tayfa, actor, target, tc.op) })
			wantCode(t, err, tc.want)
		})
	}
}

// Without app.actor_id nothing changes; this is the safe default.
func TestNoActorIsDenied(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	creator, member := id.New(), id.New()
	tayfa := newTayfa(t, pool, creator, tayfaOpts{open: true})
	seed(t, pool, tayfa, member, "member", "active")

	for name, fn := range map[string]func(q *Queries) error{
		"ekleme": func(q *Queries) error {
			return q.CreateMembership(ctx, CreateMembershipParams{TayfaID: tayfa.PG(), UserID: id.New().PG(), Role: "member", Status: "active", JoinedVia: "code", JoinedAt: ts(time.Now())})
		},
		"rol": func(q *Queries) error {
			return mustAffect(q.SetMembershipRole(ctx, SetMembershipRoleParams{TayfaID: tayfa.PG(), UserID: member.PG(), Role: "admin"}))
		},
		"ban": func(q *Queries) error {
			return mustAffect(q.BanMembership(ctx, BanMembershipParams{TayfaID: tayfa.PG(), UserID: member.PG()}))
		},
		"silme": func(q *Queries) error {
			return mustAffect(q.DeleteMembership(ctx, DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: creator.PG()}))
		},
		"boş aktör": func(q *Queries) error {
			if err := q.SetActor(ctx, ""); err != nil {
				return err
			}
			return mustAffect(q.DeleteMembership(ctx, DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: member.PG()}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			wantCode(t, as(t, pool, noActor, fn), CodeNoActor)
		})
	}

	// Geçersiz aktör değeri sessizce yok sayılmaz; işlem durur.
	err := as(t, pool, noActor, func(q *Queries) error {
		if err := q.SetActor(ctx, "admin"); err != nil {
			return err
		}
		return mustAffect(q.DeleteMembership(ctx, DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: member.PG()}))
	})
	if err == nil {
		t.Fatal("geçersiz aktör ile silme kabul edildi")
	}
	if _, ok := membership(t, New(pool), tayfa, member); !ok {
		t.Fatal("üyelik silinmiş")
	}

	// Aktör yalnızca kendi transaction'ında geçerlidir.
	err = as(t, pool, creator, func(q *Queries) error { return nil })
	wantCode(t, err, "")
	_, err = New(pool).DeleteMembership(ctx, DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: member.PG()})
	wantCode(t, err, CodeNoActor)
}

// What a person may do to their own membership.
func TestSelfChanges(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()

	cases := []struct {
		name string
		// role and status of the actor's own membership; the tayfa also has
		// a second active admin unless soleAdmin is set.
		role, status string
		soleAdmin    bool
		do           func(q *Queries, tayfa, self id.ID) error
		want         string
	}{
		{"üye ayrılır", "member", "active", false, leave, ""},
		{"moderatör ayrılır", "moderator", "active", false, leave, ""},
		{"admin ayrılır (başka admin var)", "admin", "active", false, leave, ""},
		{"tek admin ayrılamaz", "admin", "active", true, leave, CodeLastAdmin},
		{"başvuru geri çekilir", "member", "pending", false, leave, ""},
		{"davet reddedilir", "member", "invited", false, leave, ""},
		{"banlı kendi kaydını silemez", "member", "banned", false, leave, CodeRoleHierarchy},
		{"admin moderatörlüğe iner", "admin", "active", false, setOwnRole("moderator"), ""},
		{"admin üyeliğe iner", "admin", "active", false, setOwnRole("member"), ""},
		{"tek admin rolünü bırakamaz", "admin", "active", true, setOwnRole("member"), CodeLastAdmin},
		{"moderatör üyeliğe iner", "moderator", "active", false, setOwnRole("member"), ""},
		{"üye kendini moderatör yapamaz", "member", "active", false, setOwnRole("moderator"), CodeRoleHierarchy},
		{"moderatör kendini admin yapamaz", "moderator", "active", false, setOwnRole("admin"), CodeRoleHierarchy},
		{"başvuran kendini onaylayamaz", "member", "pending", false, func(q *Queries, tayfa, self id.ID) error {
			return mustAffect(q.ApproveMembership(ctx, ApproveMembershipParams{TayfaID: tayfa.PG(), UserID: self.PG(), ApprovedBy: self.PG()}))
		}, CodeRoleHierarchy},
		{"kendini susturamaz/susturmayı kaldıramaz", "member", "active", false, func(q *Queries, tayfa, self id.ID) error {
			return mustAffect(q.MuteMembership(ctx, MuteMembershipParams{TayfaID: tayfa.PG(), UserID: self.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: self.PG()}))
		}, CodeRoleHierarchy},
		{"admin kendini banlayamaz", "admin", "active", false, func(q *Queries, tayfa, self id.ID) error {
			return mustAffect(q.BanMembership(ctx, BanMembershipParams{TayfaID: tayfa.PG(), UserID: self.PG()}))
		}, CodeRoleHierarchy},
		{"davet onaya düşer", "member", "invited", false, accept("pending"), ""},
		{"üyenin daveti onaylı tayfada doğrudan aktif olmaz", "member", "invited", false, accept("active"), CodeRoleHierarchy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creator, self := id.New(), id.New()
			tayfa := newTayfa(t, pool, creator, tayfaOpts{})
			if tc.role == "admin" && tc.status == "active" && tc.soleAdmin {
				// The creator is the actor.
				self = creator
			} else {
				seed(t, pool, tayfa, self, tc.role, tc.status)
			}
			err := as(t, pool, self, func(q *Queries) error { return tc.do(q, tayfa, self) })
			wantCode(t, err, tc.want)
		})
	}

	t.Run("susturulan kendi susturmasını kaldıramaz", func(t *testing.T) {
		creator, self := id.New(), id.New()
		tayfa := newTayfa(t, pool, creator, tayfaOpts{})
		seed(t, pool, tayfa, self, "member", "active")
		wantCode(t, as(t, pool, creator, func(q *Queries) error {
			return mustAffect(q.MuteMembership(ctx, MuteMembershipParams{TayfaID: tayfa.PG(), UserID: self.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: creator.PG()}))
		}), "")
		wantCode(t, as(t, pool, self, func(q *Queries) error {
			return mustAffect(q.UnmuteMembership(ctx, UnmuteMembershipParams{TayfaID: tayfa.PG(), UserID: self.PG()}))
		}), CodeRoleHierarchy)
	})
}

func leave(q *Queries, tayfa, self id.ID) error {
	return mustAffect(q.DeleteMembership(context.Background(), DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: self.PG()}))
}

func setOwnRole(role string) func(q *Queries, tayfa, self id.ID) error {
	return func(q *Queries, tayfa, self id.ID) error {
		return mustAffect(q.SetMembershipRole(context.Background(), SetMembershipRoleParams{TayfaID: tayfa.PG(), UserID: self.PG(), Role: role}))
	}
}

func accept(status string) func(q *Queries, tayfa, self id.ID) error {
	return func(q *Queries, tayfa, self id.ID) error {
		return mustAffect(q.AcceptInvitation(context.Background(), AcceptInvitationParams{TayfaID: tayfa.PG(), UserID: self.PG(), Status: status}))
	}
}

func TestDirectInvitation(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	creator, mod, member, outsider := id.New(), id.New(), id.New(), id.New()
	tayfa := newTayfa(t, pool, creator, tayfaOpts{})
	seed(t, pool, tayfa, mod, "moderator", "active")
	seed(t, pool, tayfa, member, "member", "active")

	invite := func(by, who id.ID, role string, invitedBy id.ID) error {
		return as(t, pool, by, func(q *Queries) error {
			return q.CreateMembership(ctx, CreateMembershipParams{
				TayfaID: tayfa.PG(), UserID: who.PG(), Role: role, Status: "invited",
				JoinedVia: "direct_invite", InvitedByUserID: invitedBy.PG(),
			})
		})
	}

	byMember, byMod := id.New(), id.New()
	wantCode(t, invite(member, byMember, "member", member), "")
	wantCode(t, invite(mod, byMod, "member", mod), "")
	wantCode(t, invite(outsider, id.New(), "member", outsider), CodeRoleHierarchy)
	wantCode(t, invite(member, id.New(), "member", mod), CodeRoleHierarchy) // başkası adına
	wantCode(t, invite(creator, id.New(), "moderator", creator), CodeRoleHierarchy)

	// Başkasını doğrudan aktif üye yapmak doğrudan davet değildir.
	err := as(t, pool, creator, func(q *Queries) error {
		return q.CreateMembership(ctx, CreateMembershipParams{
			TayfaID: tayfa.PG(), UserID: id.New().PG(), Role: "member", Status: "active",
			JoinedVia: "direct_invite", InvitedByUserID: creator.PG(), JoinedAt: ts(time.Now()),
		})
	})
	wantCode(t, err, CodeRoleHierarchy)

	// Üyenin davetini kabul eden onaylı tayfada onaya düşer; moderatörün
	// davetini kabul eden doğrudan aktif olur.
	wantCode(t, as(t, pool, byMember, func(q *Queries) error { return accept("active")(q, tayfa, byMember) }), CodeRoleHierarchy)
	wantCode(t, as(t, pool, byMember, func(q *Queries) error { return accept("pending")(q, tayfa, byMember) }), "")
	wantCode(t, as(t, pool, byMod, func(q *Queries) error { return accept("active")(q, tayfa, byMod) }), "")

	// Onay: moderatör, onaylayan olarak kendini yazar.
	wantCode(t, as(t, pool, mod, func(q *Queries) error {
		return mustAffect(q.ApproveMembership(ctx, ApproveMembershipParams{TayfaID: tayfa.PG(), UserID: byMember.PG(), ApprovedBy: creator.PG()}))
	}), CodeRoleHierarchy)
	wantCode(t, as(t, pool, member, func(q *Queries) error {
		return mustAffect(q.ApproveMembership(ctx, ApproveMembershipParams{TayfaID: tayfa.PG(), UserID: byMember.PG(), ApprovedBy: member.PG()}))
	}), CodeRoleHierarchy)
	wantCode(t, as(t, pool, mod, func(q *Queries) error {
		return mustAffect(q.ApproveMembership(ctx, ApproveMembershipParams{TayfaID: tayfa.PG(), UserID: byMember.PG(), ApprovedBy: mod.PG()}))
	}), "")
	m, _ := membership(t, New(pool), tayfa, byMember)
	if m.Status != "active" || m.ApprovedBy != mod.PG() || !m.JoinedAt.Valid {
		t.Fatalf("onaylanan üyelik = %+v", m)
	}

	// Susturan, işlemi yapan kişidir.
	wantCode(t, as(t, pool, mod, func(q *Queries) error {
		return mustAffect(q.MuteMembership(ctx, MuteMembershipParams{TayfaID: tayfa.PG(), UserID: member.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: creator.PG()}))
	}), CodeRoleHierarchy)

	// Kaynak alanları değişmez.
	wantCode(t, as(t, pool, creator, func(q *Queries) error {
		_, err := q.db.Exec(ctx, `UPDATE memberships SET joined_via = 'creator' WHERE tayfa_id = $1 AND user_id = $2`, tayfa.PG(), member.PG())
		return err
	}), CodeRoleHierarchy)
}

func TestJoining(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	creator, mod, member := id.New(), id.New(), id.New()
	approval := newTayfa(t, pool, creator, tayfaOpts{})
	open := newTayfa(t, pool, creator, tayfaOpts{open: true})
	seed(t, pool, approval, mod, "moderator", "active")
	seed(t, pool, approval, member, "member", "active")

	modInvite, memberInvite := id.New(), id.New()
	for inv, by := range map[id.ID]id.ID{modInvite: mod, memberInvite: member} {
		h := sha256.Sum256(inv[:])
		if err := New(pool).CreateInvite(ctx, CreateInviteParams{ID: inv.PG(), TayfaID: approval.PG(), TokenHash: h[:], CreatedBy: by.PG(), ExpiresAt: ts(time.Now().Add(time.Hour))}); err != nil {
			t.Fatal(err)
		}
	}

	type join struct {
		tayfa            id.ID
		role, status     string
		via              string
		invite           id.ID
		self             bool // false: someone else inserts the row
		creatorInsertsAs bool
	}
	cases := []struct {
		name string
		j    join
		want string
	}{
		{"kodla başvuru", join{tayfa: approval, role: "member", status: "pending", via: "code", self: true}, ""},
		{"kodla onaysız aktif olunamaz", join{tayfa: approval, role: "member", status: "active", via: "code", self: true}, CodeRoleHierarchy},
		{"açık tayfaya kodla aktif", join{tayfa: open, role: "member", status: "active", via: "code", self: true}, ""},
		{"açık tayfaya moderatör olarak girilemez", join{tayfa: open, role: "moderator", status: "active", via: "code", self: true}, CodeRoleHierarchy},
		{"moderatörün linkiyle aktif", join{tayfa: approval, role: "member", status: "active", via: "invite_link", invite: modInvite, self: true}, ""},
		{"üyenin linkiyle aktif olunamaz", join{tayfa: approval, role: "member", status: "active", via: "invite_link", invite: memberInvite, self: true}, CodeRoleHierarchy},
		{"üyenin linkiyle başvuru", join{tayfa: approval, role: "member", status: "pending", via: "invite_link", invite: memberInvite, self: true}, ""},
		{"davetsiz invite_link", join{tayfa: approval, role: "member", status: "pending", via: "invite_link", self: true}, CodeRoleHierarchy},
		{"kendini kurucu yapamaz", join{tayfa: approval, role: "admin", status: "active", via: "creator", self: true}, CodeRoleHierarchy},
		{"kurucu ikinci kez eklenemez", join{tayfa: open, role: "admin", status: "active", via: "creator", creatorInsertsAs: true}, CodeRoleHierarchy},
		{"başkası kodla eklenemez", join{tayfa: open, role: "member", status: "active", via: "code"}, CodeRoleHierarchy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := id.New()
			actor := user
			if !tc.j.self {
				actor = creator
			}
			if tc.j.creatorInsertsAs {
				// Creator leaves first so the PK does not mask the rule.
				user, actor = id.New(), id.New()
				var tid = id.New()
				if err := as(t, pool, actor, func(q *Queries) error {
					return q.CreateTayfa(ctx, CreateTayfaParams{ID: tid.PG(), Name: "Kamp Tayfası", NameNormalized: "kamp tayfası", JoinCode: joinCode(t), JoinPolicy: "open", CreatedBy: actor.PG()})
				}); err != nil {
					t.Fatal(err)
				}
				seed(t, pool, tid, user, "member", "active")
				user = actor
				tc.j.tayfa = tid
			}
			var joined pgtype.Timestamptz
			if tc.j.status == "active" {
				joined = ts(time.Now())
			}
			p := CreateMembershipParams{TayfaID: tc.j.tayfa.PG(), UserID: user.PG(), Role: tc.j.role, Status: tc.j.status, JoinedVia: tc.j.via, JoinedAt: joined}
			if tc.j.invite != noActor {
				p.InviteID = tc.j.invite.PG()
			}
			err := as(t, pool, actor, func(q *Queries) error { return q.CreateMembership(ctx, p) })
			wantCode(t, err, tc.want)
		})
	}
}

func TestLastAdminConcurrent(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	a1, a2 := id.New(), id.New()
	tayfa := newTayfa(t, pool, a1, tayfaOpts{})
	seed(t, pool, tayfa, a2, "admin", "active")

	// a1 ayrılır ama henüz commit etmez; a2'nin ayrılması tayfa kilidinde
	// bekler, a1 commit edince son yönetici olduğunu görür.
	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	q1 := New(tx1)
	if err := q1.SetActor(ctx, a1.String()); err != nil {
		t.Fatal(err)
	}
	if err := leave(q1, tayfa, a1); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- as(t, pool, a2, func(q *Queries) error { return leave(q, tayfa, a2) })
	}()
	select {
	case err := <-done:
		t.Fatalf("ikinci ayrılma kilidi beklemedi: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wantCode(t, <-done, CodeLastAdmin)
	if _, ok := membership(t, New(pool), tayfa, a2); !ok {
		t.Fatal("tayfa yöneticisiz kaldı")
	}
}

func TestAccountDeletionJob(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()

	t.Run("halef atanır, sonra son admin silinir", func(t *testing.T) {
		admin, mod, member := id.New(), id.New(), id.New()
		tayfa := newTayfa(t, pool, admin, tayfaOpts{})
		seed(t, pool, tayfa, mod, "moderator", "active")
		seed(t, pool, tayfa, member, "member", "active")
		// Halef olmadan son admin silinemez.
		wantCode(t, asJob(t, pool, SystemJobAccountDeletion, func(q *Queries) error { return leave(q, tayfa, admin) }), CodeLastAdmin)
		wantCode(t, asJob(t, pool, SystemJobAccountDeletion, func(q *Queries) error {
			if err := mustAffect(q.SetMembershipRole(ctx, SetMembershipRoleParams{TayfaID: tayfa.PG(), UserID: mod.PG(), Role: "admin"})); err != nil {
				return err
			}
			return leave(q, tayfa, admin)
		}), "")
		m, _ := membership(t, q, tayfa, mod)
		if m.Role != "admin" {
			t.Fatalf("halef = %+v", m)
		}
	})

	t.Run("tek kişilik tayfadan silinir", func(t *testing.T) {
		admin := id.New()
		tayfa := newTayfa(t, pool, admin, tayfaOpts{})
		wantCode(t, asJob(t, pool, SystemJobAccountDeletion, func(q *Queries) error { return leave(q, tayfa, admin) }), "")
	})

	t.Run("silinen kişinin izleri boşaltılır", func(t *testing.T) {
		admin, mod, member := id.New(), id.New(), id.New()
		tayfa := newTayfa(t, pool, admin, tayfaOpts{})
		seed(t, pool, tayfa, mod, "moderator", "active")
		seed(t, pool, tayfa, member, "member", "active")
		wantCode(t, as(t, pool, mod, func(q *Queries) error {
			return mustAffect(q.MuteMembership(ctx, MuteMembershipParams{TayfaID: tayfa.PG(), UserID: member.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: mod.PG()}))
		}), "")
		wantCode(t, asJob(t, pool, SystemJobAccountDeletion, func(q *Queries) error {
			_, err := q.db.Exec(ctx, `UPDATE memberships SET muted_by = NULL WHERE muted_by = $1`, mod.PG())
			return err
		}), "")
	})

	t.Run("başka bir şey yapamaz", func(t *testing.T) {
		admin, member := id.New(), id.New()
		tayfa := newTayfa(t, pool, admin, tayfaOpts{})
		seed(t, pool, tayfa, member, "member", "active")
		for name, fn := range map[string]func(q *Queries) error{
			"ekleme": func(q *Queries) error {
				return q.CreateMembership(ctx, CreateMembershipParams{TayfaID: tayfa.PG(), UserID: id.New().PG(), Role: "member", Status: "pending", JoinedVia: "code"})
			},
			"ban": func(q *Queries) error {
				return mustAffect(q.BanMembership(ctx, BanMembershipParams{TayfaID: tayfa.PG(), UserID: member.PG()}))
			},
			"moderatör yapma": func(q *Queries) error {
				return mustAffect(q.SetMembershipRole(ctx, SetMembershipRoleParams{TayfaID: tayfa.PG(), UserID: member.PG(), Role: "moderator"}))
			},
			"susturan yazma": func(q *Queries) error {
				return mustAffect(q.MuteMembership(ctx, MuteMembershipParams{TayfaID: tayfa.PG(), UserID: member.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: admin.PG()}))
			},
		} {
			t.Run(name, func(t *testing.T) {
				wantCode(t, asJob(t, pool, SystemJobAccountDeletion, fn), CodeNoActor)
			})
		}
	})

	t.Run("bilinmeyen iş ve aktörle birlikte iş reddedilir", func(t *testing.T) {
		admin, member := id.New(), id.New()
		tayfa := newTayfa(t, pool, admin, tayfaOpts{})
		seed(t, pool, tayfa, member, "member", "active")
		wantCode(t, asJob(t, pool, "cleanup", func(q *Queries) error { return leave(q, tayfa, member) }), CodeNoActor)
		wantCode(t, as(t, pool, admin, func(q *Queries) error {
			if err := q.SetSystemJob(ctx, SystemJobAccountDeletion); err != nil {
				return err
			}
			return leave(q, tayfa, member)
		}), CodeNoActor)
	})
}

func TestTayfaDeletionCascades(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin, member := id.New(), id.New()
	tayfa := newTayfa(t, pool, admin, tayfaOpts{})
	seed(t, pool, tayfa, member, "member", "active")
	inv := id.New()
	h := sha256.Sum256(inv[:])
	if err := q.CreateInvite(ctx, CreateInviteParams{ID: inv.PG(), TayfaID: tayfa.PG(), TokenHash: h[:], CreatedBy: admin.PG(), ExpiresAt: ts(time.Now().Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateAuditLog(ctx, CreateAuditLogParams{ID: id.New().PG(), TayfaID: tayfa.PG(), ActorID: admin.PG(), Action: "tayfa.created", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	// Kalıcı silme işi (aktörsüz, core_app) tayfayı ve ona ait her şeyi
	// siler. Cascade tablo sahibinin yetkisiyle çalışır: core_app'in
	// audit_logs'u silmesi için ayrıca yetkiye gerek yoktur.
	if _, err := pool.Exec(ctx, `DELETE FROM tayfas WHERE id = $1`, tayfa.PG()); err != nil {
		t.Fatalf("tayfa silinemedi: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM memberships WHERE tayfa_id = $1)
		     + (SELECT count(*) FROM invites WHERE tayfa_id = $1)
		     + (SELECT count(*) FROM audit_logs WHERE tayfa_id = $1)`, tayfa.PG()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("tayfa silindikten sonra %d kayıt kaldı", n)
	}
}

func TestSoftDeletedTayfaSkipsLastAdmin(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	admin := id.New()
	tayfa := newTayfa(t, pool, admin, tayfaOpts{})
	if _, err := pool.Exec(ctx, `UPDATE tayfas SET deleted_at = now() WHERE id = $1`, tayfa.PG()); err != nil {
		t.Fatal(err)
	}
	wantCode(t, as(t, pool, admin, func(q *Queries) error { return leave(q, tayfa, admin) }), "")
}

func TestInviteDeletionKeepsMembership(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin, joiner := id.New(), id.New()
	tayfa := newTayfa(t, pool, admin, tayfaOpts{})
	inv := id.New()
	h := sha256.Sum256(inv[:])
	if err := q.CreateInvite(ctx, CreateInviteParams{ID: inv.PG(), TayfaID: tayfa.PG(), TokenHash: h[:], CreatedBy: admin.PG(), ExpiresAt: ts(time.Now().Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, as(t, pool, joiner, func(q *Queries) error {
		return q.CreateMembership(ctx, CreateMembershipParams{TayfaID: tayfa.PG(), UserID: joiner.PG(), Role: "member", Status: "active", JoinedVia: "invite_link", InviteID: inv.PG(), JoinedAt: ts(time.Now())})
	}), "")

	rows, err := q.ListMemberships(ctx, ListMembershipsParams{TayfaID: tayfa.PG(), Status: "active", AfterUserID: pgtype.UUID{Valid: true}, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		if r.UserID == joiner.PG() {
			found = r.InviteCreatedBy == admin.PG()
		}
	}
	if !found {
		t.Fatalf("davet kaynağı listelenmedi: %+v", rows)
	}

	// Başka tayfanın daveti bağlanamaz (bileşik FK).
	other := newTayfa(t, pool, id.New(), tayfaOpts{open: true})
	stranger := id.New()
	err = as(t, pool, stranger, func(q *Queries) error {
		return q.CreateMembership(ctx, CreateMembershipParams{TayfaID: other.PG(), UserID: stranger.PG(), Role: "member", Status: "active", JoinedVia: "invite_link", InviteID: inv.PG(), JoinedAt: ts(time.Now())})
	})
	if constraintOf(err) != "memberships_invite_fkey" {
		t.Fatalf("başka tayfanın daveti kabul edildi: %v", err)
	}

	// Davet dururken aktörsüz invite_id boşaltılamaz; istisna yalnızca
	// davetin silinmesidir.
	_, err = pool.Exec(ctx, `UPDATE memberships SET invite_id = NULL WHERE tayfa_id = $1 AND user_id = $2`, tayfa.PG(), joiner.PG())
	wantCode(t, err, CodeNoActor)

	// Davet silinince (aktörsüz) üyelik kalır, invite_id boşalır. core_app
	// davet silemez (davetler iptal edilir); silme yönetici bağlantısıyla.
	if _, err := adminOf(t, pool).Exec(ctx, `DELETE FROM invites WHERE id = $1`, inv.PG()); err != nil {
		t.Fatalf("davet silinemedi: %v", err)
	}
	m, ok := membership(t, q, tayfa, joiner)
	if !ok || m.InviteID.Valid {
		t.Fatalf("üyelik = %+v, var=%v", m, ok)
	}
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin := id.New()
	tayfa := newTayfa(t, pool, admin, tayfaOpts{})
	logID := id.New()
	if err := q.CreateAuditLog(ctx, CreateAuditLogParams{
		ID: logID.PG(), TayfaID: tayfa.PG(), ActorID: admin.PG(), Action: "member.banned",
		TargetType: text("membership"), TargetID: id.New().PG(), Metadata: []byte(`{"reason":"spam"}`),
	}); err != nil {
		t.Fatal(err)
	}

	// core_app'in audit_logs'ta yalnızca UPDATE (actor_id) ve DELETE yetkisi
	// vardır (hesap silme ve 1 yıllık saklama, 00013); geri kalanı yetki
	// düzeyinde reddedilir.
	t.Run("güncelleme", func(t *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE audit_logs SET action = 'member.unbanned' WHERE id = $1`, logID.PG())
		wantCode(t, err, "42501")
	})
	t.Run("truncate", func(t *testing.T) {
		_, err := pool.Exec(ctx, `TRUNCATE audit_logs`)
		wantCode(t, err, "42501")
	})
	// Verilen dar yetkilerin sınırı trigger'dadır: iş dışında ya da genç bir
	// kayıtta core_app de değiştiremez.
	for name, sql := range map[string]string{
		"işsiz aktörü silme":  `UPDATE audit_logs SET actor_id = NULL WHERE id = $1`,
		"aktörü değiştirme":   `UPDATE audit_logs SET actor_id = gen_random_uuid() WHERE id = $1`,
		"saklama işsiz silme": `DELETE FROM audit_logs WHERE id = $1`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(ctx, sql, logID.PG())
			wantCode(t, err, CodeAppendOnly)
		})
	}
	t.Run("saklama işi genç kaydı silemez", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := New(tx).SetAuditRetentionJob(ctx); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `DELETE FROM audit_logs WHERE id = $1`, logID.PG())
		wantCode(t, err, CodeAppendOnly)
	})
	t.Run("hesap silme işi aktörü başka değere çeviremez", func(t *testing.T) {
		wantCode(t, asJob(t, pool, SystemJobAccountDeletion, func(q *Queries) error {
			_, err := q.db.Exec(ctx, `UPDATE audit_logs SET actor_id = gen_random_uuid() WHERE id = $1`, logID.PG())
			return err
		}), CodeAppendOnly)
	})

	// Trigger ikinci savunma hattıdır: tüm yetkilere sahip bir bağlantı
	// (burada yönetici) da kaydı değiştiremez.
	priv := adminOf(t, pool)
	for name, sql := range map[string]string{
		"yönetici güncelleme":   `UPDATE audit_logs SET action = 'member.unbanned' WHERE id = $1`,
		"yönetici aktörü silme": `UPDATE audit_logs SET actor_id = NULL WHERE id = $1`,
		"yönetici silme":        `DELETE FROM audit_logs WHERE id = $1`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := priv.Exec(ctx, sql, logID.PG())
			wantCode(t, err, CodeAppendOnly)
		})
	}
	t.Run("yönetici truncate", func(t *testing.T) {
		_, err := priv.Exec(ctx, `TRUNCATE audit_logs`)
		wantCode(t, err, CodeAppendOnly)
	})
	t.Run("hesap silme işi başka alan değiştiremez", func(t *testing.T) {
		wantCode(t, asJob(t, priv, SystemJobAccountDeletion, func(q *Queries) error {
			_, err := q.db.Exec(ctx, `UPDATE audit_logs SET actor_id = NULL, action = 'x.y' WHERE id = $1`, logID.PG())
			return err
		}), CodeAppendOnly)
	})
	t.Run("hesap silme işi aktörü boşaltır", func(t *testing.T) {
		wantCode(t, asJob(t, pool, SystemJobAccountDeletion, func(q *Queries) error {
			return q.ClearAuditActor(ctx, admin.PG())
		}), "")
	})

	logs, err := q.ListAuditLogs(ctx, ListAuditLogsParams{TayfaID: tayfa.PG(), BeforeCreatedAt: ts(time.Now().Add(time.Hour)), BeforeID: id.New().PG(), PageSize: 10})
	if err != nil || len(logs) != 1 || logs[0].Action != "member.banned" || logs[0].ActorID.Valid {
		t.Fatalf("ListAuditLogs = %+v, %v", logs, err)
	}
}

func TestConstraints(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin := id.New()
	tayfa := newTayfa(t, pool, admin, tayfaOpts{})
	inv := id.New()
	h := sha256.Sum256(inv[:])
	if err := q.CreateInvite(ctx, CreateInviteParams{ID: inv.PG(), TayfaID: tayfa.PG(), TokenHash: h[:], CreatedBy: admin.PG(), ExpiresAt: ts(time.Now().Add(time.Hour)), MaxUses: pgtype.Int4{Int32: 1, Valid: true}}); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		sql, constraint string
	}{
		"2 karakter ad":           {`UPDATE tayfas SET name = 'ab' WHERE id = $1`, "tayfas_name_len"},
		"51 karakter ad":          {`UPDATE tayfas SET name = repeat('ğ', 51) WHERE id = $1`, "tayfas_name_len"},
		"501 karakter açıklama":   {`UPDATE tayfas SET description = repeat('ş', 501) WHERE id = $1`, "tayfas_description_len"},
		"kod küçük harf":          {`UPDATE tayfas SET join_code = 'abcdefgh' WHERE id = $1`, "tayfas_join_code_format"},
		"kodda O ve 0":            {`UPDATE tayfas SET join_code = 'ABCD0OXY' WHERE id = $1`, "tayfas_join_code_format"},
		"kodda I ve 1":            {`UPDATE tayfas SET join_code = 'ABCDI1XY' WHERE id = $1`, "tayfas_join_code_format"},
		"7 karakter kod":          {`UPDATE tayfas SET join_code = 'ABCDEFG' WHERE id = $1`, "tayfas_join_code_format"},
		"bilinmeyen durum":        {`UPDATE tayfas SET status = 'deleted' WHERE id = $1`, "tayfas_status"},
		"uyarısız warned":         {`UPDATE tayfas SET status = 'warned' WHERE id = $1`, "tayfas_status_timestamps"},
		"aktifte arşiv izi":       {`UPDATE tayfas SET archived_at = now() WHERE id = $1`, "tayfas_status_timestamps"},
		"arşivsiz archived":       {`UPDATE tayfas SET status = 'archived', warned_at = now() WHERE id = $1`, "tayfas_status_timestamps"},
		"bilinmeyen görünürlük":   {`UPDATE tayfas SET visibility = 'public' WHERE id = $1`, "tayfas_visibility"},
		"bilinmeyen katılım":      {`UPDATE tayfas SET join_policy = 'invite' WHERE id = $1`, "tayfas_join_policy"},
		"ilsiz ilçe":              {`UPDATE tayfas SET district = 'Karşıyaka' WHERE id = $1`, "tayfas_district_needs_city"},
		"nedensiz askı":           {`UPDATE tayfas SET suspended_at = now() WHERE id = $1`, "tayfas_suspension"},
		"planlanmamış silme":      {`UPDATE tayfas SET deletion_requested_at = now() WHERE id = $1`, "tayfas_deletion_request"},
		"geçmişe planlanan silme": {`UPDATE tayfas SET deletion_requested_at = now(), deletion_scheduled_for = now() - interval '1 hour' WHERE id = $1`, "tayfas_deletion_request"},
		"silinmeden silen":        {`UPDATE tayfas SET deleted_by = created_by WHERE id = $1`, "tayfas_deleted_by"},
		"bilinmeyen ilgi alanı":   {`INSERT INTO tayfa_interests (tayfa_id, event_type) VALUES ($1, 'cycling')`, "tayfa_interests_event_type"},
		"bilinmeyen modül":        {`INSERT INTO tayfa_modules (tayfa_id, module, enabled) VALUES ($1, 'garage', true)`, "tayfa_modules_module"},
		"sohbet modül değildir":   {`INSERT INTO tayfa_modules (tayfa_id, module, enabled) VALUES ($1, 'channels', true)`, "tayfa_modules_module"},
		"kısa davet hash'i":       {`INSERT INTO invites (id, tayfa_id, token_hash, expires_at) VALUES (gen_random_uuid(), $1, '\x01'::bytea, now() + interval '1 day')`, "invites_token_hash_len"},
		"geçmişte biten davet":    {`INSERT INTO invites (id, tayfa_id, token_hash, expires_at) VALUES (gen_random_uuid(), $1, sha256('x'::bytea), now() - interval '1 day')`, "invites_expiry"},
		"sıfır kullanımlık davet": {`INSERT INTO invites (id, tayfa_id, token_hash, expires_at, max_uses) VALUES (gen_random_uuid(), $1, sha256('y'::bytea), now() + interval '1 day', 0)`, "invites_max_uses"},
		"sınırı aşan kullanım":    {`UPDATE invites SET use_count = 2 WHERE tayfa_id = $1`, "invites_use_count"},
		"bozuk audit eylemi":      {`INSERT INTO audit_logs (id, tayfa_id, action) VALUES (gen_random_uuid(), $1, 'Ban')`, "audit_logs_action_format"},
		"hedefsiz hedef türü":     {`INSERT INTO audit_logs (id, tayfa_id, action, target_type) VALUES (gen_random_uuid(), $1, 'member.banned', 'membership')`, "audit_logs_target"},
		"dizi metadata":           {`INSERT INTO audit_logs (id, tayfa_id, action, metadata) VALUES (gen_random_uuid(), $1, 'member.banned', '[]')`, "audit_logs_metadata"},
		"büyük olay payload'ı":    {`INSERT INTO domain_events (id, type, tayfa_id, payload) VALUES (gen_random_uuid(), 'tayfa.created', $1, jsonb_build_object('x', repeat('a', 9000)))`, "domain_events_payload"},
		"bozuk olay türü":         {`INSERT INTO domain_events (id, type, tayfa_id) VALUES (gen_random_uuid(), 'created', $1)`, "domain_events_type_format"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql, tayfa.PG())
			if got := constraintOf(err); got != tc.constraint {
				t.Fatalf("kısıt = %q (err=%v), %q bekleniyordu", got, err, tc.constraint)
			}
		})
	}

	// Üyelik kısıtları trigger'dan önce değil, satır yazılırken denetlenir;
	// tetikleyicisiz yazımla doğrudan sınanır.
	for name, tc := range map[string]struct {
		role, status, joinedAt, constraint string
	}{
		"bilinmeyen rol":                {"owner", "active", "now()", "memberships_role"},
		"bilinmeyen durum":              {"member", "left", "now()", "memberships_status"},
		"başvuranın rolü olamaz":        {"moderator", "pending", "NULL", "memberships_role_before_join"},
		"aktifin katılım zamanı var":    {"member", "active", "NULL", "memberships_joined_at"},
		"davetlinin katılım zamanı yok": {"member", "invited", "now()", "memberships_joined_at"},
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := adminOf(t, pool).Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at)
				VALUES ($1, gen_random_uuid(), $2, $3, 'code', `+tc.joinedAt+`)`, tayfa.PG(), tc.role, tc.status)
			if got := constraintOf(err); got != tc.constraint {
				t.Fatalf("kısıt = %q (err=%v), %q bekleniyordu", got, err, tc.constraint)
			}
		})
	}

	// 50 karakterlik Türkçe ad kabul edilir (bayt değil karakter sayılır).
	if _, err := pool.Exec(ctx, `UPDATE tayfas SET name = repeat('ğ', 50), name_normalized = repeat('ğ', 50) WHERE id = $1`, tayfa.PG()); err != nil {
		t.Fatalf("50 karakter reddedildi: %v", err)
	}
	// Uyarı → arşiv geçişi tutarlıdır.
	if _, err := pool.Exec(ctx, `UPDATE tayfas SET status = 'warned', warned_at = now() WHERE id = $1`, tayfa.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tayfas SET status = 'archived', archived_at = now() WHERE id = $1`, tayfa.PG()); err != nil {
		t.Fatal(err)
	}
}

func TestJoinCodeAndClientRequestUnique(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	user := id.New()
	req := id.New()
	code := joinCode(t)
	p := CreateTayfaParams{ID: id.New().PG(), Name: "Yürüyüş Ekibi", NameNormalized: "yürüyüş ekibi", JoinCode: code, JoinPolicy: "approval", CreatedBy: user.PG(), ClientRequestID: req.PG()}
	if err := q.CreateTayfa(ctx, p); err != nil {
		t.Fatal(err)
	}

	p2 := p
	p2.ID, p2.ClientRequestID = id.New().PG(), id.New().PG()
	if err := q.CreateTayfa(ctx, p2); constraintOf(err) != "tayfas_join_code_key" {
		t.Fatalf("aynı katılım kodu kabul edildi: %v", err)
	}
	p3 := p
	p3.ID, p3.JoinCode = id.New().PG(), joinCode(t)
	if err := q.CreateTayfa(ctx, p3); constraintOf(err) != "tayfas_client_request_key" {
		t.Fatalf("aynı istek iki tayfa açtı: %v", err)
	}
	got, err := q.GetTayfaByClientRequest(ctx, GetTayfaByClientRequestParams{CreatedBy: user.PG(), ClientRequestID: req.PG()})
	if err != nil || got != p.ID {
		t.Fatalf("GetTayfaByClientRequest = %v, %v", got, err)
	}
	// Başka kullanıcının aynı istek kimliği çakışmaz.
	p4 := p
	p4.ID, p4.JoinCode, p4.CreatedBy = id.New().PG(), joinCode(t), id.New().PG()
	if err := q.CreateTayfa(ctx, p4); err != nil {
		t.Fatalf("başka kullanıcının istek kimliği reddedildi: %v", err)
	}

	byCode, err := q.GetTayfaByJoinCode(ctx, code)
	if err != nil || byCode.ID != p.ID {
		t.Fatalf("GetTayfaByJoinCode = %+v, %v", byCode, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tayfas SET deleted_at = now() WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetTayfaByJoinCode(ctx, code); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("silinen tayfa kodla bulundu: %v", err)
	}
	if _, err := q.GetTayfa(ctx, p.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("silinen tayfa döndü: %v", err)
	}

	// Silinen tayfa da günlük sınıra sayılır.
	n, err := q.CountTayfasCreatedSince(ctx, CountTayfasCreatedSinceParams{CreatedBy: user.PG(), Since: ts(time.Now().Add(-24 * time.Hour))})
	if err != nil || n != 1 {
		t.Fatalf("CountTayfasCreatedSince = %d, %v", n, err)
	}
}

func TestInviteUse(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin := id.New()
	tayfa := newTayfa(t, pool, admin, tayfaOpts{})

	newInvite := func(maxUses int32, expires time.Time) (id.ID, []byte) {
		inv := id.New()
		h := sha256.Sum256(inv[:])
		p := CreateInviteParams{ID: inv.PG(), TayfaID: tayfa.PG(), TokenHash: h[:], CreatedBy: admin.PG(), ExpiresAt: ts(expires)}
		if maxUses > 0 {
			p.MaxUses = pgtype.Int4{Int32: maxUses, Valid: true}
		}
		if err := q.CreateInvite(ctx, p); err != nil {
			t.Fatal(err)
		}
		return inv, h[:]
	}

	inv, hash := newInvite(2, time.Now().Add(time.Hour))
	// Aynı hash ikinci bir davete verilemez.
	if err := q.CreateInvite(ctx, CreateInviteParams{ID: id.New().PG(), TayfaID: tayfa.PG(), TokenHash: hash, ExpiresAt: ts(time.Now().Add(time.Hour))}); constraintOf(err) != "invites_token_hash_key" {
		t.Fatalf("aynı davet hash'i kabul edildi: %v", err)
	}

	use := func(h []byte) int64 {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		qtx := q.WithTx(tx)
		row, err := qtx.LockInviteByHash(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		n, err := qtx.IncrementInviteUse(ctx, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// max_uses = 2: iki kullanım sayılır, üçüncüsü reddedilir.
	if got := []int64{use(hash), use(hash), use(hash)}; got[0] != 1 || got[1] != 1 || got[2] != 0 {
		t.Fatalf("kullanım sınırı uygulanmadı: %v", got)
	}

	_, expiredHash := newInvite(0, time.Now().Add(time.Second))
	if _, err := pool.Exec(ctx, `UPDATE invites SET expires_at = created_at + interval '1 microsecond' WHERE token_hash = $1`, expiredHash); err != nil {
		t.Fatal(err)
	}
	if use(expiredHash) != 0 {
		t.Fatal("süresi dolmuş davet kullanıldı")
	}

	revoked, revokedHash := newInvite(0, time.Now().Add(time.Hour))
	if n, err := q.RevokeInvite(ctx, RevokeInviteParams{TayfaID: id.New().PG(), ID: revoked.PG()}); err != nil || n != 0 {
		t.Fatalf("davet başka tayfa üzerinden iptal edildi: n=%d err=%v", n, err)
	}
	if n, err := q.RevokeInvite(ctx, RevokeInviteParams{TayfaID: tayfa.PG(), ID: revoked.PG()}); err != nil || n != 1 {
		t.Fatalf("RevokeInvite: n=%d err=%v", n, err)
	}
	if use(revokedHash) != 0 {
		t.Fatal("iptal edilmiş davet kullanıldı")
	}

	list, err := q.ListInvites(ctx, ListInvitesParams{TayfaID: tayfa.PG(), PageSize: 10})
	if err != nil || len(list) != 3 {
		t.Fatalf("ListInvites = %d, %v", len(list), err)
	}
	_ = inv
}

// Issue #93: invites created at the same instant come back in a stable
// order (id DESC as the tie-breaker), so a page boundary never shuffles them.
func TestListInvitesTieOrder(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin := id.New()
	tayfa := newTayfa(t, pool, admin, tayfaOpts{})

	ids := make([]id.ID, 5)
	for i := range ids {
		ids[i] = id.New()
		h := sha256.Sum256(ids[i][:])
		if err := q.CreateInvite(ctx, CreateInviteParams{ID: ids[i].PG(), TayfaID: tayfa.PG(), TokenHash: h[:], CreatedBy: admin.PG(), ExpiresAt: ts(time.Now().Add(time.Hour))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE invites SET created_at = '2026-01-01T00:00:00Z' WHERE tayfa_id = $1`, tayfa.PG()); err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(ids)
	slices.SortFunc(want, func(a, b id.ID) int { return bytes.Compare(b[:], a[:]) })

	for _, size := range []int32{int32(len(ids)), 2} {
		rows, err := q.ListInvites(ctx, ListInvitesParams{TayfaID: tayfa.PG(), PageSize: size})
		if err != nil {
			t.Fatal(err)
		}
		got := make([]id.ID, len(rows))
		for i, r := range rows {
			got[i] = id.ID(r.ID.Bytes)
		}
		if !slices.Equal(got, want[:size]) {
			t.Fatalf("ListInvites(page_size=%d) sırası = %v, beklenen %v", size, got, want[:size])
		}
	}
}

func TestListUserActiveTayfas(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	user := id.New()
	mine := newTayfa(t, pool, user, tayfaOpts{})
	joined := newTayfa(t, pool, id.New(), tayfaOpts{})
	seed(t, pool, joined, user, "member", "active")
	pending := newTayfa(t, pool, id.New(), tayfaOpts{})
	seed(t, pool, pending, user, "member", "pending")
	deleted := newTayfa(t, pool, id.New(), tayfaOpts{})
	seed(t, pool, deleted, user, "moderator", "active")
	if _, err := pool.Exec(ctx, `UPDATE tayfas SET deleted_at = now() WHERE id = $1`, deleted.PG()); err != nil {
		t.Fatal(err)
	}

	rows, err := q.ListUserActiveTayfas(ctx, ListUserActiveTayfasParams{UserID: user.PG(), AfterID: pgtype.UUID{Valid: true}, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := map[pgtype.UUID]string{}
	for _, r := range rows {
		got[r.ID] = r.Role
	}
	if len(got) != 2 || got[mine.PG()] != "admin" || got[joined.PG()] != "member" {
		t.Fatalf("ListUserActiveTayfas = %+v", rows)
	}

	// Sayfalama: ilk sayfadan sonrası.
	first, _ := q.ListUserActiveTayfas(ctx, ListUserActiveTayfasParams{UserID: user.PG(), AfterID: pgtype.UUID{Valid: true}, PageSize: 1})
	rest, _ := q.ListUserActiveTayfas(ctx, ListUserActiveTayfasParams{UserID: user.PG(), AfterID: first[0].ID, PageSize: 10})
	if len(first) != 1 || len(rest) != 1 || rest[0].ID == first[0].ID {
		t.Fatalf("sayfalama: %+v / %+v", first, rest)
	}

	if n, err := q.CountActiveMembers(ctx, joined.PG()); err != nil || n != 2 {
		t.Fatalf("CountActiveMembers = %d, %v", n, err)
	}
}

func TestModulesAndInterests(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	tayfa := newTayfa(t, pool, id.New(), tayfaOpts{})
	for _, e := range []string{"camp", "hike", "camp"} {
		if err := q.AddTayfaInterest(ctx, AddTayfaInterestParams{TayfaID: tayfa.PG(), EventType: e}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := q.ListTayfaInterests(ctx, tayfa.PG()); len(got) != 2 {
		t.Fatalf("ilgi alanları = %v", got)
	}
	if err := q.DeleteTayfaInterests(ctx, tayfa.PG()); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.ListTayfaInterests(ctx, tayfa.PG()); len(got) != 0 {
		t.Fatalf("ilgi alanları silinmedi: %v", got)
	}
	for _, en := range []bool{true, false} {
		if err := q.SetTayfaModule(ctx, SetTayfaModuleParams{TayfaID: tayfa.PG(), Module: "polls", Enabled: en}); err != nil {
			t.Fatal(err)
		}
	}
	mods, err := q.ListTayfaModules(ctx, tayfa.PG())
	if err != nil || len(mods) != 1 || mods[0].Enabled {
		t.Fatalf("modüller = %+v, %v", mods, err)
	}
	for module, want := range map[string]bool{"polls": false, "events": false, "qa": false} {
		got, err := q.IsTayfaModuleEnabled(ctx, IsTayfaModuleEnabledParams{TayfaID: tayfa.PG(), Module: module})
		if err != nil || got != want {
			t.Fatalf("%s açık = %v, %v", module, got, err)
		}
	}
	if err := q.SetTayfaModule(ctx, SetTayfaModuleParams{TayfaID: tayfa.PG(), Module: "events", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := q.IsTayfaModuleEnabled(ctx, IsTayfaModuleEnabledParams{TayfaID: tayfa.PG(), Module: "events"}); err != nil || !got {
		t.Fatalf("events açık değil: %v", err)
	}
	if got, err := q.IsTayfaModuleEnabled(ctx, IsTayfaModuleEnabledParams{TayfaID: id.New().PG(), Module: "events"}); err != nil || got {
		t.Fatalf("başka tayfanın modülü: %v %v", got, err)
	}
}
