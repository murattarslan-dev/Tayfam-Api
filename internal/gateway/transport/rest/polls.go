package rest

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// PollClient is core's PollService as the gateway uses it.
type PollClient interface {
	CreatePoll(ctx context.Context, in *corev1.CreatePollRequest, opts ...grpc.CallOption) (*corev1.CreatePollResponse, error)
	GetPoll(ctx context.Context, in *corev1.GetPollRequest, opts ...grpc.CallOption) (*corev1.GetPollResponse, error)
	ListPolls(ctx context.Context, in *corev1.ListPollsRequest, opts ...grpc.CallOption) (*corev1.ListPollsResponse, error)
	VotePoll(ctx context.Context, in *corev1.VotePollRequest, opts ...grpc.CallOption) (*corev1.VotePollResponse, error)
	WithdrawPollVote(ctx context.Context, in *corev1.WithdrawPollVoteRequest, opts ...grpc.CallOption) (*corev1.WithdrawPollVoteResponse, error)
	ListPollVoters(ctx context.Context, in *corev1.ListPollVotersRequest, opts ...grpc.CallOption) (*corev1.ListPollVotersResponse, error)
	ClosePoll(ctx context.Context, in *corev1.ClosePollRequest, opts ...grpc.CallOption) (*corev1.ClosePollResponse, error)
	DeletePoll(ctx context.Context, in *corev1.DeletePollRequest, opts ...grpc.CallOption) (*corev1.DeletePollResponse, error)
}

const (
	// maxPollBody fits 200 characters of question and ten options of 100
	// in the worst JSON escaping (\uXXXX surrogate pairs).
	maxPollBody     = 64 << 10
	pollQuestionMax = 200
	pollOptionMax   = 100
	pollOptionsMin  = 2
	pollOptionsMax  = 10
)

// registerPolls serves the poll routes. Option IDs of a vote travel in the
// body, never in the path, so access logs (which record the route pattern
// only) could not hold them either.
func registerPolls(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	const one = "/v1/tayfas/{tayfa_id}/polls/{poll_id}"
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/polls", a(h.listPolls))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/polls", a(h.createPoll))
	mux.HandleFunc("GET "+one, a(h.getPoll))
	mux.HandleFunc("DELETE "+one, a(h.deletePoll))
	mux.HandleFunc("PUT "+one+"/vote", a(h.votePoll))
	mux.HandleFunc("DELETE "+one+"/vote", a(h.withdrawPollVote))
	mux.HandleFunc("GET "+one+"/options/{option_id}/voters", a(h.listPollVoters))
	mux.HandleFunc("POST "+one+"/closure", a(h.closePoll))
}

type pollOptionJSON struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Position  int32  `json:"position"`
	VoteCount int64  `json:"vote_count"`
}

type pollJSON struct {
	ID      string `json:"id"`
	TayfaID string `json:"tayfa_id"`
	// Absent when the poll is not tied to an event.
	EventID string `json:"event_id,omitempty"`
	// Absent when the creator's account was deleted.
	CreatedByUserID string           `json:"created_by_user_id,omitempty"`
	Question        string           `json:"question"`
	Anonymous       bool             `json:"anonymous"`
	MultipleChoice  bool             `json:"multiple_choice"`
	ClosesAt        *time.Time       `json:"closes_at,omitempty"`
	ClosedAt        *time.Time       `json:"closed_at,omitempty"`
	Closed          bool             `json:"closed"`
	CreatedAt       time.Time        `json:"created_at"`
	Options         []pollOptionJSON `json:"options"`
	VoterCount      int64            `json:"voter_count"`
	// The caller's own vote only.
	OwnOptionIDs []string `json:"own_option_ids"`
}

func pollFrom(p *corev1.Poll) pollJSON {
	out := pollJSON{
		ID: p.GetId(), TayfaID: p.GetTayfaId(), EventID: p.GetEventId(), CreatedByUserID: p.GetCreatedByUserId(),
		Question: p.GetQuestion(), Anonymous: p.GetAnonymous(), MultipleChoice: p.GetMultipleChoice(),
		ClosesAt: optTime(p.GetCloseTime()), ClosedAt: optTime(p.GetClosedTime()), Closed: p.GetClosed(),
		CreatedAt: p.GetCreateTime().AsTime(), VoterCount: p.GetVoterCount(),
		Options:      make([]pollOptionJSON, 0, len(p.GetOptions())),
		OwnOptionIDs: append([]string{}, p.GetOwnOptionIds()...),
	}
	for _, o := range p.GetOptions() {
		out.Options = append(out.Options, pollOptionJSON{ID: o.GetId(), Text: o.GetText(), Position: o.GetPosition(), VoteCount: o.GetVoteCount()})
	}
	return out
}

type pollListJSON struct {
	Polls         []pollJSON `json:"polls"`
	NextPageToken string     `json:"next_page_token,omitempty"`
}

type pollVoterJSON struct {
	UserID  string    `json:"user_id"`
	VotedAt time.Time `json:"voted_at"`
}

