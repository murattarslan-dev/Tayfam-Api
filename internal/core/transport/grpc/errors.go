// Package grpc is core's gRPC transport: it converts requests and responses
// and maps app errors to gRPC statuses. It holds no business logic.
package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// errorDomain is the ErrorInfo domain of core's errors (errors.proto).
const errorDomain = "core.tayfam.app"

// Fixed status messages: they never echo request values or internal detail.
const (
	msgRejected        = "istek reddedildi"
	msgInternal        = "iç hata"
	msgUnauthenticated = "kimlik doğrulanamadı"
)

// reasonFor returns the fixed code and reason of an app error
// (errors.proto); ok is false for any other error.
func reasonFor(err error) (code codes.Code, reason corev1.ErrorReason, ok bool) {
	switch {
	case errors.Is(err, apperr.ErrTayfaNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND, true
	case errors.Is(err, apperr.ErrPermissionDenied):
		return codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_PERMISSION_DENIED, true
	case errors.Is(err, apperr.ErrMuted):
		return codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_MUTED, true
	case errors.Is(err, apperr.ErrTayfaReadOnly):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_TAYFA_READ_ONLY, true
	case errors.Is(err, apperr.ErrTayfaCreateLimit):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_TAYFA_CREATE_LIMIT, true
	case errors.Is(err, apperr.ErrRateLimited):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_RATE_LIMITED, true
	case errors.Is(err, apperr.ErrInvalidJoinCode):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_INVALID_JOIN_CODE, true
	case errors.Is(err, apperr.ErrInviteInvalid):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_INVITE_INVALID, true
	case errors.Is(err, apperr.ErrInviteExpired):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_INVITE_EXPIRED, true
	case errors.Is(err, apperr.ErrInviteRevoked):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_INVITE_REVOKED, true
	case errors.Is(err, apperr.ErrInviteExhausted):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_INVITE_EXHAUSTED, true
	case errors.Is(err, apperr.ErrInviteNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_INVITE_NOT_FOUND, true
	case errors.Is(err, apperr.ErrAlreadyMember):
		return codes.AlreadyExists, corev1.ErrorReason_ERROR_REASON_ALREADY_MEMBER, true
	case errors.Is(err, apperr.ErrAlreadyPending):
		return codes.AlreadyExists, corev1.ErrorReason_ERROR_REASON_ALREADY_PENDING, true
	case errors.Is(err, apperr.ErrBanned):
		return codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_BANNED, true
	case errors.Is(err, apperr.ErrMemberNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_MEMBER_NOT_FOUND, true
	case errors.Is(err, apperr.ErrRoleHierarchy):
		return codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_ROLE_HIERARCHY, true
	case errors.Is(err, apperr.ErrLastAdmin):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_LAST_ADMIN, true
	case errors.Is(err, apperr.ErrDeletionAlreadyRequested):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_DELETION_ALREADY_REQUESTED, true
	case errors.Is(err, apperr.ErrDeletionNotRequested):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_DELETION_NOT_REQUESTED, true
	case errors.Is(err, apperr.ErrModuleDisabled):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_MODULE_DISABLED, true
	case errors.Is(err, apperr.ErrAnnouncementNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_ANNOUNCEMENT_NOT_FOUND, true
	case errors.Is(err, apperr.ErrEventNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_NOT_FOUND, true
	case errors.Is(err, apperr.ErrEventCancelled):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_CANCELLED, true
	case errors.Is(err, apperr.ErrEventEnded):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_ENDED, true
	case errors.Is(err, apperr.ErrEventNotGoing):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_NOT_GOING, true
	case errors.Is(err, apperr.ErrEventItemNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_ITEM_NOT_FOUND, true
	case errors.Is(err, apperr.ErrEventLimit):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_EVENT_LIMIT, true
	case errors.Is(err, apperr.ErrEventTypeMismatch):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_TYPE_MISMATCH, true
	case errors.Is(err, apperr.ErrCheckInClosed):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_CHECK_IN_CLOSED, true
	case errors.Is(err, apperr.ErrCheckInTooFar):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_CHECK_IN_TOO_FAR, true
	case errors.Is(err, apperr.ErrCheckInAttempts):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_CHECK_IN_ATTEMPTS_EXHAUSTED, true
	case errors.Is(err, apperr.ErrCorrectionClosed):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_CORRECTION_CLOSED, true
	case errors.Is(err, apperr.ErrHikeNotStarted):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_HIKE_NOT_STARTED, true
	case errors.Is(err, apperr.ErrNotAttended):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_NOT_ATTENDED, true
	case errors.Is(err, apperr.ErrEventSeriesNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_SERIES_NOT_FOUND, true
	case errors.Is(err, apperr.ErrEventSeriesEnded):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_SERIES_ENDED, true
	case errors.Is(err, apperr.ErrQuestionNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_QUESTION_NOT_FOUND, true
	case errors.Is(err, apperr.ErrAnswerNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_ANSWER_NOT_FOUND, true
	case errors.Is(err, apperr.ErrTagNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_TAG_NOT_FOUND, true
	case errors.Is(err, apperr.ErrOwnAnswerVote):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_OWN_ANSWER_VOTE, true
	case errors.Is(err, apperr.ErrTagLimit):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_TAG_LIMIT, true
	case errors.Is(err, apperr.ErrPollNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_POLL_NOT_FOUND, true
	case errors.Is(err, apperr.ErrPollClosed):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_POLL_CLOSED, true
	case errors.Is(err, apperr.ErrPollAnonymous):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_POLL_ANONYMOUS, true
	case errors.Is(err, apperr.ErrUserNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_USER_NOT_FOUND, true
	case errors.Is(err, apperr.ErrFriendRequestLimit):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_FRIEND_REQUEST_LIMIT, true
	case errors.Is(err, apperr.ErrFriendRequestNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_FRIEND_REQUEST_NOT_FOUND, true
	case errors.Is(err, apperr.ErrAlreadyFriends):
		return codes.AlreadyExists, corev1.ErrorReason_ERROR_REASON_ALREADY_FRIENDS, true
	case errors.Is(err, apperr.ErrFriendRequestPending):
		return codes.AlreadyExists, corev1.ErrorReason_ERROR_REASON_FRIEND_REQUEST_PENDING, true
	case errors.Is(err, apperr.ErrNotFriends):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_NOT_FRIENDS, true
	case errors.Is(err, apperr.ErrDirectInviteLimit):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_DIRECT_INVITE_LIMIT, true
	case errors.Is(err, apperr.ErrInvitationNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_INVITATION_NOT_FOUND, true
	case errors.Is(err, apperr.ErrAlreadyInvited):
		return codes.AlreadyExists, corev1.ErrorReason_ERROR_REASON_ALREADY_INVITED, true
	case errors.Is(err, apperr.ErrInviteNotAllowed):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_INVITE_NOT_ALLOWED, true
	case errors.Is(err, apperr.ErrReportNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_REPORT_NOT_FOUND, true
	case errors.Is(err, apperr.ErrReportTargetNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_REPORT_TARGET_NOT_FOUND, true
	case errors.Is(err, apperr.ErrReportLimit):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_REPORT_LIMIT, true
	case errors.Is(err, apperr.ErrReportAlreadyResolved):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_REPORT_ALREADY_RESOLVED, true
	case errors.Is(err, apperr.ErrReportActionMissing):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_REPORT_ACTION_MISSING, true
	}
	return codes.Unknown, corev1.ErrorReason_ERROR_REASON_UNSPECIFIED, false
}

// errorStatus converts an app error to the status the client sees. Errors
// it does not know are logged and become a bare Internal status.
//
// The memberships trigger is the second line of defence (ADR-0004): when it
// refuses a change the app layer let through, TF002 becomes ROLE_HIERARCHY
// and TF003 LAST_ADMIN. TF001 (no actor) and TF004 (audit log changed) are
// programming errors and stay internal.
func errorStatus(ctx context.Context, log *slog.Logger, err error) error {
	var v *apperr.ValidationError
	if errors.As(err, &v) {
		return reasonStatus(codes.InvalidArgument, corev1.ErrorReason_ERROR_REASON_VALIDATION, map[string]string{"field": v.Field})
	}
	if code, reason, ok := reasonFor(err); ok {
		return reasonStatus(code, reason, nil)
	}
	switch store.SQLState(err) {
	case store.CodeRoleHierarchy:
		log.WarnContext(ctx, "üyelik trigger'ı değişikliği reddetti", slog.String("code", store.CodeRoleHierarchy))
		return reasonStatus(codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_ROLE_HIERARCHY, nil)
	case store.CodeLastAdmin:
		return reasonStatus(codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_LAST_ADMIN, nil)
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
func reasonStatus(code codes.Code, reason corev1.ErrorReason, meta map[string]string) error {
	st := status.New(code, msgRejected)
	if withInfo, err := st.WithDetails(&errdetails.ErrorInfo{Reason: reason.String(), Domain: errorDomain, Metadata: meta}); err == nil {
		st = withInfo
	}
	return st.Err()
}

// validation is the status for a request field that failed validation in
// transport (malformed ID, unknown enum value).
func validation(field string) error {
	return reasonStatus(codes.InvalidArgument, corev1.ErrorReason_ERROR_REASON_VALIDATION, map[string]string{"field": field})
}

// callerID returns the verified caller (auth.Guard). Requests never name
// the acting user.
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

// tayfaID parses a request's tayfa_id.
func tayfaID(s string) (id.ID, error) {
	t, ok := id.Parse(s)
	if !ok {
		return id.ID{}, validation("tayfa_id")
	}
	return t, nil
}
