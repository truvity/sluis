// Package memory is an in-process [state.Store] that keeps every version. It
// is the reference backend for the conformance suite and the one tests use.
package memory

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/storage/state"
)

type entry struct {
	next     int // the next version number; survives Delete so a Rev is never reused
	versions []state.Item
}

type tree struct {
	mu      sync.Mutex
	clock   func() time.Time
	entries map[string]*entry // full key -> entry
}

// Option configures [New].
type Option func(*tree)

// WithClock sets the clock that stamps Item.Modified.
func WithClock(now func() time.Time) Option { return func(t *tree) { t.clock = now } }

type store struct {
	t      *tree
	prefix string // "" or ends with "/"
}

// New returns an empty store. Revs are "1", "2", ... per key.
func New(opts ...Option) state.Store {
	t := &tree{clock: time.Now, entries: map[string]*entry{}}
	for _, o := range opts {
		o(t)
	}
	return &store{t: t}
}

func (s *store) Child(prefix string, _ ...state.Option) state.Store {
	return &store{t: s.t, prefix: s.prefix + strings.Trim(prefix, "/") + "/"}
}

func (s *store) full(key string) (string, error) {
	if err := state.ValidateKey(key); err != nil {
		return "", err
	}
	return s.prefix + key, nil
}

func (s *store) Get(ctx context.Context, key string) (state.Item, error) {
	return s.read(ctx, key, "")
}

func (s *store) GetRev(ctx context.Context, key string, rev state.Rev) (state.Item, error) {
	if rev == "" {
		return state.Item{}, state.ErrNotFound
	}
	return s.read(ctx, key, rev)
}

func (s *store) read(ctx context.Context, key string, rev state.Rev) (state.Item, error) {
	if err := ctx.Err(); err != nil {
		return state.Item{}, err
	}
	k, err := s.full(key)
	if err != nil {
		return state.Item{}, err
	}
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	e := s.t.entries[k]
	if e == nil || len(e.versions) == 0 {
		return state.Item{}, state.ErrNotFound
	}
	if rev == "" {
		return clone(e.versions[len(e.versions)-1]), nil
	}
	for _, it := range e.versions {
		if it.Rev == rev {
			return clone(it), nil
		}
	}
	return state.Item{}, state.ErrNotFound
}

func (s *store) Put(ctx context.Context, key string, value []byte, ifRev state.Rev) (state.Rev, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	k, err := s.full(key)
	if err != nil {
		return "", err
	}
	if err := state.ValidateObject(value); err != nil {
		return "", err
	}
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	e := s.t.entries[k]
	if e == nil {
		e = &entry{}
		s.t.entries[k] = e
	}
	var prev state.Rev
	if n := len(e.versions); n > 0 {
		prev = e.versions[n-1].Rev
	}
	if prev != ifRev {
		return "", state.ErrConflict
	}
	e.next++
	rev := state.Rev(strconv.Itoa(e.next))
	e.versions = append(e.versions, state.Item{
		Value:    append([]byte(nil), value...),
		Rev:      rev,
		Modified: s.t.clock(),
		Previous: prev,
	})
	return rev, nil
}

func (s *store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	k, err := s.full(key)
	if err != nil {
		return err
	}
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	e := s.t.entries[k]
	if e == nil || len(e.versions) == 0 {
		return state.ErrNotFound
	}
	e.versions = nil // keep e.next: a recreated key never reuses a Rev
	return nil
}

func (s *store) List(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	out := []string{}
	for k, e := range s.t.entries {
		if len(e.versions) == 0 || !strings.HasPrefix(k, s.prefix) {
			continue
		}
		rest := k[len(s.prefix):]
		if !strings.Contains(rest, "/") {
			out = append(out, rest)
		}
	}
	sort.Strings(out)
	return out, nil
}

func clone(it state.Item) state.Item {
	it.Value = append([]byte(nil), it.Value...)
	return it
}
