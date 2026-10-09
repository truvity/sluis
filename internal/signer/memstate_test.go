package signer

import (
	"context"
	"sort"
	"sync"
	"time"
)

// memState is a State kept in one process, with a clock the test moves.
type memState struct {
	mu     sync.Mutex
	now    func() time.Time
	values map[string]memValue
	sets   map[string]map[string]time.Time
}

type memValue struct {
	data    []byte
	expires time.Time
}

func newMemState() *memState {
	return &memState{now: time.Now, values: map[string]memValue{}, sets: map[string]map[string]time.Time{}}
}

func (m *memState) SetClock(now func() time.Time) { m.now = now }

func (m *memState) live(key string) (memValue, bool) {
	v, ok := m.values[key]
	if ok && !v.expires.IsZero() && !m.now().Before(v.expires) {
		delete(m.values, key)
		return memValue{}, false
	}
	return v, ok
}

func (m *memState) put(key string, value []byte, ttl time.Duration) {
	v := memValue{data: append([]byte(nil), value...)}
	if ttl > 0 {
		v.expires = m.now().Add(ttl)
	}
	m.values[key] = v
}

func (m *memState) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.live(key)
	return append([]byte(nil), v.data...), ok, nil
}

func (m *memState) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.put(key, value, ttl)
	return nil
}

func (m *memState) SetIfAbsent(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.live(key); ok {
		return false, nil
	}
	m.put(key, value, ttl)
	return true, nil
}

func (m *memState) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, key)
	return nil
}

func (m *memState) Add(_ context.Context, key, member string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sets[key] == nil {
		m.sets[key] = map[string]time.Time{}
	}
	m.sets[key][member] = m.now().Add(ttl)
	return nil
}

func (m *memState) Remove(_ context.Context, key, member string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sets[key], member)
	return nil
}

func (m *memState) Members(_ context.Context, key string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for member, exp := range m.sets[key] {
		if m.now().Before(exp) {
			out = append(out, member)
		}
	}
	sort.Strings(out)
	return out, nil
}
