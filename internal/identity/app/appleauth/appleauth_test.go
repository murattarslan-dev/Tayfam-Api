package appleauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testClient  = "com.example.tayfam"
	testTeam    = "TEAM123456"
	testKeyID   = "KEY1234567"
	testSubject = "001234.abcdef.5678"
)

var now0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// fakeTokens stands in for oidc.Apple: the "identity token" is the subject.
type fakeTokens struct{ err error }

func (f fakeTokens) ExchangedSubject(_ context.Context, idToken string) (string, error) {
	return idToken, f.err
}

// apple is a stand-in for Apple's token and revoke endpoints.
type apple struct {
	t   *testing.T
	key *ecdsa.PrivateKey
	srv *httptest.Server

	tokenStatus  int
	tokenBody    string
	revokeStatus int

	tokenForms  []url.Values
	revokeForms []url.Values
}

func newApple(t *testing.T) (*apple, *Client) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &apple{
		t: t, key: key, tokenStatus: http.StatusOK, revokeStatus: http.StatusOK,
		tokenBody: `{"access_token":"AT","refresh_token":"RT","id_token":"` + testSubject + `","token_type":"Bearer","expires_in":3600}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		a.tokenForms = append(a.tokenForms, a.form(r))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.tokenStatus)
		_, _ = w.Write([]byte(a.tokenBody))
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		a.revokeForms = append(a.revokeForms, a.form(r))
		w.WriteHeader(a.revokeStatus)
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	c, err := New(Config{
		ClientID: testClient, TeamID: testTeam, KeyID: testKeyID, Key: key,
		HTTP: a.srv.Client(), Tokens: fakeTokens{}, Now: func() time.Time { return now0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	c.exchangeURL, c.revokeURL = a.srv.URL+"/token", a.srv.URL+"/revoke"
	return a, c
}

func (a *apple) form(r *http.Request) url.Values {
	a.t.Helper()
	if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		a.t.Errorf("Content-Type = %q", ct)
	}
	if err := r.ParseForm(); err != nil {
		a.t.Error(err)
	}
	return r.PostForm
}

// checkSecret verifies the client secret the way Apple does: ES256 by the
// Sign in with Apple key, issued by the team for the app, short-lived.
func (a *apple) checkSecret(secret string) {
	a.t.Helper()
	parts := strings.Split(secret, ".")
	if len(parts) != 3 {
		a.t.Fatalf("istemci sırrı %d parça", len(parts))
	}
	var hdr map[string]string
	var claims struct {
		Iss, Aud, Sub string
		Iat, Exp      int64
	}
	for i, v := range []any{&hdr, &claims} {
		raw, err := b64.DecodeString(parts[i])
		if err != nil {
			a.t.Fatal(err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			a.t.Fatal(err)
		}
	}
	if hdr["alg"] != "ES256" || hdr["kid"] != testKeyID {
		a.t.Errorf("başlık = %v", hdr)
	}
	if claims.Iss != testTeam || claims.Sub != testClient || claims.Aud != "https://appleid.apple.com" {
		a.t.Errorf("içerik = %+v", claims)
	}
	if claims.Iat != now0.Unix() || claims.Exp != now0.Add(5*time.Minute).Unix() {
		a.t.Errorf("süre = %d..%d", claims.Iat, claims.Exp)
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		a.t.Fatalf("imza: %v, %d bayt", err, len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&a.key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		a.t.Error("istemci sırrının imzası geçersiz")
	}
}

func TestRevokeByCode(t *testing.T) {
	a, c := newApple(t)
	if err := c.RevokeByCode(context.Background(), "kod-1", testSubject); err != nil {
		t.Fatal(err)
	}
	if len(a.tokenForms) != 1 || len(a.revokeForms) != 1 {
		t.Fatalf("istekler: token %d, iptal %d", len(a.tokenForms), len(a.revokeForms))
	}
	tf := a.tokenForms[0]
	if tf.Get("client_id") != testClient || tf.Get("code") != "kod-1" || tf.Get("grant_type") != "authorization_code" {
		t.Errorf("takas formu = %v", tf)
	}
	a.checkSecret(tf.Get("client_secret"))
	rf := a.revokeForms[0]
	if rf.Get("client_id") != testClient || rf.Get("token") != "RT" || rf.Get("token_type_hint") != "refresh_token" {
		t.Errorf("iptal formu = %v", rf)
	}
	a.checkSecret(rf.Get("client_secret"))
}

func TestRevokeFallsBackToAccessToken(t *testing.T) {
	a, c := newApple(t)
	a.tokenBody = `{"access_token":"AT","id_token":"` + testSubject + `"}`
	if err := c.RevokeByCode(context.Background(), "kod-1", testSubject); err != nil {
		t.Fatal(err)
	}
	if rf := a.revokeForms[0]; rf.Get("token") != "AT" || rf.Get("token_type_hint") != "access_token" {
		t.Errorf("iptal formu = %v", rf)
	}
}

// TestCodeRejected: the person has to sign in with Apple again. Nothing is
// revoked, least of all another user's tokens.
func TestCodeRejected(t *testing.T) {
	long := strings.Repeat("k", MaxCode+1)
	for name, tc := range map[string]struct {
		code, subject string
		prepare       func(*apple)
		wantToken     int
	}{
		"boş kod":     {"", testSubject, nil, 0},
		"uzun kod":    {long, testSubject, nil, 0},
		"subject yok": {"kod-1", "", nil, 0},
		"Apple kodu reddetti": {"kod-1", testSubject, func(a *apple) {
			a.tokenStatus, a.tokenBody = http.StatusBadRequest, `{"error":"invalid_grant"}`
		}, 1},
		"başka kullanıcının kodu": {"kod-1", "000999.baskasi.0001", nil, 1},
	} {
		a, c := newApple(t)
		if tc.prepare != nil {
			tc.prepare(a)
		}
		err := c.RevokeByCode(context.Background(), tc.code, tc.subject)
		if !errors.Is(err, ErrCodeRejected) {
			t.Errorf("%s: err = %v, ErrCodeRejected bekleniyordu", name, err)
		}
		if len(a.tokenForms) != tc.wantToken || len(a.revokeForms) != 0 {
			t.Errorf("%s: istekler: token %d, iptal %d", name, len(a.tokenForms), len(a.revokeForms))
		}
	}
}

// TestUnavailable: nothing the person can fix. The account is not deleted
// and the request can be retried.
func TestUnavailable(t *testing.T) {
	for name, prepare := range map[string]func(*apple, *Client){
		"token ucu 500": func(a *apple, _ *Client) { a.tokenStatus, a.tokenBody = http.StatusInternalServerError, "" },
		"yanlış yapılandırma (invalid_client)": func(a *apple, _ *Client) {
			a.tokenStatus, a.tokenBody = http.StatusBadRequest, `{"error":"invalid_client"}`
		},
		"cevap JSON değil":   func(a *apple, _ *Client) { a.tokenBody = "<html>" },
		"kimlik token'ı yok": func(a *apple, _ *Client) { a.tokenBody = `{"refresh_token":"RT"}` },
		"token yok":          func(a *apple, _ *Client) { a.tokenBody = `{"id_token":"` + testSubject + `"}` },
		"kimlik token'ı doğrulanamadı": func(_ *apple, c *Client) {
			c.cfg.Tokens = fakeTokens{err: errors.New("jwks alınamadı")}
		},
		"iptal ucu 500": func(a *apple, _ *Client) { a.revokeStatus = http.StatusInternalServerError },
		"sunucu kapalı": func(a *apple, _ *Client) { a.srv.Close() },
	} {
		a, c := newApple(t)
		prepare(a, c)
		err := c.RevokeByCode(context.Background(), "kod-1", testSubject)
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrCodeRejected) {
			t.Errorf("%s: err = %v, ErrUnavailable bekleniyordu", name, err)
		}
	}
}

// TestErrorsCarryNoSecrets: what reaches a log is a class, a status and an
// OAuth error name at most.
func TestErrorsCarryNoSecrets(t *testing.T) {
	a, c := newApple(t)
	a.tokenStatus = http.StatusBadRequest
	a.tokenBody = `{"error":"Some Free Text with kod-gizli"}`
	err := c.RevokeByCode(context.Background(), "kod-gizli", testSubject)
	if err == nil || strings.Contains(err.Error(), "kod-gizli") || strings.Contains(err.Error(), "Free Text") {
		t.Fatalf("hata metni: %v", err)
	}
	a.srv.Close()
	err = c.RevokeByCode(context.Background(), "kod-gizli", testSubject)
	if err == nil || strings.Contains(err.Error(), "kod-gizli") || strings.Contains(err.Error(), "http") {
		t.Fatalf("hata metni: %v", err)
	}
}

func TestParseKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseKey(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
	if err != nil || !got.Equal(key) {
		t.Fatalf("anahtar okunamadı: %v", err)
	}

	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der384, err := x509.MarshalPKCS8PrivateKey(p384)
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]string{
		"boş":        "",
		"PEM değil":  "anahtar",
		"yanlış tür": string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})),
		"bozuk":      string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der[:20]})),
		"P-384":      string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der384})),
	} {
		if _, err := ParseKey(in); err == nil {
			t.Errorf("%s: anahtar kabul edildi", name)
		}
	}
}

func TestNewNeedsEverything(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	full := Config{ClientID: testClient, TeamID: testTeam, KeyID: testKeyID, Key: key, HTTP: http.DefaultClient, Tokens: fakeTokens{}}
	if _, err := New(full); err != nil {
		t.Fatal(err)
	}
	for name, strip := range map[string]func(*Config){
		"client": func(c *Config) { c.ClientID = "" },
		"team":   func(c *Config) { c.TeamID = "" },
		"key id": func(c *Config) { c.KeyID = "" },
		"key":    func(c *Config) { c.Key = nil },
		"http":   func(c *Config) { c.HTTP = nil },
		"tokens": func(c *Config) { c.Tokens = nil },
	} {
		cfg := full
		strip(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s olmadan istemci kuruldu", name)
		}
	}
}
