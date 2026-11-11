package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/export"
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
	if err := send(export.Chunk{Section: export.SectionEvents, Records: [][]byte{[]byte(`{"id":"e"}`)}}); err != nil {
		return err
	}
	return send(export.Chunk{Section: export.SectionPollVotes, Records: [][]byte{[]byte(`{"poll_id":"p"}`)}})
}

type sendStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*corev1.ExportMyDataResponse
}

func (s *sendStream) Context() context.Context { return s.ctx }
func (s *sendStream) Send(m *corev1.ExportMyDataResponse) error {
	s.sent = append(s.sent, m)
	return nil
}
func (s *sendStream) SetHeader(metadata.MD) error  { return nil }
func (s *sendStream) SendHeader(metadata.MD) error { return nil }
func (s *sendStream) SetTrailer(metadata.MD)       {}

func TestExportServer(t *testing.T) {
	f := &fakeExporter{}
	srv := NewExportServer(f, discard)
	if err := srv.ExportMyData(&corev1.ExportMyDataRequest{}, &sendStream{ctx: context.Background()}); codeOf(t, err) != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	user := id.New()
	st := &sendStream{ctx: callerCtx(user)}
	if err := srv.ExportMyData(&corev1.ExportMyDataRequest{}, st); err != nil {
		t.Fatal(err)
	}
	if f.user != user || len(st.sent) != 2 ||
		st.sent[0].GetSection() != corev1.CoreExportSection_CORE_EXPORT_SECTION_EVENTS ||
		st.sent[1].GetSection() != corev1.CoreExportSection_CORE_EXPORT_SECTION_POLL_VOTES {
		t.Fatalf("akış: %v", st.sent)
	}
	f.err = errors.New("db")
	if err := srv.ExportMyData(&corev1.ExportMyDataRequest{}, &sendStream{ctx: callerCtx(user)}); codeOf(t, err) != codes.Internal {
		t.Fatalf("hata: %v", err)
	}
	// Every section has a proto value.
	for _, s := range export.Sections() {
		if exportSections[s] == corev1.CoreExportSection_CORE_EXPORT_SECTION_UNSPECIFIED {
			t.Errorf("%s bölümünün proto karşılığı yok", s)
		}
	}
}
