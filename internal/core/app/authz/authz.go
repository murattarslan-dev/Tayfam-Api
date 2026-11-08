// Package authz holds core's single permission table and the role hierarchy
// (ADR-0004).
//
// Every tayfa action is checked here before the app layer touches data. The
// answer defaults to "no": an action missing from the table, an unknown role,
// a membership that is not active or a muted member trying to write are all
// refused. The package is pure Go; callers load the membership themselves
// (scoped by tayfa_id and user_id) and pass it in as a small value, so the
// rules can be tested without a database.
//
// The same hierarchy is enforced again by the memberships trigger in the
// database (veri modeli v14, "Rol hiyerarşisi"); this package does not
// replace it. Last-admin protection lives only in that trigger.
package authz

import (
	"errors"
	"time"
)

// Role is a member's role in a tayfa. Higher values outrank lower ones:
// admin (3) › moderator (2) › member (1). The zero value is not a role.
type Role int

// Roles, in hierarchy order.
const (
	Member    Role = 1
	Moderator Role = 2
	Admin     Role = 3
)

// ErrUnknownRole is returned by ParseRole for values outside the role set.
var ErrUnknownRole = errors.New("bilinmeyen rol")

// ErrUnknownStatus is returned by ParseStatus for values outside the status
// set.
var ErrUnknownStatus = errors.New("bilinmeyen üyelik durumu")

// ParseRole converts the stored form (memberships.role) to a Role.
func ParseRole(s string) (Role, error) {
	switch s {
	case "admin":
		return Admin, nil
	case "moderator":
		return Moderator, nil
	case "member":
		return Member, nil
	}
	return 0, ErrUnknownRole
}

// String returns the stored form of the role, or "" for an invalid one.
func (r Role) String() string {
	switch r {
	case Admin:
		return "admin"
	case Moderator:
		return "moderator"
	case Member:
		return "member"
	}
	return ""
}

// Valid reports whether r is one of the defined roles.
func (r Role) Valid() bool { return r >= Member && r <= Admin }

// Status is the state of a membership (memberships.status). The zero value
// means "not a member" and is never allowed anything.
type Status string

// Membership states (veri modeli v14).
const (
	StatusInvited Status = "invited"
	StatusPending Status = "pending"
	StatusActive  Status = "active"
	StatusBanned  Status = "banned"
)

// ParseStatus converts the stored form (memberships.status) to a Status.
func ParseStatus(s string) (Status, error) {
	switch st := Status(s); st {
	case StatusInvited, StatusPending, StatusActive, StatusBanned:
		return st, nil
	}
	return "", ErrUnknownStatus
}

// Membership is the part of a memberships row that authorization needs. The
// zero value describes a user who is not a member of the tayfa.
type Membership struct {
	Role   Role
	Status Status
	// MutedUntil is the end of a mute; the zero value means not muted.
	MutedUntil time.Time
}

// Muted reports whether the membership is muted at now. A mute ends exactly
// at MutedUntil.
func (m Membership) Muted(now time.Time) bool {
	return !m.MutedUntil.IsZero() && m.MutedUntil.After(now)
}

// Action is an operation on a tayfa that needs a permission check. The zero
// value is not an action.
type Action int

// Actions. New actions are added before actionEnd and must be written into
// permissions; the tests fail otherwise.
const (
	TayfaView Action = iota + 1
	TayfaUpdateSettings
	TayfaRotateJoinCode
	TayfaManageModules
	TayfaRequestDeletion
	TayfaCancelDeletion
	InviteCreate
	InviteList
	InviteRevokeOwn
	InviteRevokeAny
	MemberList
	MemberListPending
	MemberApprove
	MemberReject
	MemberChangeRole
	MemberMute
	MemberBan
	MemberRemove
	AuditView

	actionEnd // sentinel; keep last
)

