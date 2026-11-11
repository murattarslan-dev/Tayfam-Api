package ops

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const testPort = 41234

// testEnv is a server plus the client-side state a browser would hold.
type testEnv struct {
	t      *testing.T
	srv    *Server
	h      http.Handler
	cookie *http.Cookie
	log    *bytes.Buffer
	repo   string
}

func newTestEnv(t *testing.T, repo string) *testEnv {
	t.Helper()
	if repo == "" {
		repo = t.TempDir()
		for _, d := range []string{"deploy/apps/dev", "deploy/apps/prod"} {
			if err := os.MkdirAll(filepath.Join(repo, d), 0o750); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(repo, ".sops.yaml"), []byte("creation_rules: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var log bytes.Buffer
	srv, err := New(Config{
		Repo: repo, Port: testPort,
		Logger:  slog.New(slog.NewJSONHandler(&log, nil)),
		OpenDir: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{t: t, srv: srv, h: srv.Handler(), log: &log, repo: repo}
}

func (e *testEnv) do(req *http.Request) *httptest.ResponseRecorder {
	if req.Host == "example.com" { // httptest default
		req.Host = "127.0.0.1:" + strconv.Itoa(testPort)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// login exchanges the launch token for the session cookie.
func (e *testEnv) login() {
	e.t.Helper()
	u, _ := url.Parse(e.srv.LaunchURL())
	rec := e.do(httptest.NewRequest(http.MethodGet, u.RequestURI(), nil))
	if rec.Code != http.StatusSeeOther {
		e.t.Fatalf("launch: %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			e.cookie = c
		}
	}
	if e.cookie == nil {
		e.t.Fatal("no session cookie")
	}
}

func (e *testEnv) csrf() string { return hex.EncodeToString(e.srv.csrf) }

func (e *testEnv) origin() string { return "http://127.0.0.1:" + strconv.Itoa(testPort) }

func (e *testEnv) postForm(path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", e.origin())
	req.AddCookie(e.cookie)
	return e.do(req)
}

func (e *testEnv) postKey(env string, key []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("csrf", e.csrf())
	_ = mw.WriteField("env", env)
	fw, _ := mw.CreateFormFile("key", "anahtar.json")
	_, _ = fw.Write(key)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/fcm", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", e.origin())
	req.AddCookie(e.cookie)
	return e.do(req)
}

func TestListenOnlyLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "[::]:0", "[::1]:0", "localhost:0", "192.168.1.10:0", "127.0.0.2:0", "nonsense"} {
		if ln, err := Listen(context.Background(), addr); err == nil {
			_ = ln.Close()
			t.Errorf("Listen(%q) accepted", addr)
		}
	}
	ln, err := Listen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if !strings.HasPrefix(ln.Addr().String(), "127.0.0.1:") {
		t.Fatalf("bound to %s", ln.Addr())
	}
}

func TestHostHeaderChecked(t *testing.T) {
	e := newTestEnv(t, "")
	e.login()
	for _, host := range []string{"evil.example", "evil.example:" + strconv.Itoa(testPort), "127.0.0.1:1", "127.0.0.1", "[::1]:" + strconv.Itoa(testPort)} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		req.AddCookie(e.cookie)
		if rec := e.do(req); rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q: %d", host, rec.Code)
		}
	}
	for _, host := range []string{"127.0.0.1:" + strconv.Itoa(testPort), "localhost:" + strconv.Itoa(testPort)} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		req.AddCookie(e.cookie)
		if rec := e.do(req); rec.Code != http.StatusOK {
			t.Errorf("Host %q: %d", host, rec.Code)
		}
	}
}

func TestLaunchTokenIsOneTime(t *testing.T) {
	e := newTestEnv(t, "")
	for _, p := range []string{"/", "/r2", "/fcm", "/export", "/?t=00", "/?t=" + strings.Repeat("ab", 32)} {
		if rec := e.do(httptest.NewRequest(http.MethodGet, p, nil)); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without session: %d", p, rec.Code)
		}
	}
	// A forged cookie is refused too.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: strings.Repeat("00", 32)})
	if rec := e.do(req); rec.Code != http.StatusForbidden {
		t.Errorf("forged cookie: %d", rec.Code)
	}

	u, _ := url.Parse(e.srv.LaunchURL())
	rec := e.do(httptest.NewRequest(http.MethodGet, u.RequestURI(), nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("launch: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	var c *http.Cookie
	for _, x := range rec.Result().Cookies() {
		c = x
	}
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie: %+v", c)
	}
	if strings.Contains(u.RawQuery, c.Value) {
		t.Fatal("cookie value equals the launch token")
	}
	// Second use of the link fails.
	if rec := e.do(httptest.NewRequest(http.MethodGet, u.RequestURI(), nil)); rec.Code != http.StatusForbidden {
		t.Fatalf("launch link reused: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	if rec := e.do(req); rec.Code != http.StatusOK {
		t.Fatalf("with cookie: %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newTestEnv(t, "")
	e.login()
	for _, p := range []string{"/", "/r2?env=prod", "/static/style.css", "/nope"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.AddCookie(e.cookie)
		rec := e.do(req)
		h := rec.Header()
		if h.Get("Cache-Control") != "no-store" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "default-src 'self'") ||
			!strings.Contains(h.Get("Content-Security-Policy"), "script-src 'none'") ||
			h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: headers %v", p, h)
		}
	}
	// Pages carry no script and forms disable autocomplete.
	req := httptest.NewRequest(http.MethodGet, "/r2", nil)
	req.AddCookie(e.cookie)
	body := e.do(req).Body.String()
	if strings.Contains(body, "<script") || !strings.Contains(body, `autocomplete="off"`) {
		t.Errorf("r2 page: script or autocomplete")
	}
}

func TestPostNeedsOriginAndCSRF(t *testing.T) {
	e := newTestEnv(t, "")
	e.login()
	good := url.Values{"csrf": {e.csrf()}, "env": {"dev"}}
	cases := []struct {
		name   string
		origin string
		site   string
		form   url.Values
		cookie bool
	}{
		{"no origin", "", "", good, true},
		{"foreign origin", "http://evil.example", "", good, true},
		{"other port", "http://127.0.0.1:1", "", good, true},
		{"null origin", "null", "", good, true},
		{"cross-site fetch", e.origin(), "cross-site", good, true},
		{"no csrf", e.origin(), "", url.Values{"env": {"dev"}}, true},
		{"wrong csrf", e.origin(), "", url.Values{"csrf": {strings.Repeat("00", 32)}, "env": {"dev"}}, true},
		{"no cookie", e.origin(), "", good, false},
	}
	for _, path := range []string{"/r2", "/export", "/open"} {
		for _, c := range cases {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(c.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if c.origin != "" {
				req.Header.Set("Origin", c.origin)
			}
			if c.site != "" {
				req.Header.Set("Sec-Fetch-Site", c.site)
			}
			if c.cookie {
				req.AddCookie(e.cookie)
			}
			if rec := e.do(req); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: %d", path, c.name, rec.Code)
			}
		}
	}
	// The accepted request reaches the handler (/open answers 204).
	if rec := e.postForm("/open", good); rec.Code != http.StatusNoContent {
		t.Errorf("/open: %d", rec.Code)
	}
	// Repeated or unknown environments are refused.
	if rec := e.postForm("/open", url.Values{"csrf": {e.csrf()}, "env": {"../../etc"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad env: %d", rec.Code)
	}
	if rec := e.postForm("/open", url.Values{"csrf": {e.csrf(), e.csrf()}, "env": {"dev"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("duplicate field: %d", rec.Code)
	}
}

func TestFCMNeedsCSRFFirst(t *testing.T) {
	e := newTestEnv(t, "")
	e.login()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("key", "a.json")
	_, _ = fw.Write([]byte(`{"type":"service_account"}`))
	_ = mw.WriteField("csrf", e.csrf())
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/fcm", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", e.origin())
	req.AddCookie(e.cookie)
	if rec := e.do(req); rec.Code != http.StatusForbidden {
		t.Fatalf("csrf after file: %d", rec.Code)
	}
	// Oversized keys are refused before anything runs.
	if rec := e.postKey("dev", bytes.Repeat([]byte("a"), maxKeyFile+1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized: %d", rec.Code)
	}
}

func TestR2FormValidationHidesValues(t *testing.T) {
	e := newTestEnv(t, "")
	e.login()
	secret := "BAD-SECRET-VALUE-" + strings.Repeat("x", 20)
	rec := e.postForm("/r2", url.Values{
		"csrf": {e.csrf()}, "env": {"dev"},
		"account": {strings.Repeat("a", 32)}, "bucket": {"tayfam-dev-private"},
		"key_id": {strings.Repeat("K", 32)}, "secret": {secret},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), secret) || strings.Contains(e.log.String(), secret) {
		t.Fatal("secret echoed")
	}
}

func TestProdBlockedWhileRecipientIsPlaceholder(t *testing.T) {
	e := newTestEnv(t, "")
	if err := os.WriteFile(filepath.Join(e.repo, ".sops.yaml"), []byte("age: YER_TUTUCU_PROD_AGE_ACIK_ANAHTARI\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.login()
	rec := e.postForm("/export", url.Values{"csrf": {e.csrf()}, "env": {"prod"}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "vds-kurulumu") {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestParseForm(t *testing.T) {
	f, err := parseForm([]byte("a=1%2B2+3&b=%C3%A7&c="))
	if err != nil {
		t.Fatal(err)
	}
	if string(f["a"]) != "1+2 3" || string(f["b"]) != "ç" || len(f["c"]) != 0 {
		t.Fatalf("%q", f)
	}
	for _, bad := range []string{"a=%", "a=%zz", "a=1&a=2", "%G0=1"} {
		if _, err := parseForm([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := readBody(strings.NewReader(strings.Repeat("x", 11)), 10); err == nil {
		t.Error("readBody over limit accepted")
	}
	b, err := readBody(io.LimitReader(strings.NewReader("abc"), 3), 10)
	if err != nil || string(b) != "abc" {
		t.Errorf("readBody: %q %v", b, err)
	}
}

func TestRedact(t *testing.T) {
	out := redact([]byte("a SECRETVALUE b\n-----BEGIN PRIVATE KEY-----\nok\n"), [][]byte{[]byte("SECRETVALUE")})
	if out != "a [gizlendi] b\nok" {
		t.Fatalf("%q", out)
	}
}
