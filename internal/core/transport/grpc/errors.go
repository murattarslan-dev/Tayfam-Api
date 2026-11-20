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
	case errors.Is(err, apperr.ErrInstantEventLimit):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_INSTANT_EVENT_LIMIT, true
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
	case errors.Is(err, apperr.ErrEventItemTaken):
		return codes.Aborted, corev1.ErrorReason_ERROR_REASON_EVENT_ITEM_TAKEN, true
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
	case errors.Is(err, apperr.ErrAlbumClosed):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_ALBUM_CLOSED, true
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
	case errors.Is(err, apperr.ErrReauthenticationRequired):
		return codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_REAUTHENTICATION_REQUIRED, true
	case errors.Is(err, apperr.ErrReasonRequired):
		return codes.InvalidArgument, corev1.ErrorReason_ERROR_REASON_REASON_REQUIRED, true
	case errors.Is(err, apperr.ErrDecisionNotApplicable):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_DECISION_NOT_APPLICABLE, true
	case errors.Is(err, apperr.ErrTayfaStateConflict):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_TAYFA_STATE_CONFLICT, true
	case errors.Is(err, apperr.ErrUploadNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_FOUND, true
	case errors.Is(err, apperr.ErrUploadNotReady):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_READY, true
	case errors.Is(err, apperr.ErrUploadInUse):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_UPLOAD_IN_USE, true
	case errors.Is(err, apperr.ErrUploadNotReceived):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_RECEIVED, true
	case errors.Is(err, apperr.ErrUploadRateLimited):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_UPLOAD_RATE_LIMITED, true
	case errors.Is(err, apperr.ErrUploadQuotaExceeded):
		return codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_UPLOAD_QUOTA_EXCEEDED, true
	case errors.Is(err, apperr.ErrFeatureDisabled):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_FEATURE_DISABLED, true
	case errors.Is(err, apperr.ErrDiscoverTooFewMembers):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_DISCOVER_TOO_FEW_MEMBERS, true
	case errors.Is(err, apperr.ErrFeedbackClosed):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_FEEDBACK_CLOSED, true
	case errors.Is(err, apperr.ErrSafetyContactClosed):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_SAFETY_CONTACT_CLOSED, true
	case errors.Is(err, apperr.ErrSafetyContactNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_SAFETY_CONTACT_NOT_FOUND, true
	case errors.Is(err, apperr.ErrCatalogTypeNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_CATALOG_TYPE_NOT_FOUND, true
	case errors.Is(err, apperr.ErrComponentNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_COMPONENT_NOT_FOUND, true
	case errors.Is(err, apperr.ErrComponentDisabled):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_COMPONENT_DISABLED, true
	case errors.Is(err, apperr.ErrComponentLocked):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_COMPONENT_LOCKED, true
	case errors.Is(err, apperr.ErrComponentFrozen):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN, true
	case errors.Is(err, apperr.ErrVehicleFull):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_VEHICLE_FULL, true
	case errors.Is(err, apperr.ErrVehicleSeatsTaken):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_VEHICLE_SEATS_TAKEN, true
	case errors.Is(err, apperr.ErrSeatRequestDriver):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_SEAT_REQUEST_DRIVER, true
	case errors.Is(err, apperr.ErrVocabularyNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_VOCABULARY_NOT_FOUND, true
	case errors.Is(err, apperr.ErrVocabularyMerged):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_VOCABULARY_MERGED, true
	case errors.Is(err, apperr.ErrVocabularyNotMerged):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_VOCABULARY_NOT_MERGED, true
	case errors.Is(err, apperr.ErrMeasurementNotStarted):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_MEASUREMENT_NOT_STARTED, true
	case errors.Is(err, apperr.ErrMeasurementNotParticipant):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_MEASUREMENT_NOT_PARTICIPANT, true
	case errors.Is(err, apperr.ErrMeasurementScope):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_MEASUREMENT_SCOPE, true
	case errors.Is(err, apperr.ErrTeamsManual):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_TEAMS_MANUAL, true
	case errors.Is(err, apperr.ErrTeamsTooFew):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_TEAMS_TOO_FEW, true
	case errors.Is(err, apperr.ErrTeamsNotParticipant):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_TEAMS_NOT_PARTICIPANT, true
	case errors.Is(err, apperr.ErrDrawAlreadyMade):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_DRAW_ALREADY_MADE, true
	case errors.Is(err, apperr.ErrDrawTooFew):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_DRAW_TOO_FEW, true
	case errors.Is(err, apperr.ErrScorePresetUnavailable):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_SCORE_PRESET_UNAVAILABLE, true
	case errors.Is(err, apperr.ErrScoreTableNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_SCORE_TABLE_NOT_FOUND, true
	case errors.Is(err, apperr.ErrScoreTableFinished):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_SCORE_TABLE_FINISHED, true
	case errors.Is(err, apperr.ErrScoreTableStarted):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_SCORE_TABLE_STARTED, true
	case errors.Is(err, apperr.ErrScorePlayerNotParticipant):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_SCORE_PLAYER_NOT_PARTICIPANT, true
	case errors.Is(err, apperr.ErrScoreNotStarted):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_SCORE_NOT_STARTED, true
	case errors.Is(err, apperr.ErrBracketDrawRequired):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_BRACKET_DRAW_REQUIRED, true
	case errors.Is(err, apperr.ErrBracketStarted):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_BRACKET_STARTED, true
	case errors.Is(err, apperr.ErrBracketNotStarted):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_BRACKET_NOT_STARTED, true
	case errors.Is(err, apperr.ErrBracketTooFew):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_BRACKET_TOO_FEW, true
	case errors.Is(err, apperr.ErrBracketMatchNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_BRACKET_MATCH_NOT_FOUND, true
	case errors.Is(err, apperr.ErrBracketGameNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_BRACKET_GAME_NOT_FOUND, true
	case errors.Is(err, apperr.ErrBracketMatchDecided):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_BRACKET_MATCH_DECIDED, true
	case errors.Is(err, apperr.ErrBracketRoundOpen):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_BRACKET_ROUND_OPEN, true
	case errors.Is(err, apperr.ErrBracketRoundClosed):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_BRACKET_ROUND_CLOSED, true
	case errors.Is(err, apperr.ErrBracketFinished):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_BRACKET_FINISHED, true
	case errors.Is(err, apperr.ErrGuestPlayersDisabled):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_GUEST_PLAYERS_DISABLED, true
	case errors.Is(err, apperr.ErrGuestPlayerNotFound):
		return codes.NotFound, corev1.ErrorReason_ERROR_REASON_GUEST_PLAYER_NOT_FOUND, true
	case errors.Is(err, apperr.ErrGuestPlayerInUse):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_GUEST_PLAYER_IN_USE, true
	case errors.Is(err, apperr.ErrRankingNotStarted):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_RANKING_NOT_STARTED, true
	case errors.Is(err, apperr.ErrRankingNotParticipant):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_RANKING_NOT_PARTICIPANT, true
	case errors.Is(err, apperr.ErrRankingFromMeasure):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_RANKING_FROM_MEASURE, true
	case errors.Is(err, apperr.ErrCelebrationNotStarted):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_CELEBRATION_NOT_STARTED, true
	case errors.Is(err, apperr.ErrCelebrationNoWinner):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_CELEBRATION_NO_WINNER, true
	case errors.Is(err, apperr.ErrCelebrationNotParticipant):
		return codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_CELEBRATION_NOT_PARTICIPANT, true
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
	case errors.Is(err, apperr.ErrNotPlatformAdmin):
		return status.Error(codes.Unauthenticated, msgUnauthenticated)
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
