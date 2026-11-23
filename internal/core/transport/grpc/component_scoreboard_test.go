package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeScoreboards struct {
	err     error
	method  string
	ref     component.Ref
	table   id.ID
	round   int32
	create  scoreboard.TableInput
	update  scoreboard.TableUpdate
	players []scoreboard.PlayerInput
	scores  []scoreboard.ScoreInput
	out     scoreboard.Scoreboard
	calls   int
}

func (f *fakeScoreboards) rec(method string, ref component.Ref, table id.ID) (scoreboard.Scoreboard, error) {
	f.method, f.ref, f.table, f.calls = method, ref, table, f.calls+1
	return f.out, f.err
}

func (f *fakeScoreboards) Get(_ context.Context, ref component.Ref) (scoreboard.Scoreboard, error) {
	return f.rec("Get", ref, id.ID{})
}

func (f *fakeScoreboards) CreateTable(_ context.Context, ref component.Ref, in scoreboard.TableInput) (scoreboard.Scoreboard, error) {
	f.create = in
	return f.rec("CreateTable", ref, id.ID{})
}

func (f *fakeScoreboards) UpdateTable(_ context.Context, ref component.Ref, table id.ID, in scoreboard.TableUpdate) (scoreboard.Scoreboard, error) {
	f.update = in
	return f.rec("UpdateTable", ref, table)
}

func (f *fakeScoreboards) DeleteTable(_ context.Context, ref component.Ref, table id.ID) (scoreboard.Scoreboard, error) {
	return f.rec("DeleteTable", ref, table)
}

func (f *fakeScoreboards) SetPlayers(_ context.Context, ref component.Ref, table id.ID, in []scoreboard.PlayerInput) (scoreboard.Scoreboard, error) {
	f.players = in
	return f.rec("SetPlayers", ref, table)
}

func (f *fakeScoreboards) AddRound(_ context.Context, ref component.Ref, table id.ID, in []scoreboard.ScoreInput) (scoreboard.Scoreboard, error) {
	f.scores = in
	return f.rec("AddRound", ref, table)
}

func (f *fakeScoreboards) CorrectRound(_ context.Context, ref component.Ref, table id.ID, round int32, in []scoreboard.ScoreInput) (scoreboard.Scoreboard, error) {
	f.scores, f.round = in, round
	return f.rec("CorrectRound", ref, table)
}

func (f *fakeScoreboards) DeleteRound(_ context.Context, ref component.Ref, table id.ID, round int32) (scoreboard.Scoreboard, error) {
	f.round = round
	return f.rec("DeleteRound", ref, table)
}

func (f *fakeScoreboards) FinishTable(_ context.Context, ref component.Ref, table id.ID) (scoreboard.Scoreboard, error) {
	return f.rec("FinishTable", ref, table)
}

