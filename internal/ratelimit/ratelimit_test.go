package ratelimit

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestABurstThenTheRate(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)}
	l := New(60, 3, c.now) // one a second, 3 at once
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}
	ok, wait := l.Allow("a")
	if ok {
		t.Fatal("the 4th request was allowed")
	}
	if wait < 900*time.Millisecond || wait > 1100*time.Millisecond {
		t.Errorf("retry after %s, want about a second", wait)
	}
	c.t = c.t.Add(time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Error("after a second a token is back")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Error("only one token came back")
	}
}

func TestEachCallerHasItsOwnBucket(t *testing.T) {
	c := &clock{t: time.Now()}
	l := New(60, 1, c.now)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("a")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Error("a is limited")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("b is slowed down by a")
	}
}

func TestTheBucketNeverHoldsMoreThanTheBurst(t *testing.T) {
	c := &clock{t: time.Now()}
	l := New(60, 2, c.now)
	l.Allow("a")
	c.t = c.t.Add(time.Hour)
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d after a long pause", i+1)
		}
	}
	if ok, _ := l.Allow("a"); ok {
		t.Error("an hour of silence gave more than the burst")
	}
}

func TestZeroLimitsNothingAndIdleBucketsAreForgotten(t *testing.T) {
	off := New(0, 5, nil)
	for i := 0; i < 1000; i++ {
		if ok, _ := off.Allow("a"); !ok {
			t.Fatal("a limiter of 0 refused")
		}
	}
	if off.Enabled() || (*Limiter)(nil).Enabled() {
		t.Error("Enabled")
	}

	c := &clock{t: time.Now()}
	l := New(60, 1, c.now)
	for _, k := range []string{"a", "b", "c"} {
		l.Allow(k)
	}
	c.t = c.t.Add(30 * time.Minute)
	l.Allow("d")
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 1 {
		t.Errorf("%d buckets kept, want only the one in use", n)
	}
}
