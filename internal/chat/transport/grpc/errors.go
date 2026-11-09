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
)

// errorDomain is the ErrorInfo domain of chat's errors (errors.proto).
const errorDomain = "chat.tayfam.app"

// Fixed status messages: they never echo request values or internal detail.
const (
	msgRejected        = "istek reddedildi"
	msgInternal        = "iç hata"
	msgUnauthenticated = "kimlik doğrulanamadı"
)

// errorStatus converts an app error to the status the caller sees. Errors
// it does not know are logged and become a bare Internal status.
func errorStatus(ctx context.Context, log *slog.Logger, err error) error {
	var v *apperr.ValidationError
	if errors.As(err, &v) {
		return validation(v.Field)
	}
	switch {
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
