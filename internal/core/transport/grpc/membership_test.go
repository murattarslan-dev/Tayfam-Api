package grpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeApp records the caller and returns err from every method.
type fakeApp struct {
	err    error
	caller id.ID
	tayfa  id.ID
	target id.ID
	role   authz.Role
	status authz.Status
	ttl    time.Duration
}

func (f *fakeApp) member() membership.Member {
	return membership.Member{TayfaID: f.tayfa, UserID: f.caller, Role: authz.Member, Status: authz.StatusActive, JoinedAt: time.Unix(1, 0).UTC()}
}
func (f *fakeApp) JoinByCode(_ context.Context, c id.ID, _ string) (membership.Member, error) {
	f.caller = c
	return f.member(), f.err
}
func (f *fakeApp) CreateInviteLink(_ context.Context, c, t id.ID, ttl time.Duration, _ int32) (membership.Invite, string, error) {
	f.caller, f.tayfa, f.ttl = c, t, ttl
	return membership.Invite{ID: id.New(), TayfaID: t, CreatedBy: c}, "gizli-token", f.err
}
func (f *fakeApp) ListInviteLinks(_ context.Context, c, t id.ID) ([]membership.Invite, error) {
	f.caller, f.tayfa = c, t
	return []membership.Invite{{ID: id.New(), TayfaID: t}}, f.err
}
func (f *fakeApp) RevokeInviteLink(_ context.Context, c, t, i id.ID) error {
	f.caller, f.tayfa, f.target = c, t, i
	return f.err
}
func (f *fakeApp) PreviewInvite(_ context.Context, c id.ID, _ string) (membership.Preview, error) {
	f.caller = c
	return membership.Preview{TayfaID: id.New(), Name: "Kamp", MemberCount: 3}, f.err
}
func (f *fakeApp) JoinByInvite(_ context.Context, c id.ID, _ string) (membership.Member, error) {
	f.caller = c
	return f.member(), f.err
}
func (f *fakeApp) ListMembers(_ context.Context, c, t id.ID, st authz.Status, _ int32, _ string) (membership.Page[membership.Member], error) {
	f.caller, f.tayfa, f.status = c, t, st
	return membership.Page[membership.Member]{Items: []membership.Member{f.member()}, Next: "sonraki"}, f.err
}
func (f *fakeApp) ListPending(_ context.Context, c, t id.ID, _ int32, _ string) (membership.Page[membership.Pending], error) {
	f.caller, f.tayfa = c, t
	return membership.Page[membership.Pending]{Items: []membership.Pending{{Member: f.member(), JoinedVia: "invite_link", InviterID: id.New()}}}, f.err
}
func (f *fakeApp) ApproveMember(_ context.Context, c, t, u id.ID) (membership.Member, error) {
	f.caller, f.tayfa, f.target = c, t, u
	return f.member(), f.err
}
func (f *fakeApp) RejectMember(_ context.Context, c, t, u id.ID) error {
	f.caller, f.tayfa, f.target = c, t, u
	return f.err
}
func (f *fakeApp) ChangeRole(_ context.Context, c, t, u id.ID, r authz.Role) (membership.Member, error) {
	f.caller, f.tayfa, f.target, f.role = c, t, u, r
	return f.member(), f.err
}
func (f *fakeApp) MuteMember(_ context.Context, c, t, u id.ID, _ time.Time) (membership.Member, error) {
	f.caller, f.tayfa, f.target = c, t, u
	return f.member(), f.err
}
func (f *fakeApp) UnmuteMember(_ context.Context, c, t, u id.ID) (membership.Member, error) {
	f.caller, f.tayfa, f.target = c, t, u
	return f.member(), f.err
}
func (f *fakeApp) BanMember(_ context.Context, c, t, u id.ID) error {
	f.caller, f.tayfa, f.target = c, t, u
	return f.err
}
func (f *fakeApp) RemoveMember(_ context.Context, c, t, u id.ID) error {
	f.caller, f.tayfa, f.target = c, t, u
	return f.err
}
func (f *fakeApp) LeaveTayfa(_ context.Context, c, t id.ID) error {
	f.caller, f.tayfa = c, t
	return f.err
}
func (f *fakeApp) GetMembership(_ context.Context, c, t, u id.ID) (membership.Member, error) {
	f.caller, f.tayfa, f.target = c, t, u
	return f.member(), f.err
}

