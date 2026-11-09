package authz

import (
	"errors"
	"testing"
	"time"
)

var now = time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)

// subject is a named membership used in the expectation matrix.
type subject struct {
	name string
	m    Membership
}

var (
	admin          = subject{"admin", Membership{Role: Admin, Status: StatusActive}}
	moderator      = subject{"moderator", Membership{Role: Moderator, Status: StatusActive}}
	member         = subject{"member", Membership{Role: Member, Status: StatusActive}}
	mutedMember    = subject{"susturulmuş üye", Membership{Role: Member, Status: StatusActive, MutedUntil: now.Add(time.Hour)}}
	mutedModerator = subject{"susturulmuş moderatör", Membership{Role: Moderator, Status: StatusActive, MutedUntil: now.Add(time.Hour)}}
	pending        = subject{"onay bekleyen", Membership{Role: Member, Status: StatusPending}}
	invited        = subject{"davet edilmiş", Membership{Role: Member, Status: StatusInvited}}
	banned         = subject{"banlı", Membership{Role: Member, Status: StatusBanned}}
	nonMember      = subject{"üye değil", Membership{}}
)

// expected is the permission matrix written out by hand. It is deliberately
// not derived from permissions: changing the table must change this list too.
var expected = []struct {
	who  subject
	a    Action
	want bool
}{
	// TayfaView
	{admin, TayfaView, true},
	{moderator, TayfaView, true},
	{member, TayfaView, true},
	{mutedMember, TayfaView, true},
	{mutedModerator, TayfaView, true},
	{pending, TayfaView, false},
	{invited, TayfaView, false},
	{banned, TayfaView, false},
	{nonMember, TayfaView, false},
	// TayfaUpdateSettings
	{admin, TayfaUpdateSettings, true},
	{moderator, TayfaUpdateSettings, false},
	{member, TayfaUpdateSettings, false},
	{mutedMember, TayfaUpdateSettings, false},
	{mutedModerator, TayfaUpdateSettings, false},
	{pending, TayfaUpdateSettings, false},
	{invited, TayfaUpdateSettings, false},
	{banned, TayfaUpdateSettings, false},
	{nonMember, TayfaUpdateSettings, false},
	// TayfaRotateJoinCode
	{admin, TayfaRotateJoinCode, true},
	{moderator, TayfaRotateJoinCode, false},
	{member, TayfaRotateJoinCode, false},
	{mutedMember, TayfaRotateJoinCode, false},
	{mutedModerator, TayfaRotateJoinCode, false},
	{pending, TayfaRotateJoinCode, false},
	{invited, TayfaRotateJoinCode, false},
	{banned, TayfaRotateJoinCode, false},
	{nonMember, TayfaRotateJoinCode, false},
	// TayfaManageModules
	{admin, TayfaManageModules, true},
	{moderator, TayfaManageModules, false},
	{member, TayfaManageModules, false},
	{mutedMember, TayfaManageModules, false},
	{mutedModerator, TayfaManageModules, false},
	{pending, TayfaManageModules, false},
	{invited, TayfaManageModules, false},
	{banned, TayfaManageModules, false},
	{nonMember, TayfaManageModules, false},
	// TayfaRequestDeletion
	{admin, TayfaRequestDeletion, true},
	{moderator, TayfaRequestDeletion, false},
	{member, TayfaRequestDeletion, false},
	{mutedMember, TayfaRequestDeletion, false},
	{mutedModerator, TayfaRequestDeletion, false},
	{pending, TayfaRequestDeletion, false},
	{invited, TayfaRequestDeletion, false},
	{banned, TayfaRequestDeletion, false},
	{nonMember, TayfaRequestDeletion, false},
	// TayfaCancelDeletion
	{admin, TayfaCancelDeletion, true},
	{moderator, TayfaCancelDeletion, false},
	{member, TayfaCancelDeletion, false},
	{mutedMember, TayfaCancelDeletion, false},
	{mutedModerator, TayfaCancelDeletion, false},
	{pending, TayfaCancelDeletion, false},
	{invited, TayfaCancelDeletion, false},
	{banned, TayfaCancelDeletion, false},
	{nonMember, TayfaCancelDeletion, false},
	// InviteCreate
	{admin, InviteCreate, true},
	{moderator, InviteCreate, true},
	{member, InviteCreate, true},
	{mutedMember, InviteCreate, false},
	{mutedModerator, InviteCreate, false},
	{pending, InviteCreate, false},
	{invited, InviteCreate, false},
	{banned, InviteCreate, false},
	{nonMember, InviteCreate, false},
	// InviteList
	{admin, InviteList, true},
	{moderator, InviteList, true},
	{member, InviteList, true},
	{mutedMember, InviteList, true},
	{mutedModerator, InviteList, true},
	{pending, InviteList, false},
	{invited, InviteList, false},
	{banned, InviteList, false},
	{nonMember, InviteList, false},
	// InviteRevokeOwn
	{admin, InviteRevokeOwn, true},
	{moderator, InviteRevokeOwn, true},
	{member, InviteRevokeOwn, true},
	{mutedMember, InviteRevokeOwn, true},
	{mutedModerator, InviteRevokeOwn, true},
	{pending, InviteRevokeOwn, false},
	{invited, InviteRevokeOwn, false},
	{banned, InviteRevokeOwn, false},
	{nonMember, InviteRevokeOwn, false},
	// InviteRevokeAny
	{admin, InviteRevokeAny, true},
	{moderator, InviteRevokeAny, false},
	{member, InviteRevokeAny, false},
	{mutedMember, InviteRevokeAny, false},
	{mutedModerator, InviteRevokeAny, false},
	{pending, InviteRevokeAny, false},
	{invited, InviteRevokeAny, false},
	{banned, InviteRevokeAny, false},
	{nonMember, InviteRevokeAny, false},
	// MemberList
	{admin, MemberList, true},
	{moderator, MemberList, true},
	{member, MemberList, true},
	{mutedMember, MemberList, true},
	{mutedModerator, MemberList, true},
	{pending, MemberList, false},
	{invited, MemberList, false},
	{banned, MemberList, false},
	{nonMember, MemberList, false},
	// MemberListPending
	{admin, MemberListPending, true},
	{moderator, MemberListPending, true},
	{member, MemberListPending, false},
	{mutedMember, MemberListPending, false},
	{mutedModerator, MemberListPending, true},
	{pending, MemberListPending, false},
	{invited, MemberListPending, false},
	{banned, MemberListPending, false},
	{nonMember, MemberListPending, false},
	// MemberApprove
	{admin, MemberApprove, true},
	{moderator, MemberApprove, true},
	{member, MemberApprove, false},
	{mutedMember, MemberApprove, false},
	{mutedModerator, MemberApprove, false},
	{pending, MemberApprove, false},
	{invited, MemberApprove, false},
	{banned, MemberApprove, false},
	{nonMember, MemberApprove, false},
	// MemberReject
	{admin, MemberReject, true},
	{moderator, MemberReject, true},
	{member, MemberReject, false},
	{mutedMember, MemberReject, false},
	{mutedModerator, MemberReject, false},
	{pending, MemberReject, false},
	{invited, MemberReject, false},
	{banned, MemberReject, false},
	{nonMember, MemberReject, false},
	// MemberChangeRole
	{admin, MemberChangeRole, true},
	{moderator, MemberChangeRole, false},
	{member, MemberChangeRole, false},
	{mutedMember, MemberChangeRole, false},
	{mutedModerator, MemberChangeRole, false},
	{pending, MemberChangeRole, false},
	{invited, MemberChangeRole, false},
	{banned, MemberChangeRole, false},
	{nonMember, MemberChangeRole, false},
	// MemberMute
	{admin, MemberMute, true},
	{moderator, MemberMute, true},
	{member, MemberMute, false},
	{mutedMember, MemberMute, false},
	{mutedModerator, MemberMute, false},
	{pending, MemberMute, false},
	{invited, MemberMute, false},
	{banned, MemberMute, false},
	{nonMember, MemberMute, false},
	// MemberBan
	{admin, MemberBan, true},
	{moderator, MemberBan, true},
	{member, MemberBan, false},
	{mutedMember, MemberBan, false},
	{mutedModerator, MemberBan, false},
	{pending, MemberBan, false},
	{invited, MemberBan, false},
	{banned, MemberBan, false},
	{nonMember, MemberBan, false},
	// MemberRemove
	{admin, MemberRemove, true},
	{moderator, MemberRemove, true},
	{member, MemberRemove, false},
	{mutedMember, MemberRemove, false},
	{mutedModerator, MemberRemove, false},
	{pending, MemberRemove, false},
	{invited, MemberRemove, false},
	{banned, MemberRemove, false},
	{nonMember, MemberRemove, false},
	// AuditView
	{admin, AuditView, true},
	{moderator, AuditView, false},
	{member, AuditView, false},
	{mutedMember, AuditView, false},
	{mutedModerator, AuditView, false},
	{pending, AuditView, false},
	{invited, AuditView, false},
	{banned, AuditView, false},
	{nonMember, AuditView, false},
	// RoleStepDown
	{admin, RoleStepDown, true},
	{moderator, RoleStepDown, true},
	{member, RoleStepDown, false},
	{mutedMember, RoleStepDown, false},
	{mutedModerator, RoleStepDown, true},
	{pending, RoleStepDown, false},
	{invited, RoleStepDown, false},
	{banned, RoleStepDown, false},
	{nonMember, RoleStepDown, false},
	// AnnouncementCreate
	{admin, AnnouncementCreate, true},
	{moderator, AnnouncementCreate, true},
	{member, AnnouncementCreate, false},
	{mutedMember, AnnouncementCreate, false},
	{mutedModerator, AnnouncementCreate, false},
	{pending, AnnouncementCreate, false},
	{invited, AnnouncementCreate, false},
	{banned, AnnouncementCreate, false},
	{nonMember, AnnouncementCreate, false},
	// AnnouncementEdit
	{admin, AnnouncementEdit, true},
	{moderator, AnnouncementEdit, true},
	{member, AnnouncementEdit, false},
	{mutedMember, AnnouncementEdit, false},
	{mutedModerator, AnnouncementEdit, false},
	{pending, AnnouncementEdit, false},
	{invited, AnnouncementEdit, false},
	{banned, AnnouncementEdit, false},
	{nonMember, AnnouncementEdit, false},
	// AnnouncementDelete
	{admin, AnnouncementDelete, true},
	{moderator, AnnouncementDelete, true},
	{member, AnnouncementDelete, false},
	{mutedMember, AnnouncementDelete, false},
	{mutedModerator, AnnouncementDelete, false},
	{pending, AnnouncementDelete, false},
	{invited, AnnouncementDelete, false},
	{banned, AnnouncementDelete, false},
	{nonMember, AnnouncementDelete, false},
	// AnnouncementPin
	{admin, AnnouncementPin, true},
	{moderator, AnnouncementPin, true},
	{member, AnnouncementPin, false},
	{mutedMember, AnnouncementPin, false},
	{mutedModerator, AnnouncementPin, false},
	{pending, AnnouncementPin, false},
	{invited, AnnouncementPin, false},
	{banned, AnnouncementPin, false},
	{nonMember, AnnouncementPin, false},
	// AnnouncementRestore
	{admin, AnnouncementRestore, true},
	{moderator, AnnouncementRestore, false},
	{member, AnnouncementRestore, false},
	{mutedMember, AnnouncementRestore, false},
	{mutedModerator, AnnouncementRestore, false},
	{pending, AnnouncementRestore, false},
	{invited, AnnouncementRestore, false},
	{banned, AnnouncementRestore, false},
	{nonMember, AnnouncementRestore, false},
	// EventView
	{admin, EventView, true},
	{moderator, EventView, true},
	{member, EventView, true},
	{mutedMember, EventView, true},
	{mutedModerator, EventView, true},
	{pending, EventView, false},
	{invited, EventView, false},
	{banned, EventView, false},
	{nonMember, EventView, false},
	// EventCreate
	{admin, EventCreate, true},
	{moderator, EventCreate, true},
	{member, EventCreate, false},
	{mutedMember, EventCreate, false},
	{mutedModerator, EventCreate, false},
	{pending, EventCreate, false},
	{invited, EventCreate, false},
	{banned, EventCreate, false},
	{nonMember, EventCreate, false},
	// EventEditOwn
	{admin, EventEditOwn, true},
	{moderator, EventEditOwn, true},
	{member, EventEditOwn, true},
	{mutedMember, EventEditOwn, false},
	{mutedModerator, EventEditOwn, false},
	{pending, EventEditOwn, false},
	{invited, EventEditOwn, false},
	{banned, EventEditOwn, false},
	{nonMember, EventEditOwn, false},
	// EventEditAny
	{admin, EventEditAny, true},
	{moderator, EventEditAny, true},
	{member, EventEditAny, false},
	{mutedMember, EventEditAny, false},
	{mutedModerator, EventEditAny, false},
	{pending, EventEditAny, false},
	{invited, EventEditAny, false},
	{banned, EventEditAny, false},
	{nonMember, EventEditAny, false},
	// EventCancelOwn
	{admin, EventCancelOwn, true},
	{moderator, EventCancelOwn, true},
	{member, EventCancelOwn, true},
	{mutedMember, EventCancelOwn, false},
	{mutedModerator, EventCancelOwn, false},
	{pending, EventCancelOwn, false},
	{invited, EventCancelOwn, false},
	{banned, EventCancelOwn, false},
	{nonMember, EventCancelOwn, false},
	// EventCancelAny
	{admin, EventCancelAny, true},
	{moderator, EventCancelAny, true},
	{member, EventCancelAny, false},
	{mutedMember, EventCancelAny, false},
	{mutedModerator, EventCancelAny, false},
	{pending, EventCancelAny, false},
	{invited, EventCancelAny, false},
	{banned, EventCancelAny, false},
	{nonMember, EventCancelAny, false},
	// EventRSVP
	{admin, EventRSVP, true},
	{moderator, EventRSVP, true},
	{member, EventRSVP, true},
	{mutedMember, EventRSVP, false},
	{mutedModerator, EventRSVP, false},
	{pending, EventRSVP, false},
	{invited, EventRSVP, false},
	{banned, EventRSVP, false},
	{nonMember, EventRSVP, false},
	// EventViewAttendees
	{admin, EventViewAttendees, true},
	{moderator, EventViewAttendees, true},
	{member, EventViewAttendees, true},
	{mutedMember, EventViewAttendees, true},
	{mutedModerator, EventViewAttendees, true},
	{pending, EventViewAttendees, false},
	{invited, EventViewAttendees, false},
	{banned, EventViewAttendees, false},
	{nonMember, EventViewAttendees, false},
	// EventCheckIn
	{admin, EventCheckIn, true},
	{moderator, EventCheckIn, true},
	{member, EventCheckIn, true},
	{mutedMember, EventCheckIn, true},
	{mutedModerator, EventCheckIn, true},
	{pending, EventCheckIn, false},
	{invited, EventCheckIn, false},
	{banned, EventCheckIn, false},
	{nonMember, EventCheckIn, false},
	// EventAttendanceOwn
	{admin, EventAttendanceOwn, true},
	{moderator, EventAttendanceOwn, true},
	{member, EventAttendanceOwn, true},
	{mutedMember, EventAttendanceOwn, false},
	{mutedModerator, EventAttendanceOwn, false},
	{pending, EventAttendanceOwn, false},
	{invited, EventAttendanceOwn, false},
	{banned, EventAttendanceOwn, false},
	{nonMember, EventAttendanceOwn, false},
	// EventAttendanceAny
	{admin, EventAttendanceAny, true},
	{moderator, EventAttendanceAny, true},
	{member, EventAttendanceAny, false},
	{mutedMember, EventAttendanceAny, false},
	{mutedModerator, EventAttendanceAny, false},
	{pending, EventAttendanceAny, false},
	{invited, EventAttendanceAny, false},
	{banned, EventAttendanceAny, false},
	{nonMember, EventAttendanceAny, false},
	// EventCatchAdd
	{admin, EventCatchAdd, true},
	{moderator, EventCatchAdd, true},
	{member, EventCatchAdd, true},
	{mutedMember, EventCatchAdd, false},
	{mutedModerator, EventCatchAdd, false},
	{pending, EventCatchAdd, false},
	{invited, EventCatchAdd, false},
	{banned, EventCatchAdd, false},
	{nonMember, EventCatchAdd, false},
	// EventCatchDeleteAny
	{admin, EventCatchDeleteAny, true},
	{moderator, EventCatchDeleteAny, true},
	{member, EventCatchDeleteAny, false},
	{mutedMember, EventCatchDeleteAny, false},
	{mutedModerator, EventCatchDeleteAny, false},
	{pending, EventCatchDeleteAny, false},
	{invited, EventCatchDeleteAny, false},
	{banned, EventCatchDeleteAny, false},
	{nonMember, EventCatchDeleteAny, false},
}

