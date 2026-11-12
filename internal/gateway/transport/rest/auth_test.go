package rest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

type fakeIdentity struct {
	err        error
	gotSignIn  *identityv1.SignInRequest
	gotRefresh string
	gotToken   string
	gotReg     *identityv1.CompleteRegistrationRequest
	// consentRequired is returned in Tokens (account_test.go).
	consentRequired bool
	// gotRefreshReq is the last refresh request; checkAfter is returned
	// in Tokens (integrity_test.go).
	gotRefreshReq *identityv1.RefreshRequest
	checkAfter    *timestamppb.Timestamp
}

func (f *fakeIdentity) tokens() *identityv1.Tokens {
	return &identityv1.Tokens{
		UserId: "u1", SessionId: "s1", AccessToken: "ACCESS", RefreshToken: "REFRESH",
		AccessExpireTime:    timestamppb.New(t0.Add(15 * time.Minute)),
		RefreshExpireTime:   timestamppb.New(t0.Add(90 * 24 * time.Hour)),
		RegistrationPending: true,
		ConsentRequired:     f.consentRequired,
		IntegrityCheckAfter: f.checkAfter,
	}
}

func tokenFrom(ctx context.Context) string {
	md, _ := metadata.FromOutgoingContext(ctx)
	if v := md.Get("authorization"); len(v) == 1 {
		return strings.TrimPrefix(v[0], "Bearer ")
	}
	return ""
}

func (f *fakeIdentity) SignIn(_ context.Context, in *identityv1.SignInRequest, _ ...grpc.CallOption) (*identityv1.SignInResponse, error) {
	f.gotSignIn = in
	if f.err != nil {
		return nil, f.err
	}
	return &identityv1.SignInResponse{Tokens: f.tokens()}, nil
}
func (f *fakeIdentity) Refresh(_ context.Context, in *identityv1.RefreshRequest, _ ...grpc.CallOption) (*identityv1.RefreshResponse, error) {
	f.gotRefresh, f.gotRefreshReq = in.GetRefreshToken(), in
	if f.err != nil {
		return nil, f.err
	}
	return &identityv1.RefreshResponse{Tokens: f.tokens()}, nil
}
func (f *fakeIdentity) SignOut(ctx context.Context, _ *identityv1.SignOutRequest, _ ...grpc.CallOption) (*identityv1.SignOutResponse, error) {
	f.gotToken = tokenFrom(ctx)
	return &identityv1.SignOutResponse{}, f.err
}
func (f *fakeIdentity) GetRequirements(ctx context.Context, _ *identityv1.GetRequirementsRequest, _ ...grpc.CallOption) (*identityv1.GetRequirementsResponse, error) {
	f.gotToken = tokenFrom(ctx)
	return &identityv1.GetRequirementsResponse{PrivacyNoticeVersion: "2026-11-08", TermsVersion: "2026-11-08", MinimumAge: 16}, f.err
}
func (f *fakeIdentity) CompleteRegistration(ctx context.Context, in *identityv1.CompleteRegistrationRequest, _ ...grpc.CallOption) (*identityv1.CompleteRegistrationResponse, error) {
	f.gotToken, f.gotReg = tokenFrom(ctx), in
	if f.err != nil {
		return nil, f.err
	}
	return &identityv1.CompleteRegistrationResponse{}, nil
}

// devOrigin is the local web build's origin in tests (WEB_DEV_ORIGINS).
const devOrigin = "http://localhost:3000"

// newTestRouter is a dev gateway: the local web build is served from
// devOrigin (ADR-0017).
func newTestRouter(f *fakeIdentity) http.Handler { return newTestRouterBurst(f, 100) }

func newTestRouterBurst(f *fakeIdentity, burst int) http.Handler {
	d := authTestDeps(f, burst)
	d.DevOrigins, d.WebClient = []string{devOrigin}, true
	return NewRouter(d)
}

// newProdRouter is a prod gateway: no web build, no development origin.
func newProdRouter(f *fakeIdentity) http.Handler { return NewRouter(authTestDeps(f, 100)) }

