// Package integritytest is a fake Play Integrity decode endpoint for tests.
// Tokens are made up per test; nothing here is a real credential or
// verdict.
package integritytest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/integrity"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/googleauth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/googleauth/googleauthtest"
)

// Package is the fake app's package name.
const Package = "app.tayfam.test"

// Cert is the fake app signing certificate digest.
const Cert = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// Verdict describes the answer for one token. The zero value with a binding
// is a pass on a STRONG device.
type Verdict struct {
	Binding  string
	Package  string    // default Package
	At       time.Time // default the fake's clock
	App      string    // default PLAY_RECOGNIZED
	Cert     string    // default Cert
	Labels   []string  // default STRONG, DEVICE, BASIC
	NoLabels bool
}

// Fake is the fake Google.
type Fake struct {
	G   *googleauthtest.Server
	Now func() time.Time

	mu      sync.Mutex
	next    int
	tokens  map[string]Verdict
	decodes int
	down    bool
}

// New starts a fake and returns a Play verifier that talks to it.
func New(t testing.TB, now func() time.Time) (*Fake, *integrity.Play) {
	t.Helper()
	f := &Fake{G: googleauthtest.New(t), Now: now, tokens: map[string]Verdict{}}
	f.G.API = f.serve
	acct, err := googleauth.ParseServiceAccount(f.G.Account(t))
	if err != nil {
		t.Fatal(err)
	}
	ts, err := googleauth.New(googleauth.Config{Account: acct, Scope: integrity.PlayScope, Transport: f.G.Transport(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	p, err := integrity.NewPlay(integrity.PlayConfig{
		Tokens: ts, Package: Package, CertDigests: []string{Cert}, DeviceLabel: integrity.LabelStrong,
		Transport: f.G.Transport(), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, p
}

// Token registers v and returns a token for it.
func (f *Fake) Token(v Verdict) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	tok := "fake-integrity-" + strconv.Itoa(f.next)
	f.tokens[tok] = v
	return tok
}

// Down makes the decode endpoint answer 503 (true) or work again (false).
func (f *Fake) Down(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// Decodes is how many decode requests came.
func (f *Fake) Decodes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decodes
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/"+Package+":decodeIntegrityToken" ||
		!strings.HasPrefix(r.Header.Get("Authorization"), "Bearer fake-access-") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.mu.Lock()
	f.decodes++
	down := f.down
	f.mu.Unlock()
	if down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var in struct {
		Token string `json:"integrity_token"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	v, ok := f.tokens[in.Token]
	if ok {
		// Standard requests cannot be replayed: a second decode is empty.
		delete(f.tokens, in.Token)
	}
	f.mu.Unlock()
	if !ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"tokenPayloadExternal": map[string]any{
			"requestDetails":  map[string]any{"requestPackageName": Package},
			"appIntegrity":    map[string]any{"appRecognitionVerdict": "UNEVALUATED"},
			"deviceIntegrity": map[string]any{},
		}})
		return
	}
	pkg, at, app, cert := v.Package, v.At, v.App, v.Cert
	if pkg == "" {
		pkg = Package
	}
	if at.IsZero() {
		at = f.Now()
	}
	if app == "" {
		app = "PLAY_RECOGNIZED"
	}
	if cert == "" {
		cert = Cert
	}
	labels := v.Labels
	if labels == nil && !v.NoLabels {
		labels = []string{integrity.LabelBasic, integrity.LabelDevice, integrity.LabelStrong}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"tokenPayloadExternal": map[string]any{
		"requestDetails": map[string]any{
			"requestPackageName": pkg, "requestHash": v.Binding,
			"timestampMillis": strconv.FormatInt(at.UnixMilli(), 10),
		},
		"appIntegrity": map[string]any{
			"appRecognitionVerdict": app, "packageName": pkg,
			"certificateSha256Digest": []string{cert}, "versionCode": "1",
		},
		"deviceIntegrity": map[string]any{"deviceRecognitionVerdict": labels},
		"accountDetails":  map[string]any{"appLicensingVerdict": "LICENSED"},
	}})
}
