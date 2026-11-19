package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The component framework's own routes (issue #238; ADR-0025 §4–§6,
// ADR-0037 §2): the organizer's selections for an event's components and
// the event's referees. core decides everything; the gateway only keeps
// junk out early.

const (
	// maxComponentBody bounds a selection or referee request.
	maxComponentBody = 8 << 10
	// maxComponentChoices bounds the choices of one selection; a component
	// has far fewer parameters.
	maxComponentChoices = 20
	// maxComponentSelections is the most components a type has
	// (definition.MaxComponents in core).
	maxComponentSelections = 20
	// maxComponentChoiceValue bounds an option's text: a key or an integer.
	maxComponentChoiceValue = 40
)

func registerEventComponents(mux *http.ServeMux, h componentHandlers) {
	a := h.authed
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/events/{event_id}/components", a(h.listEventComponents))
	mux.HandleFunc("PUT /v1/tayfas/{tayfa_id}/events/{event_id}/components/{component_key}", a(h.setEventComponent))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/events/{event_id}/referees", a(h.listEventReferees))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/events/{event_id}/referees", a(h.assignEventReferee))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/events/{event_id}/referees/{referee_id}", a(h.removeEventReferee))
}

func (h componentHandlers) framework() corev1.EventComponentServiceClient {
	return corev1.NewEventComponentServiceClient(h.conn)
}

type componentChoiceJSON struct {
	Param string `json:"param"`
	Value string `json:"value"`
	// Options is output only.
	Options []string `json:"options,omitempty"`
}

type eventComponentJSON struct {
	Key      string                `json:"key"`
	Kind     string                `json:"kind"`
	Optional bool                  `json:"optional"`
	Enabled  bool                  `json:"enabled"`
	Choices  []componentChoiceJSON `json:"choices"`
}

type eventComponentsJSON struct {
	Components      []eventComponentJSON `json:"components"`
	Locked          bool                 `json:"locked"`
	CallerCanManage bool                 `json:"caller_can_manage"`
}

func eventComponentsFrom(list []*corev1.EventComponent, locked, canManage bool) eventComponentsJSON {
	out := eventComponentsJSON{Components: make([]eventComponentJSON, 0, len(list)), Locked: locked, CallerCanManage: canManage}
	for _, c := range list {
		j := eventComponentJSON{
			Key: c.GetKey(), Kind: c.GetKind(), Optional: c.GetOptional(), Enabled: c.GetEnabled(),
			Choices: make([]componentChoiceJSON, 0, len(c.GetChoices())),
		}
		for _, ch := range c.GetChoices() {
			j.Choices = append(j.Choices, componentChoiceJSON{Param: ch.GetParam(), Value: ch.GetValue(), Options: nonNil(ch.GetOptions())})
		}
		out.Components = append(out.Components, j)
	}
	return out
}

// componentSelectionJSON is the organizer's selection for one component: in
// the body of the selection route (the key is in the path) and, with its
// key, in a new event's components.
type componentSelectionJSON struct {
	Key     string                `json:"key"`
	Enabled *bool                 `json:"enabled"`
	Choices []componentChoiceJSON `json:"choices"`
}

// selection checks the selection and converts it for core; ok is false for
// a bad request.
func (s componentSelectionJSON) selection(key string) (*corev1.ComponentSelection, bool) {
	if !catalogKeyPattern.MatchString(key) || len(s.Choices) > maxComponentChoices {
		return nil, false
	}
	out := &corev1.ComponentSelection{ComponentKey: key, Enabled: s.Enabled}
	for _, c := range s.Choices {
		if !catalogKeyPattern.MatchString(c.Param) || c.Value == "" || len(c.Value) > maxComponentChoiceValue || c.Options != nil {
			return nil, false
		}
		if out.Choices == nil {
			out.Choices = map[string]string{}
		}
		if _, dup := out.Choices[c.Param]; dup {
			return nil, false
		}
		out.Choices[c.Param] = c.Value
	}
	return out, true
}

