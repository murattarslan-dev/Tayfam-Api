package channelaccess

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

type fixture struct {
	svc                                      *Service
	tayfa                                    id.ID
	admin, admin2, mod, mod2, member, muted  id.ID
	pending, banned, nonMember, formerMember id.ID
	as                                       func(actor id.ID, fn func(q *store.Queries) error)
	pool                                     *pgxpool.Pool
	// join adds another active plain member.
	join func(t *testing.T) id.ID
}

func setup(t *testing.T) fixture {
	t.Helper()
	pool := dbtest.New(t, "core", coredb.Migrations())
	svc, err := New(Config{Pool: pool})
	must(t, err)
	tayfas, err := tayfa.New(tayfa.Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	f := fixture{svc: svc, admin: id.New(), nonMember: id.New(), pool: pool}
	f.as = func(actor id.ID, fn func(q *store.Queries) error) {
		t.Helper()
		must(t, txn.WithActor(ctx, pool, actor, func(_ pgx.Tx, q *store.Queries) error { return fn(q) }))
	}
	v, err := tayfas.Create(ctx, f.admin, tayfa.CreateInput{Name: "Sohbet Tayfası", JoinPolicy: tayfa.JoinOpen, ClientRequestID: id.New().String()})
	must(t, err)
	f.tayfa = v.ID
	join := func(st authz.Status, role authz.Role) id.ID {
		u := id.New()
		joined := pgtype.Timestamptz{}
		if st == authz.StatusActive {
			joined = ts(time.Now())
		}
		f.as(u, func(q *store.Queries) error {
			return q.CreateMembership(ctx, store.CreateMembershipParams{
				TayfaID: f.tayfa.PG(), UserID: u.PG(), Role: "member", Status: string(st), JoinedVia: "code", JoinedAt: joined,
			})
		})
		if role != authz.Member {
			f.as(f.admin, func(q *store.Queries) error {
				_, err := q.SetMembershipRole(ctx, store.SetMembershipRoleParams{TayfaID: f.tayfa.PG(), UserID: u.PG(), Role: role.String()})
				return err
			})
		}
		return u
	}
	f.admin2 = join(authz.StatusActive, authz.Admin)
	f.mod = join(authz.StatusActive, authz.Moderator)
	f.mod2 = join(authz.StatusActive, authz.Moderator)
	f.member = join(authz.StatusActive, authz.Member)
	f.muted = join(authz.StatusActive, authz.Member)
	f.as(f.admin, func(q *store.Queries) error {
		_, err := q.MuteMembership(ctx, store.MuteMembershipParams{TayfaID: f.tayfa.PG(), UserID: f.muted.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: f.admin.PG()})
		return err
	})
	f.pending = join(authz.StatusPending, authz.Member)
	f.banned = join(authz.StatusActive, authz.Member)
	f.as(f.admin, func(q *store.Queries) error {
		_, err := q.BanMembership(ctx, store.BanMembershipParams{TayfaID: f.tayfa.PG(), UserID: f.banned.PG()})
		return err
	})
	f.formerMember = id.New() // never joined: content of someone no longer here
	f.join = func(*testing.T) id.ID { return join(authz.StatusActive, authz.Member) }
	return f
}

func TestNonMembersGetTayfaNotFound(t *testing.T) {
	f := setup(t)
	for _, u := range []id.ID{f.nonMember, f.pending, f.banned} {
		if _, err := f.svc.Authorize(ctx, u, f.tayfa, authz.ChannelView, id.ID{}); !errors.Is(err, apperr.ErrTayfaNotFound) {
			t.Errorf("üye olmayan: %v", err)
		}
	}
	if _, err := f.svc.Authorize(ctx, f.admin, id.New(), authz.ChannelView, id.ID{}); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Errorf("olmayan tayfa: %v", err)
	}
}

func TestValidation(t *testing.T) {
	f := setup(t)
	var v *apperr.ValidationError
	if _, err := f.svc.Authorize(ctx, f.admin, f.tayfa, authz.TayfaView, id.ID{}); !errors.As(err, &v) || v.Field != "action" {
		t.Errorf("kanal dışı işlem: %v", err)
	}
	// A target only with moderate and restore: no probing of roles.
	if _, err := f.svc.Authorize(ctx, f.member, f.tayfa, authz.ChannelView, f.admin); !errors.As(err, &v) || v.Field != "target_user_id" {
		t.Errorf("hedefli okuma: %v", err)
	}
}

