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
	Log  *slog.Logger
}

// Checker runs the check for a platform. A nil *Checker is off.
type Checker struct {
	mode Mode
	play *Play
	log  *slog.Logger
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
	return &Checker{mode: cfg.Mode, play: cfg.Play, log: cfg.Log}, nil
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

// Check checks proof for a session on platform, bound to binding. Without a
// proof it returns Missing without asking anyone. iOS has no check yet
// (App Attest needs the Apple Developer membership, ADR-0022 §4): its
// sessions stay unverified.
//
// The outcome is logged as a count (platform, status, reason, licensing);
// no user, session, token or device detail goes into the line.
func (c *Checker) Check(ctx context.Context, platform string, proof Proof, binding string) *Result {
	var (
		r         *Result
		licensing string
	)
	switch {
	case proof.Empty():
		r = Missing()
	case platform != "android":
		// An Android token from an iOS or web session is not a proof of
		// that session.
		r = &Result{Status: StatusFailed, Reason: ReasonMalformed}
	default:
		res, lic := c.play.Verify(ctx, proof.Android, binding)
		r, licensing = &res, lic
	}
	c.log.InfoContext(ctx, "bütünlük denetimi",
		slog.String("mode", string(c.mode)),
		slog.String("platform", platform),
		slog.String("status", string(r.Status)),
		slog.String("reason", string(r.Reason)),
		slog.String("licensing", licensing))
	return r
}
