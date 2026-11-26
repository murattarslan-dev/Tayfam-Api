package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// AppleJWKSURL is where Apple publishes its identity token signing keys.
const AppleJWKSURL = "https://appleid.apple.com/auth/keys"

var appleIssuers = map[string]bool{"https://appleid.apple.com": true}

// Apple verifies Sign in with Apple identity tokens issued to our iOS app
// (ADR-0045). The audience of a token from the native flow is the app's
// bundle ID.
type Apple struct {
	audiences map[string]bool
	keys      *jwks
	now       func() time.Time
}

// NewApple returns a verifier accepting tokens issued to clientIDs (bundle
// IDs; not secret).
func NewApple(clientIDs []string, client *http.Client) (*Apple, error) {
	return newApple(clientIDs, AppleJWKSURL, client)
}

func newApple(clientIDs []string, jwksURL string, client *http.Client) (*Apple, error) {
	aud := make(map[string]bool, len(clientIDs))
	for _, id := range clientIDs {
		if id != "" {
			aud[id] = true
		}
	}
	if len(aud) == 0 {
		return nil, errors.New("en az bir Apple client ID (bundle kimliği) gerekli")
	}
	return &Apple{audiences: aud, keys: newJWKS(jwksURL, client), now: time.Now}, nil
}

// Verify implements Verifier.
//
// Apple puts no name in the identity token: it gives the name to the app
// only, on the first authorization, and the app sends it with registration
// (ADR-0045 §2). Name is therefore always empty here. A private relay
// address is an ordinary verified email (ADR-0004).
func (a *Apple) Verify(ctx context.Context, idToken, nonce string) (Identity, error) {
	c, err := verifyRS256(ctx, idToken, a.keys.key)
	if err != nil {
		return Identity{}, err
	}
	if err := checkStandard(c, appleIssuers, a.audiences, nonce, a.now()); err != nil {
		return Identity{}, err
	}
	// A device that cannot bind a nonce says so; such a token carries no
	// proof that it belongs to this sign-in attempt.
	if explicitlyFalse(c.NonceSupported) {
		return Identity{}, fmt.Errorf("%w: nonce desteklenmiyor", ErrInvalidToken)
	}
	return Identity{Provider: "apple", Subject: c.Sub, Email: verifiedEmail(c), IssuedAt: time.Unix(c.Iat, 0).UTC()}, nil
}

// ExchangedSubject verifies the identity token Apple's token endpoint returns
// for an authorization code and gives its subject (ADR-0045 §4; implements
// appleauth.SubjectVerifier). Signature, issuer, audience, times and subject
// are checked as in Verify. There is no nonce to compare: the token answers
// a code that Apple accepted with our client secret, not a sign-in request
// of ours. It must never be used to sign anyone in.
func (a *Apple) ExchangedSubject(ctx context.Context, idToken string) (string, error) {
	c, err := verifyRS256(ctx, idToken, a.keys.key)
	if err != nil {
		return "", err
	}
	if err := checkIssued(c, appleIssuers, a.audiences, a.now()); err != nil {
		return "", err
	}
	return c.Sub, nil
}

// explicitlyFalse reports whether a claim is present and false. Apple sends
// booleans as true/false or as "true"/"false".
func explicitlyFalse(raw json.RawMessage) bool {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return !b
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == "false"
}