func TestDecisions(t *testing.T) {
	f := setup(t)
	d, err := f.svc.Authorize(ctx, f.muted, f.tayfa, authz.ChannelPost, id.ID{})
	must(t, err)
	if d.Denial != DenialMuted || d.Role != authz.Member || d.MutedUntil.IsZero() || d.ReadOnly {
		t.Fatalf("susturulmuş = %+v", d)
	}
	if d.All[authz.ChannelView] != DenialNone || d.All[authz.ChannelDeleteOwn] != DenialNone || d.All[authz.ChannelViewStaff] != DenialRole ||
		len(d.All) != len(Actions) {
		t.Fatalf("susturulmuş tüm kararlar = %v", d.All)
	}
	d, err = f.svc.Authorize(ctx, f.member, f.tayfa, authz.ChannelManage, id.ID{})
	must(t, err)
	if d.Denial != DenialRole || d.All[authz.ChannelPost] != DenialNone {
		t.Fatalf("üye = %+v", d)
	}
}

func TestHierarchy(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct {
		name          string
		actor, target id.ID
		action        authz.Action
		want          Denial
	}{
		{"moderatör üyeyi gizler", f.mod, f.member, authz.ChannelModerate, DenialNone},
		{"moderatör moderatörü gizleyemez", f.mod, f.mod2, authz.ChannelModerate, DenialHierarchy},
		{"moderatör yöneticiyi gizleyemez", f.mod, f.admin, authz.ChannelModerate, DenialHierarchy},
		{"yönetici moderatörü gizler", f.admin, f.mod, authz.ChannelModerate, DenialNone},
		{"yönetici yöneticiyi gizleyemez", f.admin, f.admin2, authz.ChannelModerate, DenialHierarchy},
		{"ayrılmış üyenin içeriği tayfanındır", f.mod, f.formerMember, authz.ChannelModerate, DenialNone},
		{"banlının içeriği tayfanındır", f.mod, f.banned, authz.ChannelModerate, DenialNone},
		{"yönetici moderatörün mesajını geri alır", f.admin, f.mod, authz.ChannelRestore, DenialNone},
		{"yönetici yöneticinin mesajını geri alamaz", f.admin, f.admin2, authz.ChannelRestore, DenialHierarchy},
		{"üye hiç gizleyemez", f.member, f.formerMember, authz.ChannelModerate, DenialRole},
	} {
		d, err := f.svc.Authorize(ctx, tc.actor, f.tayfa, tc.action, tc.target)
		if err != nil || d.Denial != tc.want {
			t.Errorf("%s: %v %v", tc.name, d.Denial, err)
		}
	}
}

func TestReadOnlyTayfa(t *testing.T) {
	f := setup(t)
	f.as(f.admin, func(q *store.Queries) error {
		_, err := q.RequestTayfaDeletion(ctx, store.RequestTayfaDeletionParams{ID: f.tayfa.PG(), RequestedAt: ts(time.Now()), RequestedBy: f.admin.PG(), ScheduledFor: ts(time.Now().Add(72 * time.Hour))})
		return err
	})
	d, err := f.svc.Authorize(ctx, f.admin, f.tayfa, authz.ChannelPost, id.ID{})
	must(t, err)
	if !d.ReadOnly || d.Denial != DenialReadOnly {
		t.Fatalf("salt okunur gönderim = %+v", d)
	}
	for a, want := range map[authz.Action]Denial{
		authz.ChannelView: DenialNone, authz.ChannelViewStaff: DenialNone, authz.ChannelMarkRead: DenialNone,
		authz.ChannelSkipSlowMode: DenialNone,
		authz.ChannelManage:       DenialReadOnly, authz.ChannelDeleteOwn: DenialReadOnly, authz.ChannelModerate: DenialReadOnly, authz.ChannelRestore: DenialReadOnly,
	} {
		if d.All[a] != want {
			t.Errorf("%s: %v", a, d.All[a])
		}
	}
	// A role that is too low anyway is told about the role, not the state.
	d, err = f.svc.Authorize(ctx, f.member, f.tayfa, authz.ChannelManage, id.ID{})
	must(t, err)
	if d.Denial != DenialRole {
		t.Fatalf("üye yönetim = %v", d.Denial)
	}
}
