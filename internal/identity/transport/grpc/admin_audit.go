package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/platformadmin"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// AdminAuditServiceName is the gRPC service of the panel's audit log page
// (admin tokens only, issue #110).
const AdminAuditServiceName = "tayfam.identity.v1.AdminAuditService"

// AdminServiceNames are identity's admin services: their methods accept
// only admin tokens (auth.AdminPolicy). cmd/identity and the guard test use
// the same list.
func AdminServiceNames() []string {
	return []string{AdminAuthServiceName, AdminUserServiceName, AdminAuditServiceName}
}

// AdminAudits is what AdminAuditServer needs from the app layer.
type AdminAudits interface {
	ListAuditLogs(ctx context.Context, c auth.AdminClaims, f platformadmin.AuditFilter, pageSize int, token string) (platformadmin.AuditPage, error)
}

// AdminAuditServer implements identityv1.AdminAuditServiceServer.
type AdminAuditServer struct {
	identityv1.UnimplementedAdminAuditServiceServer
	audits AdminAudits
	log    *slog.Logger
}

// NewAdminAuditServer returns an AdminAuditServer.
func NewAdminAuditServer(a AdminAudits, log *slog.Logger) *AdminAuditServer {
	return &AdminAuditServer{audits: a, log: log}
}

func idOrEmpty(v id.ID) string {
	if v == (id.ID{}) {
		return ""
	}
	return v.String()
}

// ListPlatformAuditLogs implements identityv1.AdminAuditServiceServer.
func (s *AdminAuditServer) ListPlatformAuditLogs(ctx context.Context, req *identityv1.ListPlatformAuditLogsRequest) (*identityv1.ListPlatformAuditLogsResponse, error) {
	c, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	f := platformadmin.AuditFilter{Action: req.GetAction(), TargetType: req.GetTargetType()}
	if r := req.GetRelatedId(); r != "" {
		if f.Related, err = parseUser(r); err != nil {
			return nil, err
		}
	}
	if t := req.GetStartTime(); t != nil {
		f.Since = t.AsTime()
	}
	if t := req.GetEndTime(); t != nil {
		f.Until = t.AsTime()
	}
	page, err := s.audits.ListAuditLogs(ctx, c, f, int(req.GetPageSize()), req.GetPageCursor())
	if err != nil {
		return nil, adminUserError(ctx, s.log, err)
	}
	out := &identityv1.ListPlatformAuditLogsResponse{
		Entries:        make([]*identityv1.PlatformAuditEntry, 0, len(page.Entries)),
		NextPageCursor: page.Next,
	}
	for _, e := range page.Entries {
		out.Entries = append(out.Entries, &identityv1.PlatformAuditEntry{
			EntryId: e.ID.String(), CreateTime: timestamppb.New(e.CreatedAt),
			ActorId: e.Actor.String(), ActorDisplayName: e.ActorName,
			Action: e.Action, TargetType: e.TargetType, TargetId: idOrEmpty(e.TargetID), Reason: e.Reason,
			TayfaId: idOrEmpty(e.Tayfa), UserId: idOrEmpty(e.User), ReportId: idOrEmpty(e.Report),
		})
	}
	return out, nil
}
