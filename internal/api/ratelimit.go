package api

import (
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a simple per-user sliding-window rate limiter.
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time // userID → request timestamps
	rpm     int                    // requests per minute (0 = unlimited)
	window  time.Duration
}

func newRateLimiter(rpm int) *rateLimiter {
	if rpm <= 0 {
		return &rateLimiter{rpm: 0}
	}
	return &rateLimiter{
		windows: make(map[string][]time.Time),
		rpm:     rpm,
		window:  time.Minute,
	}
}

// allow returns true if the request for userID should be permitted.
func (rl *rateLimiter) allow(userID string) bool {
	if rl.rpm <= 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Prune expired entries.
	ts := rl.windows[userID]
	start := 0
	for start < len(ts) && ts[start].Before(cutoff) {
		start++
	}
	ts = ts[start:]

	if len(ts) >= rl.rpm {
		rl.windows[userID] = ts
		return false
	}
	rl.windows[userID] = append(ts, now)
	return true
}

// cleanup periodically purges stale entries. Call in a goroutine.
func (rl *rateLimiter) cleanup(interval time.Duration, done <-chan struct{}) {
	if rl.rpm <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			rl.mu.Lock()
			cutoff := time.Now().Add(-rl.window)
			for uid, ts := range rl.windows {
				start := 0
				for start < len(ts) && ts[start].Before(cutoff) {
					start++
				}
				if start == len(ts) {
					delete(rl.windows, uid)
				} else {
					rl.windows[uid] = ts[start:]
				}
			}
			rl.mu.Unlock()
		}
	}
}

// rateLimitMiddleware wraps a handler and returns 429 when a user exceeds
// the configured RPM.
func rateLimitMiddleware(rl *rateLimiter, getUserID func(r *http.Request) string, next http.HandlerFunc) http.HandlerFunc {
	if rl == nil || rl.rpm <= 0 {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		uid := getUserID(r)
		if !rl.allow(uid) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error": map[string]string{
					"message": "rate limit exceeded — try again shortly",
					"type":    "rate_limit_error",
				},
			})
			return
		}
		next(w, r)
	}
}
