package issuer_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// The tests of agent-class chains (docs/decisions/0040-agent-class-sessions.md):
// a longer chain for clients the policy marks `session: agent`, decided
// once when an authorization completes, recorded on the session, and
// followed by every lifetime that bounds the chain.

const (
	agentClient      = "mcp-host"
	agentCapped      = "mcp-capped"
	twoDayResource   = "https://mcp.example/two-days"
	weekResourceRO   = "https://mcp.example/read-only-week"
	shortTokenTarget = "https://mcp.example/short-tokens"

	day = 24 * time.Hour
)

// agentPolicyText is the demonstration policy plus two agent clients (one
// with a ttl_cap), an interactive CLI that may trade its sign-in, and three
// resources: one whose absolute_cap shortens, one read-only that would
// lengthen an interactive chain to a week, and one with a short ttl_cap.
// With agent set false the two agent clients are interactive, which is
// "the same installation after a policy change".
func agentPolicyText(agent bool) string {
	session := "agent"
	if !agent {
		session = "interactive"
	}

	return strings.Replace(demo.Policy, "clients:\n", "clients:\n"+
		"  "+agentClient+": { kind: public, redirects: [http://127.0.0.1/callback], "+
		"requires: [devel:k8s:viewer, all:directory-roster:reader], session: "+session+" }\n"+
		"  "+agentCapped+": { kind: public, redirects: [http://127.0.0.1/callback], requires: [devel:k8s:viewer], ttl_cap: 10m, session: "+session+" }\n"+
		"  cli: { kind: public, redirects: [http://127.0.0.1/callback], requires: [devel:k8s:viewer, mgmt:k8s:admin], sign_in_exchange: true }\n", 1) +
		"resources:\n" +
		"  " + twoDayResource + ": { requires: [devel:k8s:viewer], absolute_cap: 48h }\n" +
		"  " + weekResourceRO + ": { requires: [devel:k8s:viewer], read_only: true, absolute_cap: 168h }\n" +
		"  " + shortTokenTarget + ": { requires: [devel:k8s:viewer], ttl_cap: 5m }\n"
}

func agentPolicySet(t *testing.T, text string) *policy.Set {
	t.Helper()

	declared, err := policy.Parse([]byte(text))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}

	return set
}

// agentWorld is one installation on a fake clock: an issuer under the
// default lifetimes (refresh 12h, absolute 24h, agent 14d / 30d / 30m) over
// a memory State whose expiry reads the same clock.
type agentWorld struct {
	iss   *issuer.Issuer
	state *issuer.MemoryState
	t0    time.Time
	now   time.Time
}

func newAgentWorld(t *testing.T, text string) *agentWorld {
	t.Helper()

	w := &agentWorld{state: issuer.NewMemoryState(), t0: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	w.now = w.t0
	w.iss = w.issuerOver(t, text)
	w.state.SetClock(w.clock)

	return w
}

func (w *agentWorld) clock() time.Time { return w.now }

// issuerOver is another issuer over the same State and clock: the same
// installation restarted with another policy.
func (w *agentWorld) issuerOver(t *testing.T, text string) *issuer.Issuer {
	t.Helper()

	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true},
		agentPolicySet(t, text), adaDirectory(), w.state)
	iss.Sessions().SetClock(w.clock)

	return iss
}

func (w *agentWorld) at(offset time.Duration) { w.now = w.t0.Add(offset) }

func (w *agentWorld) open(t *testing.T, o issuer.Opened) issuer.Session {
	t.Helper()

	if o.Identity == "" {
		o.Identity = "ada@north.example"
	}
	if o.How == "" {
		o.How = issuer.HowCode
	}
	if o.AuthTime.IsZero() {
		o.AuthTime = w.t0
	}
	session, err := w.iss.Sessions().Record(t.Context(), o)
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	return session
}

