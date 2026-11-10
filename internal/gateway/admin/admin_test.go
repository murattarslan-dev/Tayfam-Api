package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

const (
	host     = "admin.tayfam.test"
	origin   = "https://" + host
	team     = "tayfam-test.cloudflareaccess.com"
	aud      = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	issuer   = "tayfam-identity"
	userID   = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	sessID   = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"
	accessTo = "Yonetici@Example.com"
)

type fakeIdentity struct {
	signIn    *identityv1.AdminSignInRequest
	signInErr error
	getErr    error
	gotToken  string
	token     string
}

func (f *fakeIdentity) AdminSignIn(_ context.Context, in *identityv1.AdminSignInRequest, _ ...grpc.CallOption) (*identityv1.AdminSignInResponse, error) {
	f.signIn = in
	if f.signInErr != nil {
		return nil, f.signInErr
	}
	return &identityv1.AdminSignInResponse{UserId: userID, AdminToken: f.token,
		ExpireTime: timestamppb.New(time.Now().Add(8 * time.Hour)), AuthTime: timestamppb.Now()}, nil
}

func (f *fakeIdentity) GetAdmin(ctx context.Context, _ *identityv1.GetAdminRequest, _ ...grpc.CallOption) (*identityv1.GetAdminResponse, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	if v := md.Get("authorization"); len(v) == 1 {
		f.gotToken = strings.TrimPrefix(v[0], "Bearer ")
	}
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &identityv1.GetAdminResponse{UserId: userID, DisplayName: "Yönetici", AuthTime: timestamppb.Now(), ExpireTime: timestamppb.New(time.Now().Add(time.Hour))}, nil
}

type env struct {
	h        http.Handler
	cluster  *satest.Cluster
	identity *fakeIdentity
	access   string // a valid Cloudflare Access JWT
	adminTok string
	appTok   string
	logs     *bytes.Buffer
	core     *fakeCore
	users    *fakeUsers
	audit    *fakeAudit
	// signingKey signs further admin tokens (old sign-ins).
	signingKey auth.SigningKey
}

