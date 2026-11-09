package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/platformadmin"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// AdminUserServiceName and PlatformAuditServiceName are the gRPC services
// of the panel's user management (admin tokens only) and of core's audit
// entries (tayfam-core's service token only).
const (
	AdminUserServiceName     = "tayfam.identity.v1.AdminUserService"
	PlatformAuditServiceName = "tayfam.identity.v1.PlatformAuditService"
)

// AdminUsers is what AdminUserServer needs from the app layer.
type AdminUsers interface {
	SearchUsers(ctx context.Context, c auth.AdminClaims, query string) ([]platformadmin.UserSummary, error)
	GetUser(ctx context.Context, c auth.AdminClaims, user id.ID) (platformadmin.UserDetail, error)
	RevokeUserSessions(ctx context.Context, c auth.AdminClaims, user id.ID, reason string) (int, error)
	SanctionUser(ctx context.Context, c auth.AdminClaims, in platformadmin.SanctionInput) (platformadmin.Sanction, error)
	LiftSanction(ctx context.Context, c auth.AdminClaims, user id.ID, reason string) error
}

// AdminUserServer implements identityv1.AdminUserServiceServer.
type AdminUserServer struct {
	identityv1.UnimplementedAdminUserServiceServer
	users AdminUsers
	log   *slog.Logger
}

// NewAdminUserServer returns an AdminUserServer.
func NewAdminUserServer(u AdminUsers, log *slog.Logger) *AdminUserServer {
	return &AdminUserServer{users: u, log: log}
}

var sanctionTypes = map[identityv1.SanctionType]string{
	identityv1.SanctionType_SANCTION_TYPE_SUSPEND: platformadmin.SanctionSuspend,
	identityv1.SanctionType_SANCTION_TYPE_BAN:     platformadmin.SanctionBan,
}

func sanctionTypeToProto(s string) identityv1.SanctionType {
	for k, v := range sanctionTypes {
		if v == s {
			return k
		}
	}
	return identityv1.SanctionType_SANCTION_TYPE_UNSPECIFIED
}

func adminCaller(ctx context.Context) (auth.AdminClaims, error) {
	c, ok := auth.AdminCallerFrom(ctx)
	if !ok {
		return auth.AdminClaims{}, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	return c, nil
}

func summaryToProto(u platformadmin.UserSummary) *identityv1.AdminUserSummary {
	return &identityv1.AdminUserSummary{
		UserId: u.ID.String(), DisplayName: u.DisplayName, ReferralCode: u.RefCode,
		Registered: u.Registered, Deleted: u.Deleted, ActiveSanction: sanctionTypeToProto(u.ActiveSanction),
	}
}

func sanctionToProto(s platformadmin.Sanction) *identityv1.Sanction {
	out := &identityv1.Sanction{SanctionId: s.ID.String(), Type: sanctionTypeToProto(s.Type), Reason: s.Reason, StartTime: timestamppb.New(s.StartsAt)}
	if !s.EndsAt.IsZero() {
		out.EndTime = timestamppb.New(s.EndsAt)
	}
	return out
}

// SearchUsers implements identityv1.AdminUserServiceServer.
func (s *AdminUserServer) SearchUsers(ctx context.Context, req *identityv1.SearchUsersRequest) (*identityv1.SearchUsersResponse, error) {
	c, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	us, err := s.users.SearchUsers(ctx, c, req.GetQuery())
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	out := &identityv1.SearchUsersResponse{Users: make([]*identityv1.AdminUserSummary, 0, len(us))}
	for _, u := range us {
		out.Users = append(out.Users, summaryToProto(u))
	}
	return out, nil
}

func parseUser(s string) (id.ID, error) {
	u, ok := id.Parse(s)
	if !ok {
		return id.ID{}, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_VALIDATION)
	}
	return u, nil
}

// GetUser implements identityv1.AdminUserServiceServer.
func (s *AdminUserServer) GetUser(ctx context.Context, req *identityv1.GetUserRequest) (*identityv1.GetUserResponse, error) {
	c, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	u, err := parseUser(req.GetUserId())
	if err != nil {
		return nil, err
	}
	d, err := s.users.GetUser(ctx, c, u)
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	out := &identityv1.GetUserResponse{
		User: summaryToProto(d.UserSummary), CreateTime: timestamppb.New(d.CreatedAt),
		ActiveSessions: int32(min(d.ActiveSessions, 1<<20)), // #nosec G115 -- bounded
	}
	if d.Sanction != nil {
		out.Sanction = sanctionToProto(*d.Sanction)
	}
	return out, nil
}

// RevokeUserSessions implements identityv1.AdminUserServiceServer.
func (s *AdminUserServer) RevokeUserSessions(ctx context.Context, req *identityv1.RevokeUserSessionsRequest) (*identityv1.RevokeUserSessionsResponse, error) {
	c, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	u, err := parseUser(req.GetUserId())
	if err != nil {
		return nil, err
	}
	n, err := s.users.RevokeUserSessions(ctx, c, u, req.GetReason())
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.RevokeUserSessionsResponse{Revoked: int32(min(n, 1<<20))}, nil // #nosec G115 -- bounded
}