func TestScoreboardComponentMethods(t *testing.T) {
	seat, other, user, team := id.New(), id.New(), id.New(), id.New()
	at := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	rules := scoreboard.Rules{Side: "player", Rounds: "first_to", Winner: "first", Result: "points", Target: 5}
	table := scoreboard.Table{
		ID: id.New(), Label: "Tavla", Preset: "backgammon", Rules: rules, Status: "finished", FinishedAt: at, CreatedAt: at.Add(-time.Hour),
		Players: []scoreboard.Player{{ID: seat, Position: 1, User: user}, {ID: other, Position: 2}},
		Rounds:  []scoreboard.Round{{Number: 1, Scores: []scoreboard.Score{{Player: seat, Points: 5}, {Player: other, Points: -2}}}},
		Result: scoreboard.Result{
			Standings: []scoreboard.Standing{{Player: seat, Total: 5, RoundsWon: 1, Rank: 1}, {Player: other, Total: -2, Rank: 2}},
			Winners:   []id.ID{seat},
		},
	}
	f := &fakeScoreboards{out: scoreboard.Scoreboard{
		Tables:    []scoreboard.Table{table},
		Presets:   []scoreboard.Preset{{Key: "backgammon", Labels: map[string]string{"tr": "Tavla"}, PlayersMin: 2, PlayersMax: 2, Rules: rules}},
		Rules:     scoreboard.Rules{Side: "player", Rounds: "round", Winner: "high", Result: "points"},
		MaxTables: 20, CallerCanManage: true,
	}}
	s := NewScoreboardComponentServer(f, discard)
	u, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(u)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "scoreboard"}
	want := component.Ref{User: u, Tayfa: tf, Event: ev, Key: "scoreboard"}
	check := func(method string, tbl id.ID, got *corev1.Scoreboard, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if f.method != method || f.ref != want || f.table != tbl {
			t.Fatalf("%s: app'e giden %s %+v %v", method, f.method, f.ref, f.table)
		}
		tb := got.GetTables()[0]
		if got.GetMaxTables() != 20 || !got.GetCallerCanManage() || got.GetRules().GetWinner() != "high" ||
			got.GetPresets()[0].GetLabels()["tr"] != "Tavla" || got.GetPresets()[0].GetRules().GetTarget() != 5 ||
			tb.GetLabel() != "Tavla" || tb.GetPresetKey() != "backgammon" || tb.GetStatus() != "finished" ||
			!tb.GetFinishedTime().AsTime().Equal(at) || tb.GetPlayers()[0].GetUserId() != user.String() || tb.GetPlayers()[1].GetUserId() != "" ||
			tb.GetRounds()[0].GetScores()[1].GetPoints() != -2 || tb.GetStandings()[0].GetTotal() != 5 ||
			len(tb.GetWinnerIds()) != 1 || tb.GetWinnerIds()[0] != seat.String() {
			t.Fatalf("%s: yanıt %v", method, got)
		}
	}
	tid := table.ID.String()
	scores := []*corev1.ScoreEntry{{PlayerId: seat.String(), Points: 3}, {PlayerId: other.String(), Outcome: "win"}}

	got, err := s.GetScoreboard(ctx, &corev1.GetScoreboardRequest{Ref: ref})
	check("Get", id.ID{}, got.GetScoreboard(), err)
	created, err := s.CreateScoreTable(ctx, &corev1.CreateScoreTableRequest{
		Ref: ref, Label: "Masa", PresetKey: "batak", Target: 51, TeamsKey: "teams",
		Players: []*corev1.ScorePlayerInput{{UserId: user.String()}, {TeamId: team.String()}},
	})
	check("CreateTable", id.ID{}, created.GetScoreboard(), err)
	if c := f.create; c.Label != "Masa" || c.Preset != "batak" || c.Target != 51 || c.TeamsKey != "teams" ||
		len(c.Players) != 2 || c.Players[0].User != user || c.Players[1].Team != team {
		t.Fatalf("masa girdisi: %+v", c)
	}
	updated, err := s.UpdateScoreTable(ctx, &corev1.UpdateScoreTableRequest{Ref: ref, TableId: tid, Status: "open"})
	check("UpdateTable", table.ID, updated.GetScoreboard(), err)
	if f.update.Label != nil || f.update.Status != "open" {
		t.Fatalf("güncelleme: %+v", f.update)
	}
	label := "Final"
	_, err = s.UpdateScoreTable(ctx, &corev1.UpdateScoreTableRequest{Ref: ref, TableId: tid, Label: &label})
	if err != nil || f.update.Label == nil || *f.update.Label != "Final" {
		t.Fatalf("ad: %+v %v", f.update, err)
	}
	deleted, err := s.DeleteScoreTable(ctx, &corev1.DeleteScoreTableRequest{Ref: ref, TableId: tid})
	check("DeleteTable", table.ID, deleted.GetScoreboard(), err)
	seated, err := s.SetScorePlayers(ctx, &corev1.SetScorePlayersRequest{Ref: ref, TableId: tid})
	check("SetPlayers", table.ID, seated.GetScoreboard(), err)
	added, err := s.AddScoreRound(ctx, &corev1.AddScoreRoundRequest{Ref: ref, TableId: tid, Scores: scores})
	check("AddRound", table.ID, added.GetScoreboard(), err)
	if len(f.scores) != 2 || f.scores[0].Points != 3 || f.scores[1].Outcome != "win" || f.scores[1].Player != other {
		t.Fatalf("skorlar: %+v", f.scores)
	}
	corrected, err := s.CorrectScoreRound(ctx, &corev1.CorrectScoreRoundRequest{Ref: ref, TableId: tid, Round: 2, Scores: scores})
	check("CorrectRound", table.ID, corrected.GetScoreboard(), err)
	removed, err := s.DeleteScoreRound(ctx, &corev1.DeleteScoreRoundRequest{Ref: ref, TableId: tid, Round: 3})
	check("DeleteRound", table.ID, removed.GetScoreboard(), err)
	if f.round != 3 {
		t.Fatalf("el: %d", f.round)
	}
	finished, err := s.FinishScoreTable(ctx, &corev1.FinishScoreTableRequest{Ref: ref, TableId: tid})
	check("FinishTable", table.ID, finished.GetScoreboard(), err)

	// An open table has no finish time.
	f.out.Tables[0].Status, f.out.Tables[0].FinishedAt = "open", time.Time{}
	got, err = s.GetScoreboard(ctx, &corev1.GetScoreboardRequest{Ref: ref})
	if err != nil || got.GetScoreboard().GetTables()[0].GetFinishedTime() != nil {
		t.Fatalf("açık masa: %v %v", got, err)
	}
}

