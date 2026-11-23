package membership

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Direct invitations (issue #71, data model v14 "Tayfaya davet"): an active
// member invites a friend; the memberships row gets status invited and
// invited_by_user_id. The invited user is not a member until they accept.
// The memberships trigger checks the same shape again (only an active
// member inserts an invited row for someone else, only the invited user
// accepts, only a higher role deletes it).

// DirectInvitesPerDay is the daily direct invitation limit per inviter, in
// every tayfa together (v14 "üye başına günde 10 doğrudan davet").
const DirectInvitesPerDay = 10

// Outbox events of direct invitations. tayfa.invited is counted for the
// daily limit and followed by a notification.requested (tayfa_invitation)
// for the invited user; the other two only record what happened (they are not
// membership.changed: the invited user never was a member). Accepting is a
// membership.changed (joined or requested) like every other way in.
const (
	EventInvited             = "tayfa.invited"
	EventInvitationDeclined  = "tayfa.invitation_declined"
	EventInvitationCancelled = "tayfa.invitation_cancelled"
)

// Invitation is an unanswered direct invitation.
type Invitation struct {
	TayfaID   id.ID
	TayfaName string
	UserID    id.ID
	InviterID id.ID // zero when the inviter's account was deleted
	InvitedAt time.Time
	// Interests are the tayfa's catalog interest keys, in catalog order
	// (issue #194, ADR-0025 §2).
	Interests []string
}

// withInterests fills the tayfa interests of items with one query; the
// rows come in catalog order.
func withInterests(ctx context.Context, q *store.Queries, items []Invitation) ([]Invitation, error) {
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]pgtype.UUID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.TayfaID.PG())
	}
	rows, err := q.ListInterestsForTayfas(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("ilgi alanları: %w", err)
	}
	by := map[id.ID][]string{}
	for _, r := range rows {
		t := id.ID(r.TayfaID.Bytes)
		by[t] = append(by[t], r.InterestKey)
	}
	for i := range items {
		items[i].Interests = by[items[i].TayfaID]
	}
	return items, nil
}

// InviteFriend invites the caller's friend target into tayfa.
func (s *Service) InviteFriend(ctx context.Context, caller, tayfa, target id.ID) (Invitation, error) {
	if target == caller || target == (id.ID{}) {
		return Invitation{}, apperr.Validation("user_id")
	}
	var out Invitation
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		var err error
		out, err = s.InviteFriendTx(ctx, q, caller, tayfa, target)
		return err
	})
	return out, err
}

