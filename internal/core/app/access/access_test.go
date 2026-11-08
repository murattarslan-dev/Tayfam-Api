package access

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
)

func TestAuthorize(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	muted := now.Add(time.Hour)
	for _, tc := range []struct {
		name string
		m    authz.Membership
		a    authz.Action
		want error
	}{
		{"admin ayar", authz.Membership{Role: authz.Admin, Status: authz.StatusActive}, authz.TayfaUpdateSettings, nil},
		{"üye ayar", authz.Membership{Role: authz.Member, Status: authz.StatusActive}, authz.TayfaUpdateSettings, apperr.ErrPermissionDenied},
		{"susturulmuş admin ayar", authz.Membership{Role: authz.Admin, Status: authz.StatusActive, MutedUntil: muted}, authz.TayfaUpdateSettings, apperr.ErrMuted},
		{"susturulmuş üye ayar", authz.Membership{Role: authz.Member, Status: authz.StatusActive, MutedUntil: muted}, authz.TayfaUpdateSettings, apperr.ErrPermissionDenied},
		{"susturulmuş üye davet", authz.Membership{Role: authz.Member, Status: authz.StatusActive, MutedUntil: muted}, authz.InviteCreate, apperr.ErrMuted},
		{"susturulmuş üye okuma", authz.Membership{Role: authz.Member, Status: authz.StatusActive, MutedUntil: muted}, authz.TayfaView, nil},
		{"susturma bitti", authz.Membership{Role: authz.Member, Status: authz.StatusActive, MutedUntil: now}, authz.InviteCreate, nil},
		{"bekleyen", authz.Membership{Role: authz.Member, Status: authz.StatusPending}, authz.TayfaView, apperr.ErrPermissionDenied},
		{"üye değil", authz.Membership{}, authz.TayfaView, apperr.ErrPermissionDenied},
	} {
		if err := Authorize(tc.m, tc.a, now); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

func TestReadOnly(t *testing.T) {
	set := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	for _, tc := range []struct {
		name string
		t    Tayfa
		want bool
	}{
		{"aktif", Tayfa{Status: "active"}, false},
		{"uyarılmış", Tayfa{Status: "warned"}, false},
		{"arşiv", Tayfa{Status: "archived"}, true},
		{"askı", Tayfa{Status: "active", SuspendedAt: set}, true},
		{"silme bekliyor", Tayfa{Status: "active", DeletionRequestedAt: set}, true},
	} {
		if got := ReadOnly(tc.t); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

func TestMembershipRejectsUnknownValues(t *testing.T) {
	if _, err := Membership("owner", "active", pgtype.Timestamptz{}); err == nil {
		t.Fatal("bilinmeyen rol kabul edildi")
	}
	if _, err := Membership("member", "left", pgtype.Timestamptz{}); err == nil {
		t.Fatal("bilinmeyen durum kabul edildi")
	}
}
