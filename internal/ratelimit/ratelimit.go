// Package ratelimit slows a caller that writes too much: one bucket of tokens per caller, refilled at a steady rate. It is in the
// memory of one replica of the API: with several, a caller gets the limit of each, which still bounds it.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// Limiter is a set of token buckets, one per key.
type Limiter struct {
	rate  float64 // tokens per second
	burst float64
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// idle is how long a bucket that is full again is kept: after that it is the same as a new one.
const idle = 10 * time.Minute

// New returns a limiter that lets a key do perMinute requests a minute, in bursts of up to burst. perMinute 0 limits nothing. now is
// time.Now when nil.
func New(perMinute, burst int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{rate: float64(perMinute) / 60, burst: float64(burst), now: now, buckets: map[string]*bucket{}}
}

// Enabled reports whether the limiter limits anything.
func (l *Limiter) Enabled() bool { return l != nil && l.rate > 0 }

// Allow takes a token for key. When there is none it says how long to wait for the next one.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	if !l.Enabled() {
		return true, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	b, found := l.buckets[key]
	if !found {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// sweep forgets the buckets nobody has used for a while, so that the map does not grow with every caller there ever was.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < time.Minute {
		return
	}
	l.swept = now
	for k, b := range l.buckets {
		if now.Sub(b.last) > idle {
			delete(l.buckets, k)
		}
	}
}
