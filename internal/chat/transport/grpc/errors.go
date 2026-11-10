// Package grpc is chat's gRPC transport: it converts requests and responses
// and maps app errors to gRPC statuses. It holds no business logic.
package grpc

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// errorDomain is the ErrorInfo domain of chat's errors (errors.proto).
const errorDomain = "chat.tayfam.app"

// Fixed status messages: they never echo request values or internal detail.
const (
	msgRejected        = "istek reddedildi"
	msgInternal        = "iç hata"
	msgUnauthenticated = "kimlik doğrulanamadı"
)

// reasonFor returns the fixed code and reason of an app error
// (errors.proto); ok is false for any other error.
func reasonFor(err error) (code codes.Code, reason chatv1.ErrorReason, ok bool) {
	switch {
	case errors.Is(err, apperr.ErrNotificationNotFound):
		return codes.NotFound, chatv1.ErrorReason_ERROR_REASON_NOTIFICATION_NOT_FOUND, true
	case errors.Is(err, apperr.ErrTayfaNotFound):
		return codes.NotFound, chatv1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND, true
	case errors.Is(err, apperr.ErrChannelNotFound):
		return codes.NotFound, chatv1.ErrorReason_ERROR_REASON_CHANNEL_NOT_FOUND, true
	case errors.Is(err, apperr.ErrMessageNotFound):
		return codes.NotFound, chatv1.ErrorReason_ERROR_REASON_MESSAGE_NOT_FOUND, true
	case errors.Is(err, apperr.ErrPermissionDenied):
		return codes.PermissionDenied, chatv1.ErrorReason_ERROR_REASON_PERMISSION_DENIED, true
	case errors.Is(err, apperr.ErrMuted):
		return codes.PermissionDenied, chatv1.ErrorReason_ERROR_REASON_MUTED, true
	case errors.Is(err, apperr.ErrChannelArchived):
		return codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_CHANNEL_ARCHIVED, true
	case errors.Is(err, apperr.ErrTayfaReadOnly):
		return codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_TAYFA_READ_ONLY, true
	case errors.Is(err, apperr.ErrRoleHierarchy):
		return codes.PermissionDenied, chatv1.ErrorReason_ERROR_REASON_ROLE_HIERARCHY, true
	case errors.Is(err, apperr.ErrChannelNameTaken):
		return codes.AlreadyExists, chatv1.ErrorReason_ERROR_REASON_CHANNEL_NAME_TAKEN, true
	case errors.Is(err, apperr.ErrConversationNotFound):
		return codes.NotFound, chatv1.ErrorReason_ERROR_REASON_CONVERSATION_NOT_FOUND, true
	case errors.Is(err, apperr.ErrDirectMessageNotAllowed):
		return codes.PermissionDenied, chatv1.ErrorReason_ERROR_REASON_DIRECT_MESSAGE_NOT_ALLOWED, true
	case errors.Is(err, apperr.ErrMessageRequestPending):
		return codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_MESSAGE_REQUEST_PENDING, true
	case errors.Is(err, apperr.ErrMessageRequestLimit):
		return codes.ResourceExhausted, chatv1.ErrorReason_ERROR_REASON_MESSAGE_REQUEST_LIMIT, true
	case errors.Is(err, apperr.ErrMessageRequestNotFound):
		return codes.NotFound, chatv1.ErrorReason_ERROR_REASON_MESSAGE_REQUEST_NOT_FOUND, true
	case errors.Is(err, apperr.ErrRateLimited):
		return codes.ResourceExhausted, chatv1.ErrorReason_ERROR_REASON_RATE_LIMITED, true
	case errors.Is(err, apperr.ErrE2EDeviceNotRegistered):
		return codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_E2E_DEVICE_NOT_REGISTERED, true
	case errors.Is(err, apperr.ErrE2EDeviceAlreadyRegistered):
		return codes.AlreadyExists, chatv1.ErrorReason_ERROR_REASON_E2E_DEVICE_ALREADY_REGISTERED, true
	case errors.Is(err, apperr.ErrE2EDeviceLimit):
		return codes.ResourceExhausted, chatv1.ErrorReason_ERROR_REASON_E2E_DEVICE_LIMIT, true
	case errors.Is(err, apperr.ErrE2ERecipientUnavailable):
		return codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_E2E_RECIPIENT_UNAVAILABLE, true
	case errors.Is(err, apperr.ErrE2ERequired):
		return codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_E2E_REQUIRED, true
	case errors.Is(err, apperr.ErrE2ENotPaired):
		return codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_E2E_NOT_PAIRED, true
	case errors.Is(err, apperr.ErrE2EAlreadyPaired):
		return codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_E2E_ALREADY_PAIRED, true
	case errors.Is(err, apperr.ErrE2EPairedDeviceExists):
		return codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_E2E_PAIRED_DEVICE_EXISTS, true
	case errors.Is(err, apperr.ErrE2EHistoryKeyStale):
		return codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_E2E_HISTORY_KEY_STALE, true
	case errors.Is(err, apperr.ErrE2ELinkRequestNotFound):
		return codes.NotFound, chatv1.ErrorReason_ERROR_REASON_E2E_LINK_REQUEST_NOT_FOUND, true
	}
	return codes.Unknown, chatv1.ErrorReason_ERROR_REASON_UNSPECIFIED, false
}

