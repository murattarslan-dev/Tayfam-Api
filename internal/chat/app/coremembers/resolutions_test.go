package coremembers

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeResolutions struct {
	req  *corev1.GetRequestResolutionsRequest
	resp *corev1.GetRequestResolutionsResponse
	err  error
}

func (f *fakeResolutions) GetRequestResolutions(_ context.Context, in *corev1.GetRequestResolutionsRequest, _ ...grpc.CallOption) (*corev1.GetRequestResolutionsResponse, error) {
	f.req = in
	return f.resp, f.err
}

func TestResolve(t *testing.T) {
	a, tf := id.New(), id.New()
	f := &fakeResolutions{resp: &corev1.GetRequestResolutionsResponse{Resolutions: []corev1.RequestResolution{
		corev1.RequestResolution_REQUEST_RESOLUTION_WITHDRAWN, 99,
	}}}
	got, err := NewResolutions(f).Resolve(context.Background(), []notification.RequestRef{{ID: a}, {Invitation: true, ID: tf}})
	if err != nil || len(got) != 2 || got[0] != "withdrawn" || got[1] != "" ||
		f.req.GetRefs()[0].GetKind() != corev1.RequestKind_REQUEST_KIND_FRIEND_REQUEST || f.req.GetRefs()[0].GetId() != a.String() ||
		f.req.GetRefs()[1].GetKind() != corev1.RequestKind_REQUEST_KIND_TAYFA_INVITATION {
		t.Fatalf("çözüm = %v %v %v", got, err, f.req)
	}
	f.err = status.Error(codes.Unavailable, "x")
	if _, err := NewResolutions(f).Resolve(context.Background(), []notification.RequestRef{{ID: a}}); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("core yok: %v", err)
	}
}
