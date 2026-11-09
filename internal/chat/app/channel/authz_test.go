package channel

import (
	"testing"
	"time"
)

// TestPermissionTable pins every role and action (CLAUDE.md: the permission
// table is protected by table-driven tests).
func TestPermissionTable(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	muted := now.Add(time.Hour)
	var (
		admin          = Member{Active: true, Role: RoleAdmin}
		moderator      = Member{Active: true, Role: RoleModerator}
		member         = Member{Active: true, Role: RoleMember}
		mutedMember    = Member{Active: true, Role: RoleMember, MutedUntil: muted}
		mutedModerator = Member{Active: true, Role: RoleModerator, MutedUntil: muted}
		muteOver       = Member{Active: true, Role: RoleMember, MutedUntil: now}
		inactiveAdmin  = Member{Role: RoleAdmin}
		unknownRole    = Member{Active: true, Role: 4}
		noRole         = Member{Active: true}
	)
	all := []Action{
		ActionView, ActionViewStaff, ActionManage, ActionSend, ActionSkipSlowMode,
		ActionDeleteOwn, ActionDeleteOthers, ActionRestore, ActionMarkRead,
	}
	want := map[Action][]Member{
		ActionView:         {admin, moderator, member, mutedMember, mutedModerator, muteOver},
		ActionViewStaff:    {admin, moderator, mutedModerator},
		ActionManage:       {admin, moderator},
		ActionSend:         {admin, moderator, member, muteOver},
		ActionSkipSlowMode: {admin, moderator, mutedModerator},
		ActionDeleteOwn:    {admin, moderator, member, mutedMember, mutedModerator, muteOver},
		ActionDeleteOthers: {admin, moderator},
		ActionRestore:      {admin},
		ActionMarkRead:     {admin, moderator, member, mutedMember, mutedModerator, muteOver},
	}
	everyone := []Member{admin, moderator, member, mutedMember, mutedModerator, muteOver, inactiveAdmin, unknownRole, noRole}
	for _, a := range all {
		for _, m := range everyone {
			expected := false
			for _, w := range want[a] {
				if w == m {
					expected = true
				}
			}
			if got := Allowed(m, a, now); got != expected {
				t.Errorf("eylem %d, üye %+v: %v, %v bekleniyordu", a, m, got, expected)
			}
		}
	}
	// Unknown actions are refused, even for admins.
	for _, a := range []Action{actionUnknown, ActionMarkRead + 1} {
		if Allowed(admin, a, now) {
			t.Errorf("bilinmeyen eylem %d kabul edildi", a)
		}
	}
}

func TestAuthorizeTellsMutedApart(t *testing.T) {
	now := time.Now()
	muted := Member{Active: true, Role: RoleModerator, MutedUntil: now.Add(time.Minute)}
	if err := authorize(muted, ActionSend, now); err == nil || err.Error() != "susturulmuş" {
		t.Errorf("susturulmuş: %v", err)
	}
	if err := authorize(Member{Active: true, Role: RoleMember}, ActionManage, now); err == nil || err.Error() != "yetki yok" {
		t.Errorf("yetki: %v", err)
	}
	// A muted member whose role is too low anyway is told about the role.
	if err := authorize(Member{Active: true, Role: RoleMember, MutedUntil: now.Add(time.Minute)}, ActionRestore, now); err == nil || err.Error() != "yetki yok" {
		t.Errorf("susturulmuş üye geri alma: %v", err)
	}
}

func TestSlowMode(t *testing.T) {
	s := newSlowMode()
	var ch, u [16]byte
	ch[0], u[0] = 1, 2
	now := time.Now()
	if wait, undo := s.take(ch, u, now, 10*time.Second); wait != 0 || undo == nil {
		t.Fatal("ilk mesaj bekletildi")
	}
	if wait, undo := s.take(ch, u, now.Add(4*time.Second), 10*time.Second); undo != nil || wait != 6*time.Second {
		t.Fatalf("bekleme = %v", wait)
	}
	// Another user and another channel are independent.
	other := u
	other[1] = 9
	if _, undo := s.take(ch, other, now, 10*time.Second); undo == nil {
		t.Fatal("başka kullanıcı bekletildi")
	}
	// Giving the slot back allows the next attempt at once.
	_, undo := s.take(ch, u, now.Add(10*time.Second), 10*time.Second)
	if undo == nil {
		t.Fatal("süre dolunca bekletildi")
	}
	undo()
	if _, undo := s.take(ch, u, now.Add(11*time.Second), 10*time.Second); undo == nil {
		t.Fatal("geri verilen hak kullanılamadı")
	}
}
