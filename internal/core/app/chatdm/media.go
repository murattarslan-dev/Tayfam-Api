package chatdm

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// MediaClient is the part of chat's MessageMediaService core uses.
type MediaClient interface {
	FilterVisibleMessageMedia(ctx context.Context, in *chatv1.FilterVisibleMessageMediaRequest, opts ...grpc.CallOption) (*chatv1.FilterVisibleMessageMediaResponse, error)
}

// maxMediaItems is chat's limit for one question.
const maxMediaItems = 100

// MessageMedia implements media.MessageMedia with chat (issue #205,
// ADR-0028): which files of chat's messages the caller may see. The
// connection forwards the caller's own token, so chat answers about the
// caller only; the answer is upload IDs, never content.
type MessageMedia struct {
	chat MediaClient
}

// NewMessageMedia returns a MessageMedia. chat's connection must forward
// the user's token.
func NewMessageMedia(chat MediaClient) *MessageMedia { return &MessageMedia{chat: chat} }

// Visible implements media.MessageMedia.
func (m *MessageMedia) Visible(ctx context.Context, items []media.MessageMediaItem) (map[id.ID]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	out := make(map[id.ID]bool, len(items))
	for start := 0; start < len(items); start += maxMediaItems {
		batch := items[start:min(start+maxMediaItems, len(items))]
		req := &chatv1.FilterVisibleMessageMediaRequest{Items: make([]*chatv1.MessageMediaItem, len(batch))}
		for i, it := range batch {
			kind := chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_CHANNEL
			if it.Direct {
				kind = chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_DIRECT
			}
			req.Items[i] = &chatv1.MessageMediaItem{UploadId: it.Upload.String(), MessageId: it.Message.String(), Kind: kind}
		}
		resp, err := m.chat.FilterVisibleMessageMedia(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("chat dosya görünürlüğü: %s", status.Code(err))
		}
		asked := make(map[id.ID]bool, len(batch))
		for _, it := range batch {
			asked[it.Upload] = true
		}
		for _, s := range resp.GetVisibleUploadIds() {
			// Only what was asked: chat cannot widen the answer.
			if u, ok := id.Parse(s); ok && asked[u] {
				out[u] = true
			}
		}
	}
	return out, nil
}
