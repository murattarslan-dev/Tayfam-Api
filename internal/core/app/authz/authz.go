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
	// or online check-in and their roll call start and return (roll_call
	// component, issue #242).
	EventCheckIn
	// EventAttendanceOwn is the organizer's correction on their own event
	// (the caller checks that the event is theirs); EventAttendanceAny is
	// the same on every event of the tayfa.
	EventAttendanceOwn
	EventAttendanceAny
	// EventLogAdd adds and removes one's own entries of a log component
	// (issue #243; the caller also checks verified attendance);
	// EventLogDeleteAny removes anyone's.
	EventLogAdd
	EventLogDeleteAny
	// Event album (issue #206, ADR-0028). EventPhotoAdd adds a photo (the
	// caller also checks verified attendance, the window and the limits);
	// viewing is EventView. EventPhotoDeleteOwn removes one's own photo;
	// EventPhotoDeleteOwnEvent is the organizer's removal of any photo of
	// their own event (the caller checks that the event is theirs);
	// EventPhotoDeleteAny removes any photo of the tayfa. "Her fotoğrafı":
	// no role hierarchy applies.
	EventPhotoAdd
	EventPhotoDeleteOwn
	EventPhotoDeleteOwnEvent
	EventPhotoDeleteAny
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
	// Polls (issue #60). The poll module switch is checked by the caller.
	PollView
	PollCreate
	PollVote
	// PollViewVoters lists who voted for an option of an open (not
	// anonymous) poll. Anonymous polls never answer it, whatever the role.
	PollViewVoters
	// PollCloseOwn and PollDeleteOwn are for the poll's creator (the caller
	// checks that the poll is theirs); the Any actions are for every poll
	// of the tayfa, above the creator's current role (CanActOnContent).
	PollCloseOwn
	PollCloseAny
	PollDeleteOwn
	PollDeleteAny
	// Direct invitations (issue #71, v14 "Tayfaya davet"). InviteDirect
	// invites a friend into the tayfa (the caller checks the friendship and
	// the daily limit); InviteDirectCancel withdraws anyone's unanswered
	// invitation and lists them.
	InviteDirect
	InviteDirectCancel
	// Reports (issue #76). ReportCreate files a report about a member, the
	// tayfa or content of it; ReportQueueView reads the tayfa's moderation
	// queue and ReportResolve closes a report of it. Reports about the
	// caller and those that go to the platform only never reach the queue
	// (the caller checks).
	ReportCreate
	ReportQueueView
	ReportResolve
	// Channels (issue #81). chat asks core with AuthorizeChannelAction; the
	// channels and messages are chat's data. ChannelView reads members
	// channels and their messages; ChannelViewStaff also sees staff
	// channels. ChannelManage opens, edits and archives channels.
	// ChannelPost writes a message; ChannelSkipSlowMode writes without
	// waiting for slow mode. ChannelDeleteOwn erases one's own message;
	// ChannelModerate hides someone else's (with CanActOnContent against
	// the author's current role) and ChannelRestore undoes that.
	// ChannelMarkRead moves one's own read marker.
	ChannelView
	ChannelViewStaff
	ChannelManage
	ChannelPost
	ChannelSkipSlowMode
	ChannelDeleteOwn
	ChannelModerate
	ChannelRestore
	ChannelMarkRead
	// TayfaConvert turns an instant tayfa into a standard one ("Bu ekiple
	// tayfa kur", ADR-0032). Allowed only with Settings.Instant.
	TayfaConvert
	// Keşfet (issue #210, ADR-0033). TayfaDiscoverManage turns Keşfet on
	// or off and reads its state. EventFeedback is one's own "Was
	// everything all right?" answer; EventSafetyContact is one's own
	// safety contact and "I'm back". Both are safety signals that publish
	// nothing, so a mute never refuses them.
	TayfaDiscoverManage
	EventFeedback
	EventSafetyContact
	// Event components (issue #238; ADR-0025 §5). These are the roles every
	// component shares; the component framework (package component) asks
	// them, and a component adds an action of its own only for a rule
	// these do not express. The events module switch, the component being
	// part of the event's type version and switched on, and the write
	// window are checked by the framework.
	//
	// EventComponentView reads a component's data and the event's
	// selections and referees.
	EventComponentView
	// EventComponentParticipate is a participant's own entry in a
	// component (taking a list item, a personal measurement, a question, a
	// vote). The component checks the answer or the verified attendance it
	// needs.
	EventComponentParticipate
	// EventComponentManageOwn is the organizer's part on their own event
	// (the caller checks that the event is theirs): the selections,
	// building teams, drawing lots, starting a bracket, total
	// measurements, correcting scores. EventComponentManageAny is the same
	// on every event of the tayfa.
	EventComponentManageOwn
	EventComponentManageAny
	// EventRefereeAssignOwn assigns and removes the referees of one's own
	// event; EventRefereeAssignAny those of every event of the tayfa.
	EventRefereeAssignOwn
	EventRefereeAssignAny
	// EventComponentReferee is entering results as an assigned referee
	// (the caller checks the assignment, event_referees).
	EventComponentReferee
	// TayfaVocabularyMerge merges two values of the tayfa vocabulary and
	// undoes a merge (issue #244, ADR-0037 §4). Reading the vocabulary is
	// TayfaView.
	TayfaVocabularyMerge
	// EventGuestAdd adds a guest player to an event's scoreboard as its
	// organizer or an assigned referee (issue #252, ADR-0037 §6; the caller
	// checks the role, event_referees). Removing a guest is
	// EventComponentManageOwn / EventComponentManageAny.
	EventGuestAdd
	// EventQuestionAsk asks, votes for and removes one's own questions of a
	// questions component (issue #256, ADR-0025 §5: "Soru gönder / oyla:
	// Katılımcı"; the caller also checks going or a verified attendance).
	// Marking a question answered is EventComponentManageOwn /
	// EventComponentManageAny. EventQuestionDeleteAny removes anyone's
	// question, an anonymous one without learning its author.
	EventQuestionAsk
	EventQuestionDeleteAny
	// Şimdi başlat (issue #263, ADR-0038 §2). EventCreateNow creates an
	// event that starts now (starts_now); its minimum role is the tayfa's
	// setting Settings.NowCreateMinRole (member by default), independent
	// of EventCreate. EventFinish is the organizer's "Bitir" on their own
	// open-ended event (the caller checks that the event is theirs).
	EventCreateNow
	EventFinish
	// QR ile katılım (issue #264, ADR-0038 §3). EventJoinQRCreate opens a
	// join QR for the caller's own event (the caller checks that the event
	// is theirs); EventJoinQRCreateAny for every event of the tayfa.
	EventJoinQRCreate
	EventJoinQRCreateAny

	actionEnd // sentinel; keep last
)

