package grpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/qa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeQA records what transport passed on and returns err.
type fakeQA struct {
	err      error
	calls    int
	user     id.ID
	tayfa    id.ID
	x        id.ID
	createQ  qa.CreateQuestionInput
	updateQ  qa.UpdateQuestionInput
	list     qa.ListQuestionsInput
	createA  qa.CreateAnswerInput
	body     string
	voted    bool
	question qa.Question
	answer   qa.Answer
}

func (f *fakeQA) hit(u, t, x id.ID) { f.calls++; f.user, f.tayfa, f.x = u, t, x }

func (f *fakeQA) CreateQuestion(_ context.Context, u, t id.ID, in qa.CreateQuestionInput) (qa.Question, error) {
	f.hit(u, t, id.ID{})
	f.createQ = in
	return f.question, f.err
}
func (f *fakeQA) GetQuestion(_ context.Context, u, t, q id.ID) (qa.Question, error) {
	f.hit(u, t, q)
	return f.question, f.err
}
func (f *fakeQA) ListQuestions(_ context.Context, u, t id.ID, in qa.ListQuestionsInput) ([]qa.Question, string, error) {
	f.hit(u, t, id.ID{})
	f.list = in
	return []qa.Question{f.question}, "sonraki", f.err
}
func (f *fakeQA) UpdateQuestion(_ context.Context, u, t, q id.ID, in qa.UpdateQuestionInput) (qa.Question, error) {
	f.hit(u, t, q)
	f.updateQ = in
	return f.question, f.err
}
func (f *fakeQA) DeleteQuestion(_ context.Context, u, t, q id.ID) error {
	f.hit(u, t, q)
	return f.err
}
func (f *fakeQA) RestoreQuestion(_ context.Context, u, t, q id.ID) (qa.Question, error) {
	f.hit(u, t, q)
	return f.question, f.err
}
func (f *fakeQA) CreateAnswer(_ context.Context, u, t, q id.ID, in qa.CreateAnswerInput) (qa.Answer, error) {
	f.hit(u, t, q)
	f.createA = in
	return f.answer, f.err
}
func (f *fakeQA) ListAnswers(_ context.Context, u, t, q id.ID, _ int32, _ string) ([]qa.Answer, string, error) {
	f.hit(u, t, q)
	return []qa.Answer{f.answer, f.answer}, "", f.err
}
func (f *fakeQA) UpdateAnswer(_ context.Context, u, t, a id.ID, body string) (qa.Answer, error) {
	f.hit(u, t, a)
	f.body = body
	return f.answer, f.err
}
func (f *fakeQA) DeleteAnswer(_ context.Context, u, t, a id.ID) error {
	f.hit(u, t, a)
	return f.err
}
func (f *fakeQA) RestoreAnswer(_ context.Context, u, t, a id.ID) (qa.Answer, error) {
	f.hit(u, t, a)
	return f.answer, f.err
}
func (f *fakeQA) SetVote(_ context.Context, u, t, a id.ID, voted bool) (qa.Answer, error) {
	f.hit(u, t, a)
	f.voted = voted
	return f.answer, f.err
}
func (f *fakeQA) ListTags(_ context.Context, u, t id.ID) ([]qa.Tag, error) {
	f.hit(u, t, id.ID{})
	return []qa.Tag{{ID: id.New(), Name: "kamp", QuestionCount: 3}}, f.err
}
func (f *fakeQA) DeleteTag(_ context.Context, u, t, tag id.ID) error {
	f.hit(u, t, tag)
	return f.err
}