// InviteFriendTx is InviteFriend inside the caller's transaction q, which
// must have been opened with txn.WithActor for caller (package instant,
// ADR-0032 §2).
func (s *Service) InviteFriendTx(ctx context.Context, q *store.Queries, caller, tayfa, target id.ID) (Invitation, error) {
	if target == caller || target == (id.ID{}) {
		return Invitation{}, apperr.Validation("user_id")
	}
	t, _, err := s.actorFor(ctx, q, tayfa, caller, authz.InviteDirect, true)
	if err != nil {
		return Invitation{}, err
	}
	if t.readOnly {
		return Invitation{}, apperr.ErrTayfaReadOnly
	}
	// The target must be a live user without a block between the two,
	// and the caller's friend.
	if _, err := q.GetLiveUserProfile(ctx, target.PG()); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Invitation{}, apperr.ErrUserNotFound
		}
		return Invitation{}, fmt.Errorf("profil: %w", err)
	}
	blocked, err := social.IsBlocked(ctx, q, caller, target)
	if err != nil {
		return Invitation{}, err
	}
	if blocked {
		return Invitation{}, apperr.ErrUserNotFound
	}
	friends, err := social.Friends(ctx, q, caller, target)
	if err != nil {
		return Invitation{}, err
	}
	if !friends {
		return Invitation{}, apperr.ErrNotFriends
	}
	m, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Invitation{}, fmt.Errorf("üyelik: %w", err)
	default:
		switch authz.Status(m.Status) {
		case authz.StatusActive:
			return Invitation{}, apperr.ErrAlreadyMember
		case authz.StatusInvited:
			return Invitation{}, apperr.ErrAlreadyInvited
		default:
			// Banned (the ban row stays) or a waiting join request.
			return Invitation{}, apperr.ErrInviteNotAllowed
		}
	}

	if err := q.LockUserLimit(ctx, caller.PG()); err != nil {
		return Invitation{}, fmt.Errorf("sınır kilidi: %w", err)
	}
	n, err := q.CountRecentEventsByActor(ctx, store.CountRecentEventsByActorParams{
		ActorID: caller.PG(), Type: EventInvited, Since: pgtype.Timestamptz{Time: s.now().Add(-social.LimitWindow), Valid: true},
	})
	if err != nil {
		return Invitation{}, fmt.Errorf("davet sayısı: %w", err)
	}
	if n >= DirectInvitesPerDay {
		return Invitation{}, apperr.ErrDirectInviteLimit
	}

	if err := q.CreateMembership(ctx, store.CreateMembershipParams{
		TayfaID: tayfa.PG(), UserID: target.PG(), Role: authz.Member.String(), Status: string(authz.StatusInvited),
		JoinedVia: "direct_invite", InvitedByUserID: caller.PG(),
	}); err != nil {
		return Invitation{}, fmt.Errorf("davet: %w", err)
	}
	tf, err := q.GetTayfa(ctx, tayfa.PG())
	if err != nil {
		return Invitation{}, fmt.Errorf("tayfa: %w", err)
	}
	// A new invitation: the earlier outcome no longer applies (issue #192).
	if err := q.ClearInvitationOutcome(ctx, store.ClearInvitationOutcomeParams{TayfaID: tayfa.PG(), UserID: target.PG()}); err != nil {
		return Invitation{}, fmt.Errorf("davet sonucu: %w", err)
	}
	out := Invitation{TayfaID: tayfa, TayfaName: tf.Name, UserID: target, InviterID: caller, InvitedAt: s.now()}
	withI, err := withInterests(ctx, q, []Invitation{out})
	if err != nil {
		return Invitation{}, err
	}
	out = withI[0]
	if err := s.invitationRecord(ctx, q, tayfa, caller, target, "member.invited", EventInvited); err != nil {
		return Invitation{}, err
	}
	// The invited user is not a member, so not one of notify's member
	// audiences: the one recipient is named directly.
	if err := notify.Write(ctx, q, notify.Request{
		Type: notify.TypeTayfaInvitation, Tayfa: tayfa, Actor: caller, TargetType: notify.TargetTayfa, Target: tayfa,
	}, []id.ID{target}); err != nil {
		return Invitation{}, err
	}
	return out, nil
}

// AuditNowInvited is the audit action of an invitation sent with a now
// broadcast (InviteNowTx).
const AuditNowInvited = "member.now_invited"

