package issuer_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// delegationPolicy is the demonstration policy with two backends a sidecar
// calls as the caller: `store`, which ada is admitted to, and `store-deploy`,
// which she is not. Both name the same actors; `aws:1111:power` names none.
// `store-own` acts only for tokens issued for `local-dev`, and `store-mcp`
// only for tokens issued for the MCP resource.
// One actor is limited to another client by its own exchange limit.
var delegationPolicy = strings.Replace(demo.Policy, "clients:\n",
	"clients:\n"+
		"  store:\n"+
		"    kind: exchange\n"+
		"    requires: [devel:k8s:viewer, mgmt:k8s:admin]\n"+
		"    delegation:\n"+
		"      actors:\n"+
		"        - service_account: {namespace: mcp, name: observability}\n"+
		"        - service_account: {namespace: mcp, name: second}\n"+
		"        - service_account: {namespace: mcp, name: limited}\n"+
		"  store-deploy:\n"+
		"    kind: exchange\n"+
		"    requires: [all:gitops:deployer]\n"+
		"    delegation:\n"+
		"      actors: [{service_account: {namespace: mcp, name: observability}}]\n"+
		"  store-own:\n"+
		"    kind: exchange\n"+
		"    requires: [devel:k8s:viewer, mgmt:k8s:admin]\n"+
		"    delegation:\n"+
		"      actors: [{service_account: {namespace: mcp, name: observability}}]\n"+
		"      subject_audiences: [local-dev]\n"+
		"  store-mcp:\n"+
		"    kind: exchange\n"+
		"    requires: [devel:k8s:viewer, mgmt:k8s:admin]\n"+
		"    delegation:\n"+
		"      actors: [{service_account: {namespace: mcp, name: observability}}]\n"+
		"      subject_audiences: ["+mcpResource+"]\n", 1) +
	"exchange_limits:\n" +
	"  - match: {service_account: {namespace: mcp, name: limited}}\n" +
	"    clients: [aws:1111:power]\n" +
	"resources:\n" +
	"  " + mcpResource + ": {requires: [devel:k8s:viewer]}\n"

// mcpResource is the MCP server a sidecar fronts: what a delegation's
// subject_audiences names so that only tokens issued for it are acted for.
const mcpResource = "https://mcp.example/store"

const (
	actorToken  = "k8s:mcp/observability"
	secondActor = "k8s:mcp/second"
)

var actorSubject = policy.ServiceAccountRef{Namespace: "mcp", Name: "observability"}.Subject()

func serveDelegation(t *testing.T, dir *fakeDirectory) (*httptest.Server, *issuer.Issuer, *audittest.Recorder) {
	t.Helper()
	declared, err := policy.Parse([]byte(delegationPolicy))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, dir, issuer.NewMemoryState())
	trail := audittest.New(t)
	iss.UseAudit(trail)
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	h, err := handler(iss, storage)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return server, iss, trail
}

// delegate posts one delegated exchange, presented by the sidecar's client.
func delegate(t *testing.T, server *httptest.Server, subject, subjectType, actor, audience string) (int, map[string]any) {
	t.Helper()
	form := url.Values{
		"grant_type":         {string(oidc.GrantTypeTokenExchange)},
		"subject_token":      {subject},
		"subject_token_type": {subjectType},
		"actor_token":        {actor},
		"actor_token_type":   {string(oidc.JWTTokenType)},
		"audience":           {audience},
	}
	return post(t, server, form, "local-dev")
}

// lastExchange is the data of the newest token exchange record.
func lastExchange(t *testing.T, trail *audittest.Recorder) map[string]any {
	t.Helper()
	records := trail.Find("roster.token.exchanged")
	if len(records) == 0 {
		t.Fatalf("no token exchange recorded: %v", trail.Actions())
	}
	return records[len(records)-1].GetData().AsMap()
}

