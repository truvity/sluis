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
	"strings"
	"sync"
	"time"
)

// A refresh token whose chain has ended is often presented again and again:
// an agent host keeps its credential store and retries in a loop. Each
// presentation used to cost two to four State reads before it was refused,
// and nothing said which client was looping. This file is the issuer's
// answer (docs/decisions/0040-agent-class-sessions.md, decision 10): a WARN
// naming the client and a fingerprint of the token, once per entry, and an
// in-process negative cache that refuses the repeats with no State read.
//
// What may be cached is narrow on purpose. Only a TERMINAL state, read
// consistently, is ever a candidate ([presented.dead]):
//
//   - no token pointer and no legacy rotated record: the token was never
//     issued, or its pointer has expired or been deleted;
//   - a pointer (live, or a spent mark past the grace window and its
//     tolerance band) naming a session record that is absent.
//
// A deletion never comes back: a pointer is written before its token is
// handed out, a session record is created only under a fresh id, and every
// later write of a pointer or a record is conditional on the revision read
// ([Sessions.writeRotated]). That needs a State that keeps revisions; over
// one that does not, a rotation writes blind and can bring a deleted record
// back, so nothing is a candidate there.
//
// An EXPIRY is not as final as it looks. A store that decides expiry by its
// replica's clock, to the second (the DynamoDB adapter compares `expires`
// with a `:now` the client builds, and the SDK resends the same condition on
// a retry), can let a conditional write built just before the second land
// just after another replica read the key as absent. A rolling upgrade from
// a version whose rotation deleted the pointer before writing the legacy
// rotated key shows a gap too. So one dead verdict proves nothing on its
// own: it is CONFIRMED by a second, read at least [deadRefreshConfirm]
// later, and only a confirmed entry is refused from memory. Until then every
// presentation reads the State as before, and any verdict that is not dead
// drops the entry.
//
// Never a candidate: a spent mark inside its grace window or the tolerance
// band past it, a spent mark whose session is live (a reuse that must reach
// [Storage.endReuse]), a record that is present with ExpiresAt in the past
// (a slow request on another replica can still rotate it), and any failed
// read.
//
// "Read consistently" is what the State's Get already is: a DynamoDB GetItem
// with ConsistentRead, a Valkey read from the primary. Any future
// read-replica or eventually consistent read option must leave out the reads
// [Sessions.present] makes, because they feed this cache.

const (
	// deadRefreshConfirm is how long after a token's first dead verdict a
	// second one must be read before the token is refused from memory.
	//
	// 60 s covers, with room, the longest a write can still land after a
	// replica read the key it writes as absent: the grace window (30 s) and
	// the clock tolerance past it (2 s) during which a spent token is still
	// answered or still only refused, plus the DynamoDB adapter's bound on
	// one call and its SDK retries (15 s, opTimeout, for a request with no
	// deadline of its own), plus the second to which expiry is decided.
	// 30 + 2 + 15 + 1 = 48 s.
	deadRefreshConfirm = time.Minute

	// deadRefreshTTL is how long a confirmed dead refresh token is refused
	// from memory. Fixed from its confirmation, never slid by a hit: the
	// TTL bounds memory and how often the WARN repeats for a client that
	// keeps looping.
	deadRefreshTTL = 5 * time.Minute

	// deadRefreshEntries bounds the cache. An entry is a 32-byte HMAC and
	// two times, so the bound is well under a megabyte. A flood of distinct
	// garbage tokens evicts the least recently seen, which then costs its
	// State reads again and nothing worse.
	deadRefreshEntries = 4096

	// fingerprintLabel separates the fingerprint key from every other
	// derivation of the installation's secret.
	fingerprintLabel = "sluis/issuer/refresh-token-fingerprint/v1"

	// deadRefreshWarnings bounds the WARN lines for dead tokens, across
	// every token, per [deadRefreshWarnWindow]: a flood of distinct garbage
	// tokens must not become a flood of log lines.
	deadRefreshWarnings   = 30
	deadRefreshWarnWindow = time.Minute
)

// deadRefreshes is the negative cache: an LRU of the full HMAC of each
// refresh token read as dead, pending until a second dead verdict confirms
// it ([deadRefreshConfirm]), then refused until a fixed deadline.
//
// Keyed by the FULL HMAC, never by the 8-hex fingerprint the log shows:
// 32 bits collide, and a collision would refuse a valid token.
type deadRefreshes struct {
	key     []byte
	limit   int
	confirm time.Duration
	ttl     time.Duration
	now     func() time.Time

	mu      sync.Mutex
	order   *list.List // front is the most recently seen
	entries map[[sha256.Size]byte]*list.Element

	// The WARN limiter: lines written in the current window, when it
	// started, and how many were held back since the last line written.
	warned     int
	warnSince  time.Time
	suppressed int
}

type deadRefresh struct {
	sum [sha256.Size]byte
	// notBefore is when a dead verdict confirms the entry: its first
	// verdict plus the confirmation delay.
	notBefore time.Time
	// until is when a confirmed entry stops being refused; zero while it
	// is pending.
	until time.Time
}

