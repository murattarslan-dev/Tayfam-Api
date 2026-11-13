package grpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/profile"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeProfiles struct {
	d            profile.Details
	err          error
	viewer, user id.ID
}

func (f *fakeProfiles) Get(_ context.Context, viewer, user id.ID) (profile.Details, error) {
	f.viewer, f.user = viewer, user
	return f.d, f.err
}

func newProfileServer(f *fakeProfiles) *ProfileServer {
	return NewProfileServer(f, testAudience, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestGetProfileDetails(t *testing.T) {
	viewer, user := id.New(), id.New()
	f := &fakeProfiles{d: profile.Details{Birthday: &profile.Birthday{Month: time.March, Day: 4, Year: 2008, Age: 18}}}
	resp, err := newProfileServer(f).GetProfileDetails(fullCtx(viewer), &identityv1.GetProfileDetailsRequest{UserId: user.String()})
	b := resp.GetBirthday()
	if err != nil || f.viewer != viewer || f.user != user || b.GetMonth() != 3 || b.GetDay() != 4 || b.GetYear() != 2008 || b.GetAge() != 18 {
		t.Fatalf("ayrıntılar = %v %v", resp, err)
	}
	f = &fakeProfiles{}
	if resp, err := newProfileServer(f).GetProfileDetails(fullCtx(viewer), &identityv1.GetProfileDetailsRequest{UserId: user.String()}); err != nil || resp.Birthday != nil {
		t.Fatalf("gizli = %v %v", resp, err)
	}
	for name, tc := range map[string]struct {
		err  error
		code codes.Code
	}{
		"bulunamadı": {profile.ErrUserNotFound, codes.NotFound},
		"core yok":   {profile.ErrUnavailable, codes.Unavailable},
		"iç hata":    {errors.New("x"), codes.Internal},
	} {
		_, err := newProfileServer(&fakeProfiles{err: tc.err}).GetProfileDetails(fullCtx(viewer), &identityv1.GetProfileDetailsRequest{UserId: user.String()})
		if status.Code(err) != tc.code {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := newProfileServer(&fakeProfiles{}).GetProfileDetails(fullCtx(viewer), &identityv1.GetProfileDetailsRequest{UserId: "x"}); status.Code(err) != codes.NotFound {
		t.Fatalf("bozuk kimlik: %v", err)
	}
}

// TestProfileNeedsFullToken: a registration token, another audience or no
// token never reaches the app layer.
func TestProfileNeedsFullToken(t *testing.T) {
	u := id.New()
	f := &fakeProfiles{}
	for name, ctx := range map[string]context.Context{
		"token yok":      context.Background(),
		"kayıt token'ı":  callerCtx(u),
		"başka audience": auth.WithCallerForTest(context.Background(), auth.Claims{Subject: u.String(), Audience: "core.tayfam.internal"}),
		"bozuk kimlik":   auth.WithCallerForTest(context.Background(), auth.Claims{Subject: "x", Audience: testAudience}),
	} {
		if _, err := newProfileServer(f).GetProfileDetails(ctx, &identityv1.GetProfileDetailsRequest{UserId: id.New().String()}); status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.viewer != (id.ID{}) {
		t.Fatal("uygulama katmanı çağrıldı")
	}
}
