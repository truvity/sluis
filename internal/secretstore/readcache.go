package secretstore

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/truvity/sluis/storage/state"
)

// DefaultReadCacheTTL is how long a decrypted document is kept by one process
// when `secrets.readCache` and SLUIS_SECRETS_READ_CACHE_TTL are not set. It
// bounds how long an instance serves a document another writer has replaced.
const DefaultReadCacheTTL = 5 * time.Minute

// ReadCacheEnv is the environment variable that sets the read cache's TTL (a
// Go duration; "0" turns the cache off). It wins over the installation config.
const ReadCacheEnv = "SLUIS_SECRETS_READ_CACHE_TTL"

// readCacheMax bounds the documents one process keeps.
const readCacheMax = 512

// expiryFields are the top-level fields of a document that say when a
// credential in it stops being valid. A copy is never kept past the earliest.
var expiryFields = []string{"expires_on", "access_expires", "refresh_expires"}

var readMeter = otel.Meter("github.com/truvity/sluis/secretstore")

// sluis.secret.reads is exported as sluis_secret_reads_total{result}: a read
// answered from the process (hit) or sent to the backend (miss).
var secretReads, _ = readMeter.Int64Counter("sluis.secret.reads",
	metric.WithDescription("Reads of a stored secret document, by result: hit (answered from this process) or miss (read from the backend)."))

// ReadCache keeps decrypted documents in memory, per process, for a short
// time. It is never written anywhere and holds at most readCacheMax documents.
//
// The rules that keep it correct:
//   - a Put, a Delete or a DeleteMany through the cache drops the key's copies,
//     whether it succeeded or not (a lost conditional write means the copy is
//     stale), so a writer reads its own write;
//   - a read that began before a write finished never stores its result;
//   - a copy is not kept past the expiry the document itself carries;
//   - an absent key and an error are not cached;
//   - [state.WithFreshRead] and [state.Invalidate] force a read from the backend.
type ReadCache struct {
	ttl time.Duration
	now func() time.Time

	mu    sync.Mutex
	epoch uint64 // advanced by every write and invalidation
	docs  map[string]*cachedDoc
}

type cachedDoc struct {
	cur  *cachedItem
	revs map[state.Rev]*cachedItem
}

type cachedItem struct {
	item    state.Item
	expires time.Time
}

// NewReadCache returns a cache that keeps a document for ttl. A ttl of zero or
// less is no cache: [ReadCache.Wrap] returns the store as it is.
func NewReadCache(ttl time.Duration) *ReadCache {
	return &ReadCache{ttl: ttl, now: time.Now, docs: map[string]*cachedDoc{}}
}

// WithClock sets the clock, for tests.
func (c *ReadCache) WithClock(now func() time.Time) *ReadCache { c.now = now; return c }

// Wrap returns s read through the cache. id tells stores apart in the cache
// (the root they were opened at); children share the cache.
func (c *ReadCache) Wrap(s state.Store, id string) state.Store {
	if c == nil || c.ttl <= 0 {
		return s
	}
	return &cachedStore{inner: s, c: c, path: strings.TrimSuffix(id, "/")}
}

// CachedOpener is open with every store it opens read through a new cache of
// ttl. A ttl of zero or less leaves open as it is.
func CachedOpener(open Opener, ttl time.Duration) Opener {
	if ttl <= 0 {
		return open
	}
	return func(ctx context.Context, prefix string, opts ...state.Option) (state.Store, error) {
		s, err := open(ctx, prefix, opts...)
		if err != nil {
			return nil, err
		}
		return NewReadCache(ttl).Wrap(s, prefix), nil
	}
}

func (c *ReadCache) lookup(name string, rev state.Rev) (state.Item, bool, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.docs[name]; d != nil {
		it := d.cur
		if rev != "" {
			it = d.revs[rev]
		}
		if it != nil && c.now().Before(it.expires) {
			return cloneItem(it.item), true, c.epoch
		}
	}
	return state.Item{}, false, c.epoch
}

// store keeps it unless a write or invalidation happened since epoch.
func (c *ReadCache) store(name string, rev state.Rev, it state.Item, epoch uint64) {
	now := c.now()
	expires := now.Add(c.ttl)
	if e, ok := documentExpiry(it.Value); ok && e.Before(expires) {
		expires = e
	}
	if !expires.After(now) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != epoch {
		return
	}
	d := c.docs[name]
	if d == nil {
		if len(c.docs) >= readCacheMax {
			c.makeRoomLocked(now)
		}
		d = &cachedDoc{}
		c.docs[name] = d
	}
	ci := &cachedItem{item: cloneItem(it), expires: expires}
	if rev == "" {
		d.cur = ci
		return
	}
	if d.revs == nil {
		d.revs = map[state.Rev]*cachedItem{}
	}
	if len(d.revs) >= 4 {
		clear(d.revs)
	}
	d.revs[rev] = ci
}

