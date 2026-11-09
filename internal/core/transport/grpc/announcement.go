package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/announcement"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Announcements is what AnnouncementServer needs from the app layer.
type Announcements interface {
	Create(ctx context.Context, user, tayfa id.ID, in announcement.CreateInput) (announcement.View, error)
	Get(ctx context.Context, user, tayfa, a id.ID) (announcement.View, error)
	List(ctx context.Context, user, tayfa id.ID, pageSize int32, pageToken string) ([]announcement.View, string, error)
	Update(ctx context.Context, user, tayfa, a id.ID, in announcement.UpdateInput) (announcement.View, error)
	SetPinned(ctx context.Context, user, tayfa, a id.ID, pinned bool) (announcement.View, error)
	Delete(ctx context.Context, user, tayfa, a id.ID) error
	Restore(ctx context.Context, user, tayfa, a id.ID) (announcement.View, error)
}

// AnnouncementServer implements corev1.AnnouncementServiceServer. The acting
// user always comes from the verified token (auth.Guard).
type AnnouncementServer struct {
	corev1.UnimplementedAnnouncementServiceServer
	app Announcements
	log *slog.Logger
}

// NewAnnouncementServer returns an AnnouncementServer.
func NewAnnouncementServer(app Announcements, log *slog.Logger) *AnnouncementServer {
	return &AnnouncementServer{app: app, log: log}
}

// announcementArgs reads the caller and the tayfa_id and announcement_id
// fields.
func announcementArgs(ctx context.Context, rawTayfa, rawAnnouncement string) (u, tayfa, a id.ID, err error) {
	if u, err = callerID(ctx); err != nil {
		return
	}
	if tayfa, err = tayfaID(rawTayfa); err != nil {
		return
	}
	a, err = parseID(rawAnnouncement, "announcement_id")
	return
}

// CreateAnnouncement implements corev1.AnnouncementServiceServer.
func (s *AnnouncementServer) CreateAnnouncement(ctx context.Context, req *corev1.CreateAnnouncementRequest) (*corev1.CreateAnnouncementResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Create(ctx, user, t, announcement.CreateInput{Title: req.GetTitle(), Body: req.GetBody(), ClientRequestID: req.GetClientRequestId()})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateAnnouncementResponse{Announcement: announcementPB(v)}, nil
}

// GetAnnouncement implements corev1.AnnouncementServiceServer.
func (s *AnnouncementServer) GetAnnouncement(ctx context.Context, req *corev1.GetAnnouncementRequest) (*corev1.GetAnnouncementResponse, error) {
	user, t, a, err := announcementArgs(ctx, req.GetTayfaId(), req.GetAnnouncementId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Get(ctx, user, t, a)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetAnnouncementResponse{Announcement: announcementPB(v)}, nil
}

// ListAnnouncements implements corev1.AnnouncementServiceServer.
func (s *AnnouncementServer) ListAnnouncements(ctx context.Context, req *corev1.ListAnnouncementsRequest) (*corev1.ListAnnouncementsResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.List(ctx, user, t, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	resp := &corev1.ListAnnouncementsResponse{NextPageToken: next, Announcements: make([]*corev1.Announcement, 0, len(list))}
	for _, v := range list {
		resp.Announcements = append(resp.Announcements, announcementPB(v))
	}
	return resp, nil
}

// UpdateAnnouncement implements corev1.AnnouncementServiceServer.
func (s *AnnouncementServer) UpdateAnnouncement(ctx context.Context, req *corev1.UpdateAnnouncementRequest) (*corev1.UpdateAnnouncementResponse, error) {
	user, t, a, err := announcementArgs(ctx, req.GetTayfaId(), req.GetAnnouncementId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Update(ctx, user, t, a, announcement.UpdateInput{
		Fields: req.GetUpdateMask().GetPaths(), Title: req.GetTitle(), Body: req.GetBody(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateAnnouncementResponse{Announcement: announcementPB(v)}, nil
}

// SetAnnouncementPinned implements corev1.AnnouncementServiceServer.
func (s *AnnouncementServer) SetAnnouncementPinned(ctx context.Context, req *corev1.SetAnnouncementPinnedRequest) (*corev1.SetAnnouncementPinnedResponse, error) {
	user, t, a, err := announcementArgs(ctx, req.GetTayfaId(), req.GetAnnouncementId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.SetPinned(ctx, user, t, a, req.GetPinned())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetAnnouncementPinnedResponse{Announcement: announcementPB(v)}, nil
}

// DeleteAnnouncement implements corev1.AnnouncementServiceServer.
func (s *AnnouncementServer) DeleteAnnouncement(ctx context.Context, req *corev1.DeleteAnnouncementRequest) (*corev1.DeleteAnnouncementResponse, error) {
	user, t, a, err := announcementArgs(ctx, req.GetTayfaId(), req.GetAnnouncementId())
	if err != nil {
		return nil, err
	}
	if err := s.app.Delete(ctx, user, t, a); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteAnnouncementResponse{}, nil
}

// RestoreAnnouncement implements corev1.AnnouncementServiceServer.
func (s *AnnouncementServer) RestoreAnnouncement(ctx context.Context, req *corev1.RestoreAnnouncementRequest) (*corev1.RestoreAnnouncementResponse, error) {
	user, t, a, err := announcementArgs(ctx, req.GetTayfaId(), req.GetAnnouncementId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Restore(ctx, user, t, a)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RestoreAnnouncementResponse{Announcement: announcementPB(v)}, nil
}

func announcementPB(v announcement.View) *corev1.Announcement {
	return &corev1.Announcement{
		Id: v.ID.String(), TayfaId: v.TayfaID.String(), AuthorUserId: optionalID(v.AuthorID),
		Title: v.Title, Body: v.Body, Pinned: v.Pinned,
		CreateTime: timestamppb.New(v.CreatedAt), UpdateTime: timestamppb.New(v.UpdatedAt),
	}
}
