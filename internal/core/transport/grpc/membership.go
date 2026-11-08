package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Memberships is what MembershipServer needs from the app layer
// (membership.Service).
type Memberships interface {
	JoinByCode(ctx context.Context, caller id.ID, code string) (membership.Member, error)
	CreateInviteLink(ctx context.Context, caller, tayfa id.ID, ttl time.Duration, maxUses int32) (membership.Invite, string, error)
	ListInviteLinks(ctx context.Context, caller, tayfa id.ID) ([]membership.Invite, error)
	RevokeInviteLink(ctx context.Context, caller, tayfa, invite id.ID) error
	PreviewInvite(ctx context.Context, caller id.ID, token string) (membership.Preview, error)
	JoinByInvite(ctx context.Context, caller id.ID, token string) (membership.Member, error)
	ListMembers(ctx context.Context, caller, tayfa id.ID, st authz.Status, size int32, token string) (membership.Page[membership.Member], error)
	ListPending(ctx context.Context, caller, tayfa id.ID, size int32, token string) (membership.Page[membership.Pending], error)
	ApproveMember(ctx context.Context, caller, tayfa, target id.ID) (membership.Member, error)
	RejectMember(ctx context.Context, caller, tayfa, target id.ID) error
	ChangeRole(ctx context.Context, caller, tayfa, target id.ID, role authz.Role) (membership.Member, error)
	MuteMember(ctx context.Context, caller, tayfa, target id.ID, until time.Time) (membership.Member, error)
	UnmuteMember(ctx context.Context, caller, tayfa, target id.ID) (membership.Member, error)
	BanMember(ctx context.Context, caller, tayfa, target id.ID) error
	RemoveMember(ctx context.Context, caller, tayfa, target id.ID) error
	LeaveTayfa(ctx context.Context, caller, tayfa id.ID) error
	GetMembership(ctx context.Context, caller, tayfa, target id.ID) (membership.Member, error)
}

// MembershipServer implements corev1.MembershipServiceServer. Every method
// needs a verified token (auth.Guard); the caller always comes from it.
// Codes and invite tokens are never logged.
type MembershipServer struct {
	corev1.UnimplementedMembershipServiceServer
	app Memberships
	log *slog.Logger
}

// NewMembershipServer returns a MembershipServer.
func NewMembershipServer(app Memberships, log *slog.Logger) *MembershipServer {
	return &MembershipServer{app: app, log: log}
}

// JoinByCode implements corev1.MembershipServiceServer.
func (s *MembershipServer) JoinByCode(ctx context.Context, req *corev1.JoinByCodeRequest) (*corev1.JoinByCodeResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.app.JoinByCode(ctx, u, req.GetCode())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.JoinByCodeResponse{Membership: memberPB(m)}, nil
}

// CreateInviteLink implements corev1.MembershipServiceServer. The token is
// in the response only.
func (s *MembershipServer) CreateInviteLink(ctx context.Context, req *corev1.CreateInviteLinkRequest) (*corev1.CreateInviteLinkResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	if req.GetTtl() == nil || req.GetTtl().CheckValid() != nil {
		return nil, validation("ttl")
	}
	inv, token, err := s.app.CreateInviteLink(ctx, u, tayfa, req.GetTtl().AsDuration(), req.GetMaxUses())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateInviteLinkResponse{Invite: invitePB(inv), Token: token}, nil
}

// ListInviteLinks implements corev1.MembershipServiceServer.
func (s *MembershipServer) ListInviteLinks(ctx context.Context, req *corev1.ListInviteLinksRequest) (*corev1.ListInviteLinksResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	invs, err := s.app.ListInviteLinks(ctx, u, tayfa)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListInviteLinksResponse{Invites: make([]*corev1.InviteLink, 0, len(invs))}
	for _, inv := range invs {
		out.Invites = append(out.Invites, invitePB(inv))
	}
	return out, nil
}

