package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Tayfas is what TayfaServer needs from the app layer.
type Tayfas interface {
	Create(ctx context.Context, user id.ID, in tayfa.CreateInput) (tayfa.View, error)
	Get(ctx context.Context, user, t id.ID) (tayfa.View, authz.Role, error)
	ListMine(ctx context.Context, user id.ID, pageSize int32, pageToken string) ([]tayfa.MyTayfa, string, error)
	Update(ctx context.Context, user, t id.ID, in tayfa.UpdateInput) (tayfa.View, error)
	SetModule(ctx context.Context, user, t id.ID, module string, enabled bool) error
	RotateJoinCode(ctx context.Context, user, t id.ID) (string, error)
	RequestDeletion(ctx context.Context, user, t id.ID) (time.Time, error)
	CancelDeletion(ctx context.Context, user, t id.ID) error
}

// TayfaServer implements corev1.TayfaServiceServer. The acting user always
// comes from the verified token (auth.Guard), never from the request.
type TayfaServer struct {
	corev1.UnimplementedTayfaServiceServer
	app Tayfas
	log *slog.Logger
}

// NewTayfaServer returns a TayfaServer.
func NewTayfaServer(app Tayfas, log *slog.Logger) *TayfaServer {
	return &TayfaServer{app: app, log: log}
}

// CreateTayfa implements corev1.TayfaServiceServer.
func (s *TayfaServer) CreateTayfa(ctx context.Context, req *corev1.CreateTayfaRequest) (*corev1.CreateTayfaResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	interests, ok := interestsFromProto(req.GetInterests())
	if !ok {
		return nil, validation("interests")
	}
	policy := ""
	if req.GetJoinPolicy() != corev1.JoinPolicy_JOIN_POLICY_UNSPECIFIED {
		if policy, ok = joinPolicyFromProto(req.GetJoinPolicy()); !ok {
			return nil, validation("join_policy")
		}
	}
	v, err := s.app.Create(ctx, user, tayfa.CreateInput{
		Name: req.GetName(), Description: req.GetDescription(), Interests: interests,
		JoinPolicy: policy, ClientRequestID: req.GetClientRequestId(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateTayfaResponse{Tayfa: tayfaToProto(v)}, nil
}

// GetTayfa implements corev1.TayfaServiceServer.
func (s *TayfaServer) GetTayfa(ctx context.Context, req *corev1.GetTayfaRequest) (*corev1.GetTayfaResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	v, role, err := s.app.Get(ctx, user, t)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetTayfaResponse{Tayfa: tayfaToProto(v), CallerRole: roleToProto(role)}, nil
}

// ListMyTayfas implements corev1.TayfaServiceServer.
func (s *TayfaServer) ListMyTayfas(ctx context.Context, req *corev1.ListMyTayfasRequest) (*corev1.ListMyTayfasResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.ListMine(ctx, user, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	resp := &corev1.ListMyTayfasResponse{NextPageToken: next, Tayfas: make([]*corev1.MyTayfa, 0, len(list))}
	for _, e := range list {
		m := &corev1.MyTayfa{TayfaId: e.TayfaID.String(), Name: e.Name, Role: roleToProto(e.Role), Status: statusToProto(e.Status)}
		if e.Tayfa != nil {
			m.Tayfa = tayfaToProto(*e.Tayfa)
		}
		resp.Tayfas = append(resp.Tayfas, m)
	}
	return resp, nil
}

// UpdateTayfa implements corev1.TayfaServiceServer.
func (s *TayfaServer) UpdateTayfa(ctx context.Context, req *corev1.UpdateTayfaRequest) (*corev1.UpdateTayfaResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	in := tayfa.UpdateInput{Fields: req.GetUpdateMask().GetPaths(), Name: req.GetName(), Description: req.GetDescription()}
	for _, f := range in.Fields {
		switch f {
		case tayfa.FieldInterests:
			var ok bool
			if in.Interests, ok = interestsFromProto(req.GetInterests()); !ok {
				return nil, validation("interests")
			}
		case tayfa.FieldJoinPolicy:
			var ok bool
			if in.JoinPolicy, ok = joinPolicyFromProto(req.GetJoinPolicy()); !ok {
				return nil, validation("join_policy")
			}
		}
	}
	v, err := s.app.Update(ctx, user, t, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateTayfaResponse{Tayfa: tayfaToProto(v)}, nil
}

// SetModule implements corev1.TayfaServiceServer.
func (s *TayfaServer) SetModule(ctx context.Context, req *corev1.SetModuleRequest) (*corev1.SetModuleResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	module, ok := moduleFromProto(req.GetModule())
	if !ok {
		return nil, validation("module")
	}
	if err := s.app.SetModule(ctx, user, t, module, req.GetEnabled()); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetModuleResponse{}, nil
}

// RotateJoinCode implements corev1.TayfaServiceServer.
func (s *TayfaServer) RotateJoinCode(ctx context.Context, req *corev1.RotateJoinCodeRequest) (*corev1.RotateJoinCodeResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	code, err := s.app.RotateJoinCode(ctx, user, t)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RotateJoinCodeResponse{JoinCode: code}, nil
}

// RequestDeletion implements corev1.TayfaServiceServer.
func (s *TayfaServer) RequestDeletion(ctx context.Context, req *corev1.RequestDeletionRequest) (*corev1.RequestDeletionResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	at, err := s.app.RequestDeletion(ctx, user, t)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RequestDeletionResponse{DeletionTime: timestamppb.New(at)}, nil
}

// CancelDeletion implements corev1.TayfaServiceServer.
func (s *TayfaServer) CancelDeletion(ctx context.Context, req *corev1.CancelDeletionRequest) (*corev1.CancelDeletionResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	if err := s.app.CancelDeletion(ctx, user, t); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CancelDeletionResponse{}, nil
}

func tayfaToProto(v tayfa.View) *corev1.Tayfa {
	out := &corev1.Tayfa{
		Id: v.ID.String(), Name: v.Name, Description: v.Description,
		JoinPolicy: joinPolicyToProto(v.JoinPolicy), Status: tayfaStatusToProto(v.Status),
		MemberCount: int32(min(v.MemberCount, 1<<31-1)), //nolint:gosec // clamped above
		JoinCode:    v.JoinCode,
		CreateTime:  timestamppb.New(v.CreatedAt), UpdateTime: timestamppb.New(v.UpdatedAt),
	}
	for _, i := range v.Interests {
		out.Interests = append(out.Interests, interestToProto(i))
	}
	for _, m := range v.Modules {
		out.Modules = append(out.Modules, &corev1.ModuleState{Module: moduleToProto(m.Module), Enabled: m.Enabled})
	}
	if !v.SuspendedAt.IsZero() {
		out.SuspendTime = timestamppb.New(v.SuspendedAt)
	}
	if !v.DeletionAt.IsZero() {
		out.DeletionTime = timestamppb.New(v.DeletionAt)
	}
	return out
}
