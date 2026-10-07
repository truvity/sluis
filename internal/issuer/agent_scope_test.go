package issuer_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// A person's own sign-out everywhere comes in three
// (docs/decisions/0040-agent-class-sessions.md, decision 7, amended):
// "Sign out all browsers and apps" ends every interactive session and
// every browser sign-in, "Disconnect all agents" ends every agent session,
// and "Sign out everything" ends both -- as does any scope the issuer does
// not know, and any scope on a revoke that is not the person's own.

// scopeWorld is a person signed in in two browsers, with an interactive
// session and an agent session under the first, an agent session under no
// browser, and two `openid`-only clients involved under the first: grafana
// (no session) and the agent client (which also holds its agent session
// there).
type scopeWorld struct {
	rig      *ssoRig
	browsers []string
	told     []issuer.Session
}

func newScopeWorld(t *testing.T) *scopeWorld {
	t.Helper()

	rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
	_, first := rig.signedInBrowser(t)
	_, second := rig.signedInBrowser(t)

	rig.open(t, ssoEmail, "argocd", first)
	rig.openAgent(t, ssoEmail, agentClient, first, "agent-token", time.Now())
	rig.openAgent(t, ssoEmail, "another-agent", "", "other-agent-token", time.Now())

	for _, client := range []string{"grafana", agentClient} {
		if err := rig.iss.SSO().Involve(context.Background(), first, client); err != nil {
			t.Fatal(err)
		}
	}

	return &scopeWorld{rig: rig, browsers: []string{first, second}}
}

// revoke is the person's own sign-out everywhere with scope, recording
// what the service tells by Back-Channel Logout.
func (w *scopeWorld) revoke(t *testing.T, as string, request *accessissuerv1.RevokeSessionsRequest) int32 {
	t.Helper()

	svc := w.rig.service().WithAnnounceForTest(func(_ context.Context, sessions []issuer.Session) {
		w.told = append(w.told, sessions...)
	})

	ended, err := revoke(t, svc, as, request)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}

	return ended
}

func (w *scopeWorld) clients(t *testing.T) map[string]bool {
	t.Helper()

	out := map[string]bool{}
	for client := range w.rig.listed(t, ssoEmail) {
		out[client] = true
	}

	return out
}

func (w *scopeWorld) signedIn(t *testing.T) int {
	t.Helper()

	live := 0
	for _, id := range w.browsers {
		if _, ok, err := w.rig.iss.SSO().Get(context.Background(), id); err != nil {
			t.Fatal(err)
		} else if ok {
			live++
		}
	}

	return live
}

// toldTo is which clients were told, by session (an id) or by subject.
func (w *scopeWorld) toldTo() map[string]string {
	out := map[string]string{}
	for _, one := range w.told {
		how := "subject"
		if one.ID != "" {
			how = "session"
		}
		out[one.ClientID] = how
	}

	return out
}

func TestEachSignOutEverywhereScopeEndsExactlyItsClass(t *testing.T) {
	t.Parallel()

	own := ssoEmail + "|"
	everything := map[string]string{"argocd": "session", agentClient: "session", "another-agent": "session", "grafana": "subject"}

	for _, tc := range []struct {
		name     string
		scope    accessissuerv1.RevokeScope
		ended    int32
		left     []string
		signedIn int
		told     map[string]string
		record   map[string]any
	}{
		{
			name: "sign out all browsers and apps", scope: accessissuerv1.RevokeScope_REVOKE_SCOPE_INTERACTIVE,
			ended: 1, left: []string{agentClient, "another-agent"}, signedIn: 0,
			// grafana by subject; the agent client holds a session that keeps
			// running under the same sign-in, so it gets no subject-only token.
			told:   map[string]string{"argocd": "session", "grafana": "subject"},
			record: map[string]any{"scope": audit.ScopeEveryBrowserAndApp, "ended_class": "interactive", "kept_class": "agent"},
		},
		{
			name: "disconnect all agents", scope: accessissuerv1.RevokeScope_REVOKE_SCOPE_AGENTS,
			ended: 2, left: []string{"argocd"}, signedIn: 2,
			told:   map[string]string{agentClient: "session", "another-agent": "session"},
			record: map[string]any{"scope": audit.ScopeEveryAgent, "ended_class": "agent", "kept_class": "interactive"},
		},
		{
			name: "sign out everything", scope: accessissuerv1.RevokeScope_REVOKE_SCOPE_EVERYTHING,
			ended: 3, signedIn: 0, told: everything, record: map[string]any{"scope": audit.ScopeEverywhere},
		},
		{
			name: "no scope", scope: accessissuerv1.RevokeScope_REVOKE_SCOPE_UNSPECIFIED,
			ended: 3, signedIn: 0, told: everything, record: map[string]any{"scope": audit.ScopeEverywhere},
		},
		{
			name: "a scope this issuer does not know", scope: accessissuerv1.RevokeScope(42),
			ended: 3, signedIn: 0, told: everything, record: map[string]any{"scope": audit.ScopeEverywhere},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := newScopeWorld(t)
			if ended := w.revoke(t, own, &accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail, Scope: tc.scope}); ended != tc.ended {
				t.Errorf("ended %d, want %d", ended, tc.ended)
			}

			left := w.clients(t)
			if len(left) != len(tc.left) {
				t.Errorf("left %v, want %v", left, tc.left)
			}
			for _, client := range tc.left {
				if !left[client] {
					t.Errorf("%s was ended, want it kept: left %v", client, left)
				}
			}

			if got := w.signedIn(t); got != tc.signedIn {
				t.Errorf("%d browsers still signed in, want %d", got, tc.signedIn)
			}

			told := w.toldTo()
			if len(told) != len(tc.told) {
				t.Errorf("told %v, want %v", told, tc.told)
			}
			for client, how := range tc.told {
				if told[client] != how {
					t.Errorf("%s told by %q, want by %s: %v", client, told[client], how, told)
				}
			}

			revoked := w.rig.trail.Find("roster.session.revoked")
			if len(revoked) != 1 {
				t.Fatalf("%d roster.session.revoked records: %v", len(revoked), w.rig.trail.Actions())
			}
			for field, want := range tc.record {
				if got := fieldOf(revoked[0], field); got != want {
					t.Errorf("%s = %v, want %v", field, got, want)
				}
			}
			if got := fieldOf(revoked[0], "ended"); got != float64(tc.ended) {
				t.Errorf("ended = %v, want %d", got, tc.ended)
			}
		})
	}
}

