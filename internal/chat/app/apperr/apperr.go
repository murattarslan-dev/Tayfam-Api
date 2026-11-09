// Package apperr holds the errors chat's app layer returns to transport.
// Each one corresponds to exactly one ErrorReason in
// proto/tayfam/chat/v1/errors.proto; the gRPC transport maps them
// (internal/chat/transport/grpc/errors.go). Any other error is internal and
// reaches the caller without detail.
//
// The messages are for logs only; callers see the ErrorReason.
package apperr

import "errors"

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
