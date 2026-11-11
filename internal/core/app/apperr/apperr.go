// Package apperr holds the errors core's app layer returns to transport.
// Each one corresponds to exactly one ErrorReason in
// proto/tayfam/core/v1/errors.proto; the gRPC transport maps them
// (internal/core/transport/grpc/errors.go). Any other error is internal and
// reaches the client without detail.
//
// The messages are for logs only; clients see the ErrorReason.
package apperr

import "errors"

// Tayfa-scoped errors shared by every service.
var (
	// ErrTayfaNotFound: the tayfa does not exist, is deleted, or the caller
	// is not an active member. These cases are deliberately the same.
	ErrTayfaNotFound    = errors.New("tayfa bulunamadı")
	ErrPermissionDenied = errors.New("yetki yok")
	ErrMuted            = errors.New("üye susturulmuş")
	ErrTayfaReadOnly    = errors.New("tayfa salt okunur")
	// ErrModuleDisabled: the tayfa has turned off the module the method
	// belongs to (tayfa_modules). Reads are refused too.
	ErrModuleDisabled = errors.New("modül kapalı")
)

// TayfaService errors.
var (
	ErrTayfaCreateLimit         = errors.New("günlük tayfa açma sınırı")
	ErrDeletionAlreadyRequested = errors.New("silme talebi zaten var")
	ErrDeletionNotRequested     = errors.New("silme talebi yok")
)

// MembershipService errors.
var (
	ErrRateLimited     = errors.New("çok fazla deneme")
	ErrInvalidJoinCode = errors.New("geçersiz katılım kodu")
	ErrInviteInvalid   = errors.New("geçersiz davet")
	ErrInviteExpired   = errors.New("davetin süresi dolmuş")
	ErrInviteRevoked   = errors.New("davet iptal edilmiş")
	ErrInviteExhausted = errors.New("davetin kullanım hakkı bitmiş")
	ErrInviteNotFound  = errors.New("davet bulunamadı")
	ErrAlreadyMember   = errors.New("zaten üye")
	ErrAlreadyPending  = errors.New("başvuru zaten bekliyor")
	ErrBanned          = errors.New("tayfadan uzaklaştırılmış")
	ErrMemberNotFound  = errors.New("üye bulunamadı")
	ErrRoleHierarchy   = errors.New("rol hiyerarşisi izin vermiyor")
	ErrLastAdmin       = errors.New("son yönetici")
)

// AnnouncementService errors.
var (
	// ErrAnnouncementNotFound: no announcement with this ID in this tayfa
	// that the method can work on (unknown, deleted, or past restoring).
	ErrAnnouncementNotFound = errors.New("duyuru bulunamadı")
)

// EventService errors.
var (
	ErrEventNotFound     = errors.New("etkinlik bulunamadı")
	ErrEventCancelled    = errors.New("etkinlik iptal edilmiş")
	ErrEventEnded        = errors.New("etkinlik bitmiş")
	ErrEventNotGoing     = errors.New("katılım yok")
	ErrEventItemNotFound = errors.New("kayıt bulunamadı")
	ErrEventLimit        = errors.New("liste dolu")
	ErrEventTypeMismatch = errors.New("etkinlik türü uymuyor")
)

// Attendance errors (issue #50). None of them carries the location that was
// sent.
var (
	// ErrCheckInClosed: outside the check-in window (30 minutes before the
	// start until the end).
	ErrCheckInClosed = errors.New("check-in penceresi kapalı")
	// ErrCheckInTooFar: the sent location is farther than the limit from
	// the meeting point.
	ErrCheckInTooFar = errors.New("buluşma noktasına uzak")
	// ErrCheckInAttempts: the caller used all location attempts for the
	// event.
	ErrCheckInAttempts = errors.New("check-in deneme hakkı bitti")
	// ErrCorrectionClosed: outside the organizer's correction window (the
	// seven days after the end).
	ErrCorrectionClosed = errors.New("düzeltme penceresi kapalı")
	// ErrHikeNotStarted: a hike return without a start.
	ErrHikeNotStarted = errors.New("yürüyüş başlamamış")
	// ErrNotAttended: the action needs the caller's verified attendance
	// (logging a fishing catch).
	ErrNotAttended = errors.New("katılım doğrulanmamış")
)

// Event series errors (issue #51).
var (
	ErrEventSeriesNotFound = errors.New("etkinlik serisi bulunamadı")
	ErrEventSeriesEnded    = errors.New("etkinlik serisi sonlandırılmış")
)

// QAService errors (issue #59).
var (
	// ErrQuestionNotFound: no visible question with this ID in this tayfa
	// that the method can work on (unknown, deleted, or past restoring).
	ErrQuestionNotFound = errors.New("soru bulunamadı")
	// ErrAnswerNotFound: the same for answers; an answer of a deleted
	// question is not found either.
	ErrAnswerNotFound = errors.New("cevap bulunamadı")
	ErrTagNotFound    = errors.New("etiket bulunamadı")
	// ErrOwnAnswerVote: nobody votes for their own answer.
	ErrOwnAnswerVote = errors.New("kendi cevabına oy verilemez")
	// ErrTagLimit: the tayfa already has the most tags it may have.
	ErrTagLimit = errors.New("etiket sınırı")
)

