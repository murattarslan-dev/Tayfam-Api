package integrity

import (
	"context"
	"errors"
	"log/slog"
)

// Config wires a Checker.
type Config struct {
	Mode Mode
	// Play is required unless Mode is ModeOff.
	Play *Play
	// AppAttest is optional: without it iOS proofs are not checked and iOS
	// sessions stay unverified (missing), as before App Attest.
	AppAttest *AppAttest
	Log       *slog.Logger
}

// Checker runs the check for a platform. A nil *Checker is off.
type Checker struct {
	mode      Mode
	play      *Play
	appAttest *AppAttest
	log       *slog.Logger
}

// NewChecker returns a Checker.
func NewChecker(cfg Config) (*Checker, error) {
	if cfg.Log == nil {
		return nil, errors.New("integrity: log yok")
	}
	if _, err := ParseMode(string(cfg.Mode)); err != nil {
		return nil, err
	}
	if cfg.Mode != ModeOff && cfg.Play == nil {
		return nil, errors.New("integrity: Play doğrulayıcısı yok")
	}
	return &Checker{mode: cfg.Mode, play: cfg.Play, appAttest: cfg.AppAttest, log: cfg.Log}, nil
}

// Enabled reports whether sessions are checked at all.
func (c *Checker) Enabled() bool { return c != nil && c.mode != ModeOff }

// Mode is the configured mode.
func (c *Checker) Mode() Mode {
	if c == nil {
		return ModeOff
	}
	return c.mode
}

// AppAttestEnabled reports whether iOS proofs are checked.
func (c *Checker) AppAttestEnabled() bool { return c.Enabled() && c.appAttest != nil }

// Check checks the proof of a sign-in on platform, bound to binding.
// Without a proof it returns Missing without asking anyone. On iOS the proof
// is an attestation of a new key (ADR-0022 §4); a passed result carries the
// key to bind to the new device.
//
// The outcome is logged as a count (platform, status, reason, licensing);
// no user, session, token, key or device detail goes into the line.
func (c *Checker) Check(ctx context.Context, platform string, proof Proof, binding string) *Result {
	return c.check(ctx, platform, proof, binding, false, nil)
}

// CheckRefresh checks the proof of a refresh. On iOS the proof is an
// assertion by the key bound to the session's device (key; nil when the
// device has none) and a passed result carries the key with its new counter.
func (c *Checker) CheckRefresh(ctx context.Context, platform string, proof Proof, binding string, key *AttestedKey) *Result {
	return c.check(ctx, platform, proof, binding, true, key)
}

func (c *Checker) check(ctx context.Context, platform string, proof Proof, binding string, refresh bool, key *AttestedKey) *Result {
	var (
		r         *Result
		licensing string
	)
	switch {
	case proof.Empty():
		r = Missing()
	case platform == "android" && proof.AppAttest == nil:
		res, lic := c.play.Verify(ctx, proof.Android, binding)
		r, licensing = &res, lic
	case platform == "ios" && proof.Android == "":
		r = c.checkAppAttest(*proof.AppAttest, binding, refresh, key)
	default:
		// Another platform's proof (an Android token from an iOS session) is
		// not a proof of this session; web has none.
		r = &Result{Status: StatusFailed, Reason: ReasonMalformed}
	}
	c.log.InfoContext(ctx, "bütünlük denetimi",
		slog.String("mode", string(c.mode)),
		slog.String("platform", platform),
		slog.String("status", string(r.Status)),
		slog.String("reason", string(r.Reason)),
		slog.String("licensing", licensing))
	return r
}

// checkAppAttest: an attestation at sign-in, an assertion at refresh
// (ADR-0022 §4). A refresh of a device without an attested key has nothing
// to assert against: its proof is another request's (binding).
func (c *Checker) checkAppAttest(p AppAttestProof, binding string, refresh bool, key *AttestedKey) *Result {
	if c.appAttest == nil {
		// Not configured (no Apple Developer values in this environment).
		return Missing()
	}
	var (
		res Result
		k   *AttestedKey
	)
	switch {
	case !refresh && p.Attestation != nil && p.Assertion == nil:
		res, k = c.appAttest.VerifyAttestation(p.KeyID, p.Attestation, binding)
	case refresh && p.Assertion != nil && p.Attestation == nil && key == nil:
		res = Result{Status: StatusFailed, Reason: ReasonBinding}
	case refresh && p.Assertion != nil && p.Attestation == nil:
		res, k = c.appAttest.VerifyAssertion(*key, p.KeyID, p.Assertion, binding)
	default:
		res = Result{Status: StatusFailed, Reason: ReasonMalformed}
	}
	res.Key = k
	return &res
}
