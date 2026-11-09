package grpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// infoOf returns the status code and the ErrorInfo of err; it fails the test
// if the message is not one of the fixed strings.
func infoOf(t *testing.T, err error) (codes.Code, *errdetails.ErrorInfo) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("gRPC status değil: %v", err)
	}
	switch st.Message() {
	case msgRejected, msgInternal, msgUnauthenticated:
	default:
		t.Fatalf("sabit olmayan mesaj: %q", st.Message())
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return st.Code(), info
		}
	}
	return st.Code(), nil
}

func TestErrorStatus(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason corev1.ErrorReason
	}{
		{apperr.ErrTayfaNotFound, codes.NotFound, corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND},
		{apperr.ErrPermissionDenied, codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_PERMISSION_DENIED},
		{apperr.ErrMuted, codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_MUTED},
		{apperr.ErrTayfaReadOnly, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_TAYFA_READ_ONLY},
		{apperr.ErrTayfaCreateLimit, codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_TAYFA_CREATE_LIMIT},
		{apperr.ErrRateLimited, codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_RATE_LIMITED},
		{apperr.ErrInvalidJoinCode, codes.NotFound, corev1.ErrorReason_ERROR_REASON_INVALID_JOIN_CODE},
		{apperr.ErrInviteInvalid, codes.NotFound, corev1.ErrorReason_ERROR_REASON_INVITE_INVALID},
		{apperr.ErrInviteExpired, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_INVITE_EXPIRED},
		{apperr.ErrInviteRevoked, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_INVITE_REVOKED},
		{apperr.ErrInviteExhausted, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_INVITE_EXHAUSTED},
		{apperr.ErrInviteNotFound, codes.NotFound, corev1.ErrorReason_ERROR_REASON_INVITE_NOT_FOUND},
		{apperr.ErrAlreadyMember, codes.AlreadyExists, corev1.ErrorReason_ERROR_REASON_ALREADY_MEMBER},
		{apperr.ErrAlreadyPending, codes.AlreadyExists, corev1.ErrorReason_ERROR_REASON_ALREADY_PENDING},
		{apperr.ErrBanned, codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_BANNED},
		{apperr.ErrMemberNotFound, codes.NotFound, corev1.ErrorReason_ERROR_REASON_MEMBER_NOT_FOUND},
		{apperr.ErrRoleHierarchy, codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_ROLE_HIERARCHY},
		{apperr.ErrLastAdmin, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_LAST_ADMIN},
		{apperr.ErrDeletionAlreadyRequested, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_DELETION_ALREADY_REQUESTED},
		{apperr.ErrDeletionNotRequested, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_DELETION_NOT_REQUESTED},
		{apperr.ErrModuleDisabled, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_MODULE_DISABLED},
		{apperr.ErrAnnouncementNotFound, codes.NotFound, corev1.ErrorReason_ERROR_REASON_ANNOUNCEMENT_NOT_FOUND},
		{apperr.ErrEventNotFound, codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_NOT_FOUND},
		{apperr.ErrEventCancelled, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_CANCELLED},
		{apperr.ErrEventEnded, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_ENDED},
		{apperr.ErrEventNotGoing, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_NOT_GOING},
		{apperr.ErrEventItemNotFound, codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_ITEM_NOT_FOUND},
		{apperr.ErrEventLimit, codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_EVENT_LIMIT},
		{apperr.ErrEventTypeMismatch, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_TYPE_MISMATCH},
		{apperr.ErrCheckInClosed, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_CHECK_IN_CLOSED},
		{apperr.ErrCheckInTooFar, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_CHECK_IN_TOO_FAR},
		{apperr.ErrCheckInAttempts, codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_CHECK_IN_ATTEMPTS_EXHAUSTED},
		{apperr.ErrCorrectionClosed, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_CORRECTION_CLOSED},
		{apperr.ErrHikeNotStarted, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_HIKE_NOT_STARTED},
		{apperr.ErrNotAttended, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_NOT_ATTENDED},
		{apperr.ErrEventSeriesNotFound, codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_SERIES_NOT_FOUND},
		{apperr.ErrEventSeriesEnded, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_SERIES_ENDED},
		// Wrapped errors keep their reason.
		{fmt.Errorf("katman: %w", apperr.ErrTayfaNotFound), codes.NotFound, corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND},
		// The memberships trigger, second line of defence.
		{fmt.Errorf("üyelik: %w", &pgconn.PgError{Code: store.CodeRoleHierarchy, Message: "iç ayrıntı"}), codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_ROLE_HIERARCHY},
		{&pgconn.PgError{Code: store.CodeLastAdmin}, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_LAST_ADMIN},
	} {
		code, info := infoOf(t, errorStatus(ctx, discard, tc.err))
		if code != tc.code || info == nil || info.GetReason() != tc.reason.String() || info.GetDomain() != errorDomain {
			t.Errorf("%v: %v %v", tc.err, code, info)
		}
	}

	code, info := infoOf(t, errorStatus(ctx, discard, fmt.Errorf("x: %w", apperr.Validation("name"))))
	if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() || info.GetMetadata()["field"] != "name" || len(info.GetMetadata()) != 1 {
		t.Fatalf("doğrulama: %v %v", code, info)
	}

	// Anything else is internal and says nothing about the cause.
	for _, err := range []error{
		errors.New("gizli iç ayrıntı"),
		&pgconn.PgError{Code: store.CodeNoActor, Message: "iç ayrıntı"},
		&pgconn.PgError{Code: store.CodeAppendOnly},
		&pgconn.PgError{Code: "23505", Message: "duplicate key"},
	} {
		got := errorStatus(ctx, discard, err)
		code, info := infoOf(t, got)
		if code != codes.Internal || info != nil || strings.Contains(got.Error(), "ayrıntı") {
			t.Errorf("%v: %v", err, got)
		}
	}
	if code, _ := infoOf(t, errorStatus(ctx, discard, fmt.Errorf("x: %w", context.Canceled))); code != codes.Canceled {
		t.Errorf("iptal: %v", code)
	}
}
