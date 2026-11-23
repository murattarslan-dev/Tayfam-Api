package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/bracket"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Brackets is what BracketComponentServer needs from the app layer
// (bracket.Service).
type Brackets interface {
	Get(ctx context.Context, ref component.Ref) (bracket.Bracket, error)
	Start(ctx context.Context, ref component.Ref) (bracket.Bracket, error)
	Reset(ctx context.Context, ref component.Ref) (bracket.Bracket, error)
	PairRound(ctx context.Context, ref component.Ref) (bracket.Bracket, error)
	AddGame(ctx context.Context, ref component.Ref, match id.ID, in bracket.GameInput) (bracket.Bracket, error)
	CorrectGame(ctx context.Context, ref component.Ref, match id.ID, game int32, in bracket.GameInput) (bracket.Bracket, error)
	DeleteGame(ctx context.Context, ref component.Ref, match id.ID, game int32) (bracket.Bracket, error)
}

// BracketComponentServer implements corev1.BracketComponentServiceServer
// (issue #251; ADR-0025 §4–§6). Every method needs a verified token
// (auth.Guard); the acting user is always the token's. Scores are never
// logged.
type BracketComponentServer struct {
	corev1.UnimplementedBracketComponentServiceServer
	app Brackets
	log *slog.Logger
}

// NewBracketComponentServer returns a BracketComponentServer.
func NewBracketComponentServer(app Brackets, log *slog.Logger) *BracketComponentServer {
	return &BracketComponentServer{app: app, log: log}
}

// registerBracketComponent is the bracket component's line in the registry
// (componentRegistrars).
func registerBracketComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterBracketComponentServiceServer(srv, NewBracketComponentServer(bracket.New(fw), log))
	return nil
}

// bracketPB converts a bracket; an empty place has no user or team ID
// (idString).
func bracketPB(b bracket.Bracket) *corev1.Bracket {
	out := &corev1.Bracket{
		Started: b.Started, Format: b.Format, BestOf: b.BestOf, Result: b.Result.Result, Winner: b.Winner,
		DrawKey: b.DrawKey, Drawn: b.Drawn, Subject: b.Subject, TeamsKey: b.TeamsKey,
		Rounds: b.Rounds, CurrentRound: b.CurrentRound,
		Entrants:  make([]*corev1.BracketEntrant, 0, len(b.Entrants)),
		Matches:   make([]*corev1.BracketMatch, 0, len(b.Matches)),
		Standings: make([]*corev1.BracketStanding, 0, len(b.Standings)),
		Finished:  b.Finished, WinnerPositions: append([]int32{}, b.Winners...), CallerCanManage: b.CallerCanManage,
	}
	if b.Started {
		out.StartTime = timestamppb.New(b.StartedAt)
	}
	for _, e := range b.Entrants {
		out.Entrants = append(out.Entrants, &corev1.BracketEntrant{
			Position: e.Position, UserId: idString(e.User), TeamId: idString(e.Team), GuestId: idString(e.Guest),
		})
	}
	for _, m := range b.Matches {
		pm := &corev1.BracketMatch{
			Id: m.ID.String(), Round: m.Round, Slot: m.Slot, APosition: m.A, BPosition: m.B,
			Games: make([]*corev1.BracketGame, 0, len(m.Games)), Decided: m.Decided, Bye: m.Bye, WinnerPosition: m.Winner,
			ATotal: m.ATotal, BTotal: m.BTotal, AGamesWon: m.AWon, BGamesWon: m.BWon, CallerCanScore: m.CallerCanScore,
		}
		for _, g := range m.Games {
			pm.Games = append(pm.Games, &corev1.BracketGame{Number: g.Number, APoints: g.APoints, BPoints: g.BPoints, AOutcome: g.AOutcome})
		}
		out.Matches = append(out.Matches, pm)
	}
	for _, s := range b.Standings {
		out.Standings = append(out.Standings, &corev1.BracketStanding{
			Position: s.Position, Played: s.Played, Won: s.Won, Drawn: s.Drawn, Lost: s.Lost,
			Points: s.Points, HeadToHead: s.HeadToHead, Buchholz: s.Buchholz, Rank: s.Rank,
		})
	}
	return out
}

