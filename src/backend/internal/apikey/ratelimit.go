package apikey

import (
	"sync"
	"time"
)

// RateLimiter enforces a per-key, per-project request ceiling using a fixed 60-second
// sliding window of request timestamps. It is in-memory and therefore per-process — a
// single-instance deployment (Rigger's model) counts accurately; a multi-replica
// deployment would count per replica (documented limitation). Zero limit = unlimited.
type RateLimiter struct {
	mu  sync.Mutex
	hit map[string][]int64 // key "{keyID}\x00{ws}/{proj}" → recent request unix-nanos
}

// NewRateLimiter returns a ready limiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{hit: map[string][]int64{}}
}

// Allow records a request for (keyID, workspace/project) and reports whether it is
// within limitPerMin over the trailing 60s. limitPerMin <= 0 means unlimited (always
// allowed, nothing recorded). On allow it appends the timestamp; on deny it does not,
// so a blocked client can't push the window forward.
func (rl *RateLimiter) Allow(keyID int64, project string, limitPerMin int, now time.Time) bool {
	if limitPerMin <= 0 {
		return true
	}
	bucket := itoa(keyID) + "\x00" + project
	cutoff := now.Add(-time.Minute).UnixNano()

	rl.mu.Lock()
	defer rl.mu.Unlock()
	// Drop timestamps older than the window.
	times := rl.hit[bucket]
	kept := times[:0]
	for _, t := range times {
		if t > cutoff {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limitPerMin {
		rl.hit[bucket] = kept
		return false
	}
	rl.hit[bucket] = append(kept, now.UnixNano())
	return true
}

// itoa is a tiny int64→string without importing strconv at call sites (hot path).
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
