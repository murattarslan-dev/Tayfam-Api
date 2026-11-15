package rest

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// "Boşum": free days shown to all of the caller's friends (issue #207,
// ADR-0028). Every route needs the user's full access token; core checks it
// again, takes the caller from it and applies the friendship rule. Notes
// travel only in the body, never in the path or query, so access logs
// cannot hold them.

// AvailabilityClient is core's AvailabilityService.
type AvailabilityClient interface {
	ListMyAvailability(ctx context.Context, in *corev1.ListMyAvailabilityRequest, opts ...grpc.CallOption) (*corev1.ListMyAvailabilityResponse, error)
	SetAvailability(ctx context.Context, in *corev1.SetAvailabilityRequest, opts ...grpc.CallOption) (*corev1.SetAvailabilityResponse, error)
	DeleteAvailability(ctx context.Context, in *corev1.DeleteAvailabilityRequest, opts ...grpc.CallOption) (*corev1.DeleteAvailabilityResponse, error)
	ListUserAvailability(ctx context.Context, in *corev1.ListUserAvailabilityRequest, opts ...grpc.CallOption) (*corev1.ListUserAvailabilityResponse, error)
}

func registerAvailability(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("GET /v1/me/availability", a(h.listMyAvailability))
	mux.HandleFunc("PUT /v1/me/availability/{date}", a(h.setAvailability))
	mux.HandleFunc("DELETE /v1/me/availability/{date}", a(h.deleteAvailability))
	mux.HandleFunc("GET /v1/users/{user_id}/availability", a(h.listUserAvailability))
}

var daySlots = newEnum(map[string]corev1.DaySlot{
	"morning": corev1.DaySlot_DAY_SLOT_MORNING, "afternoon": corev1.DaySlot_DAY_SLOT_AFTERNOON,
	"evening": corev1.DaySlot_DAY_SLOT_EVENING, "all_day": corev1.DaySlot_DAY_SLOT_ALL_DAY,
})

// maxSlotInput bounds the slots of one request (four names; duplicates
// are ignored, core checks the rest).
const maxSlotInput = 4

type availabilitySummaryJSON struct {
	Date  string   `json:"date"`
	Slots []string `json:"slots"`
}

type availabilityDayJSON struct {
	Date      string    `json:"date"`
	Slots     []string  `json:"slots"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type availabilityListJSON struct {
	Days []availabilityDayJSON `json:"days"`
}

type availabilityBody struct {
	Slots []string `json:"slots"`
	Note  string   `json:"note"`
}

func slotNames(in []corev1.DaySlot) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, daySlots.name(s))
	}
	return out
}

func availabilityDayFrom(d *corev1.AvailabilityDay) availabilityDayJSON {
	return availabilityDayJSON{
		Date: d.GetDate(), Slots: slotNames(d.GetSlots()), Note: d.GetNote(), UpdatedAt: d.GetUpdateTime().AsTime(),
	}
}

// availabilitySummaryFrom is the friend list badge; nil when there is none.
func availabilitySummaryFrom(d *corev1.AvailabilityDay) *availabilitySummaryJSON {
	if d.GetDate() == "" {
		return nil
	}
	return &availabilitySummaryJSON{Date: d.GetDate(), Slots: slotNames(d.GetSlots())}
}

func availabilityList(days []*corev1.AvailabilityDay) availabilityListJSON {
	out := availabilityListJSON{Days: make([]availabilityDayJSON, 0, len(days))}
	for _, d := range days {
		out.Days = append(out.Days, availabilityDayFrom(d))
	}
	return out
}

// pathDate reads a YYYY-MM-DD path value; core checks the range.
func pathDate(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := r.PathValue("date")
	if t, err := time.Parse(time.DateOnly, v); err != nil || t.Format(time.DateOnly) != v {
		badRequest(w, r)
		return "", false
	}
	return v, true
}

func (h coreHandlers) listMyAvailability(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Availability.ListMyAvailability(ctx, &corev1.ListMyAvailabilityRequest{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, availabilityList(resp.GetDays()))
}

func (h coreHandlers) setAvailability(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	date, ok := pathDate(w, r)
	if !ok {
		return
	}
	var b availabilityBody
	if !decode(w, r, &b) {
		return
	}
	if len(b.Slots) == 0 || len(b.Slots) > maxSlotInput {
		badRequest(w, r)
		return
	}
	slots := make([]corev1.DaySlot, 0, len(b.Slots))
	for _, name := range b.Slots {
		s, ok := daySlots.value(name)
		if !ok {
			badRequest(w, r)
			return
		}
		slots = append(slots, s)
	}
	resp, err := h.d.Availability.SetAvailability(ctx, &corev1.SetAvailabilityRequest{Date: date, Slots: slots, Note: b.Note})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, availabilityDayFrom(resp.GetDay()))
}

func (h coreHandlers) deleteAvailability(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	date, ok := pathDate(w, r)
	if !ok {
		return
	}
	if _, err := h.d.Availability.DeleteAvailability(ctx, &corev1.DeleteAvailabilityRequest{Date: date}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) listUserAvailability(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	resp, err := h.d.Availability.ListUserAvailability(ctx, &corev1.ListUserAvailabilityRequest{UserId: u})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, availabilityList(resp.GetDays()))
}