func TestAllowedMatrix(t *testing.T) {
	for _, tt := range expected {
		if got := Allowed(tt.who.m, tt.a, now); got != tt.want {
			t.Errorf("%s / %s: Allowed = %v, %v bekleniyordu", tt.who.name, tt.a, got, tt.want)
		}
	}
}

// TestEveryActionCovered fails when an action is added without a permission
// entry, a name, or rows in the expectation matrix.
func TestEveryActionCovered(t *testing.T) {
	subjects := []subject{admin, moderator, member, mutedMember, mutedModerator, pending, invited, banned, nonMember}
	covered := map[Action]map[string]bool{}
	for _, tt := range expected {
		if covered[tt.a] == nil {
			covered[tt.a] = map[string]bool{}
		}
		covered[tt.a][tt.who.name] = true
	}
	names := map[string]bool{}
	for a := Action(1); a < actionEnd; a++ {
		if _, ok := permissions[a]; !ok {
			t.Errorf("işlem %d (%q) izin tablosunda yok", a, a)
		}
		if a.String() == "" {
			t.Errorf("işlem %d adsız", a)
		} else if names[a.String()] {
			t.Errorf("işlem adı %q tekrarlanıyor", a)
		}
		names[a.String()] = true
		for _, s := range subjects {
			if !covered[a][s.name] {
				t.Errorf("%s / %s: beklenen sonuç tabloda yazılı değil", s.name, a)
			}
		}
	}
	if len(permissions) != int(actionEnd)-1 {
		t.Errorf("izin tablosunda %d kayıt var, %d işlem tanımlı", len(permissions), int(actionEnd)-1)
	}
	for a := range allowedWhileMuted {
		if a <= 0 || a >= actionEnd {
			t.Errorf("susturma istisnasında geçersiz işlem %d", a)
		}
	}
}

