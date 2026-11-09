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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
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
)

// Event is one identity event about a user.
type Event struct {
	ID         id.ID // identity's outbox event ID (UUIDv7)
	User       id.ID
	OccurredAt time.Time
	// Exactly one of the two.
	Profile *Profile
	Deleted bool
}

// Profile is the public profile carried by user.profile_updated.
type Profile struct {
	DisplayName    string
	AvatarUploadID id.ID // zero: no avatar
}

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
	case e.Profile != nil && !e.Deleted:
		return s.applyProfile(ctx, e)
	case e.Profile == nil && e.Deleted:
		return s.applyDeleted(ctx, e)
	}
	return apperr.Validation("event")
}

func (s *Service) applyProfile(ctx context.Context, e Event) error {
	name := e.Profile.DisplayName
	if !validDisplayName(name) {
		return apperr.Validation("profile_updated.display_name")
	}
	avatar := pgtype.UUID{}
	if e.Profile.AvatarUploadID != (id.ID{}) {
		avatar = e.Profile.AvatarUploadID.PG()
	}
	// 0 rows: a newer event or the deletion was applied already. That is
	// the expected result of a repeat, not an error.
	if _, err := store.New(s.cfg.Pool).UpsertUserProfile(ctx, store.UpsertUserProfileParams{
		UserID: e.User.PG(), DisplayName: pgtype.Text{String: name, Valid: true},
		AvatarUploadID: avatar, SourceEventID: e.ID.PG(),
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
//   - author and actor fields pointing at the user become NULL;
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
		ms, err := q.ListMembershipsOfUser(ctx, e.User.PG())
		if err != nil {
			return fmt.Errorf("üyelikler: %w", err)
		}
		for _, m := range ms {
			if err := s.leave(ctx, q, id.ID(m.TayfaID.Bytes), e.User, m, now); err != nil {
				return err
			}
		}
		for _, clear := range []func(context.Context, pgtype.UUID) error{
			q.ClearTayfaUserFields, q.ClearInviteCreator, q.ClearMembershipUserFields,
			q.ClearAuditActor, q.ClearEventActor,
		} {
			if err := clear(ctx, e.User.PG()); err != nil {
				return fmt.Errorf("yazar alanları: %w", err)
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
	if err := q.CreateSystemDomainEvent(ctx, store.CreateSystemDomainEventParams{
		ID: id.New().PG(), Type: event, TayfaID: tf.PG(), SubjectType: tt, SubjectID: target.PG(), Payload: b,
	}); err != nil {
		return fmt.Errorf("olay: %w", err)
	}
	return nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
