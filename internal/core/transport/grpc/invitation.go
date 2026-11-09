package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
)

// Direct invitations (issue #71). The invited user and the inviter always
// come from the verified token, never from the request: InviteFriend's
// user_id is the friend being invited, CancelInvitation's the invited user.

// InviteFriend implements corev1.MembershipServiceServer.
func (s *MembershipServer) InviteFriend(ctx context.Context, req *corev1.InviteFriendRequest) (*corev1.InviteFriendResponse, error) {
	u, tayfa, target, err := targetArgs(ctx, req.GetTayfaId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	inv, err := s.app.InviteFriend(ctx, u, tayfa, target)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.InviteFriendResponse{Invitation: invitationPB(inv)}, nil
}

// ListMyInvitations implements corev1.MembershipServiceServer.
func (s *MembershipServer) ListMyInvitations(ctx context.Context, req *corev1.ListMyInvitationsRequest) (*corev1.ListMyInvitationsResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.app.ListMyInvitations(ctx, u, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListMyInvitationsResponse{Invitations: invitationsPB(p.Items), NextPageToken: p.Next}, nil
}

// AcceptInvitation implements corev1.MembershipServiceServer.
func (s *MembershipServer) AcceptInvitation(ctx context.Context, req *corev1.AcceptInvitationRequest) (*corev1.AcceptInvitationResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	m, err := s.app.AcceptInvitation(ctx, u, tayfa)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AcceptInvitationResponse{Membership: memberPB(m)}, nil
}

// DeclineInvitation implements corev1.MembershipServiceServer.
func (s *MembershipServer) DeclineInvitation(ctx context.Context, req *corev1.DeclineInvitationRequest) (*corev1.DeclineInvitationResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	if err := s.app.DeclineInvitation(ctx, u, tayfa); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeclineInvitationResponse{}, nil
}

// ListInvitations implements corev1.MembershipServiceServer.
func (s *MembershipServer) ListInvitations(ctx context.Context, req *corev1.ListInvitationsRequest) (*corev1.ListInvitationsResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	p, err := s.app.ListInvitations(ctx, u, tayfa, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListInvitationsResponse{Invitations: invitationsPB(p.Items), NextPageToken: p.Next}, nil
}

// CancelInvitation implements corev1.MembershipServiceServer.
func (s *MembershipServer) CancelInvitation(ctx context.Context, req *corev1.CancelInvitationRequest) (*corev1.CancelInvitationResponse, error) {
	u, tayfa, target, err := targetArgs(ctx, req.GetTayfaId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.app.CancelInvitation(ctx, u, tayfa, target); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CancelInvitationResponse{}, nil
}

func invitationPB(inv membership.Invitation) *corev1.Invitation {
	return &corev1.Invitation{
		TayfaId: inv.TayfaID.String(), TayfaName: inv.TayfaName, UserId: inv.UserID.String(),
		InviterUserId: optionalID(inv.InviterID), InviteTime: timestamppb.New(inv.InvitedAt),
	}
}

func invitationsPB(items []membership.Invitation) []*corev1.Invitation {
	out := make([]*corev1.Invitation, 0, len(items))
	for _, inv := range items {
		out = append(out, invitationPB(inv))
	}
	return out
}