func TestAllowedDefaultDeny(t *testing.T) {
	for _, a := range []Action{0, actionEnd, actionEnd + 1, -1} {
		if Allowed(admin.m, a, now) {
			t.Errorf("tanımsız işlem %d yöneticiye açık", a)
		}
	}
	for _, r := range []Role{0, -1, 4} {
		m := Membership{Role: r, Status: StatusActive}
		if Allowed(m, TayfaView, now) {
			t.Errorf("geçersiz rol %d ile TayfaView açık", r)
		}
	}
	if Allowed(Membership{Role: Admin, Status: "ACTIVE"}, TayfaView, now) {
		t.Error("bilinmeyen durum kabul edildi")
	}
}

func TestAllowedStatusOverridesRole(t *testing.T) {
	for _, st := range []Status{StatusInvited, StatusPending, StatusBanned, ""} {
		m := Membership{Role: Admin, Status: st}
		for a := Action(1); a < actionEnd; a++ {
			if Allowed(m, a, now) {
				t.Errorf("durumu %q olan yönetici %s yapabiliyor", st, a)
			}
		}
	}
}

func TestMuteWindow(t *testing.T) {
	tests := []struct {
		name  string
		until time.Time
		want  bool // InviteCreate allowed
	}{
		{"susturma yok", time.Time{}, true},
		{"süresi dolmuş", now.Add(-time.Second), true},
		{"tam şimdi bitiyor", now, true},
		{"bir saniye kaldı", now.Add(time.Second), false},
		{"uzun süreli", now.Add(30 * 24 * time.Hour), false},
	}
	for _, tt := range tests {
		m := Membership{Role: Member, Status: StatusActive, MutedUntil: tt.until}
		if got := Allowed(m, InviteCreate, now); got != tt.want {
			t.Errorf("%s: InviteCreate = %v, %v bekleniyordu", tt.name, got, tt.want)
		}
	}
	// The same membership is judged by the now that is passed in.
	m := Membership{Role: Member, Status: StatusActive, MutedUntil: now}
	if Allowed(m, InviteCreate, now.Add(-time.Minute)) {
		t.Error("susturma bitmeden önce davet açık")
	}
}

