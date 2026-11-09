// Package userevents applies identity's user events to core (ADR-0009):
// user.profile_updated keeps the profile copy current and user.deleted
// removes the user's personal records from core.
//
// Events arrive at least once and may arrive out of order, so every apply is
// idempotent: applying the same event again, or an older one after a newer,
// leaves the same result (ADR-0002).
package userevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// systemJob is the app.system_job value the memberships and audit_logs
// triggers accept for account deletion (db/core/migrations/00001_init.sql).
const systemJob = "account_deletion"

// Membership change kinds this package emits in membership.changed.
const (
	eventMembershipChanged = "membership.changed"
	changeAccountDeleted   = "account_deleted"
	changeRoleChanged      = "role_changed"
	// eventInvitationCancelled is membership.EventInvitationCancelled.
	eventInvitationCancelled = "tayfa.invitation_cancelled"
)

// Event is one identity event about a user.
type Event struct {
	ID         id.ID // identity's outbox event ID (UUIDv7)
	User       id.ID
	OccurredAt time.Time
	// Exactly one of the three.
	Profile *Profile
	Deleted bool
	Ban     Ban
}

// Ban is a platform ban change (identity's user.banned / user.unbanned,
// issue #104).
type Ban int

// Ban values.
const (
	BanNone Ban = iota
	BanBanned
	BanLifted
)

// Profile is the public profile carried by user.profile_updated.
type Profile struct {
	DisplayName    string
	AvatarUploadID id.ID // zero: no avatar
	// ActivityVisibility is who sees the user's attendance history on their
	// profile; empty in older events, which keep the copy's current value.
	ActivityVisibility string
}

// ActivityVisibility values (user_profiles_activity_visibility).
const (
	VisibilityPrivate = "private"
	VisibilityTayfa   = "tayfa"
	VisibilityPublic  = "public"
)

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	Now  func() time.Time // optional, for tests
}

// Service applies user events.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil {
		return nil, errors.New("userevents: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}, nil
}

// Apply applies e.
func (s *Service) Apply(ctx context.Context, e Event) error {
	if e.ID == (id.ID{}) {
		return apperr.Validation("event_id")
	}
	if e.User == (id.ID{}) {
		return apperr.Validation("user_id")
	}
	switch {
	case e.Profile != nil && !e.Deleted && e.Ban == BanNone:
		return s.applyProfile(ctx, e)
	case e.Profile == nil && e.Deleted && e.Ban == BanNone:
		return s.applyDeleted(ctx, e)
	case e.Profile == nil && !e.Deleted && (e.Ban == BanBanned || e.Ban == BanLifted):
		return s.applyBan(ctx, e)
	}
	return apperr.Validation("event")
}

// applyBan records a platform ban or its lifting. A banned user's profile
// is hidden from other users; nothing is deleted (Notion "Platform Yönetim
// Paneli → Kurallar": hidden for the appeal). An older event applied after
// a newer one changes nothing.
func (s *Service) applyBan(ctx context.Context, e Event) error {
	at := pgtype.Timestamptz{}
	if e.Ban == BanBanned {
		at = pgtype.Timestamptz{Time: e.OccurredAt.UTC(), Valid: true}
		if e.OccurredAt.IsZero() {
			at.Time = time.Now().UTC()
		}
	}
	if _, err := store.New(s.cfg.Pool).ApplyUserBan(ctx, store.ApplyUserBanParams{UserID: e.User.PG(), BannedAt: at, EventID: e.ID.PG()}); err != nil {
		return fmt.Errorf("platform banı: %w", err)
	}
	return nil
}

