package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/poll"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Polls is what PollServer needs from the app layer.
type Polls interface {
	Create(ctx context.Context, user, tayfa id.ID, in poll.CreateInput) (poll.View, error)
	Get(ctx context.Context, user, tayfa, p id.ID) (poll.View, error)
	List(ctx context.Context, user, tayfa id.ID, event string, pageSize int32, pageToken string) ([]poll.View, string, error)
	Vote(ctx context.Context, user, tayfa, p id.ID, options []string) (poll.View, error)
	Withdraw(ctx context.Context, user, tayfa, p id.ID) (poll.View, error)
	ListVoters(ctx context.Context, user, tayfa, p id.ID, option string, pageSize int32, pageToken string) ([]poll.Voter, string, error)
	Close(ctx context.Context, user, tayfa, p id.ID) (poll.View, error)
	Delete(ctx context.Context, user, tayfa, p id.ID) error
}

// PollServer implements corev1.PollServiceServer. The acting user always
// comes from the verified token (auth.Guard); a poll view only ever carries
// the caller's own vote.
type PollServer struct {
	corev1.UnimplementedPollServiceServer
	app Polls
	log *slog.Logger
}

// NewPollServer returns a PollServer.
func NewPollServer(app Polls, log *slog.Logger) *PollServer {
	return &PollServer{app: app, log: log}
}

// pollArgs reads the caller and the tayfa_id and poll_id fields.
func pollArgs(ctx context.Context, rawTayfa, rawPoll string) (u, tayfa, p id.ID, err error) {
	if u, err = callerID(ctx); err != nil {
		return
	}
	if tayfa, err = tayfaID(rawTayfa); err != nil {
		return
	}
	p, err = parseID(rawPoll, "poll_id")
	return
}

// CreatePoll implements corev1.PollServiceServer.
func (s *PollServer) CreatePoll(ctx context.Context, req *corev1.CreatePollRequest) (*corev1.CreatePollResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	var closes time.Time
	if ct := req.GetCloseTime(); ct != nil {
		if ct.CheckValid() != nil {
			return nil, validation("close_time")
		}
		closes = ct.AsTime()
	}
	v, err := s.app.Create(ctx, user, t, poll.CreateInput{
		Question: req.GetQuestion(), Options: req.GetOptions(), Anonymous: req.GetAnonymous(),
		MultipleChoice: req.GetMultipleChoice(), ClosesAt: closes, EventID: req.GetEventId(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreatePollResponse{Poll: pollPB(v)}, nil
}

// GetPoll implements corev1.PollServiceServer.
func (s *PollServer) GetPoll(ctx context.Context, req *corev1.GetPollRequest) (*corev1.GetPollResponse, error) {
	user, t, p, err := pollArgs(ctx, req.GetTayfaId(), req.GetPollId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Get(ctx, user, t, p)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetPollResponse{Poll: pollPB(v)}, nil
}

// ListPolls implements corev1.PollServiceServer.
func (s *PollServer) ListPolls(ctx context.Context, req *corev1.ListPollsRequest) (*corev1.ListPollsResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.List(ctx, user, t, req.GetEventId(), req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	resp := &corev1.ListPollsResponse{NextPageToken: next, Polls: make([]*corev1.Poll, 0, len(list))}
	for _, v := range list {
		resp.Polls = append(resp.Polls, pollPB(v))
	}
	return resp, nil
}

// VotePoll implements corev1.PollServiceServer.
func (s *PollServer) VotePoll(ctx context.Context, req *corev1.VotePollRequest) (*corev1.VotePollResponse, error) {
	user, t, p, err := pollArgs(ctx, req.GetTayfaId(), req.GetPollId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Vote(ctx, user, t, p, req.GetOptionIds())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.VotePollResponse{Poll: pollPB(v)}, nil
}

// WithdrawPollVote implements corev1.PollServiceServer.
func (s *PollServer) WithdrawPollVote(ctx context.Context, req *corev1.WithdrawPollVoteRequest) (*corev1.WithdrawPollVoteResponse, error) {
	user, t, p, err := pollArgs(ctx, req.GetTayfaId(), req.GetPollId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Withdraw(ctx, user, t, p)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.WithdrawPollVoteResponse{Poll: pollPB(v)}, nil
}

// ListPollVoters implements corev1.PollServiceServer.
func (s *PollServer) ListPollVoters(ctx context.Context, req *corev1.ListPollVotersRequest) (*corev1.ListPollVotersResponse, error) {
	user, t, p, err := pollArgs(ctx, req.GetTayfaId(), req.GetPollId())
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.ListVoters(ctx, user, t, p, req.GetOptionId(), req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	resp := &corev1.ListPollVotersResponse{NextPageToken: next, Voters: make([]*corev1.PollVoter, 0, len(list))}
	for _, v := range list {
		resp.Voters = append(resp.Voters, &corev1.PollVoter{UserId: v.UserID.String(), VoteTime: timestamppb.New(v.VotedAt)})
	}
	return resp, nil
}

// ClosePoll implements corev1.PollServiceServer.
func (s *PollServer) ClosePoll(ctx context.Context, req *corev1.ClosePollRequest) (*corev1.ClosePollResponse, error) {
	user, t, p, err := pollArgs(ctx, req.GetTayfaId(), req.GetPollId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Close(ctx, user, t, p)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ClosePollResponse{Poll: pollPB(v)}, nil
}

// DeletePoll implements corev1.PollServiceServer.
func (s *PollServer) DeletePoll(ctx context.Context, req *corev1.DeletePollRequest) (*corev1.DeletePollResponse, error) {
	user, t, p, err := pollArgs(ctx, req.GetTayfaId(), req.GetPollId())
	if err != nil {
		return nil, err
	}
	if err := s.app.Delete(ctx, user, t, p); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeletePollResponse{}, nil
}

func pollPB(v poll.View) *corev1.Poll {
	p := &corev1.Poll{
		Id: v.ID.String(), TayfaId: v.TayfaID.String(), EventId: optionalID(v.EventID),
		CreatedByUserId: optionalID(v.CreatedBy), Question: v.Question,
		Anonymous: v.Anonymous, MultipleChoice: v.MultipleChoice, Closed: v.Closed,
		CreateTime: timestamppb.New(v.CreatedAt), VoterCount: v.VoterCount,
		Options:      make([]*corev1.PollOption, 0, len(v.Options)),
		OwnOptionIds: make([]string, 0, len(v.OwnOptionIDs)),
	}
	if !v.ClosesAt.IsZero() {
		p.CloseTime = timestamppb.New(v.ClosesAt)
	}
	if !v.ClosedAt.IsZero() {
		p.ClosedTime = timestamppb.New(v.ClosedAt)
	}
	for _, o := range v.Options {
		p.Options = append(p.Options, &corev1.PollOption{
			Id: o.ID.String(), Text: o.Text, Position: int32(o.Position), VoteCount: o.VoteCount, //nolint:gosec // 0–9
		})
	}
	for _, o := range v.OwnOptionIDs {
		p.OwnOptionIds = append(p.OwnOptionIds, o.String())
	}
	return p
}