func authTestDeps(f *fakeIdentity, burst int) Deps {
	return Deps{
		Core:    testCoreDeps(&fakeCore{}, burst),
		Chat:    testChatDeps(&fakeChat{}),
		Version: "test",
		Auth: AuthDeps{
			Auth: f, Registration: f, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			TrustCFConnectingIP: true,
			SignInLimit:         ratelimit.New(60, burst), RefreshLimit: ratelimit.New(60, burst),
		},
	}
}

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func problemType(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct{ Type string }
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return strings.TrimPrefix(p.Type, "https://tayfam.app/problems/")
}

const signInJSON = `{"provider":"google","id_token":"tok","nonce":"0123456789abcdef","platform":"%s"}`

func signInBodyFor(platform string) string { return strings.Replace(signInJSON, "%s", platform, 1) }

func TestSignInMobileReturnsTokensInBody(t *testing.T) {
	f := &fakeIdentity{}
	w := do(newTestRouter(f), "POST", "/v1/auth/sign-in", signInBodyFor("android"), map[string]string{clientHeader: "android"})
	if w.Code != 200 {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	var b sessionBody
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	if b.AccessToken != "ACCESS" || b.RefreshToken != "REFRESH" || !b.RegistrationPending {
		t.Fatalf("gövde = %+v", b)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("mobil yanıtta çerez var")
	}
	if f.gotSignIn.GetPlatform() != identityv1.Platform_PLATFORM_ANDROID || f.gotSignIn.GetProvider() != identityv1.Provider_PROVIDER_GOOGLE {
		t.Fatalf("identity'ye giden = %v", f.gotSignIn)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token içeren yanıt önbelleğe alınabilir")
	}
}

// TestSignInWebDevReturnsTokensInBody: outside prod the local web build
// signs in like the mobile apps: tokens in the body, no cookie (ADR-0017).
func TestSignInWebDevReturnsTokensInBody(t *testing.T) {
	f := &fakeIdentity{}
	w := do(newTestRouter(f), "POST", "/v1/auth/sign-in", signInBodyFor("web"), map[string]string{clientHeader: "web"})
	if w.Code != 200 {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	var b sessionBody
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	if b.AccessToken != "ACCESS" || b.RefreshToken != "REFRESH" {
		t.Fatalf("gövde = %+v", b)
	}
	if len(w.Result().Cookies()) != 0 || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("web yanıtında çerez var")
	}
	if f.gotSignIn.GetPlatform() != identityv1.Platform_PLATFORM_WEB {
		t.Fatalf("identity'ye giden platform = %v", f.gotSignIn.GetPlatform())
	}
}

// TestProdRefusesWebClient: prod serves no web build (ADR-0017). Sign-in
// with platform web, X-Tayfam-Client: web and a web version header are
// refused; client-config does not list web; the mobile apps are unaffected.
func TestProdRefusesWebClient(t *testing.T) {
	f := &fakeIdentity{}
	h := newProdRouter(f)
	w := do(h, "POST", "/v1/auth/sign-in", signInBodyFor("web"), map[string]string{clientHeader: "android"})
	if w.Code != 400 || problemType(t, w) != "invalid-request" || f.gotSignIn != nil {
		t.Fatalf("web girişi: %d %s", w.Code, problemType(t, w))
	}
	w = do(h, "POST", "/v1/auth/sign-in", signInBodyFor("android"), map[string]string{clientHeader: "web"})
	if w.Code != 400 || problemType(t, w) != "missing-client-header" || f.gotSignIn != nil {
		t.Fatalf("X-Tayfam-Client web: %d %s", w.Code, problemType(t, w))
	}
	w = do(h, "GET", "/v1/me/sessions", "", map[string]string{"Authorization": "Bearer USERTOKEN", versionHeader: "web/1.0.0"})
	if w.Code != 400 || problemType(t, w) != "invalid-client-version" {
		t.Fatalf("web sürüm başlığı: %d %s", w.Code, problemType(t, w))
	}
	w = do(h, "GET", "/v1/client-config", "", nil)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"platforms":{"android":{},"ios":{}}}` {
		t.Fatalf("client-config: %d %s", w.Code, w.Body)
	}
	w = do(h, "POST", "/v1/auth/sign-in", signInBodyFor("ios"), map[string]string{clientHeader: "ios", versionHeader: "ios/1.0.0"})
	if w.Code != 200 || f.gotSignIn.GetPlatform() != identityv1.Platform_PLATFORM_IOS {
		t.Fatalf("mobil giriş: %d %s", w.Code, w.Body)
	}
}

func TestStateChangingRequestsNeedClientHeader(t *testing.T) {
	h := newTestRouter(&fakeIdentity{})
	for _, path := range []string{"/v1/auth/sign-in", "/v1/auth/refresh", "/v1/auth/sign-out", "/v1/registration"} {
		w := do(h, "POST", path, `{}`, nil)
		if w.Code != 400 || problemType(t, w) != "missing-client-header" {
			t.Errorf("%s: %d %s", path, w.Code, problemType(t, w))
		}
		w = do(h, "POST", path, `{}`, map[string]string{clientHeader: "curl"})
		if problemType(t, w) != "missing-client-header" {
			t.Errorf("%s: bilinmeyen istemci kabul edildi", path)
		}
	}
	if w := do(h, "GET", "/v1/status", "", nil); w.Code != 200 {
		t.Fatal("GET için başlık istenmemeli")
	}
}

func TestBadBodies(t *testing.T) {
	h := newTestRouter(&fakeIdentity{})
	hdr := map[string]string{clientHeader: "android"}
	for name, body := range map[string]string{
		"bilinmeyen alan":      `{"provider":"google","id_token":"t","nonce":"0123456789abcdef","platform":"android","admin":true}`,
		"bilinmeyen sağlayıcı": signInBodyFor("android")[:12] + `apple"` + signInBodyFor("android")[19:],
		"kısa nonce":           `{"provider":"google","id_token":"t","nonce":"kisa","platform":"android"}`,
		"iki nesne":            signInBodyFor("android") + `{}`,
		"bozuk JSON":           `{"provider":`,
		"büyük gövde":          `{"provider":"google","id_token":"` + strings.Repeat("a", 20000) + `"}`,
	} {
		w := do(h, "POST", "/v1/auth/sign-in", body, hdr)
		if w.Code != 400 {
			t.Errorf("%s: kod %d", name, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/sign-in", strings.NewReader(signInBodyFor("android")))
	r.Header.Set(clientHeader, "android")
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("JSON olmayan içerik: %d", w.Code)
	}
}

func identityErr(code codes.Code, reason string) error {
	st, _ := status.New(code, "istek reddedildi").WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "identity.tayfam.app"})
	return st.Err()
}