func (f *fakeApp) InviteFriend(_ context.Context, c, t, u id.ID) (membership.Invitation, error) {
	f.caller, f.tayfa, f.target = c, t, u
	return membership.Invitation{TayfaID: t, TayfaName: "Kamp", UserID: u, InviterID: c, InvitedAt: time.Unix(1, 0).UTC()}, f.err
}
func (f *fakeApp) ListMyInvitations(_ context.Context, c id.ID, _ int32, _ string) (membership.Page[membership.Invitation], error) {
	f.caller = c
	return membership.Page[membership.Invitation]{Items: []membership.Invitation{{TayfaID: id.New(), UserID: c}}, Next: "sonraki"}, f.err
}
func (f *fakeApp) AcceptInvitation(_ context.Context, c, t id.ID) (membership.Member, error) {
	f.caller, f.tayfa = c, t
	return f.member(), f.err
}
func (f *fakeApp) DeclineInvitation(_ context.Context, c, t id.ID) error {
	f.caller, f.tayfa = c, t
	return f.err
}
func (f *fakeApp) ListInvitations(_ context.Context, c, t id.ID, _ int32, _ string) (membership.Page[membership.Invitation], error) {
	f.caller, f.tayfa = c, t
	return membership.Page[membership.Invitation]{Items: []membership.Invitation{{TayfaID: t, UserID: id.New()}}}, f.err
}
func (f *fakeApp) CancelInvitation(_ context.Context, c, t, u id.ID) error {
	f.caller, f.tayfa, f.target = c, t, u
	return f.err
}

func newServer(f *fakeApp) (*MembershipServer, *bytes.Buffer) {
	var buf bytes.Buffer
	return NewMembershipServer(f, slog.New(slog.NewJSONHandler(&buf, nil))), &buf
}

func reasonOf(t *testing.T, err error) (codes.Code, string, map[string]string) {
	t.Helper()
	st := status.Convert(err)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			if info.GetDomain() != errorDomain {
				t.Fatalf("domain = %q", info.GetDomain())
			}
			return st.Code(), info.GetReason(), info.GetMetadata()
		}
	}
	return st.Code(), "", nil
}

