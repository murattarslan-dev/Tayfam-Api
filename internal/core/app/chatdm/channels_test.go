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

type fakeChannelChat struct {
	got    *chatv1.VerifyChannelMessageForReportRequest
	resp   *chatv1.VerifyChannelMessageForReportResponse
	status *chatv1.GetChannelMessageStatusRequest
	err    error
}

func (f *fakeChannelChat) VerifyChannelMessageForReport(_ context.Context, in *chatv1.VerifyChannelMessageForReportRequest, _ ...grpc.CallOption) (*chatv1.VerifyChannelMessageForReportResponse, error) {
	f.got = in
	return f.resp, f.err
}

func (f *fakeChannelChat) GetChannelMessageStatus(_ context.Context, in *chatv1.GetChannelMessageStatusRequest, _ ...grpc.CallOption) (*chatv1.GetChannelMessageStatusResponse, error) {
	f.status = in
	return &chatv1.GetChannelMessageStatusResponse{Removed: true}, f.err
}

// Channel message reports (issue #205): chat's answer becomes the report's
// message; a refusal is not found without a reason, an outage unavailable.
func TestChannelMessages(t *testing.T) {
	tf, msg, ch, author := id.New(), id.New(), id.New(), id.New()
	f := &fakeChannelChat{resp: &chatv1.VerifyChannelMessageForReportResponse{
		ChannelId: ch.String(), AuthorUserId: author.String(), Kind: chatv1.MessageKind_MESSAGE_KIND_PLACE,
		Place: &chatv1.Place{Latitude: 38.4, Longitude: 27.1, Name: "İskele"},
	}}
	c := NewChannelMessages(f)
	m, err := c.ReportTarget(context.Background(), tf, msg)
	if err != nil || m.Channel != ch || m.Author != author || m.Kind != "place" || m.Place == nil || m.Place.Name != "İskele" ||
		f.got.GetTayfaId() != tf.String() || f.got.GetMessageId() != msg.String() {
		t.Fatalf("mesaj = %+v %v", m, err)
	}
	f.resp = &chatv1.VerifyChannelMessageForReportResponse{ChannelId: ch.String(), AuthorUserId: author.String()}
	if _, err := c.ReportTarget(context.Background(), tf, msg); !errors.Is(err, apperr.ErrReportTargetNotFound) {
		t.Fatalf("türsüz: %v", err)
	}
	for code, want := range map[codes.Code]error{
		codes.NotFound: apperr.ErrReportTargetNotFound, codes.InvalidArgument: apperr.ErrReportTargetNotFound,
		codes.Unavailable: apperr.ErrUnavailable, codes.DeadlineExceeded: apperr.ErrUnavailable,
	} {
		f.err = status.Error(code, "x")
		if _, err := c.ReportTarget(context.Background(), tf, msg); !errors.Is(err, want) {
			t.Errorf("%s: %v", code, err)
		}
	}

	f.err = nil
	s := NewChannelStatus(f)
	if removed, err := s.Removed(context.Background(), tf, msg); err != nil || !removed || f.status.GetMessageId() != msg.String() {
		t.Fatalf("durum = %v %v", removed, err)
	}
	f.err = status.Error(codes.Unavailable, "x")
	if _, err := s.Removed(context.Background(), tf, msg); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("kapalı: %v", err)
	}
}