// An agent chain refreshes past the installation's 24h and ends at its
// deadline, 30 days from auth_time, however often it refreshes. Its class
// and deadline are on the record from the moment it is opened.
func TestAnAgentChainOutlivesADayAndEndsAtThirtyDays(t *testing.T) {
	t.Parallel()

	w := newAgentWorld(t, agentPolicyText(true))
	opened := w.open(t, issuer.Opened{ClientID: agentClient, Token: "t-0", Class: issuer.ClassAgent})

	deadline := w.t0.Add(30 * day)
	if !opened.Agent() || opened.Class != issuer.ClassAgent || !opened.Deadline.Equal(deadline) {
		t.Fatalf("opened class %q deadline %s, want agent and %s", opened.Class, opened.Deadline, deadline)
	}
	if want := w.t0.Add(14 * day); !opened.ExpiresAt.Equal(want) {
		t.Errorf("a new agent chain's end = %s, want its 14-day idle limit %s", opened.ExpiresAt, want)
	}

	token := "t-0"
	for i, offset := range []time.Duration{12 * time.Hour, 25 * time.Hour, 10 * day, 20 * day, 30*day - time.Hour} {
		w.at(offset)
		next := "t-" + strconv.Itoa(i+1)
		refreshed, successor, ok, err := w.iss.Sessions().Refreshed(t.Context(), token, next)
		if err != nil || !ok {
			t.Fatalf("refresh at +%s: ok=%v err=%v, want it live until +30d", offset, ok, err)
		}
		if refreshed.ExpiresAt.After(deadline) {
			t.Fatalf("at +%s the end is %s, past the deadline %s", offset, refreshed.ExpiresAt, deadline)
		}
		if !refreshed.Agent() || !refreshed.Deadline.Equal(deadline) {
			t.Fatalf("at +%s the class is %q and the deadline %s: a refresh changed them", offset, refreshed.Class, refreshed.Deadline)
		}
		token = successor
	}

	// At the deadline the refresh path tells the limit apart from a chain
	// that is simply gone: the record is still stored for the class's
	// refresh window, so it is refused BY the limit, with that reason.
	w.at(30 * day)
	storage, err := issuer.NewStorage(w.iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = storage.TokenRequestByRefreshToken(t.Context(), token); err == nil ||
		!strings.Contains(err.Error(), "absolute limit") {
		t.Errorf("a refresh at +30d: %v, want it refused at the absolute limit", err)
	}
	if _, _, ok, _ := w.iss.Sessions().Refreshed(t.Context(), token, "t-last"); ok {
		t.Error("a refresh at +30d worked, want the chain ended at its deadline")
	}
}

// An agent chain idle for longer than 14 days ends, and one idle for less
// does not: the class's own refresh window, kept by the record's and the
// token pointer's store lifetimes too.
func TestAnAgentChainEndsAfterFourteenDaysIdle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		idle time.Duration
		live bool
	}{
		{"a second inside", 14*day - time.Second, true},
		{"a second past", 14*day + time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := newAgentWorld(t, agentPolicyText(true))
			w.open(t, issuer.Opened{ClientID: agentClient, Token: "t-0", Class: issuer.ClassAgent})

			w.at(tc.idle)
			_, _, ok, err := w.iss.Sessions().Refreshed(t.Context(), "t-0", "t-1")
			if err != nil || ok != tc.live {
				t.Errorf("refresh after %s idle: ok=%v err=%v, want ok=%v", tc.idle, ok, err, tc.live)
			}
		})
	}
}

