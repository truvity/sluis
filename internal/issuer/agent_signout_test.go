package issuer_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// Sign-out's three modes (docs/decisions/0040-agent-class-sessions.md,
// decision 7): the person's own sign-out spares their agent-class
// sessions, the sign-in's own absolute limit spares every live chain, and
// another person signing in in the same browser spares nothing. A spared
// session is not revoked, not announced, and stays filed under the ended
// sign-in; every revocation ends it like any other.

// openAgent files an agent-class session under a sign-in, the way a code
// redeemed after the consent page does.
func (g *ssoRig) openAgent(t *testing.T, identity, client, signIn, token string, authTime time.Time) issuer.Session {
	t.Helper()

	session, err := g.iss.Sessions().Record(context.Background(), issuer.Opened{
		Identity: identity, ClientID: client, How: issuer.HowCode, Token: token, SSO: signIn,
		AuthTime: authTime, Class: issuer.ClassAgent, Scopes: []string{"openid", "offline_access"},
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if !session.Agent() {
		t.Fatalf("recorded %+v, want an agent-class session", session)
	}

	return session
}

// listed is the identity's live sessions by client.
func (g *ssoRig) listed(t *testing.T, identity string) map[string]issuer.Session {
	t.Helper()

	sessions, err := g.iss.Sessions().List(context.Background(), issuer.Query{Identity: identity})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	out := map[string]issuer.Session{}
	for _, session := range sessions {
		out[session.ClientID] = session
	}

	return out
}

// refreshAgent renews an agent chain at /token, as its host does.
func refreshAgent(t *testing.T, serverURL, client, token string) (int, map[string]any) {
	t.Helper()

	return tokenAnswer(t, serverURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {token},
		"client_id":     {client},
		"scope":         {"openid"},
	})
}

// Own sign-out: the interactive sessions end and their clients are told,
// the agent session keeps running -- it is still listed under the ended
// sign-in, it refreshes, its client is told nothing, not even the
// subject-only logout token -- and the record names what it spared.
func TestOwnSignOutKeepsAnAgentSessionRefreshing(t *testing.T) {
	t.Parallel()

	rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
	b, id := rig.signedInBrowser(t)
	ctx := context.Background()

	rig.open(t, ssoEmail, "argocd", id)
	rig.openAgent(t, ssoEmail, agentClient, id, "agent-token", time.Now())

	// Both clients also signed in with `openid` alone under this browser:
	// grafana is owed the subject-only logout token, the agent client is
	// not, since it holds a session that keeps running.
	for _, client := range []string{"grafana", agentClient} {
		if err := rig.iss.SSO().Involve(ctx, id, client); err != nil {
			t.Fatal(err)
		}
	}

	if status, where, _ := b.do(http.MethodGet, "/logout"); status != http.StatusFound || where != "/signed-out" {
		t.Fatalf("/logout: %d to %q", status, where)
	}

	left := rig.listed(t, ssoEmail)
	if _, kept := left["argocd"]; kept {
		t.Error("the interactive session survived the sign-out")
	}

	agent, kept := left[agentClient]
	if !kept {
		t.Fatalf("the agent session was ended by the person's own sign-out: %+v", left)
	}

	if agent.SSO != id {
		t.Errorf("the spared session is filed under %q, want the ended sign-in %q", agent.SSO, id)
	}

	if status, body := refreshAgent(t, rig.server.URL, agentClient, "agent-token"); status != http.StatusOK || body["refresh_token"] == nil {
		t.Errorf("the spared agent session would not refresh: %d %v", status, body)
	}

	for _, client := range []string{"argocd", "grafana"} {
		if !rig.announcedTo(client) {
			t.Errorf("%s was not told: %+v", client, rig.told())
		}
	}

	if rig.announcedTo(agentClient) {
		t.Errorf("the spared session's client was told it ended: %+v", rig.told())
	}

	ended := rig.trail.Find("roster.session.ended")
	if len(ended) != 1 {
		t.Fatalf("%d roster.session.ended records: %v", len(ended), rig.trail.Actions())
	}

	if got := fieldOf(ended[0], "ended"); got != float64(1) {
		t.Errorf("ended = %v, want 1", got)
	}

	if got, _ := fieldOf(ended[0], "spared").([]any); len(got) != 1 || got[0] != agentClient {
		t.Errorf("spared = %v, want [%s]", fieldOf(ended[0], "spared"), agentClient)
	}

	// The page says so, and offers the way to end them too.
	_, _, page := b.do(http.MethodGet, "/signed-out")
	if !strings.Contains(page, "Agent connections were kept") || !strings.Contains(page, `href="/account"`) {
		t.Errorf("the signed-out page does not say agent connections were kept and link to sign out everywhere: %s", page)
	}
}

// `/end_session` is the person's own sign-out too, and spares the agents --
// except a client the request names, by `client_id` or by the audience of
// its `id_token_hint`: that client is signing itself out, and a name only
// ever ends more.
func TestEndSessionSparesAgentsExceptTheClientItNames(t *testing.T) {
	t.Parallel()

	t.Run("naming nobody", func(t *testing.T) {
		t.Parallel()

		rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
		b, id := rig.signedInBrowser(t)
		rig.openAgent(t, ssoEmail, agentClient, id, "agent-token", time.Now())

		b.do(http.MethodGet, "/end_session")

		if _, kept := rig.listed(t, ssoEmail)[agentClient]; !kept {
			t.Error("end_session naming no client ended the agent session")
		}
	})

	t.Run("by client_id", func(t *testing.T) {
		t.Parallel()

		rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
		b, id := rig.signedInBrowser(t)
		rig.openAgent(t, ssoEmail, agentClient, id, "agent-token", time.Now())
		rig.openAgent(t, ssoEmail, "another-agent", id, "other-token", time.Now())

		if status, _, body := b.do(http.MethodGet, "/end_session?client_id="+agentClient); status >= http.StatusBadRequest {
			t.Fatalf("end_session: %d %s", status, body)
		}

		left := rig.listed(t, ssoEmail)
		if _, kept := left[agentClient]; kept {
			t.Error("end_session naming the agent client spared its session")
		}
		if _, kept := left["another-agent"]; !kept {
			t.Error("end_session naming one agent client ended another's session")
		}
		if !rig.announcedTo(agentClient) {
			t.Error("the named agent client was not told its session ended")
		}
	})

	t.Run("by the id_token_hint's audience", func(t *testing.T) {
		t.Parallel()

		rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
		b, _ := rig.signedInBrowser(t)

		// A real ID token for the agent client, by way of its consent page.
		request, _, _, page := agentAuthorize(b, "")
		_, where, _, _ := accept(b, tokenOn(t, page))
		tokens := redeemFor(t, b, where, agentClient)

		hint, _ := tokens["id_token"].(string)
		if hint == "" || !rig.listed(t, ssoEmail)[agentClient].Agent() {
			t.Fatalf("request %s opened no agent session: %v", request, tokens)
		}

		if status, _, body := b.do(http.MethodGet, "/end_session?id_token_hint="+url.QueryEscape(hint)); status >= http.StatusBadRequest {
			t.Fatalf("end_session: %d %s", status, body)
		}

		if _, kept := rig.listed(t, ssoEmail)[agentClient]; kept {
			t.Error("end_session with the agent client's id_token_hint spared its session")
		}
	})
}

// The sign-in's own limit: past it, the silent completion ends the
// browser's sign-in and spares every live chain, agent or not; only the
// clients without a refresh token are told, and no sign-out is recorded.
func TestASignInPastItsLimitSparesEveryLiveChain(t *testing.T) {
	t.Parallel()

	rig := newSSORigWith(t, issuer.Config{AbsoluteLifetime: time.Hour}, consentPolicy())
	ctx := context.Background()

	past := time.Now().Add(-2 * time.Hour)
	rig.iss.SSO().SetClock(func() time.Time { return past })
	b, id := rig.signedInBrowser(t)
	rig.iss.SSO().SetClock(time.Now)

	rig.openAgent(t, ssoEmail, agentClient, id, "agent-token", past)
	rig.open(t, ssoEmail, "argocd", id) // live: no auth_time to hold it to
	if err := rig.iss.SSO().Involve(ctx, id, "grafana"); err != nil {
		t.Fatal(err)
	}

	if where := b.authorize(""); !strings.Contains(where, "/login/google/start") {
		t.Fatalf("authorize past the limit went to %q, want the provider", where)
	}

	if _, live, _ := rig.iss.SSO().Get(ctx, id); live {
		t.Fatal("the sign-in past its limit was not ended")
	}

	left := rig.listed(t, ssoEmail)
	for _, client := range []string{agentClient, "argocd"} {
		if _, kept := left[client]; !kept {
			t.Errorf("the live %s chain was ended at the sign-in's limit", client)
		}
		if rig.announcedTo(client) {
			t.Errorf("%s was told its live session ended", client)
		}
	}

	if !rig.announcedTo("grafana") {
		t.Error("the openid-only client was not told the sign-in ended")
	}

	if n := len(rig.trail.Find("roster.session.ended")); n != 0 {
		t.Errorf("%d sign-outs recorded for a sign-in that reached its limit", n)
	}
}

// Another person signing in in the same browser: the first person's
// sessions end, agent-class included, and their clients are told -- the
// browser holds nothing that could end them any more.
func TestAnotherPersonsSignInSparesNothing(t *testing.T) {
	t.Parallel()

	rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
	b, id := rig.signedInBrowser(t)

	rig.open(t, ssoEmail, "argocd", id)
	rig.openAgent(t, ssoEmail, agentClient, id, "agent-token", time.Now())

	rig.as(ssoBob)

	if status := reauthenticate(t, b); status != http.StatusFound {
		t.Fatalf("callback: %d", status)
	}

	if left := rig.listed(t, ssoEmail); len(left) != 0 {
		t.Errorf("the first person's sessions survived another person's sign-in: %+v", left)
	}

	for _, client := range []string{"argocd", agentClient} {
		if !rig.announcedTo(client) {
			t.Errorf("%s was not told: %+v", client, rig.told())
		}
	}

	revoked := rig.trail.Find("roster.session.revoked")
	if len(revoked) != 1 || fieldOf(revoked[0], "ended") != float64(2) || fieldOf(revoked[0], "scope") != scopeReplaced {
		t.Errorf("revoked records = %v, want one ending both sessions as %s", revoked, scopeReplaced)
	}
}

// Every revoke ends an agent session as it ends any other: sign out
// everywhere, the console's per-client revoke, and revoking the browser a
// sign-out spared it under. Nothing that revokes asks a session's class.
func TestEveryRevokeEndsAnAgentSession(t *testing.T) {
	t.Parallel()

	for name, request := range map[string]func(signIn string) *accessissuerv1.RevokeSessionsRequest{
		"sign out everywhere": func(string) *accessissuerv1.RevokeSessionsRequest {
			return &accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail}
		},
		"one client": func(string) *accessissuerv1.RevokeSessionsRequest {
			return &accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail, ClientId: agentClient}
		},
		"the browser a sign-out spared it under": func(signIn string) *accessissuerv1.RevokeSessionsRequest {
			return &accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail, Sso: signIn}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
			b, id := rig.signedInBrowser(t)
			rig.open(t, ssoEmail, "argocd", id)
			rig.openAgent(t, ssoEmail, agentClient, id, "agent-token", time.Now())

			// Signed out first: what is left is the spared agent session,
			// filed under a sign-in that has ended.
			b.do(http.MethodGet, "/logout")

			if _, kept := rig.listed(t, ssoEmail)[agentClient]; !kept {
				t.Fatal("the sign-out did not spare the agent session")
			}

			ended, err := revoke(t, rig.service(), ssoEmail+"|", request(id))
			if err != nil || ended != 1 {
				t.Fatalf("ended %d, %v; want the agent session", ended, err)
			}

			if left := rig.listed(t, ssoEmail); len(left) != 0 {
				t.Errorf("left %+v", left)
			}

			if status, _ := refreshAgent(t, rig.server.URL, agentClient, "agent-token"); status == http.StatusOK {
				t.Error("a revoked agent session still refreshes")
			}
		})
	}

	// And before any sign-out, sign out everywhere ends both classes.
	rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
	_, id := rig.signedInBrowser(t)
	rig.open(t, ssoEmail, "argocd", id)
	rig.openAgent(t, ssoEmail, agentClient, id, "agent-token", time.Now())

	ended, err := revoke(t, rig.service(), ssoEmail+"|", &accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail})
	if err != nil || ended != 2 {
		t.Fatalf("sign out everywhere ended %d, %v; want both classes", ended, err)
	}

	if !slices.Equal(sortedKeys(rig.listed(t, ssoEmail)), nil) {
		t.Error("sign out everywhere left a session")
	}
}

