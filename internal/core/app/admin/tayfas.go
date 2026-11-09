package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TayfaSummary is a tayfa's metadata in lists. Metadata of private tayfas
// is visible to the platform (Notion "Kötüye Kullanım ve Genç Kullanıcı
// Güvenliği"); content is not.
type TayfaSummary struct {
	ID                  id.ID
	Name                string
	Status              string // active, warned, archived
	Listed              bool
	MemberCount         int
	CreatedAt           time.Time
	SuspendedAt         time.Time
	DeletedAt           time.Time
	DeletionScheduledAt time.Time
}

// Member is a membership as the panel sees it.
type Member struct {
	User        id.ID
	DisplayName string
	Role        string
	Status      string
	JoinedAt    time.Time
}

// TayfaDetail is GetTayfa's answer.
type TayfaDetail struct {
	TayfaSummary
	Description         string
	City, District      string
	SuspendReason       string
	Staff               []Member
	OpenPlatformReports int
}

// UserTayfa is a tayfa a user is an active member of.
type UserTayfa struct {
	Tayfa TayfaSummary
	Role  string
}

// SearchTayfas lists tayfas whose name contains query, newest first.
func (s *Service) SearchTayfas(ctx context.Context, c auth.AdminClaims, query string) ([]TayfaSummary, error) {
	if utf8.RuneCountInString(query) > 50 || !utf8.ValidString(query) {
		return nil, apperr.Validation("query")
	}
	if _, err := s.caller(ctx, c); err != nil {
		return nil, err
	}
	rows, err := s.q.AdminSearchTayfas(ctx, normalize(query))
	if err != nil {
		return nil, fmt.Errorf("tayfa arama: %w", err)
	}
	out := make([]TayfaSummary, len(rows))
	for i, r := range rows {
		out[i] = TayfaSummary{
			ID: idOf(r.ID), Name: r.Name, Status: r.Status, Listed: r.Visibility == "listed", MemberCount: int(r.MemberCount),
			CreatedAt: timeOf(r.CreatedAt), SuspendedAt: timeOf(r.SuspendedAt), DeletedAt: timeOf(r.DeletedAt),
			DeletionScheduledAt: timeOf(r.DeletionScheduledFor),
		}
	}
	return out, nil
}

// GetTayfa returns a tayfa's metadata and staff.
func (s *Service) GetTayfa(ctx context.Context, c auth.AdminClaims, tayfa id.ID) (TayfaDetail, error) {
	if _, err := s.caller(ctx, c); err != nil {
		return TayfaDetail{}, err
	}
	r, err := s.q.AdminGetTayfa(ctx, tayfa.PG())
	if err != nil {
		return TayfaDetail{}, notFound(err, apperr.ErrTayfaNotFound)
	}
	staff, err := s.members(ctx, tayfa, true)
	if err != nil {
		return TayfaDetail{}, err
	}
	return TayfaDetail{
		TayfaSummary: TayfaSummary{
			ID: idOf(r.ID), Name: r.Name, Status: r.Status, Listed: r.Visibility == "listed", MemberCount: int(r.MemberCount),
			CreatedAt: timeOf(r.CreatedAt), SuspendedAt: timeOf(r.SuspendedAt), DeletedAt: timeOf(r.DeletedAt),
			DeletionScheduledAt: timeOf(r.DeletionScheduledFor),
		},
		Description: r.Description.String, City: r.City.String, District: r.District.String,
		SuspendReason: r.SuspendedReason.String, Staff: staff, OpenPlatformReports: int(r.OpenPlatformReports),
	}, nil
}

// ListTayfaMembers lists a tayfa's memberships and roles.
func (s *Service) ListTayfaMembers(ctx context.Context, c auth.AdminClaims, tayfa id.ID) ([]Member, error) {
	if _, err := s.caller(ctx, c); err != nil {
		return nil, err
	}
	if _, err := s.q.AdminGetTayfa(ctx, tayfa.PG()); err != nil {
		return nil, notFound(err, apperr.ErrTayfaNotFound)
	}
	return s.members(ctx, tayfa, false)
}

func (s *Service) members(ctx context.Context, tayfa id.ID, staffOnly bool) ([]Member, error) {
	rows, err := s.q.AdminListTayfaMembers(ctx, store.AdminListTayfaMembersParams{TayfaID: tayfa.PG(), StaffOnly: staffOnly})
	if err != nil {
		return nil, fmt.Errorf("üyeler: %w", err)
	}
	out := make([]Member, len(rows))
	for i, r := range rows {
		out[i] = Member{User: idOf(r.UserID), DisplayName: r.DisplayName.String, Role: r.Role, Status: r.Status, JoinedAt: timeOf(r.JoinedAt)}
	}
	return out, nil
}

