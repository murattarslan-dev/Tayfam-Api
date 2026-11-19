package event

import (
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Event covers (issue #135): the creator's own READY cover upload of the
// tayfa; replaced or removed covers go to the delete queue; not with a
// series update.
func TestEventCover(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.seed(t, tf, authz.Member)

	in := input()
	in.CoverUploadID = mediatest.Ready(t, e.pool, member, media.PurposeCover, tf)
	_, err := e.svc.Create(ctx, admin, tf, in)
	wantErr(t, "başkasının kapağı", err, apperr.ErrUploadNotFound)

	in = input()
	in.CoverUploadID = mediatest.Ready(t, e.pool, admin, media.PurposeLogPhoto, tf)
	_, err = e.svc.Create(ctx, admin, tf, in)
	wantErr(t, "günlük fotoğrafı kapak olamaz", err, apperr.ErrUploadNotFound)

	first := mediatest.Ready(t, e.pool, admin, media.PurposeCover, tf)
	in = input()
	in.CoverUploadID = first
	ev := e.create(t, tf, admin, in)
	if ev.CoverUploadID != first {
		t.Fatalf("kapak: %v", ev.CoverUploadID)
	}
	// The same upload cannot cover a second event.
	in = input()
	in.CoverUploadID = first
	_, err = e.svc.Create(ctx, admin, tf, in)
	wantErr(t, "aynı dosya iki etkinlikte", err, apperr.ErrUploadInUse)

	second := mediatest.Ready(t, e.pool, admin, media.PurposeCover, tf)
	got, err := e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldCover}, Input{CoverUploadID: second})
	must(t, err)
	if got.CoverUploadID != second || mediatest.Status(t, e.pool, first) != "deleting" {
		t.Fatal("kapak değişmeli, eskisi silinmeli")
	}
	got, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldCover}, Input{})
	must(t, err)
	if got.CoverUploadID != (id.ID{}) || mediatest.Status(t, e.pool, second) != "deleting" {
		t.Fatal("kapak kaldırılmalı")
	}
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldCover}, Input{Series: true})
	wantField(t, "seride kapak", err, "update_mask")
}
