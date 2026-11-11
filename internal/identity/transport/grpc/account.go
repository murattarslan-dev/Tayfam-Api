package grpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

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
	SetAvatar(ctx context.Context, caller, upload id.ID) error
	ClearAvatar(ctx context.Context, caller id.ID) error
}

// AccountDeps wires an AccountServer.
type AccountDeps struct {
	Accounts Accounts
	Sessions SessionManager
	Consents Consents
	Deletion Deleter
	// APIAudience is the audience of full access tokens (AUTH_AUDIENCE).
	APIAudience string
	Log         *slog.Logger
}

// AccountServer implements identityv1.AccountServiceServer. identity's
// verifier also accepts registration-only tokens (for RegistrationService);
// every method here checks that the caller's token is a full access token
// for apiAudience, so an unregistered account reaches none of them.
type AccountServer struct {
	identityv1.UnimplementedAccountServiceServer
	app         Accounts
	sessions    SessionManager
	consents    Consents
	deletion    Deleter
	apiAudience string
	log         *slog.Logger
}

// NewAccountServer returns an AccountServer.
func NewAccountServer(d AccountDeps) *AccountServer {
	return &AccountServer{
		app: d.Accounts, sessions: d.Sessions, consents: d.Consents, deletion: d.Deletion,
		apiAudience: d.APIAudience, log: d.Log,
	}
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
	out := &identityv1.GetAccountResponse{ReferralCode: a.ReferralCode, ActivityVisibility: visibilityToPB[a.ActivityVisibility]}
	if a.AvatarUploadID != (id.ID{}) {
		out.AvatarUploadId = a.AvatarUploadID.String()
	}
	return out, nil
}

// SetAvatar implements identityv1.AccountServiceServer. The upload is
// checked by core with the caller's own token (account.Media).
func (s *AccountServer) SetAvatar(ctx context.Context, req *identityv1.SetAvatarRequest) (*identityv1.SetAvatarResponse, error) {
	u, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	upload, ok := id.Parse(req.GetUploadId())
	if !ok {
		return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_VALIDATION)
	}
	if err := s.app.SetAvatar(ctx, u, upload); err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.SetAvatarResponse{AvatarUploadId: upload.String()}, nil
}

// ClearAvatar implements identityv1.AccountServiceServer.
func (s *AccountServer) ClearAvatar(ctx context.Context, _ *identityv1.ClearAvatarRequest) (*identityv1.ClearAvatarResponse, error) {
	u, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.app.ClearAvatar(ctx, u); err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.ClearAvatarResponse{}, nil
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
	t, err := s.token(ctx)
	return t.user, err
}

// verified is a full access token's user, session and issue time.
type verified struct {
	user, session id.ID
	issuedAt      time.Time
}

// token returns the verified claims of a full access token.
func (s *AccountServer) token(ctx context.Context) (verified, error) {
	c, ok := auth.CallerFrom(ctx)
	if !ok || s.apiAudience == "" || c.Audience != s.apiAudience {
		return verified{}, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	u, okU := id.Parse(c.Subject)
	sid, okS := id.Parse(c.SessionID)
	if !okU || !okS {
		return verified{}, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	return verified{user: u, session: sid, issuedAt: c.IssuedAt}, nil
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
	case errors.Is(err, account.ErrUploadNotFound):
		return fail(codes.NotFound, identityv1.ErrorReason_ERROR_REASON_UPLOAD_NOT_FOUND)
	case errors.Is(err, account.ErrUploadNotReady):
		return fail(codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_UPLOAD_NOT_READY)
	case errors.Is(err, account.ErrUploadInUse):
		return fail(codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_UPLOAD_IN_USE)
	case errors.Is(err, account.ErrMediaUnavailable):
		s.log.WarnContext(ctx, "avatar: core'a sorulamadı", slog.String("err", err.Error()))
		return status.Error(codes.Unavailable, "istek reddedildi")
	default:
		s.log.ErrorContext(ctx, "hesap işlemi başarısız", slog.Any("err", err))
		return status.Error(codes.Internal, "iç hata")
	}
}
