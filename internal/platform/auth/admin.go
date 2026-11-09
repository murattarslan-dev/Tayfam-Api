package auth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"
)

// Admin tokens are the platform administration panel's own session token
// (ADR-0008, layer 5). They are a separate token type, not an access token
// with another audience:
//
//   - the header says typ "admin+jwt"; access token parsing refuses it before
//     reading any claim, and admin parsing refuses "at+jwt";
//   - the audience is always AdminAudience, never configurable, so it cannot
//     be set to the app's audience by mistake;
//   - the claim set adds auth_time, which access tokens reject as an unknown
//     field;
//   - the lifetime limit is MaxAdminTokenTTL (8 hours), checked by the admin
//     verifier only; the access token verifier keeps its 15-minute limit.
//
// So an app token never passes in the panel, and an admin token never passes
// in the app or on a user method of an internal service (Guard).
const (
	typAdmin = "admin+jwt"

	// AdminAudience is the audience of every admin token.
	AdminAudience = "tayfam-admin"
	// MaxAdminTokenTTL is the panel session lifetime (ADR-0008: 8 hours). An
	// admin token is never refreshed; after it the administrator signs in
	// with the provider again.
	MaxAdminTokenTTL = 8 * time.Hour
	// AdminReauthWindow is how recent the provider sign-in must be for a
	// destructive panel action (ADR-0008, layer 6: 5 minutes). It also
	// bounds how old the provider sign-in may be when the token is issued.
	AdminReauthWindow = 5 * time.Minute
)

// ErrReauthenticationRequired is returned by RequireRecentAuth when the
// provider sign-in behind an admin token is older than AdminReauthWindow.
var ErrReauthenticationRequired = errors.New("yeniden giriş gerekli")

// AdminClaims are the verified contents of an admin token.
type AdminClaims struct {
	Subject string // user ID of the platform administrator
	// SessionID identifies this panel sign-in. It is random per sign-in and
	// is written to the platform audit log; there is no session row.
	SessionID string
	Issuer    string
	Audience  string
	IssuedAt  time.Time
	ExpiresAt time.Time
	// AuthTime is when the provider (Google) signed the administrator in.
	AuthTime time.Time
	KeyID    string
}

// RequireRecentAuth returns ErrReauthenticationRequired unless the provider
// sign-in was within AdminReauthWindow of now. Destructive panel actions
// (ban, tayfa deletion, plan change) call it before doing anything.
func (c AdminClaims) RequireRecentAuth(now time.Time) error {
	if c.AuthTime.IsZero() || now.Sub(c.AuthTime) > AdminReauthWindow || c.AuthTime.After(now.Add(clockSkew)) {
		return ErrReauthenticationRequired
	}
	return nil
}

type adminPayload struct {
	Iss      string `json:"iss"`
	Aud      string `json:"aud"`
	Sub      string `json:"sub"`
	Sid      string `json:"sid"`
	Iat      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
	AuthTime int64  `json:"auth_time"`
}

// AdminSignerConfig configures an AdminSigner.
type AdminSignerConfig struct {
	Key    SigningKey
	Issuer string
	TTL    time.Duration    // zero: MaxAdminTokenTTL
	Now    func() time.Time // optional, for tests
}

// AdminSigner issues admin tokens. Only identity has one, and only its
// panel sign-in uses it.
type AdminSigner struct {
	cfg AdminSignerConfig
}

