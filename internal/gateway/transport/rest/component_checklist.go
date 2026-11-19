package rest

import (
	"context"
	"encoding/json"
	"net/http"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The list component (kind "checklist", issue #241; ADR-0025 §4). core
// decides everything; the gateway only keeps junk out early. An item's name
// travels in bodies only and is never logged.

// Limits of core's checklist package.
const (
	checklistNameMax     = 60
	checklistQuantityMax = 999
)

// registerChecklistComponent is the list component's line in the registry
// (componentRoutes).
func registerChecklistComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "items"), h.authed(h.listChecklist))
	mux.HandleFunc(componentPath("POST", "items"), h.authed(h.addChecklistItem))
	mux.HandleFunc(componentPath("DELETE", "items/{item_id}"), h.authed(h.removeChecklistItem))
	mux.HandleFunc(componentPath("PUT", "items/{item_id}/assignee"), h.authed(h.assignChecklistItem))
}

func (h componentHandlers) checklist() corev1.ChecklistComponentServiceClient {
	return corev1.NewChecklistComponentServiceClient(h.conn)
}

type checklistItemJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Quantity int32  `json:"quantity"`
	// AssignedUserID is left out when nobody took the item.
	AssignedUserID string `json:"assigned_user_id,omitempty"`
}

type checklistJSON struct {
	Items             []checklistItemJSON `json:"items"`
	Label             string              `json:"label"`
	AssignmentEnabled bool                `json:"assignment_enabled"`
	QuantityEnabled   bool                `json:"quantity_enabled"`
	CallerCanManage   bool                `json:"caller_can_manage"`
	CallerCanTake     bool                `json:"caller_can_take"`
}

func checklistFrom(c *corev1.Checklist) checklistJSON {
	out := checklistJSON{
		Items: make([]checklistItemJSON, 0, len(c.GetItems())), Label: c.GetLabel(),
		AssignmentEnabled: c.GetAssignmentEnabled(), QuantityEnabled: c.GetQuantityEnabled(),
		CallerCanManage: c.GetCallerCanManage(), CallerCanTake: c.GetCallerCanTake(),
	}
	for _, it := range c.GetItems() {
		out.Items = append(out.Items, checklistItemJSON{
			ID: it.GetId(), Name: it.GetName(), Quantity: it.GetQuantity(), AssignedUserID: it.GetAssignedUserId(),
		})
	}
	return out
}

// checklistItemRef reads and checks the path's component and item.
func checklistItemRef(w http.ResponseWriter, r *http.Request) (ref *corev1.ComponentRef, itemID string, ok bool) {
	if ref, ok = componentRef(w, r); !ok {
		return nil, "", false
	}
	if itemID, ok = pathUUID(w, r, "item_id"); !ok {
		return nil, "", false
	}
	return ref, itemID, true
}

// nullableUUID reads an optional uuid: missing or null gives "", a uuid
// string gives itself; anything else is not ok.
func nullableUUID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil || !validUUID(v) {
		return "", false
	}
	return v, true
}

func (h componentHandlers) listChecklist(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.checklist().ListChecklist(ctx, &corev1.ListChecklistRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, checklistFrom(resp.GetChecklist()))
}

type checklistItemBody struct {
	Name           string          `json:"name"`
	Quantity       int32           `json:"quantity"`
	AssignedUserID json.RawMessage `json:"assigned_user_id"`
}

func (h componentHandlers) addChecklistItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b checklistItemBody
	if !decode(w, r, &b) {
		return
	}
	assigned, ok := nullableUUID(b.AssignedUserID)
	if n := runes(b.Name); !ok || n < 1 || n > checklistNameMax || b.Quantity < 0 || b.Quantity > checklistQuantityMax {
		badRequest(w, r)
		return
	}
	resp, err := h.checklist().AddChecklistItem(ctx, &corev1.AddChecklistItemRequest{
		Ref: ref, Name: b.Name, Quantity: b.Quantity, AssignedUserId: assigned,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, checklistFrom(resp.GetChecklist()))
}

func (h componentHandlers) removeChecklistItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, item, ok := checklistItemRef(w, r)
	if !ok {
		return
	}
	resp, err := h.checklist().RemoveChecklistItem(ctx, &corev1.RemoveChecklistItemRequest{Ref: ref, ItemId: item})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, checklistFrom(resp.GetChecklist()))
}

// checklistAssignBody: a uuid sets the assignee; a missing field or null
// clears it.
type checklistAssignBody struct {
	AssignedUserID json.RawMessage `json:"assigned_user_id"`
}

func (h componentHandlers) assignChecklistItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, item, ok := checklistItemRef(w, r)
	if !ok {
		return
	}
	var b checklistAssignBody
	if !decode(w, r, &b) {
		return
	}
	assigned, ok := nullableUUID(b.AssignedUserID)
	if !ok {
		badRequest(w, r)
		return
	}
	resp, err := h.checklist().AssignChecklistItem(ctx, &corev1.AssignChecklistItemRequest{Ref: ref, ItemId: item, AssignedUserId: assigned})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, checklistFrom(resp.GetChecklist()))
}
