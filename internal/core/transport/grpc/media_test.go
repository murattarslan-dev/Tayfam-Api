package grpc

import (
	"context"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
)

type fakeMedia struct {
	user id.ID
	in   media.CreateInput
	ids  []id.ID
	w, h int
	err  error
}

func (f *fakeMedia) CreateUpload(_ context.Context, u id.ID, in media.CreateInput) (media.Created, error) {
	f.user, f.in = u, in
	return media.Created{
		Upload: media.Upload{ID: id.New(), Purpose: in.Purpose, Status: media.StatusPending, CreatedAt: time.Now()},
		Links: []media.PutLink{
			{Kind: media.KindPreview, Presigned: objstore.Presigned{URL: "https://r2.example/p", Headers: map[string]string{"Content-Length": "1"}, Expires: time.Now()}},
			{Kind: media.KindFull, Presigned: objstore.Presigned{URL: "https://r2.example/f", Headers: map[string]string{"Content-Length": "2"}, Expires: time.Now()}},
		},
	}, f.err
}

func (f *fakeMedia) CompleteUpload(_ context.Context, u, _ id.ID, w, h int) (media.Upload, error) {
	f.user, f.w, f.h = u, w, h
	return media.Upload{Status: media.StatusRejected, RejectReason: "metadata_present"}, f.err
}

func (f *fakeMedia) GetUpload(_ context.Context, u, _ id.ID) (media.Upload, error) {
	f.user = u
	return media.Upload{Status: media.StatusReady, URLs: &media.URLs{Full: "https://r2.example/f", Width: 1600}}, f.err
}

func (f *fakeMedia) MediaURLs(_ context.Context, u id.ID, ids []id.ID) ([]media.URLs, error) {
	f.user, f.ids = u, ids
	return []media.URLs{{UploadID: ids[0]}}, f.err
}

func (f *fakeMedia) AttachAvatar(_ context.Context, u, _ id.ID) error {
	f.user = u
	return f.err
}

func TestMediaServer(t *testing.T) {
	f := &fakeMedia{}
	srv := NewMediaServer(f, discard)
	if _, err := srv.CreateUpload(context.Background(), &corev1.CreateUploadRequest{}); codeOf(t, err) != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	user := id.New()
	ctx := callerCtx(user)
	resp, err := srv.CreateUpload(ctx, &corev1.CreateUploadRequest{
		Purpose: corev1.UploadPurpose_UPLOAD_PURPOSE_COVER, TayfaId: id.New().String(), ClientRequestId: id.New().String(),
		Variants: []*corev1.UploadVariant{
			{Kind: corev1.VariantKind_VARIANT_KIND_PREVIEW, ContentType: "image/webp", SizeBytes: 1},
			{Kind: corev1.VariantKind_VARIANT_KIND_FULL, ContentType: "image/jpeg", SizeBytes: 2},
		},
	})
	if err != nil || f.user != user || f.in.Purpose != media.PurposeCover || len(f.in.Variants) != 2 || f.in.Variants[1].Kind != media.KindFull ||
		len(resp.GetTargets()) != 2 || resp.GetTargets()[1].GetKind() != corev1.VariantKind_VARIANT_KIND_FULL ||
		resp.GetTargets()[0].GetMethod() != http.MethodPut || resp.GetTargets()[1].GetHeaders()["Content-Length"] != "2" ||
		resp.GetUpload().GetStatus() != corev1.UploadStatus_UPLOAD_STATUS_PENDING {
		t.Fatalf("oluşturma: %v %v", resp, err)
	}
	for name, req := range map[string]*corev1.CreateUploadRequest{
		"amaç yok":           {ClientRequestId: "x"},
		"bozuk tayfa":        {Purpose: corev1.UploadPurpose_UPLOAD_PURPOSE_COVER, TayfaId: "x"},
		"bilinmeyen 9":       {Purpose: corev1.UploadPurpose(9)},
		"bilinmeyen varyant": {Purpose: corev1.UploadPurpose_UPLOAD_PURPOSE_AVATAR, Variants: []*corev1.UploadVariant{{Kind: corev1.VariantKind(7)}}},
	} {
		if _, err := srv.CreateUpload(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	c, err := srv.CompleteUpload(ctx, &corev1.CompleteUploadRequest{UploadId: id.New().String(), Width: 1600, Height: 900})
	if err != nil || f.w != 1600 || f.h != 900 || c.GetUpload().GetRejectReason() != corev1.UploadRejectReason_UPLOAD_REJECT_REASON_METADATA_PRESENT {
		t.Fatalf("tamamlama: %v %v", c, err)
	}
	g, err := srv.GetUpload(ctx, &corev1.GetUploadRequest{UploadId: id.New().String()})
	if err != nil || g.GetUpload().GetUrls().GetWidth() != 1600 {
		t.Fatalf("okuma: %v %v", g, err)
	}
	if _, err := srv.GetUpload(ctx, &corev1.GetUploadRequest{UploadId: "x"}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("bozuk kimlik: %v", err)
	}
	u := id.New()
	m, err := srv.GetMediaUrls(ctx, &corev1.GetMediaUrlsRequest{UploadIds: []string{u.String()}})
	if err != nil || len(m.GetMedia()) != 1 || f.ids[0] != u {
		t.Fatalf("linkler: %v %v", m, err)
	}
	if _, err := srv.GetMediaUrls(ctx, &corev1.GetMediaUrlsRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("boş liste: %v", err)
	}
	if _, err := srv.AttachAvatar(ctx, &corev1.AttachAvatarRequest{UploadId: id.New().String()}); err != nil {
		t.Fatal(err)
	}

	for appErr, want := range map[error]codes.Code{
		apperr.ErrUploadNotFound:      codes.NotFound,
		apperr.ErrUploadNotReady:      codes.FailedPrecondition,
		apperr.ErrUploadInUse:         codes.FailedPrecondition,
		apperr.ErrUploadNotReceived:   codes.FailedPrecondition,
		apperr.ErrUploadRateLimited:   codes.ResourceExhausted,
		apperr.ErrUploadQuotaExceeded: codes.ResourceExhausted,
		apperr.ErrUnavailable:         codes.Unavailable,
	} {
		f.err = appErr
		_, err := srv.AttachAvatar(ctx, &corev1.AttachAvatarRequest{UploadId: id.New().String()})
		if codeOf(t, err) != want {
			t.Errorf("%v: %v", appErr, err)
		}
	}
}
