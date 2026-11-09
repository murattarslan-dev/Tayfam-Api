// Package coremembers asks core whether a user is an active member of a
// tayfa (ADR-0009 "Senkron sorgular": chat → core GetMembership). The call
// carries the user's own access token (auth.ForwardToken on the
// connection), so core checks it again and answers only about the caller.
// chat keeps no copy of memberships.
package coremembers

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// callTimeout bounds one call to core.
const callTimeout = 5 * time.Second

// Client is the part of core's MembershipService chat uses.
type Client interface {
	GetMembership(ctx context.Context, in *corev1.GetMembershipRequest, opts ...grpc.CallOption) (*corev1.GetMembershipResponse, error)
}

// Checker implements notification.Memberships with core.
type Checker struct {
	core Client
}

// New returns a Checker. core's connection must forward the caller's token
// (auth.ForwardToken).
func New(core Client) *Checker { return &Checker{core: core} }

// IsActiveMember reports whether user, the caller of the current request,
// is an active member of tayfa. A tayfa core does not show the caller (none,
// deleted, not a member) is false. Core being unreachable is
// apperr.ErrUnavailable; anything else core refuses is an error.
func (c *Checker) IsActiveMember(ctx context.Context, tayfa, user id.ID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := c.core.GetMembership(ctx, &corev1.GetMembershipRequest{TayfaId: tayfa.String(), UserId: user.String()})
	if err != nil {
		st := status.Convert(err)
		switch st.Code() {
		case codes.Unavailable, codes.DeadlineExceeded:
			return false, apperr.ErrUnavailable
		case codes.NotFound:
			for _, d := range st.Details() {
				if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == "core.tayfam.app" &&
					info.GetReason() == corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String() {
					return false, nil
				}
			}
		}
		return false, fmt.Errorf("core üyelik: %s", st.Code())
	}
	return resp.GetMembership().GetStatus() == corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE, nil
}