// ListUserTayfas lists the tayfas user is an active member of.
func (s *Service) ListUserTayfas(ctx context.Context, c auth.AdminClaims, user id.ID) ([]UserTayfa, error) {
	if _, err := s.caller(ctx, c); err != nil {
		return nil, err
	}
	rows, err := s.q.AdminListUserTayfas(ctx, user.PG())
	if err != nil {
		return nil, fmt.Errorf("kullanıcının tayfaları: %w", err)
	}
	out := make([]UserTayfa, len(rows))
	for i, r := range rows {
		out[i] = UserTayfa{Role: r.Role, Tayfa: TayfaSummary{
			ID: idOf(r.ID), Name: r.Name, Status: r.Status, Listed: r.Visibility == "listed", MemberCount: int(r.MemberCount),
			CreatedAt: timeOf(r.CreatedAt), SuspendedAt: timeOf(r.SuspendedAt), DeletedAt: timeOf(r.DeletedAt),
			DeletionScheduledAt: timeOf(r.DeletionScheduledFor),
		}}
	}
	return out, nil
}

// lockTayfa locks a tayfa for a platform change.
func lockTayfa(ctx context.Context, q *store.Queries, tayfa id.ID) (store.AdminLockTayfaRow, error) {
	r, err := q.AdminLockTayfa(ctx, tayfa.PG())
	if err != nil {
		return r, notFound(err, apperr.ErrTayfaNotFound)
	}
	return r, nil
}

// SuspendTayfa makes a tayfa read-only (access.ReadOnly) until it is lifted.
// The tayfa's admins and moderators get a notification and see the reason
// in the tayfa's details; chat drops its cached permissions.
func (s *Service) SuspendTayfa(ctx context.Context, c auth.AdminClaims, tayfa id.ID, reason string) error {
	if utf8.RuneCountInString(reason) > maxSuspendReason {
		return apperr.ErrReasonRequired
	}
	cl, err := s.destructive(ctx, c, reason)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := lockTayfa(ctx, q, tayfa); err != nil {
			return err
		}
		return suspend(ctx, q, cl, tayfa, reason, s.cfg.Now().UTC(), "tayfa.suspend", "tayfa", tayfa, nil)
	})
	if err != nil {
		return err
	}
	s.cfg.Log.WarnContext(ctx, "tayfa platformca askıya alındı", slog.String("tayfa_id", tayfa.String()), slog.String("admin_id", cl.User.String()))
	return nil
}

// suspend suspends a locked tayfa and writes the record, events,
// notification and the audit entry (action on target). Report decisions
// use it too.
func suspend(ctx context.Context, q *store.Queries, c Caller, tayfa id.ID, reason string, now time.Time, action, targetType string, target id.ID, extra map[string]string) error {
	n, err := q.AdminSuspendTayfa(ctx, store.AdminSuspendTayfaParams{ID: tayfa.PG(), At: ts(now), Reason: pgtext(reason), By: c.User.PG()})
	if err != nil {
		return fmt.Errorf("askı: %w", err)
	}
	if n != 1 {
		return apperr.ErrTayfaStateConflict
	}
	return statusChange(ctx, q, c, tayfa, EventTayfaSuspended, notify.TypeTayfaSuspended, "platform.suspended", reason, action, targetType, target, extra)
}

// UnsuspendTayfa lifts a platform suspension.
func (s *Service) UnsuspendTayfa(ctx context.Context, c auth.AdminClaims, tayfa id.ID, reason string) error {
	cl, err := s.destructive(ctx, c, reason)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := lockTayfa(ctx, q, tayfa); err != nil {
			return err
		}
		n, err := q.AdminUnsuspendTayfa(ctx, tayfa.PG())
		if err != nil {
			return fmt.Errorf("askı kaldırma: %w", err)
		}
		if n != 1 {
			return apperr.ErrTayfaStateConflict
		}
		return statusChange(ctx, q, cl, tayfa, EventTayfaUnsuspended, notify.TypeTayfaUnsuspended, "platform.unsuspended", reason, "tayfa.unsuspend", "tayfa", tayfa, nil)
	})
}

