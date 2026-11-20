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

const guestPlayerService = "tayfam.core.v1.GuestPlayerComponentService"

// fakeGuestPlayers is core's GuestPlayerComponentService for the guest
// tests.
type fakeGuestPlayers struct {
	got   proto.Message
	token string
	out   *corev1.GuestPlayers
	err   error
	calls int
}

func (f *fakeGuestPlayers) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func (f *fakeGuestPlayers) ListGuestPlayers(ctx context.Context, in *corev1.ListGuestPlayersRequest, _ ...grpc.CallOption) (*corev1.ListGuestPlayersResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListGuestPlayersResponse{Guests: f.out}, f.err
}

func (f *fakeGuestPlayers) AddGuestPlayer(ctx context.Context, in *corev1.AddGuestPlayerRequest, _ ...grpc.CallOption) (*corev1.AddGuestPlayerResponse, error) {
	f.rec(ctx, in)
	return &corev1.AddGuestPlayerResponse{Guests: f.out}, f.err
}

func (f *fakeGuestPlayers) RemoveGuestPlayer(ctx context.Context, in *corev1.RemoveGuestPlayerRequest, _ ...grpc.CallOption) (*corev1.RemoveGuestPlayerResponse, error) {
	f.rec(ctx, in)
	return &corev1.RemoveGuestPlayerResponse{Guests: f.out}, f.err
}

const guestsPath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/scoreboard/guests"

var guestAddedAt = time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)

func testGuests() *corev1.GuestPlayers {
	return &corev1.GuestPlayers{
		Guests: []*corev1.GuestPlayer{
			{Id: itemID, Nickname: "Kuzen Ali", CreateTime: timestamppb.New(guestAddedAt)},
			{Id: entryID, CreateTime: timestamppb.New(guestAddedAt)},
		},
		MaxGuests: 30, CallerCanAdd: true,
	}
}

func TestGuestPlayerRoutes(t *testing.T) {
	f := &fakeGuestPlayers{out: testGuests()}
	h := componentRouter(fakeServices{guestPlayerService: f})
	ref := &corev1.ComponentRef{TayfaId: tayfaID, EventId: eventID, ComponentKey: "scoreboard"}

	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", guestsPath, "", http.StatusOK, &corev1.ListGuestPlayersRequest{Ref: ref}},
		{"POST", guestsPath, `{"nickname":"Kuzen Ali"}`, http.StatusCreated, &corev1.AddGuestPlayerRequest{Ref: ref, Nickname: "Kuzen Ali"}},
		{"POST", guestsPath, `{"nickname":"Ali","table_id":"` + itemID + `"}`, http.StatusCreated,
			&corev1.AddGuestPlayerRequest{Ref: ref, Nickname: "Ali", TableId: itemID}},
		{"DELETE", guestsPath + "/" + entryID, "", http.StatusOK, &corev1.RemoveGuestPlayerRequest{Ref: ref, GuestId: entryID}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.method, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s: core'a giden %v (%q), beklenen %v", tc.method, f.got, f.token, tc.want)
		}
		got := decodeBody[guestPlayersJSON](t, w)
		if len(got.Guests) != 2 || got.Guests[0].ID != itemID || got.Guests[0].Nickname != "Kuzen Ali" ||
			!got.Guests[0].CreatedAt.Equal(guestAddedAt) || got.Guests[1].Nickname != "" || got.MaxGuests != 30 || !got.CallerCanAdd || got.CallerCanRemove {
			t.Fatalf("%s: yanıt %+v", tc.method, got)
		}
	}
	// A cleared nickname is left out.
	if w := as(h, "GET", guestsPath, ""); !strings.Contains(w.Body.String(), `{"id":"`+entryID+`","created_at"`) {
		t.Fatalf("silinmiş takma ad: %s", w.Body)
	}
}

func TestGuestPlayerRoutesValidateInput(t *testing.T) {
	f := &fakeGuestPlayers{out: &corev1.GuestPlayers{}}
	h := componentRouter(fakeServices{guestPlayerService: f})
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/v1/tayfas/x/events/" + eventID + "/components/scoreboard/guests", ""},
		{"POST", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Score/guests", `{"nickname":"Ali"}`},
		{"POST", guestsPath, `{}`},
		{"POST", guestsPath, `{"nickname":"   "}`},
		{"POST", guestsPath, `{"nickname":"` + strings.Repeat("a", 25) + `"}`},
		{"POST", guestsPath, `{"nickname":"Ali","table_id":"x"}`},
		{"POST", guestsPath, `{"nickname":"Ali","extra":1}`},
		{"POST", guestsPath, `{"nickname":"` + strings.Repeat("a", 2000) + `"}`},
		{"DELETE", guestsPath + "/x", ""},
	} {
		if w := as(h, rt.method, rt.path, rt.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s %s: %d %s", rt.method, rt.path, rt.body, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
}

// core's refusals reach the client as their problem types.
func TestGuestPlayerErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_GUEST_PLAYERS_DISABLED": {codes.FailedPrecondition, http.StatusConflict, "guest-players-disabled"},
		"ERROR_REASON_GUEST_PLAYER_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "guest-player-not-found"},
		"ERROR_REASON_GUEST_PLAYER_IN_USE":    {codes.FailedPrecondition, http.StatusConflict, "guest-player-in-use"},
		"ERROR_REASON_SCORE_TABLE_NOT_FOUND":  {codes.NotFound, http.StatusNotFound, "score-table-not-found"},
		"ERROR_REASON_EVENT_LIMIT":            {codes.ResourceExhausted, http.StatusConflict, "event-limit"},
		"ERROR_REASON_PERMISSION_DENIED":      {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{guestPlayerService: &fakeGuestPlayers{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", guestsPath, `{"nickname":"Ali"}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}

// A guest's seat and draw place carry its ID; a seat body names one of
// user_id, team_id and guest_id.
func TestGuestPlayerSeats(t *testing.T) {
	in, ok := scorePlayersIn([]scorePlayerBody{{GuestID: itemID}, {UserID: userID}})
	if !ok || len(in) != 2 || in[0].GetGuestId() != itemID || in[1].GetUserId() != userID {
		t.Fatalf("misafir yeri: %v %v", in, ok)
	}
	for _, bad := range [][]scorePlayerBody{
		{{GuestID: "x"}},
		{{GuestID: itemID, UserID: userID}},
		{{GuestID: itemID}, {GuestID: itemID}},
	} {
		if _, ok := scorePlayersIn(bad); ok {
			t.Errorf("%+v kabul edildi", bad)
		}
	}
	sb := scoreTableFrom(&corev1.ScoreTable{Players: []*corev1.ScorePlayer{{Id: entryID, Position: 1, GuestId: itemID}}})
	if p := sb.Players[0]; p.GuestID != itemID || p.UserID != "" {
		t.Fatalf("masadaki misafir: %+v", p)
	}
	d := drawFrom(&corev1.Draw{Entries: []*corev1.DrawEntry{{Position: 1, GuestId: itemID}}})
	if e := d.Entries[0]; e.GuestID != itemID || e.UserID != "" {
		t.Fatalf("kuradaki misafir: %+v", e)
	}
}
