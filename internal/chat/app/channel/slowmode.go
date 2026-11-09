package channel

import (
	"sync"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// maxSlowModeEntries bounds the limiter's memory; above it, entries older
// than the longest slow mode are dropped.
const maxSlowModeEntries = 100000

// slowMode remembers each user's last message time per channel, in memory
// (v14 "Spam ve taciz": bellek içi rate limiter). It resets when the
// process restarts and is not shared between pods; chat runs as a single
// replica (ADR-0005 "Ölçekleme").
type slowMode struct {
	mu   sync.Mutex
	last map[[2]id.ID]time.Time
}

func newSlowMode() *slowMode { return &slowMode{last: map[[2]id.ID]time.Time{}} }

// take reserves a message slot for user in channel at now. If the previous
// message was less than every ago, it returns how long to wait and reserves
// nothing. On success it returns a function that gives the slot back (the
// message was not written after all).
func (s *slowMode) take(channel, user id.ID, now time.Time, every time.Duration) (time.Duration, func()) {
	key := [2]id.ID{channel, user}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, had := s.last[key]
	if had {
		if wait := prev.Add(every).Sub(now); wait > 0 {
			return wait, nil
		}
	}
	if len(s.last) >= maxSlowModeEntries {
		for k, t := range s.last {
			if now.Sub(t) >= MaxSlowMode {
				delete(s.last, k)
			}
		}
	}
	s.last[key] = now
	return 0, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.last[key].Equal(now) {
			if had {
				s.last[key] = prev
			} else {
				delete(s.last, key)
			}
		}
	}
}