type pollVoterListJSON struct {
	Voters        []pollVoterJSON `json:"voters"`
	NextPageToken string          `json:"next_page_token,omitempty"`
}

func pathTayfaPoll(w http.ResponseWriter, r *http.Request) (tayfaID, pollID string, ok bool) {
	ids, ok := pathIDs(w, r, "tayfa_id", "poll_id")
	if !ok {
		return "", "", false
	}
	return ids[0], ids[1], true
}

// lineOK mirrors core's length rule (after trimming); core checks the rest.
func lineOK(s string, limit int) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= 1 && n <= limit
}

func (h coreHandlers) listPolls(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	event := r.URL.Query().Get("event_id")
	if event != "" && !validUUID(event) {
		badRequest(w, r)
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Polls.ListPolls(ctx, &corev1.ListPollsRequest{TayfaId: id, EventId: event, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := pollListJSON{Polls: make([]pollJSON, 0, len(resp.GetPolls())), NextPageToken: resp.GetNextPageToken()}
	for _, p := range resp.GetPolls() {
		out.Polls = append(out.Polls, pollFrom(p))
	}
	writeJSON(w, http.StatusOK, out)
}

type pollCreateBody struct {
	Question       string     `json:"question"`
	Options        []string   `json:"options"`
	Anonymous      bool       `json:"anonymous"`
	MultipleChoice bool       `json:"multiple_choice"`
	ClosesAt       *time.Time `json:"closes_at"`
	EventID        string     `json:"event_id"`
}

func (h coreHandlers) createPoll(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b pollCreateBody
	if !decodeLimit(w, r, &b, maxPollBody) {
		return
	}
	if !lineOK(b.Question, pollQuestionMax) || len(b.Options) < pollOptionsMin || len(b.Options) > pollOptionsMax ||
		(b.EventID != "" && !validUUID(b.EventID)) {
		badRequest(w, r)
		return
	}
	for _, o := range b.Options {
		if !lineOK(o, pollOptionMax) {
			badRequest(w, r)
			return
		}
	}
	req := &corev1.CreatePollRequest{
		TayfaId: id, Question: b.Question, Options: b.Options, Anonymous: b.Anonymous,
		MultipleChoice: b.MultipleChoice, EventId: b.EventID,
	}
	if b.ClosesAt != nil {
		req.CloseTime = timestamppb.New(*b.ClosesAt)
	}
	resp, err := h.d.Polls.CreatePoll(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p := pollFrom(resp.GetPoll())
	w.Header().Set("Location", "/v1/tayfas/"+p.TayfaID+"/polls/"+p.ID)
	writeJSON(w, http.StatusCreated, p)
}

func (h coreHandlers) getPoll(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, pid, ok := pathTayfaPoll(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Polls.GetPoll(ctx, &corev1.GetPollRequest{TayfaId: id, PollId: pid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pollFrom(resp.GetPoll()))
}

func (h coreHandlers) deletePoll(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, pid, ok := pathTayfaPoll(w, r)
	if !ok {
		return
	}
	if _, err := h.d.Polls.DeletePoll(ctx, &corev1.DeletePollRequest{TayfaId: id, PollId: pid}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type pollVoteBody struct {
	OptionIDs []string `json:"option_ids"`
}

func (h coreHandlers) votePoll(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, pid, ok := pathTayfaPoll(w, r)
	if !ok {
		return
	}
	var b pollVoteBody
	if !decodeLimit(w, r, &b, maxPollBody) {
		return
	}
	if len(b.OptionIDs) < 1 || len(b.OptionIDs) > pollOptionsMax {
		badRequest(w, r)
		return
	}
	for _, o := range b.OptionIDs {
		if !validUUID(o) {
			badRequest(w, r)
			return
		}
	}
	resp, err := h.d.Polls.VotePoll(ctx, &corev1.VotePollRequest{TayfaId: id, PollId: pid, OptionIds: b.OptionIDs})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pollFrom(resp.GetPoll()))
}

func (h coreHandlers) withdrawPollVote(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, pid, ok := pathTayfaPoll(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Polls.WithdrawPollVote(ctx, &corev1.WithdrawPollVoteRequest{TayfaId: id, PollId: pid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pollFrom(resp.GetPoll()))
}

func (h coreHandlers) listPollVoters(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tayfa_id", "poll_id", "option_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Polls.ListPollVoters(ctx, &corev1.ListPollVotersRequest{
		TayfaId: ids[0], PollId: ids[1], OptionId: ids[2], PageSize: size, PageToken: token,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := pollVoterListJSON{Voters: make([]pollVoterJSON, 0, len(resp.GetVoters())), NextPageToken: resp.GetNextPageToken()}
	for _, v := range resp.GetVoters() {
		out.Voters = append(out.Voters, pollVoterJSON{UserID: v.GetUserId(), VotedAt: v.GetVoteTime().AsTime()})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h coreHandlers) closePoll(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, pid, ok := pathTayfaPoll(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Polls.ClosePoll(ctx, &corev1.ClosePollRequest{TayfaId: id, PollId: pid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pollFrom(resp.GetPoll()))
}
