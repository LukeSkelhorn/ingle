package auth

import (
	"sync"
	"time"
)

// Limiter blocks a key after too many consecutive failures. It is in memory:
// a restart clears it, which is fine for slowing down guessing on a home
// server.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*limitEntry
}

type limitEntry struct {
	failures     int
	blockedUntil time.Time
	last         time.Time
}

// NewLimiter blocks a key for window after max failures.
func NewLimiter(maxFailures int, window time.Duration) *Limiter {
	return &Limiter{max: maxFailures, window: window, now: time.Now, entries: map[string]*limitEntry{}}
}

// Blocked reports whether key is blocked and until when.
func (l *Limiter) Blocked(key string) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return false, time.Time{}
	}
	if now := l.now(); now.Before(e.blockedUntil) {
		return true, e.blockedUntil
	}
	return false, time.Time{}
}

// Fail records a failure for key and reports whether it is now blocked.
func (l *Limiter) Fail(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	e, ok := l.entries[key]
	if !ok {
		e = &limitEntry{}
		l.entries[key] = e
	}
	// Failures spread out by more than the window don't accumulate.
	if !e.blockedUntil.IsZero() && !now.Before(e.blockedUntil) || now.Sub(e.last) > l.window {
		e.failures = 0
		e.blockedUntil = time.Time{}
	}
	e.failures++
	e.last = now
	if e.failures >= l.max {
		e.blockedUntil = now.Add(l.window)
		return true
	}
	return false
}

// Reset clears key after a success.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// sweep drops stale entries so the map can't grow without bound.
func (l *Limiter) sweep(now time.Time) {
	if len(l.entries) < 1024 {
		return
	}
	for k, e := range l.entries {
		if now.Sub(e.last) > l.window && !now.Before(e.blockedUntil) {
			delete(l.entries, k)
		}
	}
}