func TestIdentityErrorsBecomeProblems(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		typ  string
	}{
		{identityErr(codes.Unauthenticated, "ERROR_REASON_INVALID_CREDENTIALS"), 401, "invalid-credentials"},
		{identityErr(codes.PermissionDenied, "ERROR_REASON_AGE_BLOCKED"), 403, "age-blocked"},
		{identityErr(codes.Aborted, "ERROR_REASON_REFRESH_CONFLICT"), 409, "refresh-conflict"},
		{status.Error(codes.Unavailable, "x"), 503, "unavailable"},
		{status.Error(codes.Unimplemented, "x"), 503, "unavailable"},
		{status.Error(codes.Internal, "SELECT * FROM users iç ayrıntı"), 500, "internal"},
		// Başka bir alan adından gelen ErrorInfo'ya güvenilmez.
		{func() error {
			st, _ := status.New(codes.PermissionDenied, "x").WithDetails(&errdetails.ErrorInfo{Reason: "ERROR_REASON_AGE_BLOCKED", Domain: "evil"})
			return st.Err()
		}(), 500, "internal"},
	} {
		w := do(newTestRouter(&fakeIdentity{err: tc.err}), "POST", "/v1/auth/sign-in", signInBodyFor("ios"), map[string]string{clientHeader: "ios"})
		if w.Code != tc.code || problemType(t, w) != tc.typ {
			t.Errorf("%v: %d %s", tc.err, w.Code, problemType(t, w))
		}
		if strings.Contains(w.Body.String(), "SELECT") {
			t.Error("iç ayrıntı istemciye sızdı")
		}
	}
}

