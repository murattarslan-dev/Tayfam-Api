package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The log component (kind "log", issue #243; ADR-0025 §4). core decides
// everything; the gateway only keeps junk out early. Values and locations
// travel in bodies only and are never logged.

// maxLogFields bounds an entry's values (definition.MaxLogFields).
const maxLogFields = 8

var logVisibilities = newEnum(map[string]corev1.LogLocationVisibility{
	"self":         corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_SELF,
	"participants": corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_PARTICIPANTS,
	"tayfa":        corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_TAYFA,
})

// registerLogComponent is the log component's line in the registry
// (componentRoutes).
func registerLogComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "entries"), h.authed(h.listLogEntries))
	mux.HandleFunc(componentPath("POST", "entries"), h.authed(h.addLogEntry))
	mux.HandleFunc(componentPath("DELETE", "entries/{entry_id}"), h.authed(h.removeLogEntry))
}

func (h componentHandlers) logbook() corev1.LogComponentServiceClient {
	return corev1.NewLogComponentServiceClient(h.conn)
}

type logEntryJSON struct {
	ID     string                     `json:"id"`
	UserID string                     `json:"user_id"`
	Values map[string]json.RawMessage `json:"values"`
	// Location is left out when there is none or core does not give it to
	// the caller.
	Location *latLngJSON `json:"location,omitempty"`
	// LocationVisibility is given on the caller's own entries that have a
	// location.
	LocationVisibility string    `json:"location_visibility,omitempty"`
	PhotoUploadID      string    `json:"photo_upload_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	CallerCanRemove    bool      `json:"caller_can_remove"`
	// VocabularyIDs are the tayfa vocabulary values of the fields bound to
	// the vocabulary (issue #244), by field key.
	VocabularyIDs map[string]string `json:"vocabulary_ids,omitempty"`
}

type logEntryListJSON struct {
	Entries         []logEntryJSON `json:"entries"`
	NextPageToken   string         `json:"next_page_token,omitempty"`
	LocationEnabled bool           `json:"location_enabled"`
	LocationDefault string         `json:"location_default,omitempty"`
	MaxEntries      int32          `json:"max_entries"`
	CallerCanAdd    bool           `json:"caller_can_add"`
}

func logEntryFrom(e *corev1.LogEntry) logEntryJSON {
	values := eventFieldsFrom(e.GetValues())
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	return logEntryJSON{
		ID: e.GetId(), UserID: e.GetUserId(), Values: values, Location: latLngFrom(e.GetLocation()),
		LocationVisibility: optName(logVisibilities, e.GetLocationVisibility()), PhotoUploadID: e.GetPhotoUploadId(),
		CreatedAt: e.GetCreateTime().AsTime(), CallerCanRemove: e.GetCallerCanRemove(), VocabularyIDs: e.GetVocabularyIds(),
	}
}

func (h componentHandlers) listLogEntries(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.logbook().ListLogEntries(ctx, &corev1.ListLogEntriesRequest{Ref: ref, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := logEntryListJSON{
		Entries: make([]logEntryJSON, 0, len(resp.GetEntries())), NextPageToken: resp.GetNextPageToken(),
		LocationEnabled: resp.GetLocationEnabled(), LocationDefault: optName(logVisibilities, resp.GetLocationDefault()),
		MaxEntries: resp.GetMaxEntries(), CallerCanAdd: resp.GetCallerCanAdd(),
	}
	for _, e := range resp.GetEntries() {
		out.Entries = append(out.Entries, logEntryFrom(e))
	}
	writeJSON(w, http.StatusOK, out)
}

type logEntryBody struct {
	Values             map[string]json.RawMessage `json:"values"`
	Location           *latLngJSON                `json:"location"`
	LocationVisibility string                     `json:"location_visibility"`
	PhotoUploadID      string                     `json:"photo_upload_id"`
	ClientRequestID    string                     `json:"client_request_id"`
	VocabularyIDs      map[string]string          `json:"vocabulary_ids"`
}

// vocabularyIDsOK checks an entry's vocabulary value IDs by field key.
func vocabularyIDsOK(in map[string]string) bool {
	if len(in) > maxLogFields {
		return false
	}
	for k, v := range in {
		if !catalogKeyPattern.MatchString(k) || !validUUID(v) {
			return false
		}
	}
	return true
}

// addLogEntry passes the entry's location to core in the body only, like
// checkIn.
func (h componentHandlers) addLogEntry(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b logEntryBody
	if !decode(w, r, &b) {
		return
	}
	values, okValues := eventFields(b.Values)
	vis, okVis := logVisibilities.valueOrUnspecified(b.LocationVisibility)
	if !okValues || len(b.Values) > maxLogFields || !okVis || !pointOK(b.Location) || !validUUID(b.ClientRequestID) ||
		(b.PhotoUploadID != "" && !validUUID(b.PhotoUploadID)) || !vocabularyIDsOK(b.VocabularyIDs) {
		badRequest(w, r)
		return
	}
	resp, err := h.logbook().AddLogEntry(ctx, &corev1.AddLogEntryRequest{
		Ref: ref, Values: values, Location: b.Location.proto(), LocationVisibility: vis,
		PhotoUploadId: b.PhotoUploadID, ClientRequestId: b.ClientRequestID, VocabularyIds: b.VocabularyIDs,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, logEntryFrom(resp.GetEntry()))
}

func (h componentHandlers) removeLogEntry(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	entry, ok := pathUUID(w, r, "entry_id")
	if !ok {
		return
	}
	if _, err := h.logbook().RemoveLogEntry(ctx, &corev1.RemoveLogEntryRequest{Ref: ref, EntryId: entry}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
