package rest

import (
	"context"
	"net/http"
	"unicode/utf8"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The vehicles component (kind "vehicles", issue #240; ADR-0025 §4,
// ADR-0037 §3). core decides everything; the gateway only keeps junk out
// early. A plate travels in bodies only and is never logged.

// Limits of core's vehicles package.
const (
	vehicleMakeMax  = 40
	vehicleColorMax = 30
	vehiclePlateMax = 16
	vehicleMaxSeats = 8
)

// registerVehiclesComponent is the vehicles component's line in the
// registry (componentRoutes).
func registerVehiclesComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "vehicles"), h.authed(h.listVehicles))
	mux.HandleFunc(componentPath("PUT", "vehicles/mine"), h.authed(h.setVehicle))
	mux.HandleFunc(componentPath("DELETE", "vehicles/{vehicle_id}"), h.authed(h.removeVehicle))
	mux.HandleFunc(componentPath("PUT", "vehicles/{vehicle_id}/seat-requests/mine"), h.authed(h.requestSeat))
	mux.HandleFunc(componentPath("PUT", "vehicles/{vehicle_id}/seat-requests/{user_id}/approval"), h.authed(h.approveSeat))
	mux.HandleFunc(componentPath("DELETE", "vehicles/{vehicle_id}/seat-requests/{user_id}"), h.authed(h.releaseSeat))
}

func (h componentHandlers) vehicles() corev1.VehiclesComponentServiceClient {
	return corev1.NewVehiclesComponentServiceClient(h.conn)
}

type eventVehicleJSON struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Make      string `json:"make"`
	Model     string `json:"model,omitempty"`
	ModelYear int32  `json:"model_year,omitempty"`
	Color     string `json:"color,omitempty"`
	// Plate is present only for the event's participants.
	Plate          string   `json:"plate,omitempty"`
	Seats          int32    `json:"seats"`
	RiderUserIDs   []string `json:"rider_user_ids"`
	PendingUserIDs []string `json:"pending_user_ids"`
}

type eventVehiclesJSON struct {
	Vehicles             []eventVehicleJSON `json:"vehicles"`
	PlateEnabled         bool               `json:"plate_enabled"`
	SeatsEnabled         bool               `json:"seats_enabled"`
	CallerCanParticipate bool               `json:"caller_can_participate"`
	CallerCanManage      bool               `json:"caller_can_manage"`
}

func eventVehiclesFrom(v *corev1.EventVehicles) eventVehiclesJSON {
	out := eventVehiclesJSON{
		Vehicles: make([]eventVehicleJSON, 0, len(v.GetVehicles())), PlateEnabled: v.GetPlateEnabled(), SeatsEnabled: v.GetSeatsEnabled(),
		CallerCanParticipate: v.GetCallerCanParticipate(), CallerCanManage: v.GetCallerCanManage(),
	}
	for _, it := range v.GetVehicles() {
		out.Vehicles = append(out.Vehicles, eventVehicleJSON{
			ID: it.GetId(), UserID: it.GetUserId(), Make: it.GetMake(), Model: it.GetModel(), ModelYear: it.GetModelYear(),
			Color: it.GetColor(), Plate: it.GetPlate(), Seats: it.GetSeats(),
			RiderUserIDs:   append([]string{}, it.GetRiderUserIds()...),
			PendingUserIDs: append([]string{}, it.GetPendingUserIds()...),
		})
	}
	return out
}

// vehicleRef reads and checks the path's component and vehicle.
func vehicleRef(w http.ResponseWriter, r *http.Request) (ref *corev1.ComponentRef, vehicleID string, ok bool) {
	if ref, ok = componentRef(w, r); !ok {
		return nil, "", false
	}
	if vehicleID, ok = pathUUID(w, r, "vehicle_id"); !ok {
		return nil, "", false
	}
	return ref, vehicleID, true
}

func (h componentHandlers) listVehicles(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.vehicles().ListVehicles(ctx, &corev1.ListVehiclesRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVehiclesFrom(resp.GetVehicles()))
}

type eventVehicleBody struct {
	Make      string `json:"make"`
	Model     string `json:"model"`
	ModelYear int32  `json:"model_year"`
	Color     string `json:"color"`
	Plate     string `json:"plate"`
	Seats     int32  `json:"seats"`
}

func (h componentHandlers) setVehicle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b eventVehicleBody
	if !decode(w, r, &b) {
		return
	}
	if n := runes(b.Make); n < 1 || n > vehicleMakeMax || runes(b.Model) > vehicleMakeMax ||
		runes(b.Color) > vehicleColorMax || utf8.RuneCountInString(b.Plate) > vehiclePlateMax ||
		b.ModelYear < 0 || b.Seats < 0 || b.Seats > vehicleMaxSeats {
		badRequest(w, r)
		return
	}
	resp, err := h.vehicles().SetVehicle(ctx, &corev1.SetVehicleRequest{
		Ref: ref, Make: b.Make, Model: b.Model, ModelYear: b.ModelYear, Color: b.Color, Plate: b.Plate, Seats: b.Seats,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVehiclesFrom(resp.GetVehicles()))
}

func (h componentHandlers) removeVehicle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, vid, ok := vehicleRef(w, r)
	if !ok {
		return
	}
	resp, err := h.vehicles().RemoveVehicle(ctx, &corev1.RemoveVehicleRequest{Ref: ref, VehicleId: vid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVehiclesFrom(resp.GetVehicles()))
}

func (h componentHandlers) requestSeat(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, vid, ok := vehicleRef(w, r)
	if !ok {
		return
	}
	resp, err := h.vehicles().RequestSeat(ctx, &corev1.RequestSeatRequest{Ref: ref, VehicleId: vid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVehiclesFrom(resp.GetVehicles()))
}

func (h componentHandlers) approveSeat(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, vid, ok := vehicleRef(w, r)
	if !ok {
		return
	}
	uid, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	resp, err := h.vehicles().ApproveSeat(ctx, &corev1.ApproveSeatRequest{Ref: ref, VehicleId: vid, UserId: uid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVehiclesFrom(resp.GetVehicles()))
}

func (h componentHandlers) releaseSeat(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, vid, ok := vehicleRef(w, r)
	if !ok {
		return
	}
	uid, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	resp, err := h.vehicles().ReleaseSeat(ctx, &corev1.ReleaseSeatRequest{Ref: ref, VehicleId: vid, UserId: uid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVehiclesFrom(resp.GetVehicles()))
}