// An interactive chain is exactly what it was: a 12h idle window, ended at
// 24h from auth_time, recorded as interactive with no deadline. And a chain
// asked for as agent by an index that records no agent lifetimes is
// interactive, which is the safe direction.
func TestAnInteractiveChainIsUnchanged(t *testing.T) {
	t.Parallel()

	w := newAgentWorld(t, agentPolicyText(true))
	opened := w.open(t, issuer.Opened{ClientID: "local-dev", Token: "t-0"})
	if opened.Agent() || opened.Class != issuer.ClassInteractive || !opened.Deadline.IsZero() {
		t.Fatalf("an interactive chain was recorded as class %q with deadline %s", opened.Class, opened.Deadline)
	}
	if want := w.t0.Add(12 * time.Hour); !opened.ExpiresAt.Equal(want) {
		t.Errorf("its end = %s, want the 12h refresh window %s", opened.ExpiresAt, want)
	}

	w.at(11 * time.Hour)
	_, token, ok, _ := w.iss.Sessions().Refreshed(t.Context(), "t-0", "t-1")
	if !ok {
		t.Fatal("a refresh at +11h was refused")
	}
	w.at(22 * time.Hour)
	if _, token, ok, _ = w.iss.Sessions().Refreshed(t.Context(), token, "t-2"); !ok {
		t.Fatal("a refresh at +22h was refused")
	}
	w.at(24 * time.Hour)
	if _, _, ok, _ = w.iss.Sessions().Refreshed(t.Context(), token, "t-3"); ok {
		t.Error("an interactive chain refreshed at +24h, past the installation's absolute limit")
	}

	idle := newAgentWorld(t, agentPolicyText(true))
	idle.open(t, issuer.Opened{ClientID: "local-dev", Token: "t-0"})
	idle.at(12*time.Hour + time.Second)
	if _, _, ok, _ := idle.iss.Sessions().Refreshed(t.Context(), "t-0", "t-1"); ok {
		t.Error("an interactive chain idle past 12h refreshed")
	}

	bare := issuer.NewSessions(issuer.NewMemoryState(), 12*time.Hour, 24*time.Hour)
	recorded, err := bare.Record(t.Context(), issuer.Opened{
		Identity: "ada@north.example", ClientID: agentClient, How: issuer.HowCode, Token: "t-0",
		AuthTime: time.Now(), Class: issuer.ClassAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Agent() {
		t.Error("an index with no agent lifetimes recorded an agent chain")
	}
}

// A resource's absolute_cap still shortens an agent chain, and is only ever
// a ceiling: a two-day cap ends it at two days, and a read-only week (which
// lengthens an interactive chain) ends it at a week rather than 30 days.
func TestAResourceCapStillShortensAnAgentChain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		resource string
		limit    time.Duration
	}{
		{twoDayResource, 2 * day},
		{weekResourceRO, 7 * day},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			t.Parallel()

			w := newAgentWorld(t, agentPolicyText(true))
			opened := w.open(t, issuer.Opened{ClientID: agentClient, Resource: tc.resource, Token: "t-0", Class: issuer.ClassAgent})
			if want := w.t0.Add(tc.limit); !opened.Deadline.Equal(want) {
				t.Fatalf("deadline = %s, want auth_time+%s", opened.Deadline, tc.limit)
			}

			w.at(tc.limit - time.Hour)
			_, token, ok, _ := w.iss.Sessions().Refreshed(t.Context(), "t-0", "t-1")
			if !ok {
				t.Fatalf("a refresh an hour before +%s was refused", tc.limit)
			}
			w.at(tc.limit)
			if _, _, ok, _ = w.iss.Sessions().Refreshed(t.Context(), token, "t-2"); ok {
				t.Errorf("a refresh at +%s worked, past the resource's cap", tc.limit)
			}
		})
	}
}

