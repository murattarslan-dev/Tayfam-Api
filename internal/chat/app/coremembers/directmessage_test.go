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
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeDMCore struct {
	got  *corev1.CheckDirectMessageRequest
	resp *corev1.CheckDirectMessageResponse
	err  error
}

func (f *fakeDMCore) CheckDirectMessage(_ context.Context, in *corev1.CheckDirectMessageRequest, _ ...grpc.CallOption) (*corev1.CheckDirectMessageResponse, error) {
	f.got = in
	return f.resp, f.err
}

func TestDirectMessageRules(t *testing.T) {
	core := &fakeDMCore{}
	r := NewDirectMessageRules(core)
	recipient := id.New()
	ctx := context.Background()
	for _, c := range []struct {
		resp *corev1.CheckDirectMessageResponse
		want dm.Verdict
	}{
		{&corev1.CheckDirectMessageResponse{Decision: corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_ACTIVE}, dm.Verdict{Decision: dm.Active}},
		{&corev1.CheckDirectMessageResponse{Decision: corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_REQUEST, DailyRequestLimit: 5}, dm.Verdict{Decision: dm.Request, RequestLimit: 5}},
		{&corev1.CheckDirectMessageResponse{Decision: corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_DENIED, DailyRequestLimit: 20}, dm.Verdict{Decision: dm.Denied}},
		// Unknown answers refuse.
		{&corev1.CheckDirectMessageResponse{}, dm.Verdict{Decision: dm.Denied}},
		{&corev1.CheckDirectMessageResponse{Decision: 99}, dm.Verdict{Decision: dm.Denied}},
	} {
		core.resp, core.err = c.resp, nil
		v, err := r.Check(ctx, id.New(), recipient)
		if err != nil || v != c.want {
			t.Errorf("%v: %+v %v", c.resp, v, err)
		}
		if core.got.GetRecipientId() != recipient.String() {
			t.Fatalf("istek = %v", core.got)
		}
	}
	for code, want := range map[codes.Code]error{
		codes.Unavailable:      apperr.ErrUnavailable,
		codes.DeadlineExceeded: apperr.ErrUnavailable,
	} {
		core.err = status.Error(code, "x")
		if _, err := r.Check(ctx, id.New(), recipient); !errors.Is(err, want) {
			t.Errorf("%s: %v", code, err)
		}
	}
	core.err = status.Error(codes.InvalidArgument, "x")
	var v *apperr.ValidationError
	if _, err := r.Check(ctx, id.New(), recipient); !errors.As(err, &v) || v.Field != "recipient_user_id" {
		t.Errorf("geçersiz: %v", err)
	}
	core.err = status.Error(codes.Internal, "x")
	if _, err := r.Check(ctx, id.New(), recipient); err == nil {
		t.Error("iç hata yutuldu")
	}
}
