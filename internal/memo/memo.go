// Package memo remembers the outcome of a pure, expensive check by a hash of
// its input.
//
// The documents sluis validates (a service document, the policy document, a
// module's document) are checked against JSON schemas and decoded, which costs
// seconds for a realistic estate, and a stack asks again and again about the
// same bytes. Only functions of their input bytes alone go through here: the
// outcome, error included, is the same every time, so it is remembered.
package memo

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// Key is a hash of the inputs a function depends on.
type Key [sha256.Size]byte

// KeyOf hashes parts so that no two different sequences of parts collide on
// the boundary between them.
func KeyOf(parts ...[]byte) Key {
	h := sha256.New()
	var n [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write(p)
	}
	var k Key
	h.Sum(k[:0])
	return k
}

type entry[V any] struct {
	once sync.Once
	val  V
	err  error
}

// Cache holds up to a fixed number of outcomes, the oldest dropped first. It is
// safe for concurrent use. The zero value is not usable: call [New].
type Cache[V any] struct {
	max   int
	mu    sync.Mutex
	items map[Key]*entry[V]
	order []Key
}

// New is a cache of at most max outcomes.
func New[V any](max int) *Cache[V] {
	return &Cache[V]{max: max, items: map[Key]*entry[V]{}}
}

// Do returns the outcome of compute for key, computing it once per key even
// when callers ask at the same time. A value is shared between callers: they
// must not modify it (or must copy it, as the caller of a slice does).
func (c *Cache[V]) Do(key Key, compute func() (V, error)) (V, error) {
	c.mu.Lock()
	e, ok := c.items[key]
	if !ok {
		e = &entry[V]{}
		c.items[key] = e
		c.order = append(c.order, key)
		if len(c.order) > c.max {
			delete(c.items, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.mu.Unlock()
	e.once.Do(func() { e.val, e.err = compute() })
	return e.val, e.err
}
