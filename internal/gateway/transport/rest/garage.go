package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// Üye garajı (issue #272; ADR-0040 §4). The garage is the person's
// (/v1/me/garage); a tayfa shows the garages its members chose to show
// (/v1/tayfas/{tayfa_id}/garage). core decides everything; the gateway
// only keeps junk out early. There is no plate anywhere here. Texts travel
// in bodies only and are never logged.

const (
	myGaragePath          = "/v1/me/garage"
	myGarageVehiclePath   = myGaragePath + "/{garage_vehicle_id}"
	tayfaGaragePath       = "/v1/tayfas/{tayfa_id}/garage"
	tayfaGarageVisibility = tayfaGaragePath + "/visibility"
	tayfaGarageVehicle    = tayfaGaragePath + "/{garage_vehicle_id}"
	// maxGarageTextBytes bounds a text before core folds it (at most 200
	// characters).
	maxGarageTextBytes = 1 << 10
	// maxGaragePhotos mirrors core's bound.
	maxGaragePhotos = 3
)

// registerMemberGarage is the member garage's line in the registry
// (componentRoutes).
func registerMemberGarage(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc("GET "+myGaragePath, h.authed(h.listMyGarage))
	mux.HandleFunc("POST "+myGaragePath, h.authed(h.createGarageVehicle))
	mux.HandleFunc("PUT "+myGarageVehiclePath, h.authed(h.updateGarageVehicle))
	mux.HandleFunc("DELETE "+myGarageVehiclePath, h.authed(h.deleteGarageVehicle))
	mux.HandleFunc("GET "+tayfaGaragePath, h.authed(h.listTayfaGarage))
	mux.HandleFunc("PUT "+tayfaGarageVisibility, h.authed(h.setGarageVisibility))
	mux.HandleFunc("DELETE "+tayfaGarageVehicle, h.authed(h.removeTayfaGarageVehicle))
}

func (h componentHandlers) garage() corev1.MemberGarageServiceClient {
	return corev1.NewMemberGarageServiceClient(h.conn)
}

