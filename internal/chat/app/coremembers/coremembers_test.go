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

type fakeCore struct {
	resp *corev1.GetMembershipResponse
	err  error
	got  *corev1.GetMembershipRequest
}

func (f *fakeCore) GetMembership(_ context.Context, in *corev1.GetMembershipRequest, _ ...grpc.CallOption) (*corev1.GetMembershipResponse, error) {
	f.got = in
	return f.resp, f.err
}

func withReason(code codes.Code, domain, reason string) error {
	st, _ := status.New(code, "x").WithDetails(&errdetails.ErrorInfo{Domain: domain, Reason: reason})
	return st.Err()
}

func member(st corev1.MembershipStatus) *corev1.GetMembershipResponse {
	return &corev1.GetMembershipResponse{Membership: &corev1.Membership{Status: st}}
}

func TestIsActiveMember(t *testing.T) {
	tf, u := id.New(), id.New()
	notFound := corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String()
	for _, tc := range []struct {
		name    string
		core    fakeCore
		want    bool
		wantErr error
		anyErr  bool
	}{
		{"etkin", fakeCore{resp: member(corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE)}, true, nil, false},
		{"bekleyen", fakeCore{resp: member(corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING)}, false, nil, false},
		{"banlı", fakeCore{resp: member(corev1.MembershipStatus_MEMBERSHIP_STATUS_BANNED)}, false, nil, false},
		{"üye değil", fakeCore{err: withReason(codes.NotFound, "core.tayfam.app", notFound)}, false, nil, false},
		{"başka alan adı", fakeCore{err: withReason(codes.NotFound, "chat.tayfam.app", notFound)}, false, nil, true},
		{"core kapalı", fakeCore{err: status.Error(codes.Unavailable, "x")}, false, apperr.ErrUnavailable, true},
		{"yetki", fakeCore{err: status.Error(codes.PermissionDenied, "x")}, false, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New(&tc.core).IsActiveMember(context.Background(), tf, u)
			if got != tc.want || (err != nil) != tc.anyErr || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("= %v %v", got, err)
			}
			if tc.core.got.GetTayfaId() != tf.String() || tc.core.got.GetUserId() != u.String() {
				t.Fatalf("istek = %v", tc.core.got)
			}
		})
	}
}
