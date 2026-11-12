package grpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/integrity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/session"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeSessions struct {
	err       error
	gotIn     session.SignInInput
	gotProof  integrity.Proof
	signedOut id.ID
	checkAt   time.Time
}

func (f *fakeSessions) SignIn(_ context.Context, in session.SignInInput) (session.Tokens, error) {
	f.gotIn = in
	return session.Tokens{UserID: id.New(), SessionID: id.New(), AccessToken: "a", RefreshToken: "r", AccessExpiresAt: time.Unix(1, 0), RegistrationPending: true, IntegrityCheckAfter: f.checkAt}, f.err
}
func (f *fakeSessions) RefreshWithProof(_ context.Context, _ string, p integrity.Proof) (session.Tokens, error) {
	f.gotProof = p
	return session.Tokens{IntegrityCheckAfter: f.checkAt}, f.err
}
func (f *fakeSessions) SignOut(_ context.Context, sid id.ID) error { f.signedOut = sid; return f.err }

func newServer(f *fakeSessions) *AuthServer {
	return NewAuthServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func reasonOf(t *testing.T, err error) (codes.Code, string) {
	t.Helper()
	st := status.Convert(err)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			if info.GetDomain() != errorDomain {
				t.Fatalf("domain = %q", info.GetDomain())
			}
			return st.Code(), info.GetReason()
		}
	}
	return st.Code(), ""
}

func TestSignInMapsRequest(t *testing.T) {
	f := &fakeSessions{}
	resp, err := newServer(f).SignIn(context.Background(), &identityv1.SignInRequest{
		Provider: identityv1.Provider_PROVIDER_GOOGLE, IdToken: "tok", Nonce: "n", Platform: identityv1.Platform_PLATFORM_IOS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.gotIn != (session.SignInInput{Provider: "google", IDToken: "tok", Nonce: "n", Platform: "ios"}) {
		t.Fatalf("girdi = %+v", f.gotIn)
	}
	if !resp.GetTokens().GetRegistrationPending() || resp.GetTokens().GetAccessToken() != "a" {
		t.Fatalf("yanıt = %v", resp)
	}
}

func TestSignInInputLimits(t *testing.T) {
	srv := newServer(&fakeSessions{})
	for name, req := range map[string]*identityv1.SignInRequest{
		"sağlayıcı yok": {IdToken: "t", Nonce: "n", Platform: identityv1.Platform_PLATFORM_WEB},
		"platform yok":  {Provider: identityv1.Provider_PROVIDER_GOOGLE, IdToken: "t", Nonce: "n"},
		"dev token":     {Provider: identityv1.Provider_PROVIDER_GOOGLE, IdToken: string(make([]byte, maxIDToken+1)), Nonce: "n", Platform: identityv1.Platform_PLATFORM_WEB},
	} {
		if _, err := srv.SignIn(context.Background(), req); status.Code(err) == codes.OK {
			t.Errorf("%s: kabul edildi", name)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason identityv1.ErrorReason
	}{
		{session.ErrInvalidCredentials, codes.Unauthenticated, identityv1.ErrorReason_ERROR_REASON_INVALID_CREDENTIALS},
		{session.ErrUnsupportedProvider, codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_UNSUPPORTED_PROVIDER},
		{session.ErrAgeBlocked, codes.PermissionDenied, identityv1.ErrorReason_ERROR_REASON_AGE_BLOCKED},
		{session.ErrInvalidRefresh, codes.Unauthenticated, identityv1.ErrorReason_ERROR_REASON_INVALID_REFRESH},
		{session.ErrRefreshConflict, codes.Aborted, identityv1.ErrorReason_ERROR_REASON_REFRESH_CONFLICT},
		{session.ErrUnavailable, codes.Unavailable, identityv1.ErrorReason_ERROR_REASON_UNSPECIFIED},
		{errors.New("veritabanı patladı: ayrıntı"), codes.Internal, identityv1.ErrorReason_ERROR_REASON_UNSPECIFIED},
	} {
		_, err := newServer(&fakeSessions{err: tc.err}).Refresh(context.Background(), &identityv1.RefreshRequest{RefreshToken: "x"})
		code, reason := reasonOf(t, err)
		wantReason := tc.reason.String()
		if tc.reason == identityv1.ErrorReason_ERROR_REASON_UNSPECIFIED {
			wantReason = ""
		}
		if code != tc.code || reason != wantReason {
			t.Errorf("%v: kod=%v neden=%q", tc.err, code, reason)
		}
		if msg := status.Convert(err).Message(); strings.Contains(msg, "veritabanı") || strings.Contains(msg, "ayrıntı") {
			t.Errorf("%v: iç ayrıntı istemciye sızdı: %q", tc.err, msg)
		}
	}
}

func TestSignOutRequiresCaller(t *testing.T) {
	f := &fakeSessions{}
	if _, err := newServer(f).SignOut(context.Background(), &identityv1.SignOutRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız çıkış: %v", err)
	}
	if f.signedOut != (id.ID{}) {
		t.Fatal("token'sız istek bir oturumu kapattı")
	}
}

// TestIntegrityPassesThrough: the proof reaches the session layer as is and
// the next check time comes back; an oversized token is refused before
// (ADR-0022).
func TestIntegrityPassesThrough(t *testing.T) {
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	f := &fakeSessions{checkAt: at}
	srv := newServer(f)
	ctx := context.Background()
	in, err := srv.SignIn(ctx, &identityv1.SignInRequest{
		Provider: identityv1.Provider_PROVIDER_GOOGLE, IdToken: "tok", Nonce: "n", Platform: identityv1.Platform_PLATFORM_ANDROID,
		IntegrityToken: "play-token",
	})
	if err != nil || f.gotIn.Integrity.Android != "play-token" || !in.GetTokens().GetIntegrityCheckAfter().AsTime().Equal(at) {
		t.Fatalf("giriş: %v %+v %v", in, f.gotIn.Integrity, err)
	}
	r, err := srv.Refresh(ctx, &identityv1.RefreshRequest{RefreshToken: "x", IntegrityToken: "play-token-2"})
	if err != nil || f.gotProof.Android != "play-token-2" || !r.GetTokens().GetIntegrityCheckAfter().AsTime().Equal(at) {
		t.Fatalf("yenileme: %v %+v %v", r, f.gotProof, err)
	}
	long := string(make([]byte, integrity.MaxPlayToken+1))
	if _, err := srv.Refresh(ctx, &identityv1.RefreshRequest{RefreshToken: "x", IntegrityToken: long}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("uzun token: %v", err)
	}
	// With checks off there is no next check time.
	f.checkAt = time.Time{}
	r, _ = srv.Refresh(ctx, &identityv1.RefreshRequest{RefreshToken: "x"})
	if r.GetTokens().GetIntegrityCheckAfter() != nil {
		t.Fatal("kapalıyken zaman döndü")
	}
}
