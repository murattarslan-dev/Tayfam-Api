package rest

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// MediaClient is core's MediaService as the gateway uses it (issue #135,
// ADR-0020). AttachAvatar is left out: identity calls it.
type MediaClient interface {
	CreateUpload(ctx context.Context, in *corev1.CreateUploadRequest, opts ...grpc.CallOption) (*corev1.CreateUploadResponse, error)
	CompleteUpload(ctx context.Context, in *corev1.CompleteUploadRequest, opts ...grpc.CallOption) (*corev1.CompleteUploadResponse, error)
	GetUpload(ctx context.Context, in *corev1.GetUploadRequest, opts ...grpc.CallOption) (*corev1.GetUploadResponse, error)
	GetMediaUrls(ctx context.Context, in *corev1.GetMediaUrlsRequest, opts ...grpc.CallOption) (*corev1.GetMediaUrlsResponse, error)
}

const (
	// maxUploadBody fits the create request.
	maxUploadBody = 1 << 10
	// maxMediaURLsBody fits 100 UUIDs.
	maxMediaURLsBody = 8 << 10
	maxMediaURLs     = 100
	maxUploadBytes   = 10 << 20
)

// registerMedia serves the upload and media link routes. The file itself
// never passes through the gateway: the client PUTs it to the returned
// link. Signed links are bearer secrets; the gateway never logs bodies.
func registerMedia(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("POST /v1/uploads", a(h.createUpload))
	mux.HandleFunc("GET /v1/uploads/{upload_id}", a(h.getUpload))
	mux.HandleFunc("POST /v1/uploads/{upload_id}/complete", a(h.completeUpload))
	mux.HandleFunc("POST /v1/media/urls", a(h.mediaURLs))
}

var (
	uploadPurposes = newEnum(map[string]corev1.UploadPurpose{
		"avatar": corev1.UploadPurpose_UPLOAD_PURPOSE_AVATAR, "catch_photo": corev1.UploadPurpose_UPLOAD_PURPOSE_CATCH_PHOTO,
		"cover": corev1.UploadPurpose_UPLOAD_PURPOSE_COVER,
	})
	uploadStatuses = newEnum(map[string]corev1.UploadStatus{
		"pending": corev1.UploadStatus_UPLOAD_STATUS_PENDING, "uploaded": corev1.UploadStatus_UPLOAD_STATUS_UPLOADED,
		"processing": corev1.UploadStatus_UPLOAD_STATUS_PROCESSING, "ready": corev1.UploadStatus_UPLOAD_STATUS_READY,
		"rejected": corev1.UploadStatus_UPLOAD_STATUS_REJECTED,
	})
	uploadRejectReasons = newEnum(map[string]corev1.UploadRejectReason{
		"not_image":        corev1.UploadRejectReason_UPLOAD_REJECT_REASON_NOT_IMAGE,
		"unsupported_type": corev1.UploadRejectReason_UPLOAD_REJECT_REASON_UNSUPPORTED_TYPE,
		"too_large":        corev1.UploadRejectReason_UPLOAD_REJECT_REASON_TOO_LARGE,
		"corrupt":          corev1.UploadRejectReason_UPLOAD_REJECT_REASON_CORRUPT,
		"size_mismatch":    corev1.UploadRejectReason_UPLOAD_REJECT_REASON_SIZE_MISMATCH,
		"failed":           corev1.UploadRejectReason_UPLOAD_REJECT_REASON_FAILED,
	})
)

