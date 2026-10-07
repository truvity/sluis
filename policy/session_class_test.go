package policy_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/policy"
)

// sessionBase is the smallest policy a client row can be validated in.
const sessionBase = "version: 1\n" +
	"groups: { a: { members: [g@h.example] } }\n"

func parseSessionPolicy(t *testing.T, rest string) (policy.Policy, error) {
	t.Helper()

	declared, err := policy.Parse([]byte(sessionBase + rest))
	if err != nil {
		return policy.Policy{}, err
	}

	return declared, declared.Validate()
}

// `session` on a declared client and once on client_documents: agent or
// interactive read back as written, and absent is interactive.
func TestSessionClassReadsBack(t *testing.T) {
	t.Parallel()

	declared, err := parseSessionPolicy(t, "clients:\n"+
		"  mcp-host: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], session: agent }\n"+
		"  console: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], session: interactive }\n"+
		"  plain: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a] }\n"+
		"client_documents: { origins: [hosts.example], requires: [a], session: agent }\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if !declared.Clients["mcp-host"].Agent() {
		t.Error("session: agent did not read back as agent")
	}
	if declared.Clients["console"].Agent() || declared.Clients["plain"].Agent() {
		t.Error("an interactive or absent session read back as agent")
	}
	if !declared.ClientDocuments.Agent() {
		t.Error("client_documents session: agent did not read back as agent")
	}
}

// What is refused at load: an unknown class, `session` on an exchange
// client (any value: an exchange opens no chain with an auth_time), agent
// together with sign_in_exchange, and a session on client_documents that
// names no origin.
func TestSessionClassRefusals(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		rest string
		want string
	}{
		{
			"an unknown class",
			"clients:\n  c: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], session: robot }\n",
			`session "robot" is not "interactive" or "agent"`,
		},
		{
			"agent on an exchange client",
			"clients:\n  c: { kind: exchange, requires: [a], session: agent }\n",
			"exchange target and declares session",
		},
		{
			"interactive on an exchange client",
			"clients:\n  c: { kind: exchange, requires: [a], session: interactive }\n",
			"exchange target and declares session",
		},
		{
			"agent with sign_in_exchange",
			"clients:\n  c: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], sign_in_exchange: true, session: agent }\n",
			"session: agent and sign_in_exchange: true",
		},
		{
			"an unknown class on client_documents",
			"client_documents: { origins: [hosts.example], requires: [a], session: robot }\n",
			`client_documents: session "robot"`,
		},
		{
			"a session on client_documents with no origins",
			"client_documents: { session: agent }\n",
			"declares requires, ttl_cap, groups or session and no origins",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseSessionPolicy(t, tc.rest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// An interactive client may still trade its sign-in: only agent is refused
// beside sign_in_exchange.
func TestAnInteractiveClientMayTradeItsSignIn(t *testing.T) {
	t.Parallel()

	if _, err := parseSessionPolicy(t, "clients:\n"+
		"  cli: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], sign_in_exchange: true, session: interactive }\n"); err != nil {
		t.Fatalf("an interactive CLI with sign_in_exchange: %v, want accepted", err)
	}
}

// The access document's spelling carries `session` on a client and on
// clientDocuments into the layer it stands for.
func TestTheAccessDocumentCarriesTheSessionClass(t *testing.T) {
	t.Parallel()

	declared, err := policy.ParseAccess([]byte("version: 1\n" +
		"access:\n" +
		"  groups: [{ name: a, members: [g@h.example] }]\n" +
		"  clients:\n" +
		"    - { name: mcp-host, kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], session: agent }\n" +
		"  clientDocuments: { origins: [hosts.example], requires: [a], session: agent }\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err = declared.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !declared.Clients["mcp-host"].Agent() {
		t.Error("the access document's client session did not reach the layer")
	}
	if !declared.ClientDocuments.Agent() {
		t.Error("the access document's clientDocuments session did not reach the layer")
	}
}

// For an agent chain a resource's absolute_cap is only ever a ceiling: it
// shortens the class's limit, never lengthens it, read-only or not; the
// client's own audience and an undeclared resource leave the class's limit.
func TestAgentAbsoluteIsShortenedByACapAndNeverLengthened(t *testing.T) {
	t.Parallel()

	declared, err := parseResources(t,
		"  'https://week.example/': { requires: [a], read_only: true, absolute_cap: 168h }\n"+
			"  'https://hour.example/': { requires: [a], absolute_cap: 1h }\n"+
			"  'https://plain.example/': { requires: [a] }\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	class := 720 * time.Hour
	for resource, want := range map[string]time.Duration{
		"":                           class,
		"https://plain.example/":     class,
		"https://not-declared.test/": class,
		"https://week.example/":      168 * time.Hour,
		"https://hour.example/":      time.Hour,
	} {
		if got := set.AgentAbsolute(class, []string{resource}); got != want {
			t.Errorf("AgentAbsolute(%q) = %s, want %s", resource, got, want)
		}
	}

	// A class shorter than a cap is not lengthened by it.
	if got := set.AgentAbsolute(24*time.Hour, []string{"https://week.example/"}); got != 24*time.Hour {
		t.Errorf("a 24h class with a 168h cap = %s, want 24h", got)
	}
}

// The rows the service warns about at start: an agent client that also
// describes a browser-facing application, and a read-only resource that
// still lengthens the installation's absolute limit (deprecated).
func TestTheRowsWarnedAboutAtStart(t *testing.T) {
	t.Parallel()

	declared, err := parseSessionPolicy(t, "clients:\n"+
		"  agent-signed-out: { kind: public, redirects: ['http://127.0.0.1/cb'], signed_out: ['http://127.0.0.1/'], requires: [a], session: agent }\n"+
		"  agent-backchannel: { kind: confidential, secret: s, redirects: ['https://a.example/cb'], backchannel_logout_uri: 'https://a.example/bc', requires: [a], session: agent }\n"+
		"  agent-plain: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], session: agent }\n"+
		"  browser: { kind: public, redirects: ['http://127.0.0.1/cb'], signed_out: ['http://127.0.0.1/'], requires: [a] }\n"+
		"resources:\n"+
		"  'https://week.example/': { requires: [a], read_only: true, absolute_cap: 168h }\n"+
		"  'https://short.example/': { requires: [a], read_only: true, absolute_cap: 12h }\n"+
		"  'https://plain.example/': { requires: [a] }\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := set.BrowserFacingAgents(), []string{"agent-backchannel", "agent-signed-out"}; !slices.Equal(got, want) {
		t.Errorf("BrowserFacingAgents = %v, want %v", got, want)
	}

	for id, want := range map[string]bool{
		"https://week.example/":  true,
		"https://short.example/": false,
		"https://plain.example/": false,
	} {
		if got := declared.Resources[id].LengthensAbsolute(24 * time.Hour); got != want {
			t.Errorf("LengthensAbsolute(%q) = %v, want %v", id, got, want)
		}
	}
}
