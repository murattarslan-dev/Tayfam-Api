package grpc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/platformadmin"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/session"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeAdmins struct {
	err    error
	gotIn  platformadmin.SignInInput
	gotFor auth.AdminClaims
	user   id.ID
}

func (f *fakeAdmins) SignIn(_ context.Context, in platformadmin.SignInInput) (platformadmin.Token, error) {
	f.gotIn = in
	if f.err != nil {
		return platformadmin.Token{}, f.err
	}
	return platformadmin.Token{UserID: f.user, Token: "admin-token", ExpiresAt: time.Unix(100, 0), AuthTime: time.Unix(10, 0)}, nil
}

func (f *fakeAdmins) Get(_ context.Context, c auth.AdminClaims) (platformadmin.Admin, error) {
	f.gotFor = c
	if f.err != nil {
		return platformadmin.Admin{}, f.err
	}
	return platformadmin.Admin{UserID: f.user, DisplayName: "Yönetici", AuthTime: c.AuthTime, ExpiresAt: c.ExpiresAt}, nil
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func validAdminSignIn() *identityv1.AdminSignInRequest {
	return &identityv1.AdminSignInRequest{IdToken: "tok", Nonce: strings.Repeat("n", 32), AccessEmail: "yonetici@example.com"}
}

func TestAdminSignInMapsRequest(t *testing.T) {
	f := &fakeAdmins{user: id.New()}
	resp, err := NewAdminAuthServer(f, discard()).AdminSignIn(context.Background(), validAdminSignIn())
	if err != nil {
		t.Fatal(err)
	}
	if f.gotIn != (platformadmin.SignInInput{IDToken: "tok", Nonce: strings.Repeat("n", 32), AccessEmail: "yonetici@example.com"}) {
		t.Fatalf("girdi = %+v", f.gotIn)
	}
	if resp.GetAdminToken() != "admin-token" || resp.GetUserId() != f.user.String() || resp.GetExpireTime().AsTime().Unix() != 100 {
		t.Fatalf("yanıt = %v", resp)
	}
}

func TestAdminSignInInputLimits(t *testing.T) {
	f := &fakeAdmins{}
	srv := NewAdminAuthServer(f, discard())
	for name, mod := range map[string]func(*identityv1.AdminSignInRequest){
		"token yok":        func(r *identityv1.AdminSignInRequest) { r.IdToken = "" },
		"dev token":        func(r *identityv1.AdminSignInRequest) { r.IdToken = strings.Repeat("a", maxIDToken+1) },
		"kısa nonce":       func(r *identityv1.AdminSignInRequest) { r.Nonce = "kisa" },
		"Access kimliksiz": func(r *identityv1.AdminSignInRequest) { r.AccessEmail = "" },
		"uzun e-posta":     func(r *identityv1.AdminSignInRequest) { r.AccessEmail = strings.Repeat("a", 321) },
	} {
		req := validAdminSignIn()
		mod(req)
		code, reason := reasonOf(t, func() error { _, err := srv.AdminSignIn(context.Background(), req); return err }())
		if code != codes.Unauthenticated || reason != "ERROR_REASON_INVALID_CREDENTIALS" {
			t.Errorf("%s: %v %q", name, code, reason)
		}
	}
	if f.gotIn != (platformadmin.SignInInput{}) {
		t.Fatal("geçersiz istek uygulama katmanına ulaştı")
	}
}

func TestAdminErrorsLeakNothing(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{platformadmin.ErrNotAllowed, codes.Unauthenticated, "ERROR_REASON_INVALID_CREDENTIALS"},
		{platformadmin.ErrNotAdmin, codes.Unauthenticated, ""},
		{platformadmin.ErrReauthenticationRequired, codes.PermissionDenied, "ERROR_REASON_REAUTHENTICATION_REQUIRED"},
		{fmt.Errorf("%w: google ayrıntısı", platformadmin.ErrUnavailable), codes.Unavailable, ""},
		{errors.New("veritabanı ayrıntısı"), codes.Internal, ""},
	} {
		srv := NewAdminAuthServer(&fakeAdmins{err: tc.err}, discard())
		_, err := srv.AdminSignIn(context.Background(), validAdminSignIn())
		code, reason := reasonOf(t, err)
		if code != tc.code || reason != tc.reason {
			t.Errorf("%v: %v %q", tc.err, code, reason)
		}
		if msg := status.Convert(err).Message(); strings.Contains(msg, "ayrıntı") || strings.Contains(msg, tc.err.Error()) {
			t.Errorf("%v: iç ayrıntı sızdı", tc.err)
		}
	}
}

func TestGetAdminNeedsAdminCaller(t *testing.T) {
	f := &fakeAdmins{user: id.New()}
	srv := NewAdminAuthServer(f, discard())
	if _, err := srv.GetAdmin(auth.WithCallerForTest(context.Background(), auth.Claims{Subject: f.user.String()}), &identityv1.GetAdminRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("kullanıcı çağıranla: %v", err)
	}
	c := auth.AdminClaims{Subject: f.user.String(), AuthTime: time.Unix(10, 0), ExpiresAt: time.Unix(100, 0)}
	resp, err := srv.GetAdmin(auth.WithAdminCallerForTest(context.Background(), c), &identityv1.GetAdminRequest{})
	if err != nil || resp.GetUserId() != f.user.String() || f.gotFor.Subject != c.Subject {
		t.Fatalf("GetAdmin = %v, %v", resp, err)
	}
}