type garageVehicleJSON struct {
	ID              string    `json:"id"`
	OwnerUserID     string    `json:"owner_user_id"`
	Make            string    `json:"make"`
	Model           string    `json:"model,omitempty"`
	ModelYear       int32     `json:"model_year,omitempty"`
	Color           string    `json:"color,omitempty"`
	Note            string    `json:"note,omitempty"`
	PhotoUploadIDs  []string  `json:"photo_upload_ids"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	CallerCanEdit   bool      `json:"caller_can_edit"`
	CallerCanRemove bool      `json:"caller_can_remove"`
}

func garageVehicleFrom(v *corev1.GarageVehicle) garageVehicleJSON {
	return garageVehicleJSON{
		ID: v.GetId(), OwnerUserID: v.GetOwnerId(), Make: v.GetMake(), Model: v.GetModel(), ModelYear: v.GetModelYear(),
		Color: v.GetColor(), Note: v.GetNote(), PhotoUploadIDs: append([]string{}, v.GetPhotoUploadIds()...),
		CreatedAt: v.GetCreateTime().AsTime(), UpdatedAt: v.GetUpdateTime().AsTime(),
		CallerCanEdit: v.GetCallerCanEdit(), CallerCanRemove: v.GetCallerCanRemove(),
	}
}

func garageVehiclesFrom(vs []*corev1.GarageVehicle) []garageVehicleJSON {
	out := make([]garageVehicleJSON, 0, len(vs))
	for _, v := range vs {
		out = append(out, garageVehicleFrom(v))
	}
	return out
}

type myGarageJSON struct {
	Vehicles     []garageVehicleJSON `json:"vehicles"`
	CallerCanAdd bool                `json:"caller_can_add"`
}

type tayfaGarageJSON struct {
	Vehicles      []garageVehicleJSON `json:"vehicles"`
	NextPageToken string              `json:"next_page_token,omitempty"`
	CallerVisible bool                `json:"caller_visible"`
}

type garageVehicleBody struct {
	Make           string   `json:"make"`
	Model          string   `json:"model"`
	ModelYear      int32    `json:"model_year"`
	Color          string   `json:"color"`
	Note           string   `json:"note"`
	PhotoUploadIDs []string `json:"photo_upload_ids"`
}

// input checks what the gateway can of a vehicle and returns core's input.
func (b garageVehicleBody) input() (*corev1.GarageVehicleInput, bool) {
	if runes(b.Make) < 1 || len(b.Make) > maxGarageTextBytes || len(b.Model) > maxGarageTextBytes ||
		len(b.Color) > maxGarageTextBytes || len(b.Note) > maxGarageTextBytes || b.ModelYear < 0 ||
		len(b.PhotoUploadIDs) > maxGaragePhotos {
		return nil, false
	}
	for _, p := range b.PhotoUploadIDs {
		if !validUUID(p) {
			return nil, false
		}
	}
	return &corev1.GarageVehicleInput{
		Make: b.Make, Model: b.Model, ModelYear: b.ModelYear, Color: b.Color, Note: b.Note, PhotoUploadIds: b.PhotoUploadIDs,
	}, true
}

func (h componentHandlers) listMyGarage(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.garage().ListMyGarageVehicles(ctx, &corev1.ListMyGarageVehiclesRequest{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, myGarageJSON{Vehicles: garageVehiclesFrom(resp.GetVehicles()), CallerCanAdd: resp.GetCallerCanAdd()})
}

type garageVehicleCreateBody struct {
	garageVehicleBody
	ClientRequestID string `json:"client_request_id"`
}

func (h componentHandlers) createGarageVehicle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b garageVehicleCreateBody
	if !decode(w, r, &b) {
		return
	}
	in, ok := b.input()
	if !ok || !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.garage().CreateGarageVehicle(ctx, &corev1.CreateGarageVehicleRequest{Vehicle: in, ClientRequestId: b.ClientRequestID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, garageVehicleFrom(resp.GetVehicle()))
}

func (h componentHandlers) updateGarageVehicle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	v, ok := pathUUID(w, r, "garage_vehicle_id")
	if !ok {
		return
	}
	var b garageVehicleBody
	if !decode(w, r, &b) {
		return
	}
	in, ok := b.input()
	if !ok {
		badRequest(w, r)
		return
	}
	resp, err := h.garage().UpdateGarageVehicle(ctx, &corev1.UpdateGarageVehicleRequest{VehicleId: v, Vehicle: in})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, garageVehicleFrom(resp.GetVehicle()))
}

func (h componentHandlers) deleteGarageVehicle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	v, ok := pathUUID(w, r, "garage_vehicle_id")
	if !ok {
		return
	}
	if _, err := h.garage().DeleteGarageVehicle(ctx, &corev1.DeleteGarageVehicleRequest{VehicleId: v}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h componentHandlers) listTayfaGarage(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	owner := r.URL.Query().Get("owner_user_id")
	if owner != "" && !validUUID(owner) {
		badRequest(w, r)
		return
	}
	resp, err := h.garage().ListTayfaGarage(ctx, &corev1.ListTayfaGarageRequest{TayfaId: tf, OwnerId: owner, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tayfaGarageJSON{
		Vehicles: garageVehiclesFrom(resp.GetVehicles()), NextPageToken: resp.GetNextPageToken(), CallerVisible: resp.GetCallerVisible(),
	})
}

type garageVisibilityJSON struct {
	Visible *bool `json:"visible"`
}

func (h componentHandlers) setGarageVisibility(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b garageVisibilityJSON
	if !decode(w, r, &b) {
		return
	}
	if b.Visible == nil {
		badRequest(w, r)
		return
	}
	resp, err := h.garage().SetGarageVisibility(ctx, &corev1.SetGarageVisibilityRequest{TayfaId: tf, Visible: *b.Visible})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	visible := resp.GetVisible()
	writeJSON(w, http.StatusOK, garageVisibilityJSON{Visible: &visible})
}

func (h componentHandlers) removeTayfaGarageVehicle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	v, ok := pathUUID(w, r, "garage_vehicle_id")
	if !ok {
		return
	}
	if _, err := h.garage().RemoveTayfaGarageVehicle(ctx, &corev1.RemoveTayfaGarageVehicleRequest{TayfaId: tf, VehicleId: v}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
