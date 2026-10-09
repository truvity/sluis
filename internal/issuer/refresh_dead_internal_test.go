package issuer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"testing"
	"time"
)

// The negative cache's own rules: its bound, its fixed TTL, its key, and
// which states [Sessions.present] calls dead.

// confirmedDead leaves the cache holding sum as confirmed dead: two verdicts a
// confirmation delay apart.
func confirmedDead(cache *deadRefreshes, clock *gcClock, sums ...[sha256.Size]byte) {
	for _, sum := range sums {
		cache.dead(sum)
	}
	clock.advance(deadRefreshConfirm)
	for _, sum := range sums {
		cache.dead(sum)
	}
}

func TestTheDeadRefreshCacheIsBoundedAndEvictsTheLeastRecentlySeen(t *testing.T) {
	t.Parallel()
	clock := newGCClock()
	cache := newDeadRefreshes(fingerprintKey([]byte("a-seed")), 3, deadRefreshConfirm, deadRefreshTTL, clock.now)
	a, b, c, d := cache.sum("a"), cache.sum("b"), cache.sum("c"), cache.sum("d")

	for _, sum := range [][sha256.Size]byte{a, b, c} {
		if !cache.dead(sum) {
			t.Fatal("a new entry was reported as already there")
		}
	}
	if cache.dead(b) {
		t.Error("a second verdict was reported as a new entry; it would be logged twice")
	}
	confirmedDead(cache, clock, a, b, c)

	// a is refused, which makes b the least recently seen.
	if !cache.refused(a) {
		t.Fatal("a confirmed entry was not refused")
	}
	cache.dead(d)
	if n := cache.len(); n != 3 {
		t.Fatalf("the cache holds %d entries, want its bound of 3", n)
	}
	if cache.refused(b) {
		t.Error("the least recently seen entry survived an add past the bound")
	}
	for name, sum := range map[string][sha256.Size]byte{"a": a, "c": c} {
		if !cache.refused(sum) {
			t.Errorf("entry %s was evicted; only the least recently seen should go", name)
		}
	}
}

// One dead verdict is never trusted alone: the entry is refused only after
// a second verdict at least the confirmation delay later, a verdict that is
// not dead undoes it at any point, and a pending entry nobody confirms is
// never refused, however long it waits.
func TestADeadVerdictIsRefusedOnlyOnceConfirmed(t *testing.T) {
	t.Parallel()
	clock := newGCClock()
	cache := newDeadRefreshes(fingerprintKey([]byte("a-seed")), deadRefreshEntries, deadRefreshConfirm, deadRefreshTTL, clock.now)
	sum := cache.sum("dead")

	cache.dead(sum)
	clock.advance(deadRefreshConfirm - time.Second)
	cache.dead(sum) // too soon to confirm
	if cache.refused(sum) {
		t.Fatal("two verdicts inside the confirmation delay confirmed the entry")
	}
	clock.advance(time.Hour)
	if cache.refused(sum) {
		t.Fatal("a pending entry nobody confirmed was refused once its delay had passed")
	}

	cache.dead(sum)
	clock.advance(deadRefreshConfirm)
	cache.alive(sum) // a write overtook the first verdict
	cache.dead(sum)
	if cache.refused(sum) {
		t.Fatal("a verdict after one that was not dead confirmed the entry it had undone")
	}
	clock.advance(deadRefreshConfirm)
	cache.dead(sum)
	if !cache.refused(sum) {
		t.Fatal("two dead verdicts the delay apart did not confirm the entry")
	}
	cache.alive(sum)
	if cache.refused(sum) {
		t.Error("a verdict that is not dead did not undo a confirmed entry")
	}
}

func TestTheDeadRefreshCacheTTLIsFixedNotSliding(t *testing.T) {
	t.Parallel()
	clock := newGCClock()
	cache := newDeadRefreshes(fingerprintKey([]byte("a-seed")), deadRefreshEntries, deadRefreshConfirm, deadRefreshTTL, clock.now)
	sum := cache.sum("dead")
	confirmedDead(cache, clock, sum)

	// Refused all through the five minutes from its confirmation, and
	// refusing it does not extend them.
	for range 4 {
		clock.advance(time.Minute)
		if !cache.refused(sum) {
			t.Fatal("an entry inside its TTL was not refused")
		}
	}
	clock.advance(time.Minute - time.Nanosecond)
	if !cache.refused(sum) {
		t.Fatal("an entry a nanosecond inside its TTL was not refused")
	}
	clock.advance(time.Nanosecond)
	if cache.refused(sum) {
		t.Fatal("an entry was refused at its TTL; hits must not slide it")
	}
	if n := cache.len(); n != 0 {
		t.Errorf("an expired entry met by a lookup is still held (%d entries)", n)
	}
	if !cache.dead(sum) {
		t.Error("a verdict after the entry expired was not reported as new; it would not be logged")
	}
}

