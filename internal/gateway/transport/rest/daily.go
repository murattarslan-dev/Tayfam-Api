package rest

import (
	"context"
	"net/http"
	"strings"
	"time"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// Günün içeriği (issue #269; ADR-0027 §5). core decides everything; the
// gateway only keeps junk out early. Texts are content: they travel in
// bodies only and are never logged.

const (
	dailyPath     = "/v1/tayfas/{tayfa_id}/daily"
	dailyItemPath = dailyPath + "/items/{daily_item_id}"
	// maxDailyItems bounds a reorder request (daily.MaxItems).
	maxDailyItems = 1000
	// maxDailyBodyBytes bounds a text before core folds its whitespace
	// (500 characters).
	maxDailyBodyBytes = 4 << 10
	// maxDailyOrderBytes bounds a reorder body: 1000 UUIDs.
	maxDailyOrderBytes = 64 << 10
)

// registerTayfaDaily is the daily content's line in the registry
// (componentRoutes). Its routes are the tayfa's, not an event's.
func registerTayfaDaily(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc("GET "+dailyPath+"/today", h.authed(h.getDailyToday))
	mux.HandleFunc("GET "+dailyPath+"/items", h.authed(h.listDailyItems))
	mux.HandleFunc("POST "+dailyPath+"/items", h.authed(h.createDailyItem))
	mux.HandleFunc("PUT "+dailyItemPath, h.authed(h.updateDailyItem))
	mux.HandleFunc("DELETE "+dailyItemPath, h.authed(h.deleteDailyItem))
	mux.HandleFunc("POST "+dailyItemPath+"/restore", h.authed(h.restoreDailyItem))
	mux.HandleFunc("PUT "+dailyPath+"/order", h.authed(h.reorderDailyItems))
}

func (h componentHandlers) daily() corev1.TayfaDailyServiceClient {
	return corev1.NewTayfaDailyServiceClient(h.conn)
}

type dailyPinJSON struct {
	Month int32 `json:"month"`
	Day   int32 `json:"day"`
	// Absent: every year.
	Year *int32 `json:"year,omitempty"`
}

type dailyItemJSON struct {
	ID          string        `json:"id"`
	Body        string        `json:"body"`
	Attribution string        `json:"attribution,omitempty"`
	Position    int32         `json:"position"`
	Pin         *dailyPinJSON `json:"pin,omitempty"`
	// Absent once the account is deleted.
	CreatedByUserID string     `json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
}

type dailyTodayJSON struct {
	// Absent when the pool has nothing to show.
	Item   *dailyItemJSON `json:"item,omitempty"`
	Pinned bool           `json:"pinned"`
	Date   string         `json:"date"`
}

type dailyListJSON struct {
	Items           []dailyItemJSON `json:"items"`
	CallerCanManage bool            `json:"caller_can_manage"`
}

type dailySavedJSON struct {
	Item        dailyItemJSON `json:"item"`
	PinConflict bool          `json:"pin_conflict"`
}

type dailyOrderJSON struct {
	Items []dailyItemJSON `json:"items"`
}

func dailyItemFrom(it *corev1.DailyItem) dailyItemJSON {
	out := dailyItemJSON{
		ID: it.GetId(), Body: it.GetBody(), Attribution: it.GetAttribution(), Position: it.GetPosition(),
		CreatedByUserID: it.GetCreatedBy(), CreatedAt: it.GetCreateTime().AsTime(), UpdatedAt: it.GetUpdateTime().AsTime(),
	}
	if p := it.GetPin(); p != nil {
		out.Pin = &dailyPinJSON{Month: p.GetMonth(), Day: p.GetDay()}
		if p.Year != nil {
			y := p.GetYear()
			out.Pin.Year = &y
		}
	}
	if it.GetDeleteTime() != nil {
		at := it.GetDeleteTime().AsTime()
		out.DeletedAt = &at
	}
	return out
}

func dailyItemsFrom(items []*corev1.DailyItem) []dailyItemJSON {
	out := make([]dailyItemJSON, 0, len(items))
	for _, it := range items {
		out = append(out, dailyItemFrom(it))
	}
	return out
}

// dailyInputBody is an item's text and pin; the create body adds its
// client request ID.
type dailyInputBody struct {
	Body        string        `json:"body"`
	Attribution string        `json:"attribution"`
	Pin         *dailyPinJSON `json:"pin"`
}

type dailyCreateBody struct {
	dailyInputBody
	ClientRequestID string `json:"client_request_id"`
}

// input checks what the gateway can and converts; core checks the rest.
func (b dailyInputBody) input() (*corev1.DailyItemInput, bool) {
	if strings.TrimSpace(b.Body) == "" || len(b.Body) > maxDailyBodyBytes || len(b.Attribution) > maxDailyBodyBytes {
		return nil, false
	}
	out := &corev1.DailyItemInput{Body: b.Body, Attribution: b.Attribution}
	if b.Pin != nil {
		out.Pin = &corev1.DailyPin{Month: b.Pin.Month, Day: b.Pin.Day, Year: b.Pin.Year}
	}
	return out, true
}

func (h componentHandlers) getDailyToday(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.daily().GetDailyToday(ctx, &corev1.GetDailyTodayRequest{TayfaId: tf})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := dailyTodayJSON{Pinned: resp.GetPinned(), Date: resp.GetDate()}
	if resp.GetItem() != nil {
		it := dailyItemFrom(resp.GetItem())
		out.Item = &it
	}
	writeJSON(w, http.StatusOK, out)
}

func (h componentHandlers) listDailyItems(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var deleted bool
	switch r.URL.Query().Get("deleted") {
	case "", "false":
	case "true":
		deleted = true
	default:
		badRequest(w, r)
		return
	}
	resp, err := h.daily().ListDailyItems(ctx, &corev1.ListDailyItemsRequest{TayfaId: tf, Deleted: deleted})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dailyListJSON{Items: dailyItemsFrom(resp.GetItems()), CallerCanManage: resp.GetCallerCanManage()})
}

func (h componentHandlers) createDailyItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b dailyCreateBody
	if !decode(w, r, &b) {
		return
	}
	in, ok := b.input()
	if !ok || !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.daily().CreateDailyItem(ctx, &corev1.CreateDailyItemRequest{TayfaId: tf, Item: in, ClientRequestId: b.ClientRequestID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, dailySavedJSON{Item: dailyItemFrom(resp.GetItem()), PinConflict: resp.GetPinConflict()})
}

// pathDailyItem reads and checks the path's tayfa and item.
func pathDailyItem(w http.ResponseWriter, r *http.Request) (tayfaID, itemID string, ok bool) {
	if tayfaID, ok = pathUUID(w, r, "tayfa_id"); !ok {
		return "", "", false
	}
	if itemID, ok = pathUUID(w, r, "daily_item_id"); !ok {
		return "", "", false
	}
	return tayfaID, itemID, true
}

func (h componentHandlers) updateDailyItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, item, ok := pathDailyItem(w, r)
	if !ok {
		return
	}
	var b dailyInputBody
	if !decode(w, r, &b) {
		return
	}
	in, ok := b.input()
	if !ok {
		badRequest(w, r)
		return
	}
	resp, err := h.daily().UpdateDailyItem(ctx, &corev1.UpdateDailyItemRequest{TayfaId: tf, ItemId: item, Item: in})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dailySavedJSON{Item: dailyItemFrom(resp.GetItem()), PinConflict: resp.GetPinConflict()})
}

func (h componentHandlers) deleteDailyItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, item, ok := pathDailyItem(w, r)
	if !ok {
		return
	}
	if _, err := h.daily().DeleteDailyItem(ctx, &corev1.DeleteDailyItemRequest{TayfaId: tf, ItemId: item}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h componentHandlers) restoreDailyItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, item, ok := pathDailyItem(w, r)
	if !ok {
		return
	}
	resp, err := h.daily().RestoreDailyItem(ctx, &corev1.RestoreDailyItemRequest{TayfaId: tf, ItemId: item})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dailySavedJSON{Item: dailyItemFrom(resp.GetItem()), PinConflict: resp.GetPinConflict()})
}

type dailyOrderBody struct {
	ItemIDs []string `json:"item_ids"`
}

func (h componentHandlers) reorderDailyItems(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b dailyOrderBody
	if !decodeLimit(w, r, &b, maxDailyOrderBytes) {
		return
	}
	if b.ItemIDs == nil || len(b.ItemIDs) > maxDailyItems {
		badRequest(w, r)
		return
	}
	for _, v := range b.ItemIDs {
		if !validUUID(v) {
			badRequest(w, r)
			return
		}
	}
	resp, err := h.daily().ReorderDailyItems(ctx, &corev1.ReorderDailyItemsRequest{TayfaId: tf, ItemIds: b.ItemIDs})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dailyOrderJSON{Items: dailyItemsFrom(resp.GetItems())})
}
