// Package apperr holds the errors chat's app layer returns to transport.
// Each one corresponds to exactly one ErrorReason in
// proto/tayfam/chat/v1/errors.proto; the gRPC transport maps them
// (internal/chat/transport/grpc/errors.go). Any other error is internal and
// reaches the caller without detail.
//
// The messages are for logs only; callers see the ErrorReason.
package apperr

// ValidationError reports a request field that failed validation. Field is
// the request field name as written in the proto; the rejected value is
// never kept.
type ValidationError struct {
	Field string
}

func (e *ValidationError) Error() string { return "geçersiz alan: " + e.Field }

// Validation returns a ValidationError for field.
func Validation(field string) error { return &ValidationError{Field: field} }
