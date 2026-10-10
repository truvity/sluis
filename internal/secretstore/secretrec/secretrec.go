// Package secretrec is a state.Store that records the full address of every
// read and write, for the tests that pin which path a caller uses on a secrets
// layout. It keeps the values in memory and never records one.
package secretrec

import (
	"context"
	"slices"
	"sync"

	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/memory"
)

// Store is the recording store.
type Store struct {
	state.Store
	log *log
}

type log struct {
	mu  sync.Mutex
	ops []string
}

// New returns an empty recording store.
func New() *Store { return &Store{Store: memory.New(), log: &log{}} }

// Ops are the operations seen, each "<op> <full address>", in order.
func (s *Store) Ops() []string {
	s.log.mu.Lock()
	defer s.log.mu.Unlock()
	return slices.Clone(s.log.ops)
}

// Addresses are the sorted, distinct full addresses of the operations seen
// (op "get", "put", "delete" or "list" included).
func (s *Store) Addresses(op string) []string {
	var out []string
	for _, o := range s.Ops() {
		if len(o) > len(op)+1 && o[:len(op)+1] == op+" " {
			out = append(out, o[len(op)+1:])
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Reset forgets the operations seen.
func (s *Store) Reset() {
	s.log.mu.Lock()
	s.log.ops = nil
	s.log.mu.Unlock()
}

func (l *log) add(op, addr string) {
	l.mu.Lock()
	l.ops = append(l.ops, op+" "+addr)
	l.mu.Unlock()
}

// view is a prefix of the recording store.
type view struct {
	state.Store
	prefix string
	log    *log
}

// Child implements [state.Store].
func (s *Store) Child(prefix string, opts ...state.Option) state.Store {
	return &view{Store: s.Store.Child(prefix, opts...), prefix: prefix + "/", log: s.log}
}

// Get implements [state.Store].
func (s *Store) Get(ctx context.Context, key string) (state.Item, error) {
	s.log.add("get", key)
	return s.Store.Get(ctx, key)
}

// Put implements [state.Store].
func (s *Store) Put(ctx context.Context, key string, v []byte, ifRev state.Rev) (state.Rev, error) {
	s.log.add("put", key)
	return s.Store.Put(ctx, key, v, ifRev)
}

// Delete implements [state.Store].
func (s *Store) Delete(ctx context.Context, key string) error {
	s.log.add("delete", key)
	return s.Store.Delete(ctx, key)
}

// Child implements [state.Store].
func (v *view) Child(prefix string, opts ...state.Option) state.Store {
	return &view{Store: v.Store.Child(prefix, opts...), prefix: v.prefix + prefix + "/", log: v.log}
}

// Get implements [state.Store].
func (v *view) Get(ctx context.Context, key string) (state.Item, error) {
	v.log.add("get", v.prefix+key)
	return v.Store.Get(ctx, key)
}

// GetRev implements [state.Store].
func (v *view) GetRev(ctx context.Context, key string, rev state.Rev) (state.Item, error) {
	v.log.add("get", v.prefix+key)
	return v.Store.GetRev(ctx, key, rev)
}

// Put implements [state.Store].
func (v *view) Put(ctx context.Context, key string, val []byte, ifRev state.Rev) (state.Rev, error) {
	v.log.add("put", v.prefix+key)
	return v.Store.Put(ctx, key, val, ifRev)
}

// Delete implements [state.Store].
func (v *view) Delete(ctx context.Context, key string) error {
	v.log.add("delete", v.prefix+key)
	return v.Store.Delete(ctx, key)
}

// List implements [state.Store].
func (v *view) List(ctx context.Context) ([]string, error) {
	v.log.add("list", v.prefix)
	return v.Store.List(ctx)
}