func setup(t *testing.T) *env {
	t.Helper()
	c := satest.New(t)
	av, err := auth.NewAccessVerifier(auth.AccessVerifierConfig{TeamDomain: team, Audience: aud, Client: c.Client(), CertsURL: c.JWKSURL()})
	if err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	copy(seed, "gateway-admin")
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	keys := auth.StaticKeys{"k1": key.Public().Key}
	adminV, err := auth.NewAdminVerifier(auth.AdminVerifierConfig{Keys: keys, Issuer: issuer})
	if err != nil {
		t.Fatal(err)
	}
	as, err := auth.NewAdminSigner(auth.AdminSignerConfig{Key: key, Issuer: issuer})
	if err != nil {
		t.Fatal(err)
	}
	adminTok, _, err := as.Issue(userID, sessID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: issuer, Audience: "tayfam-api", TTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	appTok, _, err := s.Issue(userID, sessID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e := &env{cluster: c, identity: &fakeIdentity{token: adminTok}, adminTok: adminTok, appTok: appTok, logs: &bytes.Buffer{},
		signingKey: key}
	shared := &calls{}
	e.core, e.users, e.audit = newFakeCore(shared), newFakeUsers(shared), newFakeAudit(shared)
	e.access = c.Token(t, satest.Spec{Alg: "RS256", Claims: map[string]any{
		"iss": "https://" + team, "aud": []string{aud}, "sub": "cf-1", "email": accessTo, "type": "app",
		"iat": now.Unix(), "exp": now.Add(8 * time.Hour).Unix(),
	}})
	e.h, err = NewHandler(Config{
		Host: host, Access: av, Admin: adminV, Identity: e.identity,
		Core: e.core, Users: e.users, Audit: e.audit, GoogleClientID: googleClient,
		Log: slog.New(slog.NewJSONHandler(e.logs, nil)), SignInLimit: ratelimit.New(60, 60),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// req builds a request to the panel that passed Cloudflare Access.
func (e *env) req(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "https://"+host+path, strings.NewReader(body))
	r.Header.Set(auth.AccessHeader, e.access)
	if method != http.MethodGet {
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func (e *env) do(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func problemType(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return strings.TrimPrefix(p.Type, "https://tayfam.app/problems/")
}

func cookie(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestMuxSeparatesHosts: the panel only on the admin host, the app API never
// there, and no panel at all without Cloudflare Access configuration.
func TestMuxSeparatesHosts(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	panel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	for _, tc := range []struct {
		name   string
		admin  http.Handler
		target string
		want   int
	}{
		{"panel kapalı: admin adresi", nil, "https://" + host + "/auth/nonce", http.StatusNotFound},
		{"panel kapalı: admin adresinde uygulama API'si", nil, "https://" + host + "/v1/status", http.StatusNotFound},
		{"panel kapalı: API adresi", nil, "https://api.tayfam.test/v1/status", http.StatusTeapot},
		{"panel açık: admin adresi", panel, "https://" + host + "/auth/nonce", http.StatusAccepted},
		{"panel açık: admin adresinde uygulama API'si", panel, "https://" + host + "/v1/status", http.StatusAccepted},
		{"panel açık: büyük harf ve port", panel, "https://ADMIN.tayfam.test:443/auth/me", http.StatusAccepted},
		{"panel açık: API adresinde panel yolu", panel, "https://api.tayfam.test/auth/nonce", http.StatusTeapot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			Mux(api, host, tc.admin).ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.target, nil))
			if w.Code != tc.want {
				t.Fatalf("kod = %d, beklenen %d", w.Code, tc.want)
			}
		})
	}
}

// TestAccessJWTRequired: no route runs without a valid Cloudflare Access JWT.
func TestAccessJWTRequired(t *testing.T) {
	e := setup(t)
	now := time.Now()
	otherApp := e.cluster.Token(t, satest.Spec{Alg: "RS256", Claims: map[string]any{
		"iss": "https://" + team, "aud": []string{strings.Repeat("f", 64)}, "sub": "cf-1", "email": accessTo, "type": "app",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}})
	for name, set := range map[string]func(*http.Request){
		"başlık yok":       func(r *http.Request) { r.Header.Del(auth.AccessHeader) },
		"bozuk":            func(r *http.Request) { r.Header.Set(auth.AccessHeader, "a.b.c") },
		"başka uygulama":   func(r *http.Request) { r.Header.Set(auth.AccessHeader, otherApp) },
		"uygulama token'ı": func(r *http.Request) { r.Header.Set(auth.AccessHeader, e.appTok) },
		"iki başlık":       func(r *http.Request) { r.Header.Add(auth.AccessHeader, e.access) },
		"yalnızca çerez": func(r *http.Request) {
			r.Header.Del(auth.AccessHeader)
			r.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: e.access})
		},
	} {
		for _, path := range []string{"/auth/nonce", "/auth/me", "/bilinmeyen", "/", "/giris", "/tayfalar", "/static/panel.css", HTMXPath} {
			r := e.req(http.MethodGet, path, "")
			r.AddCookie(&http.Cookie{Name: TokenCookie, Value: e.adminTok})
			set(r)
			w := e.do(r)
			if w.Code != http.StatusForbidden || problemType(t, w) != "access-denied" {
				t.Errorf("%s %s: %d %s", name, path, w.Code, problemType(t, w))
			}
		}
	}
	if w := e.do(e.req(http.MethodGet, "/bilinmeyen", "")); w.Code != http.StatusNotFound {
		t.Fatalf("geçerli Access JWT ile bilinmeyen yol: %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := setup(t)
	r := e.req(http.MethodGet, "/auth/nonce", "")
	r.Header.Del(auth.AccessHeader) // also on refusals
	w := e.do(r)
	h := w.Header()
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") ||
		!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		h.Get("X-Frame-Options") != "DENY" || h.Get("Cache-Control") != "no-store" ||
		h.Get("Referrer-Policy") != "same-origin" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("güvenlik başlıkları eksik: %v", h)
	}
}

func checkCookie(t *testing.T, c *http.Cookie, name string) {
	t.Helper()
	if c == nil {
		t.Fatalf("%s çerezi yok", name)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.MaxAge <= 0 {
		t.Fatalf("%s çerezi: %+v", name, c)
	}
}

func TestSignInFlow(t *testing.T) {
	e := setup(t)
	w := e.do(e.req(http.MethodGet, "/auth/nonce", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("nonce: %d", w.Code)
	}
	nc := cookie(w, NonceCookie)
	checkCookie(t, nc, NonceCookie)
	var nb struct{ Nonce string }
	if err := json.Unmarshal(w.Body.Bytes(), &nb); err != nil || nb.Nonce != nc.Value || len(nb.Nonce) < 32 {
		t.Fatalf("nonce gövdesi = %s", w.Body)
	}

	r := e.req(http.MethodPost, "/auth/sign-in", `{"id_token":"google-id-token"}`)
	r.AddCookie(nc)
	w = e.do(r)
	if w.Code != http.StatusOK {
		t.Fatalf("giriş: %d %s", w.Code, w.Body)
	}
	got := e.identity.signIn
	if got.GetIdToken() != "google-id-token" || got.GetNonce() != nc.Value || got.GetAccessEmail() != "yonetici@example.com" {
		t.Fatalf("identity'ye giden = %v", got)
	}
	tc := cookie(w, TokenCookie)
	checkCookie(t, tc, TokenCookie)
	if tc.Value != e.adminTok || tc.MaxAge > int((8*time.Hour).Seconds()) {
		t.Fatalf("token çerezi: %+v", tc)
	}
	if strings.Contains(w.Body.String(), e.adminTok) {
		t.Fatal("admin token'ı gövdede döndü")
	}
	if c := cookie(w, NonceCookie); c == nil || c.MaxAge >= 0 {
		t.Fatal("nonce çerezi silinmedi")
	}
	if strings.Contains(e.logs.String(), "example.com") || strings.Contains(e.logs.String(), e.adminTok) {
		t.Fatal("log'a e-posta ya da token yazıldı")
	}
}

func TestSignInRefusals(t *testing.T) {
	e := setup(t)
	nonce := &http.Cookie{Name: NonceCookie, Value: strings.Repeat("n", 43)}
	body := `{"id_token":"t"}`
	for name, tc := range map[string]struct {
		mod  func(*http.Request)
		code int
		typ  string
	}{
		"nonce çerezi yok": {func(r *http.Request) {}, http.StatusBadRequest, "invalid-request"},
		"Origin yok":       {func(r *http.Request) { r.AddCookie(nonce); r.Header.Del("Origin") }, http.StatusForbidden, "cross-origin"},
		"başka Origin":     {func(r *http.Request) { r.AddCookie(nonce); r.Header.Set("Origin", "https://tayfam.test") }, http.StatusForbidden, "cross-origin"},
		"siteler arası":    {func(r *http.Request) { r.AddCookie(nonce); r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden, "cross-origin"},
		"JSON değil":       {func(r *http.Request) { r.AddCookie(nonce); r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType, "unsupported-media-type"},
		"bilinmeyen alan": {func(r *http.Request) {
			r.AddCookie(nonce)
			r.Body = io.NopCloser(strings.NewReader(`{"id_token":"t","nonce":"x"}`))
		}, http.StatusBadRequest, "invalid-request"},
		"token yok": {func(r *http.Request) { r.AddCookie(nonce); r.Body = io.NopCloser(strings.NewReader(`{}`)) }, http.StatusBadRequest, "invalid-request"},
	} {
		t.Run(name, func(t *testing.T) {
			e.identity.signIn = nil
			r := e.req(http.MethodPost, "/auth/sign-in", body)
			tc.mod(r)
			w := e.do(r)
			if w.Code != tc.code || problemType(t, w) != tc.typ {
				t.Fatalf("%d %s", w.Code, problemType(t, w))
			}
			if e.identity.signIn != nil {
				t.Fatal("identity çağrıldı")
			}
		})
	}

	// identity refuses: one answer, no cookie.
	e.identity.signInErr = status.Error(codes.Unauthenticated, "istek reddedildi")
	r := e.req(http.MethodPost, "/auth/sign-in", body)
	r.AddCookie(nonce)
	w := e.do(r)
	if w.Code != http.StatusUnauthorized || problemType(t, w) != "unauthenticated" || cookie(w, TokenCookie) != nil {
		t.Fatalf("reddedilen giriş: %d %s", w.Code, problemType(t, w))
	}
}

func TestSignInRateLimited(t *testing.T) {
	e := setup(t)
	e.h, _ = NewHandler(Config{Host: host, Access: mustAccess(t, e.cluster), Admin: noAdmin{}, Identity: e.identity,
		Core: e.core, Users: e.users, Audit: e.audit, GoogleClientID: googleClient, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), SignInLimit: ratelimit.New(1, 1)})
	nonce := &http.Cookie{Name: NonceCookie, Value: strings.Repeat("n", 43)}
	var last int
	for range 3 {
		r := e.req(http.MethodPost, "/auth/sign-in", `{"id_token":"t"}`)
		r.AddCookie(nonce)
		last = e.do(r).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("kod = %d", last)
	}
}

func mustAccess(t *testing.T, c *satest.Cluster) *auth.AccessVerifier {
	t.Helper()
	v, err := auth.NewAccessVerifier(auth.AccessVerifierConfig{TeamDomain: team, Audience: aud, Client: c.Client(), CertsURL: c.JWKSURL()})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

type noAdmin struct{}

func (noAdmin) Verify(context.Context, string) (auth.AdminClaims, error) {
	return auth.AdminClaims{}, auth.ErrInvalidToken
}

// TestMeTakesOnlyAdminToken: on the admin host only the admin cookie opens
// a session; the app's token (cookie or header) never does (ADR-0008,
// layer 5).
func TestMeTakesOnlyAdminToken(t *testing.T) {
	e := setup(t)
	for name, tc := range map[string]struct {
		mod  func(*http.Request)
		code int
	}{
		"admin çerezi":                     {func(r *http.Request) { r.AddCookie(&http.Cookie{Name: TokenCookie, Value: e.adminTok}) }, http.StatusOK},
		"çerez yok":                        {func(r *http.Request) {}, http.StatusUnauthorized},
		"admin çerezinde uygulama token'ı": {func(r *http.Request) { r.AddCookie(&http.Cookie{Name: TokenCookie, Value: e.appTok}) }, http.StatusUnauthorized},
		"uygulama çerezi":                  {func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "tayfam_at", Value: e.appTok}) }, http.StatusUnauthorized},
		"uygulama çerezinde admin token'ı": {func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "tayfam_at", Value: e.adminTok}) }, http.StatusUnauthorized},
		"Authorization başlığı":            {func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+e.adminTok) }, http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			e.identity.gotToken = ""
			r := e.req(http.MethodGet, "/auth/me", "")
			tc.mod(r)
			w := e.do(r)
			if w.Code != tc.code {
				t.Fatalf("kod = %d %s", w.Code, w.Body)
			}
			if tc.code == http.StatusOK && e.identity.gotToken != e.adminTok {
				t.Fatal("admin token'ı identity'ye iletilmedi")
			}
			if tc.code != http.StatusOK && e.identity.gotToken != "" {
				t.Fatal("identity çağrıldı")
			}
		})
	}
}