// InviteNowTx invites the friends the caller chose for the now event of an
// instant tayfa the caller just opened (issue #265; ADR-0038 §4), inside
// the caller's transaction q (txn.WithActor for caller), and tells them
// with one now_started notification about the event. NowFriends checks
// every one again; who is left out, or is already in the tayfa in any
// way, is skipped without a trace. A now invitation does not count toward
// DirectInvitesPerDay (it writes no tayfa.invited event); the caller
// counts the broadcast (notify.TakeNowBroadcast). Accepting it is the
// ordinary AcceptInvitation: the inviter is the tayfa's admin, so the
// friend becomes an active member. It returns how many were invited.
func (s *Service) InviteNowTx(ctx context.Context, q *store.Queries, caller, tayfa, event id.ID, users []id.ID) (int, error) {
	t, _, err := s.actorFor(ctx, q, tayfa, caller, authz.InviteDirect, true)
	if err != nil {
		return 0, err
	}
	if t.readOnly || !t.instant {
		return 0, apperr.ErrPermissionDenied
	}
	to, err := notify.NowFriends(ctx, q, caller, users)
	if err != nil {
		return 0, err
	}
	invited := make([]id.ID, 0, len(to))
	for _, u := range to {
		_, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: u.PG()})
		switch {
		case err == nil:
			// Already a member, invited, waiting or banned.
			continue
		case !errors.Is(err, pgx.ErrNoRows):
			return 0, fmt.Errorf("üyelik: %w", err)
		}
		if err := q.CreateMembership(ctx, store.CreateMembershipParams{
			TayfaID: tayfa.PG(), UserID: u.PG(), Role: authz.Member.String(), Status: string(authz.StatusInvited),
			JoinedVia: "direct_invite", InvitedByUserID: caller.PG(),
		}); err != nil {
			return 0, fmt.Errorf("şimdi daveti: %w", err)
		}
		if err := q.ClearInvitationOutcome(ctx, store.ClearInvitationOutcomeParams{TayfaID: tayfa.PG(), UserID: u.PG()}); err != nil {
			return 0, fmt.Errorf("davet sonucu: %w", err)
		}
		if err := txn.Audit(ctx, q, tayfa, caller, AuditNowInvited, txn.Target{Type: "membership", ID: u}, nil); err != nil {
			return 0, err
		}
		invited = append(invited, u)
	}
	// The invited users are not members: the recipients are named
	// directly. Last write of the transaction (outbox ordering).
	if err := notify.Write(ctx, q, notify.Request{
		Type: notify.TypeNowStarted, Tayfa: tayfa, Actor: caller, TargetType: notify.TargetEvent, Target: event,
	}, invited); err != nil {
		return 0, err
	}
	return len(invited), nil
}

// AcceptInvitation accepts the caller's invitation to tayfa: active when the
// tayfa is open or the inviter is an active moderator or admin right now,
// pending otherwise (v14 "Davet kabul edilince").
func (s *Service) AcceptInvitation(ctx context.Context, caller, tayfa id.ID) (Member, error) {
	var out Member
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		t, m, err := s.ownInvitation(ctx, q, caller, tayfa)
		if err != nil {
			return err
		}
		if t.readOnly {
			return apperr.ErrTayfaReadOnly
		}
		st := authz.StatusPending
		if t.joinPolicy == "open" {
			st = authz.StatusActive
		} else if m.InvitedByUserID.Valid {
			im, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: m.InvitedByUserID})
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return fmt.Errorf("davet eden: %w", err)
			case im.Status == string(authz.StatusActive) && (im.Role == authz.Admin.String() || im.Role == authz.Moderator.String()):
				st = authz.StatusActive
			}
		}
		n, err := q.AcceptInvitation(ctx, store.AcceptInvitationParams{TayfaID: tayfa.PG(), UserID: caller.PG(), Status: string(st)})
		if err != nil {
			return fmt.Errorf("davet kabulü: %w", err)
		}
		if n != 1 {
			return apperr.ErrInvitationNotFound
		}
		if err := s.invitationOutcome(ctx, q, tayfa, caller, social.OutcomeAccepted); err != nil {
			return err
		}
		change := changeJoined
		if st == authz.StatusPending {
			change = changeRequested
		}
		meta := map[string]any{"via": "direct_invite"}
		if m.InvitedByUserID.Valid {
			meta["invited_by"] = id.ID(m.InvitedByUserID.Bytes).String()
		}
		if err := s.membershipChanged(ctx, q, tayfa, caller, caller, change, meta); err != nil {
			return err
		}
		out, err = s.reload(ctx, q, tayfa, caller)
		return err
	})
	return out, err
}

// DeclineInvitation deletes the caller's invitation to tayfa. It works on a
// read-only tayfa too.
func (s *Service) DeclineInvitation(ctx context.Context, caller, tayfa id.ID) error {
	return txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		if _, _, err := s.ownInvitation(ctx, q, caller, tayfa); err != nil {
			return err
		}
		if _, err := q.DeleteMembership(ctx, store.DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: caller.PG()}); err != nil {
			return fmt.Errorf("davet reddi: %w", err)
		}
		if err := s.invitationOutcome(ctx, q, tayfa, caller, social.OutcomeDeclined); err != nil {
			return err
		}
		return s.invitationRecord(ctx, q, tayfa, caller, caller, "member.invitation_declined", EventInvitationDeclined)
	})
}

