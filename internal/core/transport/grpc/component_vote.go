package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/vote"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// maxVoteLabelBytes bounds a free option before the app layer reads it
// (vote.LabelMax characters of up to four bytes, with room for the
// whitespace it folds).
const maxVoteLabelBytes = (vote.LabelMax + 64) * 4

// Votes is what VoteComponentServer needs from the app layer
// (vote.Service).
type Votes interface {
	Get(ctx context.Context, ref component.Ref) (vote.Vote, error)
	Cast(ctx context.Context, ref component.Ref, option id.ID) (vote.Vote, error)
	Withdraw(ctx context.Context, ref component.Ref) (vote.Vote, error)
	AddOption(ctx context.Context, ref component.Ref, label string) (vote.Vote, error)
	RemoveOption(ctx context.Context, ref component.Ref, option id.ID) (vote.Vote, error)
	Close(ctx context.Context, ref component.Ref) (vote.Vote, error)
}

// VoteComponentServer implements corev1.VoteComponentServiceServer (issue
// #254; ADR-0025 §4, §5; ADR-0037 §3). Every method needs a verified token
// (auth.Guard); the acting user is always the token's. Who voted for what
// never leaves the app layer, and option labels are never logged.
type VoteComponentServer struct {
	corev1.UnimplementedVoteComponentServiceServer
	app Votes
	log *slog.Logger
}

// NewVoteComponentServer returns a VoteComponentServer.
func NewVoteComponentServer(app Votes, log *slog.Logger) *VoteComponentServer {
	return &VoteComponentServer{app: app, log: log}
}

// registerVoteComponent is the vote component's line in the registry
// (componentRegistrars).
func registerVoteComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterVoteComponentServiceServer(srv, NewVoteComponentServer(vote.New(fw), log))
	return nil
}

func eventVotePB(v vote.Vote) *corev1.EventVote {
	out := &corev1.EventVote{
		Source: v.Source, Options: make([]*corev1.VoteOption, 0, len(v.Options)), VoterCount: v.VoterCount,
		CallerOptionId: optionalID(v.CallerOption), Closed: v.Closed, ResultsVisible: v.ResultsVisible,
		CallerCanVote: v.CallerCanVote, CallerCanEditOptions: v.CallerCanEditOptions, CallerCanClose: v.CallerCanClose,
		MaxOptions: v.MaxOptions,
	}
	if v.Closed {
		out.CloseTime = timestamppb.New(v.ClosedAt)
	}
	for _, o := range v.Options {
		po := &corev1.VoteOption{
			Id: o.ID.String(), Label: o.Label, UserId: optionalID(o.User), UploadId: optionalID(o.Upload),
		}
		if v.ResultsVisible {
			po.VoteCount, po.Winner = o.VoteCount, o.Winner
		}
		if o.Vehicle != nil {
			po.Vehicle = &corev1.VoteVehicle{
				Make: o.Vehicle.Make, Model: o.Vehicle.Model, ModelYear: o.Vehicle.ModelYear, Color: o.Vehicle.Color,
			}
		}
		out.Options = append(out.Options, po)
	}
	return out
}

// GetVote implements corev1.VoteComponentServiceServer.
func (s *VoteComponentServer) GetVote(ctx context.Context, req *corev1.GetVoteRequest) (*corev1.GetVoteResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetVoteResponse{Vote: eventVotePB(v)}, nil
}

// CastVote implements corev1.VoteComponentServiceServer.
func (s *VoteComponentServer) CastVote(ctx context.Context, req *corev1.CastVoteRequest) (*corev1.CastVoteResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	option, err := parseID(req.GetOptionId(), "option_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.Cast(ctx, ref, option)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CastVoteResponse{Vote: eventVotePB(v)}, nil
}

// WithdrawVote implements corev1.VoteComponentServiceServer.
func (s *VoteComponentServer) WithdrawVote(ctx context.Context, req *corev1.WithdrawVoteRequest) (*corev1.WithdrawVoteResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Withdraw(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.WithdrawVoteResponse{Vote: eventVotePB(v)}, nil
}

// AddVoteOption implements corev1.VoteComponentServiceServer.
func (s *VoteComponentServer) AddVoteOption(ctx context.Context, req *corev1.AddVoteOptionRequest) (*corev1.AddVoteOptionResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if len(req.GetLabel()) > maxVoteLabelBytes {
		return nil, validation("label")
	}
	v, err := s.app.AddOption(ctx, ref, req.GetLabel())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddVoteOptionResponse{Vote: eventVotePB(v)}, nil
}

// RemoveVoteOption implements corev1.VoteComponentServiceServer.
func (s *VoteComponentServer) RemoveVoteOption(ctx context.Context, req *corev1.RemoveVoteOptionRequest) (*corev1.RemoveVoteOptionResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	option, err := parseID(req.GetOptionId(), "option_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.RemoveOption(ctx, ref, option)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveVoteOptionResponse{Vote: eventVotePB(v)}, nil
}

// CloseVote implements corev1.VoteComponentServiceServer.
func (s *VoteComponentServer) CloseVote(ctx context.Context, req *corev1.CloseVoteRequest) (*corev1.CloseVoteResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Close(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CloseVoteResponse{Vote: eventVotePB(v)}, nil
}
