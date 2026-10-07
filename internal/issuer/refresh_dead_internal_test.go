package issuer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"
)

// The negative cache's own rules: its bound, its fixed TTL, its key, and
// which states [Sessions.present] calls dead.

func TestTheDeadRefreshCacheIsBoundedAndEvictsTheLeastRecentlyRefused(t *testing.T) {
	t.Parallel()
	clock := newGCClock()
	cache := newDeadRefreshes(fingerprintKey([]byte("a-seed")), 3, deadRefreshTTL, clock.now)
	a, b, c, d := cache.sum("a"), cache.sum("b"), cache.sum("c"), cache.sum("d")

	for _, sum := range [][sha256.Size]byte{a, b, c} {
		if !cache.add(sum) {
			t.Fatal("a new entry was reported as already there")
		}
	}
	if cache.add(b) {
		t.Error("an entry added twice was reported as new twice; it would be logged twice")
	}

	// a is refused, which makes b the least recently refused.
	if !cache.refused(a) {
		t.Fatal("a held entry was not refused")
	}
	cache.add(d)
	if n := cache.len(); n != 3 {
		t.Fatalf("the cache holds %d entries, want its bound of 3", n)
	}
	if cache.refused(b) {
		t.Error("the least recently refused entry survived an add past the bound")
	}
	for name, sum := range map[string][sha256.Size]byte{"a": a, "c": c, "d": d} {
		if !cache.refused(sum) {
			t.Errorf("entry %s was evicted; only the least recently refused should go", name)
		}
	}
}

func TestTheDeadRefreshCacheTTLIsFixedNotSliding(t *testing.T) {
	t.Parallel()
	clock := newGCClock()
	cache := newDeadRefreshes(fingerprintKey([]byte("a-seed")), deadRefreshEntries, deadRefreshTTL, clock.now)
	sum := cache.sum("dead")
	cache.add(sum)

	// Refused all through the five minutes, and refusing it does not extend
	// them.
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
	if !cache.add(sum) {
		t.Error("an entry added again after it expired was not reported as new; it would not be logged")
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
	if bytes.Equal(key, (&SigningKey{seed: seed}).Derive(fingerprintLabel)) {
		t.Error("the fingerprint key is the sign-in state's HMAC derivation, not one of its own")
	}
	if bytes.Equal(fingerprintKey(nil), fingerprintKey(nil)) {
		t.Error("with no secret, two processes drew the same key")
	}

	cache := newDeadRefreshes(key, 1, time.Minute, time.Now)
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
