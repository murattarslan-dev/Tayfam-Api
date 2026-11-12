package googleauth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/googleauth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/googleauth/googleauthtest"
)

const scope = "https://www.googleapis.com/auth/playintegrity"

func source(t *testing.T, f *googleauthtest.Server, now func() time.Time) *googleauth.TokenSource {
	t.Helper()
	acct, err := googleauth.ParseServiceAccount(f.Account(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := googleauth.New(googleauth.Config{Account: acct, Scope: scope, Transport: f.Transport(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTokenCachedUntilNearExpiry(t *testing.T) {
	f := googleauthtest.New(t)
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	s := source(t, f, func() time.Time { return now })
	ctx := context.Background()

	tok, err := s.Token(ctx)
	if err != nil || tok != googleauthtest.AccessToken(1) {
		t.Fatalf("ilk token = %q %v", tok, err)
	}
	if tok, _ := s.Token(ctx); tok != googleauthtest.AccessToken(1) || f.Issued() != 1 {
		t.Fatalf("önbellek kullanılmadı: %q %d", tok, f.Issued())
	}
	now = now.Add(56 * time.Minute) // within 5 minutes of the 1h expiry
	if tok, _ := s.Token(ctx); tok != googleauthtest.AccessToken(2) {
		t.Fatalf("yenilenmedi: %q", tok)
	}
	s.Forget(googleauthtest.AccessToken(2))
	if tok, _ := s.Token(ctx); tok != googleauthtest.AccessToken(3) {
		t.Fatalf("unutulan token tekrar verildi: %q", tok)
	}
	for _, sc := range f.Scopes() {
		if sc != scope {
			t.Fatalf("kapsam = %q", sc)
		}
	}
	if h := f.Hosts(); h["oauth2.googleapis.com"] != 3 || len(h) != 1 {
		t.Fatalf("istek gidilen yerler = %v", h)
	}
}

func TestTokenEndpointFailure(t *testing.T) {
	f := googleauthtest.New(t)
	s := source(t, f, nil)
	f.FailTokens(http.StatusServiceUnavailable)
	_, err := s.Token(context.Background())
	if !errors.Is(err, googleauth.ErrUnavailable) {
		t.Fatalf("hata = %v", err)
	}
	f.FailTokens(0)
	if _, err := s.Token(context.Background()); err != nil {
		t.Fatalf("düzelince: %v", err)
	}
}

// TestParseServiceAccountRejects names the field, never its value.
func TestParseServiceAccountRejects(t *testing.T) {
	f := googleauthtest.New(t)
	good := string(f.Account(t))
	for name, raw := range map[string]string{
		"json":        "{",
		"type":        strings.Replace(good, `"service_account"`, `"user"`, 1),
		"project_id":  strings.Replace(good, `"project_id":"`+googleauthtest.Project+`"`, `"project_id":"X"`, 1),
		"token_uri":   strings.Replace(good, "https://oauth2.googleapis.com/token", "https://example.com/token", 1),
		"private_key": strings.Replace(good, "BEGIN PRIVATE KEY", "BEGIN BOZUK", 1),
	} {
		_, err := googleauth.ParseServiceAccount([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), "BEGIN") {
			t.Errorf("%s: %v", name, err)
		}
	}
	acct, err := googleauth.ParseServiceAccount([]byte(good))
	if err != nil || acct.ProjectID() != googleauthtest.Project {
		t.Fatal(err)
	}
	if s := fmt.Sprint(acct); strings.Contains(s, "BEGIN") || strings.Contains(s, "test@") {
		t.Fatalf("hesap yazdırıldı: %s", s)
	}
}

func TestNewChecksScope(t *testing.T) {
	f := googleauthtest.New(t)
	acct, err := googleauth.ParseServiceAccount(f.Account(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range []string{"", "playintegrity", "https://example.com/auth/x"} {
		if _, err := googleauth.New(googleauth.Config{Account: acct, Scope: sc}); err == nil {
			t.Errorf("%q kabul edildi", sc)
		}
	}
}
