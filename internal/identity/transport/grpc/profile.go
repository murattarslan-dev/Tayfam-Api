package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/profile"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Profiles is what ProfileServer needs from the app layer (profile.Service).
type Profiles interface {
	Get(ctx context.Context, viewer, user id.ID) (profile.Details, error)
}

// ProfileServer implements identityv1.ProfileServiceServer. Like
// AccountServer it accepts only full access tokens for apiAudience. The
// details it returns are never logged.
type ProfileServer struct {
	identityv1.UnimplementedProfileServiceServer
	app         Profiles
	apiAudience string
	log         *slog.Logger
}

// NewProfileServer returns a ProfileServer.
func NewProfileServer(app Profiles, apiAudience string, log *slog.Logger) *ProfileServer {
	return &ProfileServer{app: app, apiAudience: apiAudience, log: log}
}

// GetProfileDetails implements identityv1.ProfileServiceServer.
func (s *ProfileServer) GetProfileDetails(ctx context.Context, req *identityv1.GetProfileDetailsRequest) (*identityv1.GetProfileDetailsResponse, error) {
	c, ok := auth.CallerFrom(ctx)
	if !ok || s.apiAudience == "" || c.Audience != s.apiAudience {
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	viewer, ok := id.Parse(c.Subject)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	user, ok := id.Parse(req.GetUserId())
	if !ok {
		return nil, fail(codes.NotFound, identityv1.ErrorReason_ERROR_REASON_USER_NOT_FOUND)
	}
	d, err := s.app.Get(ctx, viewer, user)
	switch {
	case errors.Is(err, profile.ErrUserNotFound):
		return nil, fail(codes.NotFound, identityv1.ErrorReason_ERROR_REASON_USER_NOT_FOUND)
	case errors.Is(err, profile.ErrUnavailable):
		s.log.WarnContext(ctx, "profil: core'a sorulamadı", slog.String("err", err.Error()))
		return nil, status.Error(codes.Unavailable, "istek reddedildi")
	case err != nil:
		s.log.ErrorContext(ctx, "profil ayrıntıları okunamadı", slog.Any("err", err))
		return nil, status.Error(codes.Internal, "iç hata")
	}
	return profileDetailsToPB(d), nil
}

func profileDetailsToPB(d profile.Details) *identityv1.GetProfileDetailsResponse {
	out := &identityv1.GetProfileDetailsResponse{}
	if b := d.Birthday; b != nil {
		// A decrypted, validated date: month 1–12, day 1–31, year 1900–now.
		out.Birthday = &identityv1.Birthday{Month: int32(b.Month), Day: int32(b.Day), Year: int32(b.Year), Age: int32(b.Age)} //nolint:gosec // bounded calendar values
	}
	if a := d.About; a != nil {
		out.About = profileFieldsToPB(a.Interests, a.About, a.Occupation, a.Team, a.Languages, a.SocialAccounts)
	}
	return out
}
