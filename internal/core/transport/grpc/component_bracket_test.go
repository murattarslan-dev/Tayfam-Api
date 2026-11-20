package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/bracket"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/draw"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeBrackets struct {
	err    error
	method string
	ref    component.Ref
	match  id.ID
	game   int32
	in     bracket.GameInput
	out    bracket.Bracket
	calls  int
}

func (f *fakeBrackets) rec(method string, ref component.Ref, match id.ID) (bracket.Bracket, error) {
	f.method, f.ref, f.match, f.calls = method, ref, match, f.calls+1
	return f.out, f.err
}

func (f *fakeBrackets) Get(_ context.Context, ref component.Ref) (bracket.Bracket, error) {
	return f.rec("Get", ref, id.ID{})
}

func (f *fakeBrackets) Start(_ context.Context, ref component.Ref) (bracket.Bracket, error) {
	return f.rec("Start", ref, id.ID{})
}

func (f *fakeBrackets) Reset(_ context.Context, ref component.Ref) (bracket.Bracket, error) {
	return f.rec("Reset", ref, id.ID{})
}

func (f *fakeBrackets) PairRound(_ context.Context, ref component.Ref) (bracket.Bracket, error) {
	return f.rec("PairRound", ref, id.ID{})
}

func (f *fakeBrackets) AddGame(_ context.Context, ref component.Ref, match id.ID, in bracket.GameInput) (bracket.Bracket, error) {
	f.in = in
	return f.rec("AddGame", ref, match)
}

func (f *fakeBrackets) CorrectGame(_ context.Context, ref component.Ref, match id.ID, game int32, in bracket.GameInput) (bracket.Bracket, error) {
	f.in, f.game = in, game
	return f.rec("CorrectGame", ref, match)
}

func (f *fakeBrackets) DeleteGame(_ context.Context, ref component.Ref, match id.ID, game int32) (bracket.Bracket, error) {
	f.game = game
	return f.rec("DeleteGame", ref, match)
}

func TestBracketComponentMethods(t *testing.T) {
	user, match := id.New(), id.New()
	at := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	f := &fakeBrackets{out: bracket.Bracket{
		Started: true, Drawn: true, CallerCanManage: true,
		Result: bracket.Result{
			Rules:   bracket.Rules{Format: "swiss", BestOf: 3, Result: "points", Winner: "high"},
			DrawKey: "draw", Subject: "participants", Rounds: 3, CurrentRound: 1, StartedAt: at,
			Entrants: []draw.Entry{{Position: 1, User: user}, {Position: 2}},
			Matches: []bracket.Match{{
				ID: match, Round: 1, Slot: 1, A: 1, B: 2, Games: []bracket.Game{{Number: 1, APoints: 11, BPoints: 4}},
				Decided: true, Winner: 1, ATotal: 11, BTotal: 4, AWon: 1, CallerCanScore: true,
			}},
			Standings: []bracket.Standing{{Position: 1, Played: 1, Won: 1, Points: 1, Buchholz: 0.5, Rank: 1}},
			Finished:  true, Winners: []int32{1},
		},
	}}
	s := NewBracketComponentServer(f, discard)
	u, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(u)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "bracket"}
	want := component.Ref{User: u, Tayfa: tf, Event: ev, Key: "bracket"}
	check := func(method string, m id.ID, got *corev1.Bracket, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if f.method != method || f.ref != want || f.match != m {
			t.Fatalf("%s: app'e giden %s %+v %v", method, f.method, f.ref, f.match)
		}
		pm := got.GetMatches()[0]
		if !got.GetStarted() || !got.GetDrawn() || got.GetFormat() != "swiss" || got.GetBestOf() != 3 || got.GetResult() != "points" ||
			got.GetWinner() != "high" || got.GetDrawKey() != "draw" || got.GetRounds() != 3 || got.GetCurrentRound() != 1 ||
			!got.GetStartTime().AsTime().Equal(at) || got.GetEntrants()[0].GetUserId() != user.String() || got.GetEntrants()[1].GetUserId() != "" ||
			pm.GetId() != match.String() || pm.GetAPosition() != 1 || pm.GetBPosition() != 2 || pm.GetGames()[0].GetBPoints() != 4 ||
			!pm.GetDecided() || pm.GetWinnerPosition() != 1 || pm.GetATotal() != 11 || pm.GetAGamesWon() != 1 || !pm.GetCallerCanScore() ||
			got.GetStandings()[0].GetBuchholz() != 0.5 || got.GetStandings()[0].GetRank() != 1 ||
			!got.GetFinished() || len(got.GetWinnerPositions()) != 1 || got.GetWinnerPositions()[0] != 1 || !got.GetCallerCanManage() {
			t.Fatalf("%s: yanıt %v", method, got)
		}
	}
	mid := match.String()

	got, err := s.GetBracket(ctx, &corev1.GetBracketRequest{Ref: ref})
	check("Get", id.ID{}, got.GetBracket(), err)
	started, err := s.StartBracket(ctx, &corev1.StartBracketRequest{Ref: ref})
	check("Start", id.ID{}, started.GetBracket(), err)
	reset, err := s.ResetBracket(ctx, &corev1.ResetBracketRequest{Ref: ref})
	check("Reset", id.ID{}, reset.GetBracket(), err)
	paired, err := s.PairBracketRound(ctx, &corev1.PairBracketRoundRequest{Ref: ref})
	check("PairRound", id.ID{}, paired.GetBracket(), err)
	added, err := s.AddBracketGame(ctx, &corev1.AddBracketGameRequest{Ref: ref, MatchId: mid, APoints: 11, BPoints: -2})
	check("AddGame", match, added.GetBracket(), err)
	if f.in.APoints != 11 || f.in.BPoints != -2 || f.in.AOutcome != "" {
		t.Fatalf("oyun: %+v", f.in)
	}
	corrected, err := s.CorrectBracketGame(ctx, &corev1.CorrectBracketGameRequest{Ref: ref, MatchId: mid, Game: 2, AOutcome: "draw"})
	check("CorrectGame", match, corrected.GetBracket(), err)
	if f.game != 2 || f.in.AOutcome != "draw" {
		t.Fatalf("düzeltme: %d %+v", f.game, f.in)
	}
	deleted, err := s.DeleteBracketGame(ctx, &corev1.DeleteBracketGameRequest{Ref: ref, MatchId: mid, Game: 3})
	check("DeleteGame", match, deleted.GetBracket(), err)
	if f.game != 3 {
		t.Fatalf("silinen oyun: %d", f.game)
	}

	// Before the start: no start time.
	f.out.Started = false
	got, err = s.GetBracket(ctx, &corev1.GetBracketRequest{Ref: ref})
	if err != nil || got.GetBracket().GetStartTime() != nil {
		t.Fatalf("başlamamış: %v %v", got, err)
	}
}

