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

// Outbox events of direct invitations. tayfa.invited is for the
// notification; the other two only record what happened (they are not
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
}

// InviteFriend invites the caller's friend target into tayfa.
func (s *Service) InviteFriend(ctx context.Context, caller, tayfa, target id.ID) (Invitation, error) {
	if target == caller || target == (id.ID{}) {
		return Invitation{}, apperr.Validation("user_id")
	}
	var out Invitation
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		t, _, err := s.actorFor(ctx, q, tayfa, caller, authz.InviteDirect, true)
		if err != nil {
			return err
		}
		if t.readOnly {
			return apperr.ErrTayfaReadOnly
		}
		// The target must be a live user without a block between the two,
		// and the caller's friend.
		if _, err := q.GetLiveUserProfile(ctx, target.PG()); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.ErrUserNotFound
			}
			return fmt.Errorf("profil: %w", err)
		}
		blocked, err := social.IsBlocked(ctx, q, caller, target)
		if err != nil {
			return err
		}
		if blocked {
			return apperr.ErrUserNotFound
		}
		friends, err := social.Friends(ctx, q, caller, target)
		if err != nil {
			return err
		}
		if !friends {
			return apperr.ErrNotFriends
		}
		m, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("üyelik: %w", err)
		default:
			switch authz.Status(m.Status) {
			case authz.StatusActive:
				return apperr.ErrAlreadyMember
			case authz.StatusInvited:
				return apperr.ErrAlreadyInvited
			default:
				// Banned (the ban row stays) or a waiting join request.
				return apperr.ErrInviteNotAllowed
			}
		}

		if err := q.LockUserLimit(ctx, caller.PG()); err != nil {
			return fmt.Errorf("sınır kilidi: %w", err)
		}
		n, err := q.CountRecentEventsByActor(ctx, store.CountRecentEventsByActorParams{
			ActorID: caller.PG(), Type: EventInvited, Since: pgtype.Timestamptz{Time: s.now().Add(-social.LimitWindow), Valid: true},
		})
		if err != nil {
			return fmt.Errorf("davet sayısı: %w", err)
		}
		if n >= DirectInvitesPerDay {
			return apperr.ErrDirectInviteLimit
		}

		if err := q.CreateMembership(ctx, store.CreateMembershipParams{
			TayfaID: tayfa.PG(), UserID: target.PG(), Role: authz.Member.String(), Status: string(authz.StatusInvited),
			JoinedVia: "direct_invite", InvitedByUserID: caller.PG(),
		}); err != nil {
			return fmt.Errorf("davet: %w", err)
		}
		tf, err := q.GetTayfa(ctx, tayfa.PG())
		if err != nil {
			return fmt.Errorf("tayfa: %w", err)
		}
		out = Invitation{TayfaID: tayfa, TayfaName: tf.Name, UserID: target, InviterID: caller, InvitedAt: s.now()}
		return s.invitationRecord(ctx, q, tayfa, caller, target, "member.invited", EventInvited)
	})
	return out, err
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
	return out, nil
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
	return out, nil
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
