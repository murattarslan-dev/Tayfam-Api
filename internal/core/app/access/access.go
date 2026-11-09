// Package access loads the caller's standing in a tayfa and applies the
// common rules of every tayfa-scoped method (tayfa.proto, membership.proto):
//
//   - A caller who is not an ACTIVE member of a live tayfa gets
//     apperr.ErrTayfaNotFound, exactly as for an unknown or deleted tayfa.
//   - The permission table (authz) decides the rest: a role that is too low
//     gets apperr.ErrPermissionDenied, a muted member apperr.ErrMuted.
//   - Changes to a read-only tayfa get apperr.ErrTayfaReadOnly.
//   - Methods of a module the tayfa turned off get apperr.ErrModuleDisabled
//     (RequireModule).
//
// The tayfa ID and the user always come from the caller (the verified token
// and the request path), and every query is scoped by both (IDOR).
package access

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Tayfa is a live (not deleted) tayfa row.
type Tayfa = store.GetTayfaRow

// Load returns the tayfa and the caller's active membership in it. With
// lock, the tayfa row is locked until the transaction ends (FOR NO KEY
// UPDATE, the same lock the memberships trigger takes), so the membership
// read here cannot change before the write that depends on it; writes must
// pass lock = true inside txn.WithActor.
func Load(ctx context.Context, q *store.Queries, tayfa, user id.ID, lock bool) (Tayfa, authz.Membership, error) {
	var (
		t   Tayfa
		err error
	)
	if lock {
		var r store.GetTayfaForUpdateRow
		r, err = q.GetTayfaForUpdate(ctx, tayfa.PG())
		t = Tayfa(r)
	} else {
		t, err = q.GetTayfa(ctx, tayfa.PG())
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Tayfa{}, authz.Membership{}, apperr.ErrTayfaNotFound
	case err != nil:
		return Tayfa{}, authz.Membership{}, fmt.Errorf("tayfa: %w", err)
	}

	row, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: user.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Tayfa{}, authz.Membership{}, apperr.ErrTayfaNotFound
	case err != nil:
		return Tayfa{}, authz.Membership{}, fmt.Errorf("üyelik: %w", err)
	}
	m, err := Membership(row.Role, row.Status, row.MutedUntil)
	if err != nil {
		return Tayfa{}, authz.Membership{}, err
	}
	if m.Status != authz.StatusActive {
		return Tayfa{}, authz.Membership{}, apperr.ErrTayfaNotFound
	}
	return t, m, nil
}

// Membership converts stored membership columns for authz.
func Membership(role, status string, mutedUntil pgtype.Timestamptz) (authz.Membership, error) {
	r, err := authz.ParseRole(role)
	if err != nil {
		return authz.Membership{}, fmt.Errorf("üyelik rolü: %w", err)
	}
	s, err := authz.ParseStatus(status)
	if err != nil {
		return authz.Membership{}, fmt.Errorf("üyelik durumu: %w", err)
	}
	m := authz.Membership{Role: r, Status: s}
	if mutedUntil.Valid {
		m.MutedUntil = mutedUntil.Time
	}
	return m, nil
}

// Authorize checks the permission table for an active member: nil,
// apperr.ErrMuted when only the mute stands in the way, or
// apperr.ErrPermissionDenied.
func Authorize(m authz.Membership, a authz.Action, now time.Time) error {
	if authz.Allowed(m, a, now) {
		return nil
	}
	unmuted := m
	unmuted.MutedUntil = time.Time{}
	if m.Muted(now) && authz.Allowed(unmuted, a, now) {
		return apperr.ErrMuted
	}
	return apperr.ErrPermissionDenied
}

// Module names (tayfa_modules.module, the tayfa_modules_module CHECK).
const (
	ModuleAnnouncements = "announcements"
	ModuleEvents        = "events"
	ModuleQA            = "qa"
	ModulePolls         = "polls"
)

// RequireModule returns apperr.ErrModuleDisabled unless module is enabled
// in tayfa. A module without a row counts as disabled (default "no"). Every
// method of a module calls it after Load, reads included.
func RequireModule(ctx context.Context, q *store.Queries, tayfa id.ID, module string) error {
	on, err := q.IsTayfaModuleEnabled(ctx, store.IsTayfaModuleEnabledParams{TayfaID: tayfa.PG(), Module: module})
	if err != nil {
		return fmt.Errorf("modül: %w", err)
	}
	if !on {
		return apperr.ErrModuleDisabled
	}
	return nil
}

// Suspended reports whether the platform has suspended the tayfa.
func Suspended(t Tayfa) bool { return t.SuspendedAt.Valid }

// Archived reports whether the tayfa was archived for inactivity.
func Archived(t Tayfa) bool { return t.Status == "archived" }

// DeletionPending reports whether the tayfa is waiting for deletion.
func DeletionPending(t Tayfa) bool { return t.DeletionRequestedAt.Valid }

// ReadOnly reports whether the tayfa refuses changes: suspended, archived or
// waiting for deletion.
func ReadOnly(t Tayfa) bool { return Suspended(t) || Archived(t) || DeletionPending(t) }
