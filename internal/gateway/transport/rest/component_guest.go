package rest

import (
	"context"
	"net/http"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// Guest players (issue #252; ADR-0037 §6), under the scoreboard component
// that takes them. core decides everything; the gateway only keeps junk out
// early. Nicknames travel in bodies only and are never logged.

const (
	// guestNicknameMax is guest.NicknameMax in core.
	guestNicknameMax = 24
	guestBodyMax     = 1 << 10
)

// registerGuestPlayers is the guest players' line in the registry
// (componentRoutes).
func registerGuestPlayers(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc(componentPath("GET", "guests"), h.authed(h.listGuestPlayers))
	mux.HandleFunc(componentPath("POST", "guests"), h.authed(h.addGuestPlayer))
	mux.HandleFunc(componentPath("DELETE", "guests/{guest_id}"), h.authed(h.removeGuestPlayer))
}

func (h componentHandlers) guestPlayers() corev1.GuestPlayerComponentServiceClient {
	return corev1.NewGuestPlayerComponentServiceClient(h.conn)
}

type guestPlayerJSON struct {
	ID string `json:"id"`
	// Nickname is left out once cleared.
	Nickname  string    `json:"nickname,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type guestPlayersJSON struct {
	Guests          []guestPlayerJSON `json:"guests"`
	MaxGuests       int32             `json:"max_guests"`
	CallerCanAdd    bool              `json:"caller_can_add"`
	CallerCanRemove bool              `json:"caller_can_remove"`
}

func guestPlayersFrom(g *corev1.GuestPlayers) guestPlayersJSON {
	out := guestPlayersJSON{
		Guests: make([]guestPlayerJSON, 0, len(g.GetGuests())), MaxGuests: g.GetMaxGuests(),
		CallerCanAdd: g.GetCallerCanAdd(), CallerCanRemove: g.GetCallerCanRemove(),
	}
	for _, v := range g.GetGuests() {
		out.Guests = append(out.Guests, guestPlayerJSON{ID: v.GetId(), Nickname: v.GetNickname(), CreatedAt: v.GetCreateTime().AsTime().UTC()})
	}
	return out
}

type guestPlayerBody struct {
	Nickname string `json:"nickname"`
	TableID  string `json:"table_id"`
}

func (h componentHandlers) listGuestPlayers(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	resp, err := h.guestPlayers().ListGuestPlayers(ctx, &corev1.ListGuestPlayersRequest{Ref: ref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, guestPlayersFrom(resp.GetGuests()))
}

func (h componentHandlers) addGuestPlayer(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	var b guestPlayerBody
	if !decodeLimit(w, r, &b, guestBodyMax) {
		return
	}
	if n := runes(b.Nickname); n < 1 || n > guestNicknameMax || (b.TableID != "" && !validUUID(b.TableID)) {
		badRequest(w, r)
		return
	}
	resp, err := h.guestPlayers().AddGuestPlayer(ctx, &corev1.AddGuestPlayerRequest{Ref: ref, Nickname: b.Nickname, TableId: b.TableID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, guestPlayersFrom(resp.GetGuests()))
}

func (h componentHandlers) removeGuestPlayer(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ref, ok := componentRef(w, r)
	if !ok {
		return
	}
	g, ok := pathUUID(w, r, "guest_id")
	if !ok {
		return
	}
	resp, err := h.guestPlayers().RemoveGuestPlayer(ctx, &corev1.RemoveGuestPlayerRequest{Ref: ref, GuestId: g})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, guestPlayersFrom(resp.GetGuests()))
}
