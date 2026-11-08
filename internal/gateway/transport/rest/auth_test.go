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
}

func (f *fakeIdentity) tokens() *identityv1.Tokens {
	return &identityv1.Tokens{
		UserId: "u1", SessionId: "s1", AccessToken: "ACCESS", RefreshToken: "REFRESH",
		AccessExpireTime:    timestamppb.New(t0.Add(15 * time.Minute)),
		RefreshExpireTime:   timestamppb.New(t0.Add(90 * 24 * time.Hour)),
		RegistrationPending: true,
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
	f.gotRefresh = in.GetRefreshToken()
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

func newTestRouter(f *fakeIdentity) http.Handler { return newTestRouterBurst(f, 100) }

func newTestRouterBurst(f *fakeIdentity, burst int) http.Handler {
	return NewRouter(Deps{
		Version: "test",
		Auth: AuthDeps{
			Auth: f, Registration: f, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			SecureCookies: true, TrustCFConnectingIP: true,
			SignInLimit: ratelimit.New(60, burst), RefreshLimit: ratelimit.New(60, burst),
			Now: func() time.Time { return t0 },
		},
		AllowedOrigins: []string{"https://tayfam.app"},
	})
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

func TestSignInWebUsesHttpOnlyCookies(t *testing.T) {
	w := do(newTestRouter(&fakeIdentity{}), "POST", "/v1/auth/sign-in", signInBodyFor("web"), map[string]string{clientHeader: "web"})
	if w.Code != 200 {
		t.Fatalf("kod %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "ACCESS") || strings.Contains(w.Body.String(), "REFRESH") {
		t.Fatal("web yanıt gövdesinde token var")
	}
	cookies := map[string]*http.Cookie{}
	for _, c := range w.Result().Cookies() {
		cookies[c.Name] = c
	}
	at, rt := cookies[accessCookie], cookies[refreshCookie]
	if at == nil || rt == nil || at.Value != "ACCESS" || rt.Value != "REFRESH" {
		t.Fatalf("çerezler = %v", cookies)
	}
	for _, c := range []*http.Cookie{at, rt} {
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
			t.Fatalf("%s güvenli değil: %+v", c.Name, c)
		}
	}
	if at.Path != "/" || rt.Path != refreshPath {
		t.Fatalf("yollar: %s %s", at.Path, rt.Path)
	}
	if at.MaxAge != 900 {
		t.Fatalf("erişim çerezi ömrü %d", at.MaxAge)
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
	r := httptest.NewRequest("POST", "/v1/auth/sign-in", strings.NewReader(signInBodyFor("android")))
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

func TestRefreshMobileAndWeb(t *testing.T) {
	f := &fakeIdentity{}
	h := newTestRouter(f)
	w := do(h, "POST", "/v1/auth/refresh", `{"refresh_token":"MOBIL"}`, map[string]string{clientHeader: "android"})
	if w.Code != 200 || f.gotRefresh != "MOBIL" {
		t.Fatalf("mobil: %d %q", w.Code, f.gotRefresh)
	}
	r := httptest.NewRequest("POST", "/v1/auth/refresh", nil)
	r.Header.Set(clientHeader, "web")
	r.AddCookie(&http.Cookie{Name: refreshCookie, Value: "WEBRT"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || f.gotRefresh != "WEBRT" || strings.Contains(rec.Body.String(), "REFRESH") {
		t.Fatalf("web: %d %q %s", rec.Code, f.gotRefresh, rec.Body)
	}

	// Geçersiz refresh'te web çerezleri silinir.
	f.err = identityErr(codes.Unauthenticated, "ERROR_REASON_INVALID_REFRESH")
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/v1/auth/refresh", nil)
	r.Header.Set(clientHeader, "web")
	r.AddCookie(&http.Cookie{Name: refreshCookie, Value: "WEBRT"})
	h.ServeHTTP(rec, r)
	if rec.Code != 401 || len(rec.Result().Cookies()) != 2 || rec.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("çerezler silinmedi: %d %v", rec.Code, rec.Result().Cookies())
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
	r := httptest.NewRequest("POST", "/v1/auth/sign-out", nil)
	r.Header.Set(clientHeader, "web")
	r.AddCookie(&http.Cookie{Name: accessCookie, Value: "COOKIETOKEN"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 204 || f.gotToken != "COOKIETOKEN" || len(rec.Result().Cookies()) != 2 {
		t.Fatalf("web çıkış: %d %q", rec.Code, f.gotToken)
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

func TestCORS(t *testing.T) {
	h := newTestRouter(&fakeIdentity{})
	pre := do(h, "OPTIONS", "/v1/auth/sign-in", "", map[string]string{"Origin": "https://tayfam.app", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type,x-tayfam-client"})
	if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Origin") != "https://tayfam.app" || pre.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("preflight: %d %v", pre.Code, pre.Header())
	}
	if !strings.Contains(pre.Header().Get("Access-Control-Allow-Headers"), clientHeader) {
		t.Fatal("X-Tayfam-Client izinli değil")
	}
	evil := do(h, "OPTIONS", "/v1/auth/sign-in", "", map[string]string{"Origin": "https://tayfam.app.evil.example", "Access-Control-Request-Method": "POST"})
	if evil.Code != 403 || evil.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("yabancı köken: %d %v", evil.Code, evil.Header())
	}
	get := do(h, "GET", "/v1/status", "", map[string]string{"Origin": "https://evil.example"})
	if get.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("yabancı kökene CORS başlığı verildi")
	}
}
