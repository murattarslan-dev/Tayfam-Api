package membership

import (
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// profile gives u a profile copy, as identity's event would.
func (e *env) profile(t *testing.T, u id.ID) {
	t.Helper()
	e.exec(t, `INSERT INTO user_profiles (user_id, display_name, source_event_id) VALUES ($1, 'Kişi', $2) ON CONFLICT DO NOTHING`, u.PG(), id.New().PG())
}

// raw runs sql with the memberships trigger off, to set up a state.
func (e *env) raw(t *testing.T, sql string, args ...any) {
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

// friends makes a and b friends (both get profiles).
func (e *env) friends(t *testing.T, a, b id.ID) {
	t.Helper()
	e.profile(t, a)
	e.profile(t, b)
	low, high := social.Pair(a, b)
	e.exec(t, `INSERT INTO friendships (user_low_id, user_high_id, status, requested_by, accepted_at) VALUES ($1, $2, 'accepted', $1, now())`, low.PG(), high.PG())
}

// friend returns a new user who is inviter's friend.
func (e *env) friend(t *testing.T, inviter id.ID) id.ID {
	t.Helper()
	u := id.New()
	e.friends(t, inviter, u)
	return u
}

func TestInviteFriend(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "approval")
	member := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	f := e.friend(t, member)

	inv, err := e.svc.InviteFriend(ctx, member, tf.id, f)
	if err != nil || inv.UserID != f || inv.InviterID != member || inv.TayfaName != "Kamp Tayfası" {
		t.Fatalf("davet: %+v %v", inv, err)
	}
	if got := e.status(t, tf.id, f); got != "invited/member" {
		t.Fatalf("durum = %s", got)
	}
	// One event for the notification, IDs only; no membership.changed.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'tayfa.invited' AND actor_id = $1 AND tayfa_id = $2
		AND payload = jsonb_build_object('tayfa_id', $2::text, 'user_id', $3::text)`, member.PG(), tf.id.PG(), f.String()); n != 1 {
		t.Fatalf("davet olayı = %d", n)
	}
	// The invited user (not a member yet) is notified, alone.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'notification.requested' AND actor_id = $1 AND tayfa_id = $2
		AND payload = jsonb_build_object('type', 'tayfa_invitation', 'tayfa_id', $2::text, 'target_type', 'tayfa', 'target_id', $2::text, 'recipient_ids', jsonb_build_array($3::text))`,
		member.PG(), tf.id.PG(), f.String()); n != 1 {
		t.Fatalf("davet bildirimi = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'membership.changed' AND subject_id = $1`, f.PG()); n != 0 {
		t.Fatalf("üyelik olayı = %d", n)
	}
	_, err = e.svc.InviteFriend(ctx, member, tf.id, f)
	wantErr(t, "tekrar davet", err, apperr.ErrAlreadyInvited)

	// Only friends; never oneself.
	stranger := id.New()
	e.profile(t, stranger)
	_, err = e.svc.InviteFriend(ctx, member, tf.id, stranger)
	wantErr(t, "arkadaş değil", err, apperr.ErrNotFriends)
	_, err = e.svc.InviteFriend(ctx, member, tf.id, member)
	wantErr(t, "kendini", err, errValidation)
	_, err = e.svc.InviteFriend(ctx, member, tf.id, id.New())
	wantErr(t, "bilinmeyen", err, apperr.ErrUserNotFound)

	// Already a member; banned; waiting join request.
	active := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	e.friends(t, member, active)
	_, err = e.svc.InviteFriend(ctx, member, tf.id, active)
	wantErr(t, "üye", err, apperr.ErrAlreadyMember)
	for _, st := range []authz.Status{authz.StatusBanned, authz.StatusPending} {
		u := e.seed(t, tf.id, authz.Member, st)
		e.friends(t, member, u)
		_, err = e.svc.InviteFriend(ctx, member, tf.id, u)
		wantErr(t, string(st), err, apperr.ErrInviteNotAllowed)
	}

	// A block in either direction hides the friend.
	blocked := e.friend(t, member)
	e.exec(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, blocked.PG(), member.PG())
	_, err = e.svc.InviteFriend(ctx, member, tf.id, blocked)
	wantErr(t, "engel", err, apperr.ErrUserNotFound)
}

// TestInviteFriendPermissions: every active, unmuted member of a writable
// tayfa; anyone else learns nothing about the tayfa (IDOR).
func TestInviteFriendPermissions(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "approval")
	for name, tc := range map[string]struct {
		st   authz.Status
		mute bool
		want error
	}{
		"onay bekleyen":   {authz.StatusPending, false, apperr.ErrTayfaNotFound},
		"davetli":         {authz.StatusInvited, false, apperr.ErrTayfaNotFound},
		"banlı":           {authz.StatusBanned, false, apperr.ErrTayfaNotFound},
		"susturulmuş üye": {authz.StatusActive, true, apperr.ErrMuted},
	} {
		u := e.seed(t, tf.id, authz.Member, tc.st)
		if tc.mute {
			e.raw(t, `UPDATE memberships SET muted_until = now() + interval '1 hour', muted_by = $3 WHERE tayfa_id = $1 AND user_id = $2`, tf.id.PG(), u.PG(), tf.admin.PG())
		}
		_, err := e.svc.InviteFriend(ctx, u, tf.id, e.friend(t, u))
		wantErr(t, name, err, tc.want)
	}
	outsider := id.New()
	_, err := e.svc.InviteFriend(ctx, outsider, tf.id, e.friend(t, outsider))
	wantErr(t, "üye değil", err, apperr.ErrTayfaNotFound)

	e.suspend(t, tf.id)
	_, err = e.svc.InviteFriend(ctx, tf.admin, tf.id, e.friend(t, tf.admin))
	wantErr(t, "salt okunur", err, apperr.ErrTayfaReadOnly)
}