// A sidecar holding a person's access token exchanges it, with its own
// ServiceAccount token as the actor, for the backend: the token is the
// person's, carries the groups the directory gives them NOW, and names the
// sidecar in `act`. The trail names both.
func TestADelegatedExchangeIsThePersonsWithTheActorInAct(t *testing.T) {
	t.Parallel()
	dir := &fakeDirectory{standing: map[string]issuer.Standing{
		"ada@north.example": {Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}},
	}}
	server, iss, trail := serveDelegation(t, dir)
	access, _ := signedInTokens(t, server, iss, "local-dev")["access_token"].(string)

	// The directory changes after the sign-in: the delegated token says what
	// it says now, not what the sign-in's token carried.
	dir.standing["ada@north.example"] = issuer.Standing{
		Found: true, Authoritative: true, Groups: []string{"engineering@north.example", "directory-admins@north.example"},
	}

	status, body := delegate(t, server, access, string(oidc.AccessTokenType), actorToken, "store")
	if status != http.StatusOK {
		t.Fatalf("delegate: %d %v", status, body)
	}
	raw, _ := body["access_token"].(string)
	claims := claimsOf(t, server, raw)

	if claims["sub"] != "ada@north.example" {
		t.Errorf("sub = %v, want the person", claims["sub"])
	}
	if aud, _ := claims["aud"].([]any); len(aud) != 1 || aud[0] != "store" {
		t.Errorf("aud = %v, want exactly the store", claims["aud"])
	}
	groups, _ := claims["groups"].([]any)
	if !slices.Contains(groups, any("mgmt:k8s:admin")) || !slices.Contains(groups, any("devel:k8s:viewer")) {
		t.Errorf("groups = %v, want the directory's current answer", groups)
	}
	act, _ := claims["act"].(map[string]any)
	if act["sub"] != actorSubject || act["act"] != nil {
		t.Errorf("act = %v, want {sub: %s}", claims["act"], actorSubject)
	}

	data := lastExchange(t, trail)
	if data["proof"] != "person" || data["acted_by"] != actorSubject || data["acted_by_proof"] != "workload" {
		t.Errorf("recorded %v, want the person acted for by the workload", data)
	}
	if got := trail.Find("roster.token.exchanged"); got[len(got)-1].GetActor().GetId() != "ada@north.example" {
		t.Errorf("actor = %v, want the person", got[len(got)-1].GetActor())
	}

	// A delegated token acted for again keeps the first actor beneath the
	// second, newest first.
	status, body = delegate(t, server, raw, string(oidc.AccessTokenType), secondActor, "store")
	if status != http.StatusOK {
		t.Fatalf("delegate a delegated token: %d %v", status, body)
	}
	again, _ := body["access_token"].(string)
	nested, _ := claimsOf(t, server, again)["act"].(map[string]any)
	prior, _ := nested["act"].(map[string]any)
	if nested["sub"] != (policy.ServiceAccountRef{Namespace: "mcp", Name: "second"}).Subject() || prior["sub"] != actorSubject {
		t.Errorf("act = %v, want the second actor over the first", nested)
	}
}

