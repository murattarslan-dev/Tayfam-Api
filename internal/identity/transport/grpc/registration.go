package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/registration"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Registrar is what RegistrationServer needs from the app layer.
type Registrar interface {
	Requirements() registration.Requirements
	Complete(ctx context.Context, user id.ID, in registration.Input) error
}

// RegistrationServer implements identityv1.RegistrationServiceServer.
type RegistrationServer struct {
	identityv1.UnimplementedRegistrationServiceServer
	reg Registrar
	log *slog.Logger
}

// NewRegistrationServer returns a RegistrationServer.
func NewRegistrationServer(r Registrar, log *slog.Logger) *RegistrationServer {
	return &RegistrationServer{reg: r, log: log}
}

// GetRequirements implements identityv1.RegistrationServiceServer.
func (s *RegistrationServer) GetRequirements(ctx context.Context, _ *identityv1.GetRequirementsRequest) (*identityv1.GetRequirementsResponse, error) {
	if _, ok := callerID(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	r := s.reg.Requirements()
	return &identityv1.GetRequirementsResponse{
		PrivacyNoticeVersion: r.Documents.PrivacyNotice,
		TermsVersion:         r.Documents.Terms,
		MinimumAge:           int32(r.MinimumAge), //nolint:gosec // small constant
	}, nil
}

// CompleteRegistration implements identityv1.RegistrationServiceServer. The
// user comes from the verified token, never from the request.
func (s *RegistrationServer) CompleteRegistration(ctx context.Context, req *identityv1.CompleteRegistrationRequest) (*identityv1.CompleteRegistrationResponse, error) {
	user, ok := callerID(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	if len(req.GetBirthDate()) > 10 || len(req.GetDisplayName()) > 400 ||
		len(req.GetAcceptedPrivacyNoticeVersion()) > 32 || len(req.GetAcceptedTermsVersion()) > 32 ||
		len(req.GetReferralCode()) > maxReferralCode {
		return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_UNSPECIFIED)
	}
	err := s.reg.Complete(ctx, user, registration.Input{
		BirthDate:            req.GetBirthDate(),
		DisplayName:          req.GetDisplayName(),
		PrivacyNoticeVersion: req.GetAcceptedPrivacyNoticeVersion(),
		TermsVersion:         req.GetAcceptedTermsVersion(),
		ReferralCode:         req.GetReferralCode(),
	})
	switch {
	case err == nil:
		return &identityv1.CompleteRegistrationResponse{}, nil
	case errors.Is(err, registration.ErrInvalidBirthDate):
		return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_INVALID_BIRTH_DATE)
	case errors.Is(err, registration.ErrUnderAge), errors.Is(err, registration.ErrAgeBlocked):
		return nil, fail(codes.PermissionDenied, identityv1.ErrorReason_ERROR_REASON_AGE_BLOCKED)
	case errors.Is(err, registration.ErrConsentOutdated):
		return nil, fail(codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_CONSENT_OUTDATED)
	case errors.Is(err, registration.ErrAlreadyRegistered):
		return nil, fail(codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_ALREADY_REGISTERED)
	case errors.Is(err, registration.ErrInvalidDisplayName):
		return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_INVALID_DISPLAY_NAME)
	case errors.Is(err, registration.ErrUnknownUser):
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	default:
		s.log.ErrorContext(ctx, "kayıt başarısız", slog.Any("err", err))
		return nil, status.Error(codes.Internal, "iç hata")
	}
}

func callerID(ctx context.Context) (id.ID, bool) {
	c, ok := auth.CallerFrom(ctx)
	if !ok {
		return id.ID{}, false
	}
	return id.Parse(c.Subject)
}
