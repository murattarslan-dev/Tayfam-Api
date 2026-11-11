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
	in.CoverUploadID = mediatest.Ready(t, e.pool, admin, media.PurposeCatchPhoto, tf)
	_, err = e.svc.Create(ctx, admin, tf, in)
	wantErr(t, "av fotoğrafı kapak olamaz", err, apperr.ErrUploadNotFound)

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

// A catch photo is the angler's own READY catch photo upload of the tayfa;
// it goes when the catch is deleted for good.
func TestCatchPhoto(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, fishingInput(SpotParticipants))
	angler := e.going(t, tf, ev.ID, authz.Member)
	other := e.seed(t, tf, authz.Member)
	c := &clock{t: ev.StartsAt}
	svc := e.clocked(t, c, nil)
	_, err := svc.CheckIn(ctx, angler, tf, ev.ID, izmir)
	must(t, err)
	add := func(photo id.ID) (Catch, error) {
		return svc.AddCatch(ctx, angler, tf, ev.ID, CatchInput{Species: "Levrek", LengthCm: 40, ClientRequestID: id.New().String(), PhotoUploadID: photo})
	}
	_, err = add(mediatest.Ready(t, e.pool, other, media.PurposeCatchPhoto, tf))
	wantErr(t, "başkasının fotoğrafı", err, apperr.ErrUploadNotFound)
	_, err = add(mediatest.Ready(t, e.pool, angler, media.PurposeCover, tf))
	wantErr(t, "kapak av fotoğrafı olamaz", err, apperr.ErrUploadNotFound)

	photo := mediatest.Ready(t, e.pool, angler, media.PurposeCatchPhoto, tf)
	cat, err := add(photo)
	must(t, err)
	if cat.PhotoUploadID != photo {
		t.Fatalf("fotoğraf: %v", cat.PhotoUploadID)
	}
	list, _, err := svc.ListCatches(ctx, other, tf, ev.ID, 0, "")
	must(t, err)
	if len(list) != 1 || list[0].PhotoUploadID != photo {
		t.Fatal("üye av fotoğrafını görmeli")
	}
	// As the 30-day purge does (core_app, triggers on).
	_, err = e.pool.Exec(ctx, "DELETE FROM fishing_catches WHERE id = $1", cat.ID.PG())
	must(t, err)
	if s := mediatest.Status(t, e.pool, photo); s != "deleting" {
		t.Fatalf("av silinince fotoğraf silinmeli: %s", s)
	}
}
