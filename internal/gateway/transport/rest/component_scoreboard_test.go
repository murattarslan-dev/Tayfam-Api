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

const scoreboardComponentService = "tayfam.core.v1.ScoreboardComponentService"

// fakeScoreboards is core's ScoreboardComponentService for the scoreboard
// tests.
type fakeScoreboards struct {
	got   proto.Message
	token string
	out   *corev1.Scoreboard
	err   error
	calls int
}

func (f *fakeScoreboards) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func (f *fakeScoreboards) GetScoreboard(ctx context.Context, in *corev1.GetScoreboardRequest, _ ...grpc.CallOption) (*corev1.GetScoreboardResponse, error) {
	f.rec(ctx, in)
	return &corev1.GetScoreboardResponse{Scoreboard: f.out}, f.err
}

func (f *fakeScoreboards) CreateScoreTable(ctx context.Context, in *corev1.CreateScoreTableRequest, _ ...grpc.CallOption) (*corev1.CreateScoreTableResponse, error) {
	f.rec(ctx, in)
	return &corev1.CreateScoreTableResponse{Scoreboard: f.out}, f.err
}

func (f *fakeScoreboards) UpdateScoreTable(ctx context.Context, in *corev1.UpdateScoreTableRequest, _ ...grpc.CallOption) (*corev1.UpdateScoreTableResponse, error) {
	f.rec(ctx, in)
	return &corev1.UpdateScoreTableResponse{Scoreboard: f.out}, f.err
}

func (f *fakeScoreboards) DeleteScoreTable(ctx context.Context, in *corev1.DeleteScoreTableRequest, _ ...grpc.CallOption) (*corev1.DeleteScoreTableResponse, error) {
	f.rec(ctx, in)
	return &corev1.DeleteScoreTableResponse{Scoreboard: f.out}, f.err
}

func (f *fakeScoreboards) SetScorePlayers(ctx context.Context, in *corev1.SetScorePlayersRequest, _ ...grpc.CallOption) (*corev1.SetScorePlayersResponse, error) {
	f.rec(ctx, in)
	return &corev1.SetScorePlayersResponse{Scoreboard: f.out}, f.err
}

func (f *fakeScoreboards) AddScoreRound(ctx context.Context, in *corev1.AddScoreRoundRequest, _ ...grpc.CallOption) (*corev1.AddScoreRoundResponse, error) {
	f.rec(ctx, in)
	return &corev1.AddScoreRoundResponse{Scoreboard: f.out}, f.err
}

func (f *fakeScoreboards) CorrectScoreRound(ctx context.Context, in *corev1.CorrectScoreRoundRequest, _ ...grpc.CallOption) (*corev1.CorrectScoreRoundResponse, error) {
	f.rec(ctx, in)
	return &corev1.CorrectScoreRoundResponse{Scoreboard: f.out}, f.err
}

func (f *fakeScoreboards) DeleteScoreRound(ctx context.Context, in *corev1.DeleteScoreRoundRequest, _ ...grpc.CallOption) (*corev1.DeleteScoreRoundResponse, error) {
	f.rec(ctx, in)
	return &corev1.DeleteScoreRoundResponse{Scoreboard: f.out}, f.err
}

func (f *fakeScoreboards) FinishScoreTable(ctx context.Context, in *corev1.FinishScoreTableRequest, _ ...grpc.CallOption) (*corev1.FinishScoreTableResponse, error) {
	f.rec(ctx, in)
	return &corev1.FinishScoreTableResponse{Scoreboard: f.out}, f.err
}

const (
	scoreboardPath  = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/scoreboard/scoreboard"
	scoreTablesPath = scoreboardPath + "/tables"
	scoreTablePath  = scoreTablesPath + "/" + itemID
)

var scoreAt = time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)

func testScoreboard() *corev1.Scoreboard {
	rules := &corev1.ScoreRules{Side: "player", Rounds: "round", Winner: "low", Result: "points"}
	return &corev1.Scoreboard{
		Tables: []*corev1.ScoreTable{{
			Id: itemID, Label: "Masa 1", PresetKey: "okey_101", Rules: rules, Status: "open", CreateTime: timestamppb.New(scoreAt),
			Players:   []*corev1.ScorePlayer{{Id: entryID, Position: 1, UserId: userID}, {Id: vehicleID, Position: 2}},
			Rounds:    []*corev1.ScoreRound{{Number: 1, Scores: []*corev1.ScoreEntry{{PlayerId: entryID, Points: 0}, {PlayerId: vehicleID, Points: -101}}}},
			Standings: []*corev1.ScoreStanding{{PlayerId: vehicleID, Total: -101, Rank: 1}, {PlayerId: entryID, Total: 0, Rank: 2, RoundsWon: 0}},
		}},
		Presets:   []*corev1.ScorePreset{{Key: "okey_101", Labels: map[string]string{"tr": "101 Okey"}, PlayersMin: 2, PlayersMax: 4, Rules: rules}},
		Rules:     &corev1.ScoreRules{Side: "player", Rounds: "round", Winner: "high", Result: "points"},
		MaxTables: 20, CallerCanManage: true,
	}
}