func TestMutedAdmin(t *testing.T) {
	m := Membership{Role: Admin, Status: StatusActive, MutedUntil: now.Add(time.Hour)}
	want := map[Action]bool{
		TayfaView:         true,
		InviteList:        true,
		InviteRevokeOwn:   true,
		MemberList:        true,
		MemberListPending: true,
		AuditView:         true,
		RoleStepDown:      true,
		EventView:         true,
		// Reads only: a muted admin can neither create, edit, cancel nor
		// answer events.
		EventViewAttendees: true,
		// Checking in publishes nothing; a hike return is a safety signal.
		EventCheckIn: true,
	}
	for a := Action(1); a < actionEnd; a++ {
		if got := Allowed(m, a, now); got != want[a] {
			t.Errorf("susturulmuş yönetici / %s: %v, %v bekleniyordu", a, got, want[a])
		}
	}
}

func TestCanActOn(t *testing.T) {
	tests := []struct {
		actor, target Role
		want          bool
	}{
		{Admin, Admin, false},
		{Admin, Moderator, true},
		{Admin, Member, true},
		{Moderator, Admin, false},
		{Moderator, Moderator, false},
		{Moderator, Member, true},
		{Member, Admin, false},
		{Member, Moderator, false},
		{Member, Member, false},
		{Admin, 0, false},
		{4, Admin, false},
		{0, -1, false},
	}
	for _, tt := range tests {
		if got := CanActOn(tt.actor, tt.target); got != tt.want {
			t.Errorf("CanActOn(%d, %d) = %v", tt.actor, tt.target, got)
		}
	}
}

