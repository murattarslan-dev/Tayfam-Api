package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/qa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// QA is what QAServer needs from the app layer.
type QA interface {
	CreateQuestion(ctx context.Context, user, tayfa id.ID, in qa.CreateQuestionInput) (qa.Question, error)
	GetQuestion(ctx context.Context, user, tayfa, question id.ID) (qa.Question, error)
	ListQuestions(ctx context.Context, user, tayfa id.ID, in qa.ListQuestionsInput) ([]qa.Question, string, error)
	UpdateQuestion(ctx context.Context, user, tayfa, question id.ID, in qa.UpdateQuestionInput) (qa.Question, error)
	DeleteQuestion(ctx context.Context, user, tayfa, question id.ID) error
	RestoreQuestion(ctx context.Context, user, tayfa, question id.ID) (qa.Question, error)
	CreateAnswer(ctx context.Context, user, tayfa, question id.ID, in qa.CreateAnswerInput) (qa.Answer, error)
	ListAnswers(ctx context.Context, user, tayfa, question id.ID, pageSize int32, pageToken string) ([]qa.Answer, string, error)
	UpdateAnswer(ctx context.Context, user, tayfa, answer id.ID, body string) (qa.Answer, error)
	DeleteAnswer(ctx context.Context, user, tayfa, answer id.ID) error
	RestoreAnswer(ctx context.Context, user, tayfa, answer id.ID) (qa.Answer, error)
	SetVote(ctx context.Context, user, tayfa, answer id.ID, voted bool) (qa.Answer, error)
	ListTags(ctx context.Context, user, tayfa id.ID) ([]qa.Tag, error)
	DeleteTag(ctx context.Context, user, tayfa, tag id.ID) error
}

// QAServer implements corev1.QAServiceServer. The acting user always comes
// from the verified token (auth.Guard). Search queries are passed on and
// never logged.
type QAServer struct {
	corev1.UnimplementedQAServiceServer
	app QA
	log *slog.Logger
}

// NewQAServer returns a QAServer.
func NewQAServer(app QA, log *slog.Logger) *QAServer {
	return &QAServer{app: app, log: log}
}

// qaArgs reads the caller, tayfa_id and one more ID field.
func qaArgs(ctx context.Context, rawTayfa, raw, field string) (u, tayfa, x id.ID, err error) {
	if u, err = callerID(ctx); err != nil {
		return
	}
	if tayfa, err = tayfaID(rawTayfa); err != nil {
		return
	}
	x, err = parseID(raw, field)
	return
}

