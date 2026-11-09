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
	return signJWS(key, kid, typAccess, payload{
		Iss: c.Issuer, Aud: c.Audience, Sub: c.Subject, Sid: c.SessionID,
		Iat: c.IssuedAt.Unix(), Exp: c.ExpiresAt.Unix(),
	})
}

// signJWS encodes the fixed header with typ and the claims p, and signs
// them with key under kid.
func signJWS(key ed25519.PrivateKey, kid, typ string, p any) (string, error) {
	if !ValidKeyID(kid) {
		return "", errors.New("geçersiz anahtar kimliği")
	}
	h, err := json.Marshal(header{Alg: algEdDSA, Typ: typ, Kid: kid})
	if err != nil {
		return "", fmt.Errorf("token başlığı: %w", err)
	}
	pb, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("token içeriği: %w", err)
	}
	signingInput := b64.EncodeToString(h) + "." + b64.EncodeToString(pb)
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

// parse splits and decodes an access token without trusting any of it.
func parse(token string) (*parsed, error) {
	var p parsed
	if err := parseJWS(token, typAccess, &p.header, &p.payload, &p.signingInput, &p.signature); err != nil {
		return nil, err
	}
	return &p, nil
}

// parseJWS splits and decodes an Ed25519 token of type typ into h and
// claims, without trusting any of it. A token of another type is refused
// here, before its claims are read: an admin token never parses as an
// access token, nor the reverse.
func parseJWS(token, typ string, h *header, claims any, signingInput, signature *[]byte) error {
	if len(token) == 0 || len(token) > maxTokenBytes {
		return fmt.Errorf("%w: uzunluk", ErrInvalidToken)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: biçim", ErrInvalidToken)
	}
	if err := decodeStrict(parts[0], h); err != nil {
		return fmt.Errorf("%w: başlık: %w", ErrInvalidToken, err)
	}
	if h.Alg != algEdDSA || h.Typ != typ || !ValidKeyID(h.Kid) {
		return fmt.Errorf("%w: başlık değerleri", ErrInvalidToken)
	}
	if err := decodeStrict(parts[1], claims); err != nil {
		return fmt.Errorf("%w: içerik: %w", ErrInvalidToken, err)
	}
	sig, err := b64.Strict().DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: imza biçimi", ErrInvalidToken)
	}
	*signature = sig
	*signingInput = []byte(token[:len(parts[0])+1+len(parts[1])])
	return nil
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
