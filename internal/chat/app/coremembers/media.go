package coremembers

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// mediaTimeout bounds one attach call; sending the message waits for it.
const mediaTimeout = 5 * time.Second

// MediaClient is the part of core's MediaService chat uses.
type MediaClient interface {
	AttachChannelPhoto(ctx context.Context, in *corev1.AttachChannelPhotoRequest, opts ...grpc.CallOption) (*corev1.AttachChannelPhotoResponse, error)
	AttachDirectMessageAttachment(ctx context.Context, in *corev1.AttachDirectMessageAttachmentRequest, opts ...grpc.CallOption) (*corev1.AttachDirectMessageAttachmentResponse, error)
}

// Media attaches a message's photo or attachment in core (issue #205,
// ADR-0028) with the sender's own token: core checks that the upload is
// the sender's, READY, of the right purpose (and tayfa) and free.
type Media struct {
	core MediaClient
}

// NewMedia returns a Media. core's connection must forward the user's
// token.
func NewMedia(core MediaClient) *Media { return &Media{core: core} }

// AttachChannelPhoto implements channel.Photos.
func (m *Media) AttachChannelPhoto(ctx context.Context, upload, tayfa, message id.ID) error {
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()
	_, err := m.core.AttachChannelPhoto(ctx, &corev1.AttachChannelPhotoRequest{
		UploadId: upload.String(), TayfaId: tayfa.String(), MessageId: message.String(),
	})
	return mediaErr(err)
}

// AttachDirectMessageAttachment implements dm.Attachments.
func (m *Media) AttachDirectMessageAttachment(ctx context.Context, upload, message id.ID) error {
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()
	_, err := m.core.AttachDirectMessageAttachment(ctx, &corev1.AttachDirectMessageAttachmentRequest{
		UploadId: upload.String(), MessageId: message.String(),
	})
	return mediaErr(err)
}

// mediaErr maps core's answer about the upload; storage being off or core
// unreachable is apperr.ErrUnavailable.
func mediaErr(err error) error {
	if err == nil {
		return nil
	}
	st := status.Convert(err)
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return apperr.ErrUnavailable
	}
	for _, d := range st.Details() {
		info, ok := d.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != "core.tayfam.app" {
			continue
		}
		switch info.GetReason() {
		case corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_FOUND.String(), corev1.ErrorReason_ERROR_REASON_VALIDATION.String():
			return apperr.ErrUploadNotFound
		case corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_READY.String():
			return apperr.ErrUploadNotReady
		case corev1.ErrorReason_ERROR_REASON_UPLOAD_IN_USE.String():
			return apperr.ErrUploadInUse
		}
	}
	return fmt.Errorf("core dosya bağlama: %s", st.Code())
}