// A change after issuance shortens an agent chain at its next refresh and
// never lengthens it past the deadline it was recorded with: a shorter
// lifetimes.agent, a resource cap added to the policy, a longer
// lifetimes.agent and a resource cap withdrawn.
func TestAChangeAfterIssuanceShortensAnAgentChainAndNeverLengthensIt(t *testing.T) {
	t.Parallel()

	withCap := strings.Replace(agentPolicyText(true),
		"  "+weekResourceRO+": { requires: [devel:k8s:viewer], read_only: true, absolute_cap: 168h }\n",
		"  "+weekResourceRO+": { requires: [devel:k8s:viewer], absolute_cap: 48h }\n", 1)
	if withCap == agentPolicyText(true) {
		t.Fatal("the policy edit matched nothing")
	}
	withoutCap := strings.Replace(agentPolicyText(true),
		"  "+weekResourceRO+": { requires: [devel:k8s:viewer], read_only: true, absolute_cap: 168h }\n",
		"  "+weekResourceRO+": { requires: [devel:k8s:viewer] }\n", 1)

	t.Run("a shorter lifetimes.agent", func(t *testing.T) {
		t.Parallel()

		w := newAgentWorld(t, agentPolicyText(true))
		w.open(t, issuer.Opened{ClientID: agentClient, Token: "t-0", Class: issuer.ClassAgent})
		w.iss.Sessions().SetAgentLifetimes(issuer.AgentLifetimes{Refresh: day, Absolute: 2 * day, Access: time.Minute}, nil)

		w.at(2*day - time.Hour)
		_, token, ok, _ := w.iss.Sessions().Refreshed(t.Context(), "t-0", "t-1")
		if !ok {
			t.Fatal("refused before the shortened limit")
		}
		w.at(2 * day)
		if _, _, ok, _ = w.iss.Sessions().Refreshed(t.Context(), token, "t-2"); ok {
			t.Error("refreshed at the shortened limit")
		}
	})

	t.Run("a resource cap added to the policy", func(t *testing.T) {
		t.Parallel()

		w := newAgentWorld(t, withoutCap)
		w.open(t, issuer.Opened{ClientID: agentClient, Resource: weekResourceRO, Token: "t-0", Class: issuer.ClassAgent})
		later := w.issuerOver(t, withCap)

		w.at(49 * time.Hour)
		if _, _, ok, _ := later.Sessions().Refreshed(t.Context(), "t-0", "t-1"); ok {
			t.Error("a cap added after issuance did not end the chain")
		}
	})

	t.Run("a longer lifetimes.agent", func(t *testing.T) {
		t.Parallel()

		w := newAgentWorld(t, agentPolicyText(true))
		w.open(t, issuer.Opened{ClientID: agentClient, Token: "t-0", Class: issuer.ClassAgent})
		w.iss.Sessions().SetAgentLifetimes(issuer.AgentLifetimes{Refresh: 60 * day, Absolute: 90 * day, Access: time.Hour}, nil)

		token := "t-0"
		for i, offset := range []time.Duration{13 * day, 26 * day} {
			w.at(offset)
			refreshed, successor, ok, _ := w.iss.Sessions().Refreshed(t.Context(), token, "t-"+strconv.Itoa(i+1))
			if !ok {
				t.Fatalf("refused at +%s", offset)
			}
			if refreshed.ExpiresAt.After(w.t0.Add(30 * day)) {
				t.Fatalf("a longer configuration moved the end to %s, past the recorded deadline", refreshed.ExpiresAt)
			}
			token = successor
		}
		w.at(30 * day)
		if _, _, ok, _ := w.iss.Sessions().Refreshed(t.Context(), token, "t-last"); ok {
			t.Error("a longer configuration let the chain outlive its recorded deadline")
		}
	})

	t.Run("a resource cap withdrawn from the policy", func(t *testing.T) {
		t.Parallel()

		w := newAgentWorld(t, withCap)
		opened := w.open(t, issuer.Opened{ClientID: agentClient, Resource: weekResourceRO, Token: "t-0", Class: issuer.ClassAgent})
		if !opened.Deadline.Equal(w.t0.Add(48 * time.Hour)) {
			t.Fatalf("deadline = %s, want auth_time+48h", opened.Deadline)
		}
		later := w.issuerOver(t, withoutCap)

		w.at(49 * time.Hour)
		if _, _, ok, _ := later.Sessions().Refreshed(t.Context(), "t-0", "t-1"); ok {
			t.Error("a cap withdrawn after issuance lengthened the chain past its recorded deadline")
		}
	})
}

