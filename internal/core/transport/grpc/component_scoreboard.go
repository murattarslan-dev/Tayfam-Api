package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Scoreboards is what ScoreboardComponentServer needs from the app layer
// (scoreboard.Service).
type Scoreboards interface {
	Get(ctx context.Context, ref component.Ref) (scoreboard.Scoreboard, error)
	CreateTable(ctx context.Context, ref component.Ref, in scoreboard.TableInput) (scoreboard.Scoreboard, error)
	UpdateTable(ctx context.Context, ref component.Ref, table id.ID, in scoreboard.TableUpdate) (scoreboard.Scoreboard, error)
	DeleteTable(ctx context.Context, ref component.Ref, table id.ID) (scoreboard.Scoreboard, error)
	SetPlayers(ctx context.Context, ref component.Ref, table id.ID, in []scoreboard.PlayerInput) (scoreboard.Scoreboard, error)
	AddRound(ctx context.Context, ref component.Ref, table id.ID, in []scoreboard.ScoreInput) (scoreboard.Scoreboard, error)
	CorrectRound(ctx context.Context, ref component.Ref, table id.ID, round int32, in []scoreboard.ScoreInput) (scoreboard.Scoreboard, error)
	DeleteRound(ctx context.Context, ref component.Ref, table id.ID, round int32) (scoreboard.Scoreboard, error)
	FinishTable(ctx context.Context, ref component.Ref, table id.ID) (scoreboard.Scoreboard, error)
}

// ScoreboardComponentServer implements
// corev1.ScoreboardComponentServiceServer (issue #250; ADR-0025 §4–§6).
// Every method needs a verified token (auth.Guard); the acting user is
// always the token's. Scores are never logged.
type ScoreboardComponentServer struct {
	corev1.UnimplementedScoreboardComponentServiceServer
	app Scoreboards
	log *slog.Logger
}

// NewScoreboardComponentServer returns a ScoreboardComponentServer.
func NewScoreboardComponentServer(app Scoreboards, log *slog.Logger) *ScoreboardComponentServer {
	return &ScoreboardComponentServer{app: app, log: log}
}

// registerScoreboardComponent is the scoreboard component's line in the
// registry (componentRegistrars).
func registerScoreboardComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterScoreboardComponentServiceServer(srv, NewScoreboardComponentServer(scoreboard.New(fw), log))
	return nil
}

func scoreRulesPB(r scoreboard.Rules) *corev1.ScoreRules {
	return &corev1.ScoreRules{Side: r.Side, Rounds: r.Rounds, Winner: r.Winner, Result: r.Result, Target: r.Target, BestOf: r.BestOf}
}

func scoreTablePB(t scoreboard.Table) *corev1.ScoreTable {
	out := &corev1.ScoreTable{
		Id: t.ID.String(), Label: t.Label, PresetKey: t.Preset, Rules: scoreRulesPB(t.Rules), TeamsKey: t.TeamsKey,
		Status: t.Status, CreateTime: timestamppb.New(t.CreatedAt),
		Players:   make([]*corev1.ScorePlayer, 0, len(t.Players)),
		Rounds:    make([]*corev1.ScoreRound, 0, len(t.Rounds)),
		Standings: make([]*corev1.ScoreStanding, 0, len(t.Result.Standings)),
		WinnerIds: make([]string, 0, len(t.Result.Winners)), CallerCanScore: t.CallerCanScore,
	}
	if !t.FinishedAt.IsZero() {
		out.FinishedTime = timestamppb.New(t.FinishedAt)
	}
	for _, p := range t.Players {
		out.Players = append(out.Players, &corev1.ScorePlayer{Id: p.ID.String(), Position: p.Position, UserId: idString(p.User), TeamId: idString(p.Team)})
	}
	for _, r := range t.Rounds {
		rd := &corev1.ScoreRound{Number: r.Number, Scores: make([]*corev1.ScoreEntry, 0, len(r.Scores))}
		for _, s := range r.Scores {
			rd.Scores = append(rd.Scores, &corev1.ScoreEntry{PlayerId: s.Player.String(), Points: s.Points, Outcome: s.Outcome})
		}
		out.Rounds = append(out.Rounds, rd)
	}
	for _, s := range t.Result.Standings {
		out.Standings = append(out.Standings, &corev1.ScoreStanding{PlayerId: s.Player.String(), Total: s.Total, RoundsWon: s.RoundsWon, Rank: s.Rank})
	}
	for _, w := range t.Result.Winners {
		out.WinnerIds = append(out.WinnerIds, w.String())
	}
	return out
}

