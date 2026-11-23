package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const voteComponentService = "tayfam.core.v1.VoteComponentService"

// fakeVotes is core's VoteComponentService for the vote tests.
type fakeVotes struct {
	ref    *corev1.ComponentRef
	option string
	label  string
	last   string
	token  string
	out    *corev1.EventVote
	err    error
	calls  int
}

func (f *fakeVotes) seen(ctx context.Context, ref *corev1.ComponentRef, last string) {
	f.ref, f.token, f.last, f.calls = ref, tokenFrom(ctx), last, f.calls+1
}

func (f *fakeVotes) GetVote(ctx context.Context, in *corev1.GetVoteRequest, _ ...grpc.CallOption) (*corev1.GetVoteResponse, error) {
	f.seen(ctx, in.GetRef(), "get")
	return &corev1.GetVoteResponse{Vote: f.out}, f.err
}

func (f *fakeVotes) CastVote(ctx context.Context, in *corev1.CastVoteRequest, _ ...grpc.CallOption) (*corev1.CastVoteResponse, error) {
	f.seen(ctx, in.GetRef(), "cast")
	f.option = in.GetOptionId()
	return &corev1.CastVoteResponse{Vote: f.out}, f.err
}

func (f *fakeVotes) WithdrawVote(ctx context.Context, in *corev1.WithdrawVoteRequest, _ ...grpc.CallOption) (*corev1.WithdrawVoteResponse, error) {
	f.seen(ctx, in.GetRef(), "withdraw")
	return &corev1.WithdrawVoteResponse{Vote: f.out}, f.err
}

func (f *fakeVotes) AddVoteOption(ctx context.Context, in *corev1.AddVoteOptionRequest, _ ...grpc.CallOption) (*corev1.AddVoteOptionResponse, error) {
	f.seen(ctx, in.GetRef(), "add")
	f.label = in.GetLabel()
	return &corev1.AddVoteOptionResponse{Vote: f.out}, f.err
}

func (f *fakeVotes) RemoveVoteOption(ctx context.Context, in *corev1.RemoveVoteOptionRequest, _ ...grpc.CallOption) (*corev1.RemoveVoteOptionResponse, error) {
	f.seen(ctx, in.GetRef(), "remove")
	f.option = in.GetOptionId()
	return &corev1.RemoveVoteOptionResponse{Vote: f.out}, f.err
}

func (f *fakeVotes) CloseVote(ctx context.Context, in *corev1.CloseVoteRequest, _ ...grpc.CallOption) (*corev1.CloseVoteResponse, error) {
	f.seen(ctx, in.GetRef(), "close")
	return &corev1.CloseVoteResponse{Vote: f.out}, f.err
}

const votePath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/best_car/vote"