// RevokeInviteLink implements corev1.MembershipServiceServer.
func (s *MembershipServer) RevokeInviteLink(ctx context.Context, req *corev1.RevokeInviteLinkRequest) (*corev1.RevokeInviteLinkResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	inv, err := parseID(req.GetInviteId(), "invite_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.RevokeInviteLink(ctx, u, tayfa, inv); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RevokeInviteLinkResponse{}, nil
}

// PreviewInvite implements corev1.MembershipServiceServer.
func (s *MembershipServer) PreviewInvite(ctx context.Context, req *corev1.PreviewInviteRequest) (*corev1.PreviewInviteResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.app.PreviewInvite(ctx, u, req.GetToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.PreviewInviteResponse{
		TayfaId: p.TayfaID.String(), Name: p.Name, MemberCount: p.MemberCount, AlreadyMember: p.AlreadyMember,
	}, nil
}

// JoinByInvite implements corev1.MembershipServiceServer.
func (s *MembershipServer) JoinByInvite(ctx context.Context, req *corev1.JoinByInviteRequest) (*corev1.JoinByInviteResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.app.JoinByInvite(ctx, u, req.GetToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.JoinByInviteResponse{Membership: memberPB(m)}, nil
}

// ListMembers implements corev1.MembershipServiceServer.
func (s *MembershipServer) ListMembers(ctx context.Context, req *corev1.ListMembersRequest) (*corev1.ListMembersResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	var st authz.Status
	switch req.GetStatus() {
	case corev1.MembershipStatus_MEMBERSHIP_STATUS_UNSPECIFIED, corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE:
		st = authz.StatusActive
	case corev1.MembershipStatus_MEMBERSHIP_STATUS_BANNED:
		st = authz.StatusBanned
	default:
		return nil, validation("status")
	}
	p, err := s.app.ListMembers(ctx, u, tayfa, st, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListMembersResponse{Members: make([]*corev1.Membership, 0, len(p.Items)), NextPageToken: p.Next}
	for _, m := range p.Items {
		out.Members = append(out.Members, memberPB(m))
	}
	return out, nil
}

// ListPending implements corev1.MembershipServiceServer.
func (s *MembershipServer) ListPending(ctx context.Context, req *corev1.ListPendingRequest) (*corev1.ListPendingResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	p, err := s.app.ListPending(ctx, u, tayfa, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListPendingResponse{Pending: make([]*corev1.PendingMember, 0, len(p.Items)), NextPageToken: p.Next}
	for _, m := range p.Items {
		out.Pending = append(out.Pending, &corev1.PendingMember{
			Membership:    memberPB(m.Member),
			RequestTime:   timestamppb.New(m.RequestedAt),
			Source:        joinSourcePB(m.JoinedVia),
			InviterUserId: optionalID(m.InviterID),
			InviteId:      optionalID(m.InviteID),
		})
	}
	return out, nil
}

// ApproveMember implements corev1.MembershipServiceServer.
func (s *MembershipServer) ApproveMember(ctx context.Context, req *corev1.ApproveMemberRequest) (*corev1.ApproveMemberResponse, error) {
	u, tayfa, target, err := targetArgs(ctx, req.GetTayfaId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	m, err := s.app.ApproveMember(ctx, u, tayfa, target)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ApproveMemberResponse{Membership: memberPB(m)}, nil
}

// RejectMember implements corev1.MembershipServiceServer.
func (s *MembershipServer) RejectMember(ctx context.Context, req *corev1.RejectMemberRequest) (*corev1.RejectMemberResponse, error) {
	u, tayfa, target, err := targetArgs(ctx, req.GetTayfaId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.app.RejectMember(ctx, u, tayfa, target); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RejectMemberResponse{}, nil
}

// ChangeRole implements corev1.MembershipServiceServer.
func (s *MembershipServer) ChangeRole(ctx context.Context, req *corev1.ChangeRoleRequest) (*corev1.ChangeRoleResponse, error) {
	u, tayfa, target, err := targetArgs(ctx, req.GetTayfaId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	role, ok := roleFromPB(req.GetNewRole())
	if !ok {
		return nil, validation("new_role")
	}
	m, err := s.app.ChangeRole(ctx, u, tayfa, target, role)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ChangeRoleResponse{Membership: memberPB(m)}, nil
}

// MuteMember implements corev1.MembershipServiceServer.
func (s *MembershipServer) MuteMember(ctx context.Context, req *corev1.MuteMemberRequest) (*corev1.MuteMemberResponse, error) {
	u, tayfa, target, err := targetArgs(ctx, req.GetTayfaId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	if req.GetExpireTime() == nil || req.GetExpireTime().CheckValid() != nil {
		return nil, validation("expire_time")
	}
	m, err := s.app.MuteMember(ctx, u, tayfa, target, req.GetExpireTime().AsTime())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.MuteMemberResponse{Membership: memberPB(m)}, nil
}

// UnmuteMember implements corev1.MembershipServiceServer.
func (s *MembershipServer) UnmuteMember(ctx context.Context, req *corev1.UnmuteMemberRequest) (*corev1.UnmuteMemberResponse, error) {
	u, tayfa, target, err := targetArgs(ctx, req.GetTayfaId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	m, err := s.app.UnmuteMember(ctx, u, tayfa, target)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UnmuteMemberResponse{Membership: memberPB(m)}, nil
}

// BanMember implements corev1.MembershipServiceServer.
func (s *MembershipServer) BanMember(ctx context.Context, req *corev1.BanMemberRequest) (*corev1.BanMemberResponse, error) {
	u, tayfa, target, err := targetArgs(ctx, req.GetTayfaId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.app.BanMember(ctx, u, tayfa, target); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.BanMemberResponse{}, nil
}

// RemoveMember implements corev1.MembershipServiceServer.
func (s *MembershipServer) RemoveMember(ctx context.Context, req *corev1.RemoveMemberRequest) (*corev1.RemoveMemberResponse, error) {
	u, tayfa, target, err := targetArgs(ctx, req.GetTayfaId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.app.RemoveMember(ctx, u, tayfa, target); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveMemberResponse{}, nil
}

// LeaveTayfa implements corev1.MembershipServiceServer.
func (s *MembershipServer) LeaveTayfa(ctx context.Context, req *corev1.LeaveTayfaRequest) (*corev1.LeaveTayfaResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	if err := s.app.LeaveTayfa(ctx, u, tayfa); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.LeaveTayfaResponse{}, nil
}

// GetMembership implements corev1.MembershipServiceServer.
func (s *MembershipServer) GetMembership(ctx context.Context, req *corev1.GetMembershipRequest) (*corev1.GetMembershipResponse, error) {
	u, tayfa, target, err := targetArgs(ctx, req.GetTayfaId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	m, err := s.app.GetMembership(ctx, u, tayfa, target)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetMembershipResponse{Membership: memberPB(m)}, nil
}

// parseID parses a UUID request field.
func parseID(s, field string) (id.ID, error) {
	u, ok := id.Parse(s)
	if !ok {
		return id.ID{}, validation(field)
	}
	return u, nil
}

// targetArgs reads the caller and the tayfa_id and user_id fields.
func targetArgs(ctx context.Context, rawTayfa, rawUser string) (u, tayfa, target id.ID, err error) {
	if u, err = callerID(ctx); err != nil {
		return
	}
	if tayfa, err = tayfaID(rawTayfa); err != nil {
		return
	}
	target, err = parseID(rawUser, "user_id")
	return
}

func memberPB(m membership.Member) *corev1.Membership {
	out := &corev1.Membership{
		TayfaId: m.TayfaID.String(), UserId: m.UserID.String(),
		Role: roleToProto(m.Role), Status: statusToProto(m.Status),
	}
	if !m.JoinedAt.IsZero() {
		out.JoinTime = timestamppb.New(m.JoinedAt)
	}
	if !m.MutedUntil.IsZero() {
		out.MuteExpireTime = timestamppb.New(m.MutedUntil)
	}
	return out
}

func invitePB(inv membership.Invite) *corev1.InviteLink {
	out := &corev1.InviteLink{
		InviteId: inv.ID.String(), TayfaId: inv.TayfaID.String(), CreatedByUserId: optionalID(inv.CreatedBy),
		CreateTime: timestamppb.New(inv.CreatedAt), ExpireTime: timestamppb.New(inv.ExpiresAt),
		MaxUses: inv.MaxUses, UseCount: inv.UseCount,
	}
	if !inv.RevokedAt.IsZero() {
		out.RevokeTime = timestamppb.New(inv.RevokedAt)
	}
	return out
}

func optionalID(u id.ID) string {
	if u == (id.ID{}) {
		return ""
	}
	return u.String()
}

func roleFromPB(r corev1.Role) (authz.Role, bool) {
	switch r {
	case corev1.Role_ROLE_ADMIN:
		return authz.Admin, true
	case corev1.Role_ROLE_MODERATOR:
		return authz.Moderator, true
	case corev1.Role_ROLE_MEMBER:
		return authz.Member, true
	}
	return 0, false
}

func joinSourcePB(via string) corev1.JoinSource {
	switch via {
	case "creator":
		return corev1.JoinSource_JOIN_SOURCE_CREATOR
	case "invite_link":
		return corev1.JoinSource_JOIN_SOURCE_INVITE_LINK
	case "direct_invite":
		return corev1.JoinSource_JOIN_SOURCE_DIRECT_INVITE
	case "code":
		return corev1.JoinSource_JOIN_SOURCE_CODE
	case "discover":
		return corev1.JoinSource_JOIN_SOURCE_DISCOVER
	}
	return corev1.JoinSource_JOIN_SOURCE_UNSPECIFIED
}
