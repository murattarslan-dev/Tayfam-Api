package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/checklist"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Checklist is what ChecklistComponentServer needs from the app layer
// (checklist.Service).
type Checklist interface {
	List(ctx context.Context, ref component.Ref) (checklist.List, error)
	Add(ctx context.Context, ref component.Ref, in checklist.Input) (checklist.List, error)
	Remove(ctx context.Context, ref component.Ref, item id.ID) (checklist.List, error)
	Assign(ctx context.Context, ref component.Ref, item, assigned id.ID) (checklist.List, error)
}

// ChecklistComponentServer implements corev1.ChecklistComponentServiceServer
// (issue #241; ADR-0025 §4). Every method needs a verified token
// (auth.Guard); the acting user is always the token's.
type ChecklistComponentServer struct {
	corev1.UnimplementedChecklistComponentServiceServer
	app Checklist
	log *slog.Logger
}

// NewChecklistComponentServer returns a ChecklistComponentServer.
func NewChecklistComponentServer(app Checklist, log *slog.Logger) *ChecklistComponentServer {
	return &ChecklistComponentServer{app: app, log: log}
}

// registerChecklistComponent is the list component's line in the registry
// (componentRegistrars).
func registerChecklistComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterChecklistComponentServiceServer(srv, NewChecklistComponentServer(checklist.New(fw), log))
	return nil
}

func checklistPB(l checklist.List) *corev1.Checklist {
	out := &corev1.Checklist{
		Items: make([]*corev1.ChecklistItem, 0, len(l.Items)), Label: l.Label,
		AssignmentEnabled: l.AssignmentEnabled, QuantityEnabled: l.QuantityEnabled,
		CallerCanManage: l.CallerCanManage, CallerCanTake: l.CallerCanTake,
	}
	for _, it := range l.Items {
		pb := &corev1.ChecklistItem{Id: it.ID.String(), Name: it.Name, Quantity: it.Quantity}
		if it.AssignedUserID != (id.ID{}) {
			pb.AssignedUserId = it.AssignedUserID.String()
		}
		out.Items = append(out.Items, pb)
	}
	return out
}

// checklistItemRef reads the component and the item of a request.
func checklistItemRef(ctx context.Context, ref *corev1.ComponentRef, itemID string) (component.Ref, id.ID, error) {
	r, err := componentRef(ctx, ref)
	if err != nil {
		return component.Ref{}, id.ID{}, err
	}
	it, ok := id.Parse(itemID)
	if !ok {
		return component.Ref{}, id.ID{}, validation("item_id")
	}
	return r, it, nil
}

// assigneeOf reads an optional assignee: empty is nobody.
func assigneeOf(s string) (id.ID, error) {
	if s == "" {
		return id.ID{}, nil
	}
	u, ok := id.Parse(s)
	if !ok {
		return id.ID{}, validation("assigned_user_id")
	}
	return u, nil
}

// ListChecklist implements corev1.ChecklistComponentServiceServer.
func (s *ChecklistComponentServer) ListChecklist(ctx context.Context, req *corev1.ListChecklistRequest) (*corev1.ListChecklistResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.List(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListChecklistResponse{Checklist: checklistPB(out)}, nil
}

// AddChecklistItem implements corev1.ChecklistComponentServiceServer.
func (s *ChecklistComponentServer) AddChecklistItem(ctx context.Context, req *corev1.AddChecklistItemRequest) (*corev1.AddChecklistItemResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if req.GetQuantity() < 0 || req.GetQuantity() > checklist.QuantityMax {
		return nil, validation("quantity")
	}
	assigned, err := assigneeOf(req.GetAssignedUserId())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Add(ctx, ref, checklist.Input{Name: req.GetName(), Quantity: req.GetQuantity(), AssignedUserID: assigned})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddChecklistItemResponse{Checklist: checklistPB(out)}, nil
}

// RemoveChecklistItem implements corev1.ChecklistComponentServiceServer.
func (s *ChecklistComponentServer) RemoveChecklistItem(ctx context.Context, req *corev1.RemoveChecklistItemRequest) (*corev1.RemoveChecklistItemResponse, error) {
	ref, item, err := checklistItemRef(ctx, req.GetRef(), req.GetItemId())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Remove(ctx, ref, item)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveChecklistItemResponse{Checklist: checklistPB(out)}, nil
}

// AssignChecklistItem implements corev1.ChecklistComponentServiceServer.
func (s *ChecklistComponentServer) AssignChecklistItem(ctx context.Context, req *corev1.AssignChecklistItemRequest) (*corev1.AssignChecklistItemResponse, error) {
	ref, item, err := checklistItemRef(ctx, req.GetRef(), req.GetItemId())
	if err != nil {
		return nil, err
	}
	assigned, err := assigneeOf(req.GetAssignedUserId())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Assign(ctx, ref, item, assigned)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AssignChecklistItemResponse{Checklist: checklistPB(out)}, nil
}
