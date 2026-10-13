package sluispulumi

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// outcomes remembers the result of loading documents, a function of their
// bytes alone: loading holds each to its JSON schema and decodes it, which costs
// seconds for a realistic estate, and a stack asks again and again about the
// same documents. It computes once per key (also when callers ask at the same
// time) and holds the newest outcomes only. A remembered value is shared: the
// callers read it and never modify it. (The same cache lives in the root
// module's internal/memo, which this library may not import.)
type outcomes[V any] struct {
	limit int
	mu    sync.Mutex
	items map[[sha256.Size]byte]*outcome[V]
	order [][sha256.Size]byte
}

type outcome[V any] struct {
	once sync.Once
	val  V
	err  error
}

func newOutcomes[V any](limit int) *outcomes[V] {
	return &outcomes[V]{limit: limit, items: map[[sha256.Size]byte]*outcome[V]{}}
}

// outcomeKey hashes parts so that no two sequences of parts collide on the
// boundary between them.
func outcomeKey(parts ...string) [sha256.Size]byte {
	h := sha256.New()
	var n [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
	}
	var k [sha256.Size]byte
	h.Sum(k[:0])
	return k
}

func (c *outcomes[V]) do(key [sha256.Size]byte, compute func() (V, error)) (V, error) {
	c.mu.Lock()
	e, ok := c.items[key]
	if !ok {
		e = &outcome[V]{}
		c.items[key] = e
		c.order = append(c.order, key)
		if len(c.order) > c.limit {
			delete(c.items, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.mu.Unlock()
	e.once.Do(func() { e.val, e.err = compute() })
	return e.val, e.err
}
