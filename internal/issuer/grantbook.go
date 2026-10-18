package issuer

import (
	"sync"
	"time"

	"github.com/zitadel/oidc/v3/pkg/op"
)

// grantTTL is how long an exchange decision waits for the token to be made.
// The library calls back about the same request within one HTTP handler, so
// the work is milliseconds; the rest is room for a slow signing call. A
// decision that outlives it is gone and the exchange fails closed, with "was
// not validated", rather than be answered from a stale entry.
const grantTTL = 2 * time.Minute

// grantBook carries an exchange decision between the calls the library makes
// about one request ([Storage.ValidateTokenExchangeRequest] decides; the claims
// and the token are read after). It forgets by time, because the library has
// no hook for "the response is made" and the last reader of a decision is not
// knowable here (the access token's claims are read after the token is
// created), and because a request that is refused after it was decided has
// no reader at all. A map that only ever grew was a slow memory exhaustion,
// one entry for every exchange the process had ever answered.
//
// The zero value is ready.
type grantBook struct {
	mu      sync.Mutex
	entries map[op.TokenExchangeRequest]grantEntry
	swept   time.Time
	// now is the clock; nil is time.Now.
	now func() time.Time
}

type grantEntry struct {
	grant   Grant
	expires time.Time
}

func (b *grantBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}

	return time.Now()
}

// Store keeps grant for request until [grantTTL] has passed, and drops what
// has already expired: at most once in a quarter of the TTL, so a burst of
// exchanges pays for one pass and not one each.
func (b *grantBook) Store(request op.TokenExchangeRequest, grant Grant) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.clock()
	if b.entries == nil {
		b.entries = make(map[op.TokenExchangeRequest]grantEntry)
	}

	if now.Sub(b.swept) >= grantTTL/4 {
		for key, entry := range b.entries {
			if !now.Before(entry.expires) {
				delete(b.entries, key)
			}
		}

		b.swept = now
	}

	b.entries[request] = grantEntry{grant: grant, expires: now.Add(grantTTL)}
}

// Load is the decision made for request, and false for one never made or
// past its time.
func (b *grantBook) Load(request op.TokenExchangeRequest) (Grant, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry, ok := b.entries[request]
	if !ok {
		return Grant{}, false
	}

	if !b.clock().Before(entry.expires) {
		delete(b.entries, request)

		return Grant{}, false
	}

	return entry.grant, true
}

// Len is how many decisions are held, expired ones not yet dropped included.
func (b *grantBook) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return len(b.entries)
}
