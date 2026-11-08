package terminal

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"
)

// Cached keeps the token of Incus that an exchange gave, for a short while, so that reading the networks of a tenant for a page does
// not mean one exchange at Authentik per request. The key is a hash of the token of the person, never the token; an entry goes when
// the token is about to expire or after MaxAge, whichever comes first, and a failure is never kept.
type Cached struct {
	Inner Exchanger
	// MaxAge bounds how long an entry is kept; Margin is how long before its expiry a token stops being served.
	MaxAge, Margin time.Duration
	Now            func() time.Time

	mu      sync.Mutex
	entries map[[32]byte]entry
	swept   time.Time
}

type entry struct {
	token Token
	until time.Time
}

// NewCached wraps an exchanger.
func NewCached(inner Exchanger, maxAge, margin time.Duration) *Cached {
	return &Cached{Inner: inner, MaxAge: maxAge, Margin: margin, entries: map[[32]byte]entry{}}
}

func (c *Cached) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Exchange serves a token that is still good, and asks the inner exchanger otherwise.
func (c *Cached) Exchange(ctx context.Context, subject string) (Token, error) {
	key := sha256.Sum256([]byte(subject))
	now := c.now()
	c.mu.Lock()
	c.sweep(now)
	if e, ok := c.entries[key]; ok && now.Before(e.until) {
		c.mu.Unlock()
		return e.token, nil
	}
	c.mu.Unlock()

	tok, err := c.Inner.Exchange(ctx, subject)
	if err != nil {
		return Token{}, err
	}
	until := now.Add(c.MaxAge)
	if exp := tok.Expires.Add(-c.Margin); exp.Before(until) {
		until = exp
	}
	if until.After(now) {
		c.mu.Lock()
		c.entries[key] = entry{token: tok, until: until}
		c.mu.Unlock()
	}
	return tok, nil
}

// sweep forgets the entries that are over, so that the map does not grow with every token there ever was. The caller holds the lock.
func (c *Cached) sweep(now time.Time) {
	if now.Sub(c.swept) < time.Minute {
		return
	}
	c.swept = now
	for k, e := range c.entries {
		if !now.Before(e.until) {
			delete(c.entries, k)
		}
	}
}
