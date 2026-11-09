package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/admin"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/report"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// AdminServiceName is the gRPC service whose methods accept only admin
// tokens (auth.AdminPolicy, issue #104).
const AdminServiceName = "tayfam.core.v1.AdminService"

// AdminServer implements corev1.AdminServiceServer.
type AdminServer struct {
	corev1.UnimplementedAdminServiceServer
	app *admin.Service
	log *slog.Logger
}

// NewAdminServer returns an AdminServer.
func NewAdminServer(a *admin.Service, log *slog.Logger) *AdminServer {
	return &AdminServer{app: a, log: log}
}

func (s *AdminServer) claims(ctx context.Context) (auth.AdminClaims, error) {
	c, ok := auth.AdminCallerFrom(ctx)
	if !ok {
		return auth.AdminClaims{}, status.Error(codes.Unauthenticated, msgUnauthenticated)
	}
	return c, nil
}

func tsOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func adminTayfaPB(t admin.TayfaSummary) *corev1.AdminTayfaSummary {
	return &corev1.AdminTayfaSummary{
		TayfaId: t.ID.String(), Name: t.Name, Status: tayfaStatusToProto(t.Status), Listed: t.Listed,
		MemberCount: int32(min(t.MemberCount, 1<<30)), // #nosec G115 -- bounded
		CreateTime:  timestamppb.New(t.CreatedAt), SuspendTime: tsOrNil(t.SuspendedAt), DeleteTime: tsOrNil(t.DeletedAt),
		DeletionScheduledTime: tsOrNil(t.DeletionScheduledAt),
	}
}

func roleStringToProto(r string) corev1.Role {
	role, err := authz.ParseRole(r)
	if err != nil {
		return corev1.Role_ROLE_UNSPECIFIED
	}
	return roleToProto(role)
}

func adminMemberPB(m admin.Member) *corev1.AdminMember {
	return &corev1.AdminMember{
		UserId: m.User.String(), DisplayName: m.DisplayName, Role: roleStringToProto(m.Role),
		Status: statusToProto(authz.Status(m.Status)), JoinTime: tsOrNil(m.JoinedAt),
	}
}

// SearchTayfas implements corev1.AdminServiceServer.
func (s *AdminServer) SearchTayfas(ctx context.Context, req *corev1.SearchTayfasRequest) (*corev1.SearchTayfasResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	ts, err := s.app.SearchTayfas(ctx, c, req.GetQuery())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.SearchTayfasResponse{Tayfas: make([]*corev1.AdminTayfaSummary, len(ts))}
	for i, t := range ts {
		out.Tayfas[i] = adminTayfaPB(t)
	}
	return out, nil
}

// GetTayfa implements corev1.AdminServiceServer.
func (s *AdminServer) GetTayfa(ctx context.Context, req *corev1.AdminServiceGetTayfaRequest) (*corev1.AdminServiceGetTayfaResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	tf, err := parseID(req.GetTayfaId(), "tayfa_id")
	if err != nil {
		return nil, err
	}
	d, err := s.app.GetTayfa(ctx, c, tf)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.AdminServiceGetTayfaResponse{
		Tayfa: adminTayfaPB(d.TayfaSummary), Description: d.Description, City: d.City, District: d.District,
		SuspendReason: d.SuspendReason, OpenPlatformReports: int32(min(d.OpenPlatformReports, 1<<30)), // #nosec G115 -- bounded
	}
	for _, m := range d.Staff {
		out.Staff = append(out.Staff, adminMemberPB(m))
	}
	return out, nil
}

// ListTayfaMembers implements corev1.AdminServiceServer.
func (s *AdminServer) ListTayfaMembers(ctx context.Context, req *corev1.ListTayfaMembersRequest) (*corev1.ListTayfaMembersResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	tf, err := parseID(req.GetTayfaId(), "tayfa_id")
	if err != nil {
		return nil, err
	}
	ms, err := s.app.ListTayfaMembers(ctx, c, tf)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListTayfaMembersResponse{Members: make([]*corev1.AdminMember, len(ms))}
	for i, m := range ms {
		out.Members[i] = adminMemberPB(m)
	}
	return out, nil
}

