package issuer

import (
	"container/list"
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// A refresh token whose chain has ended is often presented again and again:
// an agent host keeps its credential store and retries in a loop. Each
// presentation used to cost two to four State reads before it was refused,
// and nothing said which client was looping. This file is the issuer's
// answer (docs/decisions/0040-agent-class-sessions.md, decision 10): a WARN
// naming the client and a fingerprint of the token, once, and an in-process
// negative cache that refuses the repeats with no State read at all.
//
// What may be cached is narrow on purpose. Only a TERMINAL state, read
// consistently, is ever remembered ([presented.dead]):
//
//   - no token pointer and no legacy rotated record: the token was never
//     issued, or its pointer has expired or been deleted;
//   - a pointer (live, or a spent mark past the grace window and its
//     tolerance band) naming a session record that is absent.
//
// Neither can come back. A pointer is written before its token is handed
// out, and a session record is created only under a fresh id; every later
// write of a pointer or a record is conditional on the revision read, so a
// slow rotation on another replica that read the record before it was
// deleted loses ([Sessions.writeRotated]). That last part needs a State that
// keeps revisions: over one that does not, a rotation writes blind and can
// bring a deleted record back, so nothing is cached there.
//
// Never cached: a spent mark inside its grace window or the tolerance band
// past it, a spent mark whose session is live (a reuse that must reach
// [Storage.endReuse]), a record that is present with ExpiresAt in the past
// (a slow request on another replica can still rotate it), and any failed
// read.
//
// "Read consistently" is what the State's Get already is: a DynamoDB GetItem
// with ConsistentRead, a Valkey read from the primary. Any future
// read-replica or eventually consistent read option must leave out the reads
// [Sessions.present] makes, because they feed this cache.

const (
	// deadRefreshTTL is how long a dead refresh token is refused from memory.
	// Fixed from when it was first refused, never slid by a hit: a terminal
	// state never comes back, so the TTL bounds only memory and how often
	// the WARN repeats for a client that keeps looping.
	deadRefreshTTL = 5 * time.Minute

	// deadRefreshEntries bounds the cache. An entry is a 32-byte HMAC and
	// its deadline, so the bound is well under a megabyte. A flood of
	// distinct garbage tokens evicts the least recently refused, which then
	// costs its State reads again and nothing worse.
	deadRefreshEntries = 4096

	// fingerprintLabel separates the fingerprint key from every other
	// derivation of the installation's secret.
	fingerprintLabel = "sluis/issuer/refresh-token-fingerprint/v1"
)

// deadRefreshes is the negative cache: an LRU of the full HMAC of each dead
// refresh token, each with a fixed deadline.
//
// Keyed by the FULL HMAC, never by the 8-hex fingerprint the log shows:
// 32 bits collide, and a collision would refuse a valid token.
type deadRefreshes struct {
	key   []byte
	limit int
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	order   *list.List // front is the most recently refused
	entries map[[sha256.Size]byte]*list.Element
}

type deadRefresh struct {
	sum   [sha256.Size]byte
	until time.Time
}

func newDeadRefreshes(key []byte, limit int, ttl time.Duration, now func() time.Time) *deadRefreshes {
	return &deadRefreshes{
		key: key, limit: limit, ttl: ttl, now: now,
		order: list.New(), entries: map[[sha256.Size]byte]*list.Element{},
	}
}

// fingerprintKey is the HMAC key of the cache and of the logged fingerprint:
// derived with HKDF and a label of its own from seed, the secret the
// installation's sign-in state is derived from ([SigningKey.Derive]: the
// configured state secret for a KMS key, the private key's encoding for a
// file key), never the raw secret. With no seed it is drawn at random, for
// this process alone.
func fingerprintKey(seed []byte) []byte {
	if len(seed) > 0 {
		if key, err := hkdf.Key(sha256.New, seed, nil, fingerprintLabel, sha256.Size); err == nil {
			return key
		}
	}

	key := make([]byte, sha256.Size)
	_, _ = rand.Read(key) // never fails: crypto/rand panics rather than return an error

	return key
}

// sum is the HMAC-SHA-256 of a refresh token under the cache's key.
func (c *deadRefreshes) sum(token string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(token))

	var out [sha256.Size]byte
	copy(out[:], mac.Sum(nil))

	return out
}

// fingerprint is what the log shows of a token: the first 8 hex of its HMAC.
// Enough to tell one looping client's token from another's, and nothing
// that leads back to the token.
func fingerprint(sum [sha256.Size]byte) string { return hex.EncodeToString(sum[:4]) }

// refused reports whether the token with this HMAC was refused as dead
// within the TTL. An expired entry is dropped and reported as not there.
func (c *deadRefreshes) refused(sum [sha256.Size]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[sum]
	if !ok {
		return false
	}

	if !c.now().Before(element.Value.(*deadRefresh).until) {
		c.order.Remove(element)
		delete(c.entries, sum)

		return false
	}

	c.order.MoveToFront(element)

	return true
}

// add remembers a dead token, and reports whether this call made the entry:
// the one that logs it. An entry already there keeps its deadline.
func (c *deadRefreshes) add(sum [sha256.Size]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if element, ok := c.entries[sum]; ok {
		if now.Before(element.Value.(*deadRefresh).until) {
			return false
		}

		c.order.Remove(element)
		delete(c.entries, sum)
	}

	c.entries[sum] = c.order.PushFront(&deadRefresh{sum: sum, until: now.Add(c.ttl)})
	for c.order.Len() > c.limit {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*deadRefresh).sum)
	}

	return true
}

// len is how many entries are held, expired ones included until met.
func (c *deadRefreshes) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.order.Len()
}

// presentedClientKey carries the client id a token request named.
type presentedClientKey struct{}

// presentedClientFrom is the client id the token request named, "" when it
// named none or the request did not come through [presentedClients].
func presentedClientFrom(ctx context.Context) string {
	id, _ := ctx.Value(presentedClientKey{}).(string)
	return id
}

// withPresentedClient carries a client id on ctx.
func withPresentedClient(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, presentedClientKey{}, id)
}

// presentedClients carries the client id a token request names down to the
// storage, which the library calls with the refresh token alone. It is what
// a refused refresh logs, so that a looping host can be named.
//
// The id is UNAUTHENTICATED: it is what the request says, before or without
// the client authenticating, so it is logged through logsafe and never
// acted on. It never parses the body itself: [resourceIndicators], in front
// of it, already has, and a body it could not parse is left to the library
// to refuse, exactly as before.
func presentedClients(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenPaths[r.URL.Path] || r.PostForm == nil {
			next.ServeHTTP(w, r)
			return
		}

		id := r.PostForm.Get("client_id")
		if id == "" {
			// RFC 6749 2.3.1: the id in HTTP Basic is form-encoded.
			if user, _, ok := r.BasicAuth(); ok {
				if unescaped, err := url.QueryUnescape(user); err == nil {
					id = unescaped
				}
			}
		}

		next.ServeHTTP(w, r.WithContext(withPresentedClient(r.Context(), id)))
	})
}
