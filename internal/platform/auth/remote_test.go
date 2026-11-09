package auth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeFetch struct {
	mu    sync.Mutex
	keys  []PublicKey
	ttl   time.Duration
	err   error
	calls int
}

func (f *fakeFetch) fetch(context.Context) ([]PublicKey, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.keys, f.ttl, f.err
}

func newRemote(t *testing.T, f *fakeFetch, now *time.Time) *RemoteKeys {
	t.Helper()
	r := NewRemoteKeys(f.fetch)
	r.cache.now = func() time.Time { return *now }
	return r
}

func TestRemoteKeysCaches(t *testing.T) {
	k1 := testKey(t, "k1").Public()
	f := &fakeFetch{keys: []PublicKey{k1}, ttl: 5 * time.Minute}
	now := t0
	r := newRemote(t, f, &now)
	ctx := context.Background()

	for range 3 {
		if _, err := r.PublicKey(ctx, "k1"); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("fetch %d kez çağrıldı, 1 bekleniyordu", f.calls)
	}
	now = now.Add(6 * time.Minute)
	if _, err := r.PublicKey(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 {
		t.Fatalf("süre dolunca yenilenmedi: %d", f.calls)
	}
}

func TestRemoteKeysUnknownKidIsRateLimited(t *testing.T) {
	f := &fakeFetch{keys: []PublicKey{testKey(t, "k1").Public()}, ttl: time.Hour}
	now := t0
	r := newRemote(t, f, &now)
	ctx := context.Background()
	if _, err := r.PublicKey(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	for _, kid := range []string{"x1", "x2", "x3", "x4"} {
		if _, err := r.PublicKey(ctx, kid); !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("%s: err = %v", kid, err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("rastgele kid'ler %d fetch tetikledi", f.calls-1)
	}

	// A newly rotated key is picked up once the refetch interval passes.
	f.keys = append(f.keys, testKey(t, "k2").Public())
	now = now.Add(minRefetch)
	if _, err := r.PublicKey(ctx, "k2"); err != nil {
		t.Fatalf("yeni anahtar alınmadı: %v", err)
	}
}

func TestRemoteKeysStaleGrace(t *testing.T) {
	f := &fakeFetch{keys: []PublicKey{testKey(t, "k1").Public()}, ttl: time.Minute}
	now := t0
	r := newRemote(t, f, &now)
	ctx := context.Background()
	if _, err := r.PublicKey(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	f.err = errors.New("identity erişilemez")

	now = t0.Add(time.Minute + staleGrace - time.Second)
	if _, err := r.PublicKey(ctx, "k1"); err != nil {
		t.Fatalf("kısa kesintide bilinen anahtar kullanılmalı: %v", err)
	}
	now = t0.Add(time.Minute + staleGrace + time.Second)
	_, err := r.PublicKey(ctx, "k1")
	if err == nil || errors.Is(err, ErrUnknownKey) {
		t.Fatalf("süre aşımında altyapı hatası bekleniyordu: %v", err)
	}
}

func TestRemoteKeysDropsMalformedAndClampsTTL(t *testing.T) {
	f := &fakeFetch{
		keys: []PublicKey{
			{ID: "kısa", Key: make(ed25519.PublicKey, 16)},
			{ID: "../bad", Key: make(ed25519.PublicKey, ed25519.PublicKeySize)},
			testKey(t, "k1").Public(),
		},
		ttl: 30 * 24 * time.Hour,
	}
	now := t0
	r := newRemote(t, f, &now)
	ctx := context.Background()
	if _, err := r.PublicKey(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if len(r.cache.keys) != 1 {
		t.Fatalf("bozuk anahtarlar elenmedi: %d anahtar", len(r.cache.keys))
	}
	if got := r.cache.expires.Sub(now); got != maxCacheTTL {
		t.Fatalf("TTL sınırlanmadı: %v", got)
	}
}
