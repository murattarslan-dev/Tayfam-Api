package grpc

import (
	"context"
	"log/slog"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/directmessage"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// DirectMessage is what DirectMessageServer needs from the app layer.
type DirectMessage interface {
	Check(ctx context.Context, caller, recipient id.ID) (directmessage.Answer, error)
}

// DirectMessageServer implements corev1.DirectMessageServiceServer for chat
// (issue #95). The sender is always the caller of the verified token; chat
// forwards the user's own token.
type DirectMessageServer struct {
	corev1.UnimplementedDirectMessageServiceServer
	app DirectMessage
	log *slog.Logger
}

// NewDirectMessageServer returns a DirectMessageServer.
func NewDirectMessageServer(app DirectMessage, log *slog.Logger) *DirectMessageServer {
	return &DirectMessageServer{app: app, log: log}
}

var directMessageDecisions = map[directmessage.Decision]corev1.DirectMessageDecision{
	directmessage.Denied:  corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_DENIED,
	directmessage.Request: corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_REQUEST,
	directmessage.Active:  corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_ACTIVE,
}

// CheckDirectMessage implements corev1.DirectMessageServiceServer.
func (s *DirectMessageServer) CheckDirectMessage(ctx context.Context, req *corev1.CheckDirectMessageRequest) (*corev1.CheckDirectMessageResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	recipient, err := parseID(req.GetRecipientId(), "recipient_id")
	if err != nil {
		return nil, err
	}
	a, err := s.app.Check(ctx, u, recipient)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	d, ok := directMessageDecisions[a.Decision]
	if !ok {
		// Unknown decisions are refusals (default "no", ADR-0004).
		d = corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_DENIED
	}
	out := &corev1.CheckDirectMessageResponse{Decision: d}
	if d == corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_REQUEST {
		out.DailyRequestLimit = int32(a.RequestLimit) //nolint:gosec // 5 or 20
	}
	return out, nil
}