func TestVoteComponentRoutes(t *testing.T) {
	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	f := &fakeVotes{out: &corev1.EventVote{
		Source: "vehicles", VoterCount: 2, CallerOptionId: itemID, Closed: true, CloseTime: timestamppb.New(at), ResultsVisible: true,
		CallerCanVote: true, CallerCanEditOptions: false, CallerCanClose: true, MaxOptions: 20,
		Options: []*corev1.VoteOption{
			{Id: itemID, UserId: userID, Vehicle: &corev1.VoteVehicle{Make: "Ford", ModelYear: 1967}, VoteCount: 2, Winner: true},
			{Id: eventID, UserId: userID, UploadId: tayfaID},
		},
	}}
	h := componentRouter(fakeServices{voteComponentService: f})

	w := as(h, "GET", votePath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("okuma: %d %s", w.Code, w.Body)
	}
	if ref := f.ref; ref.GetTayfaId() != tayfaID || ref.GetEventId() != eventID || ref.GetComponentKey() != "best_car" || f.token != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", f.ref, f.token)
	}
	got := decodeBody[eventVoteJSON](t, w)
	if got.Source != "vehicles" || got.VoterCount != 2 || got.CallerOptionID != itemID || !got.Closed || got.ClosedAt == nil ||
		!got.ClosedAt.Equal(at) || !got.ResultsVisible || !got.CallerCanVote || got.CallerCanEditOptions || !got.CallerCanClose ||
		got.MaxOptions != 20 || len(got.Options) != 2 {
		t.Fatalf("oylama: %+v", got)
	}
	if o := got.Options[0]; o.ID != itemID || o.UserID != userID || o.Vehicle == nil || o.Vehicle.Make != "Ford" || o.Vehicle.ModelYear != 1967 ||
		o.VoteCount != 2 || !o.Winner || o.Label != "" {
		t.Fatalf("araç seçeneği: %+v", o)
	}
	if o := got.Options[1]; o.UploadID != tayfaID || o.Vehicle != nil || o.VoteCount != 0 || o.Winner {
		t.Fatalf("fotoğraf seçeneği: %+v", o)
	}

	for _, tc := range []struct {
		method, path, body, last string
	}{
		{"PUT", votePath, `{"option_id":"` + itemID + `"}`, "cast"},
		{"DELETE", votePath, "", "withdraw"},
		{"POST", votePath + "/options", `{"label":"Suç ve Ceza"}`, "add"},
		{"DELETE", votePath + "/options/" + itemID, "", "remove"},
		{"POST", votePath + "/close", "", "close"},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != http.StatusOK || f.last != tc.last || f.ref.GetComponentKey() != "best_car" {
			t.Fatalf("%s %s: %d %s (%s)", tc.method, tc.path, w.Code, w.Body, f.last)
		}
	}
	f.last = ""
	as(h, "PUT", votePath, `{"option_id":"`+eventID+`"}`)
	if f.option != eventID {
		t.Fatalf("oy: %q", f.option)
	}
	as(h, "POST", votePath+"/options", `{"label":"Tutunamayanlar"}`)
	if f.label != "Tutunamayanlar" {
		t.Fatalf("seçenek: %q", f.label)
	}

	// Hidden counts never reach the client; empty fields are left out.
	f.out = &corev1.EventVote{Source: "free", Options: []*corev1.VoteOption{{Id: itemID, Label: "Bir", VoteCount: 3, Winner: true}}}
	w = as(h, "GET", votePath, "")
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(body, `"closed_at"`) || strings.Contains(body, `"caller_option_id"`) ||
		!strings.Contains(body, `"vote_count":0`) || !strings.Contains(body, `"winner":false`) || strings.Contains(body, `"vehicle"`) {
		t.Fatalf("gizli: %d %s", w.Code, body)
	}
}

func TestVoteComponentRoutesValidateInput(t *testing.T) {
	f := &fakeVotes{out: &corev1.EventVote{}}
	h := componentRouter(fakeServices{voteComponentService: f})

	for name, tc := range map[string]struct{ method, path, body string }{
		"bozuk seçenek":       {"PUT", votePath, `{"option_id":"x"}`},
		"seçeneksiz":          {"PUT", votePath, `{}`},
		"fazla alan":          {"PUT", votePath, `{"option_id":"` + itemID + `","user_id":"` + userID + `"}`},
		"bozuk JSON":          {"PUT", votePath, `{"option_id":`},
		"boş etiket":          {"POST", votePath + "/options", `{"label":"   "}`},
		"etiketsiz":           {"POST", votePath + "/options", `{}`},
		"uzun etiket":         {"POST", votePath + "/options", `{"label":"` + strings.Repeat("a", maxVoteLabelBytes+1) + `"}`},
		"bozuk seçenek yolu":  {"DELETE", votePath + "/options/x", ""},
		"bozuk bileşen":       {"GET", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Car/vote", ""},
		"bozuk bileşen kapat": {"POST", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Car/vote/close", ""},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call.
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		if w := do(h, method, votePath, `{"option_id":"`+itemID+`"}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s: %d", method, w.Code)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestVoteComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_VOTE_CLOSED":          {codes.FailedPrecondition, http.StatusConflict, "vote-closed"},
		"ERROR_REASON_VOTE_OWN_OPTION":      {codes.FailedPrecondition, http.StatusConflict, "vote-own-option"},
		"ERROR_REASON_VOTE_OPTIONS_FIXED":   {codes.FailedPrecondition, http.StatusConflict, "vote-options-fixed"},
		"ERROR_REASON_VOTE_OPTION_EXISTS":   {codes.AlreadyExists, http.StatusConflict, "vote-option-exists"},
		"ERROR_REASON_EVENT_NOT_GOING":      {codes.FailedPrecondition, http.StatusConflict, "event-not-going"},
		"ERROR_REASON_EVENT_ITEM_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "event-item-not-found"},
		"ERROR_REASON_COMPONENT_FROZEN":     {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_PERMISSION_DENIED":    {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{voteComponentService: &fakeVotes{err: coreErr(want.grpc, reason)}})
		if w := as(h, "PUT", votePath, `{"option_id":"`+itemID+`"}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}