// actionNames are the stable names used in logs and audit records.
var actionNames = [...]string{
	TayfaView:                "tayfa.view",
	TayfaUpdateSettings:      "tayfa.update_settings",
	TayfaRotateJoinCode:      "tayfa.rotate_join_code",
	TayfaManageModules:       "tayfa.manage_modules",
	TayfaRequestDeletion:     "tayfa.request_deletion",
	TayfaCancelDeletion:      "tayfa.cancel_deletion",
	InviteCreate:             "invite.create",
	InviteList:               "invite.list",
	InviteRevokeOwn:          "invite.revoke_own",
	InviteRevokeAny:          "invite.revoke_any",
	MemberList:               "member.list",
	MemberListPending:        "member.list_pending",
	MemberApprove:            "member.approve",
	MemberReject:             "member.reject",
	MemberChangeRole:         "member.change_role",
	MemberMute:               "member.mute",
	MemberBan:                "member.ban",
	MemberRemove:             "member.remove",
	AuditView:                "audit.view",
	RoleStepDown:             "role.step_down",
	AnnouncementCreate:       "announcement.create",
	AnnouncementEdit:         "announcement.edit",
	AnnouncementDelete:       "announcement.delete",
	AnnouncementPin:          "announcement.pin",
	AnnouncementRestore:      "announcement.restore",
	EventView:                "event.view",
	EventCreate:              "event.create",
	EventEditOwn:             "event.edit_own",
	EventEditAny:             "event.edit_any",
	EventCancelOwn:           "event.cancel_own",
	EventCancelAny:           "event.cancel_any",
	EventRSVP:                "event.rsvp",
	EventViewAttendees:       "event.view_attendees",
	EventCheckIn:             "event.check_in",
	EventAttendanceOwn:       "event.attendance_own",
	EventAttendanceAny:       "event.attendance_any",
	EventLogAdd:              "event.log_add",
	EventLogDeleteAny:        "event.log_delete_any",
	EventPhotoAdd:            "event.photo_add",
	EventPhotoDeleteOwn:      "event.photo_delete_own",
	EventPhotoDeleteOwnEvent: "event.photo_delete_own_event",
	EventPhotoDeleteAny:      "event.photo_delete_any",
	QuestionCreate:           "question.create",
	QuestionEditOwn:          "question.edit_own",
	QuestionEditAny:          "question.edit_any",
	QuestionDeleteOwn:        "question.delete_own",
	QuestionDeleteAny:        "question.delete_any",
	QuestionRestore:          "question.restore",
	AnswerCreate:             "answer.create",
	AnswerEditOwn:            "answer.edit_own",
	AnswerEditAny:            "answer.edit_any",
	AnswerDeleteOwn:          "answer.delete_own",
	AnswerDeleteAny:          "answer.delete_any",
	AnswerRestore:            "answer.restore",
	AnswerVote:               "answer.vote",
	TagDelete:                "tag.delete",
	PollView:                 "poll.view",
	PollCreate:               "poll.create",
	PollVote:                 "poll.vote",
	PollViewVoters:           "poll.view_voters",
	PollCloseOwn:             "poll.close_own",
	PollCloseAny:             "poll.close_any",
	PollDeleteOwn:            "poll.delete_own",
	PollDeleteAny:            "poll.delete_any",
	InviteDirect:             "invite.direct",
	InviteDirectCancel:       "invite.direct_cancel",
	ReportCreate:             "report.create",
	ReportQueueView:          "report.queue_view",
	ReportResolve:            "report.resolve",
	ChannelView:              "channel.view",
	ChannelViewStaff:         "channel.view_staff",
	ChannelManage:            "channel.manage",
	ChannelPost:              "channel.post",
	ChannelSkipSlowMode:      "channel.skip_slow_mode",
	ChannelDeleteOwn:         "channel.delete_own",
	ChannelModerate:          "channel.moderate",
	ChannelRestore:           "channel.restore",
	ChannelMarkRead:          "channel.mark_read",
	TayfaConvert:             "tayfa.convert",
	TayfaDiscoverManage:      "tayfa.discover_manage",
	EventFeedback:            "event.feedback",
	EventSafetyContact:       "event.safety_contact",
	// Event components (issue #238).
	EventComponentView:        "event.component_view",
	EventComponentParticipate: "event.component_participate",
	EventComponentManageOwn:   "event.component_manage_own",
	EventComponentManageAny:   "event.component_manage_any",
	EventRefereeAssignOwn:     "event.referee_assign_own",
	EventRefereeAssignAny:     "event.referee_assign_any",
	EventComponentReferee:     "event.component_referee",
	TayfaVocabularyMerge:      "tayfa.vocabulary.merge",
	EventGuestAdd:             "event.guest_add",
	EventQuestionAsk:          "event.question_ask",
	EventQuestionDeleteAny:    "event.question_delete_any",
	EventCreateNow:            "event.create_now",
	EventFinish:               "event.finish",
	EventJoinQRCreate:         "event.join_qr_create",
	EventJoinQRCreateAny:      "event.join_qr_create_any",
	actionEnd:                 "",
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
	// correction of their own event, one's own log entries.
	EventCheckIn:       Member,
	EventAttendanceOwn: Member,
	// Keşfet safety net (ADR-0033): one's own feedback and safety contact.
	EventFeedback:      Member,
	EventSafetyContact: Member,
	EventLogAdd:        Member,
	// Event album (issue #206, Notion Faz 2 §2): a verified attendee adds
	// photos; the uploader removes their own and the organizer any photo
	// of their own event.
	EventPhotoAdd:            Member,
	EventPhotoDeleteOwn:      Member,
	EventPhotoDeleteOwnEvent: Member,
	// Q&A (issue #59): every active member asks, answers, votes, opens tags
	// while asking, and edits and deletes their own questions and answers.
	QuestionCreate:    Member,
	QuestionEditOwn:   Member,
	QuestionDeleteOwn: Member,
	AnswerCreate:      Member,
	AnswerEditOwn:     Member,
	AnswerDeleteOwn:   Member,
	AnswerVote:        Member,
	// Polls (issue #60, v14 "Anket (MVP)"): every active member creates
	// polls and votes; the creator closes and deletes their own poll.
	PollView:       Member,
	PollCreate:     Member,
	PollVote:       Member,
	PollViewVoters: Member,
	PollCloseOwn:   Member,
	PollDeleteOwn:  Member,
	// Every active member invites friends directly, like invite links (v14
	// "Tayfaya davet"); a muted member cannot.
	InviteDirect: Member,
	// Every active member reports (Topluluk Kuralları: "Her kullanıcı
	// şikâyet eder").
	ReportCreate: Member,
	// Channels (issue #81, v14 "Sohbet"): every active member reads members
	// channels, writes, erases their own messages and marks read.
	ChannelView:      Member,
	ChannelPost:      Member,
	ChannelDeleteOwn: Member,
	ChannelMarkRead:  Member,
	// Event components (issue #238, ADR-0025 §5): every active member
	// reads them and takes part; the organizer manages the components and
	// referees of their own event; an assigned referee enters results.
	EventComponentView:        Member,
	EventComponentParticipate: Member,
	EventComponentManageOwn:   Member,
	EventRefereeAssignOwn:     Member,
	EventComponentReferee:     Member,
	// Guest players (issue #252): the organizer or an assigned referee.
	EventGuestAdd: Member,
	// Event questions (issue #256): a participant asks and votes.
	EventQuestionAsk: Member,
	// Şimdi başlat (ADR-0038 §2): every active member by default; the
	// tayfa's now_create_min_role raises it (minRole). The organizer
	// finishes their own open-ended event.
	EventCreateNow: Member,
	EventFinish:    Member,
	// QR ile katılım (ADR-0038 §3): the organizer for their own event.
	EventJoinQRCreate: Member,

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
	// and removing anyone's log entry.
	EventAttendanceAny: Moderator,
	EventLogDeleteAny:  Moderator,
	// "Yönetim her fotoğrafı" (Notion Faz 2 §2).
	EventPhotoDeleteAny: Moderator,
	// Components and referees of every event, like editing every event.
	EventComponentManageAny: Moderator,
	EventRefereeAssignAny:   Moderator,
	// Merging duplicates of the tayfa vocabulary (ADR-0037 §4).
	TayfaVocabularyMerge: Moderator,
	// Removing anyone's event question ("Tayfa yönetimi anonim soruyu
	// yazarını görmeden silebilir", ADR-0025 §5).
	EventQuestionDeleteAny: Moderator,
	// Q&A: someone else's question or answer (with CanActOnContent), and
	// removing a tag from the tayfa.
	QuestionEditAny:   Moderator,
	QuestionDeleteAny: Moderator,
	AnswerEditAny:     Moderator,
	AnswerDeleteAny:   Moderator,
	TagDelete:         Moderator,
	// Closing early and deleting anyone's poll.
	PollCloseAny:  Moderator,
	PollDeleteAny: Moderator,
	// The moderation queue (Topluluk Kuralları: "Yöneticiler ve
	// moderatörler kendi tayfalarındaki şikâyetleri makul sürede ele alır").
	ReportQueueView: Moderator,
	ReportResolve:   Moderator,
	// Channels: only moderators and admins see staff channels and open
	// channels (v14 "Sohbet"), are exempt from slow mode (v14 "Spam ve
	// taciz") and hide others' messages.
	ChannelViewStaff:    Moderator,
	ChannelManage:       Moderator,
	ChannelSkipSlowMode: Moderator,
	ChannelModerate:     Moderator,

	// Tayfa management: admin only.
	TayfaUpdateSettings:  Admin,
	TayfaRotateJoinCode:  Admin,
	TayfaManageModules:   Admin,
	TayfaRequestDeletion: Admin,
	TayfaCancelDeletion:  Admin,
	InviteRevokeAny:      Admin,
	// Keşfet: the admins apply (ADR-0033 §14).
	TayfaDiscoverManage: Admin,
	// "Yönetici her daveti ve davet linkini iptal edebilir" (v14).
	InviteDirectCancel: Admin,
	MemberChangeRole:   Admin,
	AuditView:          Admin,
	// Undoing a moderator's or admin's deletion (v14 "Silinen içeriği geri
	// alma").
	AnnouncementRestore: Admin,
	QuestionRestore:     Admin,
	AnswerRestore:       Admin,
	ChannelRestore:      Admin,
	// "Bu ekiple tayfa kur" is the organizer's (ADR-0032); minRole refuses
	// it outside instant tayfas.
	TayfaConvert: Admin,
	// A join QR for any event of the tayfa (ADR-0038 §3: "ve tayfa
	// etkinliğinde yöneticiler").
	EventJoinQRCreateAny: Admin,
}

