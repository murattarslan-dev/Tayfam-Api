package grpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/consent"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/deletion"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/session"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeManage struct {
	err     error
	called  int
	caller  session.Caller
	target  id.ID
	user    id.ID
	del     deletion.Caller
	pn, tos string
	list    []session.Info
}

func (f *fakeManage) List(_ context.Context, c session.Caller) ([]session.Info, error) {
	f.called++
	f.caller = c
	return f.list, f.err
}
func (f *fakeManage) Revoke(_ context.Context, c session.Caller, target id.ID) error {
	f.called++
	f.caller, f.target = c, target
	return f.err
}
func (f *fakeManage) RevokeOthers(_ context.Context, c session.Caller) (int, error) {
	f.called++
	f.caller = c
	return 3, f.err
}
func (f *fakeManage) Status(_ context.Context, u id.ID) (consent.Status, error) {
	f.called++
	f.user = u
	return consent.Status{Documents: consent.Documents{PrivacyNotice: "2026-12-01", Terms: "2026-11-08"}, Required: true}, f.err
}
func (f *fakeManage) Accept(_ context.Context, u id.ID, pn, tos string) error {
	f.called++
	f.user, f.pn, f.tos = u, pn, tos
	return f.err
}
func (f *fakeManage) Delete(_ context.Context, c deletion.Caller) error {
	f.called++
	f.del = c
	return f.err
}