// TestRefreshReadsBodyOnly: the refresh token comes in the body; the old
// web cookie is ignored and no cookie is written or cleared (ADR-0017).
func TestRefreshReadsBodyOnly(t *testing.T) {
	f := &fakeIdentity{}
	h := newTestRouter(f)
	w := do(h, "POST", "/v1/auth/refresh", `{"refresh_token":"MOBIL"}`, map[string]string{clientHeader: "android", "Cookie": "tayfam_rt=ESKI"})
	var b sessionBody
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != 200 || f.gotRefresh != "MOBIL" || b.RefreshToken != "REFRESH" || len(w.Result().Cookies()) != 0 {
		t.Fatalf("gövdeyle: %d %q %s", w.Code, f.gotRefresh, w.Body)
	}

	// Cookie only: not a refresh token.
	f.gotRefresh = ""
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", nil)
	r.Header.Set(clientHeader, "web")
	r.AddCookie(&http.Cookie{Name: "tayfam_rt", Value: "WEBRT"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code == 200 || f.gotRefresh != "" || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("yalnızca çerez: %d %q", rec.Code, f.gotRefresh)
	}
	w = do(h, "POST", "/v1/auth/refresh", `{}`, map[string]string{clientHeader: "web", "Cookie": "tayfam_rt=WEBRT"})
	if w.Code != 401 || problemType(t, w) != "invalid-refresh" || f.gotRefresh != "" {
		t.Fatalf("boş gövde, çerez var: %d %s %q", w.Code, problemType(t, w), f.gotRefresh)
	}

	// An invalid refresh token clears nothing.
	f.err = identityErr(codes.Unauthenticated, "ERROR_REASON_INVALID_REFRESH")
	w = do(h, "POST", "/v1/auth/refresh", `{"refresh_token":"ESKI"}`, map[string]string{clientHeader: "android"})
	if w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatalf("geçersiz refresh: %d %v", w.Code, w.Result().Cookies())
	}
}

func TestTokenForwarding(t *testing.T) {
	f := &fakeIdentity{}
	h := newTestRouter(f)
	w := do(h, "GET", "/v1/registration/requirements", "", map[string]string{"Authorization": "Bearer TOKEN1"})
	if w.Code != 200 || f.gotToken != "TOKEN1" {
		t.Fatalf("gereksinimler: %d %q", w.Code, f.gotToken)
	}
	w = do(h, "POST", "/v1/registration", `{"birth_date":"1990-05-19","accepted_privacy_notice_version":"v","accepted_terms_version":"v"}`,
		map[string]string{"Authorization": "Bearer TOKEN2", clientHeader: "ios"})
	if w.Code != 204 || f.gotToken != "TOKEN2" || f.gotReg.GetBirthDate() != "1990-05-19" {
		t.Fatalf("kayıt: %d %q %v", w.Code, f.gotToken, f.gotReg)
	}
	if w := do(h, "GET", "/v1/registration/requirements", "", nil); w.Code != 401 {
		t.Fatalf("token'sız: %d", w.Code)
	}
	w = do(h, "POST", "/v1/auth/sign-out", "", map[string]string{"Authorization": "Bearer TOKEN3", clientHeader: "web"})
	if w.Code != 204 || f.gotToken != "TOKEN3" || len(w.Result().Cookies()) != 0 {
		t.Fatalf("çıkış: %d %q %v", w.Code, f.gotToken, w.Result().Cookies())
	}
}

