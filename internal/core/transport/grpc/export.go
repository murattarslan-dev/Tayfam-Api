package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/export"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Exporter is what ExportServer needs from the app layer (export.Service).
type Exporter interface {
	Export(ctx context.Context, user id.ID, send func(export.Chunk) error) error
}

// ExportServer implements corev1.ExportServiceServer (issue #144). The user
// is always the verified caller. Records are streamed to the caller only
// and never logged.
type ExportServer struct {
	corev1.UnimplementedExportServiceServer
	app Exporter
	log *slog.Logger
}

// NewExportServer returns an ExportServer.
func NewExportServer(app Exporter, log *slog.Logger) *ExportServer {
	return &ExportServer{app: app, log: log}
}

var exportSections = map[string]corev1.CoreExportSection{
	export.SectionMemberships:     corev1.CoreExportSection_CORE_EXPORT_SECTION_MEMBERSHIPS,
	export.SectionEventRSVPs:      corev1.CoreExportSection_CORE_EXPORT_SECTION_EVENT_RSVPS,
	export.SectionHikeCheckins:    corev1.CoreExportSection_CORE_EXPORT_SECTION_HIKE_CHECKINS,
	export.SectionConvoyVehicles:  corev1.CoreExportSection_CORE_EXPORT_SECTION_CONVOY_VEHICLES,
	export.SectionActivityRecords: corev1.CoreExportSection_CORE_EXPORT_SECTION_ACTIVITY_RECORDS,
	export.SectionAnnouncements:   corev1.CoreExportSection_CORE_EXPORT_SECTION_ANNOUNCEMENTS,
	export.SectionEvents:          corev1.CoreExportSection_CORE_EXPORT_SECTION_EVENTS,
	export.SectionQuestions:       corev1.CoreExportSection_CORE_EXPORT_SECTION_QUESTIONS,
	export.SectionAnswers:         corev1.CoreExportSection_CORE_EXPORT_SECTION_ANSWERS,
	export.SectionAnswerVotes:     corev1.CoreExportSection_CORE_EXPORT_SECTION_ANSWER_VOTES,
	export.SectionPolls:           corev1.CoreExportSection_CORE_EXPORT_SECTION_POLLS,
	export.SectionPollVotes:       corev1.CoreExportSection_CORE_EXPORT_SECTION_POLL_VOTES,
	export.SectionFishingCatches:  corev1.CoreExportSection_CORE_EXPORT_SECTION_FISHING_CATCHES,
	export.SectionFriendships:     corev1.CoreExportSection_CORE_EXPORT_SECTION_FRIENDSHIPS,
	export.SectionBlocks:          corev1.CoreExportSection_CORE_EXPORT_SECTION_BLOCKS,
	export.SectionReports:         corev1.CoreExportSection_CORE_EXPORT_SECTION_REPORTS,
	export.SectionUploads:         corev1.CoreExportSection_CORE_EXPORT_SECTION_UPLOADS,
}

// ExportMyData implements corev1.ExportServiceServer.
func (s *ExportServer) ExportMyData(_ *corev1.ExportMyDataRequest, stream grpc.ServerStreamingServer[corev1.ExportMyDataResponse]) error {
	ctx := stream.Context()
	u, err := callerID(ctx)
	if err != nil {
		return err
	}
	err = s.app.Export(ctx, u, func(c export.Chunk) error {
		return stream.Send(&corev1.ExportMyDataResponse{Section: exportSections[c.Section], Records: c.Records})
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errorStatus(ctx, s.log, err)
	}
	return nil
}
