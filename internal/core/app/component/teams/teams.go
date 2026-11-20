// Package teams is the teams component (kind "teams", issue #248;
// ADR-0025 §4, §5): the event's participants split into teams, at random,
// balanced by level or by hand; two per team pairs people up (dive
// buddies).
//
// A participant is an active member of the tayfa who answered "going" or
// whose attendance was verified. Teams are kept by position (1…50): when
// they are formed again the n-th team keeps its ID (and, when generated,
// its name), so that components that refer to teams (the scoreboard, the
// bracket) see the same team. A person is in at most one team of a
// component; someone who stops being a participant drops out of their team
// when it is read.
//
// The component's parameters (definition, ADR-0025 §4) as they hold for
// the event:
//
//	mode        random (default), balanced or manual; the organizer may
//	            choose it where the definition offers a list
//	team_count  the number of teams
//	team_size   with team_count: each team's capacity, the rest sit out
//	            (chosen at random); without it: the size teams are formed
//	            to, the few left over join teams
//
// Generated teams use crypto/rand only. Balanced teams deal the players by
// the levels (1–5) the organizer sends with the request, strongest first in
// a snake order; the levels are never stored, logged or returned (what is
// not kept cannot leak).
//
// Who may do what (the framework's actions, package component):
//
//	read                  every active member who may see the event's components
//	generate, set         managers (the organizer, and moderators and admins
//	                      as for every event)
//
// Teams are attendance, not the plan: a series occurrence does not take
// them. A person's rows are deleted with their account and exported.
package teams

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompTeams

// The component's parameters.
const (
	ParamMode      = "mode"
	ParamTeamCount = "team_count"
	ParamTeamSize  = "team_size"
)

// Modes (definition.TeamModes).
const (
	ModeRandom   = "random"
	ModeBalanced = "balanced"
	ModeManual   = "manual"
)

// Limits; the same CHECKs are on event_teams (00043).
const (
	NameMax = 40
	// MaxTeams is the most teams a component holds (the parameter's upper
	// bound).
	MaxTeams = 50
	// MaxPeople bounds the people in a component's teams and the levels
	// of a request.
	MaxPeople = 500
	// LevelMin and LevelMax bound a level; DefaultLevel is a participant's
	// level when the request gives none.
	LevelMin     = 1
	LevelMax     = 5
	DefaultLevel = 3
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// Audit actions of the component's changes. The records hold the mode and
// the number of teams only, never a person, a name or a level.
const (
	AuditGenerated = "event.teams_generated"
	AuditReplaced  = "event.teams_replaced"
)

// Team is one team of a component.
type Team struct {
	ID id.ID
	// Position is 1-based.
	Position int32
	// Name is empty for an unnamed team.
	Name string
	// Members are the people in the team who are still participants.
	Members []id.ID
}

// Teams is one teams component of an event, as the caller sees it.
type Teams struct {
	// Teams are ordered by position.
	Teams []Team
	// Mode is the mode as it holds for the event.
	Mode string
	// TeamCount and TeamSize are the parameters; 0 when unset.
	TeamCount int32
	TeamSize  int32
	// CallerCanEdit reports whether the caller may form the teams now.
	CallerCanEdit bool
}

// Level is a participant's level for balanced teams.
type Level struct {
	User  id.ID
	Level int32
}

// Input is a team set by hand.
type Input struct {
	// Name is optional: empty (after trimming) for none.
	Name    string
	Members []id.ID
}

// Service is the teams component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// mode is the component's mode, or ModeRandom.
func mode(sc *component.Scope) string {
	if m := sc.ParamString(ParamMode); m == ModeBalanced || m == ModeManual {
		return m
	}
	return ModeRandom
}

// param is an integer parameter within 1..MaxTeams, or 0.
func param(sc *component.Scope, name string) int {
	if n, ok := sc.ParamInt(name); ok && n >= 1 && n <= MaxTeams {
		return int(n)
	}
	return 0
}

// canEdit is what CallerCanEdit reports: the checks Change makes.
func canEdit(sc *component.Scope) bool {
	return sc.Enabled && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow()) && sc.IsManager()
}