// qaMethods calls every QAService method through c; x is the question,
// answer or tag ID of the method (qaIDField).
func qaMethods(ctx context.Context, c corev1.QAServiceClient, tayfaID, x string) map[string]error {
	out := map[string]error{}
	_, out["CreateQuestion"] = c.CreateQuestion(ctx, &corev1.CreateQuestionRequest{TayfaId: tayfaID, Title: "Yeni bir soru burada", ClientRequestId: id.New().String()})
	_, out["GetQuestion"] = c.GetQuestion(ctx, &corev1.GetQuestionRequest{TayfaId: tayfaID, QuestionId: x})
	_, out["ListQuestions"] = c.ListQuestions(ctx, &corev1.ListQuestionsRequest{TayfaId: tayfaID, Query: "kamp"})
	_, out["UpdateQuestion"] = c.UpdateQuestion(ctx, &corev1.UpdateQuestionRequest{TayfaId: tayfaID, QuestionId: x, Title: "Değişen soru başlığı", UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}}})
	_, out["DeleteQuestion"] = c.DeleteQuestion(ctx, &corev1.DeleteQuestionRequest{TayfaId: tayfaID, QuestionId: x})
	_, out["RestoreQuestion"] = c.RestoreQuestion(ctx, &corev1.RestoreQuestionRequest{TayfaId: tayfaID, QuestionId: x})
	_, out["CreateAnswer"] = c.CreateAnswer(ctx, &corev1.CreateAnswerRequest{TayfaId: tayfaID, QuestionId: x, Body: "Cevap", ClientRequestId: id.New().String()})
	_, out["ListAnswers"] = c.ListAnswers(ctx, &corev1.ListAnswersRequest{TayfaId: tayfaID, QuestionId: x})
	_, out["UpdateAnswer"] = c.UpdateAnswer(ctx, &corev1.UpdateAnswerRequest{TayfaId: tayfaID, AnswerId: x, Body: "Değişti"})
	_, out["DeleteAnswer"] = c.DeleteAnswer(ctx, &corev1.DeleteAnswerRequest{TayfaId: tayfaID, AnswerId: x})
	_, out["RestoreAnswer"] = c.RestoreAnswer(ctx, &corev1.RestoreAnswerRequest{TayfaId: tayfaID, AnswerId: x})
	_, out["SetAnswerVote"] = c.SetAnswerVote(ctx, &corev1.SetAnswerVoteRequest{TayfaId: tayfaID, AnswerId: x, Voted: true})
	_, out["ListTags"] = c.ListTags(ctx, &corev1.ListTagsRequest{TayfaId: tayfaID})
	_, out["DeleteTag"] = c.DeleteTag(ctx, &corev1.DeleteTagRequest{TayfaId: tayfaID, TagId: x})
	return out
}

// qaIDField is the ID field each method takes besides tayfa_id.
var qaIDField = map[string]string{
	"GetQuestion": "question_id", "UpdateQuestion": "question_id", "DeleteQuestion": "question_id",
	"RestoreQuestion": "question_id", "CreateAnswer": "question_id", "ListAnswers": "question_id",
	"UpdateAnswer": "answer_id", "DeleteAnswer": "answer_id", "RestoreAnswer": "answer_id", "SetAnswerVote": "answer_id",
	"DeleteTag": "tag_id",
}

// qaServerClient calls a server directly, as a client.
type qaServerClient struct{ s corev1.QAServiceServer }

