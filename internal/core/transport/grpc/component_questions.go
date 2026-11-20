package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/questions"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// maxQuestionBytes bounds a question's body before the app layer reads it
// (questions.BodyMax characters of up to four bytes, with room for the
// whitespace it folds).
const maxQuestionBytes = (questions.BodyMax + 64) * 4

// Questions is what QuestionsComponentServer needs from the app layer
// (questions.Service).
type Questions interface {
	List(ctx context.Context, ref component.Ref) (questions.List, error)
	Ask(ctx context.Context, ref component.Ref, in questions.Input) (questions.Question, error)
	Remove(ctx context.Context, ref component.Ref, question id.ID) error
	SetVote(ctx context.Context, ref component.Ref, question id.ID, voted bool) error
	SetAnswered(ctx context.Context, ref component.Ref, question id.ID, answered bool) error
}

// QuestionsComponentServer implements corev1.QuestionsComponentServiceServer
// (issue #256; ADR-0025 §4, §5). Every method needs a verified token
// (auth.Guard); the acting user is always the token's. Question texts are
// never logged, and the author of an anonymous question never leaves the
// app layer.
type QuestionsComponentServer struct {
	corev1.UnimplementedQuestionsComponentServiceServer
	app Questions
	log *slog.Logger
}

// NewQuestionsComponentServer returns a QuestionsComponentServer.
func NewQuestionsComponentServer(app Questions, log *slog.Logger) *QuestionsComponentServer {
	return &QuestionsComponentServer{app: app, log: log}
}

// registerQuestionsComponent is the questions component's line in the
// registry (componentRegistrars).
func registerQuestionsComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterQuestionsComponentServiceServer(srv, NewQuestionsComponentServer(questions.New(fw), log))
	return nil
}

func eventQuestionPB(q questions.Question) *corev1.EventQuestion {
	out := &corev1.EventQuestion{
		Id: q.ID.String(), Body: q.Body, Anonymous: q.Anonymous, AuthorId: optionalID(q.Author),
		CallerIsAuthor: q.CallerIsAuthor, VoteCount: q.VoteCount, CallerVoted: q.CallerVoted,
		CreateTime: timestamppb.New(q.CreatedAt), CallerCanRemove: q.CallerCanRemove,
	}
	if q.Anonymous {
		// Never the author of an anonymous question, whatever the app
		// layer holds.
		out.AuthorId = ""
	}
	if !q.AnsweredAt.IsZero() {
		out.AnswerTime = timestamppb.New(q.AnsweredAt)
	}
	return out
}

// questionRef reads a request's component and question.
func questionRef(ctx context.Context, ref *corev1.ComponentRef, question string) (component.Ref, id.ID, error) {
	r, err := componentRef(ctx, ref)
	if err != nil {
		return component.Ref{}, id.ID{}, err
	}
	q, ok := id.Parse(question)
	if !ok {
		return component.Ref{}, id.ID{}, validation("question_id")
	}
	return r, q, nil
}

// ListQuestions implements corev1.QuestionsComponentServiceServer.
func (s *QuestionsComponentServer) ListQuestions(ctx context.Context, req *corev1.ListQuestionsRequest) (*corev1.ListQuestionsResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	l, err := s.app.List(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListQuestionsResponse{
		Questions: make([]*corev1.EventQuestion, 0, len(l.Questions)), AnonymousAllowed: l.AnonymousAllowed,
		CallerCanAsk: l.CallerCanAsk, CallerCanMarkAnswered: l.CallerCanMarkAnswered, MaxQuestionsPerPerson: l.MaxPerPerson,
	}
	for _, q := range l.Questions {
		out.Questions = append(out.Questions, eventQuestionPB(q))
	}
	return out, nil
}

// AskQuestion implements corev1.QuestionsComponentServiceServer. The body
// goes to the app layer only; it is never logged.
func (s *QuestionsComponentServer) AskQuestion(ctx context.Context, req *corev1.AskQuestionRequest) (*corev1.AskQuestionResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if req.GetBody() == "" || len(req.GetBody()) > maxQuestionBytes {
		return nil, validation("body")
	}
	if _, ok := id.Parse(req.GetClientRequestId()); !ok {
		return nil, validation("client_request_id")
	}
	q, err := s.app.Ask(ctx, ref, questions.Input{
		Body: req.GetBody(), Anonymous: req.GetAnonymous(), ClientRequestID: req.GetClientRequestId(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AskQuestionResponse{Question: eventQuestionPB(q)}, nil
}

// RemoveQuestion implements corev1.QuestionsComponentServiceServer.
func (s *QuestionsComponentServer) RemoveQuestion(ctx context.Context, req *corev1.RemoveQuestionRequest) (*corev1.RemoveQuestionResponse, error) {
	ref, q, err := questionRef(ctx, req.GetRef(), req.GetQuestionId())
	if err != nil {
		return nil, err
	}
	if err := s.app.Remove(ctx, ref, q); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveQuestionResponse{}, nil
}

// SetQuestionVote implements corev1.QuestionsComponentServiceServer.
func (s *QuestionsComponentServer) SetQuestionVote(ctx context.Context, req *corev1.SetQuestionVoteRequest) (*corev1.SetQuestionVoteResponse, error) {
	ref, q, err := questionRef(ctx, req.GetRef(), req.GetQuestionId())
	if err != nil {
		return nil, err
	}
	if err := s.app.SetVote(ctx, ref, q, req.GetVoted()); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetQuestionVoteResponse{}, nil
}

// SetQuestionAnswered implements corev1.QuestionsComponentServiceServer.
func (s *QuestionsComponentServer) SetQuestionAnswered(ctx context.Context, req *corev1.SetQuestionAnsweredRequest) (*corev1.SetQuestionAnsweredResponse, error) {
	ref, q, err := questionRef(ctx, req.GetRef(), req.GetQuestionId())
	if err != nil {
		return nil, err
	}
	if err := s.app.SetAnswered(ctx, ref, q, req.GetAnswered()); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetQuestionAnsweredResponse{}, nil
}
