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
	// ErrChannelArchived: the channel is archived (read-only).
	ErrChannelArchived = errors.New("kanal arşivlenmiş")
	// ErrChannelNameTaken: another channel of the tayfa that is not archived
	// has the same name (Turkish case-insensitive).
	ErrChannelNameTaken = errors.New("kanal adı kullanılıyor")
	// ErrChannelsUnavailable: chat runs without a message key
	// (CHAT_MESSAGE_KEYS); channels are off until the key is configured.
	ErrChannelsUnavailable = errors.New("kanal mesajı anahtarı yok")
)

// SlowModeError: the channel's slow mode does not allow the caller another
// message yet.
type SlowModeError struct {
	RetryAfter time.Duration
}

func (e *SlowModeError) Error() string { return "yavaş mod" }