// CancelInvitation withdraws target's unanswered invitation (admins).
func (s *Service) CancelInvitation(ctx context.Context, caller, tayfa, target id.ID) error {
	if target == (id.ID{}) {
		return apperr.Validation("user_id")
	}
	return txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		if _, _, err := s.actorFor(ctx, q, tayfa, caller, authz.InviteDirectCancel, true); err != nil {
			return err
		}
		m, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && m.Status != string(authz.StatusInvited)) {
			return apperr.ErrInvitationNotFound
		}
		if err != nil {
			return fmt.Errorf("davet: %w", err)
		}
		if _, err := q.DeleteMembership(ctx, store.DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()}); err != nil {
			return fmt.Errorf("davet iptali: %w", err)
		}
		if err := s.invitationOutcome(ctx, q, tayfa, target, social.OutcomeWithdrawn); err != nil {
			return err
		}
		return s.invitationRecord(ctx, q, tayfa, caller, target, "member.invitation_cancelled", EventInvitationCancelled)
	})
}

// ListMyInvitations lists the caller's unanswered invitations, newest first.
func (s *Service) ListMyInvitations(ctx context.Context, caller id.ID, size int32, token string) (Page[Invitation], error) {
	const method = "ListMyInvitations"
	size, err := pageSize(size)
	if err != nil {
		return Page[Invitation]{}, err
	}
	_, after, at, ok, err := decodeCursor(token, method, id.ID{}, "")
	if err != nil {
		return Page[Invitation]{}, err
	}
	p := store.ListMyInvitationsPageParams{UserID: caller.PG(), PageSize: size + 1,
		BeforeTime: pgtype.Timestamptz{Time: farFuture(), Valid: true}, BeforeTayfaID: maxID().PG()}
	if ok {
		p.BeforeTime, p.BeforeTayfaID = pgtype.Timestamptz{Time: at, Valid: true}, after.PG()
	}
	rows, err := s.q.ListMyInvitationsPage(ctx, p)
	if err != nil {
		return Page[Invitation]{}, fmt.Errorf("davetler: %w", err)
	}
	var out Page[Invitation]
	for i, r := range rows {
		if i == int(size) {
			last := rows[i-1]
			out.Next = cursor{Method: method, Tayfa: id.ID{}.String(), Micros: last.CreatedAt.Time.UnixMicro(), User: id.ID(last.TayfaID.Bytes).String()}.encode()
			break
		}
		inv := Invitation{TayfaID: id.ID(r.TayfaID.Bytes), TayfaName: r.TayfaName, UserID: caller, InvitedAt: r.CreatedAt.Time.UTC()}
		if r.InvitedByUserID.Valid {
			inv.InviterID = id.ID(r.InvitedByUserID.Bytes)
		}
		out.Items = append(out.Items, inv)
	}
	var err2 error
	out.Items, err2 = withInterests(ctx, s.q, out.Items)
	return out, err2
}