// actionNames are the stable names used in logs and audit records.
var actionNames = [...]string{
	TayfaView:            "tayfa.view",
	TayfaUpdateSettings:  "tayfa.update_settings",
	TayfaRotateJoinCode:  "tayfa.rotate_join_code",
	TayfaManageModules:   "tayfa.manage_modules",
	TayfaRequestDeletion: "tayfa.request_deletion",
	TayfaCancelDeletion:  "tayfa.cancel_deletion",
	InviteCreate:         "invite.create",
	InviteList:           "invite.list",
	InviteRevokeOwn:      "invite.revoke_own",
	InviteRevokeAny:      "invite.revoke_any",
	MemberList:           "member.list",
	MemberListPending:    "member.list_pending",
	MemberApprove:        "member.approve",
	MemberReject:         "member.reject",
	MemberChangeRole:     "member.change_role",
	MemberMute:           "member.mute",
	MemberBan:            "member.ban",
	MemberRemove:         "member.remove",
	AuditView:            "audit.view",
	actionEnd:            "",
}

// String returns the stable name of the action, or "" for an invalid one.
func (a Action) String() string {
	if a <= 0 || a >= actionEnd {
		return ""
	}
	return actionNames[a]
}

// permissions is the single permission table (ADR-0004): the lowest role an
// active member needs for each action. An action that is not listed here is
// refused. Product rules: Notion Özellik Haritası and veri modeli v14
// ("Rol hiyerarşisi", "Tayfaya davet", "Tayfa silmede bekleme").
var permissions = map[Action]Role{
	// Every active member.
	TayfaView:       Member,
	InviteCreate:    Member,
	InviteRevokeOwn: Member,
	MemberList:      Member,

	// Moderation: moderator and above.
	InviteList:        Moderator,
	MemberListPending: Moderator,
	MemberApprove:     Moderator,
	MemberReject:      Moderator,
	MemberMute:        Moderator,
	MemberBan:         Moderator,
	MemberRemove:      Moderator,

	// Tayfa management: admin only.
	TayfaUpdateSettings:  Admin,
	TayfaRotateJoinCode:  Admin,
	TayfaManageModules:   Admin,
	TayfaRequestDeletion: Admin,
	TayfaCancelDeletion:  Admin,
	InviteRevokeAny:      Admin,
	MemberChangeRole:     Admin,
	AuditView:            Admin,
}

// allowedWhileMuted lists the actions a muted member may still perform: reads,
// and revoking their own invites, which only narrows access. Every other
// action counts as a write and is refused during a mute. An action missing
// here is therefore blocked while muted.
var allowedWhileMuted = map[Action]bool{
	TayfaView:         true,
	InviteList:        true,
	InviteRevokeOwn:   true,
	MemberList:        true,
	MemberListPending: true,
	AuditView:         true,
}

// Allowed reports whether a user with membership m may perform a at now. Only
// active memberships with a valid role are considered; muted members may only
// perform the actions in allowedWhileMuted.
//
// For actions on another member (mute, ban, remove, role change) callers must
// also check CanActOn, and for role changes CanAssign or CanDemoteSelf.
func Allowed(m Membership, a Action, now time.Time) bool {
	if m.Status != StatusActive || !m.Role.Valid() {
		return false
	}
	need, ok := permissions[a]
	if !ok || m.Role < need {
		return false
	}
	if m.Muted(now) && !allowedWhileMuted[a] {
		return false
	}
	return true
}

// CanActOn reports whether actor may mute, ban, remove or change the role of
// a member with role target: only members of a strictly lower role. A
// moderator cannot touch a moderator and an admin cannot touch an admin.
func CanActOn(actor, target Role) bool {
	return actor.Valid() && target.Valid() && actor > target
}

// CanAssign reports whether actor may give another member newRole. Nobody can
// assign their own role or a higher one, so an admin can make moderators and
// members but not other admins.
func CanAssign(actor, newRole Role) bool {
	return actor.Valid() && newRole.Valid() && newRole < actor
}

// CanDemoteSelf reports whether a member whose role is current may change
// their own role to newRole: stepping down only. This is the only way an
// admin's role changes. The database trigger still refuses the change if it
// would leave the tayfa without an active admin.
func CanDemoteSelf(current, newRole Role) bool {
	return current.Valid() && newRole.Valid() && newRole < current
}
