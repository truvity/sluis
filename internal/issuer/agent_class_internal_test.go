package issuer

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/policy"
)

// An agent chain's spent marks are kept until the deadline recorded on it,
// not for one refresh window and not until the installation's 24h: a token
// spent on the first day and presented on the 29th, while the chain lives
// on, is still known as a reuse of that chain. Past the deadline the mark
// is gone, because there is no chain left to end.
func TestAnAgentChainsSpentMarksLiveUntilItsDeadline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	now := t0
	clock := func() time.Time { return now }

	state := NewMemoryState()
	state.SetClock(clock)
	sessions := NewSessions(state, 12*time.Hour, 24*time.Hour)
	sessions.SetClock(clock)
	sessions.SetAgentLifetimes(AgentLifetimes{Refresh: 14 * 24 * time.Hour, Absolute: 30 * 24 * time.Hour, Access: 30 * time.Minute}, nil)

	opened, err := sessions.Record(ctx, Opened{
		Identity: "ada@north.example", ClientID: "mcp-host", How: HowCode, Token: "t0",
		AuthTime: t0, Class: ClassAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := t0.Add(30 * 24 * time.Hour)

	token := "t0"
	for i, at := range []time.Duration{time.Hour, 13 * 24 * time.Hour, 26 * 24 * time.Hour} {
		now = t0.Add(at)
		next := "t" + string(rune('1'+i))
		_, successor, ok, err := sessions.Refreshed(ctx, token, next)
		if err != nil || !ok {
			t.Fatalf("refresh at +%s: ok=%v err=%v", at, ok, err)
		}
		token = successor
	}

	// Day 29: t0 was spent at +1h. Its mark is still there, and it names
	// the live chain as a reuse.
	now = t0.Add(29 * 24 * time.Hour)
	raw, found, err := state.Get(ctx, sessionTokenKey("t0"))
	if err != nil || !found || !isSpent(raw) {
		t.Fatalf("on day 29 the mark of the token spent on day 1 is found=%v (err %v), want it kept to the deadline", found, err)
	}
	p, _, err := sessions.present(ctx, "t0")
	if err != nil || p.reused != opened.ID {
		t.Errorf("presenting it on day 29 reads reused=%q (err %v), want the chain %s", p.reused, err, opened.ID)
	}

	// And the rotation keeps it exactly that long.
	if got, want := sessions.spentLifetime(Session{Class: ClassAgent, Deadline: deadline, AuthTime: t0, ExpiresAt: now.Add(time.Hour)}, now), deadline.Sub(now); got != want {
		t.Errorf("spentLifetime = %s, want until the recorded deadline (%s)", got, want)
	}

	now = deadline.Add(time.Second)
	if _, found, _ = state.Get(ctx, sessionTokenKey("t0")); found {
		t.Error("the mark outlived the chain's deadline")
	}
}

// client_documents.ttl_cap reaches the ACCESS token of a document client,
// not only its ID token: the cap is looked up from the policy, not among
// declared clients alone, and never from the document.
func TestClientDocumentsTTLCapReachesAccessTokens(t *testing.T) {
	t.Parallel()

	declared, err := policy.Parse([]byte("version: 1\n" +
		"groups: { viewers: { members: [eng@north.example] } }\n" +
		"client_documents: { origins: [hosts.example], requires: [viewers], ttl_cap: 10m }\n"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	iss := New(Config{URL: "http://issuer.example", AllowInsecure: true}, set,
		aDirectory{"ada@north.example": {Found: true, Authoritative: true, Groups: []string{"eng@north.example"}}},
		NewMemoryState())
	storage, err := NewStorage(iss, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for client, want := range map[string]time.Duration{
		"https://hosts.example/client.json": 10 * time.Minute,
		"https://elsewhere.example/c.json":  time.Hour,
	} {
		issued, err := storage.issue(context.Background(), &refreshRequest{session: Session{
			ID: "s", Identity: "ada@north.example", ClientID: client, How: HowCode, AuthTime: time.Now(),
		}})
		if err != nil {
			t.Fatalf("issue for %s: %v", client, err)
		}
		if lifetime := time.Until(issued.Expires); lifetime > want || lifetime < want-time.Minute {
			t.Errorf("an access token for %s lives %s, want %s", client, lifetime.Round(time.Second), want)
		}
	}
}

// The ID token minted after an agent chain's access token is held to the
// same end, through the request's carrier; one for any other request
// keeps the client's own lifetime.
func TestAnAgentChainsIDTokenFollowsItsAccessToken(t *testing.T) {
	t.Parallel()

	ctx := withSigningAudienceContext(context.Background())
	carrier := signingAudienceFrom(ctx)
	c := &client{id: "mcp-host", lifetime: time.Hour, signing: carrier}

	if got := c.IDTokenLifetime(); got != time.Hour {
		t.Errorf("unmarked: %s, want the client's own hour", got)
	}

	carrier.markAgentUntil(time.Now().Add(30 * time.Minute))
	if got := c.IDTokenLifetime(); got > 30*time.Minute || got < 29*time.Minute {
		t.Errorf("marked for an agent chain: %s, want 30m", got)
	}

	short := &client{id: "mcp-capped", lifetime: 10 * time.Minute, signing: carrier}
	if got := short.IDTokenLifetime(); got != 10*time.Minute {
		t.Errorf("a client capped below the mark: %s, want its own 10m", got)
	}

	if got := (&client{lifetime: time.Hour}).IDTokenLifetime(); got != time.Hour {
		t.Errorf("no carrier: %s, want the client's own hour", got)
	}
}

// Invariant: index membership never ends before the record does. After
// every write of an agent chain -- its opening and each refresh, the last
// ones near its deadline -- the sets hold it at least as long as the store
// keeps its record (one refresh window of its class from the write) and
// past its end.
func TestAnAgentSessionsIndexNeverEndsBeforeItsRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	now := t0
	clock := func() time.Time { return now }

	state := NewMemoryState()
	state.SetClock(clock)
	sessions := NewSessions(state, 12*time.Hour, 24*time.Hour)
	sessions.SetClock(clock)
	sessions.SetAgentLifetimes(AgentLifetimes{Refresh: 14 * 24 * time.Hour, Absolute: 30 * 24 * time.Hour, Access: 30 * time.Minute}, nil)

	session, err := sessions.Record(ctx, Opened{
		Identity: "ada@north.example", ClientID: "mcp-host", How: HowCode, Token: "t0",
		AuthTime: t0, Class: ClassAgent,
	})
	if err != nil {
		t.Fatal(err)
	}

	check := func(written time.Time, s Session) {
		t.Helper()
		if record := written.Add(sessions.refreshOf(s)); s.IndexedUntil.Before(record) {
			t.Errorf("written at +%s: indexed until %s, before the record's store expiry %s",
				written.Sub(t0), s.IndexedUntil, record)
		}
		if s.IndexedUntil.Before(s.ExpiresAt) {
			t.Errorf("written at +%s: indexed until %s, before the session's end %s", written.Sub(t0), s.IndexedUntil, s.ExpiresAt)
		}
	}
	check(t0, session)

	token := "t0"
	for i, at := range []time.Duration{24 * time.Hour, 13 * 24 * time.Hour, 20 * 24 * time.Hour, 29 * 24 * time.Hour} {
		now = t0.Add(at)
		refreshed, successor, ok, err := sessions.Refreshed(ctx, token, "t"+string(rune('1'+i)))
		if err != nil || !ok {
			t.Fatalf("refresh at +%s: ok=%v err=%v", at, ok, err)
		}
		check(now, refreshed)
		token = successor
	}
}

// agentInternalIssuer is an issuer whose one client, mcp-host, is
// agent-class, over the default lifetimes.
func agentInternalIssuer(t *testing.T) (*Issuer, *Storage) {
	t.Helper()

	declared, err := policy.Parse([]byte("version: 1\n" +
		"groups: { viewers: { members: [eng@north.example] } }\n" +
		"clients:\n" +
		"  mcp-host: { kind: public, redirects: ['http://127.0.0.1/callback'], requires: [viewers], session: agent }\n"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	iss := New(Config{URL: "http://issuer.example", AllowInsecure: true}, set,
		aDirectory{"ada@north.example": {Found: true, Authoritative: true, Groups: []string{"eng@north.example"}}},
		NewMemoryState())
	storage, err := NewStorage(iss, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	return iss, storage
}

// On the code-redemption path, with and without a refresh token, an agent
// client's access token and the ID token minted after it are held to
// lifetimes.agent.access (30m under a 1h lifetimes.token) and never past
// the deadline the chain is about to be recorded with.
func TestAnAgentCodeRedemptionCapsItsTokens(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		refresh bool
		authAgo time.Duration
		max     time.Duration
		min     time.Duration
	}{
		{"with a refresh token", true, 0, 30 * time.Minute, 29 * time.Minute},
		{"without a refresh token", false, 0, 30 * time.Minute, 29 * time.Minute},
		{"with a refresh token, ten minutes before the deadline", true, 30*24*time.Hour - 10*time.Minute, 10 * time.Minute, 9 * time.Minute},
		{"without a refresh token, ten minutes before the deadline", false, 30*24*time.Hour - 10*time.Minute, 10 * time.Minute, 9 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, storage := agentInternalIssuer(t)
			ctx := withSigningAudienceContext(context.Background())

			pending, err := storage.CreateAuthRequest(ctx, &oidc.AuthRequest{
				ClientID: "mcp-host", RedirectURI: "http://127.0.0.1/callback",
				Scopes: oidc.SpaceDelimitedArray{"openid"}, ResponseType: oidc.ResponseTypeCode,
			}, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = storage.CompleteAcceptedForTest(ctx, pending.GetID(), Authenticated{
				Subject: "ada@north.example", AuthTime: time.Now().Add(-tc.authAgo), How: "google",
			}); err != nil {
				t.Fatalf("complete: %v", err)
			}
			request, err := storage.request(ctx, pending.GetID())
			if err != nil {
				t.Fatal(err)
			}
			client, err := storage.GetClientByClientID(ctx, "mcp-host")
			if err != nil {
				t.Fatal(err)
			}

			var expires time.Time
			if tc.refresh {
				_, _, expires, err = storage.CreateAccessAndRefreshTokens(ctx, request, "")
			} else {
				_, expires, err = storage.CreateAccessToken(ctx, request)
			}
			if err != nil {
				t.Fatalf("redeem: %v", err)
			}

			if lifetime := time.Until(expires); lifetime > tc.max || lifetime < tc.min {
				t.Errorf("the access token lives %s, want between %s and %s", lifetime.Round(time.Second), tc.min, tc.max)
			}
			if lifetime := client.IDTokenLifetime(); lifetime > tc.max || lifetime < tc.min {
				t.Errorf("the ID token lives %s, want between %s and %s", lifetime.Round(time.Second), tc.min, tc.max)
			}
		})
	}
}

// indexed re-adds a session once its last Add is a refresh window old, so a
// membership lost out of band (an eviction, a listing racing a rotation)
// is restored within one window: twelve hours for an interactive session,
// fourteen days for an agent one -- not after the 30-day horizon every Add
// now carries.
func TestALostMembershipIsRestoredWithinOneRefreshWindow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		class  SessionClass
		before time.Duration // a refresh here does not re-add
		after  time.Duration // a refresh here does
	}{
		{"interactive", ClassInteractive, 6 * time.Hour, 12*time.Hour + time.Minute},
		{"agent", ClassAgent, 13 * 24 * time.Hour, 14*24*time.Hour + time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
			now := t0
			clock := func() time.Time { return now }
			state := NewMemoryState()
			state.SetClock(clock)
			sessions := NewSessions(state, 12*time.Hour, 24*time.Hour)
			sessions.SetClock(clock)
			sessions.SetAgentLifetimes(AgentLifetimes{Refresh: 14 * 24 * time.Hour, Absolute: 30 * 24 * time.Hour, Access: 30 * time.Minute}, nil)

			opened, err := sessions.Record(ctx, Opened{
				Identity: "ada@north.example", ClientID: "c", How: HowCode, Token: "t0", AuthTime: t0, Class: tc.class,
			})
			if err != nil {
				t.Fatal(err)
			}
			key := sessionOfKey("ada@north.example")
			if err = state.Remove(ctx, key, opened.ID); err != nil {
				t.Fatal(err)
			}
			member := func() bool {
				ids, err := state.Members(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				return slices.Contains(ids, opened.ID)
			}

			now = t0.Add(tc.before)
			_, token, ok, err := sessions.Refreshed(ctx, "t0", "t1")
			if err != nil || !ok {
				t.Fatalf("refresh at +%s: %v %v", tc.before, ok, err)
			}
			if member() {
				t.Errorf("re-added at +%s, inside one window of the last Add: one Add per refresh is what indexed exists to avoid", tc.before)
			}

			now = t0.Add(tc.after)
			if _, _, ok, err = sessions.Refreshed(ctx, token, "t2"); err != nil || !ok {
				t.Fatalf("refresh at +%s: %v %v", tc.after, ok, err)
			}
			if !member() {
				t.Errorf("a membership lost at the opening is still lost at +%s, past one refresh window", tc.after)
			}
		})
	}
}

// A step-up's refile drops from the person's set the ids whose records are
// gone, as a listing does, rather than reading them again at every step-up
// for as long as the set's horizon lasts.
func TestRefileDropsTheIDsOfEndedSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	now := t0
	clock := func() time.Time { return now }
	state := NewMemoryState()
	state.SetClock(clock)
	sessions := NewSessions(state, 12*time.Hour, 24*time.Hour)
	sessions.SetClock(clock)

	for _, token := range []string{"t-a", "t-b"} {
		if _, err := sessions.Record(ctx, Opened{
			Identity: "ada@north.example", ClientID: "c", How: HowCode, Token: token, AuthTime: t0, SSO: "browser-1",
		}); err != nil {
			t.Fatal(err)
		}
	}

	now = t0.Add(13 * time.Hour) // both records ran out at 12h
	if moved, err := sessions.Refile(ctx, "ada@north.example", "browser-1", "browser-2"); err != nil || moved != 0 {
		t.Fatalf("Refile = %d, %v, want nothing to move", moved, err)
	}
	if ids, err := state.Members(ctx, sessionOfKey("ada@north.example")); err != nil || len(ids) != 0 {
		t.Errorf("the person's set still names %v (err %v) after a refile met their ended records", ids, err)
	}
}
