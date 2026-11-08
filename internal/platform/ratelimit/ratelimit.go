// Package ratelimit is an in-memory token bucket keyed by a string such as
// a client IP or a user ID (ADR-0004). Each service runs one replica, so
// memory is enough; counters reset on restart, which is accepted.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows Burst events at once and refills at Rate per second.
type Limiter struct {
	rate  float64
	burst float64
	now   func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
	maxKeys   int
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a limiter allowing burst events, refilled at perMinute.
func New(perMinute, burst int) *Limiter {
	return &Limiter{
		rate: float64(perMinute) / 60, burst: float64(burst), now: time.Now,
		buckets: map[string]*bucket{}, maxKeys: 100_000,
	}
}

// NewPer returns a limiter allowing n events per window: a burst of n,
// refilled evenly over the window (for example 10 per hour).
func NewPer(n int, window time.Duration) *Limiter {
	return &Limiter{
		rate: float64(n) / window.Seconds(), burst: float64(n), now: time.Now,
		buckets: map[string]*bucket{}, maxKeys: 100_000,
	}
}

// Allow reports whether one more event for key is allowed now.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) > time.Minute {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxKeys {
			// Under a flood of distinct keys, refuse rather than grow without
			// bound.
			return false
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep forgets buckets that have refilled completely.
func (l *Limiter) sweep(now time.Time) {
	l.lastSweep = now
	full := time.Duration(l.burst / l.rate * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) > full {
			delete(l.buckets, k)
		}
	}
}