func (e *deadRefresh) confirmed() bool { return !e.until.IsZero() }

func newDeadRefreshes(key []byte, limit int, confirm, ttl time.Duration, now func() time.Time) *deadRefreshes {
	return &deadRefreshes{
		key: key, limit: limit, confirm: confirm, ttl: ttl, now: now,
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

// refused reports whether the token with this HMAC is CONFIRMED dead and
// inside its TTL, so that it may be refused with no State read. A pending
// entry is not refused. An expired entry, or a pending one that was never
// confirmed in time, is dropped and reported as not there.
func (c *deadRefreshes) refused(sum [sha256.Size]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[sum]
	if !ok {
		return false
	}

	entry := element.Value.(*deadRefresh) //nolint:forcetypeassert // only *deadRefresh is ever stored
	now := c.now()
	switch {
	case entry.confirmed() && now.Before(entry.until):
		c.order.MoveToFront(element)
		return true
	case entry.confirmed(), !now.Before(entry.notBefore.Add(c.ttl)):
		c.drop(element)
	}

	return false
}

// dead records a dead verdict for the token with this HMAC. The first one
// makes a pending entry and reports true: the call that logs it. A later one
// at or after the entry's notBefore confirms it, from now for the TTL.
func (c *deadRefreshes) dead(sum [sha256.Size]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if element, ok := c.entries[sum]; ok {
		entry := element.Value.(*deadRefresh) //nolint:forcetypeassert // only *deadRefresh is ever stored
		if !entry.confirmed() && !now.Before(entry.notBefore) {
			entry.until = now.Add(c.ttl)
		}
		c.order.MoveToFront(element)

		return false
	}

	c.entries[sum] = c.order.PushFront(&deadRefresh{sum: sum, notBefore: now.Add(c.confirm)})
	for c.order.Len() > c.limit {
		c.drop(c.order.Back())
	}

	return true
}

// alive drops whatever is held for the token with this HMAC: a verdict that
// is not dead, or a read that failed, undoes a pending one.
func (c *deadRefreshes) alive(sum [sha256.Size]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.entries[sum]; ok {
		c.drop(element)
	}
}

func (c *deadRefreshes) drop(element *list.Element) {
	c.order.Remove(element)
	delete(c.entries, element.Value.(*deadRefresh).sum) //nolint:forcetypeassert // only *deadRefresh is ever stored
}

// warn reports whether a WARN for a dead token may be written now, and how
// many were held back before it. At most [deadRefreshWarnings] per
// [deadRefreshWarnWindow], across every token.
func (c *deadRefreshes) warn() (bool, int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if now.Sub(c.warnSince) >= deadRefreshWarnWindow {
		c.warnSince, c.warned = now, 0
	}

	if c.warned >= deadRefreshWarnings {
		c.suppressed++
		return false, 0
	}

	c.warned++
	held := c.suppressed
	c.suppressed = 0

	return true, held
}

// len is how many entries are held, pending, confirmed and expired ones
// alike until met.
func (c *deadRefreshes) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.order.Len()
}

// presentedKey carries what a token request named.
type presentedKey struct{}

// presentedRequest is the client id and the scopes a token request named,
// as it named them.
type presentedRequest struct {
	clientID string
	scopes   []string
}

// presentedFrom is what the token request named, empty when it named
// nothing or did not come through [presentedClients].
func presentedFrom(ctx context.Context) presentedRequest {
	asked, _ := ctx.Value(presentedKey{}).(presentedRequest)
	return asked
}

// withPresented carries what a token request named on ctx.
func withPresented(ctx context.Context, asked presentedRequest) context.Context {
	return context.WithValue(ctx, presentedKey{}, asked)
}

// presentedClients carries the client id and the scopes a token request
// names down to the storage, which the library calls with the refresh token
// alone: the id is what a refused refresh logs, so that a looping host can
// be named, and both are what a refresh whose session could not be read
// answers the library's checks with ([refusedLater]).
//
// The id is UNAUTHENTICATED: it is what the request says, before or without
// the client authenticating, so it is logged through logattr and never
// acted on. A client authenticating with private_key_jwt names itself in
// its assertion and not in the form, so its id is empty here. This never
// parses the body itself: [resourceIndicators], in front of it, already
// has, and a body it could not parse is left to the library to refuse,
// exactly as before.
func presentedClients(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenPaths[r.URL.Path] || r.PostForm == nil {
			next.ServeHTTP(w, r)
			return
		}

		asked := presentedRequest{
			clientID: r.PostForm.Get("client_id"),
			scopes:   strings.Fields(r.PostForm.Get("scope")),
		}
		if asked.clientID == "" {
			// RFC 6749 2.3.1: the id in HTTP Basic is form-encoded.
			if user, _, ok := r.BasicAuth(); ok {
				if unescaped, err := url.QueryUnescape(user); err == nil {
					asked.clientID = unescaped
				}
			}
		}

		next.ServeHTTP(w, r.WithContext(withPresented(r.Context(), asked)))
	})
}
