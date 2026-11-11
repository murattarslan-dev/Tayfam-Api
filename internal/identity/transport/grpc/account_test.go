package grpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/account"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

const testAudience = "tayfam-api"

type fakeAccounts struct {
	err    error
	caller id.ID
	code   string
	vis    account.Visibility
	found  account.Found
	avatar id.ID
}

func (f *fakeAccounts) Get(_ context.Context, c id.ID) (account.Account, error) {
	f.caller = c
	return account.Account{ReferralCode: "TYF-8K2M4Q", ActivityVisibility: account.VisibilityPublic}, f.err
}
func (f *fakeAccounts) RotateReferralCode(_ context.Context, c id.ID) (string, error) {
	f.caller = c
	return "TYF-2222AB", f.err
}
func (f *fakeAccounts) FindByReferralCode(_ context.Context, c id.ID, code string) (account.Found, error) {
	f.caller, f.code = c, code
	return f.found, f.err
}
func (f *fakeAccounts) SetActivityVisibility(_ context.Context, c id.ID, v account.Visibility) (account.Visibility, error) {
	f.caller, f.vis = c, v
	return v, f.err
}

func (f *fakeAccounts) SetAvatar(_ context.Context, c, u id.ID) error {
	f.caller, f.avatar = c, u
	return f.err
}

func (f *fakeAccounts) ClearAvatar(_ context.Context, c id.ID) error {
	f.caller, f.avatar = c, id.ID{}
	return f.err
}

func newAccountServer(f *fakeAccounts) *AccountServer {
	return NewAccountServer(AccountDeps{Accounts: f, APIAudience: testAudience, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func fullCtx(u id.ID) context.Context {
	return auth.WithCallerForTest(context.Background(), auth.Claims{Subject: u.String(), SessionID: id.New().String(), Audience: testAudience})
}

// TestAccountNeedsFullToken: identity's verifier also accepts
// registration-only tokens; none of the account methods does.
func TestAccountNeedsFullToken(t *testing.T) {
	f := &fakeAccounts{}
	srv := newAccountServer(f)
	u := id.New()
	for name, ctx := range map[string]context.Context{
		"token yok":      context.Background(),
		"kayıt token'ı":  callerCtx(u),
		"başka audience": auth.WithCallerForTest(context.Background(), auth.Claims{Subject: u.String(), Audience: "core.tayfam.internal"}),
		"bozuk kimlik":   auth.WithCallerForTest(context.Background(), auth.Claims{Subject: "x", Audience: testAudience}),
	} {
		calls := map[string]error{}
		_, calls["get"] = srv.GetAccount(ctx, &identityv1.GetAccountRequest{})
		_, calls["rotate"] = srv.RotateReferralCode(ctx, &identityv1.RotateReferralCodeRequest{})
		_, calls["find"] = srv.FindUserByReferralCode(ctx, &identityv1.FindUserByReferralCodeRequest{ReferralCode: "TYF-8K2M4Q"})
		_, calls["set"] = srv.SetActivityVisibility(ctx, &identityv1.SetActivityVisibilityRequest{ActivityVisibility: identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC})
		for m, err := range calls {
			if status.Code(err) != codes.Unauthenticated {
				t.Errorf("%s/%s: %v", name, m, err)
			}
		}
	}
	if f.caller != (id.ID{}) {
		t.Fatal("uygulama katmanı çağrıldı")
	}
}

func TestAccountMethods(t *testing.T) {
	avatar := id.New()
	f := &fakeAccounts{found: account.Found{UserID: id.New(), DisplayName: "Ece", AvatarUploadID: avatar}}
	srv := newAccountServer(f)
	u := id.New()
	ctx := fullCtx(u)

	a, err := srv.GetAccount(ctx, &identityv1.GetAccountRequest{})
	if err != nil || a.GetReferralCode() != "TYF-8K2M4Q" || a.GetActivityVisibility() != identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC || f.caller != u {
		t.Fatalf("hesap = %v %v", a, err)
	}
	r, err := srv.FindUserByReferralCode(ctx, &identityv1.FindUserByReferralCodeRequest{ReferralCode: "8k2m4q"})
	if err != nil || r.GetUserId() != f.found.UserID.String() || r.GetAvatarUploadId() != avatar.String() || f.code != "8k2m4q" {
		t.Fatalf("bulunan = %v %v", r, err)
	}
	if _, err := srv.FindUserByReferralCode(ctx, &identityv1.FindUserByReferralCodeRequest{ReferralCode: string(make([]byte, 33))}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("uzun kod: %v", err)
	}
	v, err := srv.SetActivityVisibility(ctx, &identityv1.SetActivityVisibilityRequest{ActivityVisibility: identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE})
	if err != nil || f.vis != account.VisibilityPrivate || v.GetActivityVisibility() != identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE {
		t.Fatalf("görünürlük = %v %v", v, err)
	}
	if _, err := srv.SetActivityVisibility(ctx, &identityv1.SetActivityVisibilityRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("belirtilmemiş görünürlük: %v", err)
	}
}

func TestAccountErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{account.ErrRateLimited, codes.ResourceExhausted, "ERROR_REASON_RATE_LIMITED"},
		{account.ErrCodeNotFound, codes.NotFound, "ERROR_REASON_REFERRAL_CODE_NOT_FOUND"},
		{account.ErrUnknownUser, codes.Unauthenticated, ""},
		{errors.New("veritabanı ayrıntısı"), codes.Internal, ""},
	} {
		srv := newAccountServer(&fakeAccounts{err: tc.err})
		_, err := srv.FindUserByReferralCode(fullCtx(id.New()), &identityv1.FindUserByReferralCodeRequest{ReferralCode: "TYF-8K2M4Q"})
		st := status.Convert(err)
		reason := ""
		for _, d := range st.Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok {
				reason = info.GetReason()
			}
		}
		if st.Code() != tc.code || reason != tc.reason {
			t.Errorf("%v: %v %q", tc.err, st.Code(), reason)
		}
		if st.Message() == tc.err.Error() {
			t.Errorf("%v: iç ayrıntı sızdı", tc.err)
		}
	}
}
