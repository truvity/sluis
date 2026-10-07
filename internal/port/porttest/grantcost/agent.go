package grantcost

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
)

// IdleAgentSessionIsRevocable holds one engine to the invariant of
// docs/decisions/0040-agent-class-sessions.md that index membership never
// ends before the record does: an agent session idle for three days --
// longer than twice lifetimes.refresh -- with an interactive session of the
// same person filed after it, is still listed and still ended by every
// revocation. On an engine whose set expiry is the whole set's, the later
// interactive Add is what would cut the agent session out.
//
// state is the engine, advance moves its clock; the session index is built
// over it with the default lifetimes.
func IdleAgentSessionIsRevocable(t *testing.T, state issuer.State, advance func(time.Duration)) {
	t.Helper()

	var skew time.Duration
	now := func() time.Time { return time.Now().Add(skew) }
	sessions := issuer.NewSessions(state, issuer.DefaultRefreshLifetime, issuer.DefaultAbsoluteLifetime)
	sessions.SetClock(now)
	sessions.SetAgentLifetimes(issuer.AgentLifetimes{
		Refresh: issuer.DefaultAgentRefresh, Absolute: issuer.DefaultAgentAbsolute, Access: issuer.DefaultAgentAccess,
	}, nil)

	idleAgentRevocation(t, sessions, now, func(d time.Duration) { advance(d); skew += d },
		func(ctx context.Context, identity string) (int, error) {
			return sessions.Revoke(ctx, issuer.Query{Identity: identity})
		})
}

// idleAgentSession is the guard over the harness's own issuer, whose
// person-wide revocation is Issuer.Revoke.
func idleAgentSession(t *testing.T, h *Harness) {
	idleAgentRevocation(t, h.Issuer.Sessions(), h.now, h.Advance, h.Issuer.Revoke)
}

func idleAgentRevocation(
	t *testing.T, sessions *issuer.Sessions, now func() time.Time, advance func(time.Duration),
	revokePerson func(ctx context.Context, identity string) (int, error),
) {
	t.Helper()
	ctx := context.Background()

	for i, kind := range []string{"person", "client for a person", "client for everybody", "browser"} {
		identity := fmt.Sprintf("agent-%d@north.example", i)
		client := fmt.Sprintf("mcp-host-%d", i)
		browser := fmt.Sprintf("browser-%d", i)

		agent, err := sessions.Record(ctx, issuer.Opened{
			Identity: identity, ClientID: client, How: issuer.HowCode, Token: "t-agent-" + browser,
			SSO: browser, AuthTime: now(), Class: issuer.ClassAgent,
		})
		if err != nil {
			t.Fatalf("%s: record the agent session: %v", kind, err)
		}
		if !agent.Agent() {
			t.Fatalf("%s: the agent session was recorded as %q", kind, agent.Class)
		}
		authTime := now()
		advance(time.Hour)
		if _, err = sessions.Record(ctx, issuer.Opened{
			Identity: identity, ClientID: ClientID, How: issuer.HowCode, Token: "t-console-" + browser,
			SSO: browser, AuthTime: authTime,
		}); err != nil {
			t.Fatalf("%s: record the interactive session: %v", kind, err)
		}

		// Three days idle: past 2 x lifetimes.refresh, inside the agent's 14.
		advance(3 * 24 * time.Hour)

		for _, q := range []issuer.Query{{Identity: identity}, {ClientID: client}, {}} {
			listed, err := sessions.List(ctx, q)
			if err != nil {
				t.Fatalf("%s: List(%+v): %v", kind, q, err)
			}
			if !slices.ContainsFunc(listed, func(s issuer.Session) bool { return s.ID == agent.ID }) {
				t.Errorf("%s: List(%+v) lost the idle agent session", kind, q)
			}
		}

		var ended int
		switch kind {
		case "person":
			ended, err = revokePerson(ctx, identity)
		case "client for a person":
			ended, err = sessions.Revoke(ctx, issuer.Query{Identity: identity, ClientID: client})
		case "client for everybody":
			ended, err = sessions.Revoke(ctx, issuer.Query{ClientID: client})
		case "browser":
			ended, err = sessions.Revoke(ctx, issuer.Query{Identity: identity, SSO: browser})
		}
		if err != nil || ended != 1 {
			t.Errorf("%s: revoke = %d, %v, want the idle agent session ended", kind, ended, err)
		}
		if _, live, _ := sessions.ByID(ctx, agent.ID); live {
			t.Errorf("%s: the agent session is still live after the revocation", kind)
		}
	}
}
