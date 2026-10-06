package grantcost

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/issuer"
)

// Budget is what one grant may cost at most. Writes and Reads hold the port
// calls and, where the [Env] can see them, the engine's own requests too;
// Resolutions is exact, because asking the hub twice about one person in one
// grant is both twice the cost and two answers that can disagree.
type Budget struct {
	Grant       string
	MaxWrites   int
	MaxReads    int
	Resolutions int
	// drive performs the grant once, untimed setup first, and returns the
	// measured step's cost.
	drive func(t *testing.T, h *Harness) Counts
}

// Budgets are the grants and what each may cost.
//
// refresh_token is the target of the cost work: it is the request an MCP
// client sends every few minutes for as long as it runs. authorization_code
// is held at what it cost when it was first measured (2026-10-07), so that
// it cannot grow while the refresh is being cut; tighten it, never loosen
// it.
//
// Measured 2026-10-07, before any of the cost work, the same on the memory
// adapter, the DynamoDB fake and LocalStack: authorization_code 13 writes, 5
// reads, 3 resolutions; refresh_token 14 writes, 8 reads, 4 resolutions,
// first refresh and steady state alike. After it, the same on all three:
// authorization_code 9 writes, 3 reads, 1 resolution; refresh_token 4
// writes, 3 reads, 1 resolution.
var Budgets = []Budget{
	{
		Grant: "authorization_code", MaxWrites: 9, MaxReads: 3, Resolutions: 1,
		drive: func(t *testing.T, h *Harness) Counts {
			code := h.SignIn()
			if code == "" {
				t.Fatal("the sign-in ended in no authorization code")
			}
			var tokens Tokens
			counts := h.Measure(func() { tokens = h.Redeem(code) })
			if tokens.Status != http.StatusOK || tokens.Refresh == "" {
				t.Fatalf("redeem: %d %q, want 200 and a refresh token", tokens.Status, tokens.Error)
			}
			return counts
		},
	},
	{
		Grant: "refresh_token", MaxWrites: 4, MaxReads: 3, Resolutions: 1,
		drive: func(t *testing.T, h *Harness) Counts {
			first := h.Grant()
			var next Tokens
			counts := h.Measure(func() { next = h.Refresh(first.Refresh) })
			if next.Status != http.StatusOK || next.Refresh == "" || next.Refresh == first.Refresh {
				t.Fatalf("refresh: %d %q, want 200 and a rotated token", next.Status, next.Error)
			}
			return counts
		},
	},
	{
		// The second refresh of a chain, which is every refresh after the
		// first for a client that keeps running: nothing about it may cost
		// more than the first.
		Grant: "refresh_token (steady)", MaxWrites: 4, MaxReads: 3, Resolutions: 1,
		drive: func(t *testing.T, h *Harness) Counts {
			first := h.Grant()
			second := h.Refresh(first.Refresh)
			if second.Status != http.StatusOK {
				t.Fatalf("first refresh: %d %q", second.Status, second.Error)
			}
			var third Tokens
			counts := h.Measure(func() { third = h.Refresh(second.Refresh) })
			if third.Status != http.StatusOK || third.Refresh == "" {
				t.Fatalf("second refresh: %d %q, want 200 and a rotated token", third.Status, third.Error)
			}
			return counts
		},
	},
}

