package rest

import (
	"context"
	"net/http"
	"unicode/utf8"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The tayfa vocabulary (issue #244; ADR-0037 §4). core decides everything;
// the gateway only keeps junk out early. Values are content: they travel
// in bodies and the search query only, and are never logged.

// maxVocabularyQuery bounds the search text (vocabulary.MaxValueLength).
const maxVocabularyQuery = 60

const vocabularyPath = "/v1/tayfas/{tayfa_id}/vocabulary/{namespace}"

// registerTayfaVocabulary is the tayfa vocabulary's line in the registry
// (componentRoutes). Its routes are the tayfa's, not an event's.
func registerTayfaVocabulary(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc("GET "+vocabularyPath, h.authed(h.listVocabulary))
	mux.HandleFunc("POST "+vocabularyPath+"/{vocabulary_id}/merge", h.authed(h.mergeVocabulary))
	mux.HandleFunc("DELETE "+vocabularyPath+"/{vocabulary_id}/merge", h.authed(h.unmergeVocabulary))
}

func (h componentHandlers) vocabulary() corev1.TayfaVocabularyServiceClient {
	return corev1.NewTayfaVocabularyServiceClient(h.conn)
}

type vocabularyValueJSON struct {
	ID    string `json:"id"`
	Value string `json:"value"`
	// Set only on a merged value.
	MergedIntoID    string `json:"merged_into_id,omitempty"`
	MergedIntoValue string `json:"merged_into_value,omitempty"`
}

type vocabularyListJSON struct {
	Values         []vocabularyValueJSON `json:"values"`
	CallerCanMerge bool                  `json:"caller_can_merge"`
}

func vocabularyValueFrom(v *corev1.VocabularyValue) vocabularyValueJSON {
	return vocabularyValueJSON{
		ID: v.GetId(), Value: v.GetValue(), MergedIntoID: v.GetMergedIntoId(), MergedIntoValue: v.GetMergedIntoValue(),
	}
}

// pathVocabulary reads and checks the path's tayfa and namespace.
func pathVocabulary(w http.ResponseWriter, r *http.Request) (tayfaID, namespace string, ok bool) {
	if tayfaID, ok = pathUUID(w, r, "tayfa_id"); !ok {
		return "", "", false
	}
	namespace = r.PathValue("namespace")
	if !catalogKeyPattern.MatchString(namespace) {
		badRequest(w, r)
		return "", "", false
	}
	return tayfaID, namespace, true
}

func (h componentHandlers) listVocabulary(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ns, ok := pathVocabulary(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	query := q.Get("q")
	var merged bool
	switch q.Get("merged") {
	case "", "false":
	case "true":
		merged = true
	default:
		badRequest(w, r)
		return
	}
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > maxVocabularyQuery {
		badRequest(w, r)
		return
	}
	resp, err := h.vocabulary().ListVocabulary(ctx, &corev1.ListVocabularyRequest{TayfaId: tf, Namespace: ns, Query: query, Merged: merged})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := vocabularyListJSON{Values: make([]vocabularyValueJSON, 0, len(resp.GetValues())), CallerCanMerge: resp.GetCallerCanMerge()}
	for _, v := range resp.GetValues() {
		out.Values = append(out.Values, vocabularyValueFrom(v))
	}
	writeJSON(w, http.StatusOK, out)
}

type vocabularyMergeBody struct {
	TargetID string `json:"target_id"`
}

func (h componentHandlers) mergeVocabulary(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ns, ok := pathVocabulary(w, r)
	if !ok {
		return
	}
	v, ok := pathUUID(w, r, "vocabulary_id")
	if !ok {
		return
	}
	var b vocabularyMergeBody
	if !decode(w, r, &b) {
		return
	}
	if !validUUID(b.TargetID) {
		badRequest(w, r)
		return
	}
	resp, err := h.vocabulary().MergeVocabulary(ctx, &corev1.MergeVocabularyRequest{
		TayfaId: tf, Namespace: ns, VocabularyId: v, TargetId: b.TargetID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, vocabularyValueFrom(resp.GetValue()))
}

func (h componentHandlers) unmergeVocabulary(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ns, ok := pathVocabulary(w, r)
	if !ok {
		return
	}
	v, ok := pathUUID(w, r, "vocabulary_id")
	if !ok {
		return
	}
	if _, err := h.vocabulary().UnmergeVocabulary(ctx, &corev1.UnmergeVocabularyRequest{TayfaId: tf, Namespace: ns, VocabularyId: v}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
