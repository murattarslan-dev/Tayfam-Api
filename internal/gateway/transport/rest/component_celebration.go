package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The celebration component (kind "celebration", issue #255; ADR-0025 §4,
// §5). core decides everything; the gateway only keeps junk out early.

const (
	// maxCelebrationBody fits {"winner_ids": […]} with ten IDs.
	maxCelebrationBody = 1 << 10
	// celebrationMaxWinners is celebration.MaxWinners in core.
	celebrationMaxWinners = 10
)

// registerCelebrationComponent is the celebration component's line in the
// registry (componentRoutes).
func registerCelebrationComponent(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "celebration"), h.authed(h.getCelebration))
	mux.HandleFunc(componentPath("PUT", "celebration"), h.authed(h.announceCelebration))
}

func (h componentHandlers) celebrations() corev1.CelebrationComponentServiceClient {
	return corev1.NewCelebrationComponentServiceClient(h.conn)
}

type celebrationWinnerJSON struct {
	UserID  string `json:"user_id,omitempty"`
	TeamID  string `json:"team_id,omitempty"`
	GuestID string `json:"guest_id,omitempty"`
	TableID string `json:"table_id,omitempty"`
}

type eventCelebrationJSON struct {
	Source            string                  `json:"source,omitempty"`
	SourceKind        string                  `json:"source_kind,omitempty"`
	Winners           []celebrationWinnerJSON `json:"winners"`
	Hidden            bool                    `json:"hidden"`
	Announced         bool                    `json:"announced"`
	AnnouncedAt       *time.Time              `json:"announced_at,omitempty"`
	CallerCanAnnounce bool                    `json:"caller_can_announce"`
}

func eventCelebrationFrom(c *corev1.EventCelebration) eventCelebrationJSON {
	if c == nil {
		c = &corev1.EventCelebration{}
	}
	out := eventCelebrationJSON{
		Source: c.GetSource(), SourceKind: c.GetSourceKind(), Hidden: c.GetHidden(), Announced: c.GetAnnounced(),
		AnnouncedAt: optTime(c.GetAnnounceTime()), CallerCanAnnounce: c.GetCallerCanAnnounce(),
		Winners: make([]celebrationWinnerJSON, 0, len(c.GetWinners())),
	}
	for _, w := range c.GetWinners() {
		out.Winners = append(out.Winners, celebrationWinnerJSON{
			UserID: w.GetUserId(), TeamID: w.GetTeamId(), GuestID: w.GetGuestId(), TableID: w.GetTableId(),
		})
	}
	return out
}

// celebrationBody is the body of an announcement: the winners chosen by
// hand, only for a celebration without a source.
type celebrationBody struct {
	WinnerIDs []string `json:"winner_ids"`
}

func (h componentHandlers) getCelebration(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.celebrations().GetCelebration(ctx, &corev1.GetCelebrationRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventCelebrationFrom(resp.GetCelebration()))
}

func (h componentHandlers) announceCelebration(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b celebrationBody
	if !decodeLimit(w, r, &b, maxCelebrationBody) {
		return
	}
	if !validUUIDs(b.WinnerIDs, celebrationMaxWinners) {
		badRequest(w, r)
		return
	}
	resp, err := h.celebrations().AnnounceCelebration(ctx, &corev1.AnnounceCelebrationRequest{Ref: ref, WinnerIds: b.WinnerIDs})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventCelebrationFrom(resp.GetCelebration()))
}
