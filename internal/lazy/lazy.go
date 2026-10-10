// Package lazy holds a value that is read on its first use and read again
// when it is older than its time to live.
//
// A module opens without reading a secret (ADR 0072, decision 4): a start
// that read them would make one call per secret in every new environment of a
// herd, and the secrets store throttles a herd. The first request that needs a
// secret reads it through a [Value]; a request after the time to live reads it
// again, so a rotated secret is picked up without a restart.
package lazy

import (
	"context"
	"sync"
	"time"
)

// TTL is how long a read is reused before the next use reads again. It is the
// secrets source's own cache period, so a secret is at most two periods old.
const TTL = 30 * time.Second

// Value is one lazily read value. It is safe for concurrent use; concurrent
// first uses share one read.
type Value[T any] struct {
	load func(context.Context) (T, error)
	ttl  time.Duration
	now  func() time.Time

	mu    sync.Mutex
	value T
	at    time.Time
	have  bool
}

// New returns a Value that calls load on first use and again once ttl has
// passed. A ttl of zero or less is [TTL]. Nothing is read here.
func New[T any](ttl time.Duration, load func(context.Context) (T, error)) *Value[T] {
	if ttl <= 0 {
		ttl = TTL
	}
	return &Value[T]{load: load, ttl: ttl, now: time.Now}
}

// Get returns the value, reading it when there is none or it is older than the
// time to live. A failed read is returned and is not remembered: the next use
// tries again, and a value that could not be refreshed is not served past its
// time to live.
func (v *Value[T]) Get(ctx context.Context) (T, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.have && v.now().Sub(v.at) < v.ttl {
		return v.value, nil
	}
	got, err := v.load(ctx)
	if err != nil {
		var zero T
		v.have = false
		return zero, err
	}
	v.value, v.at, v.have = got, v.now(), true
	return got, nil
}

// SetClock replaces the clock. For tests.
func (v *Value[T]) SetClock(now func() time.Time) { v.now = now }
