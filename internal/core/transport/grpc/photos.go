package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Event album (issue #206, ADR-0028).

// AddEventPhoto implements corev1.EventServiceServer.
func (s *EventServer) AddEventPhoto(ctx context.Context, req *corev1.AddEventPhotoRequest) (*corev1.AddEventPhotoResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	upload, ok := id.Parse(req.GetUploadId())
	if !ok {
		return nil, validation("upload_id")
	}
	p, err := s.app.AddPhoto(ctx, user, t, e, event.PhotoInput{
		UploadID: upload, Caption: req.GetCaption(), ClientRequestID: req.GetClientRequestId(),
		ConsentAck: req.GetConsentAck(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddEventPhotoResponse{Photo: photoToProto(p)}, nil
}

// ListEventPhotos implements corev1.EventServiceServer.
func (s *EventServer) ListEventPhotos(ctx context.Context, req *corev1.ListEventPhotosRequest) (*corev1.ListEventPhotosResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.ListPhotos(ctx, user, t, e, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	resp := &corev1.ListEventPhotosResponse{NextPageToken: next, Photos: make([]*corev1.EventPhoto, 0, len(list))}
	for _, p := range list {
		resp.Photos = append(resp.Photos, photoToProto(p))
	}
	return resp, nil
}

// RemoveEventPhoto implements corev1.EventServiceServer.
func (s *EventServer) RemoveEventPhoto(ctx context.Context, req *corev1.RemoveEventPhotoRequest) (*corev1.RemoveEventPhotoResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	p, ok := id.Parse(req.GetPhotoId())
	if !ok {
		return nil, validation("photo_id")
	}
	if err := s.app.RemovePhoto(ctx, user, t, e, p); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveEventPhotoResponse{}, nil
}

func photoToProto(p event.Photo) *corev1.EventPhoto {
	return &corev1.EventPhoto{
		Id: p.ID.String(), EventId: p.EventID.String(), UploaderId: p.UploaderID.String(),
		UploadId: p.UploadID.String(), Caption: p.Caption, CreateTime: timestamppb.New(p.CreatedAt),
	}
}
