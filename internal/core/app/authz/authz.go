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
	RoleStepDown
	// Announcements (issue #48). Edit and delete of another author's
	// announcement also need CanActOnContent.
	AnnouncementCreate
	AnnouncementEdit
	AnnouncementDelete
	AnnouncementPin
	AnnouncementRestore
	// Events (issue #49). The event module switch is checked by the caller.
	EventView
	// EventCreate needs a moderator; Settings.MembersCanCreateEvents lowers
	// it to every active member (AllowedIn).
	EventCreate
	// EventEditOwn and EventCancelOwn are for the organizer (events.created_by);
	// the caller checks that the event is theirs.
	EventEditOwn
	EventEditAny
	EventCancelOwn
	EventCancelAny
	EventRSVP
	EventViewAttendees
	// Attendance (issue #50). EventCheckIn covers the caller's own location
	// or online check-in and their hike start and return.
	EventCheckIn
	// EventAttendanceOwn is the organizer's correction on their own event
	// (the caller checks that the event is theirs); EventAttendanceAny is
	// the same on every event of the tayfa.
	EventAttendanceOwn
	EventAttendanceAny
	// EventCatchAdd adds and removes one's own fishing catches;
	// EventCatchDeleteAny removes anyone's.
	EventCatchAdd
	EventCatchDeleteAny
	// Q&A (issue #59). The qa module switch is checked by the caller.
	// QuestionCreate also opens new tags. The Own actions are for the
	// author (the caller checks that the content is theirs); the Any
	// actions also need CanActOnContent against the author's current role.
	QuestionCreate
	QuestionEditOwn
	QuestionEditAny
	QuestionDeleteOwn
	QuestionDeleteAny
	QuestionRestore
	AnswerCreate
	AnswerEditOwn
	AnswerEditAny
	AnswerDeleteOwn
	AnswerDeleteAny
	AnswerRestore
	// AnswerVote adds and takes back one's own helpful vote.
	AnswerVote
	TagDelete

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
	RoleStepDown:         "role.step_down",
	AnnouncementCreate:   "announcement.create",
	AnnouncementEdit:     "announcement.edit",
	AnnouncementDelete:   "announcement.delete",
	AnnouncementPin:      "announcement.pin",
	AnnouncementRestore:  "announcement.restore",
	EventView:            "event.view",
	EventCreate:          "event.create",
	EventEditOwn:         "event.edit_own",
	EventEditAny:         "event.edit_any",
	EventCancelOwn:       "event.cancel_own",
	EventCancelAny:       "event.cancel_any",
	EventRSVP:            "event.rsvp",
	EventViewAttendees:   "event.view_attendees",
	EventCheckIn:         "event.check_in",
	EventAttendanceOwn:   "event.attendance_own",
	EventAttendanceAny:   "event.attendance_any",
	EventCatchAdd:        "event.catch_add",
	EventCatchDeleteAny:  "event.catch_delete_any",
	QuestionCreate:       "question.create",
	QuestionEditOwn:      "question.edit_own",
	QuestionEditAny:      "question.edit_any",
	QuestionDeleteOwn:    "question.delete_own",
	QuestionDeleteAny:    "question.delete_any",
	QuestionRestore:      "question.restore",
	AnswerCreate:         "answer.create",
	AnswerEditOwn:        "answer.edit_own",
	AnswerEditAny:        "answer.edit_any",
	AnswerDeleteOwn:      "answer.delete_own",
	AnswerDeleteAny:      "answer.delete_any",
	AnswerRestore:        "answer.restore",
	AnswerVote:           "answer.vote",
	TagDelete:            "tag.delete",
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
	// Members see their own links; callers allowed InviteRevokeAny see all.
	InviteList: Member,
	// Events: reading, answering and the organizer's own event. An organizer
	// keeps managing their event even if the tayfa later stops letting
	// members create events (2026-11-09).
	EventView:          Member,
	EventViewAttendees: Member,
	EventRSVP:          Member,
	EventEditOwn:       Member,
	EventCancelOwn:     Member,
	// Attendance (issue #50): one's own check-in, the organizer's
	// correction of their own event, one's own fishing catches.
	EventCheckIn:       Member,
	EventAttendanceOwn: Member,
	EventCatchAdd:      Member,
	// Q&A (issue #59): every active member asks, answers, votes, opens tags
	// while asking, and edits and deletes their own questions and answers.
	QuestionCreate:    Member,
	QuestionEditOwn:   Member,
	QuestionDeleteOwn: Member,
	AnswerCreate:      Member,
	AnswerEditOwn:     Member,
	AnswerDeleteOwn:   Member,
	AnswerVote:        Member,

	// Moderation: moderator and above.
	// Lowering one's own role; members have nothing to step down to.
	RoleStepDown:      Moderator,
	MemberListPending: Moderator,
	MemberApprove:     Moderator,
	MemberReject:      Moderator,
	MemberMute:        Moderator,
	MemberBan:         Moderator,
	MemberRemove:      Moderator,
	// Announcements are published by moderators and admins only (veri
	// modeli v14, karar 2026-11-09). Editing and deleting need the same
	// role, also for one's own announcement: a demoted author no longer
	// speaks for the tayfa.
	AnnouncementCreate: Moderator,
	AnnouncementEdit:   Moderator,
	AnnouncementDelete: Moderator,
	AnnouncementPin:    Moderator,
	// Events: creating (unless the tayfa setting opens it to members) and
	// managing every event of the tayfa.
	EventCreate:    Moderator,
	EventEditAny:   Moderator,
	EventCancelAny: Moderator,
	// Correcting attendance on every event (v14 "Organizatör düzeltmesi")
	// and removing anyone's fishing catch.
	EventAttendanceAny:  Moderator,
	EventCatchDeleteAny: Moderator,
	// Q&A: someone else's question or answer (with CanActOnContent), and
	// removing a tag from the tayfa.
	QuestionEditAny:   Moderator,
	QuestionDeleteAny: Moderator,
	AnswerEditAny:     Moderator,
	AnswerDeleteAny:   Moderator,
	TagDelete:         Moderator,

	// Tayfa management: admin only.
	TayfaUpdateSettings:  Admin,
	TayfaRotateJoinCode:  Admin,
	TayfaManageModules:   Admin,
	TayfaRequestDeletion: Admin,
	TayfaCancelDeletion:  Admin,
	InviteRevokeAny:      Admin,
	MemberChangeRole:     Admin,
	AuditView:            Admin,
	// Undoing a moderator's or admin's deletion (v14 "Silinen içeriği geri
	// alma").
	AnnouncementRestore: Admin,
	QuestionRestore:     Admin,
	AnswerRestore:       Admin,
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
	EventView:         true,
	// The attendee list is a read.
	EventViewAttendees: true,
	// Stepping down only narrows the caller's own access.
	RoleStepDown: true,
	// Checking in records that one was there; it publishes nothing. A muted
	// member who attends keeps their history, and a hike return (a safety
	// signal) must never be refused.
	EventCheckIn: true,
	// Deleting one's own question or answer publishes nothing and removes
	// one's own words at once (v14 "Silinen içeriği geri alma"); a mute
	// does not keep a member's text in the tayfa against their will.
	QuestionDeleteOwn: true,
	AnswerDeleteOwn:   true,
}