func TestBracketComponentValidation(t *testing.T) {
	f := &fakeBrackets{}
	s := NewBracketComponentServer(f, discard)
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: id.New().String(), EventId: id.New().String(), ComponentKey: "bracket"}
	mid := id.New().String()
	for name, call := range map[string]func() error{
		"bozuk maç": func() error {
			_, err := s.AddBracketGame(ctx, &corev1.AddBracketGameRequest{Ref: ref, MatchId: "x"})
			return err
		},
		"sıfırıncı oyun": func() error {
			_, err := s.CorrectBracketGame(ctx, &corev1.CorrectBracketGameRequest{Ref: ref, MatchId: mid, Game: 0})
			return err
		},
		"çok büyük oyun": func() error {
			_, err := s.DeleteBracketGame(ctx, &corev1.DeleteBracketGameRequest{Ref: ref, MatchId: mid, Game: bracket.MaxGames + 1})
			return err
		},
		"bozuk bileşen": func() error {
			_, err := s.StartBracket(ctx, &corev1.StartBracketRequest{Ref: &corev1.ComponentRef{TayfaId: ref.GetTayfaId(), EventId: ref.GetEventId(), ComponentKey: "Bracket"}})
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
	f.err = apperr.ErrBracketMatchNotFound
	_, err := s.AddBracketGame(ctx, &corev1.AddBracketGameRequest{Ref: ref, MatchId: mid, AOutcome: "win"})
	if code, info := infoOf(t, err); code != codes.NotFound || info.GetReason() != corev1.ErrorReason_ERROR_REASON_BRACKET_MATCH_NOT_FOUND.String() {
		t.Fatalf("olmayan maç: %v", err)
	}
	f.err = apperr.ErrBracketRoundOpen
	_, err = s.PairBracketRound(ctx, &corev1.PairBracketRoundRequest{Ref: ref})
	if code, info := infoOf(t, err); code != codes.FailedPrecondition || info.GetReason() != corev1.ErrorReason_ERROR_REASON_BRACKET_ROUND_OPEN.String() {
		t.Fatalf("açık tur: %v", err)
	}
}
