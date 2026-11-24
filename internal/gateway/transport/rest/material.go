package rest

import (
	"context"
	"net/http"
	"strings"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// Materyal kütüphanesi (issue #261; ADR-0040 §3). core decides everything;
// the gateway only keeps junk out early. Titles and links are content: they
// travel in bodies only and are never logged. Neither the gateway nor core
// ever opens a link.

const (
	materialsPath = "/v1/tayfas/{tayfa_id}/materials"
	materialPath  = materialsPath + "/{material_id}"
	// maxMaterialTitleBytes and maxMaterialURLBytes bound the texts before
	// core folds and normalizes them (80 and 500 characters).
	maxMaterialTitleBytes = 1 << 10
	maxMaterialURLBytes   = 2 << 10
	// maxMaterialPhotos mirrors core's bound.
	maxMaterialPhotos = 3
)

// registerTayfaMaterials is the material library's line in the registry
// (componentRoutes). Its routes are the tayfa's, not an event's.
func registerTayfaMaterials(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc("GET "+materialsPath, h.authed(h.listMaterials))
	mux.HandleFunc("POST "+materialsPath, h.authed(h.createMaterial))
	mux.HandleFunc("GET "+materialPath, h.authed(h.getMaterial))
	mux.HandleFunc("PUT "+materialPath, h.authed(h.updateMaterial))
	mux.HandleFunc("DELETE "+materialPath, h.authed(h.deleteMaterial))
}

func (h componentHandlers) materials() corev1.TayfaMaterialServiceClient {
	return corev1.NewTayfaMaterialServiceClient(h.conn)
}

var materialKinds = newEnum(map[string]corev1.MaterialKind{
	"link": corev1.MaterialKind_MATERIAL_KIND_LINK, "note_photo": corev1.MaterialKind_MATERIAL_KIND_NOTE_PHOTO,
})

type materialJSON struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	Title           string    `json:"title"`
	URL             string    `json:"url,omitempty"`
	Domain          string    `json:"domain,omitempty"`
	PhotoUploadIDs  []string  `json:"photo_upload_ids,omitempty"`
	SubjectKey      string    `json:"subject_key"`
	TopicKey        string    `json:"topic_key,omitempty"`
	AuthorUserID    string    `json:"author_user_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	CallerCanEdit   bool      `json:"caller_can_edit"`
	CallerCanDelete bool      `json:"caller_can_delete"`
}

type materialListJSON struct {
	Materials     []materialJSON `json:"materials"`
	NextPageToken string         `json:"next_page_token,omitempty"`
	CallerCanAdd  bool           `json:"caller_can_add"`
}

func materialFrom(m *corev1.Material) materialJSON {
	return materialJSON{
		ID: m.GetId(), Kind: materialKinds.name(m.GetKind()), Title: m.GetTitle(), URL: m.GetUrl(), Domain: m.GetDomain(),
		PhotoUploadIDs: m.GetPhotoUploadIds(), SubjectKey: m.GetSubjectKey(), TopicKey: m.GetTopicKey(),
		AuthorUserID: m.GetAuthorId(), CreatedAt: m.GetCreateTime().AsTime(), UpdatedAt: m.GetUpdateTime().AsTime(),
		CallerCanEdit: m.GetCallerCanEdit(), CallerCanDelete: m.GetCallerCanDelete(),
	}
}

// materialFieldsOK checks what the gateway can of a material's fields.
func materialFieldsOK(title, url string, photos []string, subject, topic string) bool {
	if strings.TrimSpace(title) == "" || len(title) > maxMaterialTitleBytes || len(url) > maxMaterialURLBytes ||
		len(photos) > maxMaterialPhotos || subject == "" || !studyKeyOK(subject) || !studyKeyOK(topic) {
		return false
	}
	for _, p := range photos {
		if !validUUID(p) {
			return false
		}
	}
	return true
}

func (h componentHandlers) listMaterials(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	kind, ok := materialKinds.valueOrUnspecified(q.Get("kind"))
	subject, topic := q.Get("subject_key"), q.Get("topic_key")
	if !ok || !studyKeyOK(subject) || !studyKeyOK(topic) {
		badRequest(w, r)
		return
	}
	var mine bool
	switch q.Get("mine") {
	case "", "false":
	case "true":
		mine = true
	default:
		badRequest(w, r)
		return
	}
	resp, err := h.materials().ListMaterials(ctx, &corev1.ListMaterialsRequest{
		TayfaId: tf, Kind: kind, SubjectKey: subject, TopicKey: topic, Mine: mine, PageSize: size, PageToken: token,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := materialListJSON{
		Materials: make([]materialJSON, 0, len(resp.GetMaterials())), NextPageToken: resp.GetNextPageToken(),
		CallerCanAdd: resp.GetCallerCanAdd(),
	}
	for _, m := range resp.GetMaterials() {
		out.Materials = append(out.Materials, materialFrom(m))
	}
	writeJSON(w, http.StatusOK, out)
}

type materialCreateBody struct {
	Kind            string   `json:"kind"`
	Title           string   `json:"title"`
	URL             string   `json:"url"`
	PhotoUploadIDs  []string `json:"photo_upload_ids"`
	SubjectKey      string   `json:"subject_key"`
	TopicKey        string   `json:"topic_key"`
	ClientRequestID string   `json:"client_request_id"`
}

func (h componentHandlers) createMaterial(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b materialCreateBody
	if !decode(w, r, &b) {
		return
	}
	kind, ok := materialKinds.value(b.Kind)
	if !ok || !materialFieldsOK(b.Title, b.URL, b.PhotoUploadIDs, b.SubjectKey, b.TopicKey) || !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.materials().CreateMaterial(ctx, &corev1.CreateMaterialRequest{
		TayfaId: tf, Kind: kind, Title: b.Title, Url: b.URL, PhotoUploadIds: b.PhotoUploadIDs,
		SubjectKey: b.SubjectKey, TopicKey: b.TopicKey, ClientRequestId: b.ClientRequestID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, materialFrom(resp.GetMaterial()))
}

// pathMaterial reads and checks the path's tayfa and material.
func pathMaterial(w http.ResponseWriter, r *http.Request) (tayfaID, materialID string, ok bool) {
	if tayfaID, ok = pathUUID(w, r, "tayfa_id"); !ok {
		return "", "", false
	}
	if materialID, ok = pathUUID(w, r, "material_id"); !ok {
		return "", "", false
	}
	return tayfaID, materialID, true
}

func (h componentHandlers) getMaterial(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, m, ok := pathMaterial(w, r)
	if !ok {
		return
	}
	resp, err := h.materials().GetMaterial(ctx, &corev1.GetMaterialRequest{TayfaId: tf, MaterialId: m})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, materialFrom(resp.GetMaterial()))
}

type materialUpdateBody struct {
	Title          string   `json:"title"`
	URL            string   `json:"url"`
	PhotoUploadIDs []string `json:"photo_upload_ids"`
	SubjectKey     string   `json:"subject_key"`
	TopicKey       string   `json:"topic_key"`
}

func (h componentHandlers) updateMaterial(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, m, ok := pathMaterial(w, r)
	if !ok {
		return
	}
	var b materialUpdateBody
	if !decode(w, r, &b) {
		return
	}
	if !materialFieldsOK(b.Title, b.URL, b.PhotoUploadIDs, b.SubjectKey, b.TopicKey) {
		badRequest(w, r)
		return
	}
	resp, err := h.materials().UpdateMaterial(ctx, &corev1.UpdateMaterialRequest{
		TayfaId: tf, MaterialId: m, Title: b.Title, Url: b.URL, PhotoUploadIds: b.PhotoUploadIDs,
		SubjectKey: b.SubjectKey, TopicKey: b.TopicKey,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, materialFrom(resp.GetMaterial()))
}

func (h componentHandlers) deleteMaterial(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, m, ok := pathMaterial(w, r)
	if !ok {
		return
	}
	if _, err := h.materials().DeleteMaterial(ctx, &corev1.DeleteMaterialRequest{TayfaId: tf, MaterialId: m}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