func TestCanAssign(t *testing.T) {
	tests := []struct {
		actor, newRole Role
		want           bool
	}{
		{Admin, Admin, true},
		{Admin, Moderator, true},
		{Admin, Member, true},
		{Moderator, Admin, false},
		{Moderator, Moderator, true},
		{Moderator, Member, true},
		{Member, Admin, false},
		{Member, Moderator, false},
		{Member, Member, true},
		{Admin, 0, false},
		{4, Admin, false},
	}
	for _, tt := range tests {
		if got := CanAssign(tt.actor, tt.newRole); got != tt.want {
			t.Errorf("CanAssign(%d, %d) = %v", tt.actor, tt.newRole, got)
		}
	}
}

func TestCanDemoteSelf(t *testing.T) {
	tests := []struct {
		current, newRole Role
		want             bool
	}{
		{Admin, Admin, false},
		{Admin, Moderator, true},
		{Admin, Member, true},
		{Moderator, Admin, false},
		{Moderator, Moderator, false},
		{Moderator, Member, true},
		{Member, Admin, false},
		{Member, Moderator, false},
		{Member, Member, false},
		{Admin, 0, false},
		{4, Admin, false},
	}
	for _, tt := range tests {
		if got := CanDemoteSelf(tt.current, tt.newRole); got != tt.want {
			t.Errorf("CanDemoteSelf(%d, %d) = %v", tt.current, tt.newRole, got)
		}
	}
}

