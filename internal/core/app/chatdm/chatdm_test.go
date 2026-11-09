package chatdm

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeChat struct {
	got  *chatv1.VerifyDirectMessageForReportRequest
	resp *chatv1.VerifyDirectMessageForReportResponse
	err  error
}

func (f *fakeChat) VerifyDirectMessageForReport(_ context.Context, in *chatv1.VerifyDirectMessageForReportRequest, _ ...grpc.CallOption) (*chatv1.VerifyDirectMessageForReportResponse, error) {
	f.got = in
	return f.resp, f.err
}

func TestSender(t *testing.T) {
	sender, msg := id.New(), id.New()
	f := &fakeChat{resp: &chatv1.VerifyDirectMessageForReportResponse{ConversationId: id.New().String(), SenderUserId: sender.String()}}
	v := New(f)
	got, err := v.Sender(context.Background(), msg)
	if err != nil || got != sender || f.got.GetMessageId() != msg.String() {
		t.Fatalf("gönderen = %v %v", got, err)
	}
	f.resp = &chatv1.VerifyDirectMessageForReportResponse{}
	if _, err := v.Sender(context.Background(), msg); !errors.Is(err, apperr.ErrReportTargetNotFound) {
		t.Fatalf("boş gönderen: %v", err)
	}
	for code, want := range map[codes.Code]error{
		codes.NotFound: apperr.ErrReportTargetNotFound, codes.InvalidArgument: apperr.ErrReportTargetNotFound,
		codes.Unavailable: apperr.ErrUnavailable, codes.DeadlineExceeded: apperr.ErrUnavailable,
	} {
		f.err = status.Error(code, "x")
		if _, err := v.Sender(context.Background(), msg); !errors.Is(err, want) {
			t.Errorf("%s: %v", code, err)
		}
	}
	f.err = status.Error(codes.Internal, "x")
	if _, err := v.Sender(context.Background(), msg); err == nil || errors.Is(err, apperr.ErrReportTargetNotFound) {
		t.Errorf("iç hata: %v", err)
	}
}