// componentSelections converts the selections of a new event.
func componentSelections(in []componentSelectionJSON) ([]*corev1.ComponentSelection, bool) {
	if len(in) > maxComponentSelections {
		return nil, false
	}
	out := make([]*corev1.ComponentSelection, 0, len(in))
	for _, s := range in {
		sel, ok := s.selection(s.Key)
		if !ok {
			return nil, false
		}
		out = append(out, sel)
	}
	return out, true
}

func (h componentHandlers) listEventComponents(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	resp, err := h.framework().ListEventComponents(ctx, &corev1.ListEventComponentsRequest{TayfaId: id, EventId: eid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventComponentsFrom(resp.GetComponents(), resp.GetLocked(), resp.GetCallerCanManage()))
}

func (h componentHandlers) setEventComponent(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b componentSelectionJSON
	if !decodeLimit(w, r, &b, maxComponentBody) {
		return
	}
	// The key is the path's; a body that names one is refused rather than
	// silently ignored.
	sel, ok := b.selection(ref.GetComponentKey())
	if !ok || b.Key != "" || (b.Enabled == nil && len(b.Choices) == 0) {
		badRequest(w, r)
		return
	}
	resp, err := h.framework().SetEventComponent(ctx, &corev1.SetEventComponentRequest{
		TayfaId: ref.GetTayfaId(), EventId: ref.GetEventId(), Selection: sel,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventComponentsFrom(resp.GetComponents(), resp.GetLocked(), resp.GetCallerCanManage()))
}

type eventRefereeJSON struct {
	ID           string     `json:"id"`
	UserID       string     `json:"user_id"`
	ComponentKey string     `json:"component_key,omitempty"`
	ScopeID      string     `json:"scope_id,omitempty"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
}

func eventRefereeFrom(r *corev1.EventReferee) eventRefereeJSON {
	return eventRefereeJSON{
		ID: r.GetId(), UserID: r.GetUserId(), ComponentKey: r.GetComponentKey(), ScopeID: r.GetScopeId(),
		CreatedAt: optTime(r.GetCreateTime()),
	}
}

type eventRefereesJSON struct {
	Referees []eventRefereeJSON `json:"referees"`
}

func (h componentHandlers) listEventReferees(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	resp, err := h.framework().ListEventReferees(ctx, &corev1.ListEventRefereesRequest{TayfaId: id, EventId: eid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := eventRefereesJSON{Referees: make([]eventRefereeJSON, 0, len(resp.GetReferees()))}
	for _, ref := range resp.GetReferees() {
		out.Referees = append(out.Referees, eventRefereeFrom(ref))
	}
	writeJSON(w, http.StatusOK, out)
}

type eventRefereeBody struct {
	UserID       string `json:"user_id"`
	ComponentKey string `json:"component_key"`
	ScopeID      string `json:"scope_id"`
}

func (h componentHandlers) assignEventReferee(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b eventRefereeBody
	if !decodeLimit(w, r, &b, maxComponentBody) {
		return
	}
	if !validUUID(b.UserID) || (b.ComponentKey != "" && !catalogKeyPattern.MatchString(b.ComponentKey)) ||
		(b.ScopeID != "" && (!validUUID(b.ScopeID) || b.ComponentKey == "")) {
		badRequest(w, r)
		return
	}
	resp, err := h.framework().AssignEventReferee(ctx, &corev1.AssignEventRefereeRequest{
		TayfaId: id, EventId: eid, UserId: b.UserID, ComponentKey: b.ComponentKey, ScopeId: b.ScopeID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, eventRefereeFrom(resp.GetReferee()))
}

func (h componentHandlers) removeEventReferee(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	rid, ok := pathUUID(w, r, "referee_id")
	if !ok {
		return
	}
	if _, err := h.framework().RemoveEventReferee(ctx, &corev1.RemoveEventRefereeRequest{TayfaId: id, EventId: eid, RefereeId: rid}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