func TestScoreboardComponentRoutes(t *testing.T) {
	f := &fakeScoreboards{out: testScoreboard()}
	h := componentRouter(fakeServices{scoreboardComponentService: f})
	ref := &corev1.ComponentRef{TayfaId: tayfaID, EventId: eventID, ComponentKey: "scoreboard"}
	label := "Final"
	round := `{"scores":[{"player_id":"` + entryID + `","points":12},{"player_id":"` + vehicleID + `","outcome":"win"}]}`
	scores := []*corev1.ScoreEntry{{PlayerId: entryID, Points: 12}, {PlayerId: vehicleID, Outcome: "win"}}

	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", scoreboardPath, "", http.StatusOK, &corev1.GetScoreboardRequest{Ref: ref}},
		{"POST", scoreTablesPath, `{"label":"Masa 1","preset_key":"okey_101","target":51,"players":[{"user_id":"` + userID + `"},{"team_id":"` + itemID + `"}]}`,
			http.StatusCreated, &corev1.CreateScoreTableRequest{Ref: ref, Label: "Masa 1", PresetKey: "okey_101", Target: 51,
				Players: []*corev1.ScorePlayerInput{{UserId: userID}, {TeamId: itemID}}}},
		{"POST", scoreTablesPath, `{"label":"Masa 2"}`, http.StatusCreated, &corev1.CreateScoreTableRequest{Ref: ref, Label: "Masa 2"}},
		{"PATCH", scoreTablePath, `{"label":" Final "}`, http.StatusOK, &corev1.UpdateScoreTableRequest{Ref: ref, TableId: itemID, Label: &label}},
		{"PATCH", scoreTablePath, `{"status":"finished"}`, http.StatusOK, &corev1.UpdateScoreTableRequest{Ref: ref, TableId: itemID, Status: "finished"}},
		{"DELETE", scoreTablePath, "", http.StatusOK, &corev1.DeleteScoreTableRequest{Ref: ref, TableId: itemID}},
		{"PUT", scoreTablePath + "/players", `{"players":[]}`, http.StatusOK, &corev1.SetScorePlayersRequest{Ref: ref, TableId: itemID}},
		{"POST", scoreTablePath + "/rounds", round, http.StatusOK, &corev1.AddScoreRoundRequest{Ref: ref, TableId: itemID, Scores: scores}},
		{"PUT", scoreTablePath + "/rounds/2", round, http.StatusOK, &corev1.CorrectScoreRoundRequest{Ref: ref, TableId: itemID, Round: 2, Scores: scores}},
		{"DELETE", scoreTablePath + "/rounds/200", "", http.StatusOK, &corev1.DeleteScoreRoundRequest{Ref: ref, TableId: itemID, Round: 200}},
		{"POST", scoreTablePath + "/finish", "", http.StatusOK, &corev1.FinishScoreTableRequest{Ref: ref, TableId: itemID}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s %s: core'a giden %v (%q), beklenen %v", tc.method, tc.path, f.got, f.token, tc.want)
		}
		got := decodeBody[scoreboardJSON](t, w)
		tb := got.Tables[0]
		if got.MaxTables != 20 || !got.CallerCanManage || got.Rules.Winner != "high" || got.Presets[0].Labels["tr"] != "101 Okey" ||
			tb.Label != "Masa 1" || tb.Rules.Winner != "low" || tb.Status != "open" || tb.FinishedAt != nil || !tb.CreatedAt.Equal(scoreAt) ||
			len(tb.Players) != 2 || tb.Players[0].UserID != userID || len(tb.Rounds) != 1 || tb.Rounds[0].Scores[0].Points == nil ||
			*tb.Rounds[0].Scores[0].Points != 0 || tb.Standings[0].Total != -101 || len(tb.WinnerIDs) != 0 {
			t.Fatalf("%s %s: yanıt %+v", tc.method, tc.path, got)
		}
	}

	// An empty seat has neither ID; a zero score is written in points mode.
	w := as(h, "GET", scoreboardPath, "")
	if b := w.Body.String(); !strings.Contains(b, `{"id":"`+vehicleID+`","position":2}`) || !strings.Contains(b, `"points":0`) ||
		!strings.Contains(b, `"winner_ids":[]`) || strings.Contains(b, "outcome") {
		t.Fatalf("boş yer ve sıfır puan: %s", b)
	}
	// wdl: the outcome, never points.
	f.out.Tables[0].Rules.Result = "wdl"
	f.out.Tables[0].Rounds[0].Scores = []*corev1.ScoreEntry{{PlayerId: entryID, Outcome: "draw"}}
	w = as(h, "GET", scoreboardPath, "")
	if b := w.Body.String(); !strings.Contains(b, `{"player_id":"`+entryID+`","outcome":"draw"}`) {
		t.Fatalf("wdl: %s", b)
	}
}

