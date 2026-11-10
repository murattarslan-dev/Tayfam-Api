package rest

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/gateway/app/clientversion"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

const versionHeader = clientversion.Header

func testVersions(t *testing.T) clientversion.Policy {
	t.Helper()
	p, err := clientversion.NewPolicy(map[clientversion.Platform]clientversion.RawRange{
		clientversion.Android: {Min: "1.2.0", Latest: "1.5.0"},
		clientversion.IOS:     {Min: "1.1.0"},
		// web: no minimum.
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// versionRouter is the full router (identity, core, chat) with the test
// version policy.
func versionRouter(t *testing.T, burst int) http.Handler {
	t.Helper()
	return NewRouter(Deps{
		Version: "test",
		Core:    testCoreDeps(&fakeCore{}, 100),
		Chat:    testChatDeps(&fakeChat{}),
		Auth: AuthDeps{
			Auth: &fakeIdentity{}, Registration: &fakeIdentity{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			TrustCFConnectingIP: true,
			SignInLimit:         ratelimit.New(60, 100), RefreshLimit: ratelimit.New(60, 100),
			Now: func() time.Time { return t0 },
		},
		Client: ClientDeps{
			Versions:            testVersions(t),
			ConfigLimit:         ratelimit.New(60, burst),
			TrustCFConnectingIP: true,
		},
		AllowedOrigins: []string{"https://tayfam.app"},
	})
}

// TestClientConfigOpen: GET /v1/client-config answers without a token, with
// each platform's range and nothing else (issue #114).
func TestClientConfigOpen(t *testing.T) {
	h := versionRouter(t, 100)
	w := do(h, "GET", "/v1/client-config", "", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("token olmadan: %d %s", w.Code, w.Body)
	}
	var res ClientConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	want := map[clientversion.Platform]PlatformVersions{
		"android": {MinSupportedVersion: "1.2.0", LatestVersion: "1.5.0"},
		"ios":     {MinSupportedVersion: "1.1.0", LatestVersion: "1.1.0"},
		"web":     {},
	}
	if len(res.Platforms) != len(want) {
		t.Fatalf("platformlar: %+v", res.Platforms)
	}
	for p, v := range want {
		if res.Platforms[p] != v {
			t.Errorf("%s = %+v, want %+v", p, res.Platforms[p], v)
		}
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "max-age") {
		t.Errorf("Cache-Control = %q", w.Header().Get("Cache-Control"))
	}

	// An invalid token does not matter either: the route reads none.
	w = do(h, "GET", "/v1/client-config", "", map[string]string{"Authorization": "Bearer BAD"})
	if w.Code != http.StatusOK {
		t.Errorf("geçersiz token ile: %d", w.Code)
	}

	// The zero policy reports all three platforms with no versions.
	w = do(NewRouter(Deps{}), "GET", "/v1/client-config", "", nil)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"platforms":{"android":{},"ios":{},"web":{}}}` {
		t.Errorf("boş yapılandırma: %d %s", w.Code, w.Body)
	}
}

// TestOtherRoutesStillNeedToken: opening /v1/client-config does not open
// anything else; token-checked REST routes and the WebSocket still answer
// 401 without a token.
func TestOtherRoutesStillNeedToken(t *testing.T) {
	h := versionRouter(t, 100)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/me/sessions"},
		{"GET", "/v1/tayfas"},
		{"GET", "/v1/notifications"},
		{"POST", "/v1/tayfas"},
	} {
		hdr := map[string]string{clientHeader: "android", versionHeader: "android/1.5.0"}
		if w := do(h, tc.method, tc.path, "{}", hdr); w.Code != http.StatusUnauthorized || problemType(t, w) != "unauthenticated" {
			t.Errorf("%s %s token olmadan: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
	w := do(h, "GET", "/v1/realtime", "", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", versionHeader: "android/1.5.0"})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("WebSocket token olmadan: %d", w.Code)
	}
}

// TestOpenOperationsPinned keeps the list of operations that take no token
// (security: []) fixed. Adding one is a sensitive change (CLAUDE.md rule
// 8): it must be argued in its PR and added here.
func TestOpenOperationsPinned(t *testing.T) {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	opRe := regexp.MustCompile(`^      operationId: (\S+)$`)
	var open []string
	op := ""
	for line := range strings.SplitSeq(string(spec), "\n") {
		if m := opRe.FindStringSubmatch(line); m != nil {
			op = m[1]
			continue
		}
		if strings.TrimSpace(line) == "security: []" && strings.HasPrefix(line, "      s") {
			open = append(open, op)
		}
	}
	slices.Sort(open)
	want := []string{"getClientConfig", "getStatus", "refresh", "signIn"}
	if !slices.Equal(open, want) {
		t.Errorf("token istemeyen işlemler = %v, want %v", open, want)
	}
}

func TestClientConfigRateLimited(t *testing.T) {
	h := versionRouter(t, 2)
	ip := map[string]string{"CF-Connecting-IP": "203.0.113.7"}
	for i := range 2 {
		if w := do(h, "GET", "/v1/client-config", "", ip); w.Code != http.StatusOK {
			t.Fatalf("istek %d: %d", i, w.Code)
		}
	}
	w := do(h, "GET", "/v1/client-config", "", ip)
	if w.Code != http.StatusTooManyRequests || problemType(t, w) != "rate-limited" {
		t.Errorf("sınır aşılınca: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/v1/client-config", "", map[string]string{"CF-Connecting-IP": "203.0.113.8"}); w.Code != http.StatusOK {
		t.Errorf("başka IP: %d", w.Code)
	}
}

// TestClientVersionEnforced: REST requests from a build below its
// platform's minimum get client-outdated (426) before anything else, even
// the token check; the header is optional (ADR-0016).
func TestClientVersionEnforced(t *testing.T) {
	h := versionRouter(t, 100)
	for _, tc := range []struct {
		name    string
		version string // "" = no header
		code    int
		problem string
	}{
		{"başlık yok: kabul", "", http.StatusOK, ""},
		{"en düşük sürüm", "android/1.2.0", http.StatusOK, ""},
		{"yeni sürüm", "android/2.0.0", http.StatusOK, ""},
		{"eski android", "android/1.1.9", http.StatusUpgradeRequired, "client-outdated"},
		{"en düşüğün ön sürümü", "android/1.2.0-rc.1", http.StatusUpgradeRequired, "client-outdated"},
		{"eski ios", "ios/1.0.0", http.StatusUpgradeRequired, "client-outdated"},
		{"web'de en düşük yok", "web/0.0.1", http.StatusOK, ""},
		{"bozuk başlık", "android/1.2", http.StatusBadRequest, "invalid-client-version"},
		{"bilinmeyen platform", "desktop/1.0.0", http.StatusBadRequest, "invalid-client-version"},
	} {
		hdr := map[string]string{"Authorization": "Bearer USERTOKEN", clientHeader: "android"}
		if tc.version != "" {
			hdr[versionHeader] = tc.version
		}
		w := do(h, "GET", "/v1/me/sessions", "", hdr)
		if w.Code != tc.code || (tc.problem != "" && problemType(t, w) != tc.problem) {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
	}

	// An old build is refused before the token is looked at.
	w := do(h, "GET", "/v1/me/sessions", "", map[string]string{versionHeader: "android/1.0.0"})
	if w.Code != http.StatusUpgradeRequired || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("token olmadan eski sürüm: %d", w.Code)
	}
	// So is a state-changing request.
	w = do(h, "POST", "/v1/tayfas", "{}", map[string]string{"Authorization": "Bearer USERTOKEN", clientHeader: "android", versionHeader: "android/1.0.0"})
	if w.Code != http.StatusUpgradeRequired {
		t.Errorf("POST eski sürüm: %d", w.Code)
	}
	// Two headers are refused rather than one picked.
	r := httptest.NewRequest(http.MethodGet, "/v1/me/sessions", nil)
	r.Header.Set("Authorization", "Bearer USERTOKEN")
	r.Header.Add(versionHeader, "android/1.0.0")
	r.Header.Add(versionHeader, "android/2.0.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("iki başlık: %d", rec.Code)
	}
}

// TestClientVersionNeverRefused: /v1/status and /v1/client-config answer
// any build, and a CORS preflight is not checked.
func TestClientVersionNeverRefused(t *testing.T) {
	h := versionRouter(t, 100)
	for _, path := range []string{"/v1/status", "/v1/client-config"} {
		for _, v := range []string{"android/0.0.1", "android/bozuk"} {
			if w := do(h, "GET", path, "", map[string]string{versionHeader: v}); w.Code != http.StatusOK {
				t.Errorf("%s %s: %d", path, v, w.Code)
			}
		}
	}
	w := do(h, "OPTIONS", "/v1/me/sessions", "", map[string]string{
		"Origin": "https://tayfam.app", "Access-Control-Request-Method": "GET",
		"Access-Control-Request-Headers": versionHeader,
	})
	if w.Code != http.StatusNoContent || !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), versionHeader) {
		t.Errorf("preflight: %d %q", w.Code, w.Header().Get("Access-Control-Allow-Headers"))
	}
	// A refused web request still carries CORS headers, so the web app can
	// read client-outdated.
	w = do(h, "GET", "/v1/me/sessions", "", map[string]string{"Origin": "https://tayfam.app", versionHeader: "android/1.0.0"})
	if w.Code != http.StatusUpgradeRequired || w.Header().Get("Access-Control-Allow-Origin") != "https://tayfam.app" {
		t.Errorf("web reddi: %d %q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}
}

// TestRealtimeClientVersion: an old build's WebSocket handshake is refused
// with client-outdated and never reaches chat; a supported one and one
// without the header (browsers cannot set it) connect.
func TestRealtimeClientVersion(t *testing.T) {
	up := &fakeRealtime{}
	chat := httptest.NewServer(up)
	t.Cleanup(chat.Close)
	u, _ := url.Parse(chat.URL)
	d := testChatDeps(&fakeChat{})
	d.RealtimeURL = u
	gw := httptest.NewServer(NewRouter(Deps{
		Version: "test", Chat: d, AllowedOrigins: []string{"https://tayfam.app"},
		Client: ClientDeps{Versions: testVersions(t)},
	}))
	t.Cleanup(gw.Close)

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer USERTOKEN")
	hdr.Set(versionHeader, "android/1.1.0")
	if c, code, err := dialRealtime(t, gw, "", hdr); err == nil || code != http.StatusUpgradeRequired {
		if c != nil {
			_ = c.CloseNow()
		}
		t.Fatalf("eski sürüm: %d %v", code, err)
	}
	if up.last() != nil {
		t.Fatal("eski sürümün isteği chat'e ulaştı")
	}

	for _, v := range []string{"android/1.2.0", ""} {
		hdr := http.Header{}
		hdr.Set("Authorization", "Bearer USERTOKEN")
		if v != "" {
			hdr.Set(versionHeader, v)
		}
		c, code, err := dialRealtime(t, gw, "", hdr)
		if err != nil {
			t.Fatalf("%q: %d %v", v, code, err)
		}
		readReady(t, c)
		_ = c.CloseNow()
	}
}
