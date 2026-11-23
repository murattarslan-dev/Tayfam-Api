package rest

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const bracketComponentService = "tayfam.core.v1.BracketComponentService"

// fakeBrackets is core's BracketComponentService for the bracket tests.
type fakeBrackets struct {
	got   proto.Message
	token string
	out   *corev1.Bracket
	err   error
	calls int
}

func (f *fakeBrackets) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func (f *fakeBrackets) GetBracket(ctx context.Context, in *corev1.GetBracketRequest, _ ...grpc.CallOption) (*corev1.GetBracketResponse, error) {
	f.rec(ctx, in)
	return &corev1.GetBracketResponse{Bracket: f.out}, f.err
}

func (f *fakeBrackets) StartBracket(ctx context.Context, in *corev1.StartBracketRequest, _ ...grpc.CallOption) (*corev1.StartBracketResponse, error) {
	f.rec(ctx, in)
	return &corev1.StartBracketResponse{Bracket: f.out}, f.err
}

func (f *fakeBrackets) ResetBracket(ctx context.Context, in *corev1.ResetBracketRequest, _ ...grpc.CallOption) (*corev1.ResetBracketResponse, error) {
	f.rec(ctx, in)
	return &corev1.ResetBracketResponse{Bracket: f.out}, f.err
}

func (f *fakeBrackets) PairBracketRound(ctx context.Context, in *corev1.PairBracketRoundRequest, _ ...grpc.CallOption) (*corev1.PairBracketRoundResponse, error) {
	f.rec(ctx, in)
	return &corev1.PairBracketRoundResponse{Bracket: f.out}, f.err
}

func (f *fakeBrackets) AddBracketGame(ctx context.Context, in *corev1.AddBracketGameRequest, _ ...grpc.CallOption) (*corev1.AddBracketGameResponse, error) {
	f.rec(ctx, in)
	return &corev1.AddBracketGameResponse{Bracket: f.out}, f.err
}

func (f *fakeBrackets) CorrectBracketGame(ctx context.Context, in *corev1.CorrectBracketGameRequest, _ ...grpc.CallOption) (*corev1.CorrectBracketGameResponse, error) {
	f.rec(ctx, in)
	return &corev1.CorrectBracketGameResponse{Bracket: f.out}, f.err
}

func (f *fakeBrackets) DeleteBracketGame(ctx context.Context, in *corev1.DeleteBracketGameRequest, _ ...grpc.CallOption) (*corev1.DeleteBracketGameResponse, error) {
	f.rec(ctx, in)
	return &corev1.DeleteBracketGameResponse{Bracket: f.out}, f.err
}

const bracketPath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/bracket/bracket"

var bracketStart = time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)

func testBracket() *corev1.Bracket {
	return &corev1.Bracket{
		Started: true, Format: "elimination", BestOf: 1, Result: "points", Winner: "high", DrawKey: "draw", Drawn: true,
		Subject: "participants", Rounds: 2, CurrentRound: 1, StartTime: timestamppb.New(bracketStart),
		Entrants: []*corev1.BracketEntrant{{Position: 1, UserId: userID}, {Position: 2}, {Position: 3, UserId: userID}},
		Matches: []*corev1.BracketMatch{
			{Id: itemID, Round: 1, Slot: 1, APosition: 1, BPosition: 2, Games: []*corev1.BracketGame{{Number: 1, APoints: 0, BPoints: 3}},
				Decided: true, WinnerPosition: 2, BTotal: 3, BGamesWon: 1},
			{Id: itemID, Round: 1, Slot: 2, APosition: 3, Decided: true, Bye: true, WinnerPosition: 3},
		},
		Finished: false, CallerCanManage: true,
	}
}

func TestBracketComponentRoutes(t *testing.T) {
	f := &fakeBrackets{out: testBracket()}
	h := componentRouter(fakeServices{bracketComponentService: f})
	ref := &corev1.ComponentRef{TayfaId: tayfaID, EventId: eventID, ComponentKey: "bracket"}
	games := bracketPath + "/matches/" + itemID + "/games"

	for _, tc := range []struct {
		method, path, body string
		want               proto.Message
	}{
		{"GET", bracketPath, "", &corev1.GetBracketRequest{Ref: ref}},
		{"POST", bracketPath, "", &corev1.StartBracketRequest{Ref: ref}},
		{"DELETE", bracketPath, "", &corev1.ResetBracketRequest{Ref: ref}},
		{"POST", bracketPath + "/rounds", "", &corev1.PairBracketRoundRequest{Ref: ref}},
		{"POST", games, `{"a_points":0,"b_points":-3}`, &corev1.AddBracketGameRequest{Ref: ref, MatchId: itemID, BPoints: -3}},
		{"POST", games, `{"a_outcome":"draw"}`, &corev1.AddBracketGameRequest{Ref: ref, MatchId: itemID, AOutcome: "draw"}},
		{"PUT", games + "/2", `{"a_outcome":"loss"}`, &corev1.CorrectBracketGameRequest{Ref: ref, MatchId: itemID, Game: 2, AOutcome: "loss"}},
		{"DELETE", games + "/9", "", &corev1.DeleteBracketGameRequest{Ref: ref, MatchId: itemID, Game: 9}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s %s: core'a giden %v (%q), beklenen %v", tc.method, tc.path, f.got, f.token, tc.want)
		}
		got := decodeBody[bracketJSON](t, w)
		if !got.Started || got.Format != "elimination" || got.BestOf != 1 || got.Result != "points" || got.DrawKey != "draw" ||
			got.StartedAt == nil || !got.StartedAt.Equal(bracketStart) || len(got.Entrants) != 3 || got.Entrants[0].UserID != userID ||
			len(got.Matches) != 2 || got.Matches[0].WinnerPosition != 2 || len(got.Matches[0].Games) != 1 ||
			got.Matches[0].Games[0].APoints == nil || *got.Matches[0].Games[0].APoints != 0 || !got.Matches[1].Bye ||
			got.Finished || !got.CallerCanManage || got.Standings == nil || got.WinnerPositions == nil {
			t.Fatalf("%s %s: yanıt %+v", tc.method, tc.path, got)
		}
	}

	// An empty place has neither ID; a bye has no B side; a zero point
	// stays.
	w := as(h, "GET", bracketPath, "")
	for _, want := range []string{`{"position":2}`, `"a_points":0`, `"standings":[]`, `"winner_positions":[]`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("%s yok: %s", want, w.Body)
		}
	}
	if strings.Contains(w.Body.String(), `"b_position":0`) {
		t.Fatalf("bay maçında B tarafı: %s", w.Body)
	}
	// Under wdl a game carries the outcome only.
	f.out = &corev1.Bracket{Result: "wdl", Matches: []*corev1.BracketMatch{{Id: itemID, Games: []*corev1.BracketGame{{Number: 1, AOutcome: "win"}}}}}
	w = as(h, "GET", bracketPath, "")
	if b := w.Body.String(); !strings.Contains(b, `"games":[{"number":1,"a_outcome":"win"}]`) || strings.Contains(b, "started_at") {
		t.Fatalf("wdl: %s", b)
	}
}