// TestMeRemovedAdmin: identity no longer knows the administrator; the
// cookie is removed.
func TestMeRemovedAdmin(t *testing.T) {
	e := setup(t)
	e.identity.getErr = status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	r := e.req(http.MethodGet, "/auth/me", "")
	r.AddCookie(&http.Cookie{Name: TokenCookie, Value: e.adminTok})
	w := e.do(r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("kod = %d", w.Code)
	}
	if c := cookie(w, TokenCookie); c == nil || c.MaxAge >= 0 {
		t.Fatal("çerez silinmedi")
	}
}

func TestSignOut(t *testing.T) {
	e := setup(t)
	w := e.do(e.req(http.MethodPost, "/auth/sign-out", ""))
	if w.Code != http.StatusNoContent {
		t.Fatalf("kod = %d", w.Code)
	}
	if c := cookie(w, TokenCookie); c == nil || c.MaxAge >= 0 || !c.Secure || !c.HttpOnly {
		t.Fatalf("çerez: %+v", c)
	}
}

// TestRoutesMatchContract keeps the router and api/openapi/admin.yaml in
// step (CLAUDE.md, rule 7).
func TestRoutesMatchContract(t *testing.T) {
	e := setup(t)
	spec, err := os.ReadFile("../../../api/openapi/admin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/\S*):$`)
	opRe := regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	var path string
	ops := 0
	for line := range strings.SplitSeq(string(spec), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			path = m[1]
			continue
		}
		m := opRe.FindStringSubmatch(line)
		if m == nil || path == "" {
			continue
		}
		ops++
		p := strings.NewReplacer("{id}", tayfaID, "{file}", "panel.css").Replace(path)
		w := e.do(e.req(strings.ToUpper(m[1]), p, "{}"))
		if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
			t.Errorf("sözleşmedeki %s %s sunulmuyor (%d)", m[1], path, w.Code)
		}
	}
	if ops != 26 {
		t.Fatalf("sözleşmede %d işlem bulundu", ops)
	}
}

// TestProblemTypesDocumented: every problem type the router answers with is
// listed under x-problem-types in api/openapi/admin.yaml.
func TestProblemTypesDocumented(t *testing.T) {
	spec, err := os.ReadFile("../../../api/openapi/admin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, list, ok := strings.Cut(string(spec), "x-problem-types:")
	if !ok {
		t.Fatal("x-problem-types yok")
	}
	documented := map[string]bool{"not-found": true}
	for _, m := range regexp.MustCompile(`(?m)^\s+- ([a-z][a-z-]*)$`).FindAllStringSubmatch(list, -1) {
		documented[m[1]] = true
	}
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	used := regexp.MustCompile(`WriteProblem\(w, r, http\.Status\w+, "([a-z-]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(used) < 5 {
		t.Fatalf("yalnızca %d kullanım bulundu; ayrıştırma bozuk olabilir", len(used))
	}
	for _, m := range used {
		if !documented[m[1]] {
			t.Errorf("%s sözleşmede yok", m[1])
		}
	}
}