func TestParseRole(t *testing.T) {
	for s, want := range map[string]Role{"admin": Admin, "moderator": Moderator, "member": Member} {
		got, err := ParseRole(s)
		if err != nil || got != want {
			t.Errorf("ParseRole(%q) = %v, %v", s, got, err)
		}
		if got.String() != s {
			t.Errorf("%v.String() = %q", got, got.String())
		}
	}
	for _, s := range []string{"", "Admin", "owner", " admin", "3"} {
		if _, err := ParseRole(s); !errors.Is(err, ErrUnknownRole) {
			t.Errorf("ParseRole(%q) kabul edildi", s)
		}
	}
	if Role(0).String() != "" || Role(4).String() != "" {
		t.Error("geçersiz rolün adı var")
	}
}

func TestParseStatus(t *testing.T) {
	for _, s := range []string{"invited", "pending", "active", "banned"} {
		got, err := ParseStatus(s)
		if err != nil || string(got) != s {
			t.Errorf("ParseStatus(%q) = %v, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "Active", "muted", "removed"} {
		if _, err := ParseStatus(s); !errors.Is(err, ErrUnknownStatus) {
			t.Errorf("ParseStatus(%q) kabul edildi", s)
		}
	}
}

func TestCanActOnContent(t *testing.T) {
	tests := []struct {
		actor, author Role
		want          bool
	}{
		{Admin, Admin, false},
		{Admin, Moderator, true},
		{Admin, Member, true},
		{Admin, 0, true},
		{Moderator, Admin, false},
		{Moderator, Moderator, false},
		{Moderator, Member, true},
		{Moderator, 0, true},
		{Member, Member, false},
		{Member, 0, true},
		{0, 0, false},
		{4, 0, false},
		{Admin, 4, false},
		{Admin, -1, false},
	}
	for _, tt := range tests {
		if got := CanActOnContent(tt.actor, tt.author); got != tt.want {
			t.Errorf("CanActOnContent(%d, %d) = %v", tt.actor, tt.author, got)
		}
	}
}

// TestSettingsMatrix: with members_can_create_events every active, unmuted
// member may create events; the setting changes no other action.
func TestSettingsMatrix(t *testing.T) {
	on := Settings{MembersCanCreateEvents: true}
	want := map[string]bool{
		admin.name: true, moderator.name: true, member.name: true,
		mutedMember.name: false, mutedModerator.name: false,
		pending.name: false, invited.name: false, banned.name: false, nonMember.name: false,
	}
	for _, s := range []subject{admin, moderator, member, mutedMember, mutedModerator, pending, invited, banned, nonMember} {
		if got := AllowedIn(s.m, EventCreate, now, on); got != want[s.name] {
			t.Errorf("ayar açık, %s / %s: AllowedIn = %v, %v bekleniyordu", s.name, EventCreate, got, want[s.name])
		}
		if got := AllowedIn(s.m, EventCreate, now, Settings{}); got != Allowed(s.m, EventCreate, now) {
			t.Errorf("ayar kapalı, %s: AllowedIn ile Allowed farklı", s.name)
		}
	}
	// Every other action answers the same with the setting on.
	for _, tt := range expected {
		if tt.a == EventCreate {
			continue
		}
		if got := AllowedIn(tt.who.m, tt.a, now, on); got != tt.want {
			t.Errorf("ayar açık, %s / %s: AllowedIn = %v, %v bekleniyordu", tt.who.name, tt.a, got, tt.want)
		}
	}
}
