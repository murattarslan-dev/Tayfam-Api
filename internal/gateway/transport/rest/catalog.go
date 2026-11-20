package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
)

// The event type catalog (issue #237; ADR-0025 §8). Both routes need the
// user's full access token: the catalog is the same for everyone, but it
// is not an open endpoint. core checks the token again.
//
// The catalog's version is its ETag. A client that sends the version it
// holds in If-None-Match gets 304 without a body while nothing changed.

// CatalogClient is core's CatalogService.
type CatalogClient interface {
	GetCatalog(ctx context.Context, in *corev1.GetCatalogRequest, opts ...grpc.CallOption) (*corev1.GetCatalogResponse, error)
	GetEventTypeVersion(ctx context.Context, in *corev1.GetEventTypeVersionRequest, opts ...grpc.CallOption) (*corev1.GetEventTypeVersionResponse, error)
}

func registerCatalog(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("GET /v1/catalog", a(h.getCatalog))
	mux.HandleFunc("GET /v1/catalog/event-types/{key}/versions/{version}", a(h.getEventTypeVersion))
}

var catalogStatuses = newEnum(map[string]corev1.CatalogStatus{
	"active": corev1.CatalogStatus_CATALOG_STATUS_ACTIVE, "retired": corev1.CatalogStatus_CATALOG_STATUS_RETIRED,
})

// catalogKeyPattern is the form of a catalog key (ADR-0025 §2).
var catalogKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)

// maxTypeVersionDigits bounds the version in the path before it is parsed.
const maxTypeVersionDigits = 9

type catalogInterestJSON struct {
	Key            string            `json:"key"`
	Position       int32             `json:"position"`
	Labels         map[string]string `json:"labels"`
	Icon           string            `json:"icon"`
	Status         string            `json:"status"`
	DefaultWidgets []string          `json:"default_widgets"`
}

type catalogEventTypeJSON struct {
	Key          string            `json:"key"`
	InterestKeys []string          `json:"interest_keys"`
	Labels       map[string]string `json:"labels"`
	Icon         string            `json:"icon"`
	Modes        []string          `json:"modes"`
	Status       string            `json:"status"`
	Version      int32             `json:"version"`
	// Definition is passed on as core validated it.
	Definition json.RawMessage `json:"definition"`
}

// catalogStudyTagJSON is an entry of the study tag list (issue #259);
// parent_key is left out for a level.
type catalogStudyTagJSON struct {
	Key       string            `json:"key"`
	ParentKey string            `json:"parent_key,omitempty"`
	Position  int32             `json:"position"`
	Labels    map[string]string `json:"labels"`
	Status    string            `json:"status"`
}

type catalogJSON struct {
	CatalogVersion int64                  `json:"catalog_version"`
	Interests      []catalogInterestJSON  `json:"interests"`
	EventTypes     []catalogEventTypeJSON `json:"event_types"`
	StudyTags      []catalogStudyTagJSON  `json:"study_tags"`
}

// catalogEventTypeFrom converts a type; ok is false when core's definition
// is not a JSON object (it never should be).
func catalogEventTypeFrom(t *corev1.CatalogEventType) (catalogEventTypeJSON, bool) {
	def := []byte(t.GetDefinition())
	if !json.Valid(def) || !strings.HasPrefix(t.GetDefinition(), "{") {
		return catalogEventTypeJSON{}, false
	}
	modes := make([]string, 0, len(t.GetModes()))
	for _, m := range t.GetModes() {
		modes = append(modes, eventModes.name(m))
	}
	return catalogEventTypeJSON{
		Key: t.GetKey(), InterestKeys: nonNil(t.GetInterestKeys()), Labels: nonNilMap(t.GetLabels()), Icon: t.GetIcon(),
		Modes: modes, Status: catalogStatuses.name(t.GetStatus()), Version: t.GetVersion(), Definition: def,
	}, true
}

func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonNilMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	return in
}

// catalogETag is the entity tag of a catalog version.
func catalogETag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// knownCatalogVersion reads the catalog version from If-None-Match; 0 when
// the header is missing or is not one of this endpoint's tags.
func knownCatalogVersion(r *http.Request) int64 {
	h := r.Header.Get("If-None-Match")
	if h == "" || len(h) > 128 {
		return 0
	}
	for _, tag := range strings.Split(h, ",") {
		tag = strings.TrimPrefix(strings.TrimSpace(tag), "W/")
		if len(tag) < 3 || tag[0] != '"' || tag[len(tag)-1] != '"' {
			continue
		}
		if v, err := strconv.ParseInt(tag[1:len(tag)-1], 10, 64); err == nil && v > 0 {
			return v
		}
	}
	return 0
}

// writeCatalogJSON is writeJSON with the catalog's caching headers: the
// client may keep the answer but must ask again with If-None-Match.
func writeCatalogJSON(w http.ResponseWriter, etag string, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-cache")
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func (h coreHandlers) getCatalog(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Catalog.GetCatalog(ctx, &corev1.GetCatalogRequest{KnownVersion: knownCatalogVersion(r)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	etag := catalogETag(resp.GetCatalogVersion())
	if resp.GetNotModified() {
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	out := catalogJSON{
		CatalogVersion: resp.GetCatalogVersion(),
		Interests:      make([]catalogInterestJSON, 0, len(resp.GetInterests())),
		EventTypes:     make([]catalogEventTypeJSON, 0, len(resp.GetEventTypes())),
		StudyTags:      make([]catalogStudyTagJSON, 0, len(resp.GetStudyTags())),
	}
	for _, in := range resp.GetInterests() {
		out.Interests = append(out.Interests, catalogInterestJSON{
			Key: in.GetKey(), Position: in.GetPosition(), Labels: nonNilMap(in.GetLabels()), Icon: in.GetIcon(),
			Status: catalogStatuses.name(in.GetStatus()), DefaultWidgets: nonNil(in.GetDefaultWidgets()),
		})
	}
	for _, t := range resp.GetEventTypes() {
		j, ok := catalogEventTypeFrom(t)
		if !ok {
			h.d.Log.ErrorContext(r.Context(), "katalog tanımı geçerli JSON değil")
			httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal Server Error", "")
			return
		}
		out.EventTypes = append(out.EventTypes, j)
	}
	for _, t := range resp.GetStudyTags() {
		out.StudyTags = append(out.StudyTags, catalogStudyTagJSON{
			Key: t.GetKey(), ParentKey: t.GetParentKey(), Position: t.GetPosition(), Labels: nonNilMap(t.GetLabels()),
			Status: catalogStatuses.name(t.GetStatus()),
		})
	}
	writeCatalogJSON(w, etag, out)
}

func (h coreHandlers) getEventTypeVersion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	key, raw := r.PathValue("key"), r.PathValue("version")
	version, err := strconv.ParseInt(raw, 10, 32)
	// Only plain digits: no sign, no leading zero.
	if !catalogKeyPattern.MatchString(key) || err != nil || version < 1 || len(raw) > maxTypeVersionDigits || raw != strconv.FormatInt(version, 10) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Catalog.GetEventTypeVersion(ctx, &corev1.GetEventTypeVersionRequest{Key: key, Version: int32(version)}) //nolint:gosec // parsed with bitSize 32
	if err != nil {
		h.fail(w, r, err)
		return
	}
	j, ok := catalogEventTypeFrom(resp.GetEventType())
	if !ok {
		h.d.Log.ErrorContext(r.Context(), "katalog tanımı geçerli JSON değil")
		httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal Server Error", "")
		return
	}
	// A type version never changes.
	writeCatalogJSON(w, "", j)
}
