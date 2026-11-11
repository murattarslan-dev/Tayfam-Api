package rest

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
)

// DataExportClient is identity's DataExportService (issue #144, ADR-0021):
// the caller's KVKK data export. identity collects with the caller's own
// token; gateway only checks the token early and converts.
type DataExportClient interface {
	CreateDataExport(ctx context.Context, in *identityv1.CreateDataExportRequest, opts ...grpc.CallOption) (*identityv1.CreateDataExportResponse, error)
	ListDataExports(ctx context.Context, in *identityv1.ListDataExportsRequest, opts ...grpc.CallOption) (*identityv1.ListDataExportsResponse, error)
	GetDataExport(ctx context.Context, in *identityv1.GetDataExportRequest, opts ...grpc.CallOption) (*identityv1.GetDataExportResponse, error)
	GetDataExportDownload(ctx context.Context, in *identityv1.GetDataExportDownloadRequest, opts ...grpc.CallOption) (*identityv1.GetDataExportDownloadResponse, error)
}

const maxDataExportBody = 256

func registerDataExports(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("POST /v1/me/data-exports", a(h.createDataExport))
	mux.HandleFunc("GET /v1/me/data-exports", a(h.listDataExports))
	mux.HandleFunc("GET /v1/me/data-exports/{data_export_id}", a(h.getDataExport))
	mux.HandleFunc("POST /v1/me/data-exports/{data_export_id}/download", a(h.downloadDataExport))
}

var dataExportStatusNames = map[identityv1.DataExportStatus]string{
	identityv1.DataExportStatus_DATA_EXPORT_STATUS_PENDING:    "pending",
	identityv1.DataExportStatus_DATA_EXPORT_STATUS_PROCESSING: "processing",
	identityv1.DataExportStatus_DATA_EXPORT_STATUS_READY:      "ready",
	identityv1.DataExportStatus_DATA_EXPORT_STATUS_EXPIRED:    "expired",
	identityv1.DataExportStatus_DATA_EXPORT_STATUS_FAILED:     "failed",
}

var dataExportFailureNames = map[identityv1.DataExportFailureReason]string{
	identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_UNAVAILABLE: "unavailable",
	identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_TOO_LARGE:   "too_large",
	identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_TIMEOUT:     "timeout",
	identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_FAILED:      "failed",
}

type dataExportJSON struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	FailureReason string     `json:"failure_reason,omitempty"`
	RequestedAt   time.Time  `json:"requested_at"`
	ReadyAt       *time.Time `json:"ready_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	DownloadedAt  *time.Time `json:"downloaded_at,omitempty"`
	SizeBytes     int64      `json:"size_bytes,omitempty"`
}

type dataExportListJSON struct {
	DataExports   []dataExportJSON `json:"data_exports"`
	NextRequestAt *time.Time       `json:"next_request_at,omitempty"`
}

type dataExportDownloadJSON struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	// Key is the base64 AES-256 key; with the URL it opens the file.
	Key       []byte `json:"key"`
	SizeBytes int64  `json:"size_bytes"`
	Format    string `json:"format"`
}

type dataExportCreateBody struct {
	ClientRequestID string `json:"client_request_id"`
}

func dataExportOf(d *identityv1.DataExport) dataExportJSON {
	out := dataExportJSON{
		ID: d.GetId(), Status: nameOr(dataExportStatusNames, d.GetStatus()),
		RequestedAt: d.GetRequestTime().AsTime().UTC(), SizeBytes: d.GetSizeBytes(),
	}
	if r := d.GetFailureReason(); r != identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_UNSPECIFIED {
		out.FailureReason = nameOr(dataExportFailureNames, r)
	}
	for _, f := range []struct {
		dst **time.Time
		ok  bool
		t   func() time.Time
	}{
		{&out.ReadyAt, d.GetReadyTime() != nil, func() time.Time { return d.GetReadyTime().AsTime().UTC() }},
		{&out.ExpiresAt, d.GetExpireTime() != nil, func() time.Time { return d.GetExpireTime().AsTime().UTC() }},
		{&out.DownloadedAt, d.GetDownloadTime() != nil, func() time.Time { return d.GetDownloadTime().AsTime().UTC() }},
	} {
		if f.ok {
			v := f.t()
			*f.dst = &v
		}
	}
	return out
}

func (h coreHandlers) createDataExport(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b dataExportCreateBody
	if !decodeLimit(w, r, &b, maxDataExportBody) {
		return
	}
	if !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.DataExports.CreateDataExport(ctx, &identityv1.CreateDataExportRequest{ClientRequestId: b.ClientRequestID})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, dataExportOf(resp.GetDataExport()))
}

func (h coreHandlers) listDataExports(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.DataExports.ListDataExports(ctx, &identityv1.ListDataExportsRequest{})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	out := dataExportListJSON{DataExports: make([]dataExportJSON, 0, len(resp.GetDataExports()))}
	for _, d := range resp.GetDataExports() {
		out.DataExports = append(out.DataExports, dataExportOf(d))
	}
	if t := resp.GetNextRequestTime(); t != nil {
		v := t.AsTime().UTC()
		out.NextRequestAt = &v
	}
	writeJSON(w, http.StatusOK, out)
}

func (h coreHandlers) getDataExport(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	rid := r.PathValue("data_export_id")
	if !validUUID(rid) {
		httpx.WriteProblem(w, r, http.StatusNotFound, "data-export-not-found", "Not Found", "")
		return
	}
	resp, err := h.d.DataExports.GetDataExport(ctx, &identityv1.GetDataExportRequest{DataExportId: rid})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dataExportOf(resp.GetDataExport()))
}

func (h coreHandlers) downloadDataExport(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	rid := r.PathValue("data_export_id")
	if !validUUID(rid) {
		httpx.WriteProblem(w, r, http.StatusNotFound, "data-export-not-found", "Not Found", "")
		return
	}
	resp, err := h.d.DataExports.GetDataExportDownload(ctx, &identityv1.GetDataExportDownloadRequest{DataExportId: rid})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	// The link and the key are bearer secrets: no cache may keep them.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, dataExportDownloadJSON{
		URL: resp.GetUrl(), ExpiresAt: resp.GetExpireTime().AsTime().UTC(), Key: resp.GetKey(),
		SizeBytes: resp.GetSizeBytes(), Format: resp.GetFormat(),
	})
}