// Every delegation the target does not allow is refused, each for its own
// recorded reason, and the caller is told only that it is not for them.
func TestADelegationTheTargetDoesNotAllowIsRefused(t *testing.T) {
	t.Parallel()
	server, iss, trail := serveDelegation(t, adaDirectory())
	access, _ := signedInTokens(t, server, iss, "local-dev")["access_token"].(string)
	person := string(oidc.AccessTokenType)

	for name, tc := range map[string]struct {
		subject, subjectType, actor, audience string
		code, reason                          string
	}{
		"an actor the target does not name": {access, person, "k8s:mcp/other", "store", "invalid_target", "actor_not_admitted"},
		"a target that declares no delegation": {
			access, person, actorToken, "aws:1111:power", "invalid_target", "delegation_not_declared",
		},
		"a subject the target does not admit": {access, person, actorToken, "store-deploy", "invalid_target", "not_admitted"},
		"a CI job as the subject": {
			"github:example-org/gitops@refs/heads/master", string(oidc.JWTTokenType), actorToken, "store",
			"invalid_target", "subject_kind_not_allowed",
		},
		"an actor limited to another client": {access, person, "k8s:mcp/limited", "store", "invalid_target", "actor_exchange_limited"},
		"a subject token issued for another audience": {
			access, person, actorToken, "store-mcp", "invalid_target", "subject_audience_not_allowed",
		},
		"a person's own token as the actor": {access, person, access, "store", "invalid_grant", "actor_token_refused"},
	} {
		form := url.Values{
			"grant_type": {string(oidc.GrantTypeTokenExchange)}, "subject_token": {tc.subject},
			"subject_token_type": {tc.subjectType}, "actor_token": {tc.actor}, "audience": {tc.audience},
			"actor_token_type": {string(oidc.JWTTokenType)},
		}
		if tc.actor == access {
			form.Set("actor_token_type", person)
		}
		status, body := post(t, server, form, "local-dev")
		if status != http.StatusBadRequest || body["error"] != tc.code {
			t.Errorf("%s = %d %v, want 400 %s", name, status, body, tc.code)
			continue
		}
		if got := lastExchange(t, trail)["reason_code"]; got != tc.reason {
			t.Errorf("%s: reason_code = %v, want %s", name, got, tc.reason)
		}
	}
}

// subject_audiences narrows which of a person's tokens an actor may present:
// one issued for a listed audience is acted for, one issued for anything else
// is refused as the target's rule, recorded with the actor beside the person.
func TestADelegationActsOnlyForTokensIssuedForItsSubjectAudiences(t *testing.T) {
	t.Parallel()
	server, iss, trail := serveDelegation(t, adaDirectory())
	access, _ := signedInTokens(t, server, iss, "local-dev")["access_token"].(string)
	if aud, _ := claimsOf(t, server, access)["aud"].([]any); !slices.Contains(aud, any("local-dev")) {
		t.Fatalf("the subject token's aud = %v, want local-dev", aud)
	}

	status, body := delegate(t, server, access, string(oidc.AccessTokenType), actorToken, "store-own")
	if status != http.StatusOK {
		t.Fatalf("delegate a token issued for a listed audience: %d %v", status, body)
	}
	raw, _ := body["access_token"].(string)
	if act, _ := claimsOf(t, server, raw)["act"].(map[string]any); act["sub"] != actorSubject {
		t.Errorf("act = %v, want {sub: %s}", act, actorSubject)
	}

	status, body = delegate(t, server, access, string(oidc.AccessTokenType), actorToken, "store-mcp")
	if status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Fatalf("delegate a token issued for another audience = %d %v, want 400 invalid_target", status, body)
	}
	data := lastExchange(t, trail)
	if data["reason_code"] != "subject_audience_not_allowed" || data["acted_by"] != actorSubject || data["proof"] != "person" {
		t.Errorf("recorded %v, want subject_audience_not_allowed for the person, acted for by the workload", data)
	}
	if got := trail.Find("roster.token.exchanged"); got[len(got)-1].GetActor().GetId() != "ada@north.example" {
		t.Errorf("actor = %v, want the person", got[len(got)-1].GetActor())
	}

	// A delegated token is itself a subject token, issued for its own target:
	// presented again, it is read against that audience, not the first one's.
	status, body = delegate(t, server, raw, string(oidc.AccessTokenType), actorToken, "store-own")
	if status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Errorf("delegate a token issued for store-own at store-own = %d %v, want 400 invalid_target", status, body)
	}
}

