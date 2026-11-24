package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The question pool link component (kind "pool_link", issue #271; ADR-0037
// §3, ADR-0040 §2). core decides everything; the gateway only keeps junk
// out early. Question texts are never logged.

const (
	// poolLinksMax is poollink.MaxLinks in core.
	poolLinksMax = 50
	// maxPoolLinksBody fits poolLinksMax question IDs.
	maxPoolLinksBody = 8 << 10
)

// registerPoolLinkComponent is the pool_link component's line in the
// registry (componentRoutes).
func registerPoolLinkComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "pool-links"), h.authed(h.getPoolLinks))
	mux.HandleFunc(componentPath("PUT", "pool-links"), h.authed(h.setPoolLinks))
}

func (h componentHandlers) poolLinks() corev1.PoolLinkComponentServiceClient {
	return corev1.NewPoolLinkComponentServiceClient(h.conn)
}

type poolLinkedQuestionJSON struct {
	QuestionID string `json:"question_id"`
	// Absent when the author's account was deleted.
	AuthorUserID   string    `json:"author_user_id,omitempty"`
	Body           string    `json:"body"`
	SubjectKey     string    `json:"subject_key"`
	TopicKey       string    `json:"topic_key,omitempty"`
	PhotoUploadIDs []string  `json:"photo_upload_ids"`
	AnswerCount    int32     `json:"answer_count"`
	CreatedAt      time.Time `json:"created_at"`
}

type eventPoolLinksJSON struct {
	Questions     []poolLinkedQuestionJSON `json:"questions"`
	MaxLinks      int32                    `json:"max_links"`
	CallerCanEdit bool                     `json:"caller_can_edit"`
}

func eventPoolLinksFrom(l *corev1.EventPoolLinks) eventPoolLinksJSON {
	out := eventPoolLinksJSON{
		Questions: make([]poolLinkedQuestionJSON, 0, len(l.GetQuestions())), MaxLinks: l.GetMaxLinks(), CallerCanEdit: l.GetCallerCanEdit(),
	}
	for _, q := range l.GetQuestions() {
		out.Questions = append(out.Questions, poolLinkedQuestionJSON{
			QuestionID: q.GetQuestionId(), AuthorUserID: q.GetAuthorUserId(), Body: q.GetBody(),
			SubjectKey: q.GetSubjectKey(), TopicKey: q.GetTopicKey(), PhotoUploadIDs: stringsOrEmpty(q.GetPhotoUploadIds()),
			AnswerCount: q.GetAnswerCount(), CreatedAt: q.GetCreateTime().AsTime(),
		})
	}
	return out
}

func (h componentHandlers) getPoolLinks(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.poolLinks().GetPoolLinks(ctx, &corev1.GetPoolLinksRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventPoolLinksFrom(resp.GetLinks()))
}

// poolLinksBody is the body of a list change. QuestionIDs is a pointer so
// that a body without it is refused rather than read as "remove every
// link".
type poolLinksBody struct {
	QuestionIDs *[]string `json:"question_ids"`
}

func (h componentHandlers) setPoolLinks(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b poolLinksBody
	if !decodeLimit(w, r, &b, maxPoolLinksBody) {
		return
	}
	if b.QuestionIDs == nil || len(*b.QuestionIDs) > poolLinksMax {
		badRequest(w, r)
		return
	}
	for _, x := range *b.QuestionIDs {
		if !validUUID(x) {
			badRequest(w, r)
			return
		}
	}
	resp, err := h.poolLinks().SetPoolLinks(ctx, &corev1.SetPoolLinksRequest{Ref: ref, QuestionIds: *b.QuestionIDs})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventPoolLinksFrom(resp.GetLinks()))
}