// nobody is the minimum role of an action no role may perform.
const nobody Role = Admin + 1

// instantRules overrides the minimum role of actions in an instant tayfa
// (tayfas.kind = 'instant', ADR-0032 §3): the hidden single-event tayfa of
// an instant event. Its organizer is the only admin and the only one who
// invites; the name comes from the event title, the event is the only one
// and only the events module is on.
var instantRules = map[Action]Role{
	TayfaUpdateSettings: nobody,
	TayfaRotateJoinCode: nobody,
	TayfaManageModules:  nobody,
	EventCreate:         nobody,
	// An instant now event is created with the instant tayfa
	// (POST /v1/instant-events, ADR-0038 §2), never inside it.
	EventCreateNow: nobody,
	// No moderators: the organizer alone approves join requests.
	MemberChangeRole: nobody,
	InviteCreate:     Admin,
	InviteDirect:     Admin,
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
	// member who attends keeps their history, and a roll call return (a
	// safety signal) must never be refused.
	EventCheckIn: true,
	// Deleting one's own question or answer publishes nothing and removes
	// one's own words at once (v14 "Silinen içeriği geri alma"); a mute
	// does not keep a member's text in the tayfa against their will.
	QuestionDeleteOwn: true,
	AnswerDeleteOwn:   true,
	// Removing one's own album photo likewise: a mute does not keep a
	// member's picture in the tayfa against their will (ADR-0028).
	EventPhotoDeleteOwn: true,
	// Reading polls and their open voter lists. Creating and voting are
	// refused while muted (v14 "Anket (MVP)").
	PollView:       true,
	PollViewVoters: true,
	// A mute must never silence a safety report: filing one publishes
	// nothing to the tayfa. Reading the queue is a read; resolving is not.
	ReportCreate:    true,
	ReportQueueView: true,
	// Channels: a muted member reads (staff channels too, if their role
	// sees them), marks read and erases their own messages ("okur ama
	// yazamaz"; one's own words are not kept against one's will). Writing,
	// managing and moderating are refused.
	ChannelView:      true,
	ChannelViewStaff: true,
	ChannelDeleteOwn: true,
	ChannelMarkRead:  true,
	// Keşfet safety net (ADR-0033 §14): feedback and the safety contact
	// publish nothing; "I'm back" must never be refused.
	EventFeedback:      true,
	EventSafetyContact: true,
	// Reading components is a read. Taking part, managing and refereeing
	// publish into the event and are refused while muted.
	EventComponentView: true,
}

// Settings are the tayfa settings that change the permission table. The zero
// value is the default of a new tayfa.
type Settings struct {
	// MembersCanCreateEvents (tayfas.members_can_create_events) lets every
	// active member perform EventCreate.
	MembersCanCreateEvents bool
	// Instant (tayfas.kind = 'instant') applies instantRules and allows
	// TayfaConvert (ADR-0032).
	Instant bool
	// NowCreateMinRole (tayfas.now_create_min_role) is the lowest role
	// that may perform EventCreateNow (ADR-0038 §2). The zero value is the
	// default, Member.
	NowCreateMinRole Role
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
	if a == EventCreateNow && s.NowCreateMinRole.Valid() {
		need = s.NowCreateMinRole
	}
	if s.Instant {
		if r, ok := instantRules[a]; ok {
			need = r
		}
	} else if a == TayfaConvert {
		return 0, false
	}
	if need == nobody {
		return 0, false
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
// depends on a setting (EventCreate, EventCreateNow, and every action in
// instantRules) must
// be checked with AllowedIn and the tayfa's current settings; Allowed
// assumes a standard tayfa with the defaults.
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
