// Package grpc is chat's gRPC transport: it converts requests and responses
// and maps app errors to gRPC statuses. It holds no business logic.
package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
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
	if code, reason, ok := reasonFor(err); ok {
		return reasonStatus(code, reason, nil)
	}
	switch {
	case errors.Is(err, apperr.ErrUnavailable):
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
