package rest

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

const dataExportID = "01965a3c-0000-7000-8000-000000000044"

// The KVKK data export on fakeCore (DataExportClient, issue #144).

func (f *fakeCore) CreateDataExport(ctx context.Context, in *identityv1.CreateDataExportRequest, _ ...grpc.CallOption) (*identityv1.CreateDataExportResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &identityv1.CreateDataExportResponse{DataExport: &identityv1.DataExport{
		Id: dataExportID, Status: identityv1.DataExportStatus_DATA_EXPORT_STATUS_PENDING, RequestTime: timestamppb.New(t0),
	}}, nil
}

func (f *fakeCore) ListDataExports(ctx context.Context, in *identityv1.ListDataExportsRequest, _ ...grpc.CallOption) (*identityv1.ListDataExportsResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &identityv1.ListDataExportsResponse{
		DataExports: []*identityv1.DataExport{{
			Id: dataExportID, Status: identityv1.DataExportStatus_DATA_EXPORT_STATUS_FAILED,
			FailureReason: identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_TOO_LARGE, RequestTime: timestamppb.New(t0),
		}},
		NextRequestTime: timestamppb.New(t0.Add(30 * 24 * time.Hour)),
	}, nil
}

func (f *fakeCore) GetDataExport(ctx context.Context, in *identityv1.GetDataExportRequest, _ ...grpc.CallOption) (*identityv1.GetDataExportResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &identityv1.GetDataExportResponse{DataExport: &identityv1.DataExport{
		Id: in.GetDataExportId(), Status: identityv1.DataExportStatus_DATA_EXPORT_STATUS_READY, RequestTime: timestamppb.New(t0),
		ReadyTime: timestamppb.New(t0), ExpireTime: timestamppb.New(t0.Add(7 * 24 * time.Hour)), SizeBytes: 1234,
	}}, nil
}

func (f *fakeCore) GetDataExportDownload(ctx context.Context, in *identityv1.GetDataExportDownloadRequest, _ ...grpc.CallOption) (*identityv1.GetDataExportDownloadResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &identityv1.GetDataExportDownloadResponse{
		Url: "https://acct.r2.cloudflarestorage.com/x", ExpireTime: timestamppb.New(t0.Add(5 * time.Minute)),
		Key: []byte("0123456789abcdef0123456789abcdef"), SizeBytes: 1234, Format: "aes-256-gcm-v1",
	}, nil
}

func TestDataExportRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)

	w := as(h, "POST", "/v1/me/data-exports", `{"client_request_id":"`+dataExportID+`"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("talep: %d %s", w.Code, w.Body)
	}
	created := decodeBody[dataExportJSON](t, w)
	if created.Status != "pending" || created.ReadyAt != nil {
		t.Fatalf("talep: %+v", created)
	}
	if got, tok, _ := f.last(); got.(*identityv1.CreateDataExportRequest).GetClientRequestId() != dataExportID || tok != "USERTOKEN" {
		t.Fatalf("identity'ye giden: %v %q", got, tok)
	}
	for _, body := range []string{`{}`, `{"client_request_id":"x"}`, `{"client_request_id":"` + dataExportID + `","user_id":"x"}`} {
		if w := as(h, "POST", "/v1/me/data-exports", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, w.Code)
		}
	}

	list := decodeBody[dataExportListJSON](t, as(h, "GET", "/v1/me/data-exports", ""))
	if len(list.DataExports) != 1 || list.DataExports[0].FailureReason != "too_large" || list.NextRequestAt == nil {
		t.Fatalf("liste: %+v", list)
	}
	got := decodeBody[dataExportJSON](t, as(h, "GET", "/v1/me/data-exports/"+dataExportID, ""))
	if got.Status != "ready" || got.SizeBytes != 1234 || got.ExpiresAt == nil {
		t.Fatalf("talep: %+v", got)
	}

	w = as(h, "POST", "/v1/me/data-exports/"+dataExportID+"/download", "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("indirme: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	d := decodeBody[map[string]any](t, w)
	if d["key"] != base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")) || d["format"] != "aes-256-gcm-v1" {
		t.Fatalf("indirme: %v", d)
	}

	// A malformed ID never reaches identity.
	_, _, before := f.last()
	for _, path := range []string{"/v1/me/data-exports/x", "/v1/me/data-exports/x/download"} {
		method := "GET"
		if path != "/v1/me/data-exports/x" {
			method = "POST"
		}
		if w := as(h, method, path, ""); w.Code != http.StatusNotFound || problemType(t, w) != "data-export-not-found" {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if _, _, n := f.last(); n != before {
		t.Fatal("geçersiz kimlik identity'ye ulaştı")
	}
	// No token, no route.
	if w := do(h, "GET", "/v1/me/data-exports", "", map[string]string{clientHeader: "android"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("token'sız: %d", w.Code)
	}

	for reason, want := range map[string]struct {
		code    codes.Code
		status  int
		problem string
	}{
		"ERROR_REASON_DATA_EXPORT_NOT_FOUND":     {codes.NotFound, http.StatusNotFound, "data-export-not-found"},
		"ERROR_REASON_DATA_EXPORT_IN_PROGRESS":   {codes.FailedPrecondition, http.StatusConflict, "data-export-in-progress"},
		"ERROR_REASON_DATA_EXPORT_LIMIT":         {codes.ResourceExhausted, http.StatusTooManyRequests, "data-export-limit"},
		"ERROR_REASON_DATA_EXPORT_NOT_READY":     {codes.FailedPrecondition, http.StatusConflict, "data-export-not-ready"},
		"ERROR_REASON_DATA_EXPORT_EXPIRED":       {codes.FailedPrecondition, http.StatusGone, "data-export-expired"},
		"ERROR_REASON_REAUTHENTICATION_REQUIRED": {codes.PermissionDenied, http.StatusForbidden, "reauthentication-required"},
	} {
		f.err = identityErr(want.code, reason)
		w := as(h, "POST", "/v1/me/data-exports/"+dataExportID+"/download", "")
		if w.Code != want.status || problemType(t, w) != want.problem {
			t.Errorf("%s: %d %s", reason, w.Code, problemType(t, w))
		}
	}
}
