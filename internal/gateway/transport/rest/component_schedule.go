package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The schedule component (kind "schedule", issue #247; ADR-0025 §4). core
// decides everything; the gateway only keeps junk out early. An item's
// title and note travel in bodies only and are never logged.

// Limits of core's schedule package.
const (
	scheduleTitleMax = 120
	scheduleNoteMax  = 500
)

// registerScheduleComponent is the schedule component's line in the
// registry (componentRoutes).
func registerScheduleComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "schedule"), h.authed(h.getSchedule))
	mux.HandleFunc(componentPath("POST", "schedule/items"), h.authed(h.addScheduleItem))
	mux.HandleFunc(componentPath("PUT", "schedule/items/{item_id}"), h.authed(h.updateScheduleItem))
	mux.HandleFunc(componentPath("DELETE", "schedule/items/{item_id}"), h.authed(h.removeScheduleItem))
}

func (h componentHandlers) schedules() corev1.ScheduleComponentServiceClient {
	return corev1.NewScheduleComponentServiceClient(h.conn)
}

type scheduleItemJSON struct {
	ID       string     `json:"id"`
	StartsAt time.Time  `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`
	Title    string     `json:"title"`
	Note     string     `json:"note,omitempty"`
}

type scheduleJSON struct {
	Items         []scheduleItemJSON `json:"items"`
	MaxItems      int32              `json:"max_items"`
	CallerCanEdit bool               `json:"caller_can_edit"`
}

func scheduleFrom(s *corev1.Schedule) scheduleJSON {
	out := scheduleJSON{
		Items: make([]scheduleItemJSON, 0, len(s.GetItems())), MaxItems: s.GetMaxItems(), CallerCanEdit: s.GetCallerCanEdit(),
	}
	for _, it := range s.GetItems() {
		out.Items = append(out.Items, scheduleItemJSON{
			ID: it.GetId(), StartsAt: it.GetStartTime().AsTime(), EndsAt: optTime(it.GetEndTime()), Title: it.GetTitle(), Note: it.GetNote(),
		})
	}
	return out
}

// scheduleItemBody is an item's values. StartsAt is a pointer so that a
// body without it is refused.
type scheduleItemBody struct {
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
	Title    string     `json:"title"`
	Note     *string    `json:"note"`
}

// scheduleInput reads and checks an item's body; on a bad one it answers
// invalid-request and ok is false.
func scheduleInput(w http.ResponseWriter, r *http.Request) (in *corev1.ScheduleItemInput, ok bool) {
	var b scheduleItemBody
	if !decode(w, r, &b) {
		return nil, false
	}
	note := ""
	if b.Note != nil {
		note = *b.Note
	}
	if b.StartsAt == nil || (b.EndsAt != nil && !b.EndsAt.After(*b.StartsAt)) ||
		runes(b.Title) < 1 || runes(b.Title) > scheduleTitleMax || runes(note) > scheduleNoteMax {
		badRequest(w, r)
		return nil, false
	}
	return &corev1.ScheduleItemInput{
		StartTime: optTimestamp(b.StartsAt), EndTime: optTimestamp(b.EndsAt), Title: b.Title, Note: note,
	}, true
}

// scheduleItemRef reads and checks the path's component and item.
func scheduleItemRef(w http.ResponseWriter, r *http.Request) (ref *corev1.ComponentRef, itemID string, ok bool) {
	if ref, ok = componentRef(w, r); !ok {
		return nil, "", false
	}
	if itemID, ok = pathUUID(w, r, "item_id"); !ok {
		return nil, "", false
	}
	return ref, itemID, true
}

func (h componentHandlers) getSchedule(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.schedules().GetSchedule(ctx, &corev1.GetScheduleRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scheduleFrom(resp.GetSchedule()))
}

func (h componentHandlers) addScheduleItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	in, ok := scheduleInput(w, r)
	if !ok {
		return
	}
	resp, err := h.schedules().AddScheduleItem(ctx, &corev1.AddScheduleItemRequest{Ref: ref, Item: in})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, scheduleFrom(resp.GetSchedule()))
}

func (h componentHandlers) updateScheduleItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, item, ok := scheduleItemRef(w, r)
	if !ok {
		return
	}
	in, ok := scheduleInput(w, r)
	if !ok {
		return
	}
	resp, err := h.schedules().UpdateScheduleItem(ctx, &corev1.UpdateScheduleItemRequest{Ref: ref, ItemId: item, Item: in})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scheduleFrom(resp.GetSchedule()))
}

func (h componentHandlers) removeScheduleItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, item, ok := scheduleItemRef(w, r)
	if !ok {
		return
	}
	resp, err := h.schedules().RemoveScheduleItem(ctx, &corev1.RemoveScheduleItemRequest{Ref: ref, ItemId: item})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scheduleFrom(resp.GetSchedule()))
}
