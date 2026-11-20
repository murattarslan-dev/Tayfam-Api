package rest

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const teamsComponentService = "tayfam.core.v1.TeamsComponentService"

// fakeTeams is core's TeamsComponentService for the teams tests.
type fakeTeams struct {
	got   proto.Message
	token string
	out   *corev1.Teams
	err   error
	calls int
}

func (f *fakeTeams) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func (f *fakeTeams) GetTeams(ctx context.Context, in *corev1.GetTeamsRequest, _ ...grpc.CallOption) (*corev1.GetTeamsResponse, error) {
	f.rec(ctx, in)
	return &corev1.GetTeamsResponse{Teams: f.out}, f.err
}

func (f *fakeTeams) GenerateTeams(ctx context.Context, in *corev1.GenerateTeamsRequest, _ ...grpc.CallOption) (*corev1.GenerateTeamsResponse, error) {
	f.rec(ctx, in)
	return &corev1.GenerateTeamsResponse{Teams: f.out}, f.err
}

func (f *fakeTeams) ReplaceTeams(ctx context.Context, in *corev1.ReplaceTeamsRequest, _ ...grpc.CallOption) (*corev1.ReplaceTeamsResponse, error) {
	f.rec(ctx, in)
	return &corev1.ReplaceTeamsResponse{Teams: f.out}, f.err
}

const (
	teamsPath     = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/teams/teams"
	teamsGenerate = teamsPath + "/generate"
)

func testTeams() *corev1.Teams {
	return &corev1.Teams{
		Teams: []*corev1.Team{
			{Id: itemID, Position: 1, Name: "Kırmızılar", MemberIds: []string{userID}},
			{Id: entryID, Position: 2},
		},
		Mode: "balanced", TeamCount: 2, CallerCanEdit: true,
	}
}