func scoreboardPB(b scoreboard.Scoreboard) *corev1.Scoreboard {
	out := &corev1.Scoreboard{
		Tables: make([]*corev1.ScoreTable, 0, len(b.Tables)), Presets: make([]*corev1.ScorePreset, 0, len(b.Presets)),
		Rules: scoreRulesPB(b.Rules), MaxTables: b.MaxTables, CallerCanManage: b.CallerCanManage,
	}
	for _, t := range b.Tables {
		out.Tables = append(out.Tables, scoreTablePB(t))
	}
	for _, p := range b.Presets {
		out.Presets = append(out.Presets, &corev1.ScorePreset{
			Key: p.Key, Labels: p.Labels, PlayersMin: p.PlayersMin, PlayersMax: p.PlayersMax,
			Rules: scoreRulesPB(p.Rules), Retired: p.Retired,
		})
	}
	return out
}

// scoreTableRef reads a request's component and table.
func scoreTableRef(ctx context.Context, ref *corev1.ComponentRef, table string) (component.Ref, id.ID, error) {
	r, err := componentRef(ctx, ref)
	if err != nil {
		return component.Ref{}, id.ID{}, err
	}
	t, ok := id.Parse(table)
	if !ok {
		return component.Ref{}, id.ID{}, validation("table_id")
	}
	return r, t, nil
}

// scorePlayers reads seats: exactly one of user and team each.
func scorePlayers(in []*corev1.ScorePlayerInput) ([]scoreboard.PlayerInput, error) {
	if len(in) > scoreboard.MaxPlayers {
		return nil, validation("players")
	}
	out := make([]scoreboard.PlayerInput, 0, len(in))
	for _, p := range in {
		var v scoreboard.PlayerInput
		var ok bool
		switch {
		case p.GetUserId() != "" && p.GetTeamId() == "":
			v.User, ok = id.Parse(p.GetUserId())
		case p.GetTeamId() != "" && p.GetUserId() == "":
			v.Team, ok = id.Parse(p.GetTeamId())
		}
		if !ok {
			return nil, validation("players")
		}
		out = append(out, v)
	}
	return out, nil
}

// scoreInputs reads a round's scores.
func scoreInputs(in []*corev1.ScoreEntry) ([]scoreboard.ScoreInput, error) {
	if len(in) == 0 || len(in) > scoreboard.MaxPlayers {
		return nil, validation("scores")
	}
	out := make([]scoreboard.ScoreInput, 0, len(in))
	for _, s := range in {
		p, ok := id.Parse(s.GetPlayerId())
		if !ok {
			return nil, validation("scores")
		}
		out = append(out, scoreboard.ScoreInput{Player: p, Points: s.GetPoints(), Outcome: s.GetOutcome()})
	}
	return out, nil
}

// GetScoreboard implements corev1.ScoreboardComponentServiceServer.
func (s *ScoreboardComponentServer) GetScoreboard(ctx context.Context, req *corev1.GetScoreboardRequest) (*corev1.GetScoreboardResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetScoreboardResponse{Scoreboard: scoreboardPB(out)}, nil
}

