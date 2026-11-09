package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/profile"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Profiles is what ProfileServer needs from the app layer (profile.Service).
type Profiles interface {
	GetProfile(ctx context.Context, caller, subject id.ID) (profile.Profile, error)
	ListActivity(ctx context.Context, caller, subject id.ID, size int32, token string) (profile.Page[profile.Activity], error)
}

// ProfileServer implements corev1.ProfileServiceServer. The viewer always
// comes from the verified token.
type ProfileServer struct {
	corev1.UnimplementedProfileServiceServer
	app Profiles
	log *slog.Logger
}

// NewProfileServer returns a ProfileServer.
func NewProfileServer(app Profiles, log *slog.Logger) *ProfileServer {
	return &ProfileServer{app: app, log: log}
}

// GetUserProfile implements corev1.ProfileServiceServer.
func (s *ProfileServer) GetUserProfile(ctx context.Context, req *corev1.GetUserProfileRequest) (*corev1.GetUserProfileResponse, error) {
	u, subject, err := userArgs(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	p, err := s.app.GetProfile(ctx, u, subject)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.UserProfile{
		UserId: p.UserID.String(), DisplayName: p.DisplayName, AvatarUploadId: optionalID(p.AvatarUploadID),
		Self: p.Self, FriendState: friendStates[p.Friend], ActivityVisible: p.ActivityVisible,
		ActivityVisibility: visibilityToProto(p.ActivityVisibility),
		MutualTayfas:       make([]*corev1.MutualTayfa, 0, len(p.MutualTayfas)),
	}
	for _, t := range p.MutualTayfas {
		out.MutualTayfas = append(out.MutualTayfas, &corev1.MutualTayfa{TayfaId: t.ID.String(), Name: t.Name})
	}
	return &corev1.GetUserProfileResponse{Profile: out}, nil
}

// ListUserActivity implements corev1.ProfileServiceServer.
func (s *ProfileServer) ListUserActivity(ctx context.Context, req *corev1.ListUserActivityRequest) (*corev1.ListUserActivityResponse, error) {
	u, subject, err := userArgs(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	p, err := s.app.ListActivity(ctx, u, subject, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListUserActivityResponse{Records: make([]*corev1.ActivityRecord, 0, len(p.Items)), NextPageToken: p.Next}
	for _, a := range p.Items {
		r := &corev1.ActivityRecord{
			EventType: interestToProto(a.EventType), City: a.City, District: a.District,
			StartTime: timestamppb.New(a.StartedAt), TayfaMember: a.Member,
		}
		switch a.Mode {
		case event.ModeInPerson:
			r.Mode = corev1.EventMode_EVENT_MODE_IN_PERSON
		case event.ModeOnline:
			r.Mode = corev1.EventMode_EVENT_MODE_ONLINE
		}
		if a.Member {
			r.TayfaId, r.TayfaName = a.TayfaID.String(), a.TayfaName
			r.EventId, r.EventTitle, r.DurationMinutes = optionalID(a.EventID), a.EventTitle, a.DurationMinutes
		}
		out.Records = append(out.Records, r)
	}
	return out, nil
}

func visibilityToProto(v profile.Visibility) corev1.ActivityVisibility {
	switch v {
	case profile.VisibilityPrivate:
		return corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE
	case profile.VisibilityTayfa:
		return corev1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA
	case profile.VisibilityPublic:
		return corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC
	}
	return corev1.ActivityVisibility_ACTIVITY_VISIBILITY_UNSPECIFIED
}