type mediaURLsJSON struct {
	UploadID    string    `json:"upload_id"`
	PreviewURL  string    `json:"preview_url"`
	FullURL     string    `json:"full_url"`
	ContentType string    `json:"content_type"`
	Width       int32     `json:"width"`
	Height      int32     `json:"height"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func mediaURLsFrom(m *corev1.MediaUrls) *mediaURLsJSON {
	if m == nil {
		return nil
	}
	return &mediaURLsJSON{
		UploadID: m.GetUploadId(), PreviewURL: m.GetPreviewUrl(), FullURL: m.GetFullUrl(), ContentType: m.GetContentType(),
		Width: m.GetWidth(), Height: m.GetHeight(), ExpiresAt: m.GetExpireTime().AsTime(),
	}
}

type uploadJSON struct {
	ID           string         `json:"id"`
	Purpose      string         `json:"purpose"`
	Status       string         `json:"status"`
	RejectReason string         `json:"reject_reason,omitempty"`
	TayfaID      string         `json:"tayfa_id,omitempty"`
	SizeBytes    int64          `json:"size_bytes"`
	CreatedAt    time.Time      `json:"created_at"`
	Attached     bool           `json:"attached"`
	URLs         *mediaURLsJSON `json:"urls,omitempty"`
}

func uploadFrom(u *corev1.Upload) uploadJSON {
	out := uploadJSON{
		ID: u.GetId(), Purpose: uploadPurposes.name(u.GetPurpose()), Status: uploadStatuses.name(u.GetStatus()),
		TayfaID: u.GetTayfaId(), SizeBytes: u.GetSizeBytes(), CreatedAt: u.GetCreateTime().AsTime(),
		Attached: u.GetAttached(), URLs: mediaURLsFrom(u.GetUrls()),
	}
	if u.GetRejectReason() != corev1.UploadRejectReason_UPLOAD_REJECT_REASON_UNSPECIFIED {
		out.RejectReason = uploadRejectReasons.name(u.GetRejectReason())
	}
	return out
}

type uploadCreateBody struct {
	Purpose         string `json:"purpose"`
	ContentType     string `json:"content_type"`
	SizeBytes       int64  `json:"size_bytes"`
	TayfaID         string `json:"tayfa_id"`
	ClientRequestID string `json:"client_request_id"`
}

type uploadCreatedJSON struct {
	Upload uploadJSON `json:"upload"`
	// Absent when a repeated request finds the upload past pending.
	UploadMethod    string            `json:"upload_method,omitempty"`
	UploadURL       string            `json:"upload_url,omitempty"`
	UploadHeaders   map[string]string `json:"upload_headers,omitempty"`
	UploadExpiresAt *time.Time        `json:"upload_expires_at,omitempty"`
}

func (h coreHandlers) createUpload(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b uploadCreateBody
	if !decodeLimit(w, r, &b, maxUploadBody) {
		return
	}
	purpose, ok := uploadPurposes.value(b.Purpose)
	if !ok || b.SizeBytes < 1 || b.SizeBytes > maxUploadBytes || len(b.ContentType) > 64 ||
		!validUUID(b.ClientRequestID) || (b.TayfaID != "" && !validUUID(b.TayfaID)) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Media.CreateUpload(ctx, &corev1.CreateUploadRequest{
		Purpose: purpose, ContentType: b.ContentType, SizeBytes: b.SizeBytes, TayfaId: b.TayfaID, ClientRequestId: b.ClientRequestID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := uploadCreatedJSON{Upload: uploadFrom(resp.GetUpload())}
	if resp.GetUploadUrl() != "" {
		out.UploadMethod, out.UploadURL, out.UploadHeaders = resp.GetUploadMethod(), resp.GetUploadUrl(), resp.GetUploadHeaders()
		out.UploadExpiresAt = optTime(resp.GetUploadExpireTime())
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, out)
}

type uploadViewJSON struct {
	Upload uploadJSON `json:"upload"`
}

func (h coreHandlers) getUpload(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := pathUUID(w, r, "upload_id")
	if !ok {
		return
	}
	resp, err := h.d.Media.GetUpload(ctx, &corev1.GetUploadRequest{UploadId: u})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, uploadViewJSON{Upload: uploadFrom(resp.GetUpload())})
}

func (h coreHandlers) completeUpload(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := pathUUID(w, r, "upload_id")
	if !ok {
		return
	}
	resp, err := h.d.Media.CompleteUpload(ctx, &corev1.CompleteUploadRequest{UploadId: u})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, uploadViewJSON{Upload: uploadFrom(resp.GetUpload())})
}

type mediaURLsBody struct {
	UploadIDs []string `json:"upload_ids"`
}

type mediaURLsListJSON struct {
	Media []mediaURLsJSON `json:"media"`
}

func (h coreHandlers) mediaURLs(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b mediaURLsBody
	if !decodeLimit(w, r, &b, maxMediaURLsBody) {
		return
	}
	if len(b.UploadIDs) == 0 || !validUUIDs(b.UploadIDs, maxMediaURLs) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Media.GetMediaUrls(ctx, &corev1.GetMediaUrlsRequest{UploadIds: b.UploadIDs})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := mediaURLsListJSON{Media: make([]mediaURLsJSON, 0, len(resp.GetMedia()))}
	for _, m := range resp.GetMedia() {
		out.Media = append(out.Media, *mediaURLsFrom(m))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
