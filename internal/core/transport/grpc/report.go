package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/report"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Reports is what ReportServer needs from the app layer.
type Reports interface {
	Create(ctx context.Context, user, tayfa id.ID, in report.CreateInput) (report.MyView, bool, error)
	GetMine(ctx context.Context, user, r id.ID) (report.MyView, error)
	ListMine(ctx context.Context, user id.ID, pageSize int32, pageToken string) ([]report.MyView, string, error)
	ListQueue(ctx context.Context, user, tayfa id.ID, status report.Status, pageSize int32, pageToken string) ([]report.ModView, string, error)
	Resolve(ctx context.Context, user, tayfa, r id.ID, res report.Resolution) (report.ModView, error)
}

// ReportServer implements corev1.ReportServiceServer. The acting user
// always comes from the verified token (auth.Guard); no request names the
// reporter.
type ReportServer struct {
	corev1.UnimplementedReportServiceServer
	app Reports
	log *slog.Logger
}

// NewReportServer returns a ReportServer.
func NewReportServer(app Reports, log *slog.Logger) *ReportServer {
	return &ReportServer{app: app, log: log}
}

// CreateReport implements corev1.ReportServiceServer.
func (s *ReportServer) CreateReport(ctx context.Context, req *corev1.CreateReportRequest) (*corev1.CreateReportResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	tt, ok := reportTargets[req.GetTargetType()]
	if !ok {
		return nil, validation("target_type")
	}
	reason, ok := reportReasons[req.GetReason()]
	if !ok {
		return nil, validation("reason")
	}
	v, created, err := s.app.Create(ctx, user, t, report.CreateInput{
		TargetType: tt, TargetID: req.GetTargetId(), Reason: reason, Description: req.GetDescription(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateReportResponse{Report: myReportPB(v), Created: created}, nil
}

// GetMyReport implements corev1.ReportServiceServer.
func (s *ReportServer) GetMyReport(ctx context.Context, req *corev1.GetMyReportRequest) (*corev1.GetMyReportResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	r, err := parseID(req.GetReportId(), "report_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.GetMine(ctx, user, r)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetMyReportResponse{Report: myReportPB(v)}, nil
}

// ListMyReports implements corev1.ReportServiceServer.
func (s *ReportServer) ListMyReports(ctx context.Context, req *corev1.ListMyReportsRequest) (*corev1.ListMyReportsResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	vs, next, err := s.app.ListMine(ctx, user, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListMyReportsResponse{NextPageToken: next, Reports: make([]*corev1.MyReport, 0, len(vs))}
	for _, v := range vs {
		out.Reports = append(out.Reports, myReportPB(v))
	}
	return out, nil
}

// ListTayfaReports implements corev1.ReportServiceServer.
func (s *ReportServer) ListTayfaReports(ctx context.Context, req *corev1.ListTayfaReportsRequest) (*corev1.ListTayfaReportsResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	var status report.Status
	switch req.GetStatus() {
	case corev1.ReportStatus_REPORT_STATUS_UNSPECIFIED, corev1.ReportStatus_REPORT_STATUS_OPEN:
		status = report.StatusOpen
	case corev1.ReportStatus_REPORT_STATUS_RESOLVED:
		status = report.StatusResolved
	default:
		return nil, validation("status")
	}
	vs, next, err := s.app.ListQueue(ctx, user, t, status, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListTayfaReportsResponse{NextPageToken: next, Reports: make([]*corev1.ModerationReport, 0, len(vs))}
	for _, v := range vs {
		out.Reports = append(out.Reports, modReportPB(v))
	}
	return out, nil
}

// ResolveReport implements corev1.ReportServiceServer.
func (s *ReportServer) ResolveReport(ctx context.Context, req *corev1.ResolveReportRequest) (*corev1.ResolveReportResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	r, err := parseID(req.GetReportId(), "report_id")
	if err != nil {
		return nil, err
	}
	res, ok := reportResolutions[req.GetResolution()]
	if !ok {
		return nil, validation("resolution")
	}
	v, err := s.app.Resolve(ctx, user, t, r, res)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ResolveReportResponse{Report: modReportPB(v)}, nil
}

var reportTargets = map[corev1.ReportTargetType]report.TargetType{
	corev1.ReportTargetType_REPORT_TARGET_TYPE_USER:          report.TargetUser,
	corev1.ReportTargetType_REPORT_TARGET_TYPE_TAYFA:         report.TargetTayfa,
	corev1.ReportTargetType_REPORT_TARGET_TYPE_ANNOUNCEMENT:  report.TargetAnnouncement,
	corev1.ReportTargetType_REPORT_TARGET_TYPE_EVENT:         report.TargetEvent,
	corev1.ReportTargetType_REPORT_TARGET_TYPE_QUESTION:      report.TargetQuestion,
	corev1.ReportTargetType_REPORT_TARGET_TYPE_ANSWER:        report.TargetAnswer,
	corev1.ReportTargetType_REPORT_TARGET_TYPE_POLL:          report.TargetPoll,
	corev1.ReportTargetType_REPORT_TARGET_TYPE_FISHING_CATCH: report.TargetFishingCatch,
}

var reportReasons = map[corev1.ReportReason]report.Reason{
	corev1.ReportReason_REPORT_REASON_HARASSMENT:         report.ReasonHarassment,
	corev1.ReportReason_REPORT_REASON_HATE_SPEECH:        report.ReasonHateSpeech,
	corev1.ReportReason_REPORT_REASON_VIOLENCE:           report.ReasonViolence,
	corev1.ReportReason_REPORT_REASON_PRIVACY:            report.ReasonPrivacy,
	corev1.ReportReason_REPORT_REASON_IMPERSONATION:      report.ReasonImpersonation,
	corev1.ReportReason_REPORT_REASON_ILLEGAL_TRADE:      report.ReasonIllegalTrade,
	corev1.ReportReason_REPORT_REASON_DRUGS:              report.ReasonDrugs,
	corev1.ReportReason_REPORT_REASON_GAMBLING:           report.ReasonGambling,
	corev1.ReportReason_REPORT_REASON_DANGEROUS_ACTIVITY: report.ReasonDangerousActivity,
	corev1.ReportReason_REPORT_REASON_FAKE_EVENT:         report.ReasonFakeEvent,
	corev1.ReportReason_REPORT_REASON_SEXUAL_CONTENT:     report.ReasonSexualContent,
	corev1.ReportReason_REPORT_REASON_CHILD_SAFETY:       report.ReasonChildSafety,
	corev1.ReportReason_REPORT_REASON_SPAM:               report.ReasonSpam,
	corev1.ReportReason_REPORT_REASON_COPYRIGHT:          report.ReasonCopyright,
	corev1.ReportReason_REPORT_REASON_STAFF_CONDUCT:      report.ReasonStaffConduct,
	corev1.ReportReason_REPORT_REASON_OTHER:              report.ReasonOther,
}

var reportResolutions = map[corev1.ReportResolution]report.Resolution{
	corev1.ReportResolution_REPORT_RESOLUTION_CONTENT_REMOVED:   report.ResolutionContentRemoved,
	corev1.ReportResolution_REPORT_RESOLUTION_MEMBER_SANCTIONED: report.ResolutionMemberSanctioned,
	corev1.ReportResolution_REPORT_RESOLUTION_DISMISSED:         report.ResolutionDismissed,
}

// toProto finds the proto value of a stored one; UNSPECIFIED (zero) when
// there is none.
func toProto[P comparable, V comparable](m map[P]V, v V) P {
	for k, s := range m {
		if s == v {
			return k
		}
	}
	var zero P
	return zero
}

func reportStatusPB(s report.Status) corev1.ReportStatus {
	switch s {
	case report.StatusOpen:
		return corev1.ReportStatus_REPORT_STATUS_OPEN
	case report.StatusResolved:
		return corev1.ReportStatus_REPORT_STATUS_RESOLVED
	}
	return corev1.ReportStatus_REPORT_STATUS_UNSPECIFIED
}

func optionalTime(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func myReportPB(v report.MyView) *corev1.MyReport {
	out := &corev1.MyReport{
		Id: v.ID.String(), TayfaId: v.TayfaID.String(), TargetType: toProto(reportTargets, v.TargetType),
		TargetId: v.TargetID.String(), Reason: toProto(reportReasons, v.Reason), Description: v.Description,
		Status: reportStatusPB(v.Status), CreateTime: timestamppb.New(v.CreatedAt), ResolveTime: optionalTime(v.ResolvedAt),
	}
	switch v.Outcome {
	case report.OutcomeActionTaken:
		out.Outcome = corev1.ReportOutcome_REPORT_OUTCOME_ACTION_TAKEN
	case report.OutcomeDismissed:
		out.Outcome = corev1.ReportOutcome_REPORT_OUTCOME_DISMISSED
	}
	return out
}

func modReportPB(v report.ModView) *corev1.ModerationReport {
	out := &corev1.ModerationReport{
		Id: v.ID.String(), TayfaId: v.TayfaID.String(), TargetType: toProto(reportTargets, v.TargetType),
		TargetId: v.TargetID.String(), SubjectUserId: optionalID(v.SubjectID), Reason: toProto(reportReasons, v.Reason),
		Description: v.Description, Priority: corev1.ReportPriority_REPORT_PRIORITY_NORMAL, Status: reportStatusPB(v.Status),
		Resolution: toProto(reportResolutions, v.Resolution), HandledByUserId: optionalID(v.HandledBy),
		Escalated: v.Escalated, CreateTime: timestamppb.New(v.CreatedAt), ResolveTime: optionalTime(v.ResolvedAt),
	}
	if v.Priority == report.PriorityMinorInvolved {
		out.Priority = corev1.ReportPriority_REPORT_PRIORITY_MINOR_INVOLVED
	}
	return out
}
