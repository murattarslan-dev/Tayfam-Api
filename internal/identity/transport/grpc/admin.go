package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/platformadmin"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
)

// AdminAuthServiceName is the gRPC service whose methods (except the public
// AdminSignIn) accept only admin tokens (auth.AdminPolicy).
const AdminAuthServiceName = "tayfam.identity.v1.AdminAuthService"

// Admins is what AdminAuthServer needs from the app layer.
type Admins interface {
	SignIn(ctx context.Context, in platformadmin.SignInInput) (platformadmin.Token, error)
	Get(ctx context.Context, c auth.AdminClaims) (platformadmin.Admin, error)
}

// AdminAuthServer implements identityv1.AdminAuthServiceServer.
type AdminAuthServer struct {
	identityv1.UnimplementedAdminAuthServiceServer
	admins Admins
	log    *slog.Logger
}

// NewAdminAuthServer returns an AdminAuthServer.
func NewAdminAuthServer(a Admins, log *slog.Logger) *AdminAuthServer {
	return &AdminAuthServer{admins: a, log: log}
}

// maxEmail is the longest e-mail address accepted (RFC 5321 path limit).
const maxEmail = 320

// AdminSignIn implements identityv1.AdminAuthServiceServer.
func (s *AdminAuthServer) AdminSignIn(ctx context.Context, req *identityv1.AdminSignInRequest) (*identityv1.AdminSignInResponse, error) {
	if req.GetIdToken() == "" || len(req.GetIdToken()) > maxIDToken ||
		len(req.GetNonce()) < 16 || len(req.GetNonce()) > maxNonce ||
		req.GetAccessEmail() == "" || len(req.GetAccessEmail()) > maxEmail {
		return nil, fail(codes.Unauthenticated, identityv1.ErrorReason_ERROR_REASON_INVALID_CREDENTIALS)
	}
	t, err := s.admins.SignIn(ctx, platformadmin.SignInInput{
		IDToken: req.GetIdToken(), Nonce: req.GetNonce(), AccessEmail: req.GetAccessEmail(),
	})
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.AdminSignInResponse{
		UserId:     t.UserID.String(),
		AdminToken: t.Token,
		ExpireTime: timestamppb.New(t.ExpiresAt),
		AuthTime:   timestamppb.New(t.AuthTime),
	}, nil
}

// GetAdmin implements identityv1.AdminAuthServiceServer. The administrator
// comes from the verified admin token, never from the request.
func (s *AdminAuthServer) GetAdmin(ctx context.Context, _ *identityv1.GetAdminRequest) (*identityv1.GetAdminResponse, error) {
	c, ok := auth.AdminCallerFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	a, err := s.admins.Get(ctx, c)
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.GetAdminResponse{
		UserId:      a.UserID.String(),
		DisplayName: a.DisplayName,
		AuthTime:    timestamppb.New(a.AuthTime),
		ExpireTime:  timestamppb.New(a.ExpiresAt),
	}, nil
}

func (s *AdminAuthServer) mapError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, platformadmin.ErrNotAllowed):
		return fail(codes.Unauthenticated, identityv1.ErrorReason_ERROR_REASON_INVALID_CREDENTIALS)
	case errors.Is(err, platformadmin.ErrNotAdmin):
		return status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	case errors.Is(err, platformadmin.ErrReauthenticationRequired):
		return fail(codes.PermissionDenied, identityv1.ErrorReason_ERROR_REASON_REAUTHENTICATION_REQUIRED)
	case errors.Is(err, platformadmin.ErrUnavailable):
		s.log.WarnContext(ctx, "giriş sağlayıcısı erişilemez", slog.Any("err", err))
		return status.Error(codes.Unavailable, "kimlik doğrulama şu anda yapılamıyor")
	default:
		s.log.ErrorContext(ctx, "panel işlemi başarısız", slog.Any("err", err))
		return status.Error(codes.Internal, "iç hata")
	}
}