func TestCallerComesFromToken(t *testing.T) {
	f := &fakeApp{}
	srv, _ := newServer(f)
	u, tayfa, target := id.New(), id.New(), id.New()
	ctx := callerCtx(u)
	if _, err := srv.BanMember(ctx, &corev1.BanMemberRequest{TayfaId: tayfa.String(), UserId: target.String()}); err != nil {
		t.Fatal(err)
	}
	if f.caller != u || f.tayfa != tayfa || f.target != target {
		t.Fatalf("caller=%v tayfa=%v target=%v", f.caller, f.tayfa, f.target)
	}
	if _, err := srv.JoinByCode(context.Background(), &corev1.JoinByCodeRequest{Code: "ABCDEFGH"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	bad := auth.WithCallerForTest(context.Background(), auth.Claims{Subject: "kullanici"})
	if _, err := srv.LeaveTayfa(bad, &corev1.LeaveTayfaRequest{TayfaId: tayfa.String()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("UUID olmayan subject: %v", err)
	}
}

func TestRequestValidation(t *testing.T) {
	f := &fakeApp{}
	srv, _ := newServer(f)
	ctx := callerCtx(id.New())
	tayfa := id.New().String()
	for name, tc := range map[string]struct {
		call  func() error
		field string
	}{
		"tayfa_id": {func() error {
			_, err := srv.ListMembers(ctx, &corev1.ListMembersRequest{TayfaId: "x"})
			return err
		}, "tayfa_id"},
		"user_id": {func() error {
			_, err := srv.ApproveMember(ctx, &corev1.ApproveMemberRequest{TayfaId: tayfa, UserId: ""})
			return err
		}, "user_id"},
		"invite_id": {func() error {
			_, err := srv.RevokeInviteLink(ctx, &corev1.RevokeInviteLinkRequest{TayfaId: tayfa, InviteId: "1"})
			return err
		}, "invite_id"},
		"ttl": {func() error {
			_, err := srv.CreateInviteLink(ctx, &corev1.CreateInviteLinkRequest{TayfaId: tayfa, MaxUses: 1})
			return err
		}, "ttl"},
		"new_role": {func() error {
			_, err := srv.ChangeRole(ctx, &corev1.ChangeRoleRequest{TayfaId: tayfa, UserId: id.New().String()})
			return err
		}, "new_role"},
		"expire_time": {func() error {
			_, err := srv.MuteMember(ctx, &corev1.MuteMemberRequest{TayfaId: tayfa, UserId: id.New().String()})
			return err
		}, "expire_time"},
		"status": {func() error {
			_, err := srv.ListMembers(ctx, &corev1.ListMembersRequest{TayfaId: tayfa, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING})
			return err
		}, "status"},
	} {
		code, reason, meta := reasonOf(t, tc.call())
		if code != codes.InvalidArgument || reason != "ERROR_REASON_VALIDATION" || meta["field"] != tc.field {
			t.Errorf("%s: %v %q %v", name, code, reason, meta)
		}
	}
}

func TestConversions(t *testing.T) {
	f := &fakeApp{}
	srv, _ := newServer(f)
	ctx := callerCtx(id.New())
	tayfa := id.New().String()

	r, err := srv.CreateInviteLink(ctx, &corev1.CreateInviteLinkRequest{TayfaId: tayfa, Ttl: durationpb.New(2 * time.Hour), MaxUses: 3})
	if err != nil || r.GetToken() != "gizli-token" || f.ttl != 2*time.Hour || r.GetInvite().GetRevokeTime() != nil {
		t.Fatalf("davet: %v %v", r, err)
	}
	if _, err := srv.ChangeRole(ctx, &corev1.ChangeRoleRequest{TayfaId: tayfa, UserId: id.New().String(), NewRole: corev1.Role_ROLE_MODERATOR}); err != nil || f.role != authz.Moderator {
		t.Fatalf("rol: %v %v", f.role, err)
	}
	lm, err := srv.ListMembers(ctx, &corev1.ListMembersRequest{TayfaId: tayfa, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_BANNED})
	if err != nil || f.status != authz.StatusBanned || lm.GetNextPageToken() != "sonraki" {
		t.Fatalf("liste: %v %v", lm, err)
	}
	m := lm.GetMembers()[0]
	if m.GetRole() != corev1.Role_ROLE_MEMBER || m.GetStatus() != corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE || m.GetJoinTime() == nil || m.GetMuteExpireTime() != nil {
		t.Fatalf("üye = %v", m)
	}
	if _, err := srv.ListMembers(ctx, &corev1.ListMembersRequest{TayfaId: tayfa}); err != nil || f.status != authz.StatusActive {
		t.Fatalf("varsayılan durum: %v %v", f.status, err)
	}
	lp, err := srv.ListPending(ctx, &corev1.ListPendingRequest{TayfaId: tayfa})
	if err != nil || lp.GetPending()[0].GetSource() != corev1.JoinSource_JOIN_SOURCE_INVITE_LINK || lp.GetPending()[0].GetInviterUserId() == "" || lp.GetPending()[0].GetInviteId() != "" {
		t.Fatalf("bekleyenler: %v %v", lp, err)
	}
	if _, err := srv.MuteMember(ctx, &corev1.MuteMemberRequest{TayfaId: tayfa, UserId: id.New().String(), ExpireTime: timestamppb.New(time.Now().Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
}

// Internal errors reach the client as a bare INTERNAL; invite tokens and
// codes never reach the log.
func TestInternalErrorsLeakNothing(t *testing.T) {
	inviteTok := strings.Repeat("q", 43) // test value, not a real token
	srv, buf := newServer(&fakeApp{err: errors.New("veritabanı: bağlantı koptu")})
	_, err := srv.JoinByInvite(callerCtx(id.New()), &corev1.JoinByInviteRequest{Token: inviteTok})
	if status.Code(err) != codes.Internal || status.Convert(err).Message() != msgInternal || len(status.Convert(err).Details()) != 0 {
		t.Fatalf("iç hata: %v", err)
	}
	_, _ = srv.JoinByCode(callerCtx(id.New()), &corev1.JoinByCodeRequest{Code: "GIZLIKOD"})
	log := buf.String()
	if strings.Contains(log, inviteTok) || strings.Contains(log, "GIZLIKOD") || !strings.Contains(log, "istek başarısız") {
		t.Fatalf("log = %s", log)
	}
}
