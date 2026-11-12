package integrity_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/integrity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/integrity/integritytest"
)

func TestPlayVerdicts(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	f, p := integritytest.New(t, func() time.Time { return now })
	ctx := context.Background()
	b := integrity.SignInBinding("nonce-1")

	for _, tc := range []struct {
		name string
		v    integritytest.Verdict
		want integrity.Result
	}{
		{"geçer", integritytest.Verdict{Binding: b}, integrity.Result{Status: integrity.StatusPassed}},
		{"başka istek", integritytest.Verdict{Binding: integrity.SignInBinding("nonce-2")}, integrity.Result{Status: integrity.StatusFailed, Reason: integrity.ReasonBinding}},
		{"başka paket", integritytest.Verdict{Binding: b, Package: "com.example.kopya"}, integrity.Result{Status: integrity.StatusFailed, Reason: integrity.ReasonBinding}},
		{"eski token", integritytest.Verdict{Binding: b, At: now.Add(-3 * time.Minute)}, integrity.Result{Status: integrity.StatusFailed, Reason: integrity.ReasonBinding}},
		{"gelecekten token", integritytest.Verdict{Binding: b, At: now.Add(2 * time.Minute)}, integrity.Result{Status: integrity.StatusFailed, Reason: integrity.ReasonBinding}},
		{"tanınmayan uygulama", integritytest.Verdict{Binding: b, App: "UNRECOGNIZED_VERSION"}, integrity.Result{Status: integrity.StatusFailed, Reason: integrity.ReasonAppUnrecognized}},
		{"başka imza", integritytest.Verdict{Binding: b, Cert: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}, integrity.Result{Status: integrity.StatusFailed, Reason: integrity.ReasonAppUnrecognized}},
		{"yalnızca DEVICE", integritytest.Verdict{Binding: b, Labels: []string{integrity.LabelBasic, integrity.LabelDevice}}, integrity.Result{Status: integrity.StatusFailed, Reason: integrity.ReasonDevice}},
		{"etiket yok", integritytest.Verdict{Binding: b, NoLabels: true}, integrity.Result{Status: integrity.StatusFailed, Reason: integrity.ReasonDevice}},
	} {
		got, lic := p.Verify(ctx, f.Token(tc.v), b)
		if got != tc.want {
			t.Errorf("%s: %+v, beklenen %+v", tc.name, got, tc.want)
		}
		if lic != "LICENSED" {
			t.Errorf("%s: lisans = %q", tc.name, lic)
		}
	}

	// A token decoded once is empty the second time (replay).
	tok := f.Token(integritytest.Verdict{Binding: b})
	if r, _ := p.Verify(ctx, tok, b); r.Status != integrity.StatusPassed {
		t.Fatalf("ilk kullanım: %+v", r)
	}
	if r, _ := p.Verify(ctx, tok, b); r.Status == integrity.StatusPassed {
		t.Fatal("tekrar kullanılan token geçti")
	}

	// Google down: unavailable, no status.
	f.Down(true)
	if r, _ := p.Verify(ctx, f.Token(integritytest.Verdict{Binding: b}), b); r != (integrity.Result{Reason: integrity.ReasonUnavailable}) {
		t.Fatalf("Google kapalı: %+v", r)
	}
	f.Down(false)

	// Nothing is sent for an empty or oversized token.
	before := f.Decodes()
	for _, bad := range []string{"", strings.Repeat("x", integrity.MaxPlayToken+1)} {
		if r, _ := p.Verify(ctx, bad, b); r != (integrity.Result{Status: integrity.StatusFailed, Reason: integrity.ReasonMalformed}) {
			t.Errorf("bozuk token: %+v", r)
		}
	}
	if f.Decodes() != before {
		t.Fatal("bozuk token Google'a gitti")
	}
	if h := f.G.Hosts(); h["playintegrity.googleapis.com"] == 0 || h["oauth2.googleapis.com"] == 0 || len(h) != 2 {
		t.Fatalf("istek gidilen yerler = %v", h)
	}
}

type nopTokens struct{}