// PollService errors (issue #60). None of them says anything about votes.
var (
	ErrPollNotFound = errors.New("anket bulunamadı")
	ErrPollClosed   = errors.New("anket kapalı")
	// ErrPollAnonymous: voters of an anonymous poll are never listed.
	ErrPollAnonymous = errors.New("anket gizli")
)

// Social errors (issue #71): friendships, blocks and direct invitations.
var (
	// ErrUserNotFound: no live user with this ID, or a block between the
	// caller and the user (either way). The cases are deliberately the
	// same, so nobody learns that they were blocked.
	ErrUserNotFound = errors.New("kullanıcı bulunamadı")
	// ErrFriendRequestLimit: the caller sent the most friend requests a day
	// allows.
	ErrFriendRequestLimit = errors.New("günlük arkadaşlık isteği sınırı")
	// ErrFriendRequestNotFound: no friend request to answer.
	ErrFriendRequestNotFound = errors.New("arkadaşlık isteği bulunamadı")
	ErrAlreadyFriends        = errors.New("zaten arkadaş")
	// ErrFriendRequestPending: the caller's request waits for an answer.
	ErrFriendRequestPending = errors.New("arkadaşlık isteği bekliyor")
	// ErrNotFriends: only friends can be invited directly.
	ErrNotFriends = errors.New("arkadaş değil")
	// ErrDirectInviteLimit: the caller sent the most direct invitations a
	// day allows.
	ErrDirectInviteLimit = errors.New("günlük davet sınırı")
	// ErrInvitationNotFound: no unanswered direct invitation.
	ErrInvitationNotFound = errors.New("davet bulunamadı")
	ErrAlreadyInvited     = errors.New("zaten davet edilmiş")
	// ErrInviteNotAllowed: the user cannot be invited into this tayfa (a
	// ban, a waiting join request). Which one is not said.
	ErrInviteNotAllowed = errors.New("davet edilemez")
)

// ReportService errors (issue #76). None of them says who reported or
// whether someone else reported the same target.
var (
	// ErrReportNotFound: no report with this ID the caller may see (not
	// theirs, not in the tayfa's queue, or about the caller).
	ErrReportNotFound = errors.New("şikâyet bulunamadı")
	// ErrReportTargetNotFound: the reported user or content does not exist
	// in the tayfa or the caller cannot see it.
	ErrReportTargetNotFound = errors.New("şikâyet hedefi bulunamadı")
	// ErrUnavailable: another service core needed could not answer (chat,
	// for a direct message report); the caller may retry. No ErrorInfo.
	ErrUnavailable = errors.New("bağımlı servis yanıt vermedi")
	// ErrReportLimit: the caller filed the most reports a day allows.
	ErrReportLimit = errors.New("günlük şikâyet sınırı")
	// ErrReportAlreadyResolved: resolutions do not change.
	ErrReportAlreadyResolved = errors.New("şikâyet zaten çözülmüş")
	// ErrReportActionMissing: the resolution names an action that was not
	// taken.
	ErrReportActionMissing = errors.New("çözümdeki işlem yapılmamış")

	// Platform panel (issue #104).
	// ErrNotPlatformAdmin: identity says the admin token's user is no
	// longer a platform administrator.
	ErrNotPlatformAdmin = errors.New("platform yöneticisi değil")
	// ErrReauthenticationRequired: the Google sign-in behind the admin
	// token is older than 5 minutes.
	ErrReauthenticationRequired = errors.New("yeniden giriş gerekli")
	// ErrReasonRequired: a panel change without a valid reason.
	ErrReasonRequired = errors.New("gerekçe zorunlu")
	// ErrDecisionNotApplicable: the decision does not fit the report.
	ErrDecisionNotApplicable = errors.New("karar bu şikâyete uygulanamaz")
	// ErrTayfaStateConflict: the tayfa is already in the requested state.
	ErrTayfaStateConflict = errors.New("tayfa zaten bu durumda")
)

// MediaService errors (issue #135). None of them says whose an upload is.
var (
	// ErrUploadNotFound: no upload with this ID that the caller owns, or one
	// of another purpose or tayfa than the content it is attached to.
	ErrUploadNotFound = errors.New("yükleme bulunamadı")
	// ErrUploadNotReady: the upload is not processed (yet).
	ErrUploadNotReady = errors.New("yükleme hazır değil")
	// ErrUploadInUse: the upload is attached to other content.
	ErrUploadInUse = errors.New("yükleme başka içerikte")
	// ErrUploadNotReceived: CompleteUpload found a variant not stored yet.
	ErrUploadNotReceived = errors.New("dosya alınmadı")
	// ErrUploadRateLimited: too many uploads in the last hour.
	ErrUploadRateLimited = errors.New("yükleme sıklığı sınırı")
	// ErrUploadQuotaExceeded: the user's stored media would exceed the quota.
	ErrUploadQuotaExceeded = errors.New("yükleme kotası")
)

// ValidationError reports a request field that failed validation. Field is
// the request field name as written in the proto ("name", "update_mask");
// the rejected value is never kept.
type ValidationError struct {
	Field string
}

func (e *ValidationError) Error() string { return "geçersiz alan: " + e.Field }

// Validation returns a ValidationError for field.
func Validation(field string) error { return &ValidationError{Field: field} }
