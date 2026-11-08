package auth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"
)

const (
	// MaxTokenTTL is the longest lifetime a verifier accepts, whatever the
	// token says. It limits the damage of a token minted with a leaked key
	// (ADR-0011).
	MaxTokenTTL = 15 * time.Minute
	// clockSkew tolerates small clock differences between services.
	clockSkew = 30 * time.Second
)

// ErrUnknownKey is returned by a KeySource for a key ID it does not have.
var ErrUnknownKey = errors.New("bilinmeyen anahtar")

// KeySource looks up public keys by key ID.
type KeySource interface {
	PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error)
}

// StaticKeys is a fixed KeySource, used by identity itself and in tests.
type StaticKeys map[string]ed25519.PublicKey

// PublicKey implements KeySource.
func (s StaticKeys) PublicKey(_ context.Context, kid string) (ed25519.PublicKey, error) {
	k, ok := s[kid]
	if !ok {
		return nil, ErrUnknownKey
	}
	return k, nil
}

// VerifierConfig configures a Verifier.
type VerifierConfig struct {
	Keys     KeySource
	Issuer   string
	Audience string
	// AlsoAccept lists further audiences this verifier accepts. Only identity
	// uses it, to let users who have not finished registration reach the
	// registration methods (ADR-0012). Claims.Audience tells which one
	// matched; handlers that need a registered user check it.
	AlsoAccept []string
	Now        func() time.Time // optional, for tests
}

// Verifier checks access tokens. Every service has its own and never trusts
// another service's verdict (ADR-0011).
type Verifier struct {
	cfg       VerifierConfig
	audiences map[string]bool
}

// NewVerifier validates cfg and returns a Verifier.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if cfg.Keys == nil || cfg.Issuer == "" || cfg.Audience == "" {
		return nil, errors.New("anahtar kaynağı, issuer ve audience zorunlu")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	aud := map[string]bool{cfg.Audience: true}
	for _, a := range cfg.AlsoAccept {
		if a == "" {
			return nil, errors.New("boş audience")
		}
		aud[a] = true
	}
	return &Verifier{cfg: cfg, audiences: aud}, nil
}

// Verify checks token's structure, signature and claims, in that order, and
// returns the claims. Every failure wraps ErrInvalidToken, except a failing
// key source, which is an infrastructure error and wraps neither.
func (v *Verifier) Verify(ctx context.Context, token string) (Claims, error) {
	p, err := parse(token)
	if err != nil {
		return Claims{}, err
	}
	key, err := v.cfg.Keys.PublicKey(ctx, p.header.Kid)
	if errors.Is(err, ErrUnknownKey) {
		return Claims{}, fmt.Errorf("%w: anahtar", ErrInvalidToken)
	}
	if err != nil {
		return Claims{}, fmt.Errorf("açık anahtar alınamadı: %w", err)
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, p.signingInput, p.signature) {
		return Claims{}, fmt.Errorf("%w: imza", ErrInvalidToken)
	}

	// Claims are only read after the signature is known to be good.
	c := p.payload
	now := v.cfg.Now()
	iat, exp := time.Unix(c.Iat, 0).UTC(), time.Unix(c.Exp, 0).UTC()
	switch {
	case c.Iss != v.cfg.Issuer:
		return Claims{}, fmt.Errorf("%w: issuer", ErrInvalidToken)
	case !v.audiences[c.Aud]:
		return Claims{}, fmt.Errorf("%w: audience", ErrInvalidToken)
	case !validID(c.Sub) || !validID(c.Sid):
		return Claims{}, fmt.Errorf("%w: kimlik", ErrInvalidToken)
	case !exp.After(iat) || exp.Sub(iat) > MaxTokenTTL:
		return Claims{}, fmt.Errorf("%w: ömür", ErrInvalidToken)
	case iat.After(now.Add(clockSkew)):
		return Claims{}, fmt.Errorf("%w: gelecekte verilmiş", ErrInvalidToken)
	case !now.Before(exp.Add(clockSkew)):
		return Claims{}, fmt.Errorf("%w: süresi dolmuş", ErrInvalidToken)
	}
	return Claims{
		Subject: c.Sub, SessionID: c.Sid, Issuer: c.Iss, Audience: c.Aud,
		IssuedAt: iat, ExpiresAt: exp, KeyID: p.header.Kid,
	}, nil
}
