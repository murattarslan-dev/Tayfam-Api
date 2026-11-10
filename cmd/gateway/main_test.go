package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/service"
)

const testAUD = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// run starts gateway with env and returns its HTTP address. Upstream
// addresses are never dialled: gRPC clients connect lazily.
func run(t *testing.T, extra map[string]string) (string, error) {
	t.Helper()
	env := map[string]string{
		"TAYFAM_ENV": "local", "HTTP_ADDR": "127.0.0.1:0", "OPS_ADDR": "127.0.0.1:0",
		"IDENTITY_GRPC_ADDR": "127.0.0.1:1", "CORE_GRPC_ADDR": "127.0.0.1:1", "CHAT_GRPC_ADDR": "127.0.0.1:1",
		"CHAT_REALTIME_URL": "http://127.0.0.1:1", "AUTH_ISSUER": "tayfam-identity", "AUTH_AUDIENCE": "tayfam-api",
		"ADMIN_HOST": "admin.tayfam.test",
	}
	for k, v := range extra {
		env[k] = v
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan map[string]string, 1)
	errc := make(chan error, 1)
	go func() { errc <- service.Run(ctx, "gateway", lookup, io.Discard, setup, started) }()
	select {
	case addrs := <-started:
		t.Cleanup(func() {
			cancel()
			if err := <-errc; err != nil {
				t.Errorf("gateway durdurulamadı: %v", err)
			}
		})
		return addrs["http"], nil
	case err := <-errc:
		cancel()
		return "", err
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("gateway başlamadı")
		return "", nil
	}
}

func get(t *testing.T, addr, host, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestPanelClosedWithoutCloudflareAccess: without Cloudflare Access
// configuration the admin router is not built, every request to the admin
// host gets 404 and the app API is unaffected (issue #104).
func TestPanelClosedWithoutCloudflareAccess(t *testing.T) {
	addr, err := run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/auth/nonce", "/auth/me", "/v1/status", "/"} {
		if code := get(t, addr, "admin.tayfam.test", path); code != http.StatusNotFound {
			t.Errorf("admin adresi %s: %d", path, code)
		}
	}
	if code := get(t, addr, "api.tayfam.test", "/v1/status"); code != http.StatusOK {
		t.Fatalf("uygulama API'si: %d", code)
	}
}

func TestPanelRequiresAccessJWT(t *testing.T) {
	addr, err := run(t, map[string]string{"CF_ACCESS_TEAM_DOMAIN": "tayfam-test.cloudflareaccess.com", "CF_ACCESS_AUD": testAUD,
		"ADMIN_GOOGLE_CLIENT_ID": "panel-test.apps.googleusercontent.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/auth/nonce", "/", "/giris", "/static/panel.css", "/tayfalar"} {
		if code := get(t, addr, "admin.tayfam.test", path); code != http.StatusForbidden {
			t.Fatalf("Access JWT'siz istek %s: %d", path, code)
		}
	}
	if code := get(t, addr, "api.tayfam.test", "/auth/nonce"); code != http.StatusNotFound {
		t.Fatalf("API adresinde panel yolu: %d", code)
	}
}

func TestPanelConfigMustBeComplete(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"yalnızca takım":       {"CF_ACCESS_TEAM_DOMAIN": "tayfam-test.cloudflareaccess.com"},
		"yalnızca AUD":         {"CF_ACCESS_AUD": testAUD},
		"geçersiz takım":       {"CF_ACCESS_TEAM_DOMAIN": "tayfam.example.com", "CF_ACCESS_AUD": testAUD},
		"geçersiz AUD":         {"CF_ACCESS_TEAM_DOMAIN": "tayfam-test.cloudflareaccess.com", "CF_ACCESS_AUD": "abc"},
		"Google istemcisi yok": {"CF_ACCESS_TEAM_DOMAIN": "tayfam-test.cloudflareaccess.com", "CF_ACCESS_AUD": testAUD},
	} {
		if _, err := run(t, env); err == nil || !strings.Contains(err.Error(), "kurulum") {
			t.Errorf("%s: başladı (%v)", name, err)
		}
	}
}

