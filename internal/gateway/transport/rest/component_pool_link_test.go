package rest

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const poolLinkComponentService = "tayfam.core.v1.PoolLinkComponentService"

// fakePoolLinks is core's PoolLinkComponentService for the pool_link tests.
type fakePoolLinks struct {
	get   *corev1.GetPoolLinksRequest
	set   *corev1.SetPoolLinksRequest
	token string
	out   *corev1.EventPoolLinks
	err   error
	calls int
}

func (f *fakePoolLinks) GetPoolLinks(ctx context.Context, in *corev1.GetPoolLinksRequest, _ ...grpc.CallOption) (*corev1.GetPoolLinksResponse, error) {
	f.get, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.GetPoolLinksResponse{Links: f.out}, f.err
}

func (f *fakePoolLinks) SetPoolLinks(ctx context.Context, in *corev1.SetPoolLinksRequest, _ ...grpc.CallOption) (*corev1.SetPoolLinksResponse, error) {
	f.set, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.SetPoolLinksResponse{Links: f.out}, f.err
}

const poolLinksPath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/pool/pool-links"

func TestPoolLinkComponentRoutes(t *testing.T) {
	at := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	f := &fakePoolLinks{out: &corev1.EventPoolLinks{
		Questions: []*corev1.PoolLinkedQuestion{{
			QuestionId: questionID, AuthorUserId: userID, Body: "Soru", SubjectKey: "subject_a", AnswerCount: 1, CreateTime: timestamppb.New(at),
		}},
		MaxLinks: 50, CallerCanEdit: true,
	}}
	h := componentRouter(fakeServices{poolLinkComponentService: f})

	w := as(h, "GET", poolLinksPath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("okuma: %d %s", w.Code, w.Body)
	}
	if ref := f.get.GetRef(); ref.GetTayfaId() != tayfaID || ref.GetEventId() != eventID || ref.GetComponentKey() != "pool" || f.token != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", f.get, f.token)
	}
	got := decodeBody[eventPoolLinksJSON](t, w)
	if len(got.Questions) != 1 || got.Questions[0].QuestionID != questionID || got.Questions[0].PhotoUploadIDs == nil ||
		got.MaxLinks != 50 || !got.CallerCanEdit || !got.Questions[0].CreatedAt.Equal(at) {
		t.Fatalf("liste: %+v", got)
	}
	if strings.Contains(w.Body.String(), "topic_key") {
		t.Fatalf("boş konu yazıldı: %s", w.Body)
	}

	w = as(h, "PUT", poolLinksPath, `{"question_ids":["`+questionID+`","`+answerID+`"]}`)
	if w.Code != http.StatusOK || !slices.Equal(f.set.GetQuestionIds(), []string{questionID, answerID}) || f.set.GetRef().GetComponentKey() != "pool" {
		t.Fatalf("yazma: %d %v", w.Code, f.set)
	}
	w = as(h, "PUT", poolLinksPath, `{"question_ids":[]}`)
	if w.Code != http.StatusOK || len(f.set.GetQuestionIds()) != 0 {
		t.Fatalf("kaldırma: %d %v", w.Code, f.set)
	}
}

func TestPoolLinkComponentRoutesValidateInput(t *testing.T) {
	f := &fakePoolLinks{out: &corev1.EventPoolLinks{}}
	h := componentRouter(fakeServices{poolLinkComponentService: f})
	many := `{"question_ids":[` + strings.Repeat(`"`+questionID+`",`, poolLinksMax) + `"` + questionID + `"]}`
	for name, body := range map[string]string{
		"boş gövde":    `{}`,
		"null liste":   `{"question_ids":null}`,
		"bozuk kimlik": `{"question_ids":["soru"]}`,
		"51 soru":      many,
		"fazla alan":   `{"question_ids":[],"max_links":3}`,
		"dizi olmayan": `{"question_ids":"` + questionID + `"}`,
		"bozuk JSON":   `{"question_ids":[`,
	} {
		if w := as(h, "PUT", poolLinksPath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
}

// core's refusals reach the client as their problem types.
func TestPoolLinkComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_MODULE_DISABLED":    {codes.FailedPrecondition, http.StatusConflict, "module-disabled"},
		"ERROR_REASON_QUESTION_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "question-not-found"},
		"ERROR_REASON_COMPONENT_FROZEN":   {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
	} {
		h := componentRouter(fakeServices{poolLinkComponentService: &fakePoolLinks{err: coreErr(want.grpc, reason)}})
		if w := as(h, "PUT", poolLinksPath, `{"question_ids":[]}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