// TestCookieAuthenticatesNothing: the former web cookie, alone, is no
// credential on any route (ADR-0017): identity is not called and no
// cookie is written or cleared.
func TestCookieAuthenticatesNothing(t *testing.T) {
	f := &fakeIdentity{}
	h := newTestRouter(f)
	for _, rt := range []struct{ method, path, body string }{
		{"POST", "/v1/auth/sign-out", ""},
		{"GET", "/v1/registration/requirements", ""},
		{"POST", "/v1/registration", `{"birth_date":"1990-05-19","accepted_privacy_notice_version":"v","accepted_terms_version":"v"}`},
	} {
		r := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
		if rt.body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set(clientHeader, "web")
		r.Header.Set("Origin", devOrigin)
		r.AddCookie(&http.Cookie{Name: "tayfam_at", Value: "COOKIETOKEN"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 401 || problemType(t, rec) != "unauthenticated" || f.gotToken != "" || len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s %s: %d %s %q", rt.method, rt.path, rec.Code, problemType(t, rec), f.gotToken)
		}
	}
}

func TestSignInRateLimitedPerIP(t *testing.T) {
	h := newTestRouterBurst(&fakeIdentity{}, 3)
	send := func(ip string) int {
		return do(h, "POST", "/v1/auth/sign-in", signInBodyFor("android"), map[string]string{clientHeader: "android", "CF-Connecting-IP": ip}).Code
	}
	for range 3 {
		if c := send("203.0.113.7"); c != 200 {
			t.Fatalf("kod %d", c)
		}
	}
	if c := send("203.0.113.7"); c != 429 {
		t.Fatalf("sınır aşıldı ama kod %d", c)
	}
	if c := send("198.51.100.9"); c != 200 {
		t.Fatal("farklı IP etkilendi")
	}
}

// TestCORS: outside prod only the configured development origin gets CORS
// headers, never with credentials, and only for the listed request
// headers (ADR-0017).
func TestCORS(t *testing.T) {
	h := newTestRouter(&fakeIdentity{})
	pre := do(h, "OPTIONS", "/v1/auth/sign-in", "", map[string]string{"Origin": devOrigin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type,x-tayfam-client"})
	if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Origin") != devOrigin {
		t.Fatalf("preflight: %d %v", pre.Code, pre.Header())
	}
	if pre.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("Access-Control-Allow-Credentials yazıldı")
	}
	want := "Authorization, Content-Type, X-Tayfam-Client, X-Tayfam-Client-Version, X-Request-Id"
	if got := pre.Header().Get("Access-Control-Allow-Headers"); got != want {
		t.Fatalf("izinli başlıklar = %q", got)
	}
	get := do(h, "GET", "/v1/status", "", map[string]string{"Origin": devOrigin})
	if get.Header().Get("Access-Control-Allow-Origin") != devOrigin || get.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("GET: %v", get.Header())
	}
	for _, origin := range []string{"https://tayfam.app", "http://localhost:3001", "http://localhost:3000.evil.example", "https://evil.example", "null"} {
		evil := do(h, "OPTIONS", "/v1/auth/sign-in", "", map[string]string{"Origin": origin, "Access-Control-Request-Method": "POST"})
		if evil.Code != 403 || evil.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s preflight: %d %v", origin, evil.Code, evil.Header())
		}
		get := do(h, "GET", "/v1/status", "", map[string]string{"Origin": origin})
		if get.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s kaynağına CORS başlığı verildi", origin)
		}
	}
}

// TestProdNoCORS: prod has no development origin, so no Origin gets a CORS
// header (issue #124).
func TestProdNoCORS(t *testing.T) {
	h := newProdRouter(&fakeIdentity{})
	for _, origin := range []string{devOrigin, "http://127.0.0.1:3000", "https://tayfam.app", "https://admin.tayfam.app", "https://evil.example"} {
		pre := do(h, "OPTIONS", "/v1/auth/sign-in", "", map[string]string{"Origin": origin, "Access-Control-Request-Method": "POST"})
		if pre.Code != 403 || corsHeaders(pre) != "" {
			t.Errorf("%s preflight: %d %q", origin, pre.Code, corsHeaders(pre))
		}
		for _, path := range []string{"/v1/status", "/v1/client-config", "/v1/me/sessions"} {
			if w := do(h, "GET", path, "", map[string]string{"Origin": origin}); corsHeaders(w) != "" {
				t.Errorf("%s %s: %q", origin, path, corsHeaders(w))
			}
		}
	}
}

// corsHeaders returns every Access-Control-* response header, joined.
func corsHeaders(w *httptest.ResponseRecorder) string {
	var out []string
	for k, v := range w.Header() {
		if strings.HasPrefix(k, "Access-Control-") {
			out = append(out, k+"="+strings.Join(v, ","))
		}
	}
	return strings.Join(out, ";")
}
