// Package apperr holds the errors chat's app layer returns to transport.
// Each one corresponds to exactly one ErrorReason in
// proto/tayfam/chat/v1/errors.proto; the gRPC transport maps them
// (internal/chat/transport/grpc/errors.go). Any other error is internal and
// reaches the caller without detail.
//
// The messages are for logs only; callers see the ErrorReason.
package apperr

import (
	"errors"
	"time"
)

// NotificationService errors.
var (
	// ErrNotificationNotFound: no such notification of the caller. Another
	// user's notification is indistinguishable from a missing one.
	ErrNotificationNotFound = errors.New("bildirim bulunamadı")
	// ErrTayfaNotFound: core says the caller is not an active member of the
	// tayfa (or it does not exist).
	ErrTayfaNotFound = errors.New("tayfa bulunamadı")
	// ErrUnavailable: core could not answer; the caller may retry.
	ErrUnavailable = errors.New("core yanıt vermedi")
)

// ValidationError reports a request field that failed validation. Field is
// the request field name as written in the proto; the rejected value is
// never kept.
type ValidationError struct {
	Field string
}

func (e *ValidationError) Error() string { return "geçersiz alan: " + e.Field }

// Validation returns a ValidationError for field.
func Validation(field string) error { return &ValidationError{Field: field} }

// ChannelService errors (issue #81).
var (
	// ErrChannelNotFound: no such channel in the tayfa, or a staff channel
	// the caller may not see. The cases are indistinguishable on purpose.
	ErrChannelNotFound = errors.New("kanal bulunamadı")
	// ErrMessageNotFound: no such message in the channel, or a deletion that
	// can no longer be restored.
	ErrMessageNotFound = errors.New("mesaj bulunamadı")
	// ErrPermissionDenied: the caller's role is too low for the action.
	ErrPermissionDenied = errors.New("yetki yok")
	// ErrMuted: the caller is muted in the tayfa and the action writes.
	ErrMuted = errors.New("susturulmuş")
	// ErrTayfaReadOnly: the tayfa is suspended, archived or waiting for
	// deletion; channels take no changes.
	ErrTayfaReadOnly = errors.New("tayfa salt okunur")
	// ErrRoleHierarchy: the message's author has the caller's role or a
	// higher one.
	ErrRoleHierarchy = errors.New("rol hiyerarşisi")
	// ErrChannelArchived: the channel is archived (read-only).
	ErrChannelArchived = errors.New("kanal arşivlenmiş")
	// ErrChannelNameTaken: another channel of the tayfa that is not archived
	// has the same name (Turkish case-insensitive).
	ErrChannelNameTaken = errors.New("kanal adı kullanılıyor")
	// ErrChannelsUnavailable: chat runs without a message key
	// (CHAT_MESSAGE_KEYS); channels are off until the key is configured.
	ErrChannelsUnavailable = errors.New("kanal mesajı anahtarı yok")
)

// ErrRateLimited: the caller sends messages too fast (one per-user limit
// for channel and direct messages, REST and WebSocket together; issue
// #101).
var ErrRateLimited = errors.New("mesaj gönderim hızı aşıldı")

// SlowModeError: the channel's slow mode does not allow the caller another
// message yet.
type SlowModeError struct {
	RetryAfter time.Duration
}

func (e *SlowModeError) Error() string { return "yavaş mod" }

// Real-time errors (issue #82).
var (
	// ErrSessionEnded: the caller's session was revoked a short while ago
	// (its access token may still be valid); no connection for it.
	ErrSessionEnded = errors.New("oturum kapatılmış")
)

// DirectMessageService errors (issue #95).
var (
	// ErrConversationNotFound: no such conversation of the caller, or a
	// request the caller declined. Indistinguishable on purpose.
	ErrConversationNotFound = errors.New("konuşma bulunamadı")
	// ErrDirectMessageNotAllowed: core says the caller cannot write to the
	// user (block, no shared tayfa nor friendship, unknown user); which one
	// is never said.
	ErrDirectMessageNotAllowed = errors.New("birebir mesaj izni yok")
	// ErrMessageRequestPending: the caller's request waits for an answer
	// (or was declined, which the sender is never told).
	ErrMessageRequestPending = errors.New("mesaj isteği cevap bekliyor")
	// ErrMessageRequestLimit: the caller's daily message request limit.
	ErrMessageRequestLimit = errors.New("günlük mesaj isteği sınırı")
	// ErrMessageRequestNotFound: no request waits for the caller in the
	// conversation.
	ErrMessageRequestNotFound = errors.New("mesaj isteği bulunamadı")
)

// End-to-end encryption errors (issue #127, ADR-0018; E2EService and
// SendDirectMessage with envelopes).
var (
	// ErrE2EDeviceNotRegistered: the caller's session has no device.
	ErrE2EDeviceNotRegistered = errors.New("e2e cihazı kayıtlı değil")
	// ErrE2EDeviceAlreadyRegistered: the session has a device with other
	// keys.
	ErrE2EDeviceAlreadyRegistered = errors.New("oturumun başka anahtarlı cihazı var")
	// ErrE2EDeviceLimit: the user has the most devices allowed.
	ErrE2EDeviceLimit = errors.New("cihaz sınırı dolu")
	// ErrE2ERecipientUnavailable: the recipient has no device.
	ErrE2ERecipientUnavailable = errors.New("alıcının e2e cihazı yok")
	// ErrE2ENotPaired: the caller's device does not hold the history key.
	ErrE2ENotPaired = errors.New("cihaz eşleştirilmemiş")
	// ErrE2EAlreadyPaired: the caller's device holds the history key.
	ErrE2EAlreadyPaired = errors.New("cihaz zaten eşleştirilmiş")
	// ErrE2EPairedDeviceExists: another device of the user is paired.
	ErrE2EPairedDeviceExists = errors.New("eşleştirilmiş başka cihaz var")
	// ErrE2EHistoryKeyStale: not the current history key version.
	ErrE2EHistoryKeyStale = errors.New("geçmiş anahtarı sürümü güncel değil")
	// ErrE2ELinkRequestNotFound: no usable pairing request of the user.
	ErrE2ELinkRequestNotFound = errors.New("eşleştirme isteği bulunamadı")
)

// DevicesChangedError: envelopes or key shares do not cover exactly the
// current devices. Missing have none; Stale are gone or were replaced.
// Device IDs only, never content.
type DevicesChangedError struct {
	Missing []string
	Stale   []string
}

func (e *DevicesChangedError) Error() string { return "cihaz listesi değişti" }
