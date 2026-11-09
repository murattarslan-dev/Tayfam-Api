package rest

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// AnnouncementClient is core's AnnouncementService as the gateway uses it.
type AnnouncementClient interface {
	CreateAnnouncement(ctx context.Context, in *corev1.CreateAnnouncementRequest, opts ...grpc.CallOption) (*corev1.CreateAnnouncementResponse, error)
	GetAnnouncement(ctx context.Context, in *corev1.GetAnnouncementRequest, opts ...grpc.CallOption) (*corev1.GetAnnouncementResponse, error)
	ListAnnouncements(ctx context.Context, in *corev1.ListAnnouncementsRequest, opts ...grpc.CallOption) (*corev1.ListAnnouncementsResponse, error)
	UpdateAnnouncement(ctx context.Context, in *corev1.UpdateAnnouncementRequest, opts ...grpc.CallOption) (*corev1.UpdateAnnouncementResponse, error)
	SetAnnouncementPinned(ctx context.Context, in *corev1.SetAnnouncementPinnedRequest, opts ...grpc.CallOption) (*corev1.SetAnnouncementPinnedResponse, error)
	DeleteAnnouncement(ctx context.Context, in *corev1.DeleteAnnouncementRequest, opts ...grpc.CallOption) (*corev1.DeleteAnnouncementResponse, error)
	RestoreAnnouncement(ctx context.Context, in *corev1.RestoreAnnouncementRequest, opts ...grpc.CallOption) (*corev1.RestoreAnnouncementResponse, error)
}

const (
	// maxAnnouncementBody fits 5000 characters of body and 120 of title in
	// the worst JSON escaping (\uXXXX surrogate pairs).
	maxAnnouncementBody  = 128 << 10
	announcementTitleMax = 120
	announcementBodyMax  = 5000
)

func registerAnnouncements(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/announcements", a(h.listAnnouncements))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/announcements", a(h.createAnnouncement))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/announcements/{announcement_id}", a(h.getAnnouncement))
	mux.HandleFunc("PATCH /v1/tayfas/{tayfa_id}/announcements/{announcement_id}", a(h.updateAnnouncement))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/announcements/{announcement_id}", a(h.deleteAnnouncement))
	mux.HandleFunc("PUT /v1/tayfas/{tayfa_id}/announcements/{announcement_id}/pin", a(h.pinAnnouncement(true)))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/announcements/{announcement_id}/pin", a(h.pinAnnouncement(false)))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/announcements/{announcement_id}/restoration", a(h.restoreAnnouncement))
}