func (s *Service) applyProfile(ctx context.Context, e Event) error {
	name := e.Profile.DisplayName
	if !validDisplayName(name) {
		return apperr.Validation("profile_updated.display_name")
	}
	vis := pgtype.Text{}
	switch v := e.Profile.ActivityVisibility; v {
	case "":
	case VisibilityPrivate, VisibilityTayfa, VisibilityPublic:
		vis = pgtype.Text{String: v, Valid: true}
	default:
		return apperr.Validation("profile_updated.activity_visibility")
	}
	avatar := pgtype.UUID{}
	if e.Profile.AvatarUploadID != (id.ID{}) {
		avatar = e.Profile.AvatarUploadID.PG()
	}
	// 0 rows: a newer event or the deletion was applied already. That is
	// the expected result of a repeat, not an error.
	if _, err := store.New(s.cfg.Pool).UpsertUserProfile(ctx, store.UpsertUserProfileParams{
		UserID: e.User.PG(), DisplayName: pgtype.Text{String: name, Valid: true},
		AvatarUploadID: avatar, SourceEventID: e.ID.PG(), ActivityVisibility: vis,
	}); err != nil {
		return fmt.Errorf("profil kopyası: %w", err)
	}
	return nil
}

// validDisplayName mirrors identity's rule (1–40 characters, no control
// characters, normalised spaces) and user_profiles_display_name_len.
func validDisplayName(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 40 || !utf8.ValidString(s) || strings.Join(strings.Fields(s), " ") != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return false
		}
	}
	return true
}

// applyDeleted removes the user from core in one transaction:
//
//   - every membership is deleted; where the user was the last active admin,
//     the oldest moderator, else the oldest active member, becomes admin
//     first (data model v14 "Son yönetici koruması");
//   - a tayfa left without any active member is scheduled for deletion
//     after tayfa.DeletionDelay, requested by nobody (the system);
//   - author and actor fields pointing at the user become NULL (questions,
//     answers and polls stay, by a deleted user); the user's event RSVPs,
//     convoy vehicles (plates), hike roll calls, fishing catches, activity
//     records, helpful votes and poll votes are deleted, and the vote counts
//     drop with them;
//   - friendships, friend requests and blocks in both directions are
//     deleted, and so are the direct invitations the user sent that are
//     still unanswered (issue #71);
//   - on every upcoming event the user was going to, the seat goes to the
//     oldest waitlisted answer (rsvp.promoted), as when an answer changes;
//   - reports stay, but the user's ID in them (as reporter, reported
//     person or handler) becomes NULL (v14 "Kullanıcı → yazdığı içerik");
//   - the profile copy becomes a tombstone.
//
// Everything runs as the account_deletion system job, the only identity the
// database triggers accept for these changes.
func (s *Service) applyDeleted(ctx context.Context, e Event) error {
	now := s.cfg.Now().UTC()
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetSystemJob(ctx, systemJob); err != nil {
			return fmt.Errorf("sistem işi: %w", err)
		}
		if err := q.DeleteUserBan(ctx, e.User.PG()); err != nil {
			return fmt.Errorf("platform banı: %w", err)
		}
		ms, err := q.ListMembershipsOfUser(ctx, e.User.PG())
		if err != nil {
			return fmt.Errorf("üyelikler: %w", err)
		}
		for _, m := range ms {
			if err := s.leave(ctx, q, id.ID(m.TayfaID.Bytes), e.User, m, now); err != nil {
				return err
			}
		}
		// Direct invitations the user sent and nobody answered go with the
		// account (issue #71): the inviter, whose role decided the outcome,
		// is gone.
		sent, err := q.ListInvitationsSentByUser(ctx, e.User.PG())
		if err != nil {
			return fmt.Errorf("gönderilen davetler: %w", err)
		}
		for _, inv := range sent {
			tf, invitee := id.ID(inv.TayfaID.Bytes), id.ID(inv.UserID.Bytes)
			if _, err := q.DeleteMembership(ctx, store.DeleteMembershipParams(inv)); err != nil {
				return fmt.Errorf("davet silme: %w", err)
			}
			if err := systemRecord(ctx, q, tf, "member.invitation_cancelled", eventInvitationCancelled, "membership", invitee,
				map[string]any{"tayfa_id": tf.String(), "user_id": invitee.String(), "reason": systemJob}); err != nil {
				return err
			}
		}
		// Locked before the RSVPs go, so the freed seats are given in this
		// transaction.
		going, err := q.LockUserGoingUpcomingEvents(ctx, store.LockUserGoingUpcomingEventsParams{UserID: e.User.PG(), Now: ts(now)})
		if err != nil {
			return fmt.Errorf("etkinlikler: %w", err)
		}
		for _, clear := range []func(context.Context, pgtype.UUID) error{
			q.ClearTayfaUserFields, q.ClearInviteCreator, q.ClearMembershipUserFields,
			q.ClearAnnouncementUserFields, q.ClearEventUserFields, q.ClearEventSeriesCreator,
			q.ClearRsvpMarker, q.DeleteUserRsvps, q.DeleteUserConvoyVehicles, q.ClearCampItemUserFields,
			q.DeleteUserHikeCheckins, q.DeleteUserCatches, q.ClearCatchDeleter, q.DeleteUserActivityRecords,
			q.ClearQuestionUserFields, q.ClearAnswerUserFields, q.DeleteUserAnswerVotes,
			q.ClearPollUserFields, q.DeleteUserPollVotes,
			q.DeleteUserFriendships, q.DeleteUserBlocks,
			q.ClearAuditActor, q.ClearEventActor, q.ClearReportUserFields,
		} {
			if err := clear(ctx, e.User.PG()); err != nil {
				return fmt.Errorf("yazar alanları: %w", err)
			}
		}
		for _, ev := range going {
			if err := promoteWaitlist(ctx, q, id.ID(ev.TayfaID.Bytes), id.ID(ev.ID.Bytes), ev.Capacity); err != nil {
				return err
			}
		}
		if err := q.TombstoneUserProfile(ctx, store.TombstoneUserProfileParams{UserID: e.User.PG(), SourceEventID: e.ID.PG()}); err != nil {
			return fmt.Errorf("profil kopyası: %w", err)
		}
		return nil
	})
}

