package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// EventComponents is what EventComponentServer needs from the app layer
// (component.Service).
type EventComponents interface {
	List(ctx context.Context, user, tayfa, event id.ID) (component.States, error)
	Set(ctx context.Context, user, tayfa, event id.ID, sel component.Selection) (component.States, error)
	ListReferees(ctx context.Context, user, tayfa, event id.ID) ([]component.Referee, error)
	AssignReferee(ctx context.Context, user, tayfa, event id.ID, in component.RefereeInput) (component.Referee, error)
	RemoveReferee(ctx context.Context, user, tayfa, event, referee id.ID) error
	CheckAccess(ctx context.Context, user, tayfa, event id.ID) error
}

// EventComponentServer implements corev1.EventComponentServiceServer
// (issue #238; ADR-0025 §4–§6, ADR-0037 §2). Every method needs a verified
// token (auth.Guard); the acting user is always the token's.
type EventComponentServer struct {
	corev1.UnimplementedEventComponentServiceServer
	app EventComponents
	log *slog.Logger
}

// NewEventComponentServer returns an EventComponentServer.
func NewEventComponentServer(app EventComponents, log *slog.Logger) *EventComponentServer {
	return &EventComponentServer{app: app, log: log}
}

// componentRef parses the verified caller and a component request's
// ComponentRef. Every component's server starts with it.
func componentRef(ctx context.Context, ref *corev1.ComponentRef) (component.Ref, error) {
	user, t, e, err := scope(ctx, ref.GetTayfaId(), ptr(ref.GetEventId()))
	if err != nil {
		return component.Ref{}, err
	}
	if !definition.ValidKey(ref.GetComponentKey()) {
		return component.Ref{}, validation("component_key")
	}
	return component.Ref{User: user, Tayfa: t, Event: e, Key: ref.GetComponentKey()}, nil
}

// selectionFromProto converts a selection; field names the request field
// in a validation error.
func selectionFromProto(sel *corev1.ComponentSelection, field string) (component.Selection, error) {
	if sel == nil || !definition.ValidKey(sel.GetComponentKey()) {
		return component.Selection{}, validation(field)
	}
	out := component.Selection{Key: sel.GetComponentKey(), Enabled: sel.Enabled}
	if len(sel.GetChoices()) > definition.MaxComponents {
		return component.Selection{}, validation(field)
	}
	if len(sel.GetChoices()) > 0 {
		out.Choices = make(map[string]string, len(sel.GetChoices()))
		for name, value := range sel.GetChoices() {
			// An option is a key or an integer: never longer than a key.
			if !definition.ValidKey(name) || value == "" || len(value) > 40 {
				return component.Selection{}, validation(field)
			}
			out.Choices[name] = value
		}
	}
	return out, nil
}

// selectionsFromProto converts the selections of a create request.
func selectionsFromProto(in []*corev1.ComponentSelection) ([]component.Selection, error) {
	if len(in) > definition.MaxComponents {
		return nil, validation("components")
	}
	out := make([]component.Selection, 0, len(in))
	for _, sel := range in {
		s, err := selectionFromProto(sel, "components")
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func eventComponentsPB(st component.States) []*corev1.EventComponent {
	out := make([]*corev1.EventComponent, 0, len(st.Components))
	for _, c := range st.Components {
		pb := &corev1.EventComponent{Key: c.Key, Kind: c.Kind, Optional: c.Optional, Enabled: c.Enabled}
		for _, ch := range c.Choices {
			pb.Choices = append(pb.Choices, &corev1.ComponentChoice{Param: ch.Param, Value: ch.Value, Options: ch.Options})
		}
		out = append(out, pb)
	}
	return out
}

func eventRefereePB(r component.Referee) *corev1.EventReferee {
	out := &corev1.EventReferee{
		Id: r.ID.String(), UserId: r.User.String(), ComponentKey: r.ComponentKey, CreateTime: timestamppb.New(r.CreatedAt),
	}
	if r.Scope != (id.ID{}) {
		out.ScopeId = r.Scope.String()
	}
	return out
}

// ListEventComponents implements corev1.EventComponentServiceServer.
func (s *EventComponentServer) ListEventComponents(ctx context.Context, req *corev1.ListEventComponentsRequest) (*corev1.ListEventComponentsResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	st, err := s.app.List(ctx, user, t, e)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListEventComponentsResponse{Components: eventComponentsPB(st), Locked: st.Locked, CallerCanManage: st.CallerCanManage}, nil
}

// SetEventComponent implements corev1.EventComponentServiceServer.
func (s *EventComponentServer) SetEventComponent(ctx context.Context, req *corev1.SetEventComponentRequest) (*corev1.SetEventComponentResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	sel, err := selectionFromProto(req.GetSelection(), "selection")
	if err != nil {
		return nil, err
	}
	st, err := s.app.Set(ctx, user, t, e, sel)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetEventComponentResponse{Components: eventComponentsPB(st), Locked: st.Locked, CallerCanManage: st.CallerCanManage}, nil
}

// ListEventReferees implements corev1.EventComponentServiceServer.
func (s *EventComponentServer) ListEventReferees(ctx context.Context, req *corev1.ListEventRefereesRequest) (*corev1.ListEventRefereesResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	list, err := s.app.ListReferees(ctx, user, t, e)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListEventRefereesResponse{Referees: make([]*corev1.EventReferee, 0, len(list))}
	for _, r := range list {
		out.Referees = append(out.Referees, eventRefereePB(r))
	}
	return out, nil
}

// AssignEventReferee implements corev1.EventComponentServiceServer.
func (s *EventComponentServer) AssignEventReferee(ctx context.Context, req *corev1.AssignEventRefereeRequest) (*corev1.AssignEventRefereeResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	in := component.RefereeInput{ComponentKey: req.GetComponentKey()}
	var ok bool
	if in.User, ok = id.Parse(req.GetUserId()); !ok {
		return nil, validation("user_id")
	}
	if in.ComponentKey != "" && !definition.ValidKey(in.ComponentKey) {
		return nil, validation("component_key")
	}
	if sid := req.GetScopeId(); sid != "" {
		if in.Scope, ok = id.Parse(sid); !ok {
			return nil, validation("scope_id")
		}
	}
	r, err := s.app.AssignReferee(ctx, user, t, e, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AssignEventRefereeResponse{Referee: eventRefereePB(r)}, nil
}

// RemoveEventReferee implements corev1.EventComponentServiceServer.
func (s *EventComponentServer) RemoveEventReferee(ctx context.Context, req *corev1.RemoveEventRefereeRequest) (*corev1.RemoveEventRefereeResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	ref, ok := id.Parse(req.GetRefereeId())
	if !ok {
		return nil, validation("referee_id")
	}
	if err := s.app.RemoveReferee(ctx, user, t, e, ref); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveEventRefereeResponse{}, nil
}

// CheckEventAccess implements corev1.EventComponentServiceServer.
func (s *EventComponentServer) CheckEventAccess(ctx context.Context, req *corev1.CheckEventAccessRequest) (*corev1.CheckEventAccessResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	if err := s.app.CheckAccess(ctx, user, t, e); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CheckEventAccessResponse{}, nil
}
