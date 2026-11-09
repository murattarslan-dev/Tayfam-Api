package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/consent"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/deletion"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/session"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// SessionManager is the session listing and remote sign-out of
// session.Service.
type SessionManager interface {
	List(ctx context.Context, c session.Caller) ([]session.Info, error)
	Revoke(ctx context.Context, c session.Caller, target id.ID) error
	RevokeOthers(ctx context.Context, c session.Caller) (int, error)
}

// Consents is consent.Service.
type Consents interface {
	Status(ctx context.Context, user id.ID) (consent.Status, error)
	Accept(ctx context.Context, user id.ID, privacyNotice, terms string) error
}

// Deleter is deletion.Service.
type Deleter interface {
	Delete(ctx context.Context, c deletion.Caller) error
}

var platformToPB = map[string]identityv1.Platform{
	"android": identityv1.Platform_PLATFORM_ANDROID,
	"ios":     identityv1.Platform_PLATFORM_IOS,
	"web":     identityv1.Platform_PLATFORM_WEB,
}

// maxVersion bounds a document version before any work ("2026-11-08.2").
const maxVersion = 32

// ListSessions implements identityv1.AccountServiceServer.
func (s *AccountServer) ListSessions(ctx context.Context, _ *identityv1.ListSessionsRequest) (*identityv1.ListSessionsResponse, error) {
	t, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.sessions.List(ctx, session.Caller{User: t.user, Session: t.session})
	if err != nil {
		return nil, s.mapManageError(ctx, err)
	}
	out := &identityv1.ListSessionsResponse{Sessions: make([]*identityv1.Session, 0, len(list))}
	for _, i := range list {
		out.Sessions = append(out.Sessions, &identityv1.Session{
			SessionId:    i.ID.String(),
			Platform:     platformToPB[i.Platform],
			CreateTime:   timestamppb.New(i.CreatedAt),
			LastSeenTime: timestamppb.New(i.LastSeenAt),
			Current:      i.Current,
		})
	}
	return out, nil
}

// RevokeSession implements identityv1.AccountServiceServer.
func (s *AccountServer) RevokeSession(ctx context.Context, req *identityv1.RevokeSessionRequest) (*identityv1.RevokeSessionResponse, error) {
	t, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	target, ok := id.Parse(req.GetSessionId())
	if !ok {
		return nil, fail(codes.NotFound, identityv1.ErrorReason_ERROR_REASON_SESSION_NOT_FOUND)
	}
	if err := s.sessions.Revoke(ctx, session.Caller{User: t.user, Session: t.session}, target); err != nil {
		return nil, s.mapManageError(ctx, err)
	}
	return &identityv1.RevokeSessionResponse{}, nil
}

// RevokeOtherSessions implements identityv1.AccountServiceServer.
func (s *AccountServer) RevokeOtherSessions(ctx context.Context, _ *identityv1.RevokeOtherSessionsRequest) (*identityv1.RevokeOtherSessionsResponse, error) {
	t, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.sessions.RevokeOthers(ctx, session.Caller{User: t.user, Session: t.session})
	if err != nil {
		return nil, s.mapManageError(ctx, err)
	}
	return &identityv1.RevokeOtherSessionsResponse{RevokedCount: int32(min(n, 1<<30))}, nil //nolint:gosec // bounded
}

// GetConsents implements identityv1.AccountServiceServer.
func (s *AccountServer) GetConsents(ctx context.Context, _ *identityv1.GetConsentsRequest) (*identityv1.GetConsentsResponse, error) {
	u, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.consents.Status(ctx, u)
	if err != nil {
		return nil, s.mapManageError(ctx, err)
	}
	return &identityv1.GetConsentsResponse{
		PrivacyNoticeVersion: st.Documents.PrivacyNotice,
		TermsVersion:         st.Documents.Terms,
		ConsentRequired:      st.Required,
	}, nil
}

// AcceptConsents implements identityv1.AccountServiceServer.
func (s *AccountServer) AcceptConsents(ctx context.Context, req *identityv1.AcceptConsentsRequest) (*identityv1.AcceptConsentsResponse, error) {
	u, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetPrivacyNoticeVersion()) > maxVersion || len(req.GetTermsVersion()) > maxVersion {
		return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_UNSPECIFIED)
	}
	if err := s.consents.Accept(ctx, u, req.GetPrivacyNoticeVersion(), req.GetTermsVersion()); err != nil {
		return nil, s.mapManageError(ctx, err)
	}
	return &identityv1.AcceptConsentsResponse{}, nil
}

// DeleteAccount implements identityv1.AccountServiceServer. The user,
// session and issue time all come from the verified token.
func (s *AccountServer) DeleteAccount(ctx context.Context, _ *identityv1.DeleteAccountRequest) (*identityv1.DeleteAccountResponse, error) {
	t, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.deletion.Delete(ctx, deletion.Caller{User: t.user, Session: t.session, IssuedAt: t.issuedAt}); err != nil {
		return nil, s.mapManageError(ctx, err)
	}
	return &identityv1.DeleteAccountResponse{}, nil
}

func (s *AccountServer) mapManageError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, deletion.ErrReauthenticationRequired):
		return fail(codes.PermissionDenied, identityv1.ErrorReason_ERROR_REASON_REAUTHENTICATION_REQUIRED)
	case errors.Is(err, session.ErrSessionNotFound):
		return fail(codes.NotFound, identityv1.ErrorReason_ERROR_REASON_SESSION_NOT_FOUND)
	case errors.Is(err, consent.ErrOutdated):
		return fail(codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_CONSENT_OUTDATED)
	case errors.Is(err, session.ErrCallerSession), errors.Is(err, deletion.ErrUnknownUser), errors.Is(err, consent.ErrUnknownUser):
		return status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	default:
		s.log.ErrorContext(ctx, "hesap işlemi başarısız", slog.Any("err", err))
		return status.Error(codes.Internal, "iç hata")
	}
}
