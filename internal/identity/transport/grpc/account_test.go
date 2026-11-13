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
	update *account.Changes
}

func (f *fakeAccounts) Update(_ context.Context, c id.ID, ch account.Changes) error {
	f.caller, f.update = c, &ch
	return f.err
}

func (f *fakeAccounts) SnoozeProfilePrompt(_ context.Context, c id.ID) error {
	f.caller = c
	return f.err
}

func (f *fakeAccounts) Get(_ context.Context, c id.ID) (account.Account, error) {
	f.caller = c
	return account.Account{UserID: c, DisplayName: "Deniz", ReferralCode: "TYF-8K2M4Q", ActivityVisibility: account.VisibilityPublic, BirthdayVisibility: account.BirthdayFull}, f.err
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
		_, calls["update"] = srv.UpdateAccount(ctx, &identityv1.UpdateAccountRequest{DisplayName: ptr("Ece")})
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
	if err != nil || a.GetReferralCode() != "TYF-8K2M4Q" || a.GetActivityVisibility() != identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC || f.caller != u ||
		a.GetUserId() != u.String() || a.GetDisplayName() != "Deniz" || a.GetBirthdayVisibility() != identityv1.BirthdayVisibility_BIRTHDAY_VISIBILITY_FULL {
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

func ptr[T any](v T) *T { return &v }

func TestUpdateAccount(t *testing.T) {
	u := id.New()
	ctx := fullCtx(u)

	f := &fakeAccounts{}
	resp, err := newAccountServer(f).UpdateAccount(ctx, &identityv1.UpdateAccountRequest{
		DisplayName: ptr(" Ece  Su "), ActivityVisibility: identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE,
	})
	if err != nil || f.caller != u || f.update == nil || *f.update.DisplayName != " Ece  Su " ||
		*f.update.ActivityVisibility != account.VisibilityPrivate || resp.GetAccount().GetUserId() != u.String() {
		t.Fatalf("güncelleme = %v %v %+v", resp, err, f.update)
	}

	f = &fakeAccounts{}
	if _, err := newAccountServer(f).UpdateAccount(ctx, &identityv1.UpdateAccountRequest{
		BirthdayVisibility: identityv1.BirthdayVisibility_BIRTHDAY_VISIBILITY_HIDDEN,
	}); err != nil || *f.update.BirthdayVisibility != account.BirthdayHidden || f.update.ActivityVisibility != nil {
		t.Fatalf("doğum günü görünürlüğü = %v %+v", err, f.update)
	}

	// Unset fields stay unset: nothing changes but the account is returned.
	f = &fakeAccounts{}
	if _, err := newAccountServer(f).UpdateAccount(ctx, &identityv1.UpdateAccountRequest{}); err != nil ||
		f.update.DisplayName != nil || f.update.ActivityVisibility != nil {
		t.Fatalf("boş güncelleme = %v %+v", err, f.update)
	}

	// An over-long name never reaches the app layer.
	f = &fakeAccounts{}
	_, err = newAccountServer(f).UpdateAccount(ctx, &identityv1.UpdateAccountRequest{DisplayName: ptr(string(make([]byte, 513)))})
	if status.Code(err) != codes.InvalidArgument || f.update != nil {
		t.Fatalf("uzun ad: %v", err)
	}
	_, err = newAccountServer(f).UpdateAccount(ctx, &identityv1.UpdateAccountRequest{ActivityVisibility: identityv1.ActivityVisibility(99)})
	if status.Code(err) != codes.InvalidArgument || f.update != nil {
		t.Fatalf("bilinmeyen görünürlük: %v", err)
	}
	_, err = newAccountServer(f).UpdateAccount(ctx, &identityv1.UpdateAccountRequest{BirthdayVisibility: identityv1.BirthdayVisibility(99)})
	if status.Code(err) != codes.InvalidArgument || f.update != nil {
		t.Fatalf("bilinmeyen doğum günü görünürlüğü: %v", err)
	}

	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{account.ErrInvalidDisplayName, codes.InvalidArgument, "ERROR_REASON_INVALID_DISPLAY_NAME"},
		{account.ErrRateLimited, codes.ResourceExhausted, "ERROR_REASON_RATE_LIMITED"},
	} {
		_, err := newAccountServer(&fakeAccounts{err: tc.err}).UpdateAccount(ctx, &identityv1.UpdateAccountRequest{DisplayName: ptr("Ece")})
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
	}
}

// TestUpdateProfileFields (issue #186): enums become the stored names,
// unknown values and oversized input never reach the app layer.
func TestUpdateProfileFields(t *testing.T) {
	u := id.New()
	ctx := fullCtx(u)
	f := &fakeAccounts{}
	_, err := newAccountServer(f).UpdateAccount(ctx, &identityv1.UpdateAccountRequest{
		AboutVisibility: identityv1.AboutVisibility_ABOUT_VISIBILITY_FRIENDS,
		Profile: &identityv1.ProfileUpdate{
			Interests:      &identityv1.InterestList{Values: []identityv1.ProfileInterest{identityv1.ProfileInterest_PROFILE_INTEREST_HIKE}},
			Team:           ptr("Bursaspor"),
			Languages:      &identityv1.LanguageList{},
			SocialAccounts: &identityv1.SocialAccountList{Values: []*identityv1.SocialAccount{{Platform: identityv1.SocialPlatform_SOCIAL_PLATFORM_X, Username: "ece"}}},
			Declined:       &identityv1.ProfileFieldList{Values: []identityv1.ProfileField{identityv1.ProfileField_PROFILE_FIELD_ABOUT}},
		},
	})
	p := f.update.Profile
	if err != nil || *f.update.AboutVisibility != "friends" || (*p.Interests)[0] != "hike" || *p.Team != "Bursaspor" ||
		p.About != nil || len(*p.Languages) != 0 || (*p.SocialAccounts)[0].Platform != "x" || (*p.Declined)[0] != "about" {
		t.Fatalf("güncelleme = %v %+v", err, f.update)
	}
	for name, req := range map[string]*identityv1.UpdateAccountRequest{
		"bilinmeyen ilgi":       {Profile: &identityv1.ProfileUpdate{Interests: &identityv1.InterestList{Values: []identityv1.ProfileInterest{99}}}},
		"belirsiz platform":     {Profile: &identityv1.ProfileUpdate{SocialAccounts: &identityv1.SocialAccountList{Values: []*identityv1.SocialAccount{{Username: "ece"}}}}},
		"bilinmeyen alan":       {Profile: &identityv1.ProfileUpdate{Declined: &identityv1.ProfileFieldList{Values: []identityv1.ProfileField{0}}}},
		"bilinmeyen görünürlük": {AboutVisibility: 99},
		"çok uzun metin":        {Profile: &identityv1.ProfileUpdate{About: ptr(string(make([]byte, 4097)))}},
	} {
		f := &fakeAccounts{}
		if _, err := newAccountServer(f).UpdateAccount(ctx, req); status.Code(err) != codes.InvalidArgument || f.update != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err = newAccountServer(&fakeAccounts{err: account.ErrInvalidProfile}).UpdateAccount(ctx, &identityv1.UpdateAccountRequest{Profile: &identityv1.ProfileUpdate{Team: ptr("x")}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("geçersiz alan: %v", err)
	}
	f = &fakeAccounts{}
	if _, err := newAccountServer(f).SnoozeProfilePrompt(ctx, &identityv1.SnoozeProfilePromptRequest{}); err != nil || f.caller != u {
		t.Fatalf("erteleme: %v", err)
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
