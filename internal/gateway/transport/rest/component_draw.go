package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The draw component (kind "draw", issue #249; ADR-0025 §4). core decides
// everything and draws with crypto/rand; the gateway only passes the
// request on.

// registerDrawComponent is the draw component's line in the registry
// (componentRoutes).
func registerDrawComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "draw"), h.authed(h.getDraw))
	mux.HandleFunc(componentPath("POST", "draw"), h.authed(h.makeDraw))
}

func (h componentHandlers) drawClient() corev1.DrawComponentServiceClient {
	return corev1.NewDrawComponentServiceClient(h.conn)
}

type drawEntryJSON struct {
	Position int32  `json:"position"`
	UserID   string `json:"user_id,omitempty"`
	TeamID   string `json:"team_id,omitempty"`
}

type drawJSON struct {
	Drawn         bool            `json:"drawn"`
	Subject       string          `json:"subject"`
	TeamsKey      string          `json:"teams_key,omitempty"`
	DrawnAt       *time.Time      `json:"drawn_at,omitempty"`
	Entries       []drawEntryJSON `json:"entries"`
	CallerCanDraw bool            `json:"caller_can_draw"`
}

func drawFrom(d *corev1.Draw) drawJSON {
	out := drawJSON{
		Drawn: d.GetDrawn(), Subject: d.GetSubject(), TeamsKey: d.GetTeamsKey(), DrawnAt: optTime(d.GetDrawnTime()),
		Entries: make([]drawEntryJSON, 0, len(d.GetEntries())), CallerCanDraw: d.GetCallerCanDraw(),
	}
	for _, e := range d.GetEntries() {
		out.Entries = append(out.Entries, drawEntryJSON{Position: e.GetPosition(), UserID: e.GetUserId(), TeamID: e.GetTeamId()})
	}
	return out
}

func (h componentHandlers) getDraw(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.drawClient().GetDraw(ctx, &corev1.GetDrawRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, drawFrom(resp.GetDraw()))
}

// makeDraw takes no body.
func (h componentHandlers) makeDraw(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.drawClient().MakeDraw(ctx, &corev1.MakeDrawRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, drawFrom(resp.GetDraw()))
}