// ListUserTayfas implements corev1.AdminServiceServer.
func (s *AdminServer) ListUserTayfas(ctx context.Context, req *corev1.ListUserTayfasRequest) (*corev1.ListUserTayfasResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	u, err := parseID(req.GetUserId(), "user_id")
	if err != nil {
		return nil, err
	}
	ts, err := s.app.ListUserTayfas(ctx, c, u)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListUserTayfasResponse{Tayfas: make([]*corev1.ListUserTayfasResponse_Entry, len(ts))}
	for i, t := range ts {
		out.Tayfas[i] = &corev1.ListUserTayfasResponse_Entry{Tayfa: adminTayfaPB(t.Tayfa), Role: roleStringToProto(t.Role)}
	}
	return out, nil
}

// SuspendTayfa implements corev1.AdminServiceServer.
func (s *AdminServer) SuspendTayfa(ctx context.Context, req *corev1.SuspendTayfaRequest) (*corev1.SuspendTayfaResponse, error) {
	return run(ctx, s, req.GetTayfaId(), func(c auth.AdminClaims, tf id.ID) error {
		return s.app.SuspendTayfa(ctx, c, tf, req.GetReason())
	}, &corev1.SuspendTayfaResponse{})
}

// UnsuspendTayfa implements corev1.AdminServiceServer.
func (s *AdminServer) UnsuspendTayfa(ctx context.Context, req *corev1.UnsuspendTayfaRequest) (*corev1.UnsuspendTayfaResponse, error) {
	return run(ctx, s, req.GetTayfaId(), func(c auth.AdminClaims, tf id.ID) error {
		return s.app.UnsuspendTayfa(ctx, c, tf, req.GetReason())
	}, &corev1.UnsuspendTayfaResponse{})
}

// DeleteTayfa implements corev1.AdminServiceServer.
func (s *AdminServer) DeleteTayfa(ctx context.Context, req *corev1.AdminServiceDeleteTayfaRequest) (*corev1.AdminServiceDeleteTayfaResponse, error) {
	return run(ctx, s, req.GetTayfaId(), func(c auth.AdminClaims, tf id.ID) error {
		return s.app.DeleteTayfa(ctx, c, tf, req.GetReason())
	}, &corev1.AdminServiceDeleteTayfaResponse{})
}

var roleNames = map[corev1.Role]string{
	corev1.Role_ROLE_ADMIN: "admin", corev1.Role_ROLE_MODERATOR: "moderator", corev1.Role_ROLE_MEMBER: "member",
}

// SetMemberRole implements corev1.AdminServiceServer.
func (s *AdminServer) SetMemberRole(ctx context.Context, req *corev1.SetMemberRoleRequest) (*corev1.SetMemberRoleResponse, error) {
	return run(ctx, s, req.GetTayfaId(), func(c auth.AdminClaims, tf id.ID) error {
		u, ok := id.Parse(req.GetUserId())
		if !ok {
			return apperr.Validation("user_id")
		}
		role, ok := roleNames[req.GetRole()]
		if !ok {
			return apperr.Validation("role")
		}
		return s.app.SetMemberRole(ctx, c, tf, u, role, req.GetReason())
	}, &corev1.SetMemberRoleResponse{})
}

// run is the common shape of the tayfa changes.
func run[T any](ctx context.Context, s *AdminServer, rawID string, f func(auth.AdminClaims, id.ID) error, ok T) (T, error) {
	var zero T
	c, err := s.claims(ctx)
	if err != nil {
		return zero, err
	}
	v, err := parseID(rawID, "tayfa_id")
	if err != nil {
		return zero, err
	}
	if err := f(c, v); err != nil {
		return zero, errorStatus(ctx, s.log, err)
	}
	return ok, nil
}

var decisions = map[corev1.PlatformDecision]string{
	corev1.PlatformDecision_PLATFORM_DECISION_CONTENT_REMOVED: admin.DecisionContentRemoved,
	corev1.PlatformDecision_PLATFORM_DECISION_WARNED:          admin.DecisionWarned,
	corev1.PlatformDecision_PLATFORM_DECISION_TAYFA_SUSPENDED: admin.DecisionTayfaSuspended,
	corev1.PlatformDecision_PLATFORM_DECISION_USER_BANNED:     admin.DecisionUserBanned,
	corev1.PlatformDecision_PLATFORM_DECISION_DISMISSED:       admin.DecisionDismissed,
}