func TestTeamsComponentRoutes(t *testing.T) {
	f := &fakeTeams{out: testTeams()}
	h := componentRouter(fakeServices{teamsComponentService: f})
	ref := &corev1.ComponentRef{TayfaId: tayfaID, EventId: eventID, ComponentKey: "teams"}

	for _, tc := range []struct {
		method, path, body string
		want               proto.Message
	}{
		{"GET", teamsPath, "", &corev1.GetTeamsRequest{Ref: ref}},
		{"POST", teamsGenerate, `{}`, &corev1.GenerateTeamsRequest{Ref: ref}},
		{"POST", teamsGenerate, `{"levels":[{"user_id":"` + userID + `","level":5}]}`,
			&corev1.GenerateTeamsRequest{Ref: ref, Levels: []*corev1.TeamLevel{{UserId: userID, Level: 5}}}},
		{"PUT", teamsPath, `{"teams":[{"name":"Kırmızılar","member_ids":["` + userID + `"]},{}]}`,
			&corev1.ReplaceTeamsRequest{Ref: ref, Teams: []*corev1.TeamInput{{Name: "Kırmızılar", MemberIds: []string{userID}}, {}}}},
		{"PUT", teamsPath, `{"teams":[]}`, &corev1.ReplaceTeamsRequest{Ref: ref, Teams: []*corev1.TeamInput{}}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s %s: core'a giden %v (%q), beklenen %v", tc.method, tc.path, f.got, f.token, tc.want)
		}
		got := decodeBody[teamsJSON](t, w)
		if len(got.Teams) != 2 || got.Mode != "balanced" || got.TeamCount != 2 || got.TeamSize != 0 || !got.CallerCanEdit {
			t.Fatalf("%s %s: yanıt %+v", tc.method, tc.path, got)
		}
		if tm := got.Teams[0]; tm.ID != itemID || tm.Position != 1 || tm.Name != "Kırmızılar" || len(tm.MemberIDs) != 1 || tm.MemberIDs[0] != userID {
			t.Fatalf("%s %s: takım %+v", tc.method, tc.path, tm)
		}
	}

	// An unnamed, empty team has no name and an empty member list; unset
	// parameters are left out.
	w := as(h, "GET", teamsPath, "")
	if want := `{"id":"` + entryID + `","position":2,"member_ids":[]}`; !strings.Contains(w.Body.String(), want) || strings.Contains(w.Body.String(), "team_size") {
		t.Fatalf("adsız takım: %s", w.Body)
	}
	f.out = &corev1.Teams{Mode: "random"}
	if w := as(h, "GET", teamsPath, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"teams":[]`) {
		t.Fatalf("takımsız: %d %s", w.Code, w.Body)
	}
}

func TestTeamsComponentRoutesValidateInput(t *testing.T) {
	f := &fakeTeams{out: &corev1.Teams{}}
	h := componentRouter(fakeServices{teamsComponentService: f})
	many := make([]string, 0, teamsPeopleMax+1)
	for range teamsPeopleMax + 1 {
		many = append(many, `{"user_id":"`+userID+`","level":3}`)
	}
	for name, body := range map[string]string{
		"bozuk kimlik":    `{"levels":[{"user_id":"x","level":3}]}`,
		"düşük seviye":    `{"levels":[{"user_id":"` + userID + `","level":0}]}`,
		"yüksek seviye":   `{"levels":[{"user_id":"` + userID + `","level":6}]}`,
		"iki kez":         `{"levels":[{"user_id":"` + userID + `","level":2},{"user_id":"` + userID + `","level":3}]}`,
		"çok seviye":      `{"levels":[` + strings.Join(many, ",") + `]}`,
		"bilinmeyen alan": `{"mode":"random"}`,
		"bozuk JSON":      `{"levels":`,
		"null gövde":      `null`,
		"dizi gövde":      `[]`,
	} {
		if w := as(h, "POST", teamsGenerate, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("kurma %s: %d %s", name, w.Code, w.Body)
		}
	}
	tooMany := strings.TrimSuffix(strings.Repeat(`{},`, teamsMax+1), ",")
	for name, body := range map[string]string{
		"boş gövde":       `{}`,
		"null takımlar":   `{"teams":null}`,
		"çok takım":       `{"teams":[` + tooMany + `]}`,
		"uzun ad":         `{"teams":[{"name":"` + strings.Repeat("ç", teamsNameMax+1) + `"}]}`,
		"bozuk kimlik":    `{"teams":[{"member_ids":["x"]}]}`,
		"iki takımda":     `{"teams":[{"member_ids":["` + userID + `"]},{"member_ids":["` + userID + `"]}]}`,
		"bilinmeyen alan": `{"teams":[{"position":1}]}`,
		"sayı ad":         `{"teams":[{"name":5}]}`,
	} {
		if w := as(h, "PUT", teamsPath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("elle %s: %d %s", name, w.Code, w.Body)
		}
	}
	// The path is checked before core is asked.
	for _, rt := range []struct{ method, path string }{
		{"GET", "/v1/tayfas/x/events/" + eventID + "/components/teams/teams"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/x/components/teams/teams"},
		{"POST", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Teams/teams/generate"},
	} {
		if w := as(h, rt.method, rt.path, `{}`); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call; a write without the client header is refused.
	for _, rt := range []struct{ method, path, body string }{
		{"GET", teamsPath, ""}, {"POST", teamsGenerate, `{}`}, {"PUT", teamsPath, `{"teams":[]}`},
	} {
		if w := do(h, rt.method, rt.path, rt.body, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", rt.method, rt.path, w.Code)
		}
		if rt.method == "GET" {
			continue
		}
		if w := do(h, rt.method, rt.path, rt.body, map[string]string{"Authorization": "Bearer T"}); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
			t.Fatalf("istemci başlıksız %s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestTeamsComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_TEAMS_MANUAL":          {codes.FailedPrecondition, http.StatusConflict, "teams-manual"},
		"ERROR_REASON_TEAMS_TOO_FEW":         {codes.FailedPrecondition, http.StatusConflict, "teams-too-few"},
		"ERROR_REASON_TEAMS_NOT_PARTICIPANT": {codes.FailedPrecondition, http.StatusConflict, "teams-not-participant"},
		"ERROR_REASON_COMPONENT_FROZEN":      {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_EVENT_LIMIT":           {codes.ResourceExhausted, http.StatusConflict, "event-limit"},
		"ERROR_REASON_PERMISSION_DENIED":     {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{teamsComponentService: &fakeTeams{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", teamsGenerate, `{}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