func TestScoreboardComponentRoutesValidateInput(t *testing.T) {
	f := &fakeScoreboards{out: &corev1.Scoreboard{}}
	h := componentRouter(fakeServices{scoreboardComponentService: f})
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/v1/tayfas/x/events/" + eventID + "/components/scoreboard/scoreboard", ""},
		{"POST", scoreTablesPath + "/x/finish", ""},
		{"POST", scoreTablesPath, `{}`},
		{"POST", scoreTablesPath, `{"label":"   "}`},
		{"POST", scoreTablesPath, `{"label":"` + strings.Repeat("ç", scoreLabelMax+1) + `"}`},
		{"POST", scoreTablesPath, `{"label":"Masa","preset_key":"Batak"}`},
		{"POST", scoreTablesPath, `{"label":"Masa","teams_key":"x"}`},
		{"POST", scoreTablesPath, `{"label":"Masa","target":-1}`},
		{"POST", scoreTablesPath, `{"label":"Masa","target":100001}`},
		{"POST", scoreTablesPath, `{"label":"Masa","players":[{}]}`},
		{"POST", scoreTablesPath, `{"label":"Masa","players":[{"user_id":"` + userID + `","team_id":"` + itemID + `"}]}`},
		{"POST", scoreTablesPath, `{"label":"Masa","players":[{"user_id":"` + userID + `"},{"user_id":"` + userID + `"}]}`},
		{"POST", scoreTablesPath, `{"label":"Masa","stake":5}`},
		{"PATCH", scoreTablePath, `{}`},
		{"PATCH", scoreTablePath, `{"status":"paused"}`},
		{"PUT", scoreTablePath + "/players", `{}`},
		{"POST", scoreTablePath + "/rounds", `{"scores":[]}`},
		{"POST", scoreTablePath + "/rounds", `{"scores":[{"player_id":"x","points":1}]}`},
		{"POST", scoreTablePath + "/rounds", `{"scores":[{"player_id":"` + entryID + `","points":1000001}]}`},
		{"POST", scoreTablePath + "/rounds", `{"scores":[{"player_id":"` + entryID + `","outcome":"lose"}]}`},
		{"POST", scoreTablePath + "/rounds", `{"scores":[{"player_id":"` + entryID + `"},{"player_id":"` + entryID + `"}]}`},
		{"PUT", scoreTablePath + "/rounds/0", `{"scores":[{"player_id":"` + entryID + `"}]}`},
		{"PUT", scoreTablePath + "/rounds/201", `{"scores":[{"player_id":"` + entryID + `"}]}`},
		{"DELETE", scoreTablePath + "/rounds/x", ""},
		{"DELETE", scoreTablePath + "/rounds/-1", ""},
	} {
		if w := as(h, rt.method, rt.path, rt.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s %s: %d %s", rt.method, rt.path, rt.body, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	if w := do(h, "POST", scoreTablePath+"/finish", "", map[string]string{"Authorization": "Bearer T"}); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
		t.Fatalf("istemci başlıksız: %d %s", w.Code, w.Body)
	}
}

// core's refusals reach the client as their problem types.
func TestScoreboardComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_SCORE_PRESET_UNAVAILABLE":     {codes.FailedPrecondition, http.StatusConflict, "score-preset-unavailable"},
		"ERROR_REASON_SCORE_TABLE_NOT_FOUND":        {codes.NotFound, http.StatusNotFound, "score-table-not-found"},
		"ERROR_REASON_SCORE_TABLE_FINISHED":         {codes.FailedPrecondition, http.StatusConflict, "score-table-finished"},
		"ERROR_REASON_SCORE_TABLE_STARTED":          {codes.FailedPrecondition, http.StatusConflict, "score-table-started"},
		"ERROR_REASON_SCORE_PLAYER_NOT_PARTICIPANT": {codes.FailedPrecondition, http.StatusConflict, "score-player-not-participant"},
		"ERROR_REASON_SCORE_NOT_STARTED":            {codes.FailedPrecondition, http.StatusConflict, "score-not-started"},
		"ERROR_REASON_EVENT_LIMIT":                  {codes.ResourceExhausted, http.StatusConflict, "event-limit"},
		"ERROR_REASON_PERMISSION_DENIED":            {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{scoreboardComponentService: &fakeScoreboards{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", scoreTablePath+"/finish", ""); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
