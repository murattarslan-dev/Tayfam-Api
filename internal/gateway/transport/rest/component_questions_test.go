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
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const questionsComponentService = "tayfam.core.v1.QuestionsComponentService"

// fakeQuestions is core's QuestionsComponentService for the questions
// tests.
type fakeQuestions struct {
	got   proto.Message
	token string
	err   error
	calls int
}

func (f *fakeQuestions) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func testEventQuestion() *corev1.EventQuestion {
	return &corev1.EventQuestion{
		Id: questionID, Body: "Neden bu rota?", Anonymous: true, CallerIsAuthor: true, VoteCount: 3, CallerVoted: true,
		CreateTime: timestamppb.New(t0), CallerCanRemove: true,
	}
}

func (f *fakeQuestions) ListQuestions(ctx context.Context, in *corev1.ListQuestionsRequest, _ ...grpc.CallOption) (*corev1.ListQuestionsResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListQuestionsResponse{
		Questions: []*corev1.EventQuestion{
			testEventQuestion(),
			{Id: answerID, Body: "Mola var mı?", AuthorId: userID, CreateTime: timestamppb.New(t0), AnswerTime: timestamppb.New(t0)},
		},
		AnonymousAllowed: true, CallerCanAsk: true, MaxQuestionsPerPerson: 20,
	}, f.err
}

func (f *fakeQuestions) AskQuestion(ctx context.Context, in *corev1.AskQuestionRequest, _ ...grpc.CallOption) (*corev1.AskQuestionResponse, error) {
	f.rec(ctx, in)
	return &corev1.AskQuestionResponse{Question: testEventQuestion()}, f.err
}

func (f *fakeQuestions) RemoveQuestion(ctx context.Context, in *corev1.RemoveQuestionRequest, _ ...grpc.CallOption) (*corev1.RemoveQuestionResponse, error) {
	f.rec(ctx, in)
	return &corev1.RemoveQuestionResponse{}, f.err
}

func (f *fakeQuestions) SetQuestionVote(ctx context.Context, in *corev1.SetQuestionVoteRequest, _ ...grpc.CallOption) (*corev1.SetQuestionVoteResponse, error) {
	f.rec(ctx, in)
	return &corev1.SetQuestionVoteResponse{}, f.err
}

func (f *fakeQuestions) SetQuestionAnswered(ctx context.Context, in *corev1.SetQuestionAnsweredRequest, _ ...grpc.CallOption) (*corev1.SetQuestionAnsweredResponse, error) {
	f.rec(ctx, in)
	return &corev1.SetQuestionAnsweredResponse{}, f.err
}

const (
	questionsPath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/questions/questions"
	questionPath1 = questionsPath + "/" + questionID
)

