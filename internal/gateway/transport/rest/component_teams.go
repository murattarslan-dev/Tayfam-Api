package rest

import (
	"context"
	"net/http"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The teams component (kind "teams", issue #248; ADR-0025 §4). core decides
// everything; the gateway only keeps junk out early. Levels travel in the
// body only and are never logged.

// Limits of core's teams package.
const (
	teamsNameMax   = 40
	teamsMax       = 50
	teamsPeopleMax = 500
	teamsLevelMin  = 1
	teamsLevelMax  = 5
	// teamsBodyMax holds 500 user IDs with their levels or team places.
	teamsBodyMax = 64 << 10
)

// registerTeamsComponent is the teams component's line in the registry
// (componentRoutes).
func registerTeamsComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "teams"), h.authed(h.getTeams))
	mux.HandleFunc(componentPath("PUT", "teams"), h.authed(h.replaceTeams))
	mux.HandleFunc(componentPath("POST", "teams/generate"), h.authed(h.generateTeams))
}

func (h componentHandlers) teamsClient() corev1.TeamsComponentServiceClient {
	return corev1.NewTeamsComponentServiceClient(h.conn)
}

type teamJSON struct {
	ID        string   `json:"id"`
	Position  int32    `json:"position"`
	Name      string   `json:"name,omitempty"`
	MemberIDs []string `json:"member_ids"`
}

type teamsJSON struct {
	Teams         []teamJSON `json:"teams"`
	Mode          string     `json:"mode"`
	TeamCount     int32      `json:"team_count,omitempty"`
	TeamSize      int32      `json:"team_size,omitempty"`
	CallerCanEdit bool       `json:"caller_can_edit"`
}

func teamsFrom(t *corev1.Teams) teamsJSON {
	out := teamsJSON{
		Teams: make([]teamJSON, 0, len(t.GetTeams())), Mode: t.GetMode(),
		TeamCount: t.GetTeamCount(), TeamSize: t.GetTeamSize(), CallerCanEdit: t.GetCallerCanEdit(),
	}
	for _, tm := range t.GetTeams() {
		members := tm.GetMemberIds()
		if members == nil {
			members = []string{}
		}
		out.Teams = append(out.Teams, teamJSON{ID: tm.GetId(), Position: tm.GetPosition(), Name: tm.GetName(), MemberIDs: members})
	}
	return out
}

type teamLevelBody struct {
	UserID string `json:"user_id"`
	Level  int32  `json:"level"`
}

type teamsGenerateBody struct {
	Levels []teamLevelBody `json:"levels"`
}

type teamBody struct {
	Name      *string  `json:"name"`
	MemberIDs []string `json:"member_ids"`
}

type teamsReplaceBody struct {
	// A pointer so that a body without it is refused; [] clears the teams.
	Teams *[]teamBody `json:"teams"`
}

func (h componentHandlers) getTeams(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.teamsClient().GetTeams(ctx, &corev1.GetTeamsRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, teamsFrom(resp.GetTeams()))
}

// teamsLevels reads and checks a generate body; an empty body (no levels)
// is allowed. On a bad one it answers invalid-request and ok is false.
func teamsLevels(w http.ResponseWriter, r *http.Request) (levels []*corev1.TeamLevel, ok bool) {
	// A pointer so that a null body is refused; {} has no levels.
	var b *teamsGenerateBody
	if !decodeLimit(w, r, &b, teamsBodyMax) {
		return nil, false
	}
	if b == nil || len(b.Levels) > teamsPeopleMax {
		badRequest(w, r)
		return nil, false
	}
	seen := make(map[string]bool, len(b.Levels))
	for _, l := range b.Levels {
		if !validUUID(l.UserID) || seen[l.UserID] || l.Level < teamsLevelMin || l.Level > teamsLevelMax {
			badRequest(w, r)
			return nil, false
		}
		seen[l.UserID] = true
		levels = append(levels, &corev1.TeamLevel{UserId: l.UserID, Level: l.Level})
	}
	return levels, true
}

func (h componentHandlers) generateTeams(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	levels, ok := teamsLevels(w, r)
	if !ok {
		return
	}
	resp, err := h.teamsClient().GenerateTeams(ctx, &corev1.GenerateTeamsRequest{Ref: ref, Levels: levels})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, teamsFrom(resp.GetTeams()))
}

// teamsInput reads and checks a replace body; on a bad one it answers
// invalid-request and ok is false.
func teamsInput(w http.ResponseWriter, r *http.Request) (in []*corev1.TeamInput, ok bool) {
	var b teamsReplaceBody
	if !decodeLimit(w, r, &b, teamsBodyMax) {
		return nil, false
	}
	if b.Teams == nil || len(*b.Teams) > teamsMax {
		badRequest(w, r)
		return nil, false
	}
	seen := map[string]bool{}
	in = make([]*corev1.TeamInput, 0, len(*b.Teams))
	for _, t := range *b.Teams {
		name := ""
		if t.Name != nil {
			name = *t.Name
		}
		if runes(name) > teamsNameMax {
			badRequest(w, r)
			return nil, false
		}
		for _, m := range t.MemberIDs {
			if !validUUID(m) || seen[m] {
				badRequest(w, r)
				return nil, false
			}
			seen[m] = true
		}
		if len(seen) > teamsPeopleMax {
			badRequest(w, r)
			return nil, false
		}
		in = append(in, &corev1.TeamInput{Name: name, MemberIds: t.MemberIDs})
	}
	return in, true
}

func (h componentHandlers) replaceTeams(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	in, ok := teamsInput(w, r)
	if !ok {
		return
	}
	resp, err := h.teamsClient().ReplaceTeams(ctx, &corev1.ReplaceTeamsRequest{Ref: ref, Teams: in})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, teamsFrom(resp.GetTeams()))
}
