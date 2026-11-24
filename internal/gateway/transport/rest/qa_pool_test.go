package rest

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The question pool (issue #271) goes through the Soru-Cevap routes with
// pool: question_pool; the gateway passes the pool's fields and keeps junk
// out early.
func TestQAPoolRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	tf := "/v1/tayfas/" + tayfaID
	qs := tf + "/questions"
	q := qs + "/" + questionID
	a := tf + "/answers/" + answerID
	photo := `"` + uploadID + `"`
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", qs + "?pool=question_pool&subject_key=subject_a&topic_key=topic_a", "", 200, &corev1.ListQuestionsRequest{
			TayfaId: tayfaID, Pool: corev1.QuestionPool_QUESTION_POOL_QUESTION_POOL, SubjectKey: "subject_a", TopicKey: "topic_a",
		}},
		{"GET", qs + "?pool=qa", "", 200, &corev1.ListQuestionsRequest{TayfaId: tayfaID, Pool: corev1.QuestionPool_QUESTION_POOL_QA}},
		{"POST", qs, `{"pool":"question_pool","subject_key":"subject_a","topic_key":"topic_a","photo_upload_ids":[` + photo + `],"client_request_id":"` + reqID + `"}`, 201,
			&corev1.CreateQuestionRequest{
				TayfaId: tayfaID, Pool: corev1.QuestionPool_QUESTION_POOL_QUESTION_POOL, SubjectKey: "subject_a", TopicKey: "topic_a",
				PhotoUploadIds: []string{uploadID}, ClientRequestId: reqID,
			}},
		{"PATCH", q, `{"subject_key":"subject_b","photo_upload_ids":[]}`, 200, &corev1.UpdateQuestionRequest{
			TayfaId: tayfaID, QuestionId: questionID, SubjectKey: "subject_b", PhotoUploadIds: []string{},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"subject", "photos"}},
		}},
		{"POST", q + "/answers", `{"body":"","photo_upload_ids":[` + photo + `],"client_request_id":"` + reqID + `"}`, 201,
			&corev1.CreateAnswerRequest{TayfaId: tayfaID, QuestionId: questionID, PhotoUploadIds: []string{uploadID}, ClientRequestId: reqID}},
		{"PATCH", a, `{"body":"","photo_upload_ids":[` + photo + `]}`, 200,
			&corev1.UpdateAnswerRequest{TayfaId: tayfaID, AnswerId: answerID, PhotoUploadIds: []string{uploadID}}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, _, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) {
			t.Errorf("%s %s: %d %s; core'a giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}
	// Answers carry their photo list, empty included.
	w := as(h, "GET", q+"/answers", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"photo_upload_ids":[]`) {
		t.Fatalf("cevap listesi: %s", w.Body)
	}
}

func TestQAPoolBadRequests(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	tf := "/v1/tayfas/" + tayfaID
	qs := tf + "/questions"
	q := qs + "/" + questionID
	crid := `,"client_request_id":"` + reqID + `"`
	photo := `"` + uploadID + `"`
	for name, tc := range map[string]struct{ method, path, body string }{
		"bilinmeyen havuz":      {"GET", qs + "?pool=okul", ""},
		"bozuk ders":            {"GET", qs + "?pool=question_pool&subject_key=DersA", ""},
		"havuzda başlık":        {"POST", qs, `{"pool":"question_pool","title":"On karakterlik","subject_key":"subject_a"` + crid + `}`},
		"havuzda ders yok":      {"POST", qs, `{"pool":"question_pool","body":"Soru"` + crid + `}`},
		"bilinmeyen havuz adı":  {"POST", qs, `{"pool":"okul","title":"On karakterlik"` + crid + `}`},
		"üç fotoğraf":           {"POST", qs, `{"pool":"question_pool","subject_key":"subject_a","photo_upload_ids":[` + photo + `,` + photo + `,` + photo + `]` + crid + `}`},
		"bozuk fotoğraf":        {"POST", qs, `{"pool":"question_pool","subject_key":"subject_a","photo_upload_ids":["foto"]` + crid + `}`},
		"dersiz konu":           {"PATCH", q, `{"topic_key":"topic_a"}`},
		"boş ders":              {"PATCH", q, `{"subject_key":""}`},
		"fotoğrafsız boş cevap": {"POST", q + "/answers", `{"body":"","photo_upload_ids":[]` + crid + `}`},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek core'a gitti")
	}
}
