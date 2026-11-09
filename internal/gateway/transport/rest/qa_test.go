package rest

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const (
	questionID = "01965a3c-0000-7000-8000-000000000041"
	answerID   = "01965a3c-0000-7000-8000-000000000042"
	tagID      = "01965a3c-0000-7000-8000-000000000043"
)

func testQuestion() *corev1.Question {
	return &corev1.Question{
		Id: questionID, TayfaId: tayfaID, AuthorUserId: userID, Title: "Hangi çadırı alalım?", Body: "Dört kişilik",
		Tags: []*corev1.Tag{{Id: tagID, Name: "Çadır", QuestionCount: 9}}, AnswerCount: 2,
		CreateTime: timestamppb.New(t0), UpdateTime: timestamppb.New(t0),
	}
}

func testAnswer() *corev1.Answer {
	return &corev1.Answer{
		Id: answerID, QuestionId: questionID, TayfaId: tayfaID, AuthorUserId: userID, Body: "Kubbe çadır",
		VoteCount: 3, Voted: true, CreateTime: timestamppb.New(t0), UpdateTime: timestamppb.New(t0),
	}
}

func (f *fakeCore) CreateQuestion(ctx context.Context, in *corev1.CreateQuestionRequest, _ ...grpc.CallOption) (*corev1.CreateQuestionResponse, error) {
	return &corev1.CreateQuestionResponse{Question: testQuestion()}, f.record(ctx, in)
}
func (f *fakeCore) GetQuestion(ctx context.Context, in *corev1.GetQuestionRequest, _ ...grpc.CallOption) (*corev1.GetQuestionResponse, error) {
	return &corev1.GetQuestionResponse{Question: testQuestion()}, f.record(ctx, in)
}
func (f *fakeCore) ListQuestions(ctx context.Context, in *corev1.ListQuestionsRequest, _ ...grpc.CallOption) (*corev1.ListQuestionsResponse, error) {
	orphan := testQuestion()
	orphan.AuthorUserId, orphan.Tags = "", nil
	return &corev1.ListQuestionsResponse{Questions: []*corev1.Question{testQuestion(), orphan}, NextPageToken: "SONRAKI"}, f.record(ctx, in)
}
func (f *fakeCore) UpdateQuestion(ctx context.Context, in *corev1.UpdateQuestionRequest, _ ...grpc.CallOption) (*corev1.UpdateQuestionResponse, error) {
	return &corev1.UpdateQuestionResponse{Question: testQuestion()}, f.record(ctx, in)
}
func (f *fakeCore) DeleteQuestion(ctx context.Context, in *corev1.DeleteQuestionRequest, _ ...grpc.CallOption) (*corev1.DeleteQuestionResponse, error) {
	return &corev1.DeleteQuestionResponse{}, f.record(ctx, in)
}
func (f *fakeCore) RestoreQuestion(ctx context.Context, in *corev1.RestoreQuestionRequest, _ ...grpc.CallOption) (*corev1.RestoreQuestionResponse, error) {
	return &corev1.RestoreQuestionResponse{Question: testQuestion()}, f.record(ctx, in)
}
func (f *fakeCore) CreateAnswer(ctx context.Context, in *corev1.CreateAnswerRequest, _ ...grpc.CallOption) (*corev1.CreateAnswerResponse, error) {
	return &corev1.CreateAnswerResponse{Answer: testAnswer()}, f.record(ctx, in)
}
func (f *fakeCore) ListAnswers(ctx context.Context, in *corev1.ListAnswersRequest, _ ...grpc.CallOption) (*corev1.ListAnswersResponse, error) {
	return &corev1.ListAnswersResponse{Answers: []*corev1.Answer{testAnswer()}}, f.record(ctx, in)
}
func (f *fakeCore) UpdateAnswer(ctx context.Context, in *corev1.UpdateAnswerRequest, _ ...grpc.CallOption) (*corev1.UpdateAnswerResponse, error) {
	return &corev1.UpdateAnswerResponse{Answer: testAnswer()}, f.record(ctx, in)
}
func (f *fakeCore) DeleteAnswer(ctx context.Context, in *corev1.DeleteAnswerRequest, _ ...grpc.CallOption) (*corev1.DeleteAnswerResponse, error) {
	return &corev1.DeleteAnswerResponse{}, f.record(ctx, in)
}
func (f *fakeCore) RestoreAnswer(ctx context.Context, in *corev1.RestoreAnswerRequest, _ ...grpc.CallOption) (*corev1.RestoreAnswerResponse, error) {
	return &corev1.RestoreAnswerResponse{Answer: testAnswer()}, f.record(ctx, in)
}
func (f *fakeCore) SetAnswerVote(ctx context.Context, in *corev1.SetAnswerVoteRequest, _ ...grpc.CallOption) (*corev1.SetAnswerVoteResponse, error) {
	a := testAnswer()
	a.Voted = in.GetVoted()
	return &corev1.SetAnswerVoteResponse{Answer: a}, f.record(ctx, in)
}
func (f *fakeCore) ListTags(ctx context.Context, in *corev1.ListTagsRequest, _ ...grpc.CallOption) (*corev1.ListTagsResponse, error) {
	return &corev1.ListTagsResponse{Tags: []*corev1.Tag{{Id: tagID, Name: "Çadır", QuestionCount: 0}}}, f.record(ctx, in)
}
func (f *fakeCore) DeleteTag(ctx context.Context, in *corev1.DeleteTagRequest, _ ...grpc.CallOption) (*corev1.DeleteTagResponse, error) {
	return &corev1.DeleteTagResponse{}, f.record(ctx, in)
}

func TestQARoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	tf := "/v1/tayfas/" + tayfaID
	qs := tf + "/questions"
	q := qs + "/" + questionID
	a := tf + "/answers/" + answerID
	search := url.Values{"q": {"IŞIK İzmir"}, "tag_id": {tagID}, "page_size": {"5"}, "page_token": {"P"}}.Encode()
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", qs, "", 200, &corev1.ListQuestionsRequest{TayfaId: tayfaID}},
		{"GET", qs + "?" + search, "", 200, &corev1.ListQuestionsRequest{TayfaId: tayfaID, Query: "IŞIK İzmir", TagId: tagID, PageSize: 5, PageToken: "P"}},
		{"POST", qs, `{"title":"Hangi çadırı alalım?","body":"B","tags":["Çadır","Kamp"],"client_request_id":"` + reqID + `"}`, 201,
			&corev1.CreateQuestionRequest{TayfaId: tayfaID, Title: "Hangi çadırı alalım?", Body: "B", Tags: []string{"Çadır", "Kamp"}, ClientRequestId: reqID}},
		{"GET", q, "", 200, &corev1.GetQuestionRequest{TayfaId: tayfaID, QuestionId: questionID}},
		{"DELETE", q, "", 204, &corev1.DeleteQuestionRequest{TayfaId: tayfaID, QuestionId: questionID}},
		{"POST", q + "/restoration", "", 200, &corev1.RestoreQuestionRequest{TayfaId: tayfaID, QuestionId: questionID}},
		{"GET", q + "/answers?page_size=3", "", 200, &corev1.ListAnswersRequest{TayfaId: tayfaID, QuestionId: questionID, PageSize: 3}},
		{"POST", q + "/answers", `{"body":"Kubbe çadır","client_request_id":"` + reqID + `"}`, 201,
			&corev1.CreateAnswerRequest{TayfaId: tayfaID, QuestionId: questionID, Body: "Kubbe çadır", ClientRequestId: reqID}},
		{"PATCH", a, `{"body":"Düzeltildi"}`, 200, &corev1.UpdateAnswerRequest{TayfaId: tayfaID, AnswerId: answerID, Body: "Düzeltildi"}},
		{"DELETE", a, "", 204, &corev1.DeleteAnswerRequest{TayfaId: tayfaID, AnswerId: answerID}},
		{"POST", a + "/restoration", "", 200, &corev1.RestoreAnswerRequest{TayfaId: tayfaID, AnswerId: answerID}},
		{"PUT", a + "/vote", "", 200, &corev1.SetAnswerVoteRequest{TayfaId: tayfaID, AnswerId: answerID, Voted: true}},
		{"DELETE", a + "/vote", "", 200, &corev1.SetAnswerVoteRequest{TayfaId: tayfaID, AnswerId: answerID}},
		{"GET", tf + "/tags", "", 200, &corev1.ListTagsRequest{TayfaId: tayfaID}},
		{"DELETE", tf + "/tags/" + tagID, "", 204, &corev1.DeleteTagRequest{TayfaId: tayfaID, TagId: tagID}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, tok, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) || tok != "USERTOKEN" {
			t.Errorf("%s %s: %d %s; core'a giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}

	w := as(h, "POST", qs, `{"title":"Hangi çadırı alalım?","client_request_id":"`+reqID+`"}`)
	got := decodeBody[questionJSON](t, w)
	if w.Header().Get("Location") != q || got.ID != questionID || got.AnswerCount != 2 || len(got.Tags) != 1 || got.Tags[0].Name != "Çadır" ||
		got.Tags[0].QuestionCount != nil || !got.CreatedAt.Equal(t0) {
		t.Fatalf("oluşturma yanıtı: %v %+v", w.Header(), got)
	}
	w = as(h, "GET", qs, "")
	l := decodeBody[questionListJSON](t, w)
	if len(l.Questions) != 2 || l.NextPageToken != "SONRAKI" || l.Questions[1].AuthorUserID != "" || l.Questions[1].Tags == nil {
		t.Fatalf("liste: %+v", l)
	}
	if strings.Count(w.Body.String(), "author_user_id") != 1 || strings.Contains(w.Body.String(), "question_count") {
		t.Fatalf("liste gövdesi: %s", w.Body)
	}
	w = as(h, "DELETE", a+"/vote", "")
	if ans := decodeBody[answerJSON](t, w); ans.Voted || ans.VoteCount != 3 || ans.QuestionID != questionID {
		t.Fatalf("oy geri alma: %+v", ans)
	}
	// The tag list carries the counts, zero included.
	w = as(h, "GET", tf+"/tags", "")
	if tl := decodeBody[tagListJSON](t, w); len(tl.Tags) != 1 || tl.Tags[0].QuestionCount == nil || *tl.Tags[0].QuestionCount != 0 {
		t.Fatalf("etiketler: %s", w.Body)
	}
}

func TestUpdateQuestionMask(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	q := "/v1/tayfas/" + tayfaID + "/questions/" + questionID
	for _, tc := range []struct {
		body  string
		paths []string
		tags  []string
	}{
		{`{"body":""}`, []string{"body"}, nil},
		{`{"tags":[]}`, []string{"tags"}, []string{}},
		{`{"title":"On karakterlik","tags":["a","b"]}`, []string{"title", "tags"}, []string{"a", "b"}},
	} {
		w := as(h, "PATCH", q, tc.body)
		got, _, _ := f.last()
		req := got.(*corev1.UpdateQuestionRequest)
		if w.Code != 200 || !slices.Equal(req.GetUpdateMask().GetPaths(), tc.paths) || !slices.Equal(req.GetTags(), tc.tags) || req.GetQuestionId() != questionID {
			t.Errorf("%s: %d %v", tc.body, w.Code, got)
		}
	}
}

func TestQABadRequests(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	tf := "/v1/tayfas/" + tayfaID
	qs := tf + "/questions"
	q := qs + "/" + questionID
	a := tf + "/answers/" + answerID
	crid := `,"client_request_id":"` + reqID + `"`
	for name, tc := range map[string]struct{ method, path, body string }{
		"kısa başlık":         {"POST", qs, `{"title":"Kısa?"` + crid + `}`},
		"uzun başlık":         {"POST", qs, `{"title":"` + strings.Repeat("ş", 151) + `"` + crid + `}`},
		"uzun gövde":          {"POST", qs, `{"title":"On karakterlik","body":"` + strings.Repeat("ğ", 10001) + `"` + crid + `}`},
		"çok etiket":          {"POST", qs, `{"title":"On karakterlik","tags":["a","b","c","d","e","f"]` + crid + `}`},
		"boş etiket":          {"POST", qs, `{"title":"On karakterlik","tags":[" "]` + crid + `}`},
		"uzun etiket":         {"POST", qs, `{"title":"On karakterlik","tags":["` + strings.Repeat("ı", 31) + `"]` + crid + `}`},
		"istek kimliği yok":   {"POST", qs, `{"title":"On karakterlik"}`},
		"yazar verilemez":     {"POST", qs, `{"title":"On karakterlik","author_id":"` + userID + `"` + crid + `}`},
		"uzun arama":          {"GET", qs + "?q=" + strings.Repeat("a", 101), ""},
		"bozuk etiket id":     {"GET", qs + "?tag_id=kamp", ""},
		"bozuk sayfa":         {"GET", qs + "?page_size=101", ""},
		"boş düzenleme":       {"PATCH", q, `{}`},
		"null düzenleme":      {"PATCH", q, `{"title":null}`},
		"kısa başlıkla":       {"PATCH", q, `{"title":"x"}`},
		"bozuk soru id":       {"GET", qs + "/kim", ""},
		"bozuk tayfa id":      {"GET", "/v1/tayfas/yok/questions", ""},
		"boş cevap":           {"POST", q + "/answers", `{"body":"  "` + crid + `}`},
		"uzun cevap":          {"POST", q + "/answers", `{"body":"` + strings.Repeat("ç", 10001) + `"` + crid + `}`},
		"cevap kimliği yok":   {"POST", q + "/answers", `{"body":"x"}`},
		"oy alanı verilemez":  {"POST", q + "/answers", `{"body":"x","vote_count":9` + crid + `}`},
		"boş cevap düzenleme": {"PATCH", a, `{}`},
		"bozuk cevap id":      {"PUT", tf + "/answers/x/vote", ""},
		"bozuk etiket":        {"DELETE", tf + "/tags/x", ""},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek core'a gitti")
	}
	// A bad search answers without echoing the query.
	w := as(h, "GET", qs+"?q="+url.QueryEscape("gizli "+strings.Repeat("a", 120)), "")
	if w.Code != 400 || strings.Contains(w.Body.String(), "gizli") {
		t.Fatalf("arama hatası metni yansıttı: %s", w.Body)
	}
	// The longest valid body (10000 four-byte characters) fits the limit.
	emoji := strings.Repeat("\U0001F3D5", 10000)
	if w := as(h, "POST", qs, `{"title":"On karakterlik","body":"`+emoji+`"`+crid+`}`); w.Code != 201 {
		t.Fatalf("en uzun gövde: %d %s", w.Code, w.Body)
	}
	if w := as(h, "POST", q+"/answers", `{"body":"`+emoji+`"`+crid+`}`); w.Code != 201 {
		t.Fatalf("en uzun cevap: %d %s", w.Code, w.Body)
	}
}

func TestQAWritesNeedClientHeader(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	tf := "/v1/tayfas/" + tayfaID
	q := tf + "/questions/" + questionID
	a := tf + "/answers/" + answerID
	for _, rt := range []struct{ method, path string }{
		{"POST", tf + "/questions"}, {"PATCH", q}, {"DELETE", q}, {"POST", q + "/restoration"},
		{"POST", q + "/answers"}, {"PATCH", a}, {"DELETE", a}, {"POST", a + "/restoration"},
		{"PUT", a + "/vote"}, {"DELETE", a + "/vote"}, {"DELETE", tf + "/tags/" + tagID},
	} {
		w := do(h, rt.method, rt.path, `{}`, map[string]string{"Authorization": "Bearer T"})
		if w.Code != 400 || problemType(t, w) != "missing-client-header" {
			t.Errorf("%s %s: %d", rt.method, rt.path, w.Code)
		}
	}
	for _, p := range []string{tf + "/questions", q, q + "/answers", tf + "/tags"} {
		if w := do(h, "GET", p, "", map[string]string{}); w.Code != http.StatusUnauthorized {
			t.Errorf("token'sız okuma %s: %d", p, w.Code)
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("istek core'a gitti")
	}
}

func TestQAProblems(t *testing.T) {
	for reason, want := range map[string]struct {
		code int
		typ  string
	}{
		"ERROR_REASON_MODULE_DISABLED":    {409, "module-disabled"},
		"ERROR_REASON_QUESTION_NOT_FOUND": {404, "question-not-found"},
		"ERROR_REASON_ANSWER_NOT_FOUND":   {404, "answer-not-found"},
		"ERROR_REASON_TAG_NOT_FOUND":      {404, "tag-not-found"},
		"ERROR_REASON_OWN_ANSWER_VOTE":    {409, "own-answer-vote"},
		"ERROR_REASON_TAG_LIMIT":          {409, "tag-limit"},
		"ERROR_REASON_MUTED":              {403, "muted"},
		"ERROR_REASON_ROLE_HIERARCHY":     {403, "role-hierarchy"},
	} {
		h := coreRouter(&fakeCore{err: coreErr(codes.Unknown, reason)})
		w := as(h, "PUT", "/v1/tayfas/"+tayfaID+"/answers/"+answerID+"/vote", "")
		if w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, problemType(t, w))
		}
	}
}

// Without a QAClient the routes are not served.
func TestQARoutesOptional(t *testing.T) {
	d := testCoreDeps(&fakeCore{}, 100)
	d.QA = nil
	if w := as(coreRouterWith(d), "GET", "/v1/tayfas/"+tayfaID+"/questions", ""); w.Code != 404 {
		t.Fatalf("kod %d", w.Code)
	}
}
