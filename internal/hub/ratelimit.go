package hub

import (
	"sync"
	"time"
)

// RateLimiter implements a per-key sliding-window rate limiter with burst detection.
// Spec §14: 60 req/min sustained, 200 req/10s burst → auto-watchlist.
type RateLimiter struct {
	mu             sync.Mutex
	windows        map[string][]time.Time // per-key request timestamps
	maxRequests    int                    // sustained: max requests per windowS
	windowS        int                    // sustained window in seconds
	burstThreshold int                    // burst: max requests in burstWindowS
	burstWindowS   int
}

// NewRateLimiter creates a RateLimiter.
// maxRequests=60, windowS=60, burstThreshold=200, burstWindowS=10 matches the spec defaults.
func NewRateLimiter(maxRequests, windowS, burstThreshold, burstWindowS int) *RateLimiter {
	return &RateLimiter{
		windows:        map[string][]time.Time{},
		maxRequests:    maxRequests,
		windowS:        windowS,
		burstThreshold: burstThreshold,
		burstWindowS:   burstWindowS,
	}
}

// Check records a request for the key and returns (allowed, isFlood).
// isFlood=true means the burst threshold was exceeded; the caller should watchlist the key.
func (rl *RateLimiter) Check(key string) (allowed, isFlood bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	ts := rl.windows[key]

	// Prune old entries outside the sustained window
	cutoff := now.Add(-time.Duration(rl.windowS) * time.Second)
	fresh := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	fresh = append(fresh, now)
	rl.windows[key] = fresh

	// Burst check: count requests in the last burstWindowS seconds
	burstCutoff := now.Add(-time.Duration(rl.burstWindowS) * time.Second)
	burstCount := 0
	for _, t := range fresh {
		if t.After(burstCutoff) {
			burstCount++
		}
	}
	if burstCount > rl.burstThreshold {
		return false, true
	}

	// Sustained check
	if len(fresh) > rl.maxRequests {
		return false, false
	}
	return true, false
}

// Reset clears the rate limit state for a key (used when unblocking from watchlist).
func (rl *RateLimiter) Reset(key string) {
	rl.mu.Lock()
	delete(rl.windows, key)
	rl.mu.Unlock()
}