// statusChange writes, after a tayfa state change: the tayfa's record, the
// tayfa event (chat), the staff notification, and the platform audit
// entry last.
func statusChange(ctx context.Context, q *store.Queries, c Caller, tayfa id.ID, event, notice, record, reason, action, targetType string, target id.ID, extra map[string]string) error {
	if err := tayfaRecord(ctx, q, tayfa, record, txn.Target{}); err != nil {
		return err
	}
	staff, err := q.AdminTayfaStaffIDs(ctx, tayfa.PG())
	if err != nil {
		return fmt.Errorf("yöneticiler: %w", err)
	}
	if err := txn.Event(ctx, q, event, tayfa, id.ID{}, txn.Target{Type: "tayfa", ID: tayfa}, map[string]any{"tayfa_id": tayfa.String()}); err != nil {
		return err
	}
	if notice != "" {
		to := make([]id.ID, len(staff))
		for i, u := range staff {
			to[i] = idOf(u)
		}
		if err := notify.Write(ctx, q, notify.Request{Type: notice, Tayfa: tayfa, TargetType: notify.TargetTayfa, Target: tayfa}, to); err != nil {
			return err
		}
	}
	if action == "" {
		return nil // part of a report decision, which writes the entry
	}
	return audit(ctx, q, c, tayfa, action, targetType, target, reason, extra)
}

// DeleteTayfa soft deletes a tayfa at once (no 72-hour wait). Everything
// else follows the normal deletion: tayfa.deleted to chat, purge after 30
// days by the jobs.
func (s *Service) DeleteTayfa(ctx context.Context, c auth.AdminClaims, tayfa id.ID, reason string) error {
	cl, err := s.destructive(ctx, c, reason)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := lockTayfa(ctx, q, tayfa); err != nil {
			return err
		}
		n, err := q.AdminDeleteTayfa(ctx, store.AdminDeleteTayfaParams{ID: tayfa.PG(), At: ts(s.cfg.Now().UTC()), By: cl.User.PG()})
		if err != nil {
			return fmt.Errorf("tayfa silme: %w", err)
		}
		if n != 1 {
			return apperr.ErrTayfaStateConflict
		}
		return statusChange(ctx, q, cl, tayfa, EventTayfaDeleted, "", "platform.deleted", reason, "tayfa.delete", "tayfa", tayfa, nil)
	})
	if err != nil {
		return err
	}
	s.cfg.Log.WarnContext(ctx, "tayfa platformca silindi", slog.String("tayfa_id", tayfa.String()), slog.String("admin_id", cl.User.String()))
	return nil
}

var roles = map[string]bool{"admin": true, "moderator": true, "member": true}

// SetMemberRole changes an active member's role, under the narrow
// 'platform_admin' system job of the memberships trigger: only the role of
// an active member, and the tayfa must keep an active admin.
func (s *Service) SetMemberRole(ctx context.Context, c auth.AdminClaims, tayfa, user id.ID, role, reason string) error {
	if !roles[role] {
		return apperr.Validation("role")
	}
	cl, err := s.destructive(ctx, c, reason)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		t, err := lockTayfa(ctx, q, tayfa)
		if err != nil {
			return err
		}
		if t.DeletedAt.Valid {
			return apperr.ErrTayfaNotFound
		}
		m, err := q.AdminMemberRole(ctx, store.AdminMemberRoleParams{TayfaID: tayfa.PG(), UserID: user.PG()})
		if err != nil || m.Status != "active" {
			return apperr.ErrMemberNotFound
		}
		if m.Role == role {
			return apperr.ErrTayfaStateConflict
		}
		if err := q.SetSystemJob(ctx, systemJob); err != nil {
			return fmt.Errorf("sistem işi: %w", err)
		}
		if n, err := q.AdminSetMemberRole(ctx, store.AdminSetMemberRoleParams{TayfaID: tayfa.PG(), UserID: user.PG(), Role: role}); err != nil {
			return err
		} else if n != 1 {
			return errors.New("rol değişmedi")
		}
		if err := tayfaRecord(ctx, q, tayfa, "platform.role_changed", txn.Target{Type: "membership", ID: user}); err != nil {
			return err
		}
		if err := txn.Event(ctx, q, eventMembershipChange, tayfa, id.ID{}, txn.Target{Type: "membership", ID: user},
			map[string]any{"tayfa_id": tayfa.String(), "user_id": user.String(), "change": "role_changed"}); err != nil {
			return err
		}
		return audit(ctx, q, cl, tayfa, "membership.set_role", "tayfa", tayfa, reason, map[string]string{"user_id": user.String()})
	})
}
