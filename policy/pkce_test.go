package policy_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
)

// A public client is always PKCE-required, a confidential one only when it
// says so, and the access document's spelling carries the same field.
func TestPKCEPolicyReadsBack(t *testing.T) {
	t.Parallel()

	declared, err := parseSessionPolicy(t, "clients:\n"+
		"  strict: { kind: confidential, secret: s1, redirects: ['https://a.example/cb'], requires: [a], pkce: required }\n"+
		"  explicit: { kind: confidential, secret: s2, redirects: ['https://b.example/cb'], requires: [a], pkce: optional }\n"+
		"  plain: { kind: confidential, secret: s3, redirects: ['https://c.example/cb'], requires: [a] }\n"+
		"  cli: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a] }\n"+
		"  cli-required: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], pkce: required }\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for name, want := range map[string]bool{
		"strict": true, "explicit": false, "plain": false, "cli": true, "cli-required": true,
	} {
		if got := declared.Clients[name].PKCERequired(); got != want {
			t.Errorf("%s: PKCERequired = %t, want %t", name, got, want)
		}
	}

	access, err := policy.ParseAccess([]byte("version: 1\n" +
		"access:\n" +
		"  groups: [{ name: a, members: [g@h.example] }]\n" +
		"  clients:\n" +
		"    - { name: strict, kind: confidential, secret: s1, redirects: ['https://a.example/cb'], requires: [a], pkce: required }\n"))
	if err != nil {
		t.Fatalf("parse access: %v", err)
	}
	if got := access.Clients["strict"].PKCE; got != policy.PKCERequired {
		t.Errorf("the access document carried pkce %q, want %q", got, policy.PKCERequired)
	}
}

// A client is a harness's by saying so, and the access document's spelling
// carries it. An exchange client has no sign-in for a harness to use.
func TestHarnessClientsAreMarkedAndRefusedOnExchange(t *testing.T) {
	t.Parallel()

	declared, err := parseSessionPolicy(t, "clients:\n"+
		"  rp: { kind: confidential, secret: s1, redirects: ['https://a.example/cb'], requires: [a], harness: true }\n"+
		"  app: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a] }\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !declared.Clients["rp"].Harness || declared.Clients["app"].Harness {
		t.Errorf("harness: rp %v, app %v", declared.Clients["rp"].Harness, declared.Clients["app"].Harness)
	}
	access, err := policy.ParseAccess([]byte("version: 1\n" +
		"access:\n" +
		"  groups: [{ name: a, members: [g@h.example] }]\n" +
		"  clients:\n" +
		"    - { name: rp, kind: confidential, secret: s1, redirects: ['https://a.example/cb'], requires: [a], harness: true }\n"))
	if err != nil {
		t.Fatalf("parse access: %v", err)
	}
	if !access.Clients["rp"].Harness {
		t.Error("the access document dropped harness")
	}
	if _, err = parseSessionPolicy(t, "clients:\n  x: { kind: exchange, requires: [a], harness: true }\n"); err == nil ||
		!strings.Contains(err.Error(), "harness") {
		t.Errorf("harness on an exchange client = %v, want a refusal naming it", err)
	}
}

// `optional` on a public client, an unknown value, and any value on an
// exchange client are refused at load.
func TestPKCEPolicyIsRefusedWhereItCannotApply(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, rest, want string }{
		{
			"optional on a public client",
			"clients:\n  c: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], pkce: optional }\n",
			"public and declares pkce: optional",
		},
		{
			"an unknown value",
			"clients:\n  c: { kind: confidential, secret: s, redirects: ['https://a.example/cb'], requires: [a], pkce: always }\n",
			`pkce "always" is not "required" or "optional"`,
		},
		{
			"required on an exchange client",
			"clients:\n  c: { kind: exchange, requires: [a], pkce: required }\n",
			"exchange target and declares pkce",
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

// A policy that declares no `pkce` keeps its digest; one that does has
// another, so a rollout in the middle of the change is told apart.
func TestPKCEPolicyIsPartOfTheDigest(t *testing.T) {
	t.Parallel()

	const client = "clients:\n  c: { kind: confidential, secret: s, redirects: ['https://a.example/cb'], requires: [a]%s }\n"

	digest := func(extra string) string {
		t.Helper()

		declared, err := parseSessionPolicy(t, strings.Replace(client, "%s", extra, 1))
		if err != nil {
			t.Fatal(err)
		}
		d, err := declared.Digest()
		if err != nil {
			t.Fatal(err)
		}

		return d
	}

	if digest("") == digest(", pkce: required") {
		t.Error("declaring pkce: required did not change the digest")
	}
	if digest(", pkce: optional") == digest(", pkce: required") {
		t.Error("pkce: optional and pkce: required share a digest")
	}
}