func TestQuestionsComponentRoutes(t *testing.T) {
	f := &fakeQuestions{}
	h := componentRouter(fakeServices{questionsComponentService: f})
	ref := &corev1.ComponentRef{TayfaId: tayfaID, EventId: eventID, ComponentKey: "questions"}

	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", questionsPath, "", http.StatusOK, &corev1.ListQuestionsRequest{Ref: ref}},
		{"POST", questionsPath, `{"body":"Neden bu rota?","anonymous":true,"client_request_id":"` + reqID + `"}`, http.StatusCreated,
			&corev1.AskQuestionRequest{Ref: ref, Body: "Neden bu rota?", Anonymous: true, ClientRequestId: reqID}},
		{"POST", questionsPath, `{"body":"Mola var mı?","client_request_id":"` + reqID + `"}`, http.StatusCreated,
			&corev1.AskQuestionRequest{Ref: ref, Body: "Mola var mı?", ClientRequestId: reqID}},
		{"DELETE", questionPath1, "", http.StatusNoContent, &corev1.RemoveQuestionRequest{Ref: ref, QuestionId: questionID}},
		{"PUT", questionPath1 + "/vote", "", http.StatusNoContent, &corev1.SetQuestionVoteRequest{Ref: ref, QuestionId: questionID, Voted: true}},
		{"DELETE", questionPath1 + "/vote", "", http.StatusNoContent, &corev1.SetQuestionVoteRequest{Ref: ref, QuestionId: questionID}},
		{"PUT", questionPath1 + "/answered", "", http.StatusNoContent, &corev1.SetQuestionAnsweredRequest{Ref: ref, QuestionId: questionID, Answered: true}},
		{"DELETE", questionPath1 + "/answered", "", http.StatusNoContent, &corev1.SetQuestionAnsweredRequest{Ref: ref, QuestionId: questionID}},
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

	w := as(h, "GET", questionsPath, "")
	l := decodeBody[eventQuestionListJSON](t, w)
	if len(l.Questions) != 2 || !l.AnonymousAllowed || !l.CallerCanAsk || l.CallerCanMarkAnswered || l.MaxQuestionsPerPerson != 20 {
		t.Fatalf("liste: %+v", l)
	}
	q := l.Questions[0]
	if q.ID != questionID || q.Body != "Neden bu rota?" || !q.Anonymous || q.AuthorID != "" || !q.CallerIsAuthor || q.VoteCount != 3 ||
		!q.CallerVoted || q.AnsweredAt != nil || !q.CreatedAt.Equal(t0) || !q.CallerCanRemove {
		t.Fatalf("anonim soru: %+v", q)
	}
	if n := l.Questions[1]; n.AuthorID != userID || n.AnsweredAt == nil || !n.AnsweredAt.Equal(t0) {
		t.Fatalf("açık soru: %+v", n)
	}
	// An anonymous question has no author key at all.
	if body := w.Body.String(); !strings.Contains(body, `"anonymous":true,"caller_is_author":true`) {
		t.Fatalf("anonim sorunun yanıtı: %s", body)
	}
	w = as(h, "POST", questionsPath, `{"body":"Soru?","client_request_id":"`+reqID+`"}`)
	if a := decodeBody[eventQuestionJSON](t, w); a.ID != questionID {
		t.Fatalf("sorma yanıtı: %+v", a)
	}
}

func TestQuestionsComponentRoutesValidateInput(t *testing.T) {
	f := &fakeQuestions{}
	h := componentRouter(fakeServices{questionsComponentService: f})

	for name, body := range map[string]string{
		"istek kimliği yok":   `{"body":"Soru?"}`,
		"bozuk istek kimliği": `{"body":"Soru?","client_request_id":"x"}`,
		"metin yok":           `{"client_request_id":"` + reqID + `"}`,
		"boş metin":           `{"body":"  ","client_request_id":"` + reqID + `"}`,
		"uzun metin":          `{"body":"` + strings.Repeat("a", maxQuestionBytes+1) + `","client_request_id":"` + reqID + `"}`,
		"bilinmeyen alan":     `{"body":"Soru?","author_id":"` + userID + `","client_request_id":"` + reqID + `"}`,
		"metin anonim":        `{"body":"Soru?","anonymous":"evet","client_request_id":"` + reqID + `"}`,
		"bozuk JSON":          `{"body":`,
	} {
		if w := as(h, "POST", questionsPath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	for _, rt := range []struct{ method, path string }{
		{"GET", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Questions/questions"},
		{"DELETE", questionsPath + "/x"},
		{"PUT", questionsPath + "/x/vote"},
		{"PUT", questionsPath + "/x/answered"},
	} {
		if w := as(h, rt.method, rt.path, ""); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call; a write without the client header is refused.
	for _, rt := range []struct{ method, path string }{
		{"GET", questionsPath}, {"POST", questionsPath}, {"DELETE", questionPath1},
		{"PUT", questionPath1 + "/vote"}, {"PUT", questionPath1 + "/answered"},
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
func TestQuestionsComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_COMPONENT_NOT_FOUND":  {codes.NotFound, http.StatusNotFound, "component-not-found"},
		"ERROR_REASON_COMPONENT_FROZEN":     {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_EVENT_NOT_GOING":      {codes.FailedPrecondition, http.StatusConflict, "event-not-going"},
		"ERROR_REASON_EVENT_LIMIT":          {codes.ResourceExhausted, http.StatusConflict, "event-limit"},
		"ERROR_REASON_EVENT_ITEM_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "event-item-not-found"},
		"ERROR_REASON_PERMISSION_DENIED":    {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{questionsComponentService: &fakeQuestions{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", questionsPath, `{"body":"Soru?","client_request_id":"`+reqID+`"}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