// CreateScoreTable implements corev1.ScoreboardComponentServiceServer.
func (s *ScoreboardComponentServer) CreateScoreTable(ctx context.Context, req *corev1.CreateScoreTableRequest) (*corev1.CreateScoreTableResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	players, err := scorePlayers(req.GetPlayers())
	if err != nil {
		return nil, err
	}
	out, err := s.app.CreateTable(ctx, ref, scoreboard.TableInput{
		Label: req.GetLabel(), Preset: req.GetPresetKey(), Target: req.GetTarget(), TeamsKey: req.GetTeamsKey(), Players: players,
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateScoreTableResponse{Scoreboard: scoreboardPB(out)}, nil
}

// UpdateScoreTable implements corev1.ScoreboardComponentServiceServer.
func (s *ScoreboardComponentServer) UpdateScoreTable(ctx context.Context, req *corev1.UpdateScoreTableRequest) (*corev1.UpdateScoreTableResponse, error) {
	ref, table, err := scoreTableRef(ctx, req.GetRef(), req.GetTableId())
	if err != nil {
		return nil, err
	}
	in := scoreboard.TableUpdate{Status: req.GetStatus()}
	if req.Label != nil {
		label := req.GetLabel()
		in.Label = &label
	}
	out, err := s.app.UpdateTable(ctx, ref, table, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateScoreTableResponse{Scoreboard: scoreboardPB(out)}, nil
}

// DeleteScoreTable implements corev1.ScoreboardComponentServiceServer.
func (s *ScoreboardComponentServer) DeleteScoreTable(ctx context.Context, req *corev1.DeleteScoreTableRequest) (*corev1.DeleteScoreTableResponse, error) {
	ref, table, err := scoreTableRef(ctx, req.GetRef(), req.GetTableId())
	if err != nil {
		return nil, err
	}
	out, err := s.app.DeleteTable(ctx, ref, table)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteScoreTableResponse{Scoreboard: scoreboardPB(out)}, nil
}

// SetScorePlayers implements corev1.ScoreboardComponentServiceServer.
func (s *ScoreboardComponentServer) SetScorePlayers(ctx context.Context, req *corev1.SetScorePlayersRequest) (*corev1.SetScorePlayersResponse, error) {
	ref, table, err := scoreTableRef(ctx, req.GetRef(), req.GetTableId())
	if err != nil {
		return nil, err
	}
	players, err := scorePlayers(req.GetPlayers())
	if err != nil {
		return nil, err
	}
	out, err := s.app.SetPlayers(ctx, ref, table, players)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetScorePlayersResponse{Scoreboard: scoreboardPB(out)}, nil
}

// AddScoreRound implements corev1.ScoreboardComponentServiceServer.
func (s *ScoreboardComponentServer) AddScoreRound(ctx context.Context, req *corev1.AddScoreRoundRequest) (*corev1.AddScoreRoundResponse, error) {
	ref, table, err := scoreTableRef(ctx, req.GetRef(), req.GetTableId())
	if err != nil {
		return nil, err
	}
	scores, err := scoreInputs(req.GetScores())
	if err != nil {
		return nil, err
	}
	out, err := s.app.AddRound(ctx, ref, table, scores)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddScoreRoundResponse{Scoreboard: scoreboardPB(out)}, nil
}

// CorrectScoreRound implements corev1.ScoreboardComponentServiceServer.
func (s *ScoreboardComponentServer) CorrectScoreRound(ctx context.Context, req *corev1.CorrectScoreRoundRequest) (*corev1.CorrectScoreRoundResponse, error) {
	ref, table, err := scoreTableRef(ctx, req.GetRef(), req.GetTableId())
	if err != nil {
		return nil, err
	}
	if req.GetRound() < 1 || req.GetRound() > scoreboard.MaxRounds {
		return nil, validation("round")
	}
	scores, err := scoreInputs(req.GetScores())
	if err != nil {
		return nil, err
	}
	out, err := s.app.CorrectRound(ctx, ref, table, req.GetRound(), scores)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CorrectScoreRoundResponse{Scoreboard: scoreboardPB(out)}, nil
}

// DeleteScoreRound implements corev1.ScoreboardComponentServiceServer.
func (s *ScoreboardComponentServer) DeleteScoreRound(ctx context.Context, req *corev1.DeleteScoreRoundRequest) (*corev1.DeleteScoreRoundResponse, error) {
	ref, table, err := scoreTableRef(ctx, req.GetRef(), req.GetTableId())
	if err != nil {
		return nil, err
	}
	if req.GetRound() < 1 || req.GetRound() > scoreboard.MaxRounds {
		return nil, validation("round")
	}
	out, err := s.app.DeleteRound(ctx, ref, table, req.GetRound())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteScoreRoundResponse{Scoreboard: scoreboardPB(out)}, nil
}

// FinishScoreTable implements corev1.ScoreboardComponentServiceServer.
func (s *ScoreboardComponentServer) FinishScoreTable(ctx context.Context, req *corev1.FinishScoreTableRequest) (*corev1.FinishScoreTableResponse, error) {
	ref, table, err := scoreTableRef(ctx, req.GetRef(), req.GetTableId())
	if err != nil {
		return nil, err
	}
	out, err := s.app.FinishTable(ctx, ref, table)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.FinishScoreTableResponse{Scoreboard: scoreboardPB(out)}, nil
}