func (c qaServerClient) CreateQuestion(ctx context.Context, r *corev1.CreateQuestionRequest, _ ...grpc.CallOption) (*corev1.CreateQuestionResponse, error) {
	return c.s.CreateQuestion(ctx, r)
}
func (c qaServerClient) GetQuestion(ctx context.Context, r *corev1.GetQuestionRequest, _ ...grpc.CallOption) (*corev1.GetQuestionResponse, error) {
	return c.s.GetQuestion(ctx, r)
}
func (c qaServerClient) ListQuestions(ctx context.Context, r *corev1.ListQuestionsRequest, _ ...grpc.CallOption) (*corev1.ListQuestionsResponse, error) {
	return c.s.ListQuestions(ctx, r)
}
func (c qaServerClient) UpdateQuestion(ctx context.Context, r *corev1.UpdateQuestionRequest, _ ...grpc.CallOption) (*corev1.UpdateQuestionResponse, error) {
	return c.s.UpdateQuestion(ctx, r)
}
func (c qaServerClient) DeleteQuestion(ctx context.Context, r *corev1.DeleteQuestionRequest, _ ...grpc.CallOption) (*corev1.DeleteQuestionResponse, error) {
	return c.s.DeleteQuestion(ctx, r)
}
func (c qaServerClient) RestoreQuestion(ctx context.Context, r *corev1.RestoreQuestionRequest, _ ...grpc.CallOption) (*corev1.RestoreQuestionResponse, error) {
	return c.s.RestoreQuestion(ctx, r)
}
func (c qaServerClient) CreateAnswer(ctx context.Context, r *corev1.CreateAnswerRequest, _ ...grpc.CallOption) (*corev1.CreateAnswerResponse, error) {
	return c.s.CreateAnswer(ctx, r)
}
func (c qaServerClient) ListAnswers(ctx context.Context, r *corev1.ListAnswersRequest, _ ...grpc.CallOption) (*corev1.ListAnswersResponse, error) {
	return c.s.ListAnswers(ctx, r)
}
func (c qaServerClient) UpdateAnswer(ctx context.Context, r *corev1.UpdateAnswerRequest, _ ...grpc.CallOption) (*corev1.UpdateAnswerResponse, error) {
	return c.s.UpdateAnswer(ctx, r)
}
func (c qaServerClient) DeleteAnswer(ctx context.Context, r *corev1.DeleteAnswerRequest, _ ...grpc.CallOption) (*corev1.DeleteAnswerResponse, error) {
	return c.s.DeleteAnswer(ctx, r)
}
func (c qaServerClient) RestoreAnswer(ctx context.Context, r *corev1.RestoreAnswerRequest, _ ...grpc.CallOption) (*corev1.RestoreAnswerResponse, error) {
	return c.s.RestoreAnswer(ctx, r)
}
func (c qaServerClient) SetAnswerVote(ctx context.Context, r *corev1.SetAnswerVoteRequest, _ ...grpc.CallOption) (*corev1.SetAnswerVoteResponse, error) {
	return c.s.SetAnswerVote(ctx, r)
}
func (c qaServerClient) ListTags(ctx context.Context, r *corev1.ListTagsRequest, _ ...grpc.CallOption) (*corev1.ListTagsResponse, error) {
	return c.s.ListTags(ctx, r)
}
func (c qaServerClient) DeleteTag(ctx context.Context, r *corev1.DeleteTagRequest, _ ...grpc.CallOption) (*corev1.DeleteTagResponse, error) {
	return c.s.DeleteTag(ctx, r)
}

func TestQAServerNeedsCaller(t *testing.T) {
	f := &fakeQA{}
	c := qaServerClient{NewQAServer(f, discard)}
	methods := qaMethods(context.Background(), c, id.New().String(), id.New().String())
	if len(methods) != len(corev1.QAService_ServiceDesc.Methods) {
		t.Fatalf("test %d metod çağırıyor, servis %d", len(methods), len(corev1.QAService_ServiceDesc.Methods))
	}
	for method, err := range methods {
		if code, info := infoOf(t, err); code != codes.Unauthenticated || info != nil {
			t.Errorf("%s: %v", method, err)
		}
	}
	if f.calls != 0 {
		t.Fatal("token'sız istek app katmanına ulaştı")
	}
}

func TestQAServerValidatesIDs(t *testing.T) {
	f := &fakeQA{}
	c := qaServerClient{NewQAServer(f, discard)}
	ctx := callerCtx(id.New())
	fieldOf := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}
	good := id.New().String()
	for _, bad := range []string{"", "abc", "' OR 1=1 --"} {
		for method, err := range qaMethods(ctx, c, bad, good) {
			if got := fieldOf(err); got != "tayfa_id" {
				t.Errorf("%s tayfa %q: %s", method, bad, got)
			}
		}
		for method, err := range qaMethods(ctx, c, good, bad) {
			want, ok := qaIDField[method]
			if !ok {
				continue
			}
			if got := fieldOf(err); got != want {
				t.Errorf("%s %q: %s, %s bekleniyordu", method, bad, got, want)
			}
		}
	}
	// Only the three methods without a second ID got through, once per bad
	// value, and always with the valid IDs.
	if f.calls != 9 || f.tayfa.String() != good || f.x != (id.ID{}) {
		t.Fatalf("geçersiz kimlik app katmanına ulaştı: %d", f.calls)
	}
}

