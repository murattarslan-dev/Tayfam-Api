package oidc

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// GoogleJWKSURL is where Google publishes its ID token signing keys.
const GoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

var googleIssuers = map[string]bool{
	"https://accounts.google.com": true,
	"accounts.google.com":         true,
}

// Google verifies Google ID tokens for our OAuth client IDs (one per
// platform: Android, iOS, web).
type Google struct {
	audiences map[string]bool
	keys      *jwks
	now       func() time.Time
}

// NewGoogle returns a verifier accepting tokens issued to clientIDs.
func NewGoogle(clientIDs []string, client *http.Client) (*Google, error) {
	return newGoogle(clientIDs, GoogleJWKSURL, client)
}

func newGoogle(clientIDs []string, jwksURL string, client *http.Client) (*Google, error) {
	aud := make(map[string]bool, len(clientIDs))
	for _, id := range clientIDs {
		if id != "" {
			aud[id] = true
		}
	}
	if len(aud) == 0 {
		return nil, errors.New("en az bir Google OAuth client ID gerekli")
	}
	return &Google{audiences: aud, keys: newJWKS(jwksURL, client), now: time.Now}, nil
}

// Verify implements Verifier.
func (g *Google) Verify(ctx context.Context, idToken, nonce string) (Identity, error) {
	c, err := verifyRS256(ctx, idToken, g.keys.key)
	if err != nil {
		return Identity{}, err
	}
	if err := checkStandard(c, googleIssuers, g.audiences, nonce, g.now()); err != nil {
		return Identity{}, err
	}
	return Identity{Provider: "google", Subject: c.Sub, Email: verifiedEmail(c), Name: cleanName(c.Name)}, nil
}
