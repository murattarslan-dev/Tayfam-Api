package tayfa

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The cover (issue #135): the admin's own READY cover upload of this tayfa;
// another user's, another tayfa's or another purpose's upload is refused;
// the replaced cover goes to the delete queue.
func TestCover(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tayfa, admin := e.create(t, JoinOpen)
	e.shift = 48 * time.Hour // the daily tayfa limit
	ov, err := e.svc.Create(ctx, admin, input("Diğer Tayfa"))
	must(t, err)
	other := ov.ID
	member := e.member(t, tayfa, admin, authz.Member)
	set := func(user, upload id.ID) (View, error) {
		return e.svc.Update(ctx, user, tayfa, UpdateInput{Fields: []string{FieldCover}, CoverUploadID: upload})
	}

	first := mediatest.Ready(t, e.pool, admin, media.PurposeCover, tayfa)
	for name, tc := range map[string]struct {
		user, upload id.ID
		want         error
	}{
		"başkasının dosyası":     {admin, mediatest.Ready(t, e.pool, member, media.PurposeCover, tayfa), apperr.ErrUploadNotFound},
		"başka tayfanın":         {admin, mediatest.Ready(t, e.pool, admin, media.PurposeCover, other), apperr.ErrUploadNotFound},
		"avatar kapak olamaz":    {admin, mediatest.Ready(t, e.pool, admin, media.PurposeAvatar, id.ID{}), apperr.ErrUploadNotFound},
		"olmayan dosya":          {admin, id.New(), apperr.ErrUploadNotFound},
		"üye kapak değiştiremez": {member, mediatest.Ready(t, e.pool, member, media.PurposeCover, tayfa), apperr.ErrPermissionDenied},
	} {
		if _, err := set(tc.user, tc.upload); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	v, err := set(admin, first)
	must(t, err)
	if v.CoverUploadID != first {
		t.Fatalf("kapak: %v", v.CoverUploadID)
	}
	if got, _, err := e.svc.Get(ctx, member, tayfa); err != nil || got.CoverUploadID != first {
		t.Fatalf("üye kapağı görmeli: %v", err)
	}
	second := mediatest.Ready(t, e.pool, admin, media.PurposeCover, tayfa)
	if _, err := set(admin, second); err != nil {
		t.Fatal(err)
	}
	if s := mediatest.Status(t, e.pool, first); s != "deleting" {
		t.Fatalf("eski kapak silinmeli: %s", s)
	}
	// The cover attached to this tayfa cannot also cover another one.
	if _, err := e.svc.Update(ctx, admin, other, UpdateInput{Fields: []string{FieldCover}, CoverUploadID: second}); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("başka tayfa: %v", err)
	}
	v, err = set(admin, id.ID{})
	must(t, err)
	if v.CoverUploadID != (id.ID{}) || mediatest.Status(t, e.pool, second) != "deleting" {
		t.Fatal("kapak kaldırılmalı ve silinmeli")
	}
}