func (nopTokens) Token(context.Context) (string, error) { return "x", nil }
func (nopTokens) Forget(string)                         {}

func TestNewPlayRejectsConfig(t *testing.T) {
	good := integrity.PlayConfig{Tokens: nopTokens{}, Package: integritytest.Package, CertDigests: []string{integritytest.Cert}, DeviceLabel: integrity.LabelStrong}
	if _, err := integrity.NewPlay(good); err != nil {
		t.Fatal(err)
	}
	// Play Console's hex form is accepted too (32 zero bytes = Cert).
	hexForm := good
	hexForm.CertDigests = []string{strings.TrimSuffix(strings.Repeat("00:", 32), ":")}
	if _, err := integrity.NewPlay(hexForm); err != nil {
		t.Fatalf("hex özet: %v", err)
	}
	for name, change := range map[string]func(*integrity.PlayConfig){
		"token kaynağı yok": func(c *integrity.PlayConfig) { c.Tokens = nil },
		"paket adı yok":     func(c *integrity.PlayConfig) { c.Package = "" },
		"tek parçalı paket": func(c *integrity.PlayConfig) { c.Package = "tayfam" },
		"sertifika yok":     func(c *integrity.PlayConfig) { c.CertDigests = nil },
		"bozuk sertifika":   func(c *integrity.PlayConfig) { c.CertDigests = []string{"AA:BB"} },
		"hex olmayan özet":  func(c *integrity.PlayConfig) { c.CertDigests = []string{strings.Repeat("ZZ", 32)} },
		"bilinmeyen seviye": func(c *integrity.PlayConfig) { c.DeviceLabel = "MEETS_VIRTUAL_INTEGRITY" },
	} {
		cfg := good
		change(&cfg)
		if _, err := integrity.NewPlay(cfg); err == nil {
			t.Errorf("%s kabul edildi", name)
		}
	}
}

func TestCheckerLogsNoSecrets(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	f, p := integritytest.New(t, func() time.Time { return now })
	var buf bytes.Buffer
	c, err := integrity.NewChecker(integrity.Config{Mode: integrity.ModeReport, Play: p, Log: slog.New(slog.NewJSONHandler(&buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b := integrity.SignInBinding("nonce-1")
	tok := f.Token(integritytest.Verdict{Binding: b})
	if r := c.Check(ctx, "android", integrity.Proof{Android: tok}, b); r.Status != integrity.StatusPassed {
		t.Fatalf("android: %+v", r)
	}
	// No proof: missing, Google not asked. An Android token from an iOS
	// session is not that session's proof.
	before := f.Decodes()
	if r := c.Check(ctx, "android", integrity.Proof{}, b); *r != *integrity.Missing() {
		t.Fatalf("kanıtsız: %+v", r)
	}
	if r := c.Check(ctx, "ios", integrity.Proof{Android: f.Token(integritytest.Verdict{Binding: b})}, b); r.Reason != integrity.ReasonMalformed {
		t.Fatalf("ios: %+v", r)
	}
	if f.Decodes() != before {
		t.Fatal("gereksiz Google isteği")
	}
	if out := buf.String(); strings.Contains(out, tok) || strings.Contains(out, b) || strings.Contains(out, "fake-access") ||
		!strings.Contains(out, `"status":"passed"`) || !strings.Contains(out, `"licensing":"LICENSED"`) {
		t.Fatalf("log = %s", out)
	}

	off, err := integrity.NewChecker(integrity.Config{Mode: integrity.ModeOff, Log: slog.New(slog.NewJSONHandler(&buf, nil))})
	if err != nil || off.Enabled() {
		t.Fatalf("kapalı: %v", err)
	}
	var none *integrity.Checker
	if none.Enabled() || none.Mode() != integrity.ModeOff {
		t.Fatal("nil denetleyici açık")
	}
	if _, err := integrity.NewChecker(integrity.Config{Mode: integrity.ModeReport, Log: slog.New(slog.NewJSONHandler(&buf, nil))}); err == nil {
		t.Fatal("Play'siz report kabul edildi")
	}
}
