package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// SigningKey is an Ed25519 private key with its key ID. Only identity holds
// signing keys (ADR-0004).
type SigningKey struct {
	ID      string
	private ed25519.PrivateKey
}

// ParseSigningKey builds a signing key from a base64 (standard encoding)
// 32-byte Ed25519 seed, as stored in a secret.
func ParseSigningKey(id, seedBase64 string) (SigningKey, error) {
	if !ValidKeyID(id) {
		return SigningKey{}, errors.New("anahtar kimliği geçersiz: yalnızca küçük harf, rakam ve tire, en fazla 32 karakter")
	}
	seed, err := base64.StdEncoding.DecodeString(seedBase64)
	if err != nil {
		// The decode error may echo part of the input; do not wrap it.
		return SigningKey{}, fmt.Errorf("anahtar %q base64 olarak çözülemedi", id)
	}
	if len(seed) != ed25519.SeedSize {
		return SigningKey{}, fmt.Errorf("anahtar %q %d bayt olmalı", id, ed25519.SeedSize)
	}
	return SigningKey{ID: id, private: ed25519.NewKeyFromSeed(seed)}, nil
}

// Public returns the public half of the key.
func (k SigningKey) Public() PublicKey {
	pub, _ := k.private.Public().(ed25519.PublicKey)
	return PublicKey{ID: k.ID, Key: pub}
}

// PublicKey is an Ed25519 public key with its key ID.
type PublicKey struct {
	ID  string
	Key ed25519.PublicKey
}

// SignerConfig configures a Signer.
type SignerConfig struct {
	Key      SigningKey
	Issuer   string
	Audience string
	TTL      time.Duration    // access token lifetime; ADR-0004: 15 minutes
	Now      func() time.Time // optional, for tests
}

// Signer issues access tokens.
type Signer struct {
	cfg SignerConfig
}

// NewSigner validates cfg and returns a Signer.
func NewSigner(cfg SignerConfig) (*Signer, error) {
	switch {
	case cfg.Key.private == nil:
		return nil, errors.New("imzalama anahtarı yok")
	case cfg.Issuer == "" || cfg.Audience == "":
		return nil, errors.New("issuer ve audience zorunlu")
	case cfg.TTL <= 0 || cfg.TTL > MaxTokenTTL:
		return nil, fmt.Errorf("token ömrü 0 ile %s arasında olmalı", MaxTokenTTL)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Signer{cfg: cfg}, nil
}

// Issue returns a signed access token for userID in sessionID and its
// expiry time.
func (s *Signer) Issue(userID, sessionID string) (string, time.Time, error) {
	if !validID(userID) || !validID(sessionID) {
		return "", time.Time{}, errors.New("kullanıcı ve oturum kimliği zorunlu")
	}
	now := s.cfg.Now().UTC().Truncate(time.Second)
	exp := now.Add(s.cfg.TTL)
	tok, err := sign(s.cfg.Key.private, s.cfg.Key.ID, Claims{
		Subject: userID, SessionID: sessionID,
		Issuer: s.cfg.Issuer, Audience: s.cfg.Audience,
		IssuedAt: now, ExpiresAt: exp,
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}
