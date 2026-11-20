package rest

import (
	"context"
	"net/http"
	"strings"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The questions component (kind "questions", issue #256; ADR-0025 §4, §5).
// core decides everything; the gateway only keeps junk out early. Question
// texts travel in bodies only and are never logged; the author of an
// anonymous question never reaches the gateway.

// maxQuestionBytes bounds a question's body: 500 characters of up to four
// bytes, with room for the whitespace core folds.
const maxQuestionBytes = (500 + 64) * 4

// registerQuestionsComponent is the questions component's line in the
// registry (componentRoutes).
func registerQuestionsComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "questions"), h.authed(h.listQuestions))
	mux.HandleFunc(componentPath("POST", "questions"), h.authed(h.askQuestion))
	mux.HandleFunc(componentPath("DELETE", "questions/{question_id}"), h.authed(h.removeQuestion))
	mux.HandleFunc(componentPath("PUT", "questions/{question_id}/vote"), h.authed(h.voteQuestion(true)))
	mux.HandleFunc(componentPath("DELETE", "questions/{question_id}/vote"), h.authed(h.voteQuestion(false)))
	mux.HandleFunc(componentPath("PUT", "questions/{question_id}/answered"), h.authed(h.answerQuestion(true)))
	mux.HandleFunc(componentPath("DELETE", "questions/{question_id}/answered"), h.authed(h.answerQuestion(false)))
}

func (h componentHandlers) questions() corev1.QuestionsComponentServiceClient {
	return corev1.NewQuestionsComponentServiceClient(h.conn)
}

type eventQuestionJSON struct {
	ID   string `json:"id"`
	Body string `json:"body"`
	// Anonymous: author_id is never given.
	Anonymous      bool   `json:"anonymous"`
	AuthorID       string `json:"author_id,omitempty"`
	CallerIsAuthor bool   `json:"caller_is_author"`
	VoteCount      int32  `json:"vote_count"`
	CallerVoted    bool   `json:"caller_voted"`
	// AnsweredAt is given when the question was marked answered.
	AnsweredAt      *time.Time `json:"answered_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	CallerCanRemove bool       `json:"caller_can_remove"`
}

type eventQuestionListJSON struct {
	Questions             []eventQuestionJSON `json:"questions"`
	AnonymousAllowed      bool                `json:"anonymous_allowed"`
	CallerCanAsk          bool                `json:"caller_can_ask"`
	CallerCanMarkAnswered bool                `json:"caller_can_mark_answered"`
	MaxQuestionsPerPerson int32               `json:"max_questions_per_person"`
}

func eventQuestionFrom(q *corev1.EventQuestion) eventQuestionJSON {
	out := eventQuestionJSON{
		ID: q.GetId(), Body: q.GetBody(), Anonymous: q.GetAnonymous(), AuthorID: q.GetAuthorId(),
		CallerIsAuthor: q.GetCallerIsAuthor(), VoteCount: q.GetVoteCount(), CallerVoted: q.GetCallerVoted(),
		CreatedAt: q.GetCreateTime().AsTime(), CallerCanRemove: q.GetCallerCanRemove(),
	}
	if q.GetAnonymous() {
		out.AuthorID = ""
	}
	if q.GetAnswerTime() != nil {
		at := q.GetAnswerTime().AsTime()
		out.AnsweredAt = &at
	}
	return out
}

func (h componentHandlers) listQuestions(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.questions().ListEventQuestions(ctx, &corev1.ListEventQuestionsRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := eventQuestionListJSON{
		Questions: make([]eventQuestionJSON, 0, len(resp.GetQuestions())), AnonymousAllowed: resp.GetAnonymousAllowed(),
		CallerCanAsk: resp.GetCallerCanAsk(), CallerCanMarkAnswered: resp.GetCallerCanMarkAnswered(),
		MaxQuestionsPerPerson: resp.GetMaxQuestionsPerPerson(),
	}
	for _, q := range resp.GetQuestions() {
		out.Questions = append(out.Questions, eventQuestionFrom(q))
	}
	writeJSON(w, http.StatusOK, out)
}

type askQuestionBody struct {
	Body            string `json:"body"`
	Anonymous       bool   `json:"anonymous"`
	ClientRequestID string `json:"client_request_id"`
}

func (h componentHandlers) askQuestion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b askQuestionBody
	if !decode(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.Body) == "" || len(b.Body) > maxQuestionBytes || !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.questions().AskEventQuestion(ctx, &corev1.AskEventQuestionRequest{
		Ref: ref, Body: b.Body, Anonymous: b.Anonymous, ClientRequestId: b.ClientRequestID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, eventQuestionFrom(resp.GetQuestion()))
}

// questionPath reads the path's component and question.
func questionPath(w http.ResponseWriter, r *http.Request) (*corev1.ComponentRef, string, bool) {
	ref, ok := componentRef(w, r)
	if !ok {
		return nil, "", false
	}
	q, ok := pathUUID(w, r, "question_id")
	if !ok {
		return nil, "", false
	}
	return ref, q, true
}

func (h componentHandlers) removeQuestion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, q, ok := questionPath(w, r)
	if !ok {
		return
	}
	if _, err := h.questions().RemoveEventQuestion(ctx, &corev1.RemoveEventQuestionRequest{Ref: ref, QuestionId: q}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h componentHandlers) voteQuestion(voted bool) func(context.Context, http.ResponseWriter, *http.Request) {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		ref, q, ok := questionPath(w, r)
		if !ok {
			return
		}
		if _, err := h.questions().SetEventQuestionVote(ctx, &corev1.SetEventQuestionVoteRequest{Ref: ref, QuestionId: q, Voted: voted}); err != nil {
			h.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h componentHandlers) answerQuestion(answered bool) func(context.Context, http.ResponseWriter, *http.Request) {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		ref, q, ok := questionPath(w, r)
		if !ok {
			return
		}
		if _, err := h.questions().SetEventQuestionAnswered(ctx, &corev1.SetEventQuestionAnsweredRequest{Ref: ref, QuestionId: q, Answered: answered}); err != nil {
			h.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
