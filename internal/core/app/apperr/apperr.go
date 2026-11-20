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
	ErrTayfaCreateLimit = errors.New("günlük tayfa açma sınırı")
	// ErrInstantEventLimit: the caller already organizes the most open
	// instant events (ADR-0032 §5).
	ErrInstantEventLimit        = errors.New("açık anlık etkinlik sınırı")
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
	// ErrEventItemTaken: a list item someone else took (issues #198, #241).
	ErrEventItemTaken = errors.New("malzeme başkasında")
)

// CatalogService errors (issue #237, ADR-0025).
var (
	// ErrCatalogTypeNotFound: the catalog has no event type with this key
	// and version.
	ErrCatalogTypeNotFound = errors.New("katalogda tür sürümü yok")
)

// Event component errors (issue #238; ADR-0025 §4–§6, ADR-0037 §2). They
// belong to the component framework (package component) and are shared by
// every component.
var (
	// ErrComponentNotFound: the event's type version has no component with
	// this key, or the component is of another kind than the endpoint.
	ErrComponentNotFound = errors.New("bileşen bulunamadı")
	// ErrComponentDisabled: the organizer turned the optional component
	// off for this event (or left it off); reads are refused too.
	ErrComponentDisabled = errors.New("bileşen kapalı")
	// ErrComponentLocked: the organizer's selections cannot change once
	// the event has started.
	ErrComponentLocked = errors.New("bileşen seçimleri kilitli")
	// ErrComponentFrozen: component data cannot change from seven days
	// after the event's end (ADR-0025 §5).
	ErrComponentFrozen = errors.New("bileşen verisi donmuş")
)

// Vehicles component errors (issue #240; ADR-0037 §3).
var (
	// ErrVehicleFull: the vehicle has no free seat (or offers none).
	ErrVehicleFull = errors.New("araçta boş koltuk yok")
	// ErrVehicleSeatsTaken: fewer seats than the riders already approved.
	ErrVehicleSeatsTaken = errors.New("koltuk sayısı onaylanan yolculardan az")
	// ErrSeatRequestDriver: someone with a vehicle of their own in the
	// component asked for a seat.
	ErrSeatRequestDriver = errors.New("aracı olan koltuk isteyemez")
)

// Tayfa vocabulary errors (issue #244; ADR-0037 §4).
var (
	// ErrVocabularyNotFound: no such value in the tayfa's namespace.
	ErrVocabularyNotFound = errors.New("sözlük değeri bulunamadı")
	// ErrVocabularyMerged: the value or the merge target is already
	// merged.
	ErrVocabularyMerged = errors.New("sözlük değeri birleştirilmiş")
	// ErrVocabularyNotMerged: undoing a merge of a value that is not merged.
	ErrVocabularyNotMerged = errors.New("sözlük değeri birleştirilmemiş")
)

// Measure component errors (issue #245; ADR-0025 §4, §5).
var (
	// ErrMeasurementNotStarted: a measurement before the event's start.
	ErrMeasurementNotStarted = errors.New("etkinlik başlamadan ölçüm girilmez")
	// ErrMeasurementNotParticipant: a value for someone who is not a
	// participant of the event.
	ErrMeasurementNotParticipant = errors.New("kişi etkinliğin katılımcısı değil")
	// ErrMeasurementScope: a total for a personal measure or a person's
	// value for a total one.
	ErrMeasurementScope = errors.New("ölçümün kapsamı farklı")
)

// Teams component errors (issue #248; ADR-0025 §4, §5).
var (
	// ErrTeamsManual: generating teams when the component's mode is manual.
	ErrTeamsManual = errors.New("takımlar elle kurulur")
	// ErrTeamsTooFew: too few participants for the teams.
	ErrTeamsTooFew = errors.New("takımlar için katılımcı az")
	// ErrTeamsNotParticipant: a person put in a team is not a participant
	// of the event.
	ErrTeamsNotParticipant = errors.New("kişi etkinliğin katılımcısı değil")
)

// Draw component errors (issue #249; ADR-0025 §4, §5).
var (
	// ErrDrawAlreadyMade: the component's draw was made; its result is
	// fixed.
	ErrDrawAlreadyMade = errors.New("kura çekildi")
	// ErrDrawTooFew: fewer than two participants (or teams) to draw.
	ErrDrawTooFew = errors.New("kura için katılımcı az")
)

// Scoreboard component errors (issue #250; ADR-0025 §4, §5, ADR-0037 §5).
var (
	// ErrScorePresetUnavailable: the preset is not one the component
	// offers, is retired, or the component offers presets and none was
	// named.
	ErrScorePresetUnavailable = errors.New("ön ayar seçilemez")
	// ErrScoreTableNotFound: no such table in the component.
	ErrScoreTableNotFound = errors.New("masa bulunamadı")
	// ErrScoreTableFinished: the table is finished; a manager reopens it
	// before a new round.
	ErrScoreTableFinished = errors.New("masa bitti")
	// ErrScoreTableStarted: the table has scores; its players no longer
	// change.
	ErrScoreTableStarted = errors.New("masada skor var")
	// ErrScorePlayerNotParticipant: a player is not a participant of the
	// event, or a team is not one of the named teams component's teams.
	ErrScorePlayerNotParticipant = errors.New("oyuncu katılımcı değil")
	// ErrScoreNotStarted: scores are entered once the event has started.
	ErrScoreNotStarted = errors.New("etkinlik başlamadan skor girilmez")
)