// A client moved from agent to interactive keeps its agent chains until
// they end, and a chain recorded interactive stays interactive under a
// policy that now says agent: the class is the chain's, never re-derived.
func TestAPolicyChangeNeverChangesARecordedClass(t *testing.T) {
	t.Parallel()

	t.Run("agent stays agent", func(t *testing.T) {
		t.Parallel()

		w := newAgentWorld(t, agentPolicyText(true))
		w.open(t, issuer.Opened{ClientID: agentClient, Token: "t-0", Class: issuer.ClassAgent})
		later := w.issuerOver(t, agentPolicyText(false))

		w.at(25 * time.Hour)
		refreshed, _, ok, _ := later.Sessions().Refreshed(t.Context(), "t-0", "t-1")
		if !ok || !refreshed.Agent() {
			t.Errorf("after the client became interactive: ok=%v class=%q, want the agent chain to carry on as agent", ok, refreshed.Class)
		}
	})

	t.Run("interactive stays interactive", func(t *testing.T) {
		t.Parallel()

		w := newAgentWorld(t, agentPolicyText(false))
		w.open(t, issuer.Opened{ClientID: agentClient, Token: "t-0", Class: issuer.ClassInteractive})
		later := w.issuerOver(t, agentPolicyText(true))

		w.at(11 * time.Hour)
		refreshed, token, ok, _ := later.Sessions().Refreshed(t.Context(), "t-0", "t-1")
		if !ok || refreshed.Agent() {
			t.Fatalf("after the client became agent: ok=%v class=%q, want it interactive", ok, refreshed.Class)
		}
		w.at(24 * time.Hour)
		if _, _, ok, _ = later.Sessions().Refreshed(t.Context(), token, "t-2"); ok {
			t.Error("an interactive chain outlived 24h because its client became agent")
		}
	})
}

// The class is decided once, when the authorization request is completed,
// and taken from the request at code redemption: a policy flip between the
// two, in either direction, does not change the chain the code opens.
func TestTheClassDecidedAtCompleteSurvivesAPolicyFlip(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name               string
		atComplete, redeem bool
		want               issuer.SessionClass
	}{
		{"agent, then interactive", true, false, issuer.ClassAgent},
		{"interactive, then agent", false, true, issuer.ClassInteractive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			requests := issuer.NewMemoryState()
			w := newAgentWorld(t, agentPolicyText(tc.atComplete))
			w.now = time.Now()

			before, err := issuer.NewStorage(w.iss, fakeVerifier{}, nil, nil, nil, requests)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := before.CreateAuthRequest(ctx, &oidc.AuthRequest{
				ClientID: agentClient, RedirectURI: "http://127.0.0.1/callback",
				Scopes: oidc.SpaceDelimitedArray{"openid"}, ResponseType: oidc.ResponseTypeCode,
			}, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = before.CompleteAcceptedForTest(ctx, pending.GetID(), issuer.Authenticated{
				Subject: "ada@north.example", AuthTime: time.Now(), How: "google",
			}); err != nil {
				t.Fatalf("complete: %v", err)
			}

			after, err := issuer.NewStorage(w.issuerOver(t, agentPolicyText(tc.redeem)), fakeVerifier{}, nil, nil, nil, requests)
			if err != nil {
				t.Fatal(err)
			}
			completed, err := after.AuthRequestByID(ctx, pending.GetID())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err = after.CreateAccessAndRefreshTokens(ctx, completed.(op.TokenRequest), ""); err != nil {
				t.Fatalf("redeem: %v", err)
			}

			sessions, err := w.iss.Sessions().List(ctx, issuer.Query{Identity: "ada@north.example"})
			if err != nil || len(sessions) != 1 {
				t.Fatalf("sessions = %v, %v, want the one the code opened", sessions, err)
			}
			if got := sessions[0].Class; got != tc.want {
				t.Errorf("the chain's class = %q, want %q, as decided when the request completed", got, tc.want)
			}
		})
	}
}

