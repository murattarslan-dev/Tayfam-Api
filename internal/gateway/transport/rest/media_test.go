package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const uploadID = "01890a5d-ac96-774b-bcce-b302099a8057"

func testUpload() *corev1.Upload {
	return &corev1.Upload{
		Id: uploadID, Purpose: corev1.UploadPurpose_UPLOAD_PURPOSE_COVER, Status: corev1.UploadStatus_UPLOAD_STATUS_READY,
		TayfaId: tayfaID, SizeBytes: 1234, CreateTime: timestamppb.New(t0), Attached: true,
		Urls: &corev1.MediaUrls{
			UploadId: uploadID, PreviewUrl: "https://r2.example/p?X-Amz-Signature=a", FullUrl: "https://r2.example/f?X-Amz-Signature=b",
			ContentType: "image/jpeg", Width: 1600, Height: 1200, ExpireTime: timestamppb.New(t0),
		},
	}
}

func (f *fakeCore) CreateUpload(ctx context.Context, in *corev1.CreateUploadRequest, _ ...grpc.CallOption) (*corev1.CreateUploadResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &corev1.CreateUploadResponse{
		Upload:       &corev1.Upload{Id: uploadID, Purpose: in.GetPurpose(), Status: corev1.UploadStatus_UPLOAD_STATUS_PENDING, SizeBytes: in.GetSizeBytes(), CreateTime: timestamppb.New(t0)},
		UploadMethod: "PUT", UploadUrl: "https://r2.example/put?X-Amz-Signature=c",
		UploadHeaders:    map[string]string{"Content-Type": in.GetContentType(), "Content-Length": "1234"},
		UploadExpireTime: timestamppb.New(t0),
	}, nil
}

func (f *fakeCore) CompleteUpload(ctx context.Context, in *corev1.CompleteUploadRequest, _ ...grpc.CallOption) (*corev1.CompleteUploadResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	u := testUpload()
	u.Status, u.Urls = corev1.UploadStatus_UPLOAD_STATUS_UPLOADED, nil
	return &corev1.CompleteUploadResponse{Upload: u}, nil
}

func (f *fakeCore) GetUpload(ctx context.Context, in *corev1.GetUploadRequest, _ ...grpc.CallOption) (*corev1.GetUploadResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &corev1.GetUploadResponse{Upload: testUpload()}, nil
}

func (f *fakeCore) GetMediaUrls(ctx context.Context, in *corev1.GetMediaUrlsRequest, _ ...grpc.CallOption) (*corev1.GetMediaUrlsResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &corev1.GetMediaUrlsResponse{Media: []*corev1.MediaUrls{testUpload().GetUrls()}}, nil
}

func TestCreateUpload(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	body := `{"purpose":"cover","content_type":"image/jpeg","size_bytes":1234,"tayfa_id":"` + tayfaID + `","client_request_id":"` + uploadID + `"}`
	w := as(h, "POST", "/v1/uploads", body)
	if w.Code != http.StatusCreated || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	got := decodeBody[uploadCreatedJSON](t, w)
	if got.Upload.Status != "pending" || got.UploadMethod != "PUT" || got.UploadURL == "" || got.UploadHeaders["Content-Length"] != "1234" || got.UploadExpiresAt == nil {
		t.Fatalf("yanıt: %+v", got)
	}
	in, token, _ := f.last()
	req := in.(*corev1.CreateUploadRequest)
	if req.GetPurpose() != corev1.UploadPurpose_UPLOAD_PURPOSE_COVER || req.GetTayfaId() != tayfaID || token != "USERTOKEN" {
		t.Fatalf("core'a giden: %v", req)
	}

	for name, b := range map[string]string{
		"bilinmeyen amaç": `{"purpose":"data_export","content_type":"image/jpeg","size_bytes":1,"client_request_id":"` + uploadID + `"}`,
		"boş dosya":       `{"purpose":"avatar","content_type":"image/jpeg","size_bytes":0,"client_request_id":"` + uploadID + `"}`,
		"10 MiB üstü":     `{"purpose":"avatar","content_type":"image/jpeg","size_bytes":10485761,"client_request_id":"` + uploadID + `"}`,
		"tekrar kimliği":  `{"purpose":"avatar","content_type":"image/jpeg","size_bytes":1}`,
		"bozuk tayfa":     `{"purpose":"cover","content_type":"image/jpeg","size_bytes":1,"tayfa_id":"x","client_request_id":"` + uploadID + `"}`,
	} {
		if w := as(h, "POST", "/v1/uploads", b); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

func TestUploadErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		code int
		typ  string
	}{
		"ERROR_REASON_UPLOAD_RATE_LIMITED":    {http.StatusTooManyRequests, "upload-rate-limited"},
		"ERROR_REASON_UPLOAD_QUOTA_EXCEEDED":  {http.StatusConflict, "upload-quota-exceeded"},
		"ERROR_REASON_MEDIA_TYPE_UNSUPPORTED": {http.StatusUnsupportedMediaType, "media-type-unsupported"},
		"ERROR_REASON_UPLOAD_NOT_RECEIVED":    {http.StatusConflict, "upload-not-received"},
		"ERROR_REASON_UPLOAD_NOT_FOUND":       {http.StatusNotFound, "upload-not-found"},
	} {
		f := &fakeCore{err: coreErr(codes.FailedPrecondition, reason)}
		w := as(coreRouter(f), "POST", "/v1/uploads/"+uploadID+"/complete", "")
		if w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, problemType(t, w))
		}
	}
	// Media off in core: 503 without detail.
	w := as(coreRouter(&fakeCore{err: coreErr(codes.Unavailable, "")}), "GET", "/v1/uploads/"+uploadID, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("kapalı medya: %d", w.Code)
	}
}

func TestGetAndCompleteUpload(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "GET", "/v1/uploads/"+uploadID, "")
	got := decodeBody[uploadViewJSON](t, w)
	if w.Code != http.StatusOK || got.Upload.URLs == nil || got.Upload.URLs.Width != 1600 || !got.Upload.Attached {
		t.Fatalf("%d %+v", w.Code, got)
	}
	w = as(h, "POST", "/v1/uploads/"+uploadID+"/complete", "")
	got = decodeBody[uploadViewJSON](t, w)
	if w.Code != http.StatusOK || got.Upload.Status != "uploaded" || got.Upload.URLs != nil {
		t.Fatalf("%d %+v", w.Code, got)
	}
	if w := as(h, "GET", "/v1/uploads/not-a-uuid", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bozuk kimlik: %d", w.Code)
	}
}

func TestMediaURLs(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "POST", "/v1/media/urls", `{"upload_ids":["`+uploadID+`"]}`)
	got := decodeBody[mediaURLsListJSON](t, w)
	if w.Code != http.StatusOK || len(got.Media) != 1 || got.Media[0].FullURL == "" {
		t.Fatalf("%d %+v", w.Code, got)
	}
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = `"` + uploadID + `"`
	}
	for name, b := range map[string]string{
		"boş":        `{"upload_ids":[]}`,
		"bozuk":      `{"upload_ids":["x"]}`,
		"100 üstü":   `{"upload_ids":[` + strings.Join(ids, ",") + `]}`,
		"tür hatası": `{"upload_ids":"` + uploadID + `"}`,
	} {
		if w := as(h, "POST", "/v1/media/urls", b); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}
