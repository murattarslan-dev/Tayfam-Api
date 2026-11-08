package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxJWKSBytes   = 64 << 10
	minJWKSCache   = 5 * time.Minute
	maxJWKSCache   = 24 * time.Hour
	jwksRefetchGap = 30 * time.Second
	minRSABits     = 2048
)

// errKeysUnavailable means the provider's keys could not be fetched. It is
// not ErrInvalidToken: the token may be fine and the client may retry.
var errKeysUnavailable = errors.New("sağlayıcı anahtarları alınamadı")

// jwks fetches and caches a provider's RSA signing keys. Unknown key IDs
// trigger a refetch at most every jwksRefetchGap, so random kids cannot be
// used to hammer the provider or this service.
type jwks struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	expires   time.Time
	lastFetch time.Time
}

func newJWKS(url string, client *http.Client) *jwks {
	return &jwks{url: url, client: client, now: time.Now}
}

func (j *jwks) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	fresh := j.keys != nil && now.Before(j.expires)
	if k, ok := j.keys[kid]; ok && fresh {
		return k, nil
	}
	if j.keys != nil && now.Sub(j.lastFetch) < jwksRefetchGap {
		if k, ok := j.keys[kid]; ok {
			return k, nil
		}
		return nil, fmt.Errorf("%w: bilinmeyen anahtar", ErrInvalidToken)
	}
	if err := j.fetch(ctx, now); err != nil {
		if k, ok := j.keys[kid]; ok {
			return k, nil // provider unreachable; keep using a known key
		}
		return nil, err
	}
	if k, ok := j.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: bilinmeyen anahtar", ErrInvalidToken)
}

func (j *jwks) fetch(ctx context.Context, now time.Time) error {
	j.lastFetch = now
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", errKeysUnavailable, err)
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", errKeysUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", errKeysUnavailable, resp.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&doc); err != nil {
		return fmt.Errorf("%w: JSON: %w", errKeysUnavailable, err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		n, errN := b64.DecodeString(k.N)
		e, errE := b64.DecodeString(k.E)
		if errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < minRSABits || pub.E < 3 || pub.E%2 == 0 {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return fmt.Errorf("%w: kullanılabilir anahtar yok", errKeysUnavailable)
	}
	j.keys = keys
	j.expires = now.Add(cacheFor(resp.Header.Get("Cache-Control")))
	return nil
}

// cacheFor reads max-age from Cache-Control, clamped to sane bounds.
func cacheFor(cc string) time.Duration {
	for part := range strings.SplitSeq(cc, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(k, "max-age") {
			if s, err := strconv.Atoi(v); err == nil {
				return min(max(time.Duration(s)*time.Second, minJWKSCache), maxJWKSCache)
			}
		}
	}
	return minJWKSCache
}