func newManageServer(f *fakeManage) *AccountServer {
	return NewAccountServer(AccountDeps{
		Accounts: &fakeAccounts{}, Sessions: f, Consents: f, Deletion: f,
		APIAudience: testAudience, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// manageCalls calls every account management method once.
func manageCalls(srv *AccountServer, ctx context.Context) map[string]error {
	calls := map[string]error{}
	_, calls["list"] = srv.ListSessions(ctx, &identityv1.ListSessionsRequest{})
	_, calls["revoke"] = srv.RevokeSession(ctx, &identityv1.RevokeSessionRequest{SessionId: id.New().String()})
	_, calls["revoke-others"] = srv.RevokeOtherSessions(ctx, &identityv1.RevokeOtherSessionsRequest{})
	_, calls["consents"] = srv.GetConsents(ctx, &identityv1.GetConsentsRequest{})
	_, calls["accept"] = srv.AcceptConsents(ctx, &identityv1.AcceptConsentsRequest{PrivacyNoticeVersion: "2026-12-01", TermsVersion: "2026-11-08"})
	_, calls["delete"] = srv.DeleteAccount(ctx, &identityv1.DeleteAccountRequest{})
	return calls
}

// TestManageNeedsFullToken: a registration-only token, another audience or
// a token without a session reaches none of the methods.
func TestManageNeedsFullToken(t *testing.T) {
	f := &fakeManage{}
	srv := newManageServer(f)
	u := id.New()
	for name, ctx := range map[string]context.Context{
		"token yok":      context.Background(),
		"kayıt token'ı":  callerCtx(u),
		"başka audience": auth.WithCallerForTest(context.Background(), auth.Claims{Subject: u.String(), SessionID: id.New().String(), Audience: "core.tayfam.internal"}),
		"oturum yok":     auth.WithCallerForTest(context.Background(), auth.Claims{Subject: u.String(), Audience: testAudience}),
	} {
		for m, err := range manageCalls(srv, ctx) {
			if status.Code(err) != codes.Unauthenticated {
				t.Errorf("%s/%s: %v", name, m, err)
			}
		}
	}
	if f.called != 0 {
		t.Fatal("uygulama katmanı çağrıldı")
	}
}

func TestManageUsesVerifiedToken(t *testing.T) {
	f := &fakeManage{}
	srv := newManageServer(f)
	u, sid := id.New(), id.New()
	iat := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	ctx := auth.WithCallerForTest(context.Background(), auth.Claims{Subject: u.String(), SessionID: sid.String(), Audience: testAudience, IssuedAt: iat})

	if _, err := srv.DeleteAccount(ctx, &identityv1.DeleteAccountRequest{}); err != nil {
		t.Fatal(err)
	}
	if f.del != (deletion.Caller{User: u, Session: sid, IssuedAt: iat}) {
		t.Fatalf("silme çağıranı = %+v", f.del)
	}

	other := id.New()
	f.list = []session.Info{{ID: sid, Platform: "ios", CreatedAt: iat, LastSeenAt: iat, Current: true}, {ID: other, Platform: "web", CreatedAt: iat, LastSeenAt: iat}}
	l, err := srv.ListSessions(ctx, &identityv1.ListSessionsRequest{})
	if err != nil || len(l.GetSessions()) != 2 || f.caller != (session.Caller{User: u, Session: sid}) {
		t.Fatalf("liste = %v %v", l, err)
	}
	if s := l.GetSessions()[0]; s.GetSessionId() != sid.String() || !s.GetCurrent() || s.GetPlatform() != identityv1.Platform_PLATFORM_IOS || !s.GetLastSeenTime().AsTime().Equal(iat) {
		t.Fatalf("oturum = %v", s)
	}

	if _, err := srv.RevokeSession(ctx, &identityv1.RevokeSessionRequest{SessionId: other.String()}); err != nil || f.target != other {
		t.Fatalf("iptal: %v %v", f.target, err)
	}
	f.called = 0
	for _, bad := range []string{"", "x", string(make([]byte, 100))} {
		_, err := srv.RevokeSession(ctx, &identityv1.RevokeSessionRequest{SessionId: bad})
		if status.Code(err) != codes.NotFound {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if f.called != 0 {
		t.Fatal("geçersiz oturum kimliği uygulamaya ulaştı")
	}
	r, err := srv.RevokeOtherSessions(ctx, &identityv1.RevokeOtherSessionsRequest{})
	if err != nil || r.GetRevokedCount() != 3 {
		t.Fatalf("diğerleri: %v %v", r, err)
	}

	c, err := srv.GetConsents(ctx, &identityv1.GetConsentsRequest{})
	if err != nil || !c.GetConsentRequired() || c.GetPrivacyNoticeVersion() != "2026-12-01" || f.user != u {
		t.Fatalf("onay durumu = %v %v", c, err)
	}
	if _, err := srv.AcceptConsents(ctx, &identityv1.AcceptConsentsRequest{PrivacyNoticeVersion: "2026-12-01", TermsVersion: "2026-11-08"}); err != nil || f.pn != "2026-12-01" || f.tos != "2026-11-08" {
		t.Fatalf("onay: %v", err)
	}
	if _, err := srv.AcceptConsents(ctx, &identityv1.AcceptConsentsRequest{PrivacyNoticeVersion: string(make([]byte, 33))}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("uzun sürüm: %v", err)
	}
}

func TestManageErrorMapping(t *testing.T) {
	ctx := fullCtx(id.New())
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{deletion.ErrReauthenticationRequired, codes.PermissionDenied, "ERROR_REASON_REAUTHENTICATION_REQUIRED"},
		{session.ErrSessionNotFound, codes.NotFound, "ERROR_REASON_SESSION_NOT_FOUND"},
		{consent.ErrOutdated, codes.FailedPrecondition, "ERROR_REASON_CONSENT_OUTDATED"},
		{session.ErrCallerSession, codes.Unauthenticated, ""},
		{deletion.ErrUnknownUser, codes.Unauthenticated, ""},
		{consent.ErrUnknownUser, codes.Unauthenticated, ""},
		{errors.New("veritabanı ayrıntısı"), codes.Internal, ""},
	} {
		for m, err := range manageCalls(newManageServer(&fakeManage{err: tc.err}), ctx) {
			st := status.Convert(err)
			reason := ""
			for _, d := range st.Details() {
				if info, ok := d.(*errdetails.ErrorInfo); ok {
					reason = info.GetReason()
				}
			}
			if st.Code() != tc.code || reason != tc.reason {
				t.Errorf("%s %v: %v %q", m, tc.err, st.Code(), reason)
			}
			if st.Message() == tc.err.Error() {
				t.Errorf("%s %v: iç ayrıntı sızdı", m, tc.err)
			}
		}
	}
}

// TestNoNewPublicMethods: account management is never callable without a
// token.
func TestNoNewPublicMethods(t *testing.T) {
	for _, m := range PublicMethods {
		for _, managed := range []string{
			identityv1.AccountService_ListSessions_FullMethodName,
			identityv1.AccountService_RevokeSession_FullMethodName,
			identityv1.AccountService_RevokeOtherSessions_FullMethodName,
			identityv1.AccountService_GetConsents_FullMethodName,
			identityv1.AccountService_AcceptConsents_FullMethodName,
			identityv1.AccountService_DeleteAccount_FullMethodName,
			identityv1.AdminAuthService_GetAdmin_FullMethodName,
			identityv1.ProfileService_GetProfileDetails_FullMethodName,
			identityv1.AccountService_UpdateAccount_FullMethodName,
		} {
			if m == managed {
				t.Fatalf("%s açık metod", m)
			}
		}
	}
}