// H1: an agent session idle for longer than twice lifetimes.refresh -- and
// so past any index horizon an interactive session's Add would once have
// given the sets -- is still found by every revocation and every listing.
// An interactive session filed after it re-Adds the same sets, which on an
// engine whose set expiry is the whole set's must not cut the agent session
// out of them.
func TestAnIdleAgentSessionIsStillFoundByEveryRevocation(t *testing.T) {
	t.Parallel()

	ended := func(t *testing.T, w *agentWorld) (int, error) {
		t.Helper()
		return w.iss.Revoke(t.Context(), "ada@north.example")
	}

	for name, revoke := range map[string]func(*testing.T, *agentWorld) (int, error){
		"Issuer.Revoke(identity)": ended,
		"per client for a person": func(t *testing.T, w *agentWorld) (int, error) {
			return w.iss.Sessions().Revoke(t.Context(), issuer.Query{Identity: "ada@north.example", ClientID: agentClient})
		},
		"per client for everybody": func(t *testing.T, w *agentWorld) (int, error) {
			return w.iss.Sessions().Revoke(t.Context(), issuer.Query{ClientID: agentClient})
		},
		"per browser": func(t *testing.T, w *agentWorld) (int, error) {
			return w.iss.Sessions().Revoke(t.Context(), issuer.Query{Identity: "ada@north.example", SSO: "browser-1"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newAgentWorld(t, agentPolicyText(true))
			agent := w.open(t, issuer.Opened{ClientID: agentClient, Token: "t-agent", Class: issuer.ClassAgent, SSO: "browser-1"})
			w.at(time.Hour)
			w.open(t, issuer.Opened{ClientID: "local-dev", Token: "t-console", SSO: "browser-1", AuthTime: w.t0})

			// Three days idle: past 2 x 12h, inside the 14-day idle limit.
			w.at(3 * day)
			if _, live, _ := w.iss.Sessions().ByID(t.Context(), agent.ID); !live {
				t.Fatal("the agent session is not live three days in")
			}

			for _, q := range []issuer.Query{{Identity: "ada@north.example"}, {ClientID: agentClient}, {}} {
				listed, err := w.iss.Sessions().List(t.Context(), q)
				if err != nil || len(listed) != 1 || listed[0].ID != agent.ID {
					t.Errorf("List(%+v) = %v, %v, want the idle agent session", q, listed, err)
				}
			}

			n, err := revoke(t, w)
			if err != nil || n != 1 {
				t.Fatalf("revoke = %d, %v, want the idle agent session ended", n, err)
			}
			if _, live, _ := w.iss.Sessions().ByID(t.Context(), agent.ID); live {
				t.Error("the agent session is still live after the revocation reported it ended")
			}
		})
	}
}

// signInProof refuses a token whose session has class agent: a recorded
// class outlives a policy change, so the load-time refusal of agent with
// sign_in_exchange is not enough on its own. The same client's interactive
// session still trades.
func TestASignInHeldByAnAgentSessionIsNotAProof(t *testing.T) {
	t.Parallel()

	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true},
		agentPolicySet(t, agentPolicyText(true)), adaDirectory(), issuer.NewMemoryState())
	storage, err := issuer.NewStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := handler(iss, storage)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	for _, tc := range []struct {
		class  issuer.SessionClass
		status int
	}{
		{issuer.ClassAgent, http.StatusBadRequest},
		{issuer.ClassInteractive, http.StatusOK},
	} {
		refresh := "cli-" + string(tc.class)
		// The CLI recorded as agent stands for a chain opened while the
		// policy said so; the policy now says interactive and trades.
		if _, err = iss.Sessions().Record(t.Context(), issuer.Opened{
			Identity: "ada@north.example", ClientID: "cli", How: issuer.HowCode, Token: refresh,
			Scopes: []string{"openid"}, AuthTime: time.Now(), Class: tc.class,
		}); err != nil {
			t.Fatal(err)
		}
		tokens := postToken(t, server, url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"cli"}, "scope": {"openid"},
		}, "cli", http.StatusOK)
		access, _ := tokens["access_token"].(string)

		status, body := exchangeSubject(t, server, "cli", access, string(oidc.AccessTokenType), "aws:1111:power")
		if status != tc.status {
			t.Errorf("a %s session's sign-in exchanged with %d %v, want %d", tc.class, status, body, tc.status)
		}
		if tc.class == issuer.ClassAgent && !strings.Contains(body["error_description"].(string), "agent-class") {
			t.Errorf("the refusal says %v, want it to name the agent class", body["error_description"])
		}
	}
}