// Load reads the teams of the component key of event: for the components
// that refer to teams (scoreboard, bracket), inside their own View or
// Change. Members who are no longer participants are left out.
func Load(ctx context.Context, q *store.Queries, event id.ID, key string) ([]Team, error) {
	rows, err := q.ListTeams(ctx, store.ListTeamsParams{EventID: event.PG(), ComponentKey: key})
	if err != nil {
		return nil, fmt.Errorf("takımlar: %w", err)
	}
	out := make([]Team, 0, len(rows))
	at := make(map[id.ID]int, len(rows))
	for _, r := range rows {
		t := Team{ID: id.ID(r.ID.Bytes), Position: int32(r.Position), Name: r.Name.String, Members: []id.ID{}}
		at[t.ID] = len(out)
		out = append(out, t)
	}
	members, err := q.ListTeamMembers(ctx, store.ListTeamMembersParams{EventID: event.PG(), ComponentKey: key})
	if err != nil {
		return nil, fmt.Errorf("takım üyeleri: %w", err)
	}
	for _, m := range members {
		if i, ok := at[id.ID(m.TeamID.Bytes)]; ok {
			out[i].Members = append(out[i].Members, id.ID(m.UserID.Bytes))
		}
	}
	return out, nil
}

// load reads the teams as the caller sees them.
func load(ctx context.Context, sc *component.Scope) (Teams, error) {
	list, err := Load(ctx, sc.Q(), sc.Event, sc.Key)
	if err != nil {
		return Teams{}, err
	}
	return Teams{
		Teams: list, Mode: mode(sc),
		TeamCount: int32(param(sc, ParamTeamCount)), TeamSize: int32(param(sc, ParamTeamSize)), //nolint:gosec // 0..50
		CallerCanEdit: canEdit(sc),
	}, nil
}

// Get returns the teams. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Teams, error) {
	var out Teams
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return Teams{}, err
	}
	return out, nil
}

// candidates are the event's participants, by ID.
func candidates(ctx context.Context, sc *component.Scope) ([]id.ID, error) {
	rows, err := sc.Q().ListTeamCandidates(ctx, sc.Event.PG())
	if err != nil {
		return nil, fmt.Errorf("katılımcılar: %w", err)
	}
	out := make([]id.ID, 0, len(rows))
	for _, r := range rows {
		out = append(out, id.ID(r.Bytes))
	}
	return out, nil
}

// cleanName trims a team's name and refuses invalid UTF-8, control
// characters and a length above NameMax characters; empty is no name.
func cleanName(s string) (string, error) {
	if len(s) > (NameMax+64)*maxRuneBytes {
		return "", apperr.Validation("teams")
	}
	t := strings.TrimSpace(s)
	if !utf8.ValidString(t) || strings.IndexFunc(t, unicode.IsControl) >= 0 || utf8.RuneCountInString(t) > NameMax {
		return "", apperr.Validation("teams")
	}
	return t, nil
}

// cleanLevels checks the levels of a request and returns them by person.
func cleanLevels(levels []Level) (map[id.ID]int32, error) {
	if len(levels) > MaxPeople {
		return nil, apperr.Validation("levels")
	}
	out := make(map[id.ID]int32, len(levels))
	for _, l := range levels {
		if l.User == (id.ID{}) || l.Level < LevelMin || l.Level > LevelMax {
			return nil, apperr.Validation("levels")
		}
		if _, dup := out[l.User]; dup {
			return nil, apperr.Validation("levels")
		}
		out[l.User] = l.Level
	}
	return out, nil
}

// intn returns a uniform random integer in [0, n) from crypto/rand.
func intn(n int) (int, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, fmt.Errorf("rastgele sayı: %w", err)
	}
	return int(v.Int64()), nil //nolint:gosec // 0..n-1
}

// shuffle permutes s in place (Fisher–Yates with crypto/rand).
func shuffle[T any](s []T) error {
	for i := len(s) - 1; i > 0; i-- {
		j, err := intn(i + 1)
		if err != nil {
			return err
		}
		s[i], s[j] = s[j], s[i]
	}
	return nil
}

