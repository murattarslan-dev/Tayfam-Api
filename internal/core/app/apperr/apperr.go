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

// ValidationError reports a request field that failed validation. Field is
// the request field name as written in the proto ("name", "update_mask");
// the rejected value is never kept.
type ValidationError struct {
	Field string
}

func (e *ValidationError) Error() string { return "geçersiz alan: " + e.Field }

// Validation returns a ValidationError for field.
func Validation(field string) error { return &ValidationError{Field: field} }