// NewAdminSigner validates cfg and returns an AdminSigner.
func NewAdminSigner(cfg AdminSignerConfig) (*AdminSigner, error) {
	if cfg.TTL == 0 {
		cfg.TTL = MaxAdminTokenTTL
	}
	switch {
	case cfg.Key.private == nil:
		return nil, errors.New("imzalama anahtarı yok")
	case cfg.Issuer == "":
		return nil, errors.New("issuer zorunlu")
	case cfg.TTL < 0 || cfg.TTL > MaxAdminTokenTTL:
		return nil, fmt.Errorf("admin token ömrü 0 ile %s arasında olmalı", MaxAdminTokenTTL)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &AdminSigner{cfg: cfg}, nil
}

// Issue returns a signed admin token for userID and its expiry time.
// authTime is when the provider signed the user in; it must be within
// AdminReauthWindow, because an admin token is only issued right after a
// provider sign-in.
func (s *AdminSigner) Issue(userID, sessionID string, authTime time.Time) (string, time.Time, error) {
	if !validID(userID) || !validID(sessionID) {
		return "", time.Time{}, errors.New("kullanıcı ve oturum kimliği zorunlu")
	}
	now := s.cfg.Now().UTC().Truncate(time.Second)
	authTime = authTime.UTC().Truncate(time.Second)
	if authTime.After(now.Add(clockSkew)) || now.Sub(authTime) > AdminReauthWindow {
		return "", time.Time{}, errors.New("sağlayıcı girişi güncel değil")
	}
	if authTime.After(now) {
		authTime = now
	}
	exp := now.Add(s.cfg.TTL)
	tok, err := signJWS(s.cfg.Key.private, s.cfg.Key.ID, typAdmin, adminPayload{
		Iss: s.cfg.Issuer, Aud: AdminAudience, Sub: userID, Sid: sessionID,
		Iat: now.Unix(), Exp: exp.Unix(), AuthTime: authTime.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// AdminVerifierConfig configures an AdminVerifier.
type AdminVerifierConfig struct {
	Keys   KeySource // identity's public keys
	Issuer string
	Now    func() time.Time // optional, for tests
}

// AdminVerifier checks admin tokens. It accepts nothing else: access
// tokens, registration tokens and service tokens all fail.
type AdminVerifier struct {
	cfg AdminVerifierConfig
}

// NewAdminVerifier validates cfg and returns an AdminVerifier.
func NewAdminVerifier(cfg AdminVerifierConfig) (*AdminVerifier, error) {
	if cfg.Keys == nil || cfg.Issuer == "" {
		return nil, errors.New("anahtar kaynağı ve issuer zorunlu")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &AdminVerifier{cfg: cfg}, nil
}

// Verify checks token's structure, signature and claims, in that order, and
// returns the claims. Every failure wraps ErrInvalidToken, except a failing
// key source, which is an infrastructure error and wraps neither.
func (v *AdminVerifier) Verify(ctx context.Context, token string) (AdminClaims, error) {
	var (
		h          header
		c          adminPayload
		input, sig []byte
		zero       AdminClaims
	)
	if err := parseJWS(token, typAdmin, &h, &c, &input, &sig); err != nil {
		return zero, err
	}
	key, err := v.cfg.Keys.PublicKey(ctx, h.Kid)
	if errors.Is(err, ErrUnknownKey) {
		return zero, fmt.Errorf("%w: anahtar", ErrInvalidToken)
	}
	if err != nil {
		return zero, fmt.Errorf("açık anahtar alınamadı: %w", err)
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, input, sig) {
		return zero, fmt.Errorf("%w: imza", ErrInvalidToken)
	}

	// Claims are only read after the signature is known to be good.
	now := v.cfg.Now()
	iat, exp := time.Unix(c.Iat, 0).UTC(), time.Unix(c.Exp, 0).UTC()
	authTime := time.Unix(c.AuthTime, 0).UTC()
	switch {
	case c.Iss != v.cfg.Issuer:
		return zero, fmt.Errorf("%w: issuer", ErrInvalidToken)
	case c.Aud != AdminAudience:
		return zero, fmt.Errorf("%w: audience", ErrInvalidToken)
	case !validID(c.Sub) || !validID(c.Sid):
		return zero, fmt.Errorf("%w: kimlik", ErrInvalidToken)
	case c.Iat == 0 || !exp.After(iat) || exp.Sub(iat) > MaxAdminTokenTTL:
		return zero, fmt.Errorf("%w: ömür", ErrInvalidToken)
	case c.AuthTime == 0 || authTime.After(iat) || iat.Sub(authTime) > AdminReauthWindow:
		return zero, fmt.Errorf("%w: auth_time", ErrInvalidToken)
	case iat.After(now.Add(clockSkew)):
		return zero, fmt.Errorf("%w: gelecekte verilmiş", ErrInvalidToken)
	case !now.Before(exp.Add(clockSkew)):
		return zero, fmt.Errorf("%w: süresi dolmuş", ErrInvalidToken)
	}
	return AdminClaims{
		Subject: c.Sub, SessionID: c.Sid, Issuer: c.Iss, Audience: c.Aud,
		IssuedAt: iat, ExpiresAt: exp, AuthTime: authTime, KeyID: h.Kid,
	}, nil
}
