package rest

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// QAClient is core's QAService as the gateway uses it.
type QAClient interface {
	CreateQuestion(ctx context.Context, in *corev1.CreateQuestionRequest, opts ...grpc.CallOption) (*corev1.CreateQuestionResponse, error)
	GetQuestion(ctx context.Context, in *corev1.GetQuestionRequest, opts ...grpc.CallOption) (*corev1.GetQuestionResponse, error)
	ListQuestions(ctx context.Context, in *corev1.ListQuestionsRequest, opts ...grpc.CallOption) (*corev1.ListQuestionsResponse, error)
	UpdateQuestion(ctx context.Context, in *corev1.UpdateQuestionRequest, opts ...grpc.CallOption) (*corev1.UpdateQuestionResponse, error)
	DeleteQuestion(ctx context.Context, in *corev1.DeleteQuestionRequest, opts ...grpc.CallOption) (*corev1.DeleteQuestionResponse, error)
	RestoreQuestion(ctx context.Context, in *corev1.RestoreQuestionRequest, opts ...grpc.CallOption) (*corev1.RestoreQuestionResponse, error)
	CreateAnswer(ctx context.Context, in *corev1.CreateAnswerRequest, opts ...grpc.CallOption) (*corev1.CreateAnswerResponse, error)
	ListAnswers(ctx context.Context, in *corev1.ListAnswersRequest, opts ...grpc.CallOption) (*corev1.ListAnswersResponse, error)
	UpdateAnswer(ctx context.Context, in *corev1.UpdateAnswerRequest, opts ...grpc.CallOption) (*corev1.UpdateAnswerResponse, error)
	DeleteAnswer(ctx context.Context, in *corev1.DeleteAnswerRequest, opts ...grpc.CallOption) (*corev1.DeleteAnswerResponse, error)
	RestoreAnswer(ctx context.Context, in *corev1.RestoreAnswerRequest, opts ...grpc.CallOption) (*corev1.RestoreAnswerResponse, error)
	SetAnswerVote(ctx context.Context, in *corev1.SetAnswerVoteRequest, opts ...grpc.CallOption) (*corev1.SetAnswerVoteResponse, error)
	ListTags(ctx context.Context, in *corev1.ListTagsRequest, opts ...grpc.CallOption) (*corev1.ListTagsResponse, error)
	DeleteTag(ctx context.Context, in *corev1.DeleteTagRequest, opts ...grpc.CallOption) (*corev1.DeleteTagResponse, error)
}

// Limits mirror core's (qa package); core checks every rule again.
const (
	// maxQABody fits 10000 characters of body, 150 of title and five tags
	// in the worst JSON escaping (\uXXXX surrogate pairs).
	maxQABody         = 256 << 10
	questionTitleMin  = 10
	questionTitleMax  = 150
	qaBodyMax         = 10000
	questionTagsMax   = 5
	tagNameMax        = 30
	questionSearchMax = 100
)

func registerQA(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/questions", a(h.listQuestions))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/questions", a(h.createQuestion))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/questions/{question_id}", a(h.getQuestion))
	mux.HandleFunc("PATCH /v1/tayfas/{tayfa_id}/questions/{question_id}", a(h.updateQuestion))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/questions/{question_id}", a(h.deleteQuestion))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/questions/{question_id}/restoration", a(h.restoreQuestion))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/questions/{question_id}/answers", a(h.listAnswers))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/questions/{question_id}/answers", a(h.createAnswer))
	mux.HandleFunc("PATCH /v1/tayfas/{tayfa_id}/answers/{answer_id}", a(h.updateAnswer))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/answers/{answer_id}", a(h.deleteAnswer))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/answers/{answer_id}/restoration", a(h.restoreAnswer))
	mux.HandleFunc("PUT /v1/tayfas/{tayfa_id}/answers/{answer_id}/vote", a(h.voteAnswer(true)))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/answers/{answer_id}/vote", a(h.voteAnswer(false)))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/tags", a(h.listTags))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/tags/{tag_id}", a(h.deleteTag))
}

type tagJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Only in the tag list.
	QuestionCount *int32 `json:"question_count,omitempty"`
}

type tagListJSON struct {
	Tags []tagJSON `json:"tags"`
}

type questionJSON struct {
	ID      string `json:"id"`
	TayfaID string `json:"tayfa_id"`
	// Absent when the author's account was deleted.
	AuthorUserID string    `json:"author_user_id,omitempty"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	Tags         []tagJSON `json:"tags"`
	AnswerCount  int32     `json:"answer_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type questionListJSON struct {
	Questions     []questionJSON `json:"questions"`
	NextPageToken string         `json:"next_page_token,omitempty"`
}

type answerJSON struct {
	ID         string `json:"id"`
	QuestionID string `json:"question_id"`
	TayfaID    string `json:"tayfa_id"`
	// Absent when the author's account was deleted.
	AuthorUserID string    `json:"author_user_id,omitempty"`
	Body         string    `json:"body"`
	VoteCount    int32     `json:"vote_count"`
	Voted        bool      `json:"voted"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type answerListJSON struct {
	Answers       []answerJSON `json:"answers"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}

