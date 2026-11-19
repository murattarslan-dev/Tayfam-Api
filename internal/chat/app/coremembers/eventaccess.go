package coremembers

import (
	"context"
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// EventAccessClient is the part of core's EventComponentService chat uses.
type EventAccessClient interface {
	CheckEventAccess(ctx context.Context, in *corev1.CheckEventAccessRequest, opts ...grpc.CallOption) (*corev1.CheckEventAccessResponse, error)
}

// EventAccess implements realtime.EventAccess with core (issue #238,
// ADR-0025 §6): whether the user may watch an event's component changes is
// core's answer, asked with the user's own token, which ctx carries
// (auth.WithToken for WebSocket connections). Answers are not kept.
type EventAccess struct{ core EventAccessClient }

// NewEventAccess returns an EventAccess.
func NewEventAccess(core EventAccessClient) *EventAccess { return &EventAccess{core: core} }

// CheckEventAccess implements realtime.EventAccess. Not an active member
// is apperr.ErrTayfaNotFound; an unknown event, and a tayfa whose events
// module is off, apperr.ErrEventNotFound; core unreachable
// apperr.ErrUnavailable. Anything else is an error: the default answer is
// no.
func (a *EventAccess) CheckEventAccess(ctx context.Context, tayfa, event id.ID) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := a.core.CheckEventAccess(ctx, &corev1.CheckEventAccessRequest{TayfaId: tayfa.String(), EventId: event.String()})
	if err == nil {
		return nil
	}
	st := status.Convert(err)
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return apperr.ErrUnavailable
	case codes.NotFound, codes.FailedPrecondition:
		for _, d := range st.Details() {
			info, ok := d.(*errdetails.ErrorInfo)
			if !ok || info.GetDomain() != "core.tayfam.app" {
				continue
			}
			switch info.GetReason() {
			case corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String():
				return apperr.ErrTayfaNotFound
			case corev1.ErrorReason_ERROR_REASON_EVENT_NOT_FOUND.String(),
				corev1.ErrorReason_ERROR_REASON_MODULE_DISABLED.String():
				return apperr.ErrEventNotFound
			}
		}
	}
	return fmt.Errorf("core etkinlik erişimi: %s", st.Code())
}