// Plan is how n participants are split: k teams, and play of them in teams
// (the rest sit out).
type Plan struct {
	Teams int
	Play  int
}

// PlanFor splits n participants by the parameters count and size (0 when
// unset): count teams (each holding at most size when both are set);
// without count, n/size teams of at least size people (at most MaxTeams);
// without either, two teams. Refused: fewer than two participants, or than
// teams (apperr.ErrTeamsTooFew).
func PlanFor(n, count, size int) (Plan, error) {
	if n < 2 {
		return Plan{}, apperr.ErrTeamsTooFew
	}
	switch {
	case count > 0:
		if n < count {
			return Plan{}, apperr.ErrTeamsTooFew
		}
		play := n
		if size > 0 {
			play = min(n, count*size)
		}
		return Plan{Teams: count, Play: play}, nil
	case size > 0:
		k := min(n/size, MaxTeams)
		if k < 1 {
			return Plan{}, apperr.ErrTeamsTooFew
		}
		return Plan{Teams: k, Play: n}, nil
	default:
		return Plan{Teams: 2, Play: n}, nil
	}
}

// Deal splits players into k teams: in order, round robin, or in a snake
// order (1…k, k…1, …) when snake is set. Team sizes differ by at most one.
func Deal(players []id.ID, k int, snake bool) [][]id.ID {
	out := make([][]id.ID, k)
	for i, p := range players {
		round, pos := i/k, i%k
		t := pos
		if snake && round%2 == 1 {
			t = k - 1 - pos
		}
		out[t] = append(out[t], p)
	}
	return out
}

// form splits the participants into teams for mode: random or balanced by
// levels.
func form(people []id.ID, plan Plan, balanced bool, levels map[id.ID]int32) ([][]id.ID, error) {
	players := slices.Clone(people)
	// Who plays (when some sit out) and the order of equals: at random.
	if err := shuffle(players); err != nil {
		return nil, err
	}
	players = players[:plan.Play]
	if balanced {
		level := func(u id.ID) int32 {
			if l, ok := levels[u]; ok {
				return l
			}
			return DefaultLevel
		}
		slices.SortStableFunc(players, func(a, b id.ID) int { return cmp.Compare(level(b), level(a)) })
	}
	teams := Deal(players, plan.Teams, balanced)
	// Which team gets the first pick: at random.
	if err := shuffle(teams); err != nil {
		return nil, err
	}
	return teams, nil
}

func optText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// write replaces the component's teams: the n-th team keeps the row (and
// ID) of the current n-th team.
func write(ctx context.Context, sc *component.Scope, names []string, members [][]id.ID) error {
	q := sc.Q()
	if err := q.DeleteTeamMembers(ctx, store.DeleteTeamMembersParams{EventID: sc.Event.PG(), ComponentKey: sc.Key}); err != nil {
		return fmt.Errorf("takım üyelerini silme: %w", err)
	}
	if err := q.DeleteTeamsFrom(ctx, store.DeleteTeamsFromParams{
		EventID: sc.Event.PG(), ComponentKey: sc.Key, FromPosition: int16(len(members) + 1), //nolint:gosec // 0..50
	}); err != nil {
		return fmt.Errorf("takımları silme: %w", err)
	}
	now := pgtype.Timestamptz{Time: sc.Now, Valid: true}
	for i, people := range members {
		team, err := q.UpsertTeam(ctx, store.UpsertTeamParams{
			ID: id.New().PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key,
			Position: int16(i + 1), Name: optText(names[i]), Now: now, //nolint:gosec // 1..50
		})
		if err != nil {
			return fmt.Errorf("takım: %w", err)
		}
		for _, u := range people {
			if err := q.AddTeamMember(ctx, store.AddTeamMemberParams{
				EventID: sc.Event.PG(), ComponentKey: sc.Key, TeamID: team, UserID: u.PG(), CreatedAt: now,
			}); err != nil {
				return fmt.Errorf("takım üyesi: %w", err)
			}
		}
	}
	return nil
}