func TestScoreboardComponentValidation(t *testing.T) {
	f := &fakeScoreboards{}
	s := NewScoreboardComponentServer(f, discard)
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: id.New().String(), EventId: id.New().String(), ComponentKey: "scoreboard"}
	tid, seat := id.New().String(), id.New().String()
	ok := []*corev1.ScoreEntry{{PlayerId: seat}}
	for name, call := range map[string]func() error{
		"bozuk masa": func() error {
			_, err := s.FinishScoreTable(ctx, &corev1.FinishScoreTableRequest{Ref: ref, TableId: "x"})
			return err
		},
		"iki kimlikli yer": func() error {
			_, err := s.SetScorePlayers(ctx, &corev1.SetScorePlayersRequest{Ref: ref, TableId: tid,
				Players: []*corev1.ScorePlayerInput{{UserId: seat, TeamId: seat}}})
			return err
		},
		"kimliksiz yer": func() error {
			_, err := s.CreateScoreTable(ctx, &corev1.CreateScoreTableRequest{Ref: ref, Label: "Masa", Players: []*corev1.ScorePlayerInput{{}}})
			return err
		},
		"skorsuz el": func() error {
			_, err := s.AddScoreRound(ctx, &corev1.AddScoreRoundRequest{Ref: ref, TableId: tid})
			return err
		},
		"bozuk yer kimliği": func() error {
			_, err := s.AddScoreRound(ctx, &corev1.AddScoreRoundRequest{Ref: ref, TableId: tid, Scores: []*corev1.ScoreEntry{{PlayerId: "x"}}})
			return err
		},
		"sıfırıncı el": func() error {
			_, err := s.CorrectScoreRound(ctx, &corev1.CorrectScoreRoundRequest{Ref: ref, TableId: tid, Round: 0, Scores: ok})
			return err
		},
		"çok büyük el": func() error {
			_, err := s.DeleteScoreRound(ctx, &corev1.DeleteScoreRoundRequest{Ref: ref, TableId: tid, Round: scoreboard.MaxRounds + 1})
			return err
		},
	} {
		if code, info := infoOf(t, call()); code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Errorf("%s: %v %v", name, code, info)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app'e ulaştı: %d", f.calls)
	}
	f.err = apperr.ErrScoreTableNotFound
	_, err := s.FinishScoreTable(ctx, &corev1.FinishScoreTableRequest{Ref: ref, TableId: tid})
	if code, info := infoOf(t, err); code != codes.NotFound || info.GetReason() != corev1.ErrorReason_ERROR_REASON_SCORE_TABLE_NOT_FOUND.String() {
		t.Fatalf("olmayan masa: %v", err)
	}
}