type announcementJSON struct {
	ID      string `json:"id"`
	TayfaID string `json:"tayfa_id"`
	// Absent when the author's account was deleted.
	AuthorUserID string    `json:"author_user_id,omitempty"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	Pinned       bool      `json:"pinned"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func announcementFrom(a *corev1.Announcement) announcementJSON {
	return announcementJSON{
		ID: a.GetId(), TayfaID: a.GetTayfaId(), AuthorUserID: a.GetAuthorUserId(),
		Title: a.GetTitle(), Body: a.GetBody(), Pinned: a.GetPinned(),
		CreatedAt: a.GetCreateTime().AsTime(), UpdatedAt: a.GetUpdateTime().AsTime(),
	}
}

type announcementListJSON struct {
	Announcements []announcementJSON `json:"announcements"`
	NextPageToken string             `json:"next_page_token,omitempty"`
}

// pathTayfaAnnouncement reads tayfa_id and announcement_id from the path.
func pathTayfaAnnouncement(w http.ResponseWriter, r *http.Request) (tayfaID, announcementID string, ok bool) {
	if tayfaID, ok = pathUUID(w, r, "tayfa_id"); !ok {
		return "", "", false
	}
	if announcementID, ok = pathUUID(w, r, "announcement_id"); !ok {
		return "", "", false
	}
	return tayfaID, announcementID, true
}

// The length checks mirror core's rules (after trimming); core checks the
// rest again.
func announcementTitleOK(s string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= 1 && n <= announcementTitleMax
}

func announcementBodyOK(s string) bool {
	return utf8.RuneCountInString(strings.TrimSpace(s)) <= announcementBodyMax
}

func (h coreHandlers) listAnnouncements(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Announcements.ListAnnouncements(ctx, &corev1.ListAnnouncementsRequest{TayfaId: id, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := announcementListJSON{Announcements: make([]announcementJSON, 0, len(resp.GetAnnouncements())), NextPageToken: resp.GetNextPageToken()}
	for _, a := range resp.GetAnnouncements() {
		out.Announcements = append(out.Announcements, announcementFrom(a))
	}
	writeJSON(w, http.StatusOK, out)
}

type announcementCreateBody struct {
	Title           string `json:"title"`
	Body            string `json:"body"`
	ClientRequestID string `json:"client_request_id"`
}

func (h coreHandlers) createAnnouncement(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b announcementCreateBody
	if !decodeLimit(w, r, &b, maxAnnouncementBody) {
		return
	}
	if !announcementTitleOK(b.Title) || !announcementBodyOK(b.Body) || !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Announcements.CreateAnnouncement(ctx, &corev1.CreateAnnouncementRequest{
		TayfaId: id, Title: b.Title, Body: b.Body, ClientRequestId: b.ClientRequestID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	a := announcementFrom(resp.GetAnnouncement())
	w.Header().Set("Location", "/v1/tayfas/"+a.TayfaID+"/announcements/"+a.ID)
	writeJSON(w, http.StatusCreated, a)
}

func (h coreHandlers) getAnnouncement(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, aid, ok := pathTayfaAnnouncement(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Announcements.GetAnnouncement(ctx, &corev1.GetAnnouncementRequest{TayfaId: id, AnnouncementId: aid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, announcementFrom(resp.GetAnnouncement()))
}

// announcementUpdateBody: a field present in the body is changed; null
// counts as absent.
type announcementUpdateBody struct {
	Title *string `json:"title"`
	Body  *string `json:"body"`
}

func (h coreHandlers) updateAnnouncement(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, aid, ok := pathTayfaAnnouncement(w, r)
	if !ok {
		return
	}
	var b announcementUpdateBody
	if !decodeLimit(w, r, &b, maxAnnouncementBody) {
		return
	}
	req := &corev1.UpdateAnnouncementRequest{TayfaId: id, AnnouncementId: aid, UpdateMask: &fieldmaskpb.FieldMask{}}
	if b.Title != nil {
		if !announcementTitleOK(*b.Title) {
			badRequest(w, r)
			return
		}
		req.Title = *b.Title
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "title")
	}
	if b.Body != nil {
		if !announcementBodyOK(*b.Body) {
			badRequest(w, r)
			return
		}
		req.Body = *b.Body
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "body")
	}
	if len(req.UpdateMask.Paths) == 0 {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Announcements.UpdateAnnouncement(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, announcementFrom(resp.GetAnnouncement()))
}

func (h coreHandlers) deleteAnnouncement(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, aid, ok := pathTayfaAnnouncement(w, r)
	if !ok {
		return
	}
	if _, err := h.d.Announcements.DeleteAnnouncement(ctx, &corev1.DeleteAnnouncementRequest{TayfaId: id, AnnouncementId: aid}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) pinAnnouncement(pinned bool) coreHandler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		id, aid, ok := pathTayfaAnnouncement(w, r)
		if !ok {
			return
		}
		resp, err := h.d.Announcements.SetAnnouncementPinned(ctx, &corev1.SetAnnouncementPinnedRequest{TayfaId: id, AnnouncementId: aid, Pinned: pinned})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, announcementFrom(resp.GetAnnouncement()))
	}
}

func (h coreHandlers) restoreAnnouncement(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, aid, ok := pathTayfaAnnouncement(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Announcements.RestoreAnnouncement(ctx, &corev1.RestoreAnnouncementRequest{TayfaId: id, AnnouncementId: aid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, announcementFrom(resp.GetAnnouncement()))
}