// bracketMatchRef reads a request's component and match.
func bracketMatchRef(ctx context.Context, ref *corev1.ComponentRef, match string) (component.Ref, id.ID, error) {
	r, err := componentRef(ctx, ref)
	if err != nil {
		return component.Ref{}, id.ID{}, err
	}
	m, ok := id.Parse(match)
	if !ok {
		return component.Ref{}, id.ID{}, validation("match_id")
	}
	return r, m, nil
}

// bracketGame reads a game number: 1–MaxGames.
func bracketGame(n int32) error {
	if n < 1 || n > bracket.MaxGames {
		return validation("game")
	}
	return nil
}

// GetBracket implements corev1.BracketComponentServiceServer.
func (s *BracketComponentServer) GetBracket(ctx context.Context, req *corev1.GetBracketRequest) (*corev1.GetBracketResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetBracketResponse{Bracket: bracketPB(out)}, nil
}

// StartBracket implements corev1.BracketComponentServiceServer.
func (s *BracketComponentServer) StartBracket(ctx context.Context, req *corev1.StartBracketRequest) (*corev1.StartBracketResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Start(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.StartBracketResponse{Bracket: bracketPB(out)}, nil
}

// ResetBracket implements corev1.BracketComponentServiceServer.
func (s *BracketComponentServer) ResetBracket(ctx context.Context, req *corev1.ResetBracketRequest) (*corev1.ResetBracketResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Reset(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ResetBracketResponse{Bracket: bracketPB(out)}, nil
}

// PairBracketRound implements corev1.BracketComponentServiceServer.
func (s *BracketComponentServer) PairBracketRound(ctx context.Context, req *corev1.PairBracketRoundRequest) (*corev1.PairBracketRoundResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.PairRound(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.PairBracketRoundResponse{Bracket: bracketPB(out)}, nil
}

// AddBracketGame implements corev1.BracketComponentServiceServer.
func (s *BracketComponentServer) AddBracketGame(ctx context.Context, req *corev1.AddBracketGameRequest) (*corev1.AddBracketGameResponse, error) {
	ref, match, err := bracketMatchRef(ctx, req.GetRef(), req.GetMatchId())
	if err != nil {
		return nil, err
	}
	in := bracket.GameInput{APoints: req.GetAPoints(), BPoints: req.GetBPoints(), AOutcome: req.GetAOutcome()}
	out, err := s.app.AddGame(ctx, ref, match, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddBracketGameResponse{Bracket: bracketPB(out)}, nil
}

// CorrectBracketGame implements corev1.BracketComponentServiceServer.
func (s *BracketComponentServer) CorrectBracketGame(ctx context.Context, req *corev1.CorrectBracketGameRequest) (*corev1.CorrectBracketGameResponse, error) {
	ref, match, err := bracketMatchRef(ctx, req.GetRef(), req.GetMatchId())
	if err != nil {
		return nil, err
	}
	if err := bracketGame(req.GetGame()); err != nil {
		return nil, err
	}
	in := bracket.GameInput{APoints: req.GetAPoints(), BPoints: req.GetBPoints(), AOutcome: req.GetAOutcome()}
	out, err := s.app.CorrectGame(ctx, ref, match, req.GetGame(), in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CorrectBracketGameResponse{Bracket: bracketPB(out)}, nil
}

// DeleteBracketGame implements corev1.BracketComponentServiceServer.
func (s *BracketComponentServer) DeleteBracketGame(ctx context.Context, req *corev1.DeleteBracketGameRequest) (*corev1.DeleteBracketGameResponse, error) {
	ref, match, err := bracketMatchRef(ctx, req.GetRef(), req.GetMatchId())
	if err != nil {
		return nil, err
	}
	if err := bracketGame(req.GetGame()); err != nil {
		return nil, err
	}
	out, err := s.app.DeleteGame(ctx, ref, match, req.GetGame())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteBracketGameResponse{Bracket: bracketPB(out)}, nil
}
