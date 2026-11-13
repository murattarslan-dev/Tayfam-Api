// Package corerelation asks core's ProfileService how the caller stands to
// another user (issue #185). The call carries the caller's own access token
// (auth.ForwardToken on the connection), so core answers for the verified
// caller, not for anyone identity names.
package corerelation

import (
	"context"
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/profile"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// coreDomain is the ErrorInfo domain of core's errors.
const coreDomain = "core.tayfam.app"

// Client is the part of core's ProfileService identity calls.
type Client interface {
	GetUserProfile(ctx context.Context, in *corev1.GetUserProfileRequest, opts ...grpc.CallOption) (*corev1.GetUserProfileResponse, error)
}

// Relations implements profile.Relations.
type Relations struct{ c Client }

// New returns Relations over c.
func New(c Client) *Relations { return &Relations{c: c} }

// Relation implements profile.Relations. core's USER_NOT_FOUND (unknown,
// deleted or blocked either way) is profile.ErrUserNotFound; anything else
// is profile.ErrUnavailable.
func (r *Relations) Relation(ctx context.Context, user id.ID) (profile.Relation, error) {
	resp, err := r.c.GetUserProfile(ctx, &corev1.GetUserProfileRequest{UserId: user.String()})
	if err != nil {
		st, ok := status.FromError(err)
		if !ok {
			return profile.Relation{}, fmt.Errorf("%w: %w", profile.ErrUnavailable, err)
		}
		for _, d := range st.Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == coreDomain &&
				info.GetReason() == corev1.ErrorReason_ERROR_REASON_USER_NOT_FOUND.String() {
				return profile.Relation{}, profile.ErrUserNotFound
			}
		}
		return profile.Relation{}, fmt.Errorf("%w: %s", profile.ErrUnavailable, st.Code().String())
	}
	p := resp.GetProfile()
	if p.GetUserId() != user.String() {
		return profile.Relation{}, fmt.Errorf("%w: core başka bir profil döndürdü", profile.ErrUnavailable)
	}
	return profile.Relation{
		Self:   p.GetSelf(),
		Friend: p.GetFriendState() == corev1.FriendState_FRIEND_STATE_FRIENDS,
		Mutual: len(p.GetMutualTayfas()) > 0,
	}, nil
}
