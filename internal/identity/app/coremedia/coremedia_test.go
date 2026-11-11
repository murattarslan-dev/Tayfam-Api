package coremedia

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/account"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fake struct {
	err error
	got string
}

func (f *fake) AttachAvatar(_ context.Context, in *corev1.AttachAvatarRequest, _ ...grpc.CallOption) (*corev1.AttachAvatarResponse, error) {
	f.got = in.GetUploadId()
	return &corev1.AttachAvatarResponse{}, f.err
}

func withInfo(code codes.Code, domain, reason string) error {
	st, _ := status.New(code, "istek reddedildi").WithDetails(&errdetails.ErrorInfo{Domain: domain, Reason: reason})
	return st.Err()
}

func TestAttachAvatar(t *testing.T) {
	u := id.New()
	f := &fake{}
	if err := New(f).AttachAvatar(context.Background(), u); err != nil || f.got != u.String() {
		t.Fatalf("başarı: %v %s", err, f.got)
	}
	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"bulunamadı":     {withInfo(codes.NotFound, coreDomain, "ERROR_REASON_UPLOAD_NOT_FOUND"), account.ErrUploadNotFound},
		"hazır değil":    {withInfo(codes.FailedPrecondition, coreDomain, "ERROR_REASON_UPLOAD_NOT_READY"), account.ErrUploadNotReady},
		"kullanımda":     {withInfo(codes.FailedPrecondition, coreDomain, "ERROR_REASON_UPLOAD_IN_USE"), account.ErrUploadInUse},
		"başka alan adı": {withInfo(codes.NotFound, "chat.tayfam.app", "ERROR_REASON_UPLOAD_NOT_FOUND"), account.ErrMediaUnavailable},
		"kapalı":         {status.Error(codes.Unavailable, "x"), account.ErrMediaUnavailable},
		"durumsuz":       {errors.New("x"), account.ErrMediaUnavailable},
	} {
		f.err = tc.err
		if err := New(f).AttachAvatar(context.Background(), u); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