// TestIdentityGuardSeparatesTokens builds identity's guard the way
// cmd/identity does and checks every token type against a user method and
// the admin method: app and registration tokens never reach the panel's
// methods and admin tokens never reach the app's (ADR-0008, layer 5).
func TestIdentityGuardSeparatesTokens(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	copy(seed, "identity-guard")
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	keys := auth.StaticKeys{"k1": key.Public().Key}
	const issuer = "tayfam-identity"
	v, err := auth.NewVerifier(auth.VerifierConfig{Keys: keys, Issuer: issuer, Audience: session.AudienceAPI, AlsoAccept: []string{session.AudienceRegistration}})
	if err != nil {
		t.Fatal(err)
	}
	av, err := auth.NewAdminVerifier(auth.AdminVerifierConfig{Keys: keys, Issuer: issuer})
	if err != nil {
		t.Fatal(err)
	}
	g, err := auth.NewGuard(v, PublicMethods...).WithServices(auth.ServicePolicy{Services: []string{MinorServiceName, PlatformAuditServiceName}})
	if err != nil {
		t.Fatal(err)
	}
	g, err = g.WithAdmin(auth.AdminPolicy{Verifier: av, Services: []string{AdminAuthServiceName, AdminUserServiceName}})
	if err != nil {
		t.Fatal(err)
	}

	user, sid := id.New(), id.New()
	issue := func(aud string) string {
		s, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: issuer, Audience: aud, TTL: 15 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		tok, _, err := s.Issue(user.String(), sid.String())
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	as, err := auth.NewAdminSigner(auth.AdminSignerConfig{Key: key, Issuer: issuer})
	if err != nil {
		t.Fatal(err)
	}
	adminTok, _, err := as.Issue(user.String(), sid.String(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	appTok, regTok := issue(session.AudienceAPI), issue(session.AudienceRegistration)

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(g.ServerOptions()...)
	identityv1.RegisterAuthServiceServer(srv, NewAuthServer(&fakeSessions{}, discard()))
	identityv1.RegisterAdminAuthServiceServer(srv, NewAdminAuthServer(&fakeAdmins{user: user}, discard()))
	identityv1.RegisterAdminUserServiceServer(srv, NewAdminUserServer(&fakeUsers{}, discard()))
	identityv1.RegisterPlatformAuditServiceServer(srv, NewPlatformAuditServer(&fakeAudits{}, discard()))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	authc := identityv1.NewAuthServiceClient(conn)
	adminc := identityv1.NewAdminAuthServiceClient(conn)

	userMethod := func(ctx context.Context) error {
		_, err := authc.SignOut(ctx, &identityv1.SignOutRequest{})
		return err
	}
	usersc := identityv1.NewAdminUserServiceClient(conn)
	userAdminMethod := func(ctx context.Context) error {
		_, err := usersc.GetUser(ctx, &identityv1.GetUserRequest{UserId: user.String()})
		return err
	}
	auditc := identityv1.NewPlatformAuditServiceClient(conn)
	auditMethod := func(ctx context.Context) error {
		_, err := auditc.RecordPlatformAudit(ctx, &identityv1.RecordPlatformAuditRequest{})
		return err
	}
	adminMethod := func(ctx context.Context) error {
		_, err := adminc.GetAdmin(ctx, &identityv1.GetAdminRequest{})
		return err
	}
	for _, tc := range []struct {
		name  string
		call  func(context.Context) error
		token string
		want  codes.Code
	}{
		{"uygulama metodu: uygulama token'ı", userMethod, appTok, codes.OK},
		{"uygulama metodu: admin token'ı", userMethod, adminTok, codes.Unauthenticated},
		{"panel metodu: admin token'ı", adminMethod, adminTok, codes.OK},
		{"panel metodu: uygulama token'ı", adminMethod, appTok, codes.Unauthenticated},
		{"panel metodu: kayıt token'ı", adminMethod, regTok, codes.Unauthenticated},
		{"panel metodu: token yok", adminMethod, "", codes.Unauthenticated},
		{"kullanıcı yönetimi: admin token'ı", userAdminMethod, adminTok, codes.OK},
		{"kullanıcı yönetimi: uygulama token'ı", userAdminMethod, appTok, codes.Unauthenticated},
		{"denetim servisi: admin token'ı", auditMethod, adminTok, codes.Unauthenticated},
		{"denetim servisi: uygulama token'ı", auditMethod, appTok, codes.Unauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.token != "" {
				ctx = auth.WithToken(ctx, tc.token)
			}
			if got := status.Code(tc.call(ctx)); got != tc.want {
				t.Fatalf("kod = %v, beklenen %v", got, tc.want)
			}
		})
	}
	// The panel's sign-in is public: it reaches the handler without a token.
	_, err = adminc.AdminSignIn(context.Background(), &identityv1.AdminSignInRequest{})
	if code, reason := reasonOf(t, err); code != codes.Unauthenticated || reason != "ERROR_REASON_INVALID_CREDENTIALS" {
		t.Fatalf("AdminSignIn: %v %q", code, reason)
	}
}
