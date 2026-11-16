package coremembers

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeMediaCore struct {
	photo *corev1.AttachChannelPhotoRequest
	dm    *corev1.AttachDirectMessageAttachmentRequest
	err   error
}

func (f *fakeMediaCore) AttachChannelPhoto(_ context.Context, in *corev1.AttachChannelPhotoRequest, _ ...grpc.CallOption) (*corev1.AttachChannelPhotoResponse, error) {
	f.photo = in
	return &corev1.AttachChannelPhotoResponse{}, f.err
}

func (f *fakeMediaCore) AttachDirectMessageAttachment(_ context.Context, in *corev1.AttachDirectMessageAttachmentRequest, _ ...grpc.CallOption) (*corev1.AttachDirectMessageAttachmentResponse, error) {
	f.dm = in
	return &corev1.AttachDirectMessageAttachmentResponse{}, f.err
}

func coreReason(code codes.Code, reason corev1.ErrorReason) error {
	st, _ := status.New(code, "x").WithDetails(&errdetails.ErrorInfo{Domain: "core.tayfam.app", Reason: reason.String()})
	return st.Err()
}

func TestMediaAttach(t *testing.T) {
	core := &fakeMediaCore{}
	m := NewMedia(core)
	ctx := context.Background()
	up, tayfa, msg := id.New(), id.New(), id.New()
	if err := m.AttachChannelPhoto(ctx, up, tayfa, msg); err != nil {
		t.Fatal(err)
	}
	if core.photo.GetUploadId() != up.String() || core.photo.GetTayfaId() != tayfa.String() || core.photo.GetMessageId() != msg.String() {
		t.Fatalf("istek = %v", core.photo)
	}
	if err := m.AttachDirectMessageAttachment(ctx, up, msg); err != nil || core.dm.GetMessageId() != msg.String() {
		t.Fatalf("birebir: %v %v", core.dm, err)
	}
	for _, tc := range []struct {
		err  error
		want error
	}{
		{coreReason(codes.NotFound, corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_FOUND), apperr.ErrUploadNotFound},
		{coreReason(codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_READY), apperr.ErrUploadNotReady},
		{coreReason(codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_UPLOAD_IN_USE), apperr.ErrUploadInUse},
		{coreReason(codes.InvalidArgument, corev1.ErrorReason_ERROR_REASON_VALIDATION), apperr.ErrUploadNotFound},
		{status.Error(codes.Unavailable, "x"), apperr.ErrUnavailable},
	} {
		core.err = tc.err
		if err := m.AttachChannelPhoto(ctx, up, tayfa, msg); !errors.Is(err, tc.want) {
			t.Errorf("%v: %v", tc.err, err)
		}
	}
	// Another service's reason is not trusted.
	st, _ := status.New(codes.NotFound, "x").WithDetails(&errdetails.ErrorInfo{Domain: "chat.tayfam.app", Reason: "ERROR_REASON_UPLOAD_IN_USE"})
	core.err = st.Err()
	if err := m.AttachChannelPhoto(ctx, up, tayfa, msg); err == nil || errors.Is(err, apperr.ErrUploadInUse) {
		t.Fatalf("başka alan adı: %v", err)
	}
}
