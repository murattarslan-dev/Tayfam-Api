package rest

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const vocabularyService = "tayfam.core.v1.TayfaVocabularyService"

// fakeVocabulary is core's TayfaVocabularyService for the vocabulary tests.
type fakeVocabulary struct {
	got   proto.Message
	token string
	err   error
	calls int
}

func (f *fakeVocabulary) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func (f *fakeVocabulary) ListVocabulary(ctx context.Context, in *corev1.ListVocabularyRequest, _ ...grpc.CallOption) (*corev1.ListVocabularyResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListVocabularyResponse{Values: []*corev1.VocabularyValue{
		{Id: itemID, Value: "Levrek"},
		{Id: entryID, Value: "lavrak", MergedIntoId: itemID, MergedIntoValue: "Levrek"},
	}, CallerCanMerge: true}, f.err
}

func (f *fakeVocabulary) MergeVocabulary(ctx context.Context, in *corev1.MergeVocabularyRequest, _ ...grpc.CallOption) (*corev1.MergeVocabularyResponse, error) {
	f.rec(ctx, in)
	return &corev1.MergeVocabularyResponse{Value: &corev1.VocabularyValue{Id: entryID, Value: "lavrak", MergedIntoId: itemID, MergedIntoValue: "Levrek"}}, f.err
}

func (f *fakeVocabulary) UnmergeVocabulary(ctx context.Context, in *corev1.UnmergeVocabularyRequest, _ ...grpc.CallOption) (*corev1.UnmergeVocabularyResponse, error) {
	f.rec(ctx, in)
	return &corev1.UnmergeVocabularyResponse{}, f.err
}

const (
	vocabPath      = "/v1/tayfas/" + tayfaID + "/vocabulary/fish_species"
	vocabMergePath = vocabPath + "/" + entryID + "/merge"
)

func TestVocabularyRoutes(t *testing.T) {
	f := &fakeVocabulary{}
	h := componentRouter(fakeServices{vocabularyService: f})

	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", vocabPath, "", http.StatusOK, &corev1.ListVocabularyRequest{TayfaId: tayfaID, Namespace: "fish_species"}},
		{"GET", vocabPath + "?q=lev&merged=true", "", http.StatusOK,
			&corev1.ListVocabularyRequest{TayfaId: tayfaID, Namespace: "fish_species", Query: "lev", Merged: true}},
		{"POST", vocabMergePath, `{"target_id":"` + itemID + `"}`, http.StatusOK,
			&corev1.MergeVocabularyRequest{TayfaId: tayfaID, Namespace: "fish_species", VocabularyId: entryID, TargetId: itemID}},
		{"DELETE", vocabMergePath, "", http.StatusNoContent,
			&corev1.UnmergeVocabularyRequest{TayfaId: tayfaID, Namespace: "fish_species", VocabularyId: entryID}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s %s: core'a giden %v (%q), beklenen %v", tc.method, tc.path, f.got, f.token, tc.want)
		}
	}

	w := as(h, "GET", vocabPath, "")
	l := decodeBody[vocabularyListJSON](t, w)
	if len(l.Values) != 2 || !l.CallerCanMerge || l.Values[0].ID != itemID || l.Values[0].Value != "Levrek" ||
		l.Values[1].MergedIntoID != itemID || l.Values[1].MergedIntoValue != "Levrek" {
		t.Fatalf("liste: %+v", l)
	}
	// A value that is not merged has no merge keys.
	if want := `{"id":"` + itemID + `","value":"Levrek"}`; !strings.Contains(w.Body.String(), want) {
		t.Fatalf("birleştirilmemiş değer: %s", w.Body)
	}
	w = as(h, "POST", vocabMergePath, `{"target_id":"`+itemID+`"}`)
	if v := decodeBody[vocabularyValueJSON](t, w); v.ID != entryID || v.MergedIntoID != itemID {
		t.Fatalf("birleştirme yanıtı: %+v", v)
	}
}

func TestVocabularyRoutesValidateInput(t *testing.T) {
	f := &fakeVocabulary{}
	h := componentRouter(fakeServices{vocabularyService: f})
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/v1/tayfas/x/vocabulary/fish_species", ""},
		{"GET", "/v1/tayfas/" + tayfaID + "/vocabulary/Fish", ""},
		{"GET", vocabPath + "?merged=evet", ""},
		{"GET", vocabPath + "?q=" + strings.Repeat("a", 61), ""},
		{"POST", vocabPath + "/x/merge", `{"target_id":"` + itemID + `"}`},
		{"POST", vocabMergePath, `{"target_id":"x"}`},
		{"POST", vocabMergePath, `{}`},
		{"POST", vocabMergePath, `{"target_id":"` + itemID + `","value":"Levrek"}`},
		{"DELETE", vocabPath + "/x/merge", ""},
	} {
		if w := as(h, rt.method, rt.path, rt.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s %s: %d %s", rt.method, rt.path, rt.body, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call; a write without the client header is refused.
	for _, rt := range []struct{ method, path string }{
		{"GET", vocabPath}, {"POST", vocabMergePath}, {"DELETE", vocabMergePath},
	} {
		if w := do(h, rt.method, rt.path, `{}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", rt.method, rt.path, w.Code)
		}
		if rt.method == "GET" {
			continue
		}
		if w := do(h, rt.method, rt.path, `{}`, map[string]string{"Authorization": "Bearer T"}); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
			t.Fatalf("istemci başlıksız %s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestVocabularyErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_VOCABULARY_NOT_FOUND":  {codes.NotFound, http.StatusNotFound, "vocabulary-not-found"},
		"ERROR_REASON_VOCABULARY_MERGED":     {codes.FailedPrecondition, http.StatusConflict, "vocabulary-merged"},
		"ERROR_REASON_VOCABULARY_NOT_MERGED": {codes.FailedPrecondition, http.StatusConflict, "vocabulary-not-merged"},
		"ERROR_REASON_PERMISSION_DENIED":     {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{vocabularyService: &fakeVocabulary{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", vocabMergePath, `{"target_id":"`+itemID+`"}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