// Run measures every [Budget] and runs the guards over env, each on an
// adapter of its own from factory.
func Run(t *testing.T, factory func(t *testing.T) Env) {
	t.Helper()
	for _, budget := range Budgets {
		t.Run("budget/"+budget.Grant, func(t *testing.T) {
			h := New(t, factory(t))
			counts := budget.drive(t, h)
			t.Logf("%s: %s", budget.Grant, counts.Summary())
			t.Logf("%s calls:\n%s", budget.Grant, counts.Breakdown())

			if counts.Resolutions != budget.Resolutions {
				t.Errorf("%s asked the directory %d times, want exactly %d", budget.Grant, counts.Resolutions, budget.Resolutions)
			}
			if counts.Writes > budget.MaxWrites {
				t.Errorf("%s made %d State/Index writes, budget %d", budget.Grant, counts.Writes, budget.MaxWrites)
			}
			if counts.Reads > budget.MaxReads {
				t.Errorf("%s made %d State/Index reads, budget %d", budget.Grant, counts.Reads, budget.MaxReads)
			}
			if counts.Engine != nil {
				if w := counts.EngineWrites(); w > budget.MaxWrites {
					t.Errorf("%s sent the engine %d write requests, budget %d", budget.Grant, w, budget.MaxWrites)
				}
				if r := counts.EngineReads(); r > budget.MaxReads {
					t.Errorf("%s sent the engine %d read requests, budget %d", budget.Grant, r, budget.MaxReads)
				}
			}
			if counts.SnapshotReads > counts.Resolutions {
				t.Errorf("%s read the snapshot %d times for %d resolutions", budget.Grant, counts.SnapshotReads, counts.Resolutions)
			}
		})
	}
	for _, guard := range guards {
		t.Run("guard/"+guard.name, func(t *testing.T) {
			guard.run(t, New(t, factory(t)))
		})
	}
}

// guards are the behaviour a cheaper grant must keep. Each passes before the
// cost work and must pass after it.
var guards = []struct {
	name string
	run  func(t *testing.T, h *Harness)
}{
	{"replay-in-grace-gets-the-same-successor", replayInGrace},
	{"reuse-after-grace-is-refused", reuseAfterGrace},
	{"revocation-ends-the-chain", revocation},
	{"operator-revoke-ends-the-chain", operatorRevoke},
	{"stale-snapshot-is-not-authoritative", staleSnapshot},
	{"index-membership", indexMembership},
}

// A spent refresh token presented again within the grace window is answered
// with the successor the first presentation got, not a second credential.
func replayInGrace(t *testing.T, h *Harness) {
	first := h.Grant()
	rotated := h.Refresh(first.Refresh)
	if rotated.Status != http.StatusOK || rotated.Refresh == first.Refresh {
		t.Fatalf("refresh: %d %q, want 200 and a rotated token", rotated.Status, rotated.Error)
	}
	replay := h.Refresh(first.Refresh)
	if replay.Status != http.StatusOK || replay.Refresh != rotated.Refresh {
		t.Fatalf("replay in the grace window: %d, token same as the successor=%v; want 200 and the same successor",
			replay.Status, replay.Refresh == rotated.Refresh)
	}
}

// Past the grace window a spent refresh token is refused: rotation is how a
// stolen token is detected. Today's issuer refuses the spent token and does
// NOT end the chain -- the successor keeps working -- so that is what is
// pinned: ending the chain on reuse would be a behaviour change of its own,
// to be made on purpose and not as a side effect of making a grant cheaper.
func reuseAfterGrace(t *testing.T, h *Harness) {
	first := h.Grant()
	rotated := h.Refresh(first.Refresh)
	if rotated.Status != http.StatusOK {
		t.Fatalf("refresh: %d %q", rotated.Status, rotated.Error)
	}
	h.Advance(time.Minute) // past the 30-second grace window
	reused := h.Refresh(first.Refresh)
	if reused.Status == http.StatusOK {
		t.Fatal("a spent refresh token was accepted after the grace window")
	}
	if reused.Error != "invalid_grant" {
		t.Errorf("reuse answered %q, want invalid_grant", reused.Error)
	}
	if next := h.Refresh(rotated.Refresh); next.Status != http.StatusOK {
		t.Errorf("the successor stopped working after a reuse of its predecessor: %d %q "+
			"(the chain was ended; that is a behaviour change)", next.Status, next.Error)
	}
}

