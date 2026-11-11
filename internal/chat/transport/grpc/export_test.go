package grpc

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/export"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeExporter struct {
	user id.ID
	err  error
}

func (f *fakeExporter) Export(_ context.Context, u id.ID, send func(export.Chunk) error) error {
	f.user = u
	if f.err != nil {
		return f.err
	}
	return send(export.Chunk{Section: export.SectionChannelMessages, Records: [][]byte{[]byte(`{"id":"m"}`)}})
}

type exportStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*chatv1.ExportMyDataResponse
}

func (s *exportStream) Context() context.Context { return s.ctx }
func (s *exportStream) Send(m *chatv1.ExportMyDataResponse) error {
	s.sent = append(s.sent, m)
	return nil
}
func (s *exportStream) SetHeader(metadata.MD) error  { return nil }
func (s *exportStream) SendHeader(metadata.MD) error { return nil }
func (s *exportStream) SetTrailer(metadata.MD)       {}

func TestExportServer(t *testing.T) {
	f := &fakeExporter{}
	srv := NewExportServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := srv.ExportMyData(&chatv1.ExportMyDataRequest{}, &exportStream{ctx: context.Background()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	user := id.New()
	ctx := auth.WithCallerForTest(context.Background(), auth.Claims{Subject: user.String()})
	st := &exportStream{ctx: ctx}
	if err := srv.ExportMyData(&chatv1.ExportMyDataRequest{}, st); err != nil {
		t.Fatal(err)
	}
	if f.user != user || len(st.sent) != 1 || st.sent[0].GetSection() != chatv1.ChatExportSection_CHAT_EXPORT_SECTION_CHANNEL_MESSAGES {
		t.Fatalf("akış: %v", st.sent)
	}
	f.err = apperr.ErrChannelsUnavailable
	if err := srv.ExportMyData(&chatv1.ExportMyDataRequest{}, &exportStream{ctx: ctx}); status.Code(err) != codes.Unavailable {
		t.Fatalf("anahtar yokken: %v", err)
	}
}
