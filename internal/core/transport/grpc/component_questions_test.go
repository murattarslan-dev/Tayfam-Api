package grpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/questions"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeQuestions struct {
	err      error
	ref      component.Ref
	in       questions.Input
	question id.ID
	flag     bool
	list     questions.List
	out      questions.Question
	calls    int
	last     string
}

func (f *fakeQuestions) List(_ context.Context, ref component.Ref) (questions.List, error) {
	f.ref, f.calls, f.last = ref, f.calls+1, "list"
	return f.list, f.err
}

func (f *fakeQuestions) Ask(_ context.Context, ref component.Ref, in questions.Input) (questions.Question, error) {
	f.ref, f.in, f.calls, f.last = ref, in, f.calls+1, "ask"
	return f.out, f.err
}

func (f *fakeQuestions) Remove(_ context.Context, ref component.Ref, q id.ID) error {
	f.ref, f.question, f.calls, f.last = ref, q, f.calls+1, "remove"
	return f.err
}

func (f *fakeQuestions) SetVote(_ context.Context, ref component.Ref, q id.ID, voted bool) error {
	f.ref, f.question, f.flag, f.calls, f.last = ref, q, voted, f.calls+1, "vote"
	return f.err
}

func (f *fakeQuestions) SetAnswered(_ context.Context, ref component.Ref, q id.ID, answered bool) error {
	f.ref, f.question, f.flag, f.calls, f.last = ref, q, answered, f.calls+1, "answered"
	return f.err
}

func TestQuestionsComponentMethods(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	author, anon, named := id.New(), id.New(), id.New()
	f := &fakeQuestions{list: questions.List{
		AnonymousAllowed: true, CallerCanAsk: true, MaxPerPerson: 20,
		Questions: []questions.Question{
			// The app layer never fills Author on an anonymous question;
			// the transport drops it anyway.
			{ID: anon, Body: "Anonim?", Anonymous: true, Author: author, CallerIsAuthor: true, VoteCount: 3, CallerVoted: true, CreatedAt: at, CallerCanRemove: true},
			{ID: named, Body: "Açık?", Author: author, AnsweredAt: at.Add(time.Hour), CreatedAt: at},
		},
	}}
	s := NewQuestionsComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "questions"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "questions"}

	got, err := s.ListEventQuestions(ctx, &corev1.ListEventQuestionsRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if f.ref != want || !got.GetAnonymousAllowed() || !got.GetCallerCanAsk() || got.GetCallerCanMarkAnswered() ||
		got.GetMaxQuestionsPerPerson() != 20 || len(got.GetQuestions()) != 2 {
		t.Fatalf("okuma: %v", got)
	}
	a, n := got.GetQuestions()[0], got.GetQuestions()[1]
	if a.GetId() != anon.String() || a.GetAuthorId() != "" || !a.GetAnonymous() || !a.GetCallerIsAuthor() || a.GetVoteCount() != 3 ||
		!a.GetCallerVoted() || a.GetAnswerTime() != nil || !a.GetCreateTime().AsTime().Equal(at) || !a.GetCallerCanRemove() {
		t.Fatalf("anonim soru: %v", a)
	}
	if n.GetAuthorId() != author.String() || n.GetAnonymous() || !n.GetAnswerTime().AsTime().Equal(at.Add(time.Hour)) {
		t.Fatalf("açık soru: %v", n)
	}

	crid := id.New().String()
	f.out = questions.Question{ID: anon, Body: "Soru?", Anonymous: true, CallerIsAuthor: true, CreatedAt: at}
	resp, err := s.AskEventQuestion(ctx, &corev1.AskEventQuestionRequest{Ref: ref, Body: "Soru?", Anonymous: true, ClientRequestId: crid})
	if err != nil || f.last != "ask" || f.in != (questions.Input{Body: "Soru?", Anonymous: true, ClientRequestID: crid}) ||
		resp.GetQuestion().GetId() != anon.String() || resp.GetQuestion().GetAuthorId() != "" {
		t.Fatalf("sorma: %v %+v %v", err, f.in, resp)
	}
	q := id.New()
	if _, err := s.RemoveEventQuestion(ctx, &corev1.RemoveEventQuestionRequest{Ref: ref, QuestionId: q.String()}); err != nil || f.last != "remove" || f.question != q {
		t.Fatalf("kaldırma: %v %+v", err, f)
	}
	if _, err := s.SetEventQuestionVote(ctx, &corev1.SetEventQuestionVoteRequest{Ref: ref, QuestionId: q.String(), Voted: true}); err != nil || f.last != "vote" || !f.flag {
		t.Fatalf("oy: %v %+v", err, f)
	}
	if _, err := s.SetEventQuestionAnswered(ctx, &corev1.SetEventQuestionAnsweredRequest{Ref: ref, QuestionId: q.String()}); err != nil || f.last != "answered" || f.flag {
		t.Fatalf("işaret: %v %+v", err, f)
	}

	// The app's errors keep their reasons.
	for err, want := range map[error]struct {
		code   codes.Code
		reason corev1.ErrorReason
	}{
		apperr.ErrEventNotGoing:     {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_NOT_GOING},
		apperr.ErrEventItemNotFound: {codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_ITEM_NOT_FOUND},
		apperr.ErrComponentFrozen:   {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN},
	} {
		f.err = err
		_, got := s.SetEventQuestionVote(ctx, &corev1.SetEventQuestionVoteRequest{Ref: ref, QuestionId: q.String(), Voted: true})
		if code, info := infoOf(t, got); code != want.code || info.GetReason() != want.reason.String() {
			t.Errorf("%v: %v", err, got)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestQuestionsComponentValidation(t *testing.T) {
	f := &fakeQuestions{}
	s := NewQuestionsComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "questions"}
	crid := id.New().String()

	for name, req := range map[string]*corev1.AskEventQuestionRequest{
		"başvurusuz":    {Body: "Soru?", ClientRequestId: crid},
		"bozuk anahtar": {Ref: &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "Questions"}, Body: "Soru?", ClientRequestId: crid},
		"boş":           {Ref: ref, ClientRequestId: crid},
		"çok uzun":      {Ref: ref, Body: strings.Repeat("a", maxQuestionBytes+1), ClientRequestId: crid},
		"istek kimliği": {Ref: ref, Body: "Soru?", ClientRequestId: "x"},
	} {
		if _, err := s.AskEventQuestion(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.RemoveEventQuestion(ctx, &corev1.RemoveEventQuestionRequest{Ref: ref, QuestionId: "x"}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("bozuk soru kaldırma: %v", err)
	}
	if _, err := s.SetEventQuestionVote(ctx, &corev1.SetEventQuestionVoteRequest{Ref: ref}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("sorusuz oy: %v", err)
	}
	if _, err := s.SetEventQuestionAnswered(ctx, &corev1.SetEventQuestionAnsweredRequest{QuestionId: id.New().String()}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("başvurusuz işaret: %v", err)
	}
	if _, err := s.ListEventQuestions(ctx, &corev1.ListEventQuestionsRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("başvurusuz okuma: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.ListEventQuestions(context.Background(), &corev1.ListEventQuestionsRequest{Ref: ref}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the questions component.
func TestQuestionsComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.QuestionsComponentService"]
	if !ok || len(info.Methods) != 5 {
		t.Fatalf("soru servisi: %v", info)
	}
}