func (s *Service) leave(ctx context.Context, q *store.Queries, tf, user id.ID, m store.ListMembershipsOfUserRow, now time.Time) error {
	t, err := q.LockTayfaForSystem(ctx, tf.PG())
	if err != nil {
		return fmt.Errorf("tayfa kilidi: %w", err)
	}
	live := !t.DeletedAt.Valid

	if live && m.Status == "active" && m.Role == "admin" {
		others, err := q.CountOtherActiveAdmins(ctx, store.CountOtherActiveAdminsParams{TayfaID: tf.PG(), UserID: user.PG()})
		if err != nil {
			return fmt.Errorf("yöneticiler: %w", err)
		}
		if others == 0 {
			if err := s.promoteSuccessor(ctx, q, tf, user); err != nil {
				return err
			}
		}
	}

	if _, err := q.DeleteMembership(ctx, store.DeleteMembershipParams{TayfaID: tf.PG(), UserID: user.PG()}); err != nil {
		return fmt.Errorf("üyelik silme: %w", err)
	}
	if err := systemRecord(ctx, q, tf, "member."+changeAccountDeleted, eventMembershipChanged, "membership", user,
		map[string]any{"tayfa_id": tf.String(), "user_id": user.String(), "change": changeAccountDeleted}); err != nil {
		return err
	}

	if !live || t.DeletionRequestedAt.Valid {
		return nil
	}
	n, err := q.CountActiveMembers(ctx, tf.PG())
	if err != nil {
		return fmt.Errorf("aktif üyeler: %w", err)
	}
	if n > 0 {
		return nil
	}
	// The last person left with their account: nobody can manage the tayfa
	// any more. It waits like a requested deletion and is then deleted by
	// the same job.
	at := now.Add(tayfa.DeletionDelay)
	if _, err := q.ScheduleTayfaDeletionBySystem(ctx, store.ScheduleTayfaDeletionBySystemParams{
		ID: tf.PG(), RequestedAt: ts(now), ScheduledFor: ts(at),
	}); err != nil {
		return fmt.Errorf("silme beklemesi: %w", err)
	}
	return systemRecord(ctx, q, tf, tayfa.EventDeletionRequested, tayfa.EventDeletionRequested, "tayfa", tf,
		map[string]any{"deletion_scheduled_for": at.Format(time.RFC3339Nano), "reason": "no_active_members"})
}

