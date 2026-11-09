package rest

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const (
	pollID    = "01965a3c-0000-7000-8000-000000000010"
	optionID  = "01965a3c-0000-7000-8000-000000000011"
	option2ID = "01965a3c-0000-7000-8000-000000000012"
)

func testPoll() *corev1.Poll {
	return &corev1.Poll{
		Id: pollID, TayfaId: tayfaID, CreatedByUserId: userID, Question: "Kamp hangi hafta sonu?",
		Anonymous: true, CloseTime: timestamppb.New(t0.Add(48 * 3600e9)), CreateTime: timestamppb.New(t0),
		Options: []*corev1.PollOption{
			{Id: optionID, Text: "Bu hafta", Position: 0, VoteCount: 2},
			{Id: option2ID, Text: "Gelecek hafta", Position: 1, VoteCount: 1},
		},
		VoterCount: 3, OwnOptionIds: []string{option2ID},
	}
}

func (f *fakeCore) CreatePoll(ctx context.Context, in *corev1.CreatePollRequest, _ ...grpc.CallOption) (*corev1.CreatePollResponse, error) {
	return &corev1.CreatePollResponse{Poll: testPoll()}, f.record(ctx, in)
}
func (f *fakeCore) GetPoll(ctx context.Context, in *corev1.GetPollRequest, _ ...grpc.CallOption) (*corev1.GetPollResponse, error) {
	return &corev1.GetPollResponse{Poll: testPoll()}, f.record(ctx, in)
}
func (f *fakeCore) ListPolls(ctx context.Context, in *corev1.ListPollsRequest, _ ...grpc.CallOption) (*corev1.ListPollsResponse, error) {
	orphan := testPoll()
	orphan.CreatedByUserId, orphan.CloseTime, orphan.OwnOptionIds = "", nil, nil
	return &corev1.ListPollsResponse{Polls: []*corev1.Poll{testPoll(), orphan}, NextPageToken: "SONRAKI"}, f.record(ctx, in)
}
func (f *fakeCore) VotePoll(ctx context.Context, in *corev1.VotePollRequest, _ ...grpc.CallOption) (*corev1.VotePollResponse, error) {
	return &corev1.VotePollResponse{Poll: testPoll()}, f.record(ctx, in)
}
func (f *fakeCore) WithdrawPollVote(ctx context.Context, in *corev1.WithdrawPollVoteRequest, _ ...grpc.CallOption) (*corev1.WithdrawPollVoteResponse, error) {
	return &corev1.WithdrawPollVoteResponse{Poll: testPoll()}, f.record(ctx, in)
}
func (f *fakeCore) ListPollVoters(ctx context.Context, in *corev1.ListPollVotersRequest, _ ...grpc.CallOption) (*corev1.ListPollVotersResponse, error) {
	return &corev1.ListPollVotersResponse{Voters: []*corev1.PollVoter{{UserId: userID, VoteTime: timestamppb.New(t0)}}}, f.record(ctx, in)
}
func (f *fakeCore) ClosePoll(ctx context.Context, in *corev1.ClosePollRequest, _ ...grpc.CallOption) (*corev1.ClosePollResponse, error) {
	p := testPoll()
	p.Closed, p.ClosedTime = true, timestamppb.New(t0)
	return &corev1.ClosePollResponse{Poll: p}, f.record(ctx, in)
}
func (f *fakeCore) DeletePoll(ctx context.Context, in *corev1.DeletePollRequest, _ ...grpc.CallOption) (*corev1.DeletePollResponse, error) {
	return &corev1.DeletePollResponse{}, f.record(ctx, in)
}

func TestPollRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/polls"
	one := base + "/" + pollID
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", base + "?page_size=10&page_token=P&event_id=" + eventID, "", 200, &corev1.ListPollsRequest{TayfaId: tayfaID, EventId: eventID, PageSize: 10, PageToken: "P"}},
		{"POST", base, `{"question":"Kamp?","options":["A","B"],"anonymous":true,"multiple_choice":true,"closes_at":"2030-01-02T03:04:05Z","event_id":"` + eventID + `"}`, 201,
			&corev1.CreatePollRequest{TayfaId: tayfaID, Question: "Kamp?", Options: []string{"A", "B"}, Anonymous: true, MultipleChoice: true,
				CloseTime: &timestamppb.Timestamp{Seconds: 1893553445}, EventId: eventID}},
		{"POST", base, `{"question":"Kamp?","options":["A","B"]}`, 201, &corev1.CreatePollRequest{TayfaId: tayfaID, Question: "Kamp?", Options: []string{"A", "B"}}},
		{"GET", one, "", 200, &corev1.GetPollRequest{TayfaId: tayfaID, PollId: pollID}},
		{"DELETE", one, "", 204, &corev1.DeletePollRequest{TayfaId: tayfaID, PollId: pollID}},
		{"PUT", one + "/vote", `{"option_ids":["` + optionID + `","` + option2ID + `"]}`, 200,
			&corev1.VotePollRequest{TayfaId: tayfaID, PollId: pollID, OptionIds: []string{optionID, option2ID}}},
		{"DELETE", one + "/vote", "", 200, &corev1.WithdrawPollVoteRequest{TayfaId: tayfaID, PollId: pollID}},
		{"GET", one + "/options/" + optionID + "/voters?page_size=5", "", 200, &corev1.ListPollVotersRequest{TayfaId: tayfaID, PollId: pollID, OptionId: optionID, PageSize: 5}},
		{"POST", one + "/closure", "", 200, &corev1.ClosePollRequest{TayfaId: tayfaID, PollId: pollID}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, tok, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) || tok != "USERTOKEN" {
			t.Errorf("%s %s: %d %s; core'a giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}

	w := as(h, "POST", base, `{"question":"Q","options":["A","B"]}`)
	p := decodeBody[pollJSON](t, w)
	if w.Header().Get("Location") != one || p.ID != pollID || !p.Anonymous || p.ClosesAt == nil || p.ClosedAt != nil ||
		len(p.Options) != 2 || p.Options[0].VoteCount != 2 || p.VoterCount != 3 || len(p.OwnOptionIDs) != 1 || p.OwnOptionIDs[0] != option2ID {
		t.Fatalf("oluşturma yanıtı: %v %+v", w.Header(), p)
	}
	w = as(h, "GET", base, "")
	l := decodeBody[pollListJSON](t, w)
	if len(l.Polls) != 2 || l.NextPageToken != "SONRAKI" || l.Polls[1].CreatedByUserID != "" || l.Polls[1].OwnOptionIDs == nil {
		t.Fatalf("liste: %+v", l)
	}
	if strings.Count(w.Body.String(), "created_by_user_id") != 1 || strings.Count(w.Body.String(), `"own_option_ids":[]`) != 1 {
		t.Fatalf("boş alanlar: %s", w.Body)
	}
	w = as(h, "POST", one+"/closure", "")
	if c := decodeBody[pollJSON](t, w); !c.Closed || c.ClosedAt == nil {
		t.Fatalf("kapatma: %+v", c)
	}
	w = as(h, "GET", one+"/options/"+optionID+"/voters", "")
	if v := decodeBody[pollVoterListJSON](t, w); len(v.Voters) != 1 || v.Voters[0].UserID != userID || !v.Voters[0].VotedAt.Equal(t0) {
		t.Fatalf("oy verenler: %+v", v)
	}
}

func TestPollBadRequests(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/polls"
	one := base + "/" + pollID
	eleven := `["1","2","3","4","5","6","7","8","9","10","11"]`
	for name, tc := range map[string]struct{ method, path, body string }{
		"boş soru":          {"POST", base, `{"question":" ","options":["A","B"]}`},
		"uzun soru":         {"POST", base, `{"question":"` + strings.Repeat("ş", 201) + `","options":["A","B"]}`},
		"tek seçenek":       {"POST", base, `{"question":"Q","options":["A"]}`},
		"on bir seçenek":    {"POST", base, `{"question":"Q","options":` + eleven + `}`},
		"uzun seçenek":      {"POST", base, `{"question":"Q","options":["A","` + strings.Repeat("ğ", 101) + `"]}`},
		"boş seçenek":       {"POST", base, `{"question":"Q","options":["A",""]}`},
		"bozuk etkinlik":    {"POST", base, `{"question":"Q","options":["A","B"],"event_id":"x"}`},
		"bozuk kapanma":     {"POST", base, `{"question":"Q","options":["A","B"],"closes_at":"yarın"}`},
		"bilinmeyen alan":   {"POST", base, `{"question":"Q","options":["A","B"],"created_by":"` + userID + `"}`},
		"boş oy":            {"PUT", one + "/vote", `{"option_ids":[]}`},
		"oy yok":            {"PUT", one + "/vote", `{}`},
		"bozuk oy":          {"PUT", one + "/vote", `{"option_ids":["x"]}`},
		"başkası adına oy":  {"PUT", one + "/vote", `{"option_ids":["` + optionID + `"],"user_id":"` + userID + `"}`},
		"bozuk anket id":    {"GET", base + "/kim", ""},
		"bozuk seçenek id":  {"GET", one + "/options/x/voters", ""},
		"bozuk filtre":      {"GET", base + "?event_id=x", ""},
		"bozuk sayfa":       {"GET", base + "?page_size=101", ""},
		"bozuk tayfa id":    {"GET", "/v1/tayfas/yok/polls", ""},
		"bozuk kapatma yol": {"POST", base + "/x/closure", ""},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek core'a gitti")
	}
}

func TestPollErrors(t *testing.T) {
	one := "/v1/tayfas/" + tayfaID + "/polls/" + pollID
	for _, tc := range []struct {
		reason corev1.ErrorReason
		code   codes.Code
		status int
		typ    string
	}{
		{corev1.ErrorReason_ERROR_REASON_POLL_NOT_FOUND, codes.NotFound, 404, "poll-not-found"},
		{corev1.ErrorReason_ERROR_REASON_POLL_CLOSED, codes.FailedPrecondition, 409, "poll-closed"},
		{corev1.ErrorReason_ERROR_REASON_POLL_ANONYMOUS, codes.FailedPrecondition, 403, "poll-anonymous"},
	} {
		f := &fakeCore{err: coreErr(tc.code, tc.reason.String())}
		w := as(coreRouter(f), "GET", one+"/options/"+optionID+"/voters", "")
		if w.Code != tc.status || problemType(t, w) != tc.typ {
			t.Errorf("%s: %d %s", tc.reason, w.Code, problemType(t, w))
		}
	}
}