func (c *ReadCache) makeRoomLocked(now time.Time) {
	for n, d := range c.docs {
		if d.live(now) {
			continue
		}
		delete(c.docs, n)
	}
	for n := range c.docs {
		if len(c.docs) < readCacheMax {
			return
		}
		delete(c.docs, n)
	}
}

func (d *cachedDoc) live(now time.Time) bool {
	if d.cur != nil && now.Before(d.cur.expires) {
		return true
	}
	for _, r := range d.revs {
		if now.Before(r.expires) {
			return true
		}
	}
	return false
}

func (c *ReadCache) evict(names ...string) {
	c.mu.Lock()
	c.epoch++
	for _, n := range names {
		delete(c.docs, n)
	}
	c.mu.Unlock()
}

// documentExpiry is the earliest expiry a JSON document states at its top
// level. A document that states none, or is not an object, has none.
func documentExpiry(value []byte) (time.Time, bool) {
	if !bytes.Contains(value, []byte("expires")) {
		return time.Time{}, false
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(value, &doc) != nil {
		return time.Time{}, false
	}
	var earliest time.Time
	for _, f := range expiryFields {
		var s string
		if json.Unmarshal(doc[f], &s) != nil || s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil || t.IsZero() || t.Year() < 2000 {
			continue
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest, !earliest.IsZero()
}

func cloneItem(it state.Item) state.Item {
	it.Value = bytes.Clone(it.Value)
	return it
}

// cachedStore is a [state.Store] that reads through a [ReadCache]. It is the
// one place every module's documents are read from, because every module's
// stores are Children of the store [Open] and [OpenV5] build.
type cachedStore struct {
	inner state.Store
	c     *ReadCache
	path  string // identifies the store: root plus the Child prefixes
}

func (s *cachedStore) name(key string) string { return s.path + "\x00" + key }

func (s *cachedStore) Get(ctx context.Context, key string) (state.Item, error) {
	return s.read(ctx, key, "", func() (state.Item, error) { return s.inner.Get(ctx, key) })
}

func (s *cachedStore) GetRev(ctx context.Context, key string, rev state.Rev) (state.Item, error) {
	return s.read(ctx, key, rev, func() (state.Item, error) { return s.inner.GetRev(ctx, key, rev) })
}

func (s *cachedStore) read(ctx context.Context, key string, rev state.Rev, fetch func() (state.Item, error)) (state.Item, error) {
	name := s.name(key)
	var epoch uint64
	if !state.FreshRead(ctx) {
		var ok bool
		var it state.Item
		if it, ok, epoch = s.c.lookup(name, rev); ok {
			secretReads.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "hit")))
			return it, nil
		}
	} else {
		s.c.mu.Lock()
		epoch = s.c.epoch
		s.c.mu.Unlock()
	}
	secretReads.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "miss")))
	it, err := fetch()
	if err != nil {
		return it, err
	}
	s.c.store(name, rev, it, epoch)
	return cloneItem(it), nil
}

func (s *cachedStore) Put(ctx context.Context, key string, value []byte, ifRev state.Rev) (state.Rev, error) {
	rev, err := s.inner.Put(ctx, key, value, ifRev)
	// Evicted after the write, whatever it answered: a conflict means the copy
	// was behind; a transport error leaves it unknown whether the write landed.
	s.c.evict(s.name(key))
	return rev, err
}

func (s *cachedStore) Delete(ctx context.Context, key string) error {
	err := s.inner.Delete(ctx, key)
	s.c.evict(s.name(key))
	return err
}

func (s *cachedStore) List(ctx context.Context) ([]string, error) { return s.inner.List(ctx) }

func (s *cachedStore) Child(prefix string, opts ...state.Option) state.Store {
	return &cachedStore{inner: s.inner.Child(prefix, opts...), c: s.c, path: s.path + "/" + strings.Trim(prefix, "/")}
}

// Walk implements [state.Walker] when the inner store does.
func (s *cachedStore) Walk(ctx context.Context) ([]string, error) {
	if w, ok := s.inner.(state.Walker); ok {
		return w.Walk(ctx)
	}
	return nil, state.ErrNoWalk
}

// DeleteMany implements [state.BatchDeleter] when the inner store does; a
// store without it is deleted from one key at a time.
func (s *cachedStore) DeleteMany(ctx context.Context, keys []string) (int, error) {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = s.name(k)
	}
	defer s.c.evict(names...)
	if bd, ok := s.inner.(state.BatchDeleter); ok {
		return bd.DeleteMany(ctx, keys)
	}
	n := 0
	for _, k := range keys {
		switch err := s.inner.Delete(ctx, k); err {
		case nil:
			n++
		case state.ErrNotFound:
		default:
			return n, err
		}
	}
	return n, nil
}

// Invalidate implements [state.Invalidator]: the next read of key goes to the
// backend.
func (s *cachedStore) Invalidate(key string) { s.c.evict(s.name(key)) }

var (
	_ state.Walker       = (*cachedStore)(nil)
	_ state.BatchDeleter = (*cachedStore)(nil)
	_ state.Invalidator  = (*cachedStore)(nil)
)