// Bracket component errors (issue #251; ADR-0025 §4, §5, ADR-0037 §3).
var (
	// ErrBracketDrawRequired: the bracket seeds from the type's draw
	// component, and its draw was not made (or the type has none).
	ErrBracketDrawRequired = errors.New("eşleşme için kura çekilmeli")
	// ErrBracketStarted: the bracket was started; a manager resets it
	// before starting again.
	ErrBracketStarted = errors.New("eşleşme ağacı başladı")
	// ErrBracketNotStarted: the bracket has not been started.
	ErrBracketNotStarted = errors.New("eşleşme ağacı başlamadı")
	// ErrBracketTooFew: fewer than two entrants in the draw.
	ErrBracketTooFew = errors.New("eşleşme için katılımcı az")
	// ErrBracketMatchNotFound: no such match in the bracket.
	ErrBracketMatchNotFound = errors.New("maç bulunamadı")
	// ErrBracketGameNotFound: no such game in the match, or not its last
	// game (only the last one is deleted).
	ErrBracketGameNotFound = errors.New("oyun bulunamadı")
	// ErrBracketMatchDecided: the match is decided (or a bye); no more
	// games.
	ErrBracketMatchDecided = errors.New("maç sonuçlandı")
	// ErrBracketRoundOpen: a match of the current round is not decided
	// yet.
	ErrBracketRoundOpen = errors.New("turda sonuçlanmamış maç var")
	// ErrBracketRoundClosed: a later round was paired from this match's
	// round; its games no longer change.
	ErrBracketRoundClosed = errors.New("sonraki tur başladı")
	// ErrBracketFinished: the bracket has no next round.
	ErrBracketFinished = errors.New("eşleşme ağacı bitti")
)

// Guest player errors (issue #252; ADR-0037 §6). None of them carries a
// nickname.
var (
	// ErrGuestPlayersDisabled: the scoreboard component does not take
	// guest players (its `guests` parameter is off).
	ErrGuestPlayersDisabled = errors.New("bileşen misafir oyuncu almıyor")
	// ErrGuestPlayerNotFound: no such guest player in the event.
	ErrGuestPlayerNotFound = errors.New("misafir oyuncu bulunamadı")
	// ErrGuestPlayerInUse: the guest has a seat at a table or a place in a
	// draw; it is not removed.
	ErrGuestPlayerInUse = errors.New("misafir oyuncu masada ya da kurada")
)

// Ranking component errors (issue #253; ADR-0037 §3).
var (
	// ErrRankingNotStarted: a ranking before the event's start.
	ErrRankingNotStarted = errors.New("etkinlik başlamadan derece girilmez")
	// ErrRankingNotParticipant: a ranking for someone who is not a
	// participant of the event.
	ErrRankingNotParticipant = errors.New("kişi etkinliğin katılımcısı değil")
	// ErrRankingFromMeasure: a value for a ranking computed from a measure;
	// the measure is entered instead.
	ErrRankingFromMeasure = errors.New("derece ölçümden hesaplanır")
)

// Celebration component errors (issue #255; ADR-0025 §4).
var (
	// ErrCelebrationNotStarted: an announcement before the event's start.
	ErrCelebrationNotStarted = errors.New("etkinlik başlamadan kazanan ilan edilmez")
	// ErrCelebrationNoWinner: the source has no winner to announce yet (an
	// open table, an unfinished bracket, an empty or hidden ranking).
	ErrCelebrationNoWinner = errors.New("ilan edilecek kazanan yok")
	// ErrCelebrationNotParticipant: a winner chosen by hand who is not a
	// participant of the event.
	ErrCelebrationNotParticipant = errors.New("kazanan etkinliğin katılımcısı değil")
)

// Type flag errors (issue #257; ADR-0037 §1).
var (
	// ErrAttendeesHidden: the attendee list of an event whose type's
	// attendance is sensitive (sensitive_attendance) asked by someone other
	// than the organizer, a moderator or an admin; the counts stay on the
	// event.
	ErrAttendeesHidden = errors.New("katılımcı listesi yalnızca organizatöre açık")
	// ErrAlbumConsentRequired: an album photo of a type with album_notice
	// sent without the uploader's consent acknowledgement (consent_ack).
	ErrAlbumConsentRequired = errors.New("albüm için izin onayı gerekli")
)

// Şimdi başlat errors (issue #263; ADR-0038 §1, §2).
var (
	// ErrNowLimitReached: the caller already organizes an open now event
	// (at most one at a time, in tayfas and instant events together).
	ErrNowLimitReached = errors.New("açık şimdi etkinliği sınırı")
	// ErrEventNotOpenEnded: "Bitir" on an event that was not started now.
	ErrEventNotOpenEnded = errors.New("etkinlik açık uçlu değil")
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
	// (adding a log entry, adding an album photo).
	ErrNotAttended = errors.New("katılım doğrulanmamış")
	// ErrAlbumClosed: the event's album takes no photos now (before the
	// start, or from seven days after the end; issue #206).
	ErrAlbumClosed = errors.New("albüm kapalı")
)

// Keşfet (issue #210, ADR-0033).
var (
	// ErrFeatureDisabled: the feature is turned off on this server
	// (DISCOVER_ENABLED); nothing was read.
	ErrFeatureDisabled = errors.New("özellik kapalı")
	// ErrDiscoverTooFewMembers: Keşfet needs at least five active members.
	ErrDiscoverTooFewMembers = errors.New("keşfet için üye sayısı yetersiz")
	// ErrFeedbackClosed: the event takes no "Was everything all right?"
	// answer (not a discoverable in-person event, not ended, or more than
	// seven days after the end).
	ErrFeedbackClosed = errors.New("geri bildirim kapalı")
	// ErrSafetyContactClosed: no safety contact can be chosen or changed
	// (online, cancelled or ended event, or the alert went out).
	ErrSafetyContactClosed = errors.New("güvenlik kişisi değiştirilemez")
	// ErrSafetyContactNotFound: no safety contact was chosen.
	ErrSafetyContactNotFound = errors.New("güvenlik kişisi yok")
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
