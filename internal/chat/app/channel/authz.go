package channel

import "time"

// Role is a member's role in a tayfa as core reports it. Higher values
// outrank lower ones; the zero value is not a role.
type Role int

// Roles, in hierarchy order (v14 "Rol hiyerarşisi").
const (
	RoleMember    Role = 1
	RoleModerator Role = 2
	RoleAdmin     Role = 3
)

// Member is the caller's membership in a tayfa, from core's GetMembership
// (ADR-0009). The zero value is "not an active member".
type Member struct {
	Active bool
	Role   Role
	// MutedUntil is the end of a mute; zero means not muted.
	MutedUntil time.Time
}

// Muted reports whether the member is muted at now.
func (m Member) Muted(now time.Time) bool {
	return !m.MutedUntil.IsZero() && m.MutedUntil.After(now)
}

// Action is something a member does with channels.
type Action int

// Actions. The table below is the only place their rules are written.
const (
	actionUnknown Action = iota
	// ActionView: list channels, read a members channel and its messages.
	ActionView
	// ActionViewStaff: see staff channels at all.
	ActionViewStaff
	// ActionManage: create, edit, archive and unarchive channels.
	ActionManage
	// ActionSend: write a message.
	ActionSend
	// ActionSkipSlowMode: write without waiting for slow mode.
	ActionSkipSlowMode
	// ActionDeleteOwn: delete one's own message (content erased at once).
	ActionDeleteOwn
	// ActionDeleteOthers: hide someone else's message (restorable 30 days).
	ActionDeleteOthers
	// ActionRestore: undo a moderation deletion.
	ActionRestore
	// ActionMarkRead: move one's own read marker.
	ActionMarkRead
)

// minRole is chat's permission table for channel actions (issue #81; v14
// "Sohbet", "Spam ve taciz", "Silinen içeriği geri alma"). An action
// missing here is refused.
var minRole = map[Action]Role{
	ActionView:         RoleMember,
	ActionViewStaff:    RoleModerator,
	ActionManage:       RoleModerator,
	ActionSend:         RoleMember,
	ActionSkipSlowMode: RoleModerator,
	ActionDeleteOwn:    RoleMember,
	ActionDeleteOthers: RoleModerator,
	ActionRestore:      RoleAdmin,
	ActionMarkRead:     RoleMember,
}

// allowedWhileMuted lists what a muted member may still do: read, mark
// read, and remove their own content. Writing, managing channels and
// moderating others are refused while muted (v14 "Susturma": "okur ama
// yazamaz").
var allowedWhileMuted = map[Action]bool{
	ActionView:         true,
	ActionViewStaff:    true,
	ActionMarkRead:     true,
	ActionDeleteOwn:    true,
	ActionSkipSlowMode: true,
}

// Allowed reports whether m may perform a at now. The answer defaults to
// "no": inactive memberships, unknown roles and unknown actions are
// refused.
func Allowed(m Member, a Action, now time.Time) bool {
	return roleAllows(m, a) && (!m.Muted(now) || allowedWhileMuted[a])
}

func roleAllows(m Member, a Action) bool {
	if !m.Active || m.Role < RoleMember || m.Role > RoleAdmin {
		return false
	}
	need, ok := minRole[a]
	return ok && m.Role >= need
}