// RFC 7009 revocation of the live refresh token ends the session: the token
// stops refreshing and the session is gone from every listing.
func revocation(t *testing.T, h *Harness) {
	first := h.Grant()
	rotated := h.Refresh(first.Refresh)
	if rotated.Status != http.StatusOK {
		t.Fatalf("refresh: %d %q", rotated.Status, rotated.Error)
	}
	if status := h.Revoke(rotated.Refresh); status != http.StatusOK {
		t.Fatalf("revoke: %d", status)
	}
	if after := h.Refresh(rotated.Refresh); after.Status == http.StatusOK {
		t.Error("a revoked refresh token still refreshes")
	}
	assertSessions(t, h, 0)
}

// The operator's revoke of a person ends every chain they hold.
func operatorRevoke(t *testing.T, h *Harness) {
	first := h.Grant()
	n, err := h.Issuer.Revoke(context.Background(), Person)
	if err != nil || n != 1 {
		t.Fatalf("Revoke(person) = %d, %v; want 1 session ended", n, err)
	}
	if after := h.Refresh(first.Refresh); after.Status == http.StatusOK {
		t.Error("a refresh token of a revoked person still refreshes")
	}
	assertSessions(t, h, 0)
}

// A snapshot older than the hub's freshness window is not authoritative:
// the issuer holds the last-known groups for its hold window and refuses
// once that has passed, rather than acting on a stale directory.
func staleSnapshot(t *testing.T, h *Harness) {
	first := h.Grant()
	h.Snapshot(time.Now().Add(-hub.DefaultFreshnessWindow - time.Minute))

	standing, err := h.Directory.ResolveUser(context.Background(), Person)
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if standing.Authoritative {
		t.Fatal("a snapshot past the freshness window was answered as authoritative")
	}

	held := h.Refresh(first.Refresh)
	if held.Status != http.StatusOK {
		t.Fatalf("refresh inside the hold window: %d %q, want the last-known groups held", held.Status, held.Error)
	}
	h.Advance(issuer.DefaultHoldWindow + time.Minute)
	if refused := h.Refresh(held.Refresh); refused.Status == http.StatusOK {
		t.Error("a stale snapshot was acted on after the hold window had passed")
	}
}

// One session is listed once, by person, by client and in the whole index,
// and stays one session with the same id across refreshes.
func indexMembership(t *testing.T, h *Harness) {
	first := h.Grant()
	opened := assertSessions(t, h, 1)
	refreshed := h.Refresh(first.Refresh)
	if refreshed.Status != http.StatusOK {
		t.Fatalf("refresh: %d %q", refreshed.Status, refreshed.Error)
	}
	if again := h.Refresh(refreshed.Refresh); again.Status != http.StatusOK {
		t.Fatalf("second refresh: %d %q", again.Status, again.Error)
	}
	after := assertSessions(t, h, 1)
	if after[0].ID != opened[0].ID {
		t.Errorf("a refresh changed the session id from %q to %q", opened[0].ID, after[0].ID)
	}
	if after[0].LastRefreshed.IsZero() {
		t.Error("a refreshed session does not say when it was refreshed")
	}
	if ended, err := h.Issuer.Sessions().RevokeID(context.Background(), opened[0].ID); err != nil || !ended {
		t.Fatalf("RevokeID = %v, %v", ended, err)
	}
	assertSessions(t, h, 0)
}

// assertSessions lists by person, by client and everything, and wants want
// sessions in each, the same ones.
func assertSessions(t *testing.T, h *Harness, want int) []issuer.Session {
	t.Helper()
	var first []issuer.Session
	for _, q := range []issuer.Query{{Identity: Person}, {ClientID: ClientID}, {}} {
		got, err := h.Issuer.Sessions().List(context.Background(), q)
		if err != nil {
			t.Fatalf("List(%+v): %v", q, err)
		}
		if len(got) != want {
			t.Fatalf("List(%+v) = %d sessions, want %d", q, len(got), want)
		}
		ids := make([]string, 0, len(got))
		for i := range got {
			ids = append(ids, got[i].ID)
		}
		if first == nil {
			first = got
			continue
		}
		for i := range first {
			if !slices.Contains(ids, first[i].ID) {
				t.Errorf("List(%+v) does not hold session %s", q, first[i].ID)
			}
		}
	}
	return first
}
