package grpc

import (
	"context"
	"slices"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/teams"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeTeams struct {
	err    error
	method string
	ref    component.Ref
	levels []teams.Level
	in     []teams.Input
	out    teams.Teams
	calls  int
}

func (f *fakeTeams) Get(_ context.Context, ref component.Ref) (teams.Teams, error) {
	f.method, f.ref, f.calls = "Get", ref, f.calls+1
	return f.out, f.err
}

func (f *fakeTeams) Generate(_ context.Context, ref component.Ref, levels []teams.Level) (teams.Teams, error) {
	f.method, f.ref, f.levels, f.calls = "Generate", ref, levels, f.calls+1
	return f.out, f.err
}

func (f *fakeTeams) Replace(_ context.Context, ref component.Ref, in []teams.Input) (teams.Teams, error) {
	f.method, f.ref, f.in, f.calls = "Replace", ref, in, f.calls+1
	return f.out, f.err
}

func TestTeamsComponentMethods(t *testing.T) {
	red, blue := id.New(), id.New()
	a, b, c := id.New(), id.New(), id.New()
	f := &fakeTeams{out: teams.Teams{
		Teams: []teams.Team{
			{ID: red, Position: 1, Name: "Kırmızılar", Members: []id.ID{a, b}},
			{ID: blue, Position: 2, Members: []id.ID{}},
		},
		Mode: teams.ModeBalanced, TeamCount: 2, CallerCanEdit: true,
	}}
	s := NewTeamsComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "teams"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "teams"}
	check := func(method string, got *corev1.Teams, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if f.method != method || f.ref != want {
			t.Fatalf("%s: app'e giden %s %+v", method, f.method, f.ref)
		}
		tm := got.GetTeams()
		if len(tm) != 2 || tm[0].GetId() != red.String() || tm[0].GetPosition() != 1 || tm[0].GetName() != "Kırmızılar" ||
			!slices.Equal(tm[0].GetMemberIds(), []string{a.String(), b.String()}) || tm[1].GetName() != "" || len(tm[1].GetMemberIds()) != 0 ||
			got.GetMode() != "balanced" || got.GetTeamCount() != 2 || got.GetTeamSize() != 0 || !got.GetCallerCanEdit() {
			t.Fatalf("%s: yanıt %v", method, got)
		}
	}

	got, err := s.GetTeams(ctx, &corev1.GetTeamsRequest{Ref: ref})
	check("Get", got.GetTeams(), err)
	gen, err := s.GenerateTeams(ctx, &corev1.GenerateTeamsRequest{Ref: ref, Levels: []*corev1.TeamLevel{{UserId: a.String(), Level: 5}, {UserId: c.String(), Level: 1}}})
	check("Generate", gen.GetTeams(), err)
	if !slices.Equal(f.levels, []teams.Level{{User: a, Level: 5}, {User: c, Level: 1}}) {
		t.Fatalf("seviyeler: %+v", f.levels)
	}
	if _, err := s.GenerateTeams(ctx, &corev1.GenerateTeamsRequest{Ref: ref}); err != nil || len(f.levels) != 0 {
		t.Fatalf("seviyesiz: %v %+v", err, f.levels)
	}
	rep, err := s.ReplaceTeams(ctx, &corev1.ReplaceTeamsRequest{Ref: ref, Teams: []*corev1.TeamInput{
		{Name: " Kırmızılar ", MemberIds: []string{a.String(), b.String()}}, {},
	}})
	check("Replace", rep.GetTeams(), err)
	if len(f.in) != 2 || f.in[0].Name != " Kırmızılar " || !slices.Equal(f.in[0].Members, []id.ID{a, b}) || len(f.in[1].Members) != 0 {
		t.Fatalf("elle takımlar: %+v", f.in)
	}

	// The app's errors keep their reasons.
	for appErr, want := range map[error]struct {
		code   codes.Code
		reason corev1.ErrorReason
	}{
		apperr.ErrTeamsManual:         {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_TEAMS_MANUAL},
		apperr.ErrTeamsTooFew:         {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_TEAMS_TOO_FEW},
		apperr.ErrTeamsNotParticipant: {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_TEAMS_NOT_PARTICIPANT},
		apperr.ErrEventLimit:          {codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_EVENT_LIMIT},
		apperr.ErrComponentFrozen:     {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN},
		apperr.Validation("teams"):    {codes.InvalidArgument, corev1.ErrorReason_ERROR_REASON_VALIDATION},
	} {
		f.err = appErr
		_, err := s.GenerateTeams(ctx, &corev1.GenerateTeamsRequest{Ref: ref})
		if code, info := infoOf(t, err); code != want.code || info.GetReason() != want.reason.String() {
			t.Errorf("%v: %v", appErr, err)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestTeamsComponentValidation(t *testing.T) {
	f := &fakeTeams{}
	s := NewTeamsComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "teams"}
	field := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}

	for name, bad := range map[string]*corev1.ComponentRef{
		"başvurusuz":     nil,
		"bozuk tayfa":    {TayfaId: "x", EventId: ev, ComponentKey: "teams"},
		"bozuk etkinlik": {TayfaId: tf, EventId: "x", ComponentKey: "teams"},
		"bozuk anahtar":  {TayfaId: tf, EventId: ev, ComponentKey: "Teams"},
	} {
		_, e1 := s.GetTeams(ctx, &corev1.GetTeamsRequest{Ref: bad})
		_, e2 := s.GenerateTeams(ctx, &corev1.GenerateTeamsRequest{Ref: bad})
		_, e3 := s.ReplaceTeams(ctx, &corev1.ReplaceTeamsRequest{Ref: bad})
		for i, err := range []error{e1, e2, e3} {
			if codeOf(t, err) != codes.InvalidArgument {
				t.Errorf("%s / %d: %v", name, i, err)
			}
		}
	}
	good := id.New().String()
	for name, levels := range map[string][]*corev1.TeamLevel{
		"bozuk kimlik": {{UserId: "x", Level: 3}},
		"düşük":        {{UserId: good, Level: 0}},
		"yüksek":       {{UserId: good, Level: 6}},
		"çok":          make([]*corev1.TeamLevel, teams.MaxPeople+1),
	} {
		_, err := s.GenerateTeams(ctx, &corev1.GenerateTeamsRequest{Ref: ref, Levels: levels})
		if got := field(err); got != "levels" {
			t.Errorf("%s: %q", name, got)
		}
	}
	many := make([]string, teams.MaxPeople+1)
	for i := range many {
		many[i] = id.New().String()
	}
	for name, in := range map[string][]*corev1.TeamInput{
		"bozuk kimlik": {{MemberIds: []string{"x"}}},
		"çok takım":    make([]*corev1.TeamInput, teams.MaxTeams+1),
		"çok kişi":     {{MemberIds: many[:300]}, {MemberIds: many[300:]}},
	} {
		_, err := s.ReplaceTeams(ctx, &corev1.ReplaceTeamsRequest{Ref: ref, Teams: in})
		if got := field(err); got != "teams" {
			t.Errorf("%s: %q", name, got)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.GenerateTeams(context.Background(), &corev1.GenerateTeamsRequest{Ref: ref}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the teams component.
func TestTeamsComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.TeamsComponentService"]
	if !ok || len(info.Methods) != 3 {
		t.Fatalf("takım servisi: %v", info)
	}
}
