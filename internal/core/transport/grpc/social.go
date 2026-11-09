package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Socials is what SocialServer needs from the app layer (social.Service).
type Socials interface {
	SendFriendRequest(ctx context.Context, caller, target id.ID) (social.Friend, error)
	AcceptFriendRequest(ctx context.Context, caller, requester id.ID) (social.Friend, error)
	RemoveFriendship(ctx context.Context, caller, other id.ID) error
	ListFriends(ctx context.Context, caller id.ID, size int32, token string) (social.Page[social.Friend], error)
	ListFriendRequests(ctx context.Context, caller id.ID, incoming bool, size int32, token string) (social.Page[social.Friend], error)
	BlockUser(ctx context.Context, caller, target id.ID) error
	UnblockUser(ctx context.Context, caller, target id.ID) error
	ListBlockedUsers(ctx context.Context, caller id.ID, size int32, token string) (social.Page[social.Blocked], error)
}

// SocialServer implements corev1.SocialServiceServer. Every method needs a
// verified token (auth.Guard); the caller always comes from it, and every
// list is the caller's own.
type SocialServer struct {
	corev1.UnimplementedSocialServiceServer
	app Socials
	log *slog.Logger
}

// NewSocialServer returns a SocialServer.
func NewSocialServer(app Socials, log *slog.Logger) *SocialServer {
	return &SocialServer{app: app, log: log}
}

// userArgs reads the caller and the request's user_id.
func userArgs(ctx context.Context, raw string) (caller, user id.ID, err error) {
	if caller, err = callerID(ctx); err != nil {
		return
	}
	user, err = parseID(raw, "user_id")
	return
}

// SendFriendRequest implements corev1.SocialServiceServer.
func (s *SocialServer) SendFriendRequest(ctx context.Context, req *corev1.SendFriendRequestRequest) (*corev1.SendFriendRequestResponse, error) {
	u, target, err := userArgs(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	f, err := s.app.SendFriendRequest(ctx, u, target)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SendFriendRequestResponse{Friend: friendPB(f)}, nil
}

// AcceptFriendRequest implements corev1.SocialServiceServer.
func (s *SocialServer) AcceptFriendRequest(ctx context.Context, req *corev1.AcceptFriendRequestRequest) (*corev1.AcceptFriendRequestResponse, error) {
	u, requester, err := userArgs(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	f, err := s.app.AcceptFriendRequest(ctx, u, requester)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AcceptFriendRequestResponse{Friend: friendPB(f)}, nil
}

// RemoveFriendship implements corev1.SocialServiceServer.
func (s *SocialServer) RemoveFriendship(ctx context.Context, req *corev1.RemoveFriendshipRequest) (*corev1.RemoveFriendshipResponse, error) {
	u, other, err := userArgs(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.app.RemoveFriendship(ctx, u, other); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveFriendshipResponse{}, nil
}

// ListFriends implements corev1.SocialServiceServer.
func (s *SocialServer) ListFriends(ctx context.Context, req *corev1.ListFriendsRequest) (*corev1.ListFriendsResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.app.ListFriends(ctx, u, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListFriendsResponse{Friends: friendsPB(p.Items), NextPageToken: p.Next}, nil
}

// ListFriendRequests implements corev1.SocialServiceServer.
func (s *SocialServer) ListFriendRequests(ctx context.Context, req *corev1.ListFriendRequestsRequest) (*corev1.ListFriendRequestsResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	var incoming bool
	switch req.GetDirection() {
	case corev1.RequestDirection_REQUEST_DIRECTION_INCOMING:
		incoming = true
	case corev1.RequestDirection_REQUEST_DIRECTION_OUTGOING:
	default:
		return nil, validation("direction")
	}
	p, err := s.app.ListFriendRequests(ctx, u, incoming, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListFriendRequestsResponse{Requests: friendsPB(p.Items), NextPageToken: p.Next}, nil
}

// BlockUser implements corev1.SocialServiceServer.
func (s *SocialServer) BlockUser(ctx context.Context, req *corev1.BlockUserRequest) (*corev1.BlockUserResponse, error) {
	u, target, err := userArgs(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.app.BlockUser(ctx, u, target); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.BlockUserResponse{}, nil
}

// UnblockUser implements corev1.SocialServiceServer.
func (s *SocialServer) UnblockUser(ctx context.Context, req *corev1.UnblockUserRequest) (*corev1.UnblockUserResponse, error) {
	u, target, err := userArgs(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.app.UnblockUser(ctx, u, target); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UnblockUserResponse{}, nil
}

// ListBlockedUsers implements corev1.SocialServiceServer.
func (s *SocialServer) ListBlockedUsers(ctx context.Context, req *corev1.ListBlockedUsersRequest) (*corev1.ListBlockedUsersResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.app.ListBlockedUsers(ctx, u, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListBlockedUsersResponse{Users: make([]*corev1.BlockedUser, 0, len(p.Items)), NextPageToken: p.Next}
	for _, b := range p.Items {
		out.Users = append(out.Users, &corev1.BlockedUser{
			UserId: b.UserID.String(), DisplayName: b.DisplayName,
			AvatarUploadId: optionalID(b.AvatarUploadID), BlockTime: timestamppb.New(b.BlockedAt),
		})
	}
	return out, nil
}

var friendStates = map[social.State]corev1.FriendState{
	social.StateFriends:  corev1.FriendState_FRIEND_STATE_FRIENDS,
	social.StateIncoming: corev1.FriendState_FRIEND_STATE_INCOMING,
	social.StateOutgoing: corev1.FriendState_FRIEND_STATE_OUTGOING,
}

func friendPB(f social.Friend) *corev1.Friend {
	out := &corev1.Friend{
		UserId: f.UserID.String(), DisplayName: f.DisplayName, AvatarUploadId: optionalID(f.AvatarUploadID),
		State: friendStates[f.State], RequestTime: timestamppb.New(f.RequestedAt),
	}
	if !f.AcceptedAt.IsZero() {
		out.AcceptTime = timestamppb.New(f.AcceptedAt)
	}
	return out
}

func friendsPB(items []social.Friend) []*corev1.Friend {
	out := make([]*corev1.Friend, 0, len(items))
	for _, f := range items {
		out = append(out, friendPB(f))
	}
	return out
}
