package service

import (
	"context"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/config"
)

func testRuntime(name, env string, vars map[string]string) *Runtime {
	return &Runtime{Name: name, Env: env, Config: config.New(lookup(vars)), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestServiceIdentityLocalMayBeOff(t *testing.T) {
	rt := testRuntime("core", "local", nil)
	si, err := rt.ServiceIdentity()
	if err != nil || si.Verifier != nil {
		t.Fatalf("yerelde kapalı olmalı: %+v %v", si, err)
	}
}

func TestServiceIdentityRequiredOutsideLocal(t *testing.T) {
	for _, env := range []string{"dev", "prod"} {
		t.Run(env, func(t *testing.T) {
			rt := testRuntime("core", env, nil)
			_, err := rt.ServiceIdentity()
			if err == nil || !strings.Contains(err.Error(), "SERVICE_TOKEN_ISSUER") {
				t.Fatalf("servis token'ı %s ortamında kapatılabildi: %v", env, err)
			}
		})
	}
}

func TestServiceTokenRequiredOutsideLocal(t *testing.T) {
	rt := testRuntime("identity", "prod", nil)
	if _, ok := rt.ServiceToken("core"); ok || rt.Config.Err() == nil {
		t.Fatal("SERVICE_TOKEN_FILE_CORE prod'da zorunlu olmalı")
	}
	rt = testRuntime("identity", "local", nil)
	if _, ok := rt.ServiceToken("core"); ok || rt.Config.Err() != nil {
		t.Fatal("yerelde isteğe bağlı olmalı")
	}
	rt = testRuntime("identity", "dev", map[string]string{"SERVICE_TOKEN_FILE_CORE": "/x/core-token"})
	if f, ok := rt.ServiceToken("core"); !ok || f != "/x/core-token" {
		t.Fatalf("dosya = %q", f)
	}
}

func TestServiceIdentityVerifiesClusterTokens(t *testing.T) {
	c := satest.New(t)
	dir := t.TempDir()
	// The stand-in cluster serves with httptest's certificate; it is the CA
	// the pod would get from kube-root-ca.crt.
	srv := httptest.NewTLSServer(nil)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	srv.Close()
	caFile := filepath.Join(dir, "ca.crt")
	tokFile := filepath.Join(dir, "token")
	if err := os.WriteFile(caFile, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokFile, []byte("api"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := testRuntime("core", "dev", map[string]string{
		"SERVICE_TOKEN_ISSUER":         satest.Issuer,
		"SERVICE_NAMESPACE":            "tayfam-dev",
		"SERVICE_TOKEN_JWKS_URL":       c.JWKSURL(),
		"SERVICE_TOKEN_CA_FILE":        caFile,
		"SERVICE_TOKEN_API_TOKEN_FILE": tokFile,
	})
	si, err := rt.ServiceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	sa := si.Account("tayfam-identity")
	if sa.Subject() != "system:serviceaccount:tayfam-dev:tayfam-identity" {
		t.Fatalf("hesap = %s", sa.Subject())
	}
	got, err := si.Verifier.Verify(context.Background(), c.ServiceToken(t, sa, "core.tayfam.internal"))
	if err != nil || got.Account != sa {
		t.Fatalf("%+v %v", got, err)
	}
	if c.LastBearer() != "api" {
		t.Fatal("JWKS isteğinde API sunucusu token'ı yok")
	}
	// A token for another service's audience does not pass here.
	if _, err := si.Verifier.Verify(context.Background(), c.ServiceToken(t, sa, "chat.tayfam.internal")); err == nil {
		t.Fatal("başka audience geçti")
	}
}

func TestServiceIdentityBadCA(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("sertifika değil"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := testRuntime("core", "prod", map[string]string{
		"SERVICE_TOKEN_ISSUER": satest.Issuer, "SERVICE_NAMESPACE": "tayfam",
		"SERVICE_TOKEN_CA_FILE": caFile,
	})
	if _, err := rt.ServiceIdentity(); err == nil {
		t.Fatal("geçersiz CA kabul edildi")
	}
	rt = testRuntime("core", "prod", map[string]string{
		"SERVICE_TOKEN_ISSUER": satest.Issuer, "SERVICE_NAMESPACE": "Geçersiz_NS",
		"SERVICE_TOKEN_CA_FILE": caFile,
	})
	if _, err := rt.ServiceIdentity(); err == nil {
		t.Fatal("geçersiz namespace kabul edildi")
	}
}
