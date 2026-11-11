package grpc

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/account"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func TestAvatar(t *testing.T) {
	f := &fakeAccounts{}
	srv := newAccountServer(f)
	u, upload := id.New(), id.New()
	ctx := fullCtx(u)
	if _, err := srv.SetAvatar(callerCtx(u), &identityv1.SetAvatarRequest{UploadId: upload.String()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("kayıt token'ı: %v", err)
	}
	r, err := srv.SetAvatar(ctx, &identityv1.SetAvatarRequest{UploadId: upload.String()})
	if err != nil || f.caller != u || f.avatar != upload || r.GetAvatarUploadId() != upload.String() {
		t.Fatalf("avatar: %v %v", r, err)
	}
	if _, err := srv.SetAvatar(ctx, &identityv1.SetAvatarRequest{UploadId: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bozuk kimlik: %v", err)
	}
	if _, err := srv.ClearAvatar(ctx, &identityv1.ClearAvatarRequest{}); err != nil || f.avatar != (id.ID{}) {
		t.Fatalf("kaldırma: %v", err)
	}
	for appErr, want := range map[error]codes.Code{
		account.ErrUploadNotFound:   codes.NotFound,
		account.ErrUploadNotReady:   codes.FailedPrecondition,
		account.ErrUploadInUse:      codes.FailedPrecondition,
		account.ErrMediaUnavailable: codes.Unavailable,
	} {
		f.err = appErr
		if _, err := srv.SetAvatar(ctx, &identityv1.SetAvatarRequest{UploadId: upload.String()}); status.Code(err) != want {
			t.Errorf("%v: %v", appErr, err)
		}
	}
}