// CreateQuestion implements corev1.QAServiceServer.
func (s *QAServer) CreateQuestion(ctx context.Context, req *corev1.CreateQuestionRequest) (*corev1.CreateQuestionResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.CreateQuestion(ctx, user, t, qa.CreateQuestionInput{
		Title: req.GetTitle(), Body: req.GetBody(), Tags: req.GetTags(), ClientRequestID: req.GetClientRequestId(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateQuestionResponse{Question: questionPB(v)}, nil
}

// GetQuestion implements corev1.QAServiceServer.
func (s *QAServer) GetQuestion(ctx context.Context, req *corev1.GetQuestionRequest) (*corev1.GetQuestionResponse, error) {
	user, t, q, err := qaArgs(ctx, req.GetTayfaId(), req.GetQuestionId(), "question_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.GetQuestion(ctx, user, t, q)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetQuestionResponse{Question: questionPB(v)}, nil
}

// ListQuestions implements corev1.QAServiceServer.
func (s *QAServer) ListQuestions(ctx context.Context, req *corev1.ListQuestionsRequest) (*corev1.ListQuestionsResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.ListQuestions(ctx, user, t, qa.ListQuestionsInput{
		Query: req.GetQuery(), TagID: req.GetTagId(), PageSize: req.GetPageSize(), PageToken: req.GetPageToken(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	resp := &corev1.ListQuestionsResponse{NextPageToken: next, Questions: make([]*corev1.Question, 0, len(list))}
	for _, v := range list {
		resp.Questions = append(resp.Questions, questionPB(v))
	}
	return resp, nil
}

// UpdateQuestion implements corev1.QAServiceServer.
func (s *QAServer) UpdateQuestion(ctx context.Context, req *corev1.UpdateQuestionRequest) (*corev1.UpdateQuestionResponse, error) {
	user, t, q, err := qaArgs(ctx, req.GetTayfaId(), req.GetQuestionId(), "question_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.UpdateQuestion(ctx, user, t, q, qa.UpdateQuestionInput{
		Fields: req.GetUpdateMask().GetPaths(), Title: req.GetTitle(), Body: req.GetBody(), Tags: req.GetTags(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateQuestionResponse{Question: questionPB(v)}, nil
}

// DeleteQuestion implements corev1.QAServiceServer.
func (s *QAServer) DeleteQuestion(ctx context.Context, req *corev1.DeleteQuestionRequest) (*corev1.DeleteQuestionResponse, error) {
	user, t, q, err := qaArgs(ctx, req.GetTayfaId(), req.GetQuestionId(), "question_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteQuestion(ctx, user, t, q); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteQuestionResponse{}, nil
}

// RestoreQuestion implements corev1.QAServiceServer.
func (s *QAServer) RestoreQuestion(ctx context.Context, req *corev1.RestoreQuestionRequest) (*corev1.RestoreQuestionResponse, error) {
	user, t, q, err := qaArgs(ctx, req.GetTayfaId(), req.GetQuestionId(), "question_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.RestoreQuestion(ctx, user, t, q)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RestoreQuestionResponse{Question: questionPB(v)}, nil
}

// CreateAnswer implements corev1.QAServiceServer.
func (s *QAServer) CreateAnswer(ctx context.Context, req *corev1.CreateAnswerRequest) (*corev1.CreateAnswerResponse, error) {
	user, t, q, err := qaArgs(ctx, req.GetTayfaId(), req.GetQuestionId(), "question_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.CreateAnswer(ctx, user, t, q, qa.CreateAnswerInput{Body: req.GetBody(), ClientRequestID: req.GetClientRequestId()})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateAnswerResponse{Answer: answerPB(v)}, nil
}

// ListAnswers implements corev1.QAServiceServer.
func (s *QAServer) ListAnswers(ctx context.Context, req *corev1.ListAnswersRequest) (*corev1.ListAnswersResponse, error) {
	user, t, q, err := qaArgs(ctx, req.GetTayfaId(), req.GetQuestionId(), "question_id")
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.ListAnswers(ctx, user, t, q, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	resp := &corev1.ListAnswersResponse{NextPageToken: next, Answers: make([]*corev1.Answer, 0, len(list))}
	for _, v := range list {
		resp.Answers = append(resp.Answers, answerPB(v))
	}
	return resp, nil
}

// UpdateAnswer implements corev1.QAServiceServer.
func (s *QAServer) UpdateAnswer(ctx context.Context, req *corev1.UpdateAnswerRequest) (*corev1.UpdateAnswerResponse, error) {
	user, t, a, err := qaArgs(ctx, req.GetTayfaId(), req.GetAnswerId(), "answer_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.UpdateAnswer(ctx, user, t, a, req.GetBody())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateAnswerResponse{Answer: answerPB(v)}, nil
}

// DeleteAnswer implements corev1.QAServiceServer.
func (s *QAServer) DeleteAnswer(ctx context.Context, req *corev1.DeleteAnswerRequest) (*corev1.DeleteAnswerResponse, error) {
	user, t, a, err := qaArgs(ctx, req.GetTayfaId(), req.GetAnswerId(), "answer_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteAnswer(ctx, user, t, a); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteAnswerResponse{}, nil
}

// RestoreAnswer implements corev1.QAServiceServer.
func (s *QAServer) RestoreAnswer(ctx context.Context, req *corev1.RestoreAnswerRequest) (*corev1.RestoreAnswerResponse, error) {
	user, t, a, err := qaArgs(ctx, req.GetTayfaId(), req.GetAnswerId(), "answer_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.RestoreAnswer(ctx, user, t, a)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RestoreAnswerResponse{Answer: answerPB(v)}, nil
}

// SetAnswerVote implements corev1.QAServiceServer.
func (s *QAServer) SetAnswerVote(ctx context.Context, req *corev1.SetAnswerVoteRequest) (*corev1.SetAnswerVoteResponse, error) {
	user, t, a, err := qaArgs(ctx, req.GetTayfaId(), req.GetAnswerId(), "answer_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.SetVote(ctx, user, t, a, req.GetVoted())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetAnswerVoteResponse{Answer: answerPB(v)}, nil
}

// ListTags implements corev1.QAServiceServer.
func (s *QAServer) ListTags(ctx context.Context, req *corev1.ListTagsRequest) (*corev1.ListTagsResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	tags, err := s.app.ListTags(ctx, user, t)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListTagsResponse{Tags: tagsPB(tags)}, nil
}

// DeleteTag implements corev1.QAServiceServer.
func (s *QAServer) DeleteTag(ctx context.Context, req *corev1.DeleteTagRequest) (*corev1.DeleteTagResponse, error) {
	user, t, tag, err := qaArgs(ctx, req.GetTayfaId(), req.GetTagId(), "tag_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteTag(ctx, user, t, tag); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteTagResponse{}, nil
}

func questionPB(v qa.Question) *corev1.Question {
	return &corev1.Question{
		Id: v.ID.String(), TayfaId: v.TayfaID.String(), AuthorUserId: optionalID(v.AuthorID),
		Title: v.Title, Body: v.Body, Tags: tagsPB(v.Tags), AnswerCount: v.AnswerCount,
		CreateTime: timestamppb.New(v.CreatedAt), UpdateTime: timestamppb.New(v.UpdatedAt),
	}
}

func answerPB(v qa.Answer) *corev1.Answer {
	return &corev1.Answer{
		Id: v.ID.String(), QuestionId: v.QuestionID.String(), TayfaId: v.TayfaID.String(),
		AuthorUserId: optionalID(v.AuthorID), Body: v.Body, VoteCount: v.VoteCount, Voted: v.Voted,
		CreateTime: timestamppb.New(v.CreatedAt), UpdateTime: timestamppb.New(v.UpdatedAt),
	}
}

func tagsPB(tags []qa.Tag) []*corev1.Tag {
	out := make([]*corev1.Tag, 0, len(tags))
	for _, t := range tags {
		out = append(out, &corev1.Tag{Id: t.ID.String(), Name: t.Name, QuestionCount: t.QuestionCount})
	}
	return out
}
