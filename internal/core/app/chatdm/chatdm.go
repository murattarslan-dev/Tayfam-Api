// Package chatdm asks chat about a direct message someone reports (issue
// #95; chat's DirectMessageService.VerifyDirectMessageForReport; ADR-0009).
// The connection forwards the reporter's own access token
// (auth.ForwardToken, ADR-0011, ADR-0013): chat answers only about
// conversations the reporter is a party of, and the answer is the sender's
// ID, never the message's content.
package chatdm

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// callTimeout bounds one call; filing the report waits for it.
const callTimeout = 3 * time.Second

// Client is the part of chat's DirectMessageService core uses.
type Client interface {
	VerifyDirectMessageForReport(ctx context.Context, in *chatv1.VerifyDirectMessageForReportRequest, opts ...grpc.CallOption) (*chatv1.VerifyDirectMessageForReportResponse, error)
}

// Verifier implements report.DirectMessages with chat.
type Verifier struct {
	chat Client
}

// New returns a Verifier. chat's connection must forward the user's token.
func New(chat Client) *Verifier { return &Verifier{chat: chat} }

// Sender implements report.DirectMessages: the message's sender, or
// apperr.ErrReportTargetNotFound when chat does not confirm the message for
// the caller, or apperr.ErrUnavailable when chat cannot answer.
func (v *Verifier) Sender(ctx context.Context, message id.ID) (id.ID, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := v.chat.VerifyDirectMessageForReport(ctx, &chatv1.VerifyDirectMessageForReportRequest{MessageId: message.String()})
	if err != nil {
		switch c := status.Code(err); c {
		case codes.NotFound, codes.InvalidArgument:
			return id.ID{}, apperr.ErrReportTargetNotFound
		case codes.Unavailable, codes.DeadlineExceeded:
			return id.ID{}, apperr.ErrUnavailable
		default:
			return id.ID{}, fmt.Errorf("chat şikâyet doğrulaması: %s", c)
		}
	}
	sender, ok := id.Parse(resp.GetSenderUserId())
	if !ok {
		return id.ID{}, apperr.ErrReportTargetNotFound
	}
	return sender, nil
}