// An agent chain's access and ID tokens are held to lifetimes.agent.access
// (30m, under a 1h lifetimes.token), and to anything shorter: the client's
// ttl_cap, a resource's ttl_cap, and the chain's own deadline. An
// interactive chain keeps the ordinary lifetime.
func TestAgentTokensAreCappedAtTheClassAccessLifetime(t *testing.T) {
	t.Parallel()

	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true},
		agentPolicySet(t, agentPolicyText(true)), adaDirectory(), issuer.NewMemoryState())
	storage, err := issuer.NewStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := handler(iss, storage)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	nearDeadline := time.Now().Add(-30*day + 10*time.Minute)
	for _, tc := range []struct {
		name     string
		client   string
		resource string
		class    issuer.SessionClass
		authTime time.Time
		max      time.Duration // the token's lifetime is at most this
		min      time.Duration // and at least this
		idToken  bool
	}{
		{"agent", agentClient, "", issuer.ClassAgent, time.Now(), 30 * time.Minute, 25 * time.Minute, true},
		{"agent with a client ttl_cap", agentCapped, "", issuer.ClassAgent, time.Now(), 10 * time.Minute, 5 * time.Minute, true},
		{"agent with a resource ttl_cap", agentClient, shortTokenTarget, issuer.ClassAgent, time.Now(), 5 * time.Minute, 3 * time.Minute, false},
		{"agent near its deadline", agentClient, "", issuer.ClassAgent, nearDeadline, 10 * time.Minute, 5 * time.Minute, true},
		{"interactive", "local-dev", "", issuer.ClassInteractive, time.Now(), time.Hour, 55 * time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refresh := "t-" + strings.ReplaceAll(tc.name, " ", "-")
			if _, err := iss.Sessions().Record(t.Context(), issuer.Opened{
				Identity: "ada@north.example", ClientID: tc.client, Resource: tc.resource, How: issuer.HowCode,
				Token: refresh, Scopes: []string{"openid"}, AuthTime: tc.authTime, Class: tc.class,
			}); err != nil {
				t.Fatal(err)
			}
			body := postToken(t, server, url.Values{
				"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {tc.client}, "scope": {"openid"},
			}, tc.client, http.StatusOK)

			now := time.Now()
			check := func(kind, raw string) {
				exp, ok := payloadOf(t, raw)["exp"].(float64)
				if !ok {
					t.Fatalf("the %s carries no exp", kind)
				}
				lifetime := time.Unix(int64(exp), 0).Sub(now)
				if lifetime > tc.max+5*time.Second || lifetime < tc.min {
					t.Errorf("the %s lives %s, want between %s and %s", kind, lifetime.Round(time.Second), tc.min, tc.max)
				}
			}
			access, _ := body["access_token"].(string)
			check("access token", access)
			if tc.idToken {
				id, _ := body["id_token"].(string)
				if id == "" {
					t.Fatalf("no id_token in %v", body)
				}
				check("ID token", id)
			}
		})
	}
}

