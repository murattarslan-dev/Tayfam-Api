package auth

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
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
// caches them. It is safe for concurrent use.
type RemoteKeys struct {
	fetch FetchFunc
	now   func() time.Time

	mu        sync.Mutex
	keys      map[string]ed25519.PublicKey
	expires   time.Time
	lastFetch time.Time
}

// NewRemoteKeys returns a RemoteKeys that loads keys with fetch on first use.
func NewRemoteKeys(fetch FetchFunc) *RemoteKeys {
	return &RemoteKeys{fetch: fetch, now: time.Now}
}

// PublicKey implements KeySource.
func (r *RemoteKeys) PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	fresh := r.keys != nil && now.Before(r.expires)
	if k, ok := r.keys[kid]; ok && fresh {
		return k, nil
	}
	if fresh && now.Sub(r.lastFetch) < minRefetch {
		return nil, ErrUnknownKey
	}

	if err := r.refresh(ctx, now); err != nil {
		// identity is unreachable: keep using known keys for a short while.
		if k, ok := r.keys[kid]; ok && now.Before(r.expires.Add(staleGrace)) {
			return k, nil
		}
		return nil, err
	}
	if k, ok := r.keys[kid]; ok {
		return k, nil
	}
	return nil, ErrUnknownKey
}

func (r *RemoteKeys) refresh(ctx context.Context, now time.Time) error {
	r.lastFetch = now
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	list, ttl, err := r.fetch(ctx)
	if err != nil {
		return fmt.Errorf("açık anahtarlar alınamadı: %w", err)
	}
	keys := make(map[string]ed25519.PublicKey, len(list))
	for _, k := range list {
		if ValidKeyID(k.ID) && len(k.Key) == ed25519.PublicKeySize {
			keys[k.ID] = k.Key
		}
	}
	r.keys = keys
	r.expires = now.Add(min(max(ttl, minCacheTTL), maxCacheTTL))
	return nil
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