func (s *Service) promoteSuccessor(ctx context.Context, q *store.Queries, tf, user id.ID) error {
	succ, err := q.PickAdminSuccessor(ctx, store.PickAdminSuccessorParams{TayfaID: tf.PG(), UserID: user.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // no active member left; the tayfa is scheduled for deletion
	}
	if err != nil {
		return fmt.Errorf("halef: %w", err)
	}
	n, err := q.SetMembershipRole(ctx, store.SetMembershipRoleParams{TayfaID: tf.PG(), UserID: succ, Role: "admin"})
	if err != nil {
		return fmt.Errorf("halef atama: %w", err)
	}
	if n != 1 {
		return errors.New("halef atanamadı")
	}
	sid := id.ID(succ.Bytes)
	s.cfg.Log.InfoContext(ctx, "son yöneticinin hesabı silindi, halef atandı",
		slog.String("tayfa_id", tf.String()), slog.String("user_id", sid.String()))
	return systemRecord(ctx, q, tf, "member."+changeRoleChanged, eventMembershipChanged, "membership", sid,
		map[string]any{"tayfa_id": tf.String(), "user_id": sid.String(), "change": changeRoleChanged, "role": "admin", "reason": systemJob})
}

// promoteAll bounds one promotion round of an event without a capacity
// (event capacities are at most 10000).
const promoteAll = 10000

// eventRSVPPromoted is the event app's rsvp.promoted (event.RSVPPromoted).
const eventRSVPPromoted = "rsvp.promoted"

// promoteWaitlist moves the oldest waitlisted answers of a locked event to
// going while there is room, as the event app does when a going answer
// changes (data model v14 "Bekleme listesi terfisi").
func promoteWaitlist(ctx context.Context, q *store.Queries, tf, event id.ID, capacity pgtype.Int4) error {
	free := int64(promoteAll)
	if capacity.Valid {
		n, err := q.CountGoing(ctx, event.PG())
		if err != nil {
			return fmt.Errorf("katılımcı sayısı: %w", err)
		}
		free = int64(capacity.Int32) - n
	}
	if free <= 0 {
		return nil
	}
	users, err := q.NextWaitlisted(ctx, store.NextWaitlistedParams{EventID: event.PG(), N: int32(min(free, promoteAll))}) //nolint:gosec // bounded by promoteAll
	if err != nil {
		return fmt.Errorf("bekleme listesi: %w", err)
	}
	for _, u := range users {
		n, err := q.PromoteRsvp(ctx, store.PromoteRsvpParams{EventID: event.PG(), UserID: u})
		if err != nil {
			return fmt.Errorf("terfi: %w", err)
		}
		if n != 1 {
			continue
		}
		uid := id.ID(u.Bytes)
		if err := systemRecord(ctx, q, tf, eventRSVPPromoted, eventRSVPPromoted, "event", event,
			map[string]any{"tayfa_id": tf.String(), "event_id": event.String(), "user_id": uid.String()}); err != nil {
			return err
		}
		// The promoted member hears about the freed seat (ADR-0009).
		to, err := notify.Only(ctx, q, tf, []id.ID{uid}, id.ID{})
		if err != nil {
			return err
		}
		if err := notify.Write(ctx, q, notify.Request{
			Type: notify.TypeRSVPPromoted, Tayfa: tf, TargetType: notify.TargetEvent, Target: event,
		}, to); err != nil {
			return err
		}
	}
	return nil
}

// systemRecord writes an audit record and an outbox event without an actor:
// the change is the system's (account deletion), not a user's.
func systemRecord(ctx context.Context, q *store.Queries, tf id.ID, action, event, targetType string, target id.ID, meta map[string]any) error {
	b, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("json: %w", err)
	}
	tt := pgtype.Text{String: targetType, Valid: true}
	if err := q.CreateSystemAuditLog(ctx, store.CreateSystemAuditLogParams{
		ID: id.New().PG(), TayfaID: tf.PG(), Action: action, TargetType: tt, TargetID: target.PG(), Metadata: b,
	}); err != nil {
		return fmt.Errorf("denetim kaydı: %w", err)
	}
	// No actor: outbox.Write leaves actor_id NULL for the zero ID.
	return txn.Event(ctx, q, event, tf, id.ID{}, txn.Target{Type: targetType, ID: target}, meta)
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
