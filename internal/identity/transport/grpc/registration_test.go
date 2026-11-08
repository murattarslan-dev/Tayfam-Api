package grpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/registration"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeRegistrar struct {
	err  error
	user id.ID
	in   registration.Input
}

func (f *fakeRegistrar) Requirements() registration.Requirements {
	return registration.Requirements{Documents: registration.Documents{PrivacyNotice: "2026-11-08", Terms: "2026-11-08"}, MinimumAge: 16}
}
func (f *fakeRegistrar) Complete(_ context.Context, u id.ID, in registration.Input) error {
	f.user, f.in = u, in
	return f.err
}

func callerCtx(u id.ID) context.Context {
	return auth.WithCallerForTest(context.Background(), auth.Claims{Subject: u.String(), SessionID: id.New().String(), Audience: "tayfam-registration"})
}

func TestCompleteRegistrationUsesTokenUser(t *testing.T) {
	f := &fakeRegistrar{}
	srv := NewRegistrationServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	u := id.New()
	_, err := srv.CompleteRegistration(callerCtx(u), &identityv1.CompleteRegistrationRequest{BirthDate: "1990-05-19", AcceptedPrivacyNoticeVersion: "v", AcceptedTermsVersion: "v"})
	if err != nil || f.user != u || f.in.BirthDate != "1990-05-19" {
		t.Fatalf("user=%v in=%+v err=%v", f.user, f.in, err)
	}
	if _, err := srv.CompleteRegistration(context.Background(), &identityv1.CompleteRegistrationRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	if _, err := srv.GetRequirements(context.Background(), &identityv1.GetRequirementsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız gereksinim: %v", err)
	}
	r, err := srv.GetRequirements(callerCtx(u), &identityv1.GetRequirementsRequest{})
	if err != nil || r.GetMinimumAge() != 16 || r.GetTermsVersion() == "" {
		t.Fatalf("gereksinimler = %v %v", r, err)
	}
}

func TestRegistrationErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason identityv1.ErrorReason
	}{
		{registration.ErrInvalidBirthDate, codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_INVALID_BIRTH_DATE},
		{registration.ErrUnderAge, codes.PermissionDenied, identityv1.ErrorReason_ERROR_REASON_AGE_BLOCKED},
		{registration.ErrAgeBlocked, codes.PermissionDenied, identityv1.ErrorReason_ERROR_REASON_AGE_BLOCKED},
		{registration.ErrConsentOutdated, codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_CONSENT_OUTDATED},
		{registration.ErrAlreadyRegistered, codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_ALREADY_REGISTERED},
		{registration.ErrInvalidDisplayName, codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_INVALID_DISPLAY_NAME},
	} {
		srv := NewRegistrationServer(&fakeRegistrar{err: tc.err}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		_, err := srv.CompleteRegistration(callerCtx(id.New()), &identityv1.CompleteRegistrationRequest{BirthDate: "1990-01-01"})
		code, reason := reasonOf(t, err)
		if code != tc.code || reason != tc.reason.String() {
			t.Errorf("%v: %v %q", tc.err, code, reason)
		}
	}
	srv := NewRegistrationServer(&fakeRegistrar{err: errors.New("iç ayrıntı")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := srv.CompleteRegistration(callerCtx(id.New()), &identityv1.CompleteRegistrationRequest{})
	if status.Code(err) != codes.Internal || status.Convert(err).Message() != "iç hata" {
		t.Fatalf("iç hata: %v", err)
	}
	_, err = srv.CompleteRegistration(callerCtx(id.New()), &identityv1.CompleteRegistrationRequest{BirthDate: "1990-01-01-uzun"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("uzun tarih: %v", err)
	}
}
