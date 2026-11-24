package rest

import (
	"context"
	"net/http"
	"strings"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The vote component (kind "vote", issue #254; ADR-0025 §4, §5; ADR-0037
// §3). core decides everything; the gateway only keeps junk out early. Who
// voted for what never reaches the gateway; option labels travel in bodies
// only and are never logged.

const (
	// maxVoteLabelBytes bounds a free option: 100 characters of up to four
	// bytes, with room for the whitespace core folds.
	maxVoteLabelBytes = (100 + 64) * 4
	// maxVoteBody fits {"label": …} with the longest label, escaped.
	maxVoteBody = 4 << 10
)

// registerVoteComponent is the vote component's line in the registry
// (componentRoutes).
func registerVoteComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "vote"), h.authed(h.getVote))
	mux.HandleFunc(componentPath("PUT", "vote"), h.authed(h.castVote))
	mux.HandleFunc(componentPath("DELETE", "vote"), h.authed(h.withdrawVote))
	mux.HandleFunc(componentPath("POST", "vote/options"), h.authed(h.addVoteOption))
	mux.HandleFunc(componentPath("DELETE", "vote/options/{option_id}"), h.authed(h.removeVoteOption))
	mux.HandleFunc(componentPath("POST", "vote/close"), h.authed(h.closeVote))
}

func (h componentHandlers) votes() corev1.VoteComponentServiceClient {
	return corev1.NewVoteComponentServiceClient(h.conn)
}

type voteVehicleJSON struct {
	Make      string `json:"make"`
	Model     string `json:"model,omitempty"`
	ModelYear int32  `json:"model_year,omitempty"`
	Color     string `json:"color,omitempty"`
}

type voteOptionJSON struct {
	ID        string           `json:"id"`
	Label     string           `json:"label,omitempty"`
	UserID    string           `json:"user_id,omitempty"`
	Vehicle   *voteVehicleJSON `json:"vehicle,omitempty"`
	UploadID  string           `json:"upload_id,omitempty"`
	VoteCount int32            `json:"vote_count"`
	Winner    bool             `json:"winner"`
}

type eventVoteJSON struct {
	Source               string           `json:"source"`
	Options              []voteOptionJSON `json:"options"`
	VoterCount           int32            `json:"voter_count"`
	CallerOptionID       string           `json:"caller_option_id,omitempty"`
	Closed               bool             `json:"closed"`
	ClosedAt             *time.Time       `json:"closed_at,omitempty"`
	ResultsVisible       bool             `json:"results_visible"`
	CallerCanVote        bool             `json:"caller_can_vote"`
	CallerCanEditOptions bool             `json:"caller_can_edit_options"`
	CallerCanClose       bool             `json:"caller_can_close"`
	MaxOptions           int32            `json:"max_options"`
}

func eventVoteFrom(v *corev1.EventVote) eventVoteJSON {
	if v == nil {
		v = &corev1.EventVote{}
	}
	out := eventVoteJSON{
		Source: v.GetSource(), Options: make([]voteOptionJSON, 0, len(v.GetOptions())), VoterCount: v.GetVoterCount(),
		CallerOptionID: v.GetCallerOptionId(), Closed: v.GetClosed(), ClosedAt: optTime(v.GetCloseTime()),
		ResultsVisible: v.GetResultsVisible(), CallerCanVote: v.GetCallerCanVote(),
		CallerCanEditOptions: v.GetCallerCanEditOptions(), CallerCanClose: v.GetCallerCanClose(), MaxOptions: v.GetMaxOptions(),
	}
	for _, o := range v.GetOptions() {
		jo := voteOptionJSON{ID: o.GetId(), Label: o.GetLabel(), UserID: o.GetUserId(), UploadID: o.GetUploadId()}
		if v.GetResultsVisible() {
			jo.VoteCount, jo.Winner = o.GetVoteCount(), o.GetWinner()
		}
		if veh := o.GetVehicle(); veh != nil {
			jo.Vehicle = &voteVehicleJSON{Make: veh.GetMake(), Model: veh.GetModel(), ModelYear: veh.GetModelYear(), Color: veh.GetColor()}
		}
		out.Options = append(out.Options, jo)
	}
	return out
}

func (h componentHandlers) getVote(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.votes().GetVote(ctx, &corev1.GetVoteRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVoteFrom(resp.GetVote()))
}

// castVoteBody is the body of a vote: one option of the vote.
type castVoteBody struct {
	OptionID string `json:"option_id"`
}

func (h componentHandlers) castVote(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b castVoteBody
	if !decodeLimit(w, r, &b, maxVoteBody) {
		return
	}
	if !validUUID(b.OptionID) {
		badRequest(w, r)
		return
	}
	resp, err := h.votes().CastVote(ctx, &corev1.CastVoteRequest{Ref: ref, OptionId: b.OptionID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVoteFrom(resp.GetVote()))
}

func (h componentHandlers) withdrawVote(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.votes().WithdrawVote(ctx, &corev1.WithdrawVoteRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVoteFrom(resp.GetVote()))
}

// voteOptionBody is the body of a new free option.
type voteOptionBody struct {
	Label string `json:"label"`
}

func (h componentHandlers) addVoteOption(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b voteOptionBody
	if !decodeLimit(w, r, &b, maxVoteBody) {
		return
	}
	if strings.TrimSpace(b.Label) == "" || len(b.Label) > maxVoteLabelBytes {
		badRequest(w, r)
		return
	}
	resp, err := h.votes().AddVoteOption(ctx, &corev1.AddVoteOptionRequest{Ref: ref, Label: b.Label})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVoteFrom(resp.GetVote()))
}

func (h componentHandlers) removeVoteOption(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	option, ok := pathUUID(w, r, "option_id")
	if !ok {
		return
	}
	resp, err := h.votes().RemoveVoteOption(ctx, &corev1.RemoveVoteOptionRequest{Ref: ref, OptionId: option})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVoteFrom(resp.GetVote()))
}

func (h componentHandlers) closeVote(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.votes().CloseVote(ctx, &corev1.CloseVoteRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventVoteFrom(resp.GetVote()))
}
