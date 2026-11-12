// Package integrity checks that a session belongs to the official app
// (ADR-0022, issue #163): Play Integrity on Android, App Attest on iOS
// (later). A check runs at sign-in and, once the last pass is older than
// Recheck, at token refresh; its outcome is kept on the session.
//
// A proof is bound to a value the server already treats as single use and
// verifies (ADR-0022 §2): the OIDC nonce at sign-in, the refresh token at
// refresh. The binding is a hash; the refresh token itself never leaves
// identity.
//
// Nothing raw is kept: not the integrity token, not Google's answer, not
// device details. Only the session's status, a reason from a closed list and
// two times.
package integrity

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

// Mode is INTEGRITY_MODE (ADR-0022 §6).
type Mode string

// Modes.
const (
	// ModeOff asks for no proof and decodes nothing.
	ModeOff Mode = "off"
	// ModeReport decodes proofs and records the outcome; nothing is refused.
	ModeReport Mode = "report"
	// ModeEnforce is ModeReport here; gateway refuses tokens without the
	// claim (issue #164).
	ModeEnforce Mode = "enforce"
)

// ParseMode reads INTEGRITY_MODE.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeOff, ModeReport, ModeEnforce:
		return m, nil
	}
	return "", fmt.Errorf("geçersiz INTEGRITY_MODE: %q", s)
}

// Status of a session (sessions.integrity_status).
type Status string

// Statuses.
const (
	StatusUnverified Status = "unverified"
	StatusPassed     Status = "passed"
	StatusFailed     Status = "failed"
)

// Reason is why a session is not passed (sessions.integrity_reason); empty
// when passed. A closed list: the database refuses anything else.
type Reason string

// Reasons (ADR-0022 §5).
const (
	ReasonNone Reason = ""
	// ReasonMissing: no proof came (an old app, a platform without a check).
	ReasonMissing Reason = "missing"
	// ReasonBinding: the proof is for another request, another package or
	// too old.
	ReasonBinding Reason = "binding"
	// ReasonStale: the session passed once, but no new proof came after
	// Recheck.
	ReasonStale Reason = "stale"
	// ReasonAppUnrecognized: not the app Google Play knows.
	ReasonAppUnrecognized Reason = "app_unrecognized"
	// ReasonDevice: the device does not meet the required level.
	ReasonDevice Reason = "device"
	// ReasonUnavailable: Google could not be asked; temporary.
	ReasonUnavailable Reason = "unavailable"
	// ReasonMalformed: the proof could not be read.
	ReasonMalformed Reason = "malformed"
)

const (
	// Recheck is how long a pass lasts before the next refresh must carry a
	// new proof (platform owner, 2026-11-12).
	Recheck = 24 * time.Hour
	// RetryFailed is how long a session that did not pass waits before its
	// next check; it keeps a failing device from spending the daily quota on
	// every 15-minute refresh.
	RetryFailed = time.Hour
	// Grace keeps a session passed while Google cannot be asked, if it
	// passed this recently (ADR-0022 §5).
	Grace = 7 * 24 * time.Hour
)

// Proof is what the client sends. Empty fields mean no proof.
type Proof struct {
	// Android is a Play Integrity standard request token.
	Android string
}

// Empty reports whether no proof came.
func (p Proof) Empty() bool { return p.Android == "" }

// Result is one check's outcome. Status is empty for ReasonUnavailable:
// whether the session stays passed depends on its history (Next).
type Result struct {
	Status Status
	Reason Reason
}

// State is a session's integrity as stored.
type State struct {
	Status    Status
	Reason    Reason
	CheckedAt time.Time // zero: never checked
	PassedAt  time.Time // zero: never passed
}

// Passed reports whether the session counts as passed now. It is what the
// access token's claim carries (issue #164).
func (s State) Passed() bool { return s.Status == StatusPassed }

// CheckAfter is when the client should next send a proof: Recheck after the
// last pass, RetryFailed after any other check, now when never checked.
func (s State) CheckAfter(now time.Time) time.Time {
	switch {
	case s.Status == StatusPassed && !s.PassedAt.IsZero():
		return s.PassedAt.Add(Recheck)
	case !s.CheckedAt.IsZero():
		return s.CheckedAt.Add(RetryFailed)
	}
	return now
}

// Due reports whether a refresh at now should be checked.
func (s State) Due(now time.Time) bool { return !now.Before(s.CheckAfter(now)) }

// Next is the state after a check at now. r is nil when no check ran
// because the session was not due; a due session without a proof is
// checked with ReasonMissing (Missing).
func Next(prev State, r *Result, now time.Time) State {
	if r == nil {
		return prev
	}
	next := State{Status: r.Status, Reason: r.Reason, CheckedAt: now, PassedAt: prev.PassedAt}
	switch {
	case r.Status == StatusPassed:
		next.Reason, next.PassedAt = ReasonNone, now
	case r.Reason == ReasonUnavailable:
		// Google's outage does not lock users out: a recent pass stands.
		if prev.Status == StatusPassed && !prev.PassedAt.IsZero() && now.Sub(prev.PassedAt) < Grace {
			next.Status, next.Reason = StatusPassed, ReasonNone
		} else {
			next.Status = StatusUnverified
		}
	case r.Reason == ReasonMissing && prev.Status == StatusPassed:
		// It passed once and stopped proving it.
		next.Status, next.Reason = StatusFailed, ReasonStale
	case r.Reason == ReasonMissing:
		next.Status = StatusUnverified
	default:
		next.Status = StatusFailed
	}
	return next
}

// Missing returns the result of a due check without a proof.
func Missing() *Result { return &Result{Status: StatusUnverified, Reason: ReasonMissing} }

// Binding values (ADR-0022 §2), base64url without padding. They go to
// Google as requestHash (at most 500 bytes; these are 43) and to Apple as
// client data.
const (
	signInPrefix  = "tayfam-integrity/v1/sign-in\n"
	refreshPrefix = "tayfam-integrity/v1/refresh\n"
)

// SignInBinding binds a proof to the sign-in that carries nonce.
func SignInBinding(nonce string) string {
	sum := sha256.Sum256([]byte(signInPrefix + nonce))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RefreshBinding binds a proof to the refresh that presents refreshToken
// (its bytes as sent). Only a hash of the token's hash is ever sent
// anywhere.
func RefreshBinding(refreshToken string) string {
	inner := sha256.Sum256([]byte(refreshToken))
	sum := sha256.Sum256(append([]byte(refreshPrefix), inner[:]...))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
