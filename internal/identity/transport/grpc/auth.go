package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/session"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// errorDomain is the ErrorInfo domain of identity's errors.
const errorDomain = "identity.tayfam.app"

// Sessions is what AuthServer needs from the app layer.
type Sessions interface {
	SignIn(ctx context.Context, in session.SignInInput) (session.Tokens, error)
	Refresh(ctx context.Context, refreshToken string) (session.Tokens, error)
	SignOut(ctx context.Context, sessionID id.ID) error
}

// AuthServer implements identityv1.AuthServiceServer.
type AuthServer struct {
	identityv1.UnimplementedAuthServiceServer
	sessions Sessions
	log      *slog.Logger
}

// NewAuthServer returns an AuthServer.
func NewAuthServer(s Sessions, log *slog.Logger) *AuthServer {
	return &AuthServer{sessions: s, log: log}
}

var providers = map[identityv1.Provider]string{
	identityv1.Provider_PROVIDER_GOOGLE: "google",
	identityv1.Provider_PROVIDER_APPLE:  "apple",
}

var platforms = map[identityv1.Platform]string{
	identityv1.Platform_PLATFORM_ANDROID: "android",
	identityv1.Platform_PLATFORM_IOS:     "ios",
	identityv1.Platform_PLATFORM_WEB:     "web",
}

// Input limits checked before any work is done.
const (
	maxIDToken = 8 << 10
	maxNonce   = 256
	maxRefresh = 64
)

// SignIn implements identityv1.AuthServiceServer.
func (s *AuthServer) SignIn(ctx context.Context, req *identityv1.SignInRequest) (*identityv1.SignInResponse, error) {
	provider, ok := providers[req.GetProvider()]
	if !ok {
		return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_UNSUPPORTED_PROVIDER)
	}
	platform := platforms[req.GetPlatform()]
	if platform == "" || len(req.GetIdToken()) > maxIDToken || len(req.GetNonce()) > maxNonce {
		return nil, fail(codes.Unauthenticated, identityv1.ErrorReason_ERROR_REASON_INVALID_CREDENTIALS)
	}
	t, err := s.sessions.SignIn(ctx, session.SignInInput{
		Provider: provider, IDToken: req.GetIdToken(), Nonce: req.GetNonce(), Platform: platform,
	})
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.SignInResponse{Tokens: tokens(t)}, nil
}

// Refresh implements identityv1.AuthServiceServer.
func (s *AuthServer) Refresh(ctx context.Context, req *identityv1.RefreshRequest) (*identityv1.RefreshResponse, error) {
	if len(req.GetRefreshToken()) > maxRefresh {
		return nil, fail(codes.Unauthenticated, identityv1.ErrorReason_ERROR_REASON_INVALID_REFRESH)
	}
	t, err := s.sessions.Refresh(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.RefreshResponse{Tokens: tokens(t)}, nil
}

// SignOut implements identityv1.AuthServiceServer. The session comes from
// the verified access token, never from the request body, so a caller can
// only end its own session.
func (s *AuthServer) SignOut(ctx context.Context, _ *identityv1.SignOutRequest) (*identityv1.SignOutResponse, error) {
	c, ok := auth.CallerFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	sid, ok := id.Parse(c.SessionID)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	if err := s.sessions.SignOut(ctx, sid); err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.SignOutResponse{}, nil
}

func (s *AuthServer) mapError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, session.ErrInvalidCredentials):
		return fail(codes.Unauthenticated, identityv1.ErrorReason_ERROR_REASON_INVALID_CREDENTIALS)
	case errors.Is(err, session.ErrUnsupportedProvider):
		return fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_UNSUPPORTED_PROVIDER)
	case errors.Is(err, session.ErrSanctioned):
		return fail(codes.PermissionDenied, identityv1.ErrorReason_ERROR_REASON_ACCOUNT_SANCTIONED)
	case errors.Is(err, session.ErrAgeBlocked):
		return fail(codes.PermissionDenied, identityv1.ErrorReason_ERROR_REASON_AGE_BLOCKED)
	case errors.Is(err, session.ErrInvalidRefresh):
		return fail(codes.Unauthenticated, identityv1.ErrorReason_ERROR_REASON_INVALID_REFRESH)
	case errors.Is(err, session.ErrRefreshConflict):
		return fail(codes.Aborted, identityv1.ErrorReason_ERROR_REASON_REFRESH_CONFLICT)
	case errors.Is(err, session.ErrUnavailable):
		s.log.WarnContext(ctx, "giriş sağlayıcısı erişilemez", slog.Any("err", err))
		return status.Error(codes.Unavailable, "kimlik doğrulama şu anda yapılamıyor")
	default:
		s.log.ErrorContext(ctx, "kimlik işlemi başarısız", slog.Any("err", err))
		return status.Error(codes.Internal, "iç hata")
	}
}

// fail builds a status with an ErrorInfo detail. The message is fixed and
// says nothing about which check failed.
func fail(code codes.Code, reason identityv1.ErrorReason) error {
	st := status.New(code, "istek reddedildi")
	if reason == identityv1.ErrorReason_ERROR_REASON_UNSPECIFIED {
		return st.Err()
	}
	if withInfo, err := st.WithDetails(&errdetails.ErrorInfo{Reason: reason.String(), Domain: errorDomain}); err == nil {
		st = withInfo
	}
	return st.Err()
}

func tokens(t session.Tokens) *identityv1.Tokens {
	return &identityv1.Tokens{
		UserId:              t.UserID.String(),
		SessionId:           t.SessionID.String(),
		AccessToken:         t.AccessToken,
		AccessExpireTime:    timestamppb.New(t.AccessExpiresAt),
		RefreshToken:        t.RefreshToken,
		RefreshExpireTime:   timestamppb.New(t.RefreshExpiresAt),
		RegistrationPending: t.RegistrationPending,
		ConsentRequired:     t.ConsentRequired,
	}
}
