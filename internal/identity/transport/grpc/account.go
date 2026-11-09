package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/account"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Accounts is what AccountServer needs from the app layer (account.Service).
type Accounts interface {
	Get(ctx context.Context, caller id.ID) (account.Account, error)
	RotateReferralCode(ctx context.Context, caller id.ID) (string, error)
	FindByReferralCode(ctx context.Context, caller id.ID, code string) (account.Found, error)
	SetActivityVisibility(ctx context.Context, caller id.ID, v account.Visibility) (account.Visibility, error)
}

// AccountServer implements identityv1.AccountServiceServer. identity's
// verifier also accepts registration-only tokens (for RegistrationService);
// every method here checks that the caller's token is a full access token
// for apiAudience, so an unregistered account reaches none of them.
type AccountServer struct {
	identityv1.UnimplementedAccountServiceServer
	app         Accounts
	apiAudience string
	log         *slog.Logger
}

// NewAccountServer returns an AccountServer. apiAudience is the audience of
// full access tokens (AUTH_AUDIENCE).
func NewAccountServer(app Accounts, apiAudience string, log *slog.Logger) *AccountServer {
	return &AccountServer{app: app, apiAudience: apiAudience, log: log}
}

var visibilityToPB = map[account.Visibility]identityv1.ActivityVisibility{
	account.VisibilityPrivate: identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE,
	account.VisibilityTayfa:   identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA,
	account.VisibilityPublic:  identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC,
}

var visibilityFromPB = map[identityv1.ActivityVisibility]account.Visibility{
	identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE: account.VisibilityPrivate,
	identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA:   account.VisibilityTayfa,
	identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC:  account.VisibilityPublic,
}

// maxReferralCode bounds the code before any work; a code is 10 characters.
const maxReferralCode = 32

// GetAccount implements identityv1.AccountServiceServer.
func (s *AccountServer) GetAccount(ctx context.Context, _ *identityv1.GetAccountRequest) (*identityv1.GetAccountResponse, error) {
	u, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.app.Get(ctx, u)
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.GetAccountResponse{ReferralCode: a.ReferralCode, ActivityVisibility: visibilityToPB[a.ActivityVisibility]}, nil
}

// RotateReferralCode implements identityv1.AccountServiceServer.
func (s *AccountServer) RotateReferralCode(ctx context.Context, _ *identityv1.RotateReferralCodeRequest) (*identityv1.RotateReferralCodeResponse, error) {
	u, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	code, err := s.app.RotateReferralCode(ctx, u)
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.RotateReferralCodeResponse{ReferralCode: code}, nil
}

// FindUserByReferralCode implements identityv1.AccountServiceServer. The
// code is never logged.
func (s *AccountServer) FindUserByReferralCode(ctx context.Context, req *identityv1.FindUserByReferralCodeRequest) (*identityv1.FindUserByReferralCodeResponse, error) {
	u, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetReferralCode()) > maxReferralCode {
		return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_UNSPECIFIED)
	}
	f, err := s.app.FindByReferralCode(ctx, u, req.GetReferralCode())
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	out := &identityv1.FindUserByReferralCodeResponse{UserId: f.UserID.String(), DisplayName: f.DisplayName}
	if f.AvatarUploadID != (id.ID{}) {
		out.AvatarUploadId = f.AvatarUploadID.String()
	}
	return out, nil
}

// SetActivityVisibility implements identityv1.AccountServiceServer.
func (s *AccountServer) SetActivityVisibility(ctx context.Context, req *identityv1.SetActivityVisibilityRequest) (*identityv1.SetActivityVisibilityResponse, error) {
	u, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	v, ok := visibilityFromPB[req.GetActivityVisibility()]
	if !ok {
		return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_UNSPECIFIED)
	}
	got, err := s.app.SetActivityVisibility(ctx, u, v)
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.SetActivityVisibilityResponse{ActivityVisibility: visibilityToPB[got]}, nil
}

// caller returns the verified user of a full access token.
func (s *AccountServer) caller(ctx context.Context) (id.ID, error) {
	c, ok := auth.CallerFrom(ctx)
	if !ok || s.apiAudience == "" || c.Audience != s.apiAudience {
		return id.ID{}, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	u, ok := id.Parse(c.Subject)
	if !ok {
		return id.ID{}, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	return u, nil
}

func (s *AccountServer) mapError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, account.ErrRateLimited):
		return fail(codes.ResourceExhausted, identityv1.ErrorReason_ERROR_REASON_RATE_LIMITED)
	case errors.Is(err, account.ErrCodeNotFound):
		return fail(codes.NotFound, identityv1.ErrorReason_ERROR_REASON_REFERRAL_CODE_NOT_FOUND)
	case errors.Is(err, account.ErrInvalidVisibility):
		return fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_UNSPECIFIED)
	case errors.Is(err, account.ErrUnknownUser):
		return status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	default:
		s.log.ErrorContext(ctx, "hesap işlemi başarısız", slog.Any("err", err))
		return status.Error(codes.Internal, "iç hata")
	}
}