func TestQAServerConverts(t *testing.T) {
	user, tid, qid, aid := id.New(), id.New(), id.New(), id.New()
	now := time.Now().UTC()
	f := &fakeQA{
		question: qa.Question{ID: qid, TayfaID: tid, Title: "Başlık burada", Body: "Gövde", Tags: []qa.Tag{{ID: id.New(), Name: "Çadır"}}, AnswerCount: 2, CreatedAt: now, UpdatedAt: now},
		answer:   qa.Answer{ID: aid, QuestionID: qid, TayfaID: tid, AuthorID: user, Body: "Cevap", VoteCount: 4, Voted: true, CreatedAt: now, UpdatedAt: now},
	}
	srv := NewQAServer(f, discard)
	ctx := callerCtx(user)

	crid := id.New().String()
	resp, err := srv.CreateQuestion(ctx, &corev1.CreateQuestionRequest{TayfaId: tid.String(), Title: "T", Body: "B", Tags: []string{"a", "b"}, ClientRequestId: crid})
	if err != nil || f.user != user || f.tayfa != tid || f.createQ.Title != "T" || len(f.createQ.Tags) != 2 || f.createQ.ClientRequestID != crid {
		t.Fatalf("oluşturma: %+v %v", f.createQ, err)
	}
	p := resp.GetQuestion()
	if p.GetId() != qid.String() || p.GetAuthorUserId() != "" || p.GetAnswerCount() != 2 || len(p.GetTags()) != 1 || p.GetTags()[0].GetName() != "Çadır" ||
		!p.GetCreateTime().AsTime().Equal(now) {
		t.Fatalf("soru = %v", p)
	}
	if _, err := srv.UpdateQuestion(ctx, &corev1.UpdateQuestionRequest{TayfaId: tid.String(), QuestionId: qid.String(), Tags: []string{"x"}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"tags"}}}); err != nil ||
		f.x != qid || len(f.updateQ.Fields) != 1 || f.updateQ.Fields[0] != "tags" || f.updateQ.Tags[0] != "x" {
		t.Fatalf("düzenleme: %+v %v", f.updateQ, err)
	}
	tagID := id.New().String()
	list, err := srv.ListQuestions(ctx, &corev1.ListQuestionsRequest{TayfaId: tid.String(), Query: "İzmir", TagId: tagID, PageSize: 5, PageToken: "t"})
	if err != nil || f.list != (qa.ListQuestionsInput{Query: "İzmir", TagID: tagID, PageSize: 5, PageToken: "t"}) || len(list.GetQuestions()) != 1 || list.GetNextPageToken() != "sonraki" {
		t.Fatalf("liste: %+v %v", f.list, err)
	}
	a, err := srv.SetAnswerVote(ctx, &corev1.SetAnswerVoteRequest{TayfaId: tid.String(), AnswerId: aid.String(), Voted: true})
	if err != nil || !f.voted || f.x != aid || a.GetAnswer().GetVoteCount() != 4 || !a.GetAnswer().GetVoted() || a.GetAnswer().GetAuthorUserId() != user.String() ||
		a.GetAnswer().GetQuestionId() != qid.String() {
		t.Fatalf("oy: %v %v", a, err)
	}
	if _, err := srv.CreateAnswer(ctx, &corev1.CreateAnswerRequest{TayfaId: tid.String(), QuestionId: qid.String(), Body: "B", ClientRequestId: crid}); err != nil ||
		f.createA != (qa.CreateAnswerInput{Body: "B", ClientRequestID: crid}) || f.x != qid {
		t.Fatalf("cevap: %+v %v", f.createA, err)
	}
	tags, err := srv.ListTags(ctx, &corev1.ListTagsRequest{TayfaId: tid.String()})
	if err != nil || len(tags.GetTags()) != 1 || tags.GetTags()[0].GetQuestionCount() != 3 {
		t.Fatalf("etiketler: %v %v", tags, err)
	}

	for appErr, reason := range map[error]corev1.ErrorReason{
		apperr.ErrModuleDisabled:   corev1.ErrorReason_ERROR_REASON_MODULE_DISABLED,
		apperr.ErrQuestionNotFound: corev1.ErrorReason_ERROR_REASON_QUESTION_NOT_FOUND,
		apperr.ErrOwnAnswerVote:    corev1.ErrorReason_ERROR_REASON_OWN_ANSWER_VOTE,
	} {
		f.err = appErr
		for method, err := range qaMethods(ctx, qaServerClient{srv}, tid.String(), qid.String()) {
			if _, info := infoOf(t, err); info.GetReason() != reason.String() {
				t.Errorf("%s: %v", method, err)
			}
		}
	}
}

