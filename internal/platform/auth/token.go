// Package auth issues and verifies access tokens (ADR-0004) and carries the
// verified caller through gRPC (ADR-0011).
//
// Tokens are compact JWS (JWT) signed with Ed25519. The format is
// deliberately narrow: exactly one algorithm (EdDSA), one token type
// (at+jwt, RFC 9068), a fixed header and a fixed claim set. There is no
// algorithm negotiation, so "alg: none" and algorithm-confusion attacks have
// nothing to work with. Signatures use crypto/ed25519 from the standard
// library; this package only handles the encoding around it.
package auth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	algEdDSA  = "EdDSA"
	typAccess = "at+jwt"

	// maxTokenBytes bounds the work done on untrusted input before the
	// signature is checked. Real tokens are around 350 bytes.
	maxTokenBytes = 2048
	// maxIDLength bounds sub, sid and kid values.
	maxIDLength = 64
)

// ErrInvalidToken is returned for every verification failure. Callers get no
// detail about which check failed; the wrapped cause is for tests and logs
// only and never contains token material.
var ErrInvalidToken = errors.New("geçersiz token")

var kidPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidKeyID reports whether kid is a well-formed key ID.
func ValidKeyID(kid string) bool { return kidPattern.MatchString(kid) }

// Claims are the verified contents of an access token. Roles and
// permissions are deliberately absent; they are read from core on every
// request (ADR-0004).
type Claims struct {
	Subject   string // user ID
	SessionID string
	Issuer    string
	Audience  string
	IssuedAt  time.Time
	ExpiresAt time.Time
	KeyID     string
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type payload struct {
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	Sub string `json:"sub"`
	Sid string `json:"sid"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

var b64 = base64.RawURLEncoding

// sign encodes c and signs it with key under kid.
func sign(key ed25519.PrivateKey, kid string, c Claims) (string, error) {
	if !ValidKeyID(kid) {
		return "", errors.New("geçersiz anahtar kimliği")
	}
	h, err := json.Marshal(header{Alg: algEdDSA, Typ: typAccess, Kid: kid})
	if err != nil {
		return "", fmt.Errorf("token başlığı: %w", err)
	}
	p, err := json.Marshal(payload{
		Iss: c.Issuer, Aud: c.Audience, Sub: c.Subject, Sid: c.SessionID,
		Iat: c.IssuedAt.Unix(), Exp: c.ExpiresAt.Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("token içeriği: %w", err)
	}
	signingInput := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	sig := ed25519.Sign(key, []byte(signingInput))
	return signingInput + "." + b64.EncodeToString(sig), nil
}

// parsed is a token whose structure is valid but whose signature and claims
// have not been checked yet.
type parsed struct {
	header       header
	payload      payload
	signingInput []byte
	signature    []byte
}

// parse splits and decodes a token without trusting any of it.
func parse(token string) (*parsed, error) {
	if len(token) == 0 || len(token) > maxTokenBytes {
		return nil, fmt.Errorf("%w: uzunluk", ErrInvalidToken)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: biçim", ErrInvalidToken)
	}
	var p parsed
	if err := decodeStrict(parts[0], &p.header); err != nil {
		return nil, fmt.Errorf("%w: başlık: %w", ErrInvalidToken, err)
	}
	if p.header.Alg != algEdDSA || p.header.Typ != typAccess || !ValidKeyID(p.header.Kid) {
		return nil, fmt.Errorf("%w: başlık değerleri", ErrInvalidToken)
	}
	if err := decodeStrict(parts[1], &p.payload); err != nil {
		return nil, fmt.Errorf("%w: içerik: %w", ErrInvalidToken, err)
	}
	sig, err := b64.Strict().DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: imza biçimi", ErrInvalidToken)
	}
	p.signature = sig
	p.signingInput = []byte(token[:len(parts[0])+1+len(parts[1])])
	return &p, nil
}

// decodeStrict decodes one base64url JSON segment, rejecting padding,
// unknown fields and trailing data.
func decodeStrict(seg string, v any) error {
	raw, err := b64.Strict().DecodeString(seg)
	if err != nil {
		return fmt.Errorf("base64: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("json: %w", err)
	}
	if dec.More() {
		return errors.New("json: fazladan veri")
	}
	return nil
}

func validID(s string) bool { return s != "" && len(s) <= maxIDLength }