// ListInvitations lists tayfa's unanswered invitations, newest first
// (admins).
func (s *Service) ListInvitations(ctx context.Context, caller, tayfa id.ID, size int32, token string) (Page[Invitation], error) {
	const method = "ListInvitations"
	size, err := pageSize(size)
	if err != nil {
		return Page[Invitation]{}, err
	}
	_, after, at, ok, err := decodeCursor(token, method, tayfa, "")
	if err != nil {
		return Page[Invitation]{}, err
	}
	if _, _, err := s.actorFor(ctx, s.q, tayfa, caller, authz.InviteDirectCancel, false); err != nil {
		return Page[Invitation]{}, err
	}
	tf, err := s.q.GetTayfa(ctx, tayfa.PG())
	if err != nil {
		return Page[Invitation]{}, fmt.Errorf("tayfa: %w", err)
	}
	p := store.ListTayfaInvitationsPageParams{TayfaID: tayfa.PG(), PageSize: size + 1,
		BeforeTime: pgtype.Timestamptz{Time: farFuture(), Valid: true}, BeforeUserID: maxID().PG()}
	if ok {
		p.BeforeTime, p.BeforeUserID = pgtype.Timestamptz{Time: at, Valid: true}, after.PG()
	}
	rows, err := s.q.ListTayfaInvitationsPage(ctx, p)
	if err != nil {
		return Page[Invitation]{}, fmt.Errorf("davetler: %w", err)
	}
	var out Page[Invitation]
	for i, r := range rows {
		if i == int(size) {
			last := rows[i-1]
			out.Next = cursor{Method: method, Tayfa: tayfa.String(), Micros: last.CreatedAt.Time.UnixMicro(), User: id.ID(last.UserID.Bytes).String()}.encode()
			break
		}
		inv := Invitation{TayfaID: tayfa, TayfaName: tf.Name, UserID: id.ID(r.UserID.Bytes), InvitedAt: r.CreatedAt.Time.UTC()}
		if r.InvitedByUserID.Valid {
			inv.InviterID = id.ID(r.InvitedByUserID.Bytes)
		}
		out.Items = append(out.Items, inv)
	}
	var err2 error
	out.Items, err2 = withInterests(ctx, s.q, out.Items)
	return out, err2
}

// invitationOutcome records how user's invitation to tayfa ended (issue
// #192); only the invited user reads it.
func (s *Service) invitationOutcome(ctx context.Context, q *store.Queries, tayfa, user id.ID, outcome string) error {
	if err := q.SetInvitationOutcome(ctx, store.SetInvitationOutcomeParams{
		TayfaID: tayfa.PG(), UserID: user.PG(), Outcome: outcome, ResolvedAt: pgtype.Timestamptz{Time: s.now(), Valid: true},
	}); err != nil {
		return fmt.Errorf("davet sonucu: %w", err)
	}
	return nil
}

// farFuture and maxID start a newest-first list.
func farFuture() time.Time { return time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC) }

func maxID() id.ID {
	var u id.ID
	for i := range u {
		u[i] = 0xff
	}
	return u
}

// ownInvitation locks tayfa and returns the caller's invitation there. A
// missing tayfa or invitation is ErrInvitationNotFound: the caller is not a
// member, so the tayfa's existence says nothing more.
func (s *Service) ownInvitation(ctx context.Context, q *store.Queries, caller, tayfa id.ID) (tayfaState, store.GetMembershipRow, error) {
	t, err := lockTayfa(ctx, q, tayfa)
	if errors.Is(err, apperr.ErrTayfaNotFound) {
		return tayfaState{}, store.GetMembershipRow{}, apperr.ErrInvitationNotFound
	}
	if err != nil {
		return tayfaState{}, store.GetMembershipRow{}, err
	}
	m, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: caller.PG()})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && m.Status != string(authz.StatusInvited)) {
		return tayfaState{}, store.GetMembershipRow{}, apperr.ErrInvitationNotFound
	}
	if err != nil {
		return tayfaState{}, store.GetMembershipRow{}, fmt.Errorf("davet: %w", err)
	}
	return t, m, nil
}

// invitationRecord writes the audit record and the event of an invitation
// change. The payload names the invited user; the actor is the event's
// actor_id.
func (s *Service) invitationRecord(ctx context.Context, q *store.Queries, tayfa, actor, invited id.ID, action, event string) error {
	target := txn.Target{Type: "membership", ID: invited}
	if err := txn.Audit(ctx, q, tayfa, actor, action, target, nil); err != nil {
		return err
	}
	return txn.Event(ctx, q, event, tayfa, actor, target, map[string]any{"tayfa_id": tayfa.String(), "user_id": invited.String()})
}
