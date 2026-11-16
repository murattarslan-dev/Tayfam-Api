package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// Friend suggestions and profiling settings (issue #208, ADR-0030).

func (f *fakeCore) ListFriendSuggestions(ctx context.Context, in *corev1.ListFriendSuggestionsRequest, _ ...grpc.CallOption) (*corev1.ListFriendSuggestionsResponse, error) {
	return &corev1.ListFriendSuggestionsResponse{Suggestions: []*corev1.FriendSuggestion{
		{UserId: userID, DisplayName: "Ece", SuggestTime: timestamppb.New(time.Unix(3, 0))},
	}}, f.record(ctx, in)
}
func (f *fakeCore) DismissFriendSuggestion(ctx context.Context, in *corev1.DismissFriendSuggestionRequest, _ ...grpc.CallOption) (*corev1.DismissFriendSuggestionResponse, error) {
	return &corev1.DismissFriendSuggestionResponse{}, f.record(ctx, in)
}
func (f *fakeCore) GetSocialSettings(ctx context.Context, in *corev1.GetSocialSettingsRequest, _ ...grpc.CallOption) (*corev1.GetSocialSettingsResponse, error) {
	return &corev1.GetSocialSettingsResponse{Settings: &corev1.SocialSettings{ProfilingEnabled: true}}, f.record(ctx, in)
}
func (f *fakeCore) UpdateSocialSettings(ctx context.Context, in *corev1.UpdateSocialSettingsRequest, _ ...grpc.CallOption) (*corev1.UpdateSocialSettingsResponse, error) {
	st := &corev1.SocialSettings{ProfilingEnabled: true}
	if in.ProfilingEnabled != nil {
		st.ProfilingEnabled = in.GetProfilingEnabled()
	}
	if in.HideFromSuggestions != nil {
		st.HideFromSuggestions = in.GetHideFromSuggestions()
	}
	return &corev1.UpdateSocialSettingsResponse{Settings: st}, f.record(ctx, in)
}

func TestFriendSuggestionRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)

	w := as(h, "GET", "/v1/me/friend-suggestions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("liste: %d %s", w.Code, w.Body)
	}
	// Exactly these keys: no score, no shared count, no reason.
	var raw struct {
		Suggestions []map[string]any `json:"suggestions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || len(raw.Suggestions) != 1 {
		t.Fatalf("cevap: %s %v", w.Body, err)
	}
	var keys []string
	for k := range raw.Suggestions[0] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"display_name", "suggested_at", "user_id"}) {
		t.Fatalf("öneri alanları: %v", keys)
	}
	_, tok, _ := f.last()
	if tok != "USERTOKEN" {
		t.Fatalf("token: %q", tok)
	}

	if w := as(h, "DELETE", "/v1/me/friend-suggestions/"+userID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("kapat: %d", w.Code)
	}
	got, _, _ := f.last()
	if got.(*corev1.DismissFriendSuggestionRequest).GetUserId() != userID {
		t.Fatalf("core'a giden: %v", got)
	}

	w = as(h, "GET", "/v1/me/social-settings", "")
	if v := decodeBody[socialSettingsJSON](t, w); w.Code != 200 || !v.ProfilingEnabled || v.HideFromSuggestions {
		t.Fatalf("ayarlar: %d %+v", w.Code, v)
	}
	w = as(h, "PATCH", "/v1/me/social-settings", `{"hide_from_suggestions":true}`)
	if v := decodeBody[socialSettingsJSON](t, w); w.Code != 200 || !v.ProfilingEnabled || !v.HideFromSuggestions {
		t.Fatalf("güncelleme: %d %+v", w.Code, v)
	}
	got, _, _ = f.last()
	if in := got.(*corev1.UpdateSocialSettingsRequest); in.ProfilingEnabled != nil || !in.GetHideFromSuggestions() {
		t.Fatalf("core'a giden: %v", in)
	}
}

func TestFriendSuggestionRoutesValidateInput(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	for _, c := range []struct{ method, path, body string }{
		{"DELETE", "/v1/me/friend-suggestions/x", ""},
		{"PATCH", "/v1/me/social-settings", `{}`},
		{"PATCH", "/v1/me/social-settings", `{"profiling_enabled":null}`},
		{"PATCH", "/v1/me/social-settings", `{"profiling_enabled":"no"}`},
		{"PATCH", "/v1/me/social-settings", `{"score":1}`},
	} {
		if w := as(h, c.method, c.path, c.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s %s: %d", c.method, c.path, c.body, w.Code)
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatalf("geçersiz istekler core'a ulaştı: %d", n)
	}
	w := do(h, "GET", "/v1/me/friend-suggestions", "", map[string]string{clientHeader: "android"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("token'sız: %d", w.Code)
	}
}

// TestFriendSuggestionContractHasNoScore pins ADR-0030 in the contract:
// the suggestion and settings schemas name no field that could carry the
// friendship score or the number of shared events.
func TestFriendSuggestionContractHasNoScore(t *testing.T) {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(spec), "\n")
	schemaRe := regexp.MustCompile(`^    ([A-Za-z]+):$`)
	propRe := regexp.MustCompile(`^        ([a-z_]+):`)
	want := map[string]bool{"FriendSuggestion": true, "FriendSuggestionList": true, "SocialSettings": true, "SocialSettingsUpdate": true}
	found := map[string]bool{}
	cur := ""
	for _, l := range lines {
		if m := schemaRe.FindStringSubmatch(l); m != nil {
			cur = m[1]
			continue
		}
		if !want[cur] {
			continue
		}
		m := propRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		found[cur] = true
		for _, bad := range []string{"score", "shared", "weight", "rank", "count"} {
			if strings.Contains(m[1], bad) {
				t.Errorf("%s.%s puanı ya da ortak katılımı taşıyabilir", cur, m[1])
			}
		}
	}
	for s := range want {
		if !found[s] {
			t.Errorf("sözleşmede %s şeması bulunamadı", s)
		}
	}
}
