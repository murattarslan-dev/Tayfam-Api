package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/export"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Exporter is what ExportServer needs from the app layer (export.Service).
type Exporter interface {
	Export(ctx context.Context, user id.ID, send func(export.Chunk) error) error
}

// ExportServer implements chatv1.ExportServiceServer (issue #144). The user
// is always the verified caller. Records (message text included) go to the
// caller only and are never logged.
type ExportServer struct {
	chatv1.UnimplementedExportServiceServer
	app Exporter
	log *slog.Logger
}

// NewExportServer returns an ExportServer.
func NewExportServer(app Exporter, log *slog.Logger) *ExportServer {
	return &ExportServer{app: app, log: log}
}

var exportSections = map[string]chatv1.ChatExportSection{
	export.SectionChannelMessages:         chatv1.ChatExportSection_CHAT_EXPORT_SECTION_CHANNEL_MESSAGES,
	export.SectionNotificationPreferences: chatv1.ChatExportSection_CHAT_EXPORT_SECTION_NOTIFICATION_PREFERENCES,
}

// ExportMyData implements chatv1.ExportServiceServer.
func (s *ExportServer) ExportMyData(_ *chatv1.ExportMyDataRequest, stream grpc.ServerStreamingServer[chatv1.ExportMyDataResponse]) error {
	ctx := stream.Context()
	u, err := callerID(ctx)
	if err != nil {
		return err
	}
	err = s.app.Export(ctx, u, func(c export.Chunk) error {
		return stream.Send(&chatv1.ExportMyDataResponse{Section: exportSections[c.Section], Records: c.Records})
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errorStatus(ctx, s.log, err)
	}
	return nil
}
