package coremembers

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// DirectMessageClient is core's DirectMessageService as chat uses it.
type DirectMessageClient interface {
	CheckDirectMessage(ctx context.Context, in *corev1.CheckDirectMessageRequest, opts ...grpc.CallOption) (*corev1.CheckDirectMessageResponse, error)
}

// DirectMessageRules implements dm.Rules with core's CheckDirectMessage
// (issue #95): core decides with the caller's own token, which ctx carries
// (auth.ForwardToken for gRPC requests, auth.WithToken for WebSocket
// connections). Answers are never cached: every message is asked about.
type DirectMessageRules struct {
	core DirectMessageClient
}

// NewDirectMessageRules returns DirectMessageRules.
func NewDirectMessageRules(core DirectMessageClient) *DirectMessageRules {
	return &DirectMessageRules{core: core}
}

var decisions = map[corev1.DirectMessageDecision]dm.Decision{
	corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_DENIED:  dm.Denied,
	corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_REQUEST: dm.Request,
	corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_ACTIVE:  dm.Active,
}

// Check implements dm.Rules. caller is not sent: core reads the sender from
// the token.
func (r *DirectMessageRules) Check(ctx context.Context, _, recipient id.ID) (dm.Verdict, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := r.core.CheckDirectMessage(ctx, &corev1.CheckDirectMessageRequest{RecipientId: recipient.String()})
	if err != nil {
		switch c := status.Code(err); c {
		case codes.Unavailable, codes.DeadlineExceeded:
			return dm.Verdict{}, apperr.ErrUnavailable
		case codes.InvalidArgument:
			return dm.Verdict{}, apperr.Validation("recipient_user_id")
		default:
			return dm.Verdict{}, fmt.Errorf("core birebir mesaj kuralı: %s", c)
		}
	}
	d, ok := decisions[resp.GetDecision()]
	if !ok {
		d = dm.Denied // default "no"
	}
	v := dm.Verdict{Decision: d}
	if d == dm.Request {
		v.RequestLimit = int(resp.GetDailyRequestLimit())
	}
	return v, nil
}
