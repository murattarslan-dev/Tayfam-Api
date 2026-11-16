package grpc

import (
	"context"
	"log/slog"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// maxMediaItems is the most items one FilterVisibleMessageMedia may ask
// about.
const maxMediaItems = 100

// ChannelPhotos and DirectAttachments answer who may see a message's file
// (issue #205, ADR-0031).
type ChannelPhotos interface {
	VisiblePhotos(ctx context.Context, caller id.ID, items []channel.MediaItem) (map[id.ID]bool, error)
}

// DirectAttachments answers for direct messages.
type DirectAttachments interface {
	VisibleAttachments(ctx context.Context, caller id.ID, items []dm.MediaItem) (map[id.ID]bool, error)
}

// MessageMediaServer implements chatv1.MessageMediaServiceServer. core calls
// it with the user's own token before it signs a link; the caller always
// comes from the verified token.
type MessageMediaServer struct {
	chatv1.UnimplementedMessageMediaServiceServer
	channels ChannelPhotos
	direct   DirectAttachments
	log      *slog.Logger
}

// NewMessageMediaServer returns a MessageMediaServer.
func NewMessageMediaServer(channels ChannelPhotos, direct DirectAttachments, log *slog.Logger) *MessageMediaServer {
	return &MessageMediaServer{channels: channels, direct: direct, log: log}
}

// FilterVisibleMessageMedia implements chatv1.MessageMediaServiceServer.
func (s *MessageMediaServer) FilterVisibleMessageMedia(ctx context.Context, req *chatv1.FilterVisibleMessageMediaRequest) (*chatv1.FilterVisibleMessageMediaResponse, error) {
	caller, _, err := ids(ctx)
	if err != nil {
		return nil, err
	}
	items := req.GetItems()
	if len(items) == 0 || len(items) > maxMediaItems {
		return nil, validation("items")
	}
	order := make([]id.ID, 0, len(items))
	var (
		chans  []channel.MediaItem
		direct []dm.MediaItem
	)
	for _, it := range items {
		up, ok1 := id.Parse(it.GetUploadId())
		msg, ok2 := id.Parse(it.GetMessageId())
		if !ok1 || !ok2 {
			return nil, validation("items")
		}
		switch it.GetKind() {
		case chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_CHANNEL:
			chans = append(chans, channel.MediaItem{Upload: up, Message: msg})
		case chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_DIRECT:
			direct = append(direct, dm.MediaItem{Upload: up, Message: msg})
		default:
			return nil, validation("items")
		}
		order = append(order, up)
	}
	visible := map[id.ID]bool{}
	if len(chans) > 0 {
		v, err := s.channels.VisiblePhotos(ctx, caller, chans)
		if err != nil {
			return nil, errorStatus(ctx, s.log, err)
		}
		for u := range v {
			visible[u] = true
		}
	}
	if len(direct) > 0 {
		v, err := s.direct.VisibleAttachments(ctx, caller, direct)
		if err != nil {
			return nil, errorStatus(ctx, s.log, err)
		}
		for u := range v {
			visible[u] = true
		}
	}
	out := &chatv1.FilterVisibleMessageMediaResponse{}
	for _, u := range order {
		if visible[u] {
			out.VisibleUploadIds = append(out.VisibleUploadIds, u.String())
			delete(visible, u) // each once
		}
	}
	return out, nil
}