func decisionToProto(d string) corev1.PlatformDecision {
	for k, v := range decisions {
		if v == d {
			return k
		}
	}
	return corev1.PlatformDecision_PLATFORM_DECISION_UNSPECIFIED
}

func targetToProto(t string) corev1.ReportTargetType {
	for k, v := range reportTargets {
		if string(v) == t {
			return k
		}
	}
	return corev1.ReportTargetType_REPORT_TARGET_TYPE_UNSPECIFIED
}

func reasonToProto(r string) corev1.ReportReason {
	for k, v := range reportReasons {
		if string(v) == r {
			return k
		}
	}
	return corev1.ReportReason_REPORT_REASON_UNSPECIFIED
}

func idString(v id.ID) string {
	if v == (id.ID{}) {
		return ""
	}
	return v.String()
}

func platformReportPB(r admin.Report) *corev1.PlatformReport {
	priority := corev1.ReportPriority_REPORT_PRIORITY_NORMAL
	if r.Priority == string(report.PriorityMinorInvolved) {
		priority = corev1.ReportPriority_REPORT_PRIORITY_MINOR_INVOLVED
	}
	return &corev1.PlatformReport{
		ReportId: r.ID.String(), TayfaId: idString(r.Tayfa), TargetType: targetToProto(r.TargetType), TargetId: r.Target.String(),
		SubjectUserId: idString(r.Subject), ReporterId: idString(r.Reporter), Reason: reasonToProto(r.Reason),
		Description: r.Description, Priority: priority, CreateTime: timestamppb.New(r.CreatedAt),
		EscalateTime: tsOrNil(r.EscalatedAt), ResolvedOnPlatform: r.Resolved, Decision: decisionToProto(r.Decision),
		DecideTime: tsOrNil(r.DecidedAt),
	}
}

// ListPlatformReports implements corev1.AdminServiceServer.
func (s *AdminServer) ListPlatformReports(ctx context.Context, req *corev1.ListPlatformReportsRequest) (*corev1.ListPlatformReportsResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	rs, err := s.app.ListPlatformReports(ctx, c, req.GetIncludeResolved())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListPlatformReportsResponse{Reports: make([]*corev1.PlatformReport, len(rs))}
	for i, r := range rs {
		out.Reports[i] = platformReportPB(r)
	}
	return out, nil
}

// GetReportedItem implements corev1.AdminServiceServer.
func (s *AdminServer) GetReportedItem(ctx context.Context, req *corev1.GetReportedItemRequest) (*corev1.GetReportedItemResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	rid, err := parseID(req.GetReportId(), "report_id")
	if err != nil {
		return nil, err
	}
	r, item, err := s.app.GetReportedItem(ctx, c, rid)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.GetReportedItemResponse{Report: platformReportPB(r)}
	if item != nil {
		out.Item = &corev1.ReportedItem{
			TargetType: targetToProto(item.TargetType), TargetId: item.Target.String(), AuthorId: idString(item.Author),
			Title: item.Title, Body: item.Body, Options: item.Options,
			CreateTime: tsOrNil(item.CreatedAt), DeleteTime: tsOrNil(item.DeletedAt),
		}
	}
	return out, nil
}

// DecideReport implements corev1.AdminServiceServer.
func (s *AdminServer) DecideReport(ctx context.Context, req *corev1.DecideReportRequest) (*corev1.DecideReportResponse, error) {
	c, err := s.claims(ctx)
	if err != nil {
		return nil, err
	}
	rid, err := parseID(req.GetReportId(), "report_id")
	if err != nil {
		return nil, err
	}
	d, ok := decisions[req.GetDecision()]
	if !ok {
		return nil, errorStatus(ctx, s.log, apperr.Validation("decision"))
	}
	if err := s.app.DecideReport(ctx, c, rid, d, req.GetReason()); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DecideReportResponse{}, nil
}
