package grpc

import (
	"testing"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func TestEventPhotoServerValidates(t *testing.T) {
	f := &fakeEvents{}
	srv := NewEventServer(f, discard)
	ctx := callerCtx(id.New())
	good := id.New().String()
	for name, tc := range map[string]struct {
		call  func() error
		field string
	}{
		"bozuk yükleme": {func() error {
			_, err := srv.AddEventPhoto(ctx, &corev1.AddEventPhotoRequest{TayfaId: good, EventId: good, UploadId: "x"})
			return err
		}, "upload_id"},
		"yüklemesiz": {func() error {
			_, err := srv.AddEventPhoto(ctx, &corev1.AddEventPhotoRequest{TayfaId: good, EventId: good})
			return err
		}, "upload_id"},
		"bozuk etkinlik": {func() error {
			_, err := srv.ListEventPhotos(ctx, &corev1.ListEventPhotosRequest{TayfaId: good, EventId: "x"})
			return err
		}, "event_id"},
		"bozuk fotoğraf": {func() error {
			_, err := srv.RemoveEventPhoto(ctx, &corev1.RemoveEventPhotoRequest{TayfaId: good, EventId: good, PhotoId: "x"})
			return err
		}, "photo_id"},
	} {
		code, info := infoOf(t, tc.call())
		if code != codes.InvalidArgument || info.GetMetadata()["field"] != tc.field {
			t.Errorf("%s: %v %v", name, code, info.GetMetadata())
		}
	}
	if f.calls != 0 {
		t.Fatal("geçersiz istek app katmanına ulaştı")
	}
}

func TestEventPhotoServerConverts(t *testing.T) {
	user := id.New()
	f := &fakeEvents{}
	srv := NewEventServer(f, discard)
	ctx := callerCtx(user)
	tid, eid, upload := id.New(), id.New(), id.New()

	resp, err := srv.AddEventPhoto(ctx, &corev1.AddEventPhotoRequest{
		TayfaId: tid.String(), EventId: eid.String(), UploadId: upload.String(), Caption: "Zirvede", ClientRequestId: "istek",
	})
	if err != nil || f.tayfa != tid || f.event != eid || f.user != user || f.photo.UploadID != upload ||
		f.photo.Caption != "Zirvede" || f.photo.ClientRequestID != "istek" {
		t.Fatalf("ekleme app'e %+v olarak gitti: %v", f.photo, err)
	}
	p := resp.GetPhoto()
	if p.GetUploadId() != upload.String() || p.GetUploaderId() != user.String() || p.GetEventId() != eid.String() ||
		p.GetCaption() != "Zirvede" || p.GetCreateTime() == nil {
		t.Fatalf("fotoğraf = %v", p)
	}

	list, err := srv.ListEventPhotos(ctx, &corev1.ListEventPhotosRequest{TayfaId: tid.String(), EventId: eid.String()})
	if err != nil || len(list.GetPhotos()) != 1 || list.GetNextPageToken() != "sonraki" || list.GetPhotos()[0].GetCaption() != "" {
		t.Fatalf("liste = %v %v", list, err)
	}

	photo := id.New()
	if _, err := srv.RemoveEventPhoto(ctx, &corev1.RemoveEventPhotoRequest{TayfaId: tid.String(), EventId: eid.String(), PhotoId: photo.String()}); err != nil || f.photoID != photo {
		t.Fatalf("kaldırma = %v %v", f.photoID, err)
	}

	f.err = apperr.ErrAlbumClosed
	_, err = srv.AddEventPhoto(ctx, &corev1.AddEventPhotoRequest{TayfaId: tid.String(), EventId: eid.String(), UploadId: upload.String()})
	if code, info := infoOf(t, err); code != codes.FailedPrecondition || info.GetReason() != corev1.ErrorReason_ERROR_REASON_ALBUM_CLOSED.String() {
		t.Fatalf("albüm kapalı = %v %v", code, info)
	}
}
