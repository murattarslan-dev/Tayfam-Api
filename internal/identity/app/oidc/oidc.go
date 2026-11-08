// Package oidc verifies ID tokens from sign-in providers (ADR-0004).
//
// Only what Tayfam needs is implemented: RS256-signed JWTs whose keys come
// from the provider's JWKS endpoint. Signatures are checked with crypto/rsa
// from the standard library; there is no algorithm negotiation, and header
// fields that point at other keys (jku, x5u, jwk) are never followed.
package oidc

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidToken is returned for every rejected ID token. The wrapped
// cause is for logs and tests; clients only learn that sign-in failed.
var ErrInvalidToken = errors.New("geçersiz kimlik token'ı")

const (
	maxTokenBytes = 8 << 10
	clockSkew     = 60 * time.Second
	// Providers issue ID tokens valid for about an hour.
	maxLifetime = 2 * time.Hour
	maxSubject  = 255
	maxEmail    = 320
)

// Identity is the verified result: who the provider says signed in.
type Identity struct {
	Provider string
	// Subject is the provider's stable user ID; the account key (ADR-0004).
	Subject string
	// Email is set only when the provider marks it verified. It is stored
	// for contact purposes and never used to find or link accounts.
	Email string
	// Name is the display name the provider suggests, possibly empty.
	Name string
}

// Verifier checks one provider's ID tokens.
type Verifier interface {
	Verify(ctx context.Context, idToken, nonce string) (Identity, error)
}

// rawClaims are the standard claims plus the profile fields used here.
// Unknown claims are ignored: providers add fields over time.
type rawClaims struct {
	Iss           string          `json:"iss"`
	Aud           json.RawMessage `json:"aud"`
	Sub           string          `json:"sub"`
	Exp           int64           `json:"exp"`
	Iat           int64           `json:"iat"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
	Name          string          `json:"name"`
}

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

var b64 = base64.RawURLEncoding

// verifyRS256 checks structure and signature and returns the claims. keyFor
// looks up a public key by kid.
func verifyRS256(ctx context.Context, token string, keyFor func(context.Context, string) (*rsa.PublicKey, error)) (*rawClaims, error) {
	if len(token) == 0 || len(token) > maxTokenBytes {
		return nil, fmt.Errorf("%w: uzunluk", ErrInvalidToken)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: biçim", ErrInvalidToken)
	}
	var h header
	if err := decodeSegment(parts[0], &h); err != nil {
		return nil, fmt.Errorf("%w: başlık", ErrInvalidToken)
	}
	if h.Alg != "RS256" || h.Kid == "" || len(h.Kid) > 128 {
		return nil, fmt.Errorf("%w: algoritma veya kid", ErrInvalidToken)
	}
	sig, err := b64.Strict().DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: imza biçimi", ErrInvalidToken)
	}
	key, err := keyFor(ctx, h.Kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("%w: imza", ErrInvalidToken)
	}
	var c rawClaims
	if err := decodeSegment(parts[1], &c); err != nil {
		return nil, fmt.Errorf("%w: içerik", ErrInvalidToken)
	}
	return &c, nil
}

func decodeSegment(seg string, v any) error {
	raw, err := b64.Strict().DecodeString(seg)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("fazladan veri")
	}
	return nil
}

// checkStandard validates issuer, audience, times, subject and nonce.
func checkStandard(c *rawClaims, issuers, audiences map[string]bool, nonce string, now time.Time) error {
	if !issuers[c.Iss] {
		return fmt.Errorf("%w: issuer", ErrInvalidToken)
	}
	if !audienceMatches(c.Aud, audiences) {
		return fmt.Errorf("%w: audience", ErrInvalidToken)
	}
	iat, exp := time.Unix(c.Iat, 0), time.Unix(c.Exp, 0)
	switch {
	case c.Iat == 0 || c.Exp == 0 || !exp.After(iat) || exp.Sub(iat) > maxLifetime:
		return fmt.Errorf("%w: ömür", ErrInvalidToken)
	case iat.After(now.Add(clockSkew)):
		return fmt.Errorf("%w: gelecekte verilmiş", ErrInvalidToken)
	case !now.Before(exp.Add(clockSkew)):
		return fmt.Errorf("%w: süresi dolmuş", ErrInvalidToken)
	case c.Sub == "" || len(c.Sub) > maxSubject:
		return fmt.Errorf("%w: subject", ErrInvalidToken)
	}
	// The nonce binds the token to this sign-in attempt (ADR-0004). An empty
	// expected nonce is never accepted.
	if nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1 {
		return fmt.Errorf("%w: nonce", ErrInvalidToken)
	}
	return nil
}

// audienceMatches accepts aud as a string or an array of strings; at least
// one value must be ours.
func audienceMatches(raw json.RawMessage, audiences map[string]bool) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return audiences[one]
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return false
	}
	for _, a := range many {
		if audiences[a] {
			return true
		}
	}
	return false
}

// emailVerified accepts true or "true"; providers differ.
func emailVerified(raw json.RawMessage) bool {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == "true"
}

func verifiedEmail(c *rawClaims) string {
	if !emailVerified(c.EmailVerified) || len(c.Email) < 3 || len(c.Email) > maxEmail || !strings.Contains(c.Email, "@") {
		return ""
	}
	return c.Email
}

// cleanName trims the provider's display name to the users.display_name
// limit (1–40 characters).
func cleanName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 40 {
		s = strings.TrimSpace(string(r[:40]))
	}
	return s
}