func tagsFrom(in []*corev1.Tag, counts bool) []tagJSON {
	out := make([]tagJSON, 0, len(in))
	for _, t := range in {
		j := tagJSON{ID: t.GetId(), Name: t.GetName()}
		if counts {
			n := t.GetQuestionCount()
			j.QuestionCount = &n
		}
		out = append(out, j)
	}
	return out
}

func questionFrom(q *corev1.Question) questionJSON {
	return questionJSON{
		ID: q.GetId(), TayfaID: q.GetTayfaId(), AuthorUserID: q.GetAuthorUserId(),
		Title: q.GetTitle(), Body: q.GetBody(), Tags: tagsFrom(q.GetTags(), false), AnswerCount: q.GetAnswerCount(),
		CreatedAt: q.GetCreateTime().AsTime(), UpdatedAt: q.GetUpdateTime().AsTime(),
	}
}

func answerFrom(a *corev1.Answer) answerJSON {
	return answerJSON{
		ID: a.GetId(), QuestionID: a.GetQuestionId(), TayfaID: a.GetTayfaId(), AuthorUserID: a.GetAuthorUserId(),
		Body: a.GetBody(), VoteCount: a.GetVoteCount(), Voted: a.GetVoted(),
		CreatedAt: a.GetCreateTime().AsTime(), UpdatedAt: a.GetUpdateTime().AsTime(),
	}
}

// pathTayfaAnd reads tayfa_id and one more UUID path value.
func pathTayfaAnd(w http.ResponseWriter, r *http.Request, name string) (tayfaID, other string, ok bool) {
	if tayfaID, ok = pathUUID(w, r, "tayfa_id"); !ok {
		return "", "", false
	}
	if other, ok = pathUUID(w, r, name); !ok {
		return "", "", false
	}
	return tayfaID, other, true
}

// The length checks mirror core's rules (after trimming); core checks the
// rest again.
func runesBetween(s string, minLen, maxLen int) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= minLen && n <= maxLen
}

func questionTitleOK(s string) bool { return runesBetween(s, questionTitleMin, questionTitleMax) }

func tagsOK(tags []string) bool {
	if len(tags) > questionTagsMax {
		return false
	}
	for _, t := range tags {
		if !runesBetween(t, 1, tagNameMax) {
			return false
		}
	}
	return true
}

// listQuestions also searches. The query text is passed to core and is
// never logged; a bad request answers without echoing it.
func (h coreHandlers) listQuestions(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	query, tag := q.Get("q"), q.Get("tag_id")
	if !runesBetween(query, 0, questionSearchMax) || (tag != "" && !validUUID(tag)) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.QA.ListQuestions(ctx, &corev1.ListQuestionsRequest{TayfaId: id, Query: query, TagId: tag, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := questionListJSON{Questions: make([]questionJSON, 0, len(resp.GetQuestions())), NextPageToken: resp.GetNextPageToken()}
	for _, x := range resp.GetQuestions() {
		out.Questions = append(out.Questions, questionFrom(x))
	}
	writeJSON(w, http.StatusOK, out)
}

type questionCreateBody struct {
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Tags            []string `json:"tags"`
	ClientRequestID string   `json:"client_request_id"`
}

func (h coreHandlers) createQuestion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b questionCreateBody
	if !decodeLimit(w, r, &b, maxQABody) {
		return
	}
	if !questionTitleOK(b.Title) || !runesBetween(b.Body, 0, qaBodyMax) || !tagsOK(b.Tags) || !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.QA.CreateQuestion(ctx, &corev1.CreateQuestionRequest{
		TayfaId: id, Title: b.Title, Body: b.Body, Tags: b.Tags, ClientRequestId: b.ClientRequestID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q := questionFrom(resp.GetQuestion())
	w.Header().Set("Location", "/v1/tayfas/"+q.TayfaID+"/questions/"+q.ID)
	writeJSON(w, http.StatusCreated, q)
}

func (h coreHandlers) getQuestion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, qid, ok := pathTayfaAnd(w, r, "question_id")
	if !ok {
		return
	}
	resp, err := h.d.QA.GetQuestion(ctx, &corev1.GetQuestionRequest{TayfaId: id, QuestionId: qid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, questionFrom(resp.GetQuestion()))
}

// questionUpdateBody: a field present in the body is changed; null counts
// as absent. "tags": [] removes every tag.
type questionUpdateBody struct {
	Title *string   `json:"title"`
	Body  *string   `json:"body"`
	Tags  *[]string `json:"tags"`
}

func (h coreHandlers) updateQuestion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, qid, ok := pathTayfaAnd(w, r, "question_id")
	if !ok {
		return
	}
	var b questionUpdateBody
	if !decodeLimit(w, r, &b, maxQABody) {
		return
	}
	req := &corev1.UpdateQuestionRequest{TayfaId: id, QuestionId: qid, UpdateMask: &fieldmaskpb.FieldMask{}}
	if b.Title != nil {
		if !questionTitleOK(*b.Title) {
			badRequest(w, r)
			return
		}
		req.Title = *b.Title
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "title")
	}
	if b.Body != nil {
		if !runesBetween(*b.Body, 0, qaBodyMax) {
			badRequest(w, r)
			return
		}
		req.Body = *b.Body
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "body")
	}
	if b.Tags != nil {
		if !tagsOK(*b.Tags) {
			badRequest(w, r)
			return
		}
		req.Tags = *b.Tags
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "tags")
	}
	if len(req.UpdateMask.Paths) == 0 {
		badRequest(w, r)
		return
	}
	resp, err := h.d.QA.UpdateQuestion(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, questionFrom(resp.GetQuestion()))
}