func TestTheDeadRefreshWarningIsRateLimitedAcrossTokens(t *testing.T) {
	t.Parallel()
	clock := newGCClock()
	cache := newDeadRefreshes(fingerprintKey(nil), deadRefreshEntries, deadRefreshConfirm, deadRefreshTTL, clock.now)

	for i := range deadRefreshWarnings {
		if ok, held := cache.warn(); !ok || held != 0 {
			t.Fatalf("warning %d = %v, %d; want written, nothing held", i, ok, held)
		}
	}
	for range 5 {
		if ok, _ := cache.warn(); ok {
			t.Fatal("a warning past the limit was written")
		}
	}
	clock.advance(deadRefreshWarnWindow)
	if ok, held := cache.warn(); !ok || held != 5 {
		t.Errorf("the first warning of the next window = %v, %d; want written, saying 5 were held back", ok, held)
	}
}

func TestTheFingerprintKeyIsDerivedAndNeverTheSecret(t *testing.T) {
	t.Parallel()
	seed := []byte("the installation's state secret, 32+ bytes long")

	key := fingerprintKey(seed)
	if !bytes.Equal(key, fingerprintKey(seed)) {
		t.Error("the same secret derived two keys; replicas would fingerprint a token differently")
	}
	if bytes.Equal(key, seed) || bytes.Contains(seed, key) || len(key) != sha256.Size {
		t.Error("the fingerprint key is the secret, or part of it")
	}
	// Not the derivation the sign-in state uses, under any label.
	if bytes.Equal(key, hmacOf(seed, []byte(fingerprintLabel))) {
		t.Error("the fingerprint key is the sign-in state's HMAC derivation, not one of its own")
	}
	if bytes.Equal(fingerprintKey(nil), fingerprintKey(nil)) {
		t.Error("with no secret, two processes drew the same key")
	}

	cache := newDeadRefreshes(key, 1, time.Minute, time.Minute, time.Now)
	if got := fingerprint(cache.sum("token")); len(got) != 8 {
		t.Errorf("fingerprint = %q, want 8 hex", got)
	}
}

// Which states present calls dead, over each kind of State: only terminal
// ones, and only over a State whose later writes are conditional.
func TestPresentCallsDeadOnlyATerminalState(t *testing.T) {
	t.Parallel()

	type check struct {
		name string
		// arrange returns the token to present, after setting the state.
		arrange   func(t *testing.T, e *gcEnv) string
		ok, dead  bool
		reused    bool
		successor bool
	}
	checks := []check{
		{name: "never issued", arrange: func(*testing.T, *gcEnv) string { return "nobody" }, dead: true},
		{name: "pointer to an absent record", arrange: func(t *testing.T, e *gcEnv) string {
			s := e.open(t, "t0")
			if ended, err := e.sessions.RevokeID(context.Background(), s.ID); err != nil || !ended {
				t.Fatalf("RevokeID = %v, %v", ended, err)
			}
			return "t0"
		}, dead: true},
		{name: "pointer to a record past its end", arrange: func(t *testing.T, e *gcEnv) string {
			e.open(t, "t0")
			e.advance(2 * time.Hour) // past the 1h absolute limit, inside the 12h the record is kept
			return "t0"
		}},
		{name: "spent inside the grace window", arrange: func(t *testing.T, e *gcEnv) string {
			e.open(t, "t0")
			e.refresh(t, "t0", "t1")
			e.advance(10 * time.Second)
			return "t0"
		}, ok: true, successor: true},
		{name: "spent in the tolerance band", arrange: func(t *testing.T, e *gcEnv) string {
			e.open(t, "t0")
			e.refresh(t, "t0", "t1")
			e.advance(refreshGrace + time.Second)
			return "t0"
		}},
		{name: "spent past the band in a live session", arrange: func(t *testing.T, e *gcEnv) string {
			e.open(t, "t0")
			e.refresh(t, "t0", "t1")
			e.advance(time.Minute)
			return "t0"
		}, reused: true},
		{name: "spent past the band in an ended session", arrange: func(t *testing.T, e *gcEnv) string {
			s := e.open(t, "t0")
			e.refresh(t, "t0", "t1")
			if ended, err := e.sessions.RevokeID(context.Background(), s.ID); err != nil || !ended {
				t.Fatalf("RevokeID = %v, %v", ended, err)
			}
			e.advance(time.Minute)
			return "t0"
		}, dead: true},
	}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			gcEachKind(t, time.Hour, false, func(t *testing.T, e *gcEnv) {
				token := c.arrange(t, e)
				p, ok, err := e.sessions.present(context.Background(), token)
				if err != nil {
					t.Fatalf("present: %v", err)
				}
				// Over a State without revisions nothing is dead; the rest
				// of the answer is the same.
				wantDead := c.dead && e.versioned
				if ok != c.ok || p.dead != wantDead || (p.reused != "") != c.reused || (p.successor != "") != c.successor {
					t.Errorf("present = ok %v, dead %v, reused %v, successor %v; want ok %v, dead %v, reused %v, successor %v",
						ok, p.dead, p.reused != "", p.successor != "", c.ok, wantDead, c.reused, c.successor)
				}
			})
		})
	}
}

// hmacOf is what the signing key derives for a label: HMAC-SHA256 of the label under the seed.
func hmacOf(seed, label []byte) []byte {
	mac := hmac.New(sha256.New, seed)
	mac.Write(label)
	return mac.Sum(nil)
}