func TestBracketComponentRoutesValidateInput(t *testing.T) {
	f := &fakeBrackets{out: &corev1.Bracket{}}
	h := componentRouter(fakeServices{bracketComponentService: f})
	games := bracketPath + "/matches/" + itemID + "/games"
	for name, rt := range map[string]struct{ method, path, body string }{
		"bozuk tayfa":       {"GET", "/v1/tayfas/x/events/" + eventID + "/components/bracket/bracket", ""},
		"bozuk anahtar":     {"POST", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Bracket/bracket", ""},
		"bozuk maç":         {"POST", bracketPath + "/matches/x/games", `{"a_outcome":"win"}`},
		"sıfırıncı oyun":    {"PUT", games + "/0", `{"a_outcome":"win"}`},
		"onuncu oyun":       {"DELETE", games + "/10", ""},
		"sayı olmayan oyun": {"DELETE", games + "/x", ""},
		"boş gövde":         {"POST", games, `{}`},
		"tek puan":          {"POST", games, `{"a_points":3}`},
		"puan ve sonuç":     {"POST", games, `{"a_points":3,"b_points":1,"a_outcome":"win"}`},
		"bozuk sonuç":       {"POST", games, `{"a_outcome":"won"}`},
		"çok büyük puan":    {"POST", games, `{"a_points":1000001,"b_points":0}`},
		"bilinmeyen alan":   {"POST", games, `{"a_outcome":"win","player_id":"x"}`},
		"metin puan":        {"PUT", games + "/1", `{"a_points":"3","b_points":1}`},
	} {
		if w := as(h, rt.method, rt.path, rt.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call; a change without the client header is refused.
	for _, method := range []string{"GET", "POST", "DELETE"} {
		if w := do(h, method, bracketPath, "", map[string]string{clientHeader: "android"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s: %d", method, w.Code)
		}
	}
	if w := do(h, "POST", bracketPath, "", map[string]string{"Authorization": "Bearer T"}); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
		t.Fatalf("istemci başlıksız: %d %s", w.Code, w.Body)
	}
}

// core's refusals reach the client as their problem types.
func TestBracketComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_BRACKET_DRAW_REQUIRED":   {codes.FailedPrecondition, http.StatusConflict, "bracket-draw-required"},
		"ERROR_REASON_BRACKET_STARTED":         {codes.FailedPrecondition, http.StatusConflict, "bracket-started"},
		"ERROR_REASON_BRACKET_NOT_STARTED":     {codes.FailedPrecondition, http.StatusConflict, "bracket-not-started"},
		"ERROR_REASON_BRACKET_TOO_FEW":         {codes.FailedPrecondition, http.StatusConflict, "bracket-too-few"},
		"ERROR_REASON_BRACKET_MATCH_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "bracket-match-not-found"},
		"ERROR_REASON_BRACKET_GAME_NOT_FOUND":  {codes.NotFound, http.StatusNotFound, "bracket-game-not-found"},
		"ERROR_REASON_BRACKET_MATCH_DECIDED":   {codes.FailedPrecondition, http.StatusConflict, "bracket-match-decided"},
		"ERROR_REASON_BRACKET_ROUND_OPEN":      {codes.FailedPrecondition, http.StatusConflict, "bracket-round-open"},
		"ERROR_REASON_BRACKET_ROUND_CLOSED":    {codes.FailedPrecondition, http.StatusConflict, "bracket-round-closed"},
		"ERROR_REASON_BRACKET_FINISHED":        {codes.FailedPrecondition, http.StatusConflict, "bracket-finished"},
		"ERROR_REASON_SCORE_NOT_STARTED":       {codes.FailedPrecondition, http.StatusConflict, "score-not-started"},
		"ERROR_REASON_PERMISSION_DENIED":       {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{bracketComponentService: &fakeBrackets{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", bracketPath+"/matches/"+itemID+"/games", `{"a_outcome":"win"}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
