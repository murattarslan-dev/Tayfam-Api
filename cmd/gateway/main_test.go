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
		t.Cleanup(func() { cancel(); <-errc })
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
	addr, err := run(t, map[string]string{"CF_ACCESS_TEAM_DOMAIN": "tayfam-test.cloudflareaccess.com", "CF_ACCESS_AUD": testAUD})
	if err != nil {
		t.Fatal(err)
	}
	if code := get(t, addr, "admin.tayfam.test", "/auth/nonce"); code != http.StatusForbidden {
		t.Fatalf("Access JWT'siz istek: %d", code)
	}
	if code := get(t, addr, "api.tayfam.test", "/auth/nonce"); code != http.StatusNotFound {
		t.Fatalf("API adresinde panel yolu: %d", code)
	}
}

func TestPanelConfigMustBeComplete(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"yalnızca takım": {"CF_ACCESS_TEAM_DOMAIN": "tayfam-test.cloudflareaccess.com"},
		"yalnızca AUD":   {"CF_ACCESS_AUD": testAUD},
		"geçersiz takım": {"CF_ACCESS_TEAM_DOMAIN": "tayfam.example.com", "CF_ACCESS_AUD": testAUD},
		"geçersiz AUD":   {"CF_ACCESS_TEAM_DOMAIN": "tayfam-test.cloudflareaccess.com", "CF_ACCESS_AUD": "abc"},
	} {
		if _, err := run(t, env); err == nil || !strings.Contains(err.Error(), "kurulum") {
			t.Errorf("%s: başladı (%v)", name, err)
		}
	}
}
