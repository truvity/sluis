// Package secretrec is a state.Store that records the full address of every
// read and write, for the tests that pin which path a caller uses on a secrets
// layout. It keeps the values in memory and never records one.
package secretrec

import (
	"context"
	"errors"
	"fmt"
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
	// batches are the sizes of the DeleteMany calls; fail is how many of the
	// next calls fail with failWith before one goes through.
	batches  []int
	fail     int
	failWith error
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

// Walk implements [state.Walker].
func (s *Store) Walk(ctx context.Context) ([]string, error) {
	s.log.add("list", "")
	return state.ListAll(ctx, s.Store)
}

// Walk implements [state.Walker].
func (v *view) Walk(ctx context.Context) ([]string, error) {
	v.log.add("list", v.prefix)
	return state.ListAll(ctx, v.Store)
}

// DeleteMany implements [state.BatchDeleter]: each key is recorded as a delete,
// and the size of the call is kept for [Store.Batches].
func (s *Store) DeleteMany(ctx context.Context, keys []string) (int, error) {
	return s.log.deleteMany(ctx, "", s.Store, keys)
}

// DeleteMany implements [state.BatchDeleter].
func (v *view) DeleteMany(ctx context.Context, keys []string) (int, error) {
	return v.log.deleteMany(ctx, v.prefix, v.Store, keys)
}

func (l *log) deleteMany(ctx context.Context, prefix string, inner state.Store, keys []string) (int, error) {
	l.mu.Lock()
	if l.fail > 0 {
		l.fail--
		err := l.failWith
		l.mu.Unlock()
		return 0, err
	}
	l.batches = append(l.batches, len(keys))
	l.mu.Unlock()
	if len(keys) > state.MaxDeleteBatch {
		return 0, fmt.Errorf("secretrec: %d keys in one batch", len(keys))
	}
	n := 0
	for _, k := range keys {
		l.add("delete", prefix+k)
		switch err := inner.Delete(ctx, k); {
		case err == nil:
			n++
		case errors.Is(err, state.ErrNotFound):
		default:
			return n, err
		}
	}
	return n, nil
}

// Batches are the sizes of the DeleteMany calls that went through, in order.
func (s *Store) Batches() []int {
	s.log.mu.Lock()
	defer s.log.mu.Unlock()
	return slices.Clone(s.log.batches)
}

// FailBatches makes the next n DeleteMany calls fail with err.
func (s *Store) FailBatches(n int, err error) {
	s.log.mu.Lock()
	s.log.fail, s.log.failWith = n, err
	s.log.mu.Unlock()
}