func (h coreHandlers) deleteQuestion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, qid, ok := pathTayfaAnd(w, r, "question_id")
	if !ok {
		return
	}
	if _, err := h.d.QA.DeleteQuestion(ctx, &corev1.DeleteQuestionRequest{TayfaId: id, QuestionId: qid}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) restoreQuestion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, qid, ok := pathTayfaAnd(w, r, "question_id")
	if !ok {
		return
	}
	resp, err := h.d.QA.RestoreQuestion(ctx, &corev1.RestoreQuestionRequest{TayfaId: id, QuestionId: qid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, questionFrom(resp.GetQuestion()))
}

func (h coreHandlers) listAnswers(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, qid, ok := pathTayfaAnd(w, r, "question_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.QA.ListAnswers(ctx, &corev1.ListAnswersRequest{TayfaId: id, QuestionId: qid, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := answerListJSON{Answers: make([]answerJSON, 0, len(resp.GetAnswers())), NextPageToken: resp.GetNextPageToken()}
	for _, a := range resp.GetAnswers() {
		out.Answers = append(out.Answers, answerFrom(a))
	}
	writeJSON(w, http.StatusOK, out)
}

type answerCreateBody struct {
	Body            string `json:"body"`
	ClientRequestID string `json:"client_request_id"`
}

func (h coreHandlers) createAnswer(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, qid, ok := pathTayfaAnd(w, r, "question_id")
	if !ok {
		return
	}
	var b answerCreateBody
	if !decodeLimit(w, r, &b, maxQABody) {
		return
	}
	if !runesBetween(b.Body, 1, qaBodyMax) || !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.QA.CreateAnswer(ctx, &corev1.CreateAnswerRequest{TayfaId: id, QuestionId: qid, Body: b.Body, ClientRequestId: b.ClientRequestID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, answerFrom(resp.GetAnswer()))
}

type answerUpdateBody struct {
	Body *string `json:"body"`
}

func (h coreHandlers) updateAnswer(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, aid, ok := pathTayfaAnd(w, r, "answer_id")
	if !ok {
		return
	}
	var b answerUpdateBody
	if !decodeLimit(w, r, &b, maxQABody) {
		return
	}
	if b.Body == nil || !runesBetween(*b.Body, 1, qaBodyMax) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.QA.UpdateAnswer(ctx, &corev1.UpdateAnswerRequest{TayfaId: id, AnswerId: aid, Body: *b.Body})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answerFrom(resp.GetAnswer()))
}

func (h coreHandlers) deleteAnswer(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, aid, ok := pathTayfaAnd(w, r, "answer_id")
	if !ok {
		return
	}
	if _, err := h.d.QA.DeleteAnswer(ctx, &corev1.DeleteAnswerRequest{TayfaId: id, AnswerId: aid}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) restoreAnswer(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, aid, ok := pathTayfaAnd(w, r, "answer_id")
	if !ok {
		return
	}
	resp, err := h.d.QA.RestoreAnswer(ctx, &corev1.RestoreAnswerRequest{TayfaId: id, AnswerId: aid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answerFrom(resp.GetAnswer()))
}

func (h coreHandlers) voteAnswer(voted bool) coreHandler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		id, aid, ok := pathTayfaAnd(w, r, "answer_id")
		if !ok {
			return
		}
		resp, err := h.d.QA.SetAnswerVote(ctx, &corev1.SetAnswerVoteRequest{TayfaId: id, AnswerId: aid, Voted: voted})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, answerFrom(resp.GetAnswer()))
	}
}

func (h coreHandlers) listTags(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.d.QA.ListTags(ctx, &corev1.ListTagsRequest{TayfaId: id})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tagListJSON{Tags: tagsFrom(resp.GetTags(), true)})
}

func (h coreHandlers) deleteTag(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, tid, ok := pathTayfaAnd(w, r, "tag_id")
	if !ok {
		return
	}
	if _, err := h.d.QA.DeleteTag(ctx, &corev1.DeleteTagRequest{TayfaId: id, TagId: tid}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