// TestDirectInviteDailyLimit: 10 per inviter in 24 hours, in all tayfas
// together; declined and cancelled invitations still count.
func TestDirectInviteDailyLimit(t *testing.T) {
	e := setup(t)
	t1, t2 := e.newTayfa(t, "approval"), e.newTayfa(t, "open")
	inviter := e.seed(t, t1.id, authz.Member, authz.StatusActive)
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at) VALUES ($1, $2, 'member', 'active', 'code', now())`, t2.id.PG(), inviter.PG())
	must(t, err)
	must(t, tx.Commit(ctx))
	for i := range DirectInvitesPerDay {
		tf := t1.id
		if i%2 == 1 {
			tf = t2.id
		}
		f := e.friend(t, inviter)
		_, err := e.svc.InviteFriend(ctx, inviter, tf, f)
		must(t, err)
		if i == 0 {
			must(t, e.svc.DeclineInvitation(ctx, f, tf))
		}
	}
	_, err = e.svc.InviteFriend(ctx, inviter, t1.id, e.friend(t, inviter))
	wantErr(t, "11. davet", err, apperr.ErrDirectInviteLimit)
	// Another member has their own limit.
	if _, err := e.svc.InviteFriend(ctx, t1.admin, t1.id, e.friend(t, t1.admin)); err != nil {
		t.Fatalf("başka üye: %v", err)
	}
	// Moving outbox events back in time is a fixture core_app cannot write.
	_, err = e.admin.Exec(ctx, `UPDATE domain_events SET occurred_at = occurred_at - interval '25 hours' WHERE actor_id = $1`, inviter.PG())
	must(t, err)
	if _, err := e.svc.InviteFriend(ctx, inviter, t1.id, e.friend(t, inviter)); err != nil {
		t.Fatalf("ertesi gün: %v", err)
	}
}

// TestAcceptInvitationOutcome: v14 "Davet kabul edilince" for every
// join policy and inviter role, decided by the inviter's role at the time
// of acceptance.
func TestAcceptInvitationOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  string
		inviter authz.Role
		// demote: the inviter is no longer a moderator when the invitation
		// is accepted.
		demote bool
		want   string
	}{
		{"açık, üye davet etti", "open", authz.Member, false, "active/member"},
		{"açık, yönetici davet etti", "open", authz.Admin, false, "active/member"},
		{"onaylı, yönetici davet etti", "approval", authz.Admin, false, "active/member"},
		{"onaylı, moderatör davet etti", "approval", authz.Moderator, false, "active/member"},
		{"onaylı, üye davet etti", "approval", authz.Member, false, "pending/member"},
		{"onaylı, moderatör sonradan üye oldu", "approval", authz.Moderator, true, "pending/member"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			tf := e.newTayfa(t, tc.policy)
			inviter := tf.admin
			if tc.inviter != authz.Admin {
				inviter = e.seed(t, tf.id, tc.inviter, authz.StatusActive)
			}
			f := e.friend(t, inviter)
			_, err := e.svc.InviteFriend(ctx, inviter, tf.id, f)
			must(t, err)
			if tc.demote {
				e.raw(t, `UPDATE memberships SET role = 'member' WHERE tayfa_id = $1 AND user_id = $2`, tf.id.PG(), inviter.PG())
			}
			m, err := e.svc.AcceptInvitation(ctx, f, tf.id)
			must(t, err)
			if got := string(m.Status) + "/" + m.Role.String(); got != tc.want {
				t.Fatalf("sonuç = %s, beklenen %s", got, tc.want)
			}
			change := "joined"
			if tc.want == "pending/member" {
				change = "requested"
			}
			if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'membership.changed' AND payload->>'change' = $1 AND payload->>'user_id' = $2`, change, f.String()); n != 1 {
				t.Fatalf("üyelik olayı (%s) = %d", change, n)
			}
			_, err = e.svc.AcceptInvitation(ctx, f, tf.id)
			wantErr(t, "tekrar kabul", err, apperr.ErrInvitationNotFound)
		})
	}
}

