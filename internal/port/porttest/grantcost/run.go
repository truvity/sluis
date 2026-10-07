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
// authorization_code 9 writes, 4 reads, 1 resolution; refresh_token 4
// writes, 4 reads, 1 resolution. One read of each is the revision of the
// last-known groups, read so that their write is skipped only while the
// record is still this process's own: an eventually consistent read of no
// value, half a read unit on DynamoDB (port.RevisionPeeker).
//
// authorization_code was raised to 5 reads once, on purpose: a code
// completed under a browser sign-in reads that sign-in once its session
// is filed, and ends the session when the sign-in has ended, so that a
// sign-out landing between a silent completion and the redemption is not
// outlived by the session the code opened (issuer.Storage.signInEnded).
// The refresh path pays nothing for it.
var Budgets = []Budget{
	{
		Grant: "authorization_code", MaxWrites: 9, MaxReads: 5, Resolutions: 1,
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
		Grant: "refresh_token", MaxWrites: 4, MaxReads: 4, Resolutions: 1,
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
		Grant: "refresh_token (steady)", MaxWrites: 4, MaxReads: 4, Resolutions: 1,
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
	{
		// A refresh token whose session has ended -- here by the operator's
		// revoke of the person, which deletes the record and leaves the
		// token's pointer -- presented for the first time: the pointer and
		// the record it names are read, and that is all. Measured
		// 2026-10-07 on the memory adapter: 4 reads before the negative
		// cache (the same two again, to tell an absolute-limit refusal
		// apart), 2 after; the dead state needs no second look.
		Grant: "refresh_token (dead)", MaxWrites: 0, MaxReads: 2, Resolutions: 0,
		drive: func(t *testing.T, h *Harness) Counts {
			dead := deadRefreshToken(t, h)
			var refused Tokens
			counts := h.Measure(func() { refused = h.Refresh(dead) })
			wantDeadRefused(t, refused)
			return counts
		},
	},
	{
		// The same dead refresh token presented again, as a host looping on
		// an ended chain does, once a second dead verdict a minute after
		// the first has confirmed it: refused from the issuer's in-process
		// negative cache before anything is read
		// (docs/decisions/0040-agent-class-sessions.md, decision 10). Until
		// the confirmation every presentation costs what the first does.
		// Measured 2026-10-07 on the memory adapter, the DynamoDB fake and
		// LocalStack: 0 writes, 0 reads, no resolution. It was 4 reads
		// before.
		Grant: "refresh_token (dead, repeated)", MaxWrites: 0, MaxReads: 0, Resolutions: 0,
		drive: func(t *testing.T, h *Harness) Counts {
			dead := deadRefreshToken(t, h)
			wantDeadRefused(t, h.Refresh(dead))
			h.Advance(time.Minute + time.Second)
			wantDeadRefused(t, h.Refresh(dead))
			var refused Tokens
			counts := h.Measure(func() { refused = h.Refresh(dead) })
			wantDeadRefused(t, refused)
			return counts
		},
	},
	{
		// One request to the console on this issuer's origin from a browser
		// already signed in, in steady state. The console decides whether
		// the browser's sign-in still stands with the issuer's own checks
		// -- the absolute limit and the directory -- and then resolves the
		// role; both ask about the same person, and they are answered by
		// ONE resolution ([hub.OneAnswerPerRequest]).
		//
		// Measured 2026-10-07 on the memory adapter. Before the console
		// applied the issuer's checks: 0 writes, 3 reads (the sign-in's
		// cookie and record, the workspaces), 1 resolution, 1 snapshot
		// read. After: 0 writes, 4 reads, 1 resolution, 1 snapshot read --
		// the fourth read is the revision of the last-known groups (an
		// eventually consistent read of no value), the same one every
		// refresh pays, and the resolution is the one the authorizer
		// already made. Without the one-answer memory it was 5 reads, 2
		// resolutions and 2 snapshot reads.
		Grant: "console request", MaxWrites: 0, MaxReads: 4, Resolutions: 1,
		drive: func(t *testing.T, h *Harness) Counts {
			if code := h.SignIn(); code == "" {
				t.Fatal("the sign-in ended in no authorization code")
			}
			if first := h.Console(); first.Status != "signed-in" {
				t.Fatalf("first console request: %+v, want signed in", first)
			}
			var who Whoami
			counts := h.Measure(func() { who = h.Console() })
			if who.Status != "signed-in" || who.Email != Person {
				t.Fatalf("console request: %+v, want %s signed in", who, Person)
			}
			return counts
		},
	},
	{
		// The issuer's session service, by the browser's cookie: a person
		// listing their own sessions, as the console's account page does.
		// Measured 2026-10-07 on the memory adapter, before and after the
		// cookie was held to the issuer's own sign-in check (the absolute
		// limit, the directory): the same, 0 writes, 7 reads (the sign-in's
		// cookie and record, the workspaces, the revision of the last-known
		// groups, then the listing's two index reads and the sign-in record
		// it shows), 1 resolution, 1 snapshot read. The check costs no read
		// of its own: the limit is arithmetic on the record already read,
		// and the directory's answer it asks is the one the groups were
		// already evaluated from.
		Grant: "session service call", MaxWrites: 0, MaxReads: 7, Resolutions: 1,
		drive: func(t *testing.T, h *Harness) Counts {
			if code := h.SignIn(); code == "" {
				t.Fatal("the sign-in ended in no authorization code")
			}
			if _, err := h.OwnSessions(); err != nil {
				t.Fatalf("first session service call: %v", err)
			}
			var err error
			counts := h.Measure(func() { _, err = h.OwnSessions() })
			if err != nil {
				t.Fatalf("session service call: %v", err)
			}
			return counts
		},
	},
}

// deadRefreshToken is a refresh token whose session the operator has ended:
// its pointer is still stored, the record it names is gone.
func deadRefreshToken(t *testing.T, h *Harness) string {
	t.Helper()
	first := h.Grant()
	if n, err := h.Issuer.Revoke(context.Background(), Person); err != nil || n != 1 {
		t.Fatalf("Revoke(person) = %d, %v; want 1 session ended", n, err)
	}
	return first.Refresh
}

// wantDeadRefused is a dead refresh token's answer: invalid_grant.
func wantDeadRefused(t *testing.T, refused Tokens) {
	t.Helper()
	if refused.Status == http.StatusOK || refused.Error != "invalid_grant" {
		t.Fatalf("a dead refresh token: %d %q, want invalid_grant", refused.Status, refused.Error)
	}
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
	{"reuse-after-grace-ends-the-session", reuseAfterGrace},
	{"revocation-ends-the-chain", revocation},
	{"operator-revoke-ends-the-chain", operatorRevoke},
	{"stale-snapshot-is-not-authoritative", staleSnapshot},
	{"index-membership", indexMembership},
	{"idle-agent-session-is-revocable", idleAgentSession},
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

// Past the grace window a spent refresh token is refused AND ends the
// session it was spent in (RFC 9700 section 4.14.2): two parties presenting
// one token is how a stolen token shows itself, and which of them is the
// thief cannot be told, so the successor stops working too and the session
// is gone from every listing.
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
	if next := h.Refresh(rotated.Refresh); next.Status == http.StatusOK {
		t.Error("the successor still refreshes after a reuse of its predecessor; the session was not ended")
	}
	assertSessions(t, h, 0)
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