// errorStatus converts an app error to the status the caller sees. Errors
// it does not know are logged and become a bare Internal status.
func errorStatus(ctx context.Context, log *slog.Logger, err error) error {
	var v *apperr.ValidationError
	if errors.As(err, &v) {
		return validation(v.Field)
	}
	var slow *apperr.SlowModeError
	if errors.As(err, &slow) {
		secs := int64(math.Ceil(slow.RetryAfter.Seconds()))
		return reasonStatus(codes.ResourceExhausted, chatv1.ErrorReason_ERROR_REASON_SLOW_MODE,
			map[string]string{"retry_after_seconds": strconv.FormatInt(max(secs, 1), 10)})
	}
	var changed *apperr.DevicesChangedError
	if errors.As(err, &changed) {
		return reasonStatus(codes.FailedPrecondition, chatv1.ErrorReason_ERROR_REASON_E2E_DEVICES_CHANGED, map[string]string{
			"missing_device_ids": strings.Join(changed.Missing, ","), "stale_device_ids": strings.Join(changed.Stale, ","),
		})
	}
	if errors.Is(err, apperr.ErrSessionEnded) {
		// The session ended; its token may still be valid for a while.
		return status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	if code, reason, ok := reasonFor(err); ok {
		return reasonStatus(code, reason, nil)
	}
	switch {
	case errors.Is(err, apperr.ErrUnavailable), errors.Is(err, apperr.ErrChannelsUnavailable), errors.Is(err, apperr.ErrDirectMessagesUnavailable):
		return status.Error(codes.Unavailable, msgRejected)
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, msgRejected)
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, msgRejected)
	}
	log.ErrorContext(ctx, "istek başarısız", slog.Any("err", err))
	return status.Error(codes.Internal, msgInternal)
}

// reasonStatus builds a status with a google.rpc.ErrorInfo detail.
func reasonStatus(code codes.Code, reason chatv1.ErrorReason, meta map[string]string) error {
	st := status.New(code, msgRejected)
	if withInfo, err := st.WithDetails(&errdetails.ErrorInfo{Reason: reason.String(), Domain: errorDomain, Metadata: meta}); err == nil {
		st = withInfo
	}
	return st.Err()
}

// validation is the status for a request field that failed validation.
func validation(field string) error {
	return reasonStatus(codes.InvalidArgument, chatv1.ErrorReason_ERROR_REASON_VALIDATION, map[string]string{"field": field})
}

// callerID returns the verified user (auth.Guard). Requests never name the
// acting user.
func callerID(ctx context.Context) (id.ID, error) {
	c, ok := auth.CallerFrom(ctx)
	if !ok {
		return id.ID{}, status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	u, ok := id.Parse(c.Subject)
	if !ok {
		return id.ID{}, status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	return u, nil
}

// callerSession returns the verified caller with the session (sid) and
// issue time of the token: an end-to-end device is the session (ADR-0018).
func callerSession(ctx context.Context) (keys.Caller, error) {
	c, ok := auth.CallerFrom(ctx)
	if !ok {
		return keys.Caller{}, status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	u, okU := id.Parse(c.Subject)
	sid, okS := id.Parse(c.SessionID)
	if !okU || !okS {
		return keys.Caller{}, status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	return keys.Caller{User: u, Session: sid, IssuedAt: c.IssuedAt}, nil
}