func sortedKeys(m map[string]issuer.Session) []string {
	var out []string
	for key := range m {
		out = append(out, key)
	}

	slices.Sort(out)

	return out
}

// The operator's lever for a client whose tokens are in doubt: one client
// ended for every identity, agent sessions included, recorded -- and
// nobody else's to pull, nor one that can be pulled by leaving the
// identity out by accident.
func TestTheOperatorEndsOneClientForEveryIdentity(t *testing.T) {
	t.Parallel()

	rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
	_, id := rig.signedInBrowser(t)
	rig.open(t, ssoEmail, "argocd", id)
	rig.openAgent(t, ssoEmail, agentClient, id, "ada-agent", time.Now())
	rig.openAgent(t, ssoBob, agentClient, "", "bob-agent", time.Now())

	svc := rig.service()
	operator := "op@north.example|" + policy.GroupOperators
	everybody := &accessissuerv1.RevokeSessionsRequest{ClientId: agentClient, EveryIdentity: true}

	for name, tc := range map[string]struct {
		as      string
		request *accessissuerv1.RevokeSessionsRequest
		code    connect.Code
	}{
		"by a person":           {ssoEmail + "|", everybody, connect.CodePermissionDenied},
		"naming no client":      {operator, &accessissuerv1.RevokeSessionsRequest{EveryIdentity: true}, connect.CodeInvalidArgument},
		"naming an identity":    {operator, &accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail, ClientId: agentClient, EveryIdentity: true}, connect.CodeInvalidArgument},
		"without every_identity": {operator, &accessissuerv1.RevokeSessionsRequest{ClientId: agentClient}, connect.CodeInvalidArgument},
	} {
		if _, err := revoke(t, svc, tc.as, tc.request); connect.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
	}

	if n := len(rig.listed(t, ssoEmail)) + len(rig.listed(t, ssoBob)); n != 3 {
		t.Fatalf("a refused revoke ended something: %d sessions left of 3", n)
	}

	ended, err := revoke(t, svc, operator, everybody)
	if err != nil || ended != 2 {
		t.Fatalf("ended %d, %v; want the agent client's 2 sessions", ended, err)
	}

	if _, kept := rig.listed(t, ssoEmail)["argocd"]; !kept || len(rig.listed(t, ssoEmail)) != 1 || len(rig.listed(t, ssoBob)) != 0 {
		t.Errorf("left %v and %v, want only ada's argocd", rig.listed(t, ssoEmail), rig.listed(t, ssoBob))
	}

	for _, token := range []string{"ada-agent", "bob-agent"} {
		if status, _ := refreshAgent(t, rig.server.URL, agentClient, token); status == http.StatusOK {
			t.Errorf("%s still refreshes", token)
		}
	}

	revoked := rig.trail.Find("roster.session.revoked")
	if len(revoked) != 1 || revoked[0].GetActor().GetId() != "op@north.example" ||
		fieldOf(revoked[0], "scope") != issuer.ScopeClientEverywhere || fieldOf(revoked[0], "ended") != float64(2) ||
		len(revoked[0].GetTargets()) != 1 || revoked[0].GetTargets()[0].GetId() != agentClient {
		t.Errorf("revoked records = %v, want the operator ending %s everywhere", revoked, agentClient)
	}
}

