package chatdm

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeMediaChat struct {
	got     []*chatv1.FilterVisibleMessageMediaRequest
	visible []string
	err     error
}

func (f *fakeMediaChat) FilterVisibleMessageMedia(_ context.Context, in *chatv1.FilterVisibleMessageMediaRequest, _ ...grpc.CallOption) (*chatv1.FilterVisibleMessageMediaResponse, error) {
	f.got = append(f.got, in)
	return &chatv1.FilterVisibleMessageMediaResponse{VisibleUploadIds: f.visible}, f.err
}

func TestMessageMedia(t *testing.T) {
	chat := &fakeMediaChat{}
	m := NewMessageMedia(chat)
	photo, att, extra := id.New(), id.New(), id.New()
	items := []media.MessageMediaItem{{Upload: photo, Message: id.New()}, {Upload: att, Message: id.New(), Direct: true}}
	// chat may only narrow the answer: an upload that was not asked about
	// is ignored.
	chat.visible = []string{att.String(), extra.String(), "bozuk"}
	got, err := m.Visible(context.Background(), items)
	if err != nil || len(got) != 1 || !got[att] {
		t.Fatalf("görünen = %v %v", got, err)
	}
	req := chat.got[0].GetItems()
	if len(req) != 2 || req[0].GetKind() != chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_CHANNEL || req[1].GetKind() != chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_DIRECT ||
		req[0].GetMessageId() != items[0].Message.String() {
		t.Fatalf("istek = %v", req)
	}
	// More than chat's limit goes in batches.
	chat.got, chat.visible = nil, nil
	many := make([]media.MessageMediaItem, 150)
	for i := range many {
		many[i] = media.MessageMediaItem{Upload: id.New(), Message: id.New()}
	}
	if _, err := m.Visible(context.Background(), many); err != nil || len(chat.got) != 2 || len(chat.got[0].GetItems()) != 100 {
		t.Fatalf("parçalar: %d %v", len(chat.got), err)
	}
	chat.err = status.Error(codes.Unavailable, "x")
	if _, err := m.Visible(context.Background(), items); err == nil {
		t.Fatal("chat yokken hata bekleniyordu")
	}
}