// TestQAServiceBehindGuard serves the real services behind auth.Guard, as
// cmd/core does.
func TestQAServiceBehindGuard(t *testing.T) {
	key, err := auth.ParseSigningKey("test", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-test", Audience: "tayfam-api", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{key.ID: key.Public().Key}, Issuer: "tayfam-test", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	pool := dbtest.New(t, "core", coredb.Migrations())
	tayfas, err := tayfa.New(tayfa.Config{Pool: pool, Log: discard})
	if err != nil {
		t.Fatal(err)
	}
	app, err := qa.New(qa.Config{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(auth.NewGuard(v).ServerOptions()...)
	corev1.RegisterTayfaServiceServer(s, NewTayfaServer(tayfas, discard))
	corev1.RegisterQAServiceServer(s, NewQAServer(app, discard))
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	as := func(user id.ID) context.Context {
		tok, _, err := signer.Issue(user.String(), id.New().String())
		if err != nil {
			t.Fatal(err)
		}
		return auth.WithToken(context.Background(), tok)
	}
	tc, qc := corev1.NewTayfaServiceClient(conn), corev1.NewQAServiceClient(conn)

	owner, stranger := id.New(), id.New()
	created, err := tc.CreateTayfa(as(owner), &corev1.CreateTayfaRequest{Name: "Sahibin Tayfası", ClientRequestId: id.New().String()})
	if err != nil {
		t.Fatal(err)
	}
	tid := created.GetTayfa().GetId()
	q, err := qc.CreateQuestion(as(owner), &corev1.CreateQuestionRequest{TayfaId: tid, Title: "IŞIK nereden alınır?", Tags: []string{"Ekipman"}, ClientRequestId: id.New().String()})
	if err != nil {
		t.Fatal(err)
	}
	qid := q.GetQuestion().GetId()
	found, err := qc.ListQuestions(as(owner), &corev1.ListQuestionsRequest{TayfaId: tid, Query: "ışık"})
	if err != nil || len(found.GetQuestions()) != 1 || found.GetQuestions()[0].GetId() != qid {
		t.Fatalf("arama: %v %v", found, err)
	}

	for method, err := range qaMethods(context.Background(), qc, tid, qid) {
		if code, info := infoOf(t, err); code != codes.Unauthenticated || info != nil {
			t.Errorf("token'sız %s: %v", method, err)
		}
	}
	for method, err := range qaMethods(as(stranger), qc, tid, qid) {
		code, info := infoOf(t, err)
		if code != codes.NotFound || info.GetReason() != corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String() {
			t.Errorf("yabancı %s: %v", method, err)
		}
	}
	got, err := qc.GetQuestion(as(owner), &corev1.GetQuestionRequest{TayfaId: tid, QuestionId: qid})
	if err != nil || got.GetQuestion().GetTitle() != "IŞIK nereden alınır?" || len(got.GetQuestion().GetTags()) != 1 {
		t.Fatalf("soru değişti: %v %v", got, err)
	}
}
