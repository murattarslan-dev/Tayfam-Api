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
// ADR-0031): which files of chat's messages the caller may see. The
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

// HoldersClient is the part of chat's MediaHolderService core uses.
type HoldersClient interface {
	FindMessageMediaHolders(ctx context.Context, in *chatv1.FindMessageMediaHoldersRequest, opts ...grpc.CallOption) (*chatv1.FindMessageMediaHoldersResponse, error)
}

// Holders implements media.MediaHolders with chat (ADR-0031). The
// connection carries core's service token for chat (ADR-0013): the
// reconciliation job runs without a user.
type Holders struct {
	chat HoldersClient
}

// NewHolders returns a Holders.
func NewHolders(chat HoldersClient) *Holders { return &Holders{chat: chat} }

// Holders implements media.MediaHolders: only uploads that were asked
// about are taken from the answer.
func (h *Holders) Holders(ctx context.Context, items []media.HolderItem) (map[id.ID]id.ID, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	out := make(map[id.ID]id.ID, len(items))
	for start := 0; start < len(items); start += maxMediaItems {
		batch := items[start:min(start+maxMediaItems, len(items))]
		req := &chatv1.FindMessageMediaHoldersRequest{Items: make([]*chatv1.MediaHolderQuery, len(batch))}
		asked := make(map[id.ID]bool, len(batch))
		for i, it := range batch {
			kind := chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_CHANNEL
			if it.Direct {
				kind = chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_DIRECT
			}
			req.Items[i] = &chatv1.MediaHolderQuery{UploadId: it.Upload.String(), Kind: kind}
			asked[it.Upload] = true
		}
		resp, err := h.chat.FindMessageMediaHolders(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("chat dosya sahibi: %s", status.Code(err))
		}
		for _, x := range resp.GetHolders() {
			u, ok1 := id.Parse(x.GetUploadId())
			m, ok2 := id.Parse(x.GetMessageId())
			if ok1 && ok2 && asked[u] {
				out[u] = m
			}
		}
	}
	return out, nil
}
