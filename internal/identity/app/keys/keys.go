// Package keys holds identity's token signing keys (ADR-0004).
//
// identity signs with one active private key. Keys used before a rotation
// are kept as public keys only, until every token they signed has expired;
// their private halves are destroyed and never configured here (ADR-0011).
package keys

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
)

// Ring is the active signing key plus the public halves of retired keys.
type Ring struct {
	active  auth.SigningKey
	retired []auth.PublicKey
}

// Parse builds a Ring from configuration values:
//
//	signing: "<kid>:<base64 32-byte seed>"           (secret)
//	retired: "<kid>:<base64 32-byte public key>,..." (not secret, may be empty)
func Parse(signing, retired string) (*Ring, error) {
	kid, seed, ok := strings.Cut(signing, ":")
	if !ok {
		return nil, errors.New("imzalama anahtarı \"<kid>:<base64>\" biçiminde olmalı")
	}
	active, err := auth.ParseSigningKey(kid, seed)
	if err != nil {
		return nil, err
	}
	r := &Ring{active: active}
	seen := map[string]bool{kid: true}
	for entry := range strings.SplitSeq(retired, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, b64, ok := strings.Cut(entry, ":")
		if !ok || !auth.ValidKeyID(id) {
			return nil, fmt.Errorf("emekli anahtar girdisi geçersiz: %q", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("anahtar kimliği tekrar ediyor: %q", id)
		}
		seen[id] = true
		pub, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("emekli anahtar %q: %d baytlık base64 açık anahtar olmalı", id, ed25519.PublicKeySize)
		}
		r.retired = append(r.retired, auth.PublicKey{ID: id, Key: pub})
	}
	return r, nil
}

// Active returns the key new tokens are signed with.
func (r *Ring) Active() auth.SigningKey { return r.active }

// PublicKeys returns every key that may verify a live token, active first.
func (r *Ring) PublicKeys() []auth.PublicKey {
	return append([]auth.PublicKey{r.active.Public()}, r.retired...)
}

// Static returns the public keys as a KeySource; identity verifies tokens
// with its own keys and does not call itself.
func (r *Ring) Static() auth.StaticKeys {
	s := make(auth.StaticKeys, 1+len(r.retired))
	for _, k := range r.PublicKeys() {
		s[k.ID] = k.Key
	}
	return s
}