// done marks the change, writes its audit record (the mode and the number
// of teams) and reads the result for the caller.
func done(ctx context.Context, sc *component.Scope, action string, teams int) (Teams, error) {
	sc.Changed()
	if err := sc.Audit(ctx, action, map[string]any{"mode": mode(sc), "teams": teams}); err != nil {
		return Teams{}, err
	}
	return load(ctx, sc)
}

// Generate forms the teams from the event's participants and replaces the
// current ones (keeping their names by position); managers only. levels
// are used in balanced mode only and never kept. Refused: manual mode
// (apperr.ErrTeamsManual), too few participants (apperr.ErrTeamsTooFew),
// more than MaxPeople (apperr.ErrEventLimit), bad levels (validation error
// on levels) and the refusals of component.Service.Change.
func (s *Service) Generate(ctx context.Context, ref component.Ref, levels []Level) (Teams, error) {
	byUser, err := cleanLevels(levels)
	if err != nil {
		return Teams{}, err
	}
	var out Teams
	err = s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		m := mode(sc)
		if m == ModeManual {
			return apperr.ErrTeamsManual
		}
		people, err := candidates(ctx, sc)
		if err != nil {
			return err
		}
		if len(people) > MaxPeople {
			return apperr.ErrEventLimit
		}
		plan, err := PlanFor(len(people), param(sc, ParamTeamCount), param(sc, ParamTeamSize))
		if err != nil {
			return err
		}
		members, err := form(people, plan, m == ModeBalanced, byUser)
		if err != nil {
			return err
		}
		current, err := Load(ctx, sc.Q(), sc.Event, sc.Key)
		if err != nil {
			return err
		}
		names := make([]string, plan.Teams)
		for _, t := range current {
			if i := int(t.Position) - 1; i >= 0 && i < len(names) {
				names[i] = t.Name
			}
		}
		if err := write(ctx, sc, names, members); err != nil {
			return err
		}
		out, err = done(ctx, sc, AuditGenerated, plan.Teams)
		return err
	})
	if err != nil {
		return Teams{}, err
	}
	return out, nil
}

// Replace sets the teams by hand, in order, and replaces the current ones;
// managers only, in every mode. An empty list clears them. Refused: more
// teams than team_count (or MaxTeams), more people in a team than
// team_size when team_count is set too, a bad name, a person twice or more
// than MaxPeople (validation error on teams), someone who is not a
// participant (apperr.ErrTeamsNotParticipant) and the refusals of
// component.Service.Change.
func (s *Service) Replace(ctx context.Context, ref component.Ref, in []Input) (Teams, error) {
	if len(in) > MaxTeams {
		return Teams{}, apperr.Validation("teams")
	}
	names := make([]string, len(in))
	members := make([][]id.ID, len(in))
	seen := map[id.ID]bool{}
	for i, t := range in {
		name, err := cleanName(t.Name)
		if err != nil {
			return Teams{}, err
		}
		names[i] = name
		for _, u := range t.Members {
			if u == (id.ID{}) || seen[u] {
				return Teams{}, apperr.Validation("teams")
			}
			seen[u] = true
		}
		if len(seen) > MaxPeople {
			return Teams{}, apperr.Validation("teams")
		}
		members[i] = slices.Clone(t.Members)
	}
	var out Teams
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		count, size := param(sc, ParamTeamCount), param(sc, ParamTeamSize)
		if count > 0 && len(in) > count {
			return apperr.Validation("teams")
		}
		if count > 0 && size > 0 {
			for _, m := range members {
				if len(m) > size {
					return apperr.Validation("teams")
				}
			}
		}
		people, err := candidates(ctx, sc)
		if err != nil {
			return err
		}
		ok := make(map[id.ID]bool, len(people))
		for _, u := range people {
			ok[u] = true
		}
		for u := range seen {
			if !ok[u] {
				return apperr.ErrTeamsNotParticipant
			}
		}
		if err := write(ctx, sc, names, members); err != nil {
			return err
		}
		out, err = done(ctx, sc, AuditReplaced, len(in))
		return err
	})
	if err != nil {
		return Teams{}, err
	}
	return out, nil
}
