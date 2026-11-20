package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/teams"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Teams is what TeamsComponentServer needs from the app layer
// (teams.Service).
type Teams interface {
	Get(ctx context.Context, ref component.Ref) (teams.Teams, error)
	Generate(ctx context.Context, ref component.Ref, levels []teams.Level) (teams.Teams, error)
	Replace(ctx context.Context, ref component.Ref, in []teams.Input) (teams.Teams, error)
}

// TeamsComponentServer implements corev1.TeamsComponentServiceServer
// (issue #248; ADR-0025 §4). Every method needs a verified token
// (auth.Guard); the acting user is always the token's. Levels are never
// logged.
type TeamsComponentServer struct {
	corev1.UnimplementedTeamsComponentServiceServer
	app Teams
	log *slog.Logger
}

// NewTeamsComponentServer returns a TeamsComponentServer.
func NewTeamsComponentServer(app Teams, log *slog.Logger) *TeamsComponentServer {
	return &TeamsComponentServer{app: app, log: log}
}

// registerTeamsComponent is the teams component's line in the registry
// (componentRegistrars).
func registerTeamsComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterTeamsComponentServiceServer(srv, NewTeamsComponentServer(teams.New(fw), log))
	return nil
}

func teamsPB(t teams.Teams) *corev1.Teams {
	out := &corev1.Teams{
		Teams: make([]*corev1.Team, 0, len(t.Teams)),
		Mode:  t.Mode, TeamCount: t.TeamCount, TeamSize: t.TeamSize, CallerCanEdit: t.CallerCanEdit,
	}
	for _, tm := range t.Teams {
		pb := &corev1.Team{Id: tm.ID.String(), Position: tm.Position, Name: tm.Name, MemberIds: make([]string, 0, len(tm.Members))}
		for _, u := range tm.Members {
			pb.MemberIds = append(pb.MemberIds, u.String())
		}
		out.Teams = append(out.Teams, pb)
	}
	return out
}

// GetTeams implements corev1.TeamsComponentServiceServer.
func (s *TeamsComponentServer) GetTeams(ctx context.Context, req *corev1.GetTeamsRequest) (*corev1.GetTeamsResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	out, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetTeamsResponse{Teams: teamsPB(out)}, nil
}

// GenerateTeams implements corev1.TeamsComponentServiceServer.
func (s *TeamsComponentServer) GenerateTeams(ctx context.Context, req *corev1.GenerateTeamsRequest) (*corev1.GenerateTeamsResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if len(req.GetLevels()) > teams.MaxPeople {
		return nil, validation("levels")
	}
	levels := make([]teams.Level, 0, len(req.GetLevels()))
	for _, l := range req.GetLevels() {
		u, ok := id.Parse(l.GetUserId())
		if !ok || l.GetLevel() < teams.LevelMin || l.GetLevel() > teams.LevelMax {
			return nil, validation("levels")
		}
		levels = append(levels, teams.Level{User: u, Level: l.GetLevel()})
	}
	out, err := s.app.Generate(ctx, ref, levels)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GenerateTeamsResponse{Teams: teamsPB(out)}, nil
}

// ReplaceTeams implements corev1.TeamsComponentServiceServer.
func (s *TeamsComponentServer) ReplaceTeams(ctx context.Context, req *corev1.ReplaceTeamsRequest) (*corev1.ReplaceTeamsResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if len(req.GetTeams()) > teams.MaxTeams {
		return nil, validation("teams")
	}
	in := make([]teams.Input, 0, len(req.GetTeams()))
	people := 0
	for _, t := range req.GetTeams() {
		people += len(t.GetMemberIds())
		if people > teams.MaxPeople {
			return nil, validation("teams")
		}
		members := make([]id.ID, 0, len(t.GetMemberIds()))
		for _, m := range t.GetMemberIds() {
			u, ok := id.Parse(m)
			if !ok {
				return nil, validation("teams")
			}
			members = append(members, u)
		}
		in = append(in, teams.Input{Name: t.GetName(), Members: members})
	}
	out, err := s.app.Replace(ctx, ref, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ReplaceTeamsResponse{Teams: teamsPB(out)}, nil
}
