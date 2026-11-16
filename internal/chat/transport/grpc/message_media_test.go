package grpc

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeVisible struct {
	caller   id.ID
	channels []channel.MediaItem
	direct   []dm.MediaItem
	yes      map[id.ID]bool
}

func (f *fakeVisible) VisiblePhotos(_ context.Context, caller id.ID, items []channel.MediaItem) (map[id.ID]bool, error) {
	f.caller, f.channels = caller, items
	return f.yes, nil
}

func (f *fakeVisible) VisibleAttachments(_ context.Context, caller id.ID, items []dm.MediaItem) (map[id.ID]bool, error) {
	f.caller, f.direct = caller, items
	return f.yes, nil
}

func TestFilterVisibleMessageMedia(t *testing.T) {
	user := id.New()
	ctx := auth.WithCallerForTest(context.Background(), auth.Claims{Subject: user.String()})
	photo, att, hidden := id.New(), id.New(), id.New()
	f := &fakeVisible{yes: map[id.ID]bool{att: true, photo: true}}
	s := NewMessageMediaServer(f, f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	item := func(u id.ID, k chatv1.MessageMediaKind) *chatv1.MessageMediaItem {
		return &chatv1.MessageMediaItem{UploadId: u.String(), MessageId: id.New().String(), Kind: k}
	}
	resp, err := s.FilterVisibleMessageMedia(ctx, &chatv1.FilterVisibleMessageMediaRequest{Items: []*chatv1.MessageMediaItem{
		item(att, chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_DIRECT),
		item(hidden, chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_CHANNEL),
		item(photo, chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_CHANNEL),
	}})
	if err != nil {
		t.Fatal(err)
	}
	// In request order, each once; the caller is the token's.
	got := resp.GetVisibleUploadIds()
	if len(got) != 2 || got[0] != att.String() || got[1] != photo.String() || f.caller != user {
		t.Fatalf("cevap = %v, çağıran %v", got, f.caller)
	}
	if len(f.channels) != 2 || len(f.direct) != 1 {
		t.Fatalf("ayrım: %d kanal, %d birebir", len(f.channels), len(f.direct))
	}
	many := make([]*chatv1.MessageMediaItem, 101)
	for i := range many {
		many[i] = item(id.New(), chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_CHANNEL)
	}
	for name, items := range map[string][]*chatv1.MessageMediaItem{
		"boş":       nil,
		"çok":       many,
		"tür yok":   {item(id.New(), chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_UNSPECIFIED)},
		"bozuk kim": {{UploadId: "x", MessageId: id.New().String(), Kind: chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_DIRECT}},
	} {
		if _, err := s.FilterVisibleMessageMedia(ctx, &chatv1.FilterVisibleMessageMediaRequest{Items: items}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without a verified caller nothing is answered.
	if _, err := s.FilterVisibleMessageMedia(context.Background(), &chatv1.FilterVisibleMessageMediaRequest{Items: many[:1]}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("çağıransız: %v", err)
	}
}

type fakeHolders struct {
	channel, direct map[id.ID]id.ID
	askedC, askedD  []id.ID
}

func (f *fakeHolders) PhotoHolders(_ context.Context, uploads []id.ID) (map[id.ID]id.ID, error) {
	f.askedC = uploads
	return f.channel, nil
}

func (f *fakeHolders) AttachmentHolders(_ context.Context, uploads []id.ID) (map[id.ID]id.ID, error) {
	f.askedD = uploads
	return f.direct, nil
}

func TestFindMessageMediaHolders(t *testing.T) {
	photo, att, msg1, msg2 := id.New(), id.New(), id.New(), id.New()
	f := &fakeHolders{channel: map[id.ID]id.ID{photo: msg1}, direct: map[id.ID]id.ID{att: msg2}}
	s := NewMediaHolderServer(f, f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := auth.WithServiceCallerForTest(context.Background(), auth.ServiceClaims{})
	resp, err := s.FindMessageMediaHolders(ctx, &chatv1.FindMessageMediaHoldersRequest{Items: []*chatv1.MediaHolderQuery{
		{UploadId: photo.String(), Kind: chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_CHANNEL},
		{UploadId: att.String(), Kind: chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_DIRECT},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, h := range resp.GetHolders() {
		got[h.GetUploadId()] = h.GetMessageId()
	}
	if len(got) != 2 || got[photo.String()] != msg1.String() || got[att.String()] != msg2.String() || len(f.askedC) != 1 || len(f.askedD) != 1 {
		t.Fatalf("cevap = %v", got)
	}
	if _, err := s.FindMessageMediaHolders(ctx, &chatv1.FindMessageMediaHoldersRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("boş: %v", err)
	}
	// A user token never reaches it (the Guard refuses first; this is the
	// handler's own check).
	user := auth.WithCallerForTest(context.Background(), auth.Claims{Subject: id.New().String()})
	if _, err := s.FindMessageMediaHolders(user, &chatv1.FindMessageMediaHoldersRequest{Items: []*chatv1.MediaHolderQuery{
		{UploadId: photo.String(), Kind: chatv1.MessageMediaKind_MESSAGE_MEDIA_KIND_CHANNEL}}}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("kullanıcı token'ı: %v", err)
	}
}
