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

// TestCheckerAppAttest: an iOS sign-in proves itself with an attestation, a
// refresh with an assertion by the device's key; nothing else counts, and
// no key or proof reaches the log.
func TestCheckerAppAttest(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	_, play := integritytest.New(t, clock)
	apple, aa := integritytest.NewApple(t, clock)
	var buf bytes.Buffer
	c, err := integrity.NewChecker(integrity.Config{Mode: integrity.ModeReport, Play: play, AppAttest: aa, Log: slog.New(slog.NewJSONHandler(&buf, nil))})
	if err != nil || !c.AppAttestEnabled() {
		t.Fatalf("denetleyici: %v", err)
	}
	ctx := context.Background()
	dev := apple.NewDevice()
	in := integrity.SignInBinding("nonce-1")

	r := c.Check(ctx, "ios", integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: dev.KeyID, Attestation: dev.Attest(in)}}, in)
	if r.Status != integrity.StatusPassed || r.Key == nil || r.Key.Counter != 0 || !bytes.Equal(r.Key.KeyID, dev.KeyID) {
		t.Fatalf("attestation: %+v", r)
	}
	key := r.Key

	ref := integrity.RefreshBinding("refresh-1")
	assert := func(b string) *integrity.Proof {
		return &integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: dev.KeyID, Assertion: dev.Assert(b)}}
	}
	r = c.CheckRefresh(ctx, "ios", *assert(ref), ref, key)
	if r.Status != integrity.StatusPassed || r.Key == nil || r.Key.Counter != 1 {
		t.Fatalf("assertion: %+v", r)
	}
	stored := *r.Key

	other := apple.NewDevice()
	for name, tc := range map[string]struct {
		refresh bool
		proof   integrity.Proof
		binding string
		key     *integrity.AttestedKey
		want    integrity.Reason
	}{
		"girişte assertion":      {false, *assert(in), in, nil, integrity.ReasonMalformed},
		"yenilemede attestation": {true, integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: dev.KeyID, Attestation: dev.Attest(ref)}}, ref, &stored, integrity.ReasonMalformed},
		"ikisi birden":           {true, integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: dev.KeyID, Attestation: dev.Attest(ref), Assertion: dev.Assert(ref)}}, ref, &stored, integrity.ReasonMalformed},
		"anahtarsız cihaz":       {true, *assert(ref), ref, nil, integrity.ReasonBinding},
		"başka yenileme":         {true, *assert(integrity.RefreshBinding("refresh-2")), ref, &stored, integrity.ReasonBinding},
		"tekrar":                 {true, integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: dev.KeyID, Assertion: dev.AssertWith(ref, 1)}}, ref, &stored, integrity.ReasonBinding},
		"başka cihazın anahtarı": {true, integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: other.KeyID, Assertion: other.Assert(ref)}}, ref, &stored, integrity.ReasonBinding},
		"girişte başka istek":    {false, integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: dev.KeyID, Attestation: dev.Attest(ref)}}, in, nil, integrity.ReasonBinding},
		"android kanıtı iOS'ta":  {false, integrity.Proof{Android: "x"}, in, nil, integrity.ReasonMalformed},
		"iOS kanıtı Android'de":  {false, integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: dev.KeyID, Attestation: dev.Attest(in)}}, in, nil, integrity.ReasonMalformed},
	} {
		platform := "ios"
		if strings.HasSuffix(name, "Android'de") {
			platform = "android"
		}
		var r *integrity.Result
		if tc.refresh {
			r = c.CheckRefresh(ctx, platform, tc.proof, tc.binding, tc.key)
		} else {
			r = c.Check(ctx, platform, tc.proof, tc.binding)
		}
		if r.Status != integrity.StatusFailed || r.Reason != tc.want || r.Key != nil {
			t.Errorf("%s: %+v; beklenen %s", name, r, tc.want)
		}
	}
	if out := buf.String(); strings.Contains(out, in) || strings.Contains(out, ref) || !strings.Contains(out, `"platform":"ios"`) {
		t.Fatalf("log = %s", out)
	}

	// Without an App Attest verifier iOS stays as before: missing.
	plain, err := integrity.NewChecker(integrity.Config{Mode: integrity.ModeReport, Play: play, Log: slog.New(slog.NewJSONHandler(&buf, nil))})
	if err != nil || plain.AppAttestEnabled() {
		t.Fatal(err)
	}
	if r := plain.Check(ctx, "ios", integrity.Proof{AppAttest: &integrity.AppAttestProof{KeyID: dev.KeyID, Attestation: dev.Attest(in)}}, in); *r != *integrity.Missing() {
		t.Fatalf("doğrulayıcısız: %+v", r)
	}
}