// A step-up in the same browser refiles an agent session idle for days
// under the new sign-in, keeping its record for its class's window: the
// refile writes the record with the store lifetime its last write gave it,
// which for an agent chain is 14 days, not the installation's 12 hours.
func TestAnIdleAgentSessionIsRefiledAndKept(t *testing.T) {
	t.Parallel()

	w := newAgentWorld(t, agentPolicyText(true))
	agent := w.open(t, issuer.Opened{ClientID: agentClient, Token: "t-0", Class: issuer.ClassAgent, SSO: "browser-1"})

	w.at(2 * day)
	moved, err := w.iss.Sessions().Refile(t.Context(), "ada@north.example", "browser-1", "browser-2")
	if err != nil || moved != 1 {
		t.Fatalf("Refile = %d, %v, want the agent session moved", moved, err)
	}

	w.at(13 * day)
	got, live, err := w.iss.Sessions().ByID(t.Context(), agent.ID)
	if err != nil || !live || got.SSO != "browser-2" || !got.Agent() {
		t.Errorf("on day 13: live=%v sso=%q class=%q (err %v), want it live, agent, under browser-2", live, got.SSO, got.Class, err)
	}
}

// Lowering lifetimes.agent.refresh ends, at its next refresh, an agent chain
// already idle past the new window, though the end its record was written
// with is still ahead; one idle less than the new window carries on.
func TestAShorterAgentIdleLimitEndsAChainAlreadyIdlePastIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		idle    time.Duration
		refused bool
	}{
		{3 * day, true},
		{day, false},
	} {
		t.Run(tc.idle.String(), func(t *testing.T) {
			t.Parallel()

			w := newAgentWorld(t, agentPolicyText(true))
			w.open(t, issuer.Opened{ClientID: agentClient, Token: "t-0", Class: issuer.ClassAgent})
			w.iss.Sessions().SetAgentLifetimes(issuer.AgentLifetimes{Refresh: 2 * day, Absolute: 30 * day, Access: 30 * time.Minute}, nil)
			storage, err := issuer.NewStorage(w.iss, fakeVerifier{}, nil, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			w.at(tc.idle)
			_, err = storage.TokenRequestByRefreshToken(t.Context(), "t-0")
			if refused := err != nil; refused != tc.refused {
				t.Errorf("a chain idle %s under a 2-day window: err = %v, want refused = %v", tc.idle, err, tc.refused)
			}
		})
	}
}

// A recovery sign-in, the way in that bypasses the directory, always opens
// an interactive chain, whatever the client's class.
func TestARecoverySignInIsAlwaysInteractive(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newAgentWorld(t, agentPolicyText(true))
	w.now = time.Now()
	storage, err := issuer.NewStorage(w.iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, who := range []issuer.Authenticated{
		{Subject: "k8s:identity-system:authorization-webhook", AuthTime: time.Now(), How: issuer.RecoveryHow},
		{Subject: "ada@north.example", AuthTime: time.Now(), How: "google"},
	} {
		pending, err := storage.CreateAuthRequest(ctx, &oidc.AuthRequest{
			ClientID: agentClient, RedirectURI: "http://127.0.0.1/callback",
			Scopes: oidc.SpaceDelimitedArray{"openid"}, ResponseType: oidc.ResponseTypeCode,
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		// A recovery sign-in needs no acceptance, being interactive
		// whatever the client; a person's to an agent client does.
		complete := storage.Complete
		if who.How != issuer.RecoveryHow {
			complete = storage.CompleteAcceptedForTest
		}
		if err = complete(ctx, pending.GetID(), who); err != nil {
			t.Fatalf("complete %s: %v", who.How, err)
		}
		completed, err := storage.AuthRequestByID(ctx, pending.GetID())
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err = storage.CreateAccessAndRefreshTokens(ctx, completed.(op.TokenRequest), ""); err != nil {
			t.Fatalf("redeem %s: %v", who.How, err)
		}

		sessions, err := w.iss.Sessions().List(ctx, issuer.Query{Identity: who.Subject})
		if err != nil || len(sessions) != 1 {
			t.Fatalf("%s: sessions = %v, %v", who.How, sessions, err)
		}
		want := issuer.ClassAgent
		if who.How == issuer.RecoveryHow {
			want = issuer.ClassInteractive
		}
		if got := sessions[0].Class; got != want {
			t.Errorf("a %s sign-in to an agent client opened a %q chain, want %q", who.How, got, want)
		}
	}
}