// A sign-in that has ended is not acted for, though its access token has not
// expired, and the refusal names the actor.
func TestAnEndedSignInIsNotActedFor(t *testing.T) {
	t.Parallel()
	server, iss, trail := serveDelegation(t, adaDirectory())
	access, _ := signedInTokens(t, server, iss, "local-dev")["access_token"].(string)
	if _, err := iss.Sessions().Revoke(t.Context(), issuer.Query{Identity: "ada@north.example"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	status, body := delegate(t, server, access, string(oidc.AccessTokenType), actorToken, "store")
	if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("delegate an ended sign-in = %d %v, want 400 invalid_grant", status, body)
	}
	data := lastExchange(t, trail)
	if data["reason_code"] != "session_ended" || data["acted_by"] != actorSubject {
		t.Errorf("recorded %v, want session_ended acted for by the workload", data)
	}
}

// An actor_token and its type travel together: either alone is malformed.
func TestAnActorHalfNamedIsMalformed(t *testing.T) {
	t.Parallel()
	server, iss, _ := serveDelegation(t, adaDirectory())
	access, _ := signedInTokens(t, server, iss, "local-dev")["access_token"].(string)
	base := url.Values{
		"grant_type": {string(oidc.GrantTypeTokenExchange)}, "subject_token": {access},
		"subject_token_type": {string(oidc.AccessTokenType)}, "audience": {"store"},
	}
	typeOnly, tokenOnly := url.Values{}, url.Values{}
	for k, v := range base {
		typeOnly[k], tokenOnly[k] = v, v
	}
	typeOnly.Set("actor_token_type", string(oidc.JWTTokenType))
	tokenOnly.Set("actor_token", actorToken)
	for name, form := range map[string]url.Values{"a type alone": typeOnly, "a token alone": tokenOnly} {
		if status, body := post(t, server, form, "local-dev"); status != http.StatusBadRequest || body["error"] != "invalid_request" {
			t.Errorf("%s = %d %v, want 400 invalid_request", name, status, body)
		}
	}
}

// A delegation is refused at load when it names no actor, names a person as
// one, names a subject kind there is not, or lists subject audiences that
// are empty, repeated, or neither a resource nor a client.
func TestADelegationIsCheckedAtLoad(t *testing.T) {
	t.Parallel()
	for name, rule := range map[string]string{
		"no actors":                      "{actors: []}",
		"a person":                       "{actors: [{email: ada@north.example}]}",
		"an unknown kind":                "{actors: [{service_account: {namespace: mcp, name: a}}], subjects: [robot]}",
		"a repeated actor":               "{actors: [{service_account: {namespace: mcp, name: a}}, {service_account: {namespace: mcp, name: a}}]}",
		"an empty subject audience list": "{actors: [{service_account: {namespace: mcp, name: a}}], subject_audiences: []}",
		"an unknown subject audience":    "{actors: [{service_account: {namespace: mcp, name: a}}], subject_audiences: [nowhere]}",
		"a repeated subject audience":    "{actors: [{service_account: {namespace: mcp, name: a}}], subject_audiences: [local-dev, local-dev]}",
	} {
		doc := strings.Replace(demo.Policy, "clients:\n",
			"clients:\n  store: {kind: exchange, requires: [devel:k8s:viewer], delegation: "+rule+"}\n", 1)
		declared, err := policy.Parse([]byte(doc))
		if err == nil {
			_, err = policy.NewSet(declared)
		}
		if err == nil || !strings.Contains(err.Error(), "delegation") {
			t.Errorf("%s: err = %v, want a delegation refusal", name, err)
		}
		if strings.Contains(rule, "subject_audiences") && (err == nil || !strings.Contains(err.Error(), "subject_audiences")) {
			t.Errorf("%s: err = %v, want a subject_audiences refusal", name, err)
		}
	}

	// A list naming a resource and a client of the policy loads.
	doc := strings.Replace(demo.Policy, "clients:\n", "clients:\n  store: {kind: exchange, requires: [devel:k8s:viewer], delegation: "+
		"{actors: [{service_account: {namespace: mcp, name: a}}], subject_audiences: [local-dev, "+mcpResource+"]}}\n", 1) +
		"resources:\n  " + mcpResource + ": {requires: [devel:k8s:viewer]}\n"
	declared, err := policy.Parse([]byte(doc))
	if err == nil {
		_, err = policy.NewSet(declared)
	}
	if err != nil {
		t.Errorf("subject_audiences naming a resource and a client: %v, want it to load", err)
	}
}