func TestAnswerAndCancelInvitation(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "approval")
	member := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	mod := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	f1, f2 := e.friend(t, member), e.friend(t, member)
	for _, f := range []id.ID{f1, f2} {
		_, err := e.svc.InviteFriend(ctx, member, tf.id, f)
		must(t, err)
	}

	// The invited user sees their invitation; nobody else's.
	my, err := e.svc.ListMyInvitations(ctx, f1, 0, "")
	if err != nil || len(my.Items) != 1 || my.Items[0].TayfaID != tf.id || my.Items[0].InviterID != member || my.Items[0].TayfaName == "" {
		t.Fatalf("davetlerim: %+v %v", my, err)
	}
	// Someone without an invitation cannot answer one (IDOR), nor touch
	// another user's.
	_, err = e.svc.AcceptInvitation(ctx, id.New(), tf.id)
	wantErr(t, "davetsiz kabul", err, apperr.ErrInvitationNotFound)
	err = e.svc.DeclineInvitation(ctx, member, tf.id)
	wantErr(t, "üyenin reddi", err, apperr.ErrInvitationNotFound)
	_, err = e.svc.AcceptInvitation(ctx, f1, id.New())
	wantErr(t, "bilinmeyen tayfa", err, apperr.ErrInvitationNotFound)

	must(t, e.svc.DeclineInvitation(ctx, f1, tf.id))
	if got := e.status(t, tf.id, f1); got != "none" {
		t.Fatalf("ret sonrası = %s", got)
	}

	// Only admins list and cancel invitations.
	_, err = e.svc.ListInvitations(ctx, mod, tf.id, 0, "")
	wantErr(t, "moderatör listesi", err, apperr.ErrPermissionDenied)
	err = e.svc.CancelInvitation(ctx, mod, tf.id, f2)
	wantErr(t, "moderatör iptali", err, apperr.ErrPermissionDenied)
	err = e.svc.CancelInvitation(ctx, member, tf.id, f2)
	wantErr(t, "davet edenin iptali", err, apperr.ErrPermissionDenied)
	l, err := e.svc.ListInvitations(ctx, tf.admin, tf.id, 0, "")
	if err != nil || len(l.Items) != 1 || l.Items[0].UserID != f2 {
		t.Fatalf("davetler: %+v %v", l, err)
	}
	err = e.svc.CancelInvitation(ctx, tf.admin, tf.id, member)
	wantErr(t, "üyeyi davet gibi iptal", err, apperr.ErrInvitationNotFound)
	must(t, e.svc.CancelInvitation(ctx, tf.admin, tf.id, f2))
	if got := e.status(t, tf.id, f2); got != "none" {
		t.Fatalf("iptal sonrası = %s", got)
	}
	err = e.svc.CancelInvitation(ctx, tf.admin, tf.id, f2)
	wantErr(t, "tekrar iptal", err, apperr.ErrInvitationNotFound)
	for _, ev := range []string{"tayfa.invitation_declined", "tayfa.invitation_cancelled"} {
		if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = $1`, ev); n != 1 {
			t.Fatalf("%s = %d", ev, n)
		}
	}
}

func TestInvitedUserIsNotAMember(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "open")
	f := e.friend(t, tf.admin)
	_, err := e.svc.InviteFriend(ctx, tf.admin, tf.id, f)
	must(t, err)
	_, err = e.svc.ListMembers(ctx, f, tf.id, "", 0, "")
	wantErr(t, "davetli üye listesi", err, apperr.ErrTayfaNotFound)
	_, err = e.svc.InviteFriend(ctx, f, tf.id, e.friend(t, f))
	wantErr(t, "davetli davet edemez", err, apperr.ErrTayfaNotFound)
}