func getWithVersion(t *testing.T, addr, host, path, version string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("X-Tayfam-Client-Version", version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestClientVersionsFromEnv: the minimum is read from the environment and
// enforced on the app API only; the admin panel's host is outside the
// check (issue #114, ADR-0016).
func TestClientVersionsFromEnv(t *testing.T) {
	addr, err := run(t, map[string]string{
		"CLIENT_MIN_VERSION_ANDROID": "1.2.0", "CLIENT_LATEST_VERSION_ANDROID": "1.3.0",
		"CF_ACCESS_TEAM_DOMAIN": "tayfam-test.cloudflareaccess.com", "CF_ACCESS_AUD": testAUD,
		"ADMIN_GOOGLE_CLIENT_ID": "panel-test.apps.googleusercontent.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if code := getWithVersion(t, addr, "api.tayfam.test", "/v1/me/sessions", "android/1.1.0"); code != http.StatusUpgradeRequired {
		t.Errorf("eski sürüm: %d", code)
	}
	if code := getWithVersion(t, addr, "api.tayfam.test", "/v1/me/sessions", "android/1.2.0"); code != http.StatusUnauthorized {
		t.Errorf("desteklenen sürüm token olmadan: %d", code)
	}
	for _, path := range []string{"/v1/status", "/v1/client-config"} {
		if code := getWithVersion(t, addr, "api.tayfam.test", path, "android/1.1.0"); code != http.StatusOK {
			t.Errorf("%s eski sürümle: %d", path, code)
		}
	}
	// The panel answers as it would without the header: Cloudflare
	// Access first.
	if code := getWithVersion(t, addr, "admin.tayfam.test", "/", "android/1.1.0"); code != http.StatusForbidden {
		t.Errorf("yönetim paneli adresi: %d", code)
	}
}

// TestClientVersionsInvalidStopsService: a value the gateway would misread
// stops it; unset values do not (no minimum, nothing refused).
func TestClientVersionsInvalidStopsService(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"bozuk en düşük": {"CLIENT_MIN_VERSION_IOS": "1.2"},
		"v öneki":        {"CLIENT_MIN_VERSION_ANDROID": "v1.0.0"},
		"bozuk son":      {"CLIENT_LATEST_VERSION_WEB": "son"},
		"son < en düşük": {"CLIENT_MIN_VERSION_ANDROID": "2.0.0", "CLIENT_LATEST_VERSION_ANDROID": "1.0.0"},
	} {
		if _, err := run(t, env); err == nil || !strings.Contains(err.Error(), "istemci sürümleri") {
			t.Errorf("%s: başladı (%v)", name, err)
		}
	}
	addr, err := run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code := getWithVersion(t, addr, "api.tayfam.test", "/v1/me/sessions", "android/0.0.1"); code != http.StatusUnauthorized {
		t.Errorf("en düşük tanımsızken: %d", code)
	}
}

// TestDevOrigins: WEB_DEV_ORIGINS takes local development origins only,
// exactly as a browser writes them, and nothing in prod (ADR-0017).
func TestDevOrigins(t *testing.T) {
	got, err := devOrigins("dev", " http://localhost:3000, http://127.0.0.1:3000 ,")
	if err != nil || len(got) != 2 || got[0] != "http://localhost:3000" || got[1] != "http://127.0.0.1:3000" {
		t.Fatalf("geçerli liste: %v %v", got, err)
	}
	for _, env := range []string{"local", "dev", "prod"} {
		if got, err := devOrigins(env, ""); err != nil || len(got) != 0 {
			t.Errorf("%s, boş: %v %v", env, got, err)
		}
	}
	if _, err := devOrigins("prod", "http://localhost:3000"); err == nil {
		t.Error("prod'da localhost kabul edildi")
	}
	for _, o := range []string{
		"https://tayfam.app", "http://tayfam.app", "https://localhost:3000", "http://localhost",
		"http://localhost:3000/", "http://localhost:3000/yol", "http://localhost:0", "http://localhost:65536",
		"http://localhost:03000", "http://localhost:+3000", "http://localhost:3000.evil.example",
		"http://evil.localhost:3000", "http://user@localhost:3000", "http://[::1]:3000", "*", "null",
		"http://LOCALHOST:3000", "http://0.0.0.0:3000",
	} {
		if _, err := devOrigins("dev", o); err == nil {
			t.Errorf("%q kabul edildi", o)
		}
	}
}

// TestWebDevOriginsFromEnv: a remote origin or any origin in prod stops
// the gateway; a local one outside prod gets CORS without credentials.
func TestWebDevOriginsFromEnv(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"uzak kaynak":        {"WEB_DEV_ORIGINS": "https://tayfam.app"},
		"prod'da localhost":  {"TAYFAM_ENV": "prod", "WEB_DEV_ORIGINS": "http://localhost:3000"},
		"prod'da web sürümü": {"TAYFAM_ENV": "prod", "CLIENT_MIN_VERSION_WEB": "0.1.0"},
		"prod'da web son":    {"TAYFAM_ENV": "prod", "CLIENT_LATEST_VERSION_WEB": "0.1.0"},
	} {
		if _, err := run(t, env); err == nil || !strings.Contains(err.Error(), "kurulum") {
			t.Errorf("%s: başladı (%v)", name, err)
		}
	}
	addr, err := run(t, map[string]string{"WEB_DEV_ORIGINS": "http://localhost:3000"})
	if err != nil {
		t.Fatal(err)
	}
	for origin, want := range map[string]string{"http://localhost:3000": "http://localhost:3000", "https://tayfam.app": ""} {
		req, err := http.NewRequest(http.MethodOptions, "http://"+addr+"/v1/auth/sign-in", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "api.tayfam.test"
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != want || resp.Header.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%s: %q %q", origin, got, resp.Header.Get("Access-Control-Allow-Credentials"))
		}
	}
}