// The console's listing shows each session's class and the deadline it is
// held to beside its sliding expiry, and keeps an agent session a sign-out
// spared under the sign-in id that ended, which is no longer among the
// listing's sign-ins: the console groups it as an ended sign-in.
func TestTheListingShowsClassAndDeadline(t *testing.T) {
	t.Parallel()

	rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
	b, id := rig.signedInBrowser(t)
	authTime := time.Now().Truncate(time.Second)

	agent := rig.openAgent(t, ssoEmail, agentClient, id, "agent-token", authTime)
	if _, err := rig.iss.Sessions().Record(context.Background(), issuer.Opened{
		Identity: ssoEmail, ClientID: "argocd", How: issuer.HowCode, Token: "argocd-token", SSO: id, AuthTime: authTime,
	}); err != nil {
		t.Fatal(err)
	}

	b.do(http.MethodGet, "/logout")
	_, other := rig.signedInBrowser(t)

	got, err := list(t, rig.service(), ssoEmail+"|", &accessissuerv1.ListSessionsRequest{Identity: ssoEmail})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.GetSessions()) != 1 {
		t.Fatalf("listed %v, want the spared agent session", got.GetSessions())
	}

	row := got.GetSessions()[0]
	if row.GetSessionClass() != accessissuerv1.SessionClass_SESSION_CLASS_AGENT {
		t.Errorf("class = %s, want agent", row.GetSessionClass())
	}
	if !row.GetDeadline().AsTime().Equal(agent.Deadline) || !agent.Deadline.Equal(authTime.Add(30*day)) {
		t.Errorf("deadline = %s, want %s", row.GetDeadline().AsTime(), authTime.Add(30*day))
	}
	if row.GetSso() != id {
		t.Errorf("sso = %q, want the ended sign-in %q", row.GetSso(), id)
	}

	for _, signIn := range got.GetSignIns() {
		if signIn.GetId() == id {
			t.Error("the ended sign-in is listed as signed in")
		}
	}
	if len(got.GetSignIns()) != 1 || got.GetSignIns()[0].GetId() != other {
		t.Errorf("sign-ins = %v, want only the live one", got.GetSignIns())
	}

	// An interactive session, listed before any sign-out, carries its class
	// and the installation's absolute limit.
	rig2 := newSSORigWith(t, issuer.Config{}, consentPolicy())
	_, id2 := rig2.signedInBrowser(t)
	if _, err = rig2.iss.Sessions().Record(context.Background(), issuer.Opened{
		Identity: ssoEmail, ClientID: "argocd", How: issuer.HowCode, Token: "argocd-token", SSO: id2, AuthTime: authTime,
	}); err != nil {
		t.Fatal(err)
	}

	got, err = list(t, rig2.service(), ssoEmail+"|", &accessissuerv1.ListSessionsRequest{Identity: ssoEmail})
	if err != nil || len(got.GetSessions()) != 1 {
		t.Fatalf("listed %v, %v", got.GetSessions(), err)
	}

	row = got.GetSessions()[0]
	if row.GetSessionClass() != accessissuerv1.SessionClass_SESSION_CLASS_INTERACTIVE ||
		!row.GetDeadline().AsTime().Equal(authTime.Add(issuer.DefaultAbsoluteLifetime)) {
		t.Errorf("class %s deadline %s, want interactive and %s", row.GetSessionClass(), row.GetDeadline().AsTime(),
			authTime.Add(issuer.DefaultAbsoluteLifetime))
	}
}
