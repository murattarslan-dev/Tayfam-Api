package auth

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// keyCache caches public keys fetched from a remote source. Both the access
// token keys from identity (RemoteKeys) and the Kubernetes service account
// keys (ServiceVerifier, ADR-0013) go through it, so the same rules apply to
// both:
//
//   - keys are cached for the TTL the source gives, clamped to
//     [minCacheTTL, maxCacheTTL];
//   - an unknown key ID triggers a refetch at most every minRefetch, so tokens
//     with random key IDs cannot flood the source;
//   - while the source is unreachable, known keys stay usable for staleGrace
//     after they expire.
//
// It is safe for concurrent use.
type keyCache[K any] struct {
	fetch func(ctx context.Context) (map[string]K, time.Duration, error)
	now   func() time.Time

	mu        sync.Mutex
	keys      map[string]K
	expires   time.Time
	lastFetch time.Time
}

func (c *keyCache[K]) get(ctx context.Context, kid string) (K, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var zero K
	now := c.now()
	fresh := c.keys != nil && now.Before(c.expires)
	if k, ok := c.keys[kid]; ok && fresh {
		return k, nil
	}
	if fresh && now.Sub(c.lastFetch) < minRefetch {
		return zero, ErrUnknownKey
	}

	if err := c.refresh(ctx, now); err != nil {
		// The source is unreachable: keep using known keys for a short while.
		if k, ok := c.keys[kid]; ok && now.Before(c.expires.Add(staleGrace)) {
			return k, nil
		}
		return zero, err
	}
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	return zero, ErrUnknownKey
}

func (c *keyCache[K]) refresh(ctx context.Context, now time.Time) error {
	c.lastFetch = now
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	keys, ttl, err := c.fetch(ctx)
	if err != nil {
		return fmt.Errorf("açık anahtarlar alınamadı: %w", err)
	}
	c.keys = keys
	c.expires = now.Add(min(max(ttl, minCacheTTL), maxCacheTTL))
	return nil
}