// Settings are the tayfa settings that change the permission table. The zero
// value is the default of a new tayfa.
type Settings struct {
	// MembersCanCreateEvents (tayfas.members_can_create_events) lets every
	// active member perform EventCreate.
	MembersCanCreateEvents bool
}

// minRole returns the lowest role that may perform a under s; ok is false
// for an action missing from the table. A setting can only lower the
// minimum of the action it names, never another one.
func minRole(a Action, s Settings) (need Role, ok bool) {
	need, ok = permissions[a]
	if !ok {
		return 0, false
	}
	if a == EventCreate && s.MembersCanCreateEvents {
		need = Member
	}
	return need, true
}

// Allowed reports whether a user with membership m may perform a at now. Only
// active memberships with a valid role are considered; muted members may only
// perform the actions in allowedWhileMuted.
//
// For actions on another member (mute, ban, remove, role change) callers must
// also check CanActOn, and for role changes CanAssign or CanDemoteSelf.
func Allowed(m Membership, a Action, now time.Time) bool {
	return AllowedIn(m, a, now, Settings{})
}

// AllowedIn is Allowed for a tayfa with settings s. Actions whose rule
// depends on a setting (EventCreate) must be checked with AllowedIn and the
// tayfa's current settings; Allowed assumes the defaults.
func AllowedIn(m Membership, a Action, now time.Time, s Settings) bool {
	if m.Status != StatusActive || !m.Role.Valid() {
		return false
	}
	need, ok := minRole(a, s)
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
// assign a role higher than their own (veri modeli v14, "Rol hiyerarşisi"), so
// an admin can make another admin; that is how a tayfa gets a second admin
// before the first one steps down. The target's current role is checked
// separately with CanActOn: an admin still cannot change another admin.
func CanAssign(actor, newRole Role) bool {
	return actor.Valid() && newRole.Valid() && newRole <= actor
}

// CanDemoteSelf reports whether a member whose role is current may change
// their own role to newRole: stepping down only. This is the only way an
// admin's role changes. The database trigger still refuses the change if it
// would leave the tayfa without an active admin.
func CanDemoteSelf(current, newRole Role) bool {
	return current.Valid() && newRole.Valid() && newRole < current
}

// CanActOnContent reports whether actor may edit or delete content written
// by someone else whose current role in the tayfa is author: the same
// hierarchy as for members, so a moderator cannot change a moderator's or an
// admin's announcement and an admin cannot change another admin's. author
// is 0 when the writer is no longer an active member (left, removed, banned
// or deleted account); such content belongs to the tayfa and any actor with
// a valid role may act on it. Callers check the action itself with Allowed.
func CanActOnContent(actor, author Role) bool {
	if !actor.Valid() {
		return false
	}
	if author == 0 {
		return true
	}
	return CanActOn(actor, author)
}
