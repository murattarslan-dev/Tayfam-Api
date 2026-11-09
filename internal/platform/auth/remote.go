package auth

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

const (
	minCacheTTL = 30 * time.Second
	maxCacheTTL = time.Hour
	// minRefetch limits how often an unknown key ID can trigger a fetch, so
	// tokens with random key IDs cannot be used to flood identity.
	minRefetch = 30 * time.Second
	// staleGrace is how long expired keys are still used while identity is
	// unreachable. Kept short so a key removed after a leak stops being
	// trusted soon even during an outage (ADR-0011).
	staleGrace   = 10 * time.Minute
	fetchTimeout = 3 * time.Second
)

// FetchFunc returns the current public keys and how long they may be cached.
type FetchFunc func(ctx context.Context) ([]PublicKey, time.Duration, error)

// RemoteKeys is a KeySource that fetches public keys from identity and
// caches them (keyCache rules). It is safe for concurrent use.
type RemoteKeys struct {
	cache keyCache[ed25519.PublicKey]
}

// NewRemoteKeys returns a RemoteKeys that loads keys with fetch on first use.
// Keys with a malformed ID or length are dropped.
func NewRemoteKeys(fetch FetchFunc) *RemoteKeys {
	r := &RemoteKeys{}
	r.cache.now = time.Now
	r.cache.fetch = func(ctx context.Context) (map[string]ed25519.PublicKey, time.Duration, error) {
		list, ttl, err := fetch(ctx)
		if err != nil {
			return nil, 0, err
		}
		keys := make(map[string]ed25519.PublicKey, len(list))
		for _, k := range list {
			if ValidKeyID(k.ID) && len(k.Key) == ed25519.PublicKeySize {
				keys[k.ID] = k.Key
			}
		}
		return keys, ttl, nil
	}
	return r
}

// PublicKey implements KeySource.
func (r *RemoteKeys) PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	return r.cache.get(ctx, kid)
}

// FetchFromIdentity returns a FetchFunc backed by identity's KeyService.
// Keys with an unknown algorithm or malformed contents are skipped.
func FetchFromIdentity(c identityv1.KeyServiceClient) FetchFunc {
	return func(ctx context.Context) ([]PublicKey, time.Duration, error) {
		resp, err := c.ListPublicKeys(ctx, &identityv1.ListPublicKeysRequest{})
		if err != nil {
			return nil, 0, fmt.Errorf("ListPublicKeys: %w", err)
		}
		out := make([]PublicKey, 0, len(resp.GetKeys()))
		for _, k := range resp.GetKeys() {
			if k.GetAlgorithm() != identityv1.KeyAlgorithm_KEY_ALGORITHM_ED25519 {
				continue
			}
			out = append(out, PublicKey{ID: k.GetKid(), Key: ed25519.PublicKey(k.GetPublicKey())})
		}
		return out, resp.GetCacheTtl().AsDuration(), nil
	}
}
