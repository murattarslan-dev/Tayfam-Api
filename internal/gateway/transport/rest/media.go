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
	// maxCompleteBody fits the size.
	maxCompleteBody = 256
	// maxMediaURLsBody fits 100 UUIDs.
	maxMediaURLsBody = 8 << 10
	maxMediaURLs     = 100
	maxVariantBytes  = 10 << 20
	maxFullSide      = 1600
)

// registerMedia serves the upload and media link routes. The files never
// pass through the gateway: the client PUTs each variant to its returned
// link. No route takes a URL or storage key from the client. Signed links
// are bearer secrets; the gateway never logs bodies.
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
		"pending": corev1.UploadStatus_UPLOAD_STATUS_PENDING, "ready": corev1.UploadStatus_UPLOAD_STATUS_READY,
		"rejected": corev1.UploadStatus_UPLOAD_STATUS_REJECTED,
	})
	uploadRejectReasons = newEnum(map[string]corev1.UploadRejectReason{
		"not_image":          corev1.UploadRejectReason_UPLOAD_REJECT_REASON_NOT_IMAGE,
		"unsupported_type":   corev1.UploadRejectReason_UPLOAD_REJECT_REASON_UNSUPPORTED_TYPE,
		"metadata_present":   corev1.UploadRejectReason_UPLOAD_REJECT_REASON_METADATA_PRESENT,
		"dimension_mismatch": corev1.UploadRejectReason_UPLOAD_REJECT_REASON_DIMENSION_MISMATCH,
		"size_mismatch":      corev1.UploadRejectReason_UPLOAD_REJECT_REASON_SIZE_MISMATCH,
		"too_large":          corev1.UploadRejectReason_UPLOAD_REJECT_REASON_TOO_LARGE,
		"failed":             corev1.UploadRejectReason_UPLOAD_REJECT_REASON_FAILED,
	})
	variantKinds = newEnum(map[string]corev1.VariantKind{
		"preview": corev1.VariantKind_VARIANT_KIND_PREVIEW, "full": corev1.VariantKind_VARIANT_KIND_FULL,
	})
)

type mediaURLsJSON struct {
	UploadID           string    `json:"upload_id"`
	PreviewURL         string    `json:"preview_url"`
	PreviewContentType string    `json:"preview_content_type"`
	FullURL            string    `json:"full_url"`
	FullContentType    string    `json:"full_content_type"`
	Width              int32     `json:"width"`
	Height             int32     `json:"height"`
	ExpiresAt          time.Time `json:"expires_at"`
}

func mediaURLsFrom(m *corev1.MediaUrls) *mediaURLsJSON {
	if m == nil {
		return nil
	}
	return &mediaURLsJSON{
		UploadID: m.GetUploadId(), PreviewURL: m.GetPreviewUrl(), PreviewContentType: m.GetPreviewContentType(),
		FullURL: m.GetFullUrl(), FullContentType: m.GetFullContentType(),
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

type uploadVariantJSON struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type uploadCreateBody struct {
	Purpose         string              `json:"purpose"`
	Variants        []uploadVariantJSON `json:"variants"`
	TayfaID         string              `json:"tayfa_id"`
	ClientRequestID string              `json:"client_request_id"`
}

type uploadTargetJSON struct {
	Kind      string            `json:"kind"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type uploadCreatedJSON struct {
	Upload uploadJSON `json:"upload"`
	// Empty when a repeated request finds the upload past pending.
	Targets []uploadTargetJSON `json:"targets"`
}

func (h coreHandlers) createUpload(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b uploadCreateBody
	if !decodeLimit(w, r, &b, maxUploadBody) {
		return
	}
	purpose, ok := uploadPurposes.value(b.Purpose)
	if !ok || len(b.Variants) != 2 || !validUUID(b.ClientRequestID) || (b.TayfaID != "" && !validUUID(b.TayfaID)) {
		badRequest(w, r)
		return
	}
	req := &corev1.CreateUploadRequest{Purpose: purpose, TayfaId: b.TayfaID, ClientRequestId: b.ClientRequestID}
	for _, v := range b.Variants {
		kind, ok := variantKinds.value(v.Kind)
		if !ok || (v.ContentType != "image/jpeg" && v.ContentType != "image/webp") || v.SizeBytes < 1 || v.SizeBytes > maxVariantBytes {
			badRequest(w, r)
			return
		}
		req.Variants = append(req.Variants, &corev1.UploadVariant{Kind: kind, ContentType: v.ContentType, SizeBytes: v.SizeBytes})
	}
	resp, err := h.d.Media.CreateUpload(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := uploadCreatedJSON{Upload: uploadFrom(resp.GetUpload()), Targets: []uploadTargetJSON{}}
	for _, t := range resp.GetTargets() {
		out.Targets = append(out.Targets, uploadTargetJSON{
			Kind: variantKinds.name(t.GetKind()), Method: t.GetMethod(), URL: t.GetUrl(), Headers: t.GetHeaders(),
			ExpiresAt: t.GetExpireTime().AsTime(),
		})
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

type uploadCompleteBody struct {
	Width  int32 `json:"width"`
	Height int32 `json:"height"`
}

func (h coreHandlers) completeUpload(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := pathUUID(w, r, "upload_id")
	if !ok {
		return
	}
	var b uploadCompleteBody
	if !decodeLimit(w, r, &b, maxCompleteBody) {
		return
	}
	if b.Width < 1 || b.Width > maxFullSide || b.Height < 1 || b.Height > maxFullSide {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Media.CompleteUpload(ctx, &corev1.CompleteUploadRequest{UploadId: u, Width: b.Width, Height: b.Height})
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