// A scope narrows only the person's OWN sign-out everywhere. An operator
// revoking somebody else's sessions with a scope, and every path that is
// not a person's sign-out -- Issuer.Revoke, removal from the directory,
// the operator's revoke of one client for everybody -- end every class.
func TestAScopeNeverNarrowsAnOperatorOrTheDirectory(t *testing.T) {
	t.Parallel()

	operator := "op@north.example|" + policy.GroupOperators

	for _, scope := range []accessissuerv1.RevokeScope{
		accessissuerv1.RevokeScope_REVOKE_SCOPE_INTERACTIVE, accessissuerv1.RevokeScope_REVOKE_SCOPE_AGENTS,
	} {
		w := newScopeWorld(t)
		if ended := w.revoke(t, operator, &accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail, Scope: scope}); ended != 3 {
			t.Errorf("an operator's revoke with %s ended %d, want all 3", scope, ended)
		}
		if left := w.clients(t); len(left) != 0 || w.signedIn(t) != 0 {
			t.Errorf("an operator's revoke with %s left %v and %d browsers", scope, left, w.signedIn(t))
		}
		if got := fieldOf(w.rig.trail.Find("roster.session.revoked")[0], "scope"); got != audit.ScopeEverywhere {
			t.Errorf("an operator's revoke with %s was recorded as %v", scope, got)
		}

		// A scope beside a client is the per-client revoke, unchanged.
		w = newScopeWorld(t)
		if ended := w.revoke(t, ssoEmail+"|", &accessissuerv1.RevokeSessionsRequest{
			Identity: ssoEmail, ClientId: agentClient, Scope: scope,
		}); ended != 1 || w.clients(t)[agentClient] {
			t.Errorf("a per-client revoke with %s ended %d and left %v", scope, ended, w.clients(t))
		}
	}

	t.Run("Issuer.Revoke", func(t *testing.T) {
		w := newScopeWorld(t)
		if ended, err := w.rig.iss.Revoke(context.Background(), ssoEmail); err != nil || ended != 3 {
			t.Errorf("Issuer.Revoke ended %d, %v; want every class", ended, err)
		}
	})

	t.Run("removal from the directory", func(t *testing.T) {
		w := newScopeWorld(t)
		w.rig.dir.standing[ssoEmail] = issuer.Standing{Found: false, Authoritative: true}

		if status, _ := refreshAgent(t, w.rig.server.URL, agentClient, "agent-token"); status == http.StatusOK {
			t.Error("an agent session refreshed for a person the directory no longer has")
		}
	})

	t.Run("one client for everybody", func(t *testing.T) {
		w := newScopeWorld(t)
		if ended := w.revoke(t, operator, &accessissuerv1.RevokeSessionsRequest{
			ClientId: agentClient, EveryIdentity: true, Scope: accessissuerv1.RevokeScope_REVOKE_SCOPE_INTERACTIVE,
		}); ended != 1 || w.clients(t)[agentClient] {
			t.Errorf("one client for everybody with a scope ended %d and left %v", ended, w.clients(t))
		}
	})
}