// SanctionUser implements identityv1.AdminUserServiceServer.
func (s *AdminUserServer) SanctionUser(ctx context.Context, req *identityv1.SanctionUserRequest) (*identityv1.SanctionUserResponse, error) {
	c, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	u, err := parseUser(req.GetUserId())
	if err != nil {
		return nil, err
	}
	typ, ok := sanctionTypes[req.GetType()]
	if !ok {
		return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_VALIDATION)
	}
	in := platformadmin.SanctionInput{User: u, Type: typ, Reason: req.GetReason()}
	if req.GetEndTime() != nil {
		in.EndsAt = req.GetEndTime().AsTime()
	}
	if r := req.GetReportId(); r != "" {
		if in.Report, ok = id.Parse(r); !ok {
			return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_VALIDATION)
		}
	}
	sn, err := s.users.SanctionUser(ctx, c, in)
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.SanctionUserResponse{Sanction: sanctionToProto(sn)}, nil
}

// LiftSanction implements identityv1.AdminUserServiceServer.
func (s *AdminUserServer) LiftSanction(ctx context.Context, req *identityv1.LiftSanctionRequest) (*identityv1.LiftSanctionResponse, error) {
	c, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	u, err := parseUser(req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.users.LiftSanction(ctx, c, u, req.GetReason()); err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.LiftSanctionResponse{}, nil
}

func (s *AdminUserServer) mapError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, platformadmin.ErrNotAdmin):
		return status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	case errors.Is(err, platformadmin.ErrReauthenticationRequired):
		return fail(codes.PermissionDenied, identityv1.ErrorReason_ERROR_REASON_REAUTHENTICATION_REQUIRED)
	case errors.Is(err, platformadmin.ErrReasonRequired):
		return fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_REASON_REQUIRED)
	case errors.Is(err, platformadmin.ErrUserNotFound):
		return fail(codes.NotFound, identityv1.ErrorReason_ERROR_REASON_USER_NOT_FOUND)
	case errors.Is(err, platformadmin.ErrSanctionConflict):
		return fail(codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_SANCTION_CONFLICT)
	case errors.Is(err, platformadmin.ErrValidation):
		return fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_VALIDATION)
	default:
		s.log.ErrorContext(ctx, "panel kullanıcı işlemi başarısız", slog.Any("err", err))
		return status.Error(codes.Internal, "iç hata")
	}
}

// CoreAudits is what PlatformAuditServer needs from the app layer.
type CoreAudits interface {
	RecordFromCore(ctx context.Context, e platformadmin.CoreEntry) error
}

// PlatformAuditServer implements identityv1.PlatformAuditServiceServer.
type PlatformAuditServer struct {
	identityv1.UnimplementedPlatformAuditServiceServer
	audits CoreAudits
	log    *slog.Logger
}

// NewPlatformAuditServer returns a PlatformAuditServer.
func NewPlatformAuditServer(a CoreAudits, log *slog.Logger) *PlatformAuditServer {
	return &PlatformAuditServer{audits: a, log: log}
}

// RecordPlatformAudit implements identityv1.PlatformAuditServiceServer. The
// caller is checked by Guard (tayfam-core's service token).
func (s *PlatformAuditServer) RecordPlatformAudit(ctx context.Context, req *identityv1.RecordPlatformAuditRequest) (*identityv1.RecordPlatformAuditResponse, error) {
	if _, ok := auth.ServiceCallerFrom(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	invalid := status.Error(codes.InvalidArgument, "geçersiz denetim kaydı")
	ev, ok1 := id.Parse(req.GetEventId())
	actor, ok2 := id.Parse(req.GetActorId())
	if !ok1 || !ok2 || req.GetOccurTime() == nil {
		return nil, invalid
	}
	e := platformadmin.CoreEntry{
		EventID: ev, OccurredAt: req.GetOccurTime().AsTime(), Actor: actor,
		Entry: platformadmin.Entry{Action: req.GetAction(), TargetType: req.GetTargetType(), Reason: req.GetReason()},
	}
	if t := req.GetTargetId(); t != "" {
		if e.TargetID, ok1 = id.Parse(t); !ok1 {
			return nil, invalid
		}
	}
	if sid := req.GetAdminSessionId(); sid != "" {
		if e.AdminSession, ok1 = id.Parse(sid); !ok1 {
			return nil, invalid
		}
	}
	e.Metadata = map[string]string{}
	for k, v := range map[string]string{"tayfa_id": req.GetTayfaId(), "user_id": req.GetUserId(), "report_id": req.GetReportId()} {
		if v == "" {
			continue
		}
		if _, ok := id.Parse(v); !ok {
			return nil, invalid
		}
		e.Metadata[k] = v
	}
	if err := s.audits.RecordFromCore(ctx, e); err != nil {
		if errors.Is(err, platformadmin.ErrInvalidAudit) {
			return nil, invalid
		}
		s.log.ErrorContext(ctx, "platform denetim kaydı yazılamadı", slog.Any("err", err))
		return nil, status.Error(codes.Internal, "iç hata")
	}
	return &identityv1.RecordPlatformAuditResponse{}, nil
}
