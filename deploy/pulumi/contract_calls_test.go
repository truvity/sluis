package sluispulumi_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// The module-call contract. The routing table (arp.ModuleCalls) says who calls
// whom, through which alias, and with which proof; what the library creates and
// grants must follow it, whatever the transport. Each row of the table is held
// to three things:
//
//  1. the callee has the alias the caller targets;
//  2. the caller's role may lambda:InvokeFunction on that qualified alias;
//  3. a caller that proves itself with its web identity token holds
//     sts:GetWebIdentityToken, scoped as the issuer's own statement is.
//
// The table is read, not copied: a call added to it is held to the contract at
// once, and a function that makes a call the table does not name is a bug in
// the table (the rc.3 field failures were both: a github pass that could not
// ask STS for a token, and a console page whose alias did not exist).

const contractAudience = "https://access.example.test/console"

// contractPolicy is the statements of the role's policy.
func contractPolicy(t *testing.T, rec *recorder, role string) []map[string]any {
	t.Helper()
	return statements(t, prop(rec.one(t, policyType, "staging-"+role+"-policy"), "policy").StringValue())
}

func contractAllows(st []map[string]any, action, resource string) bool {
	for _, s := range st {
		if s["Effect"] == "Allow" && slices.Contains(strs(s["Action"]), action) && slices.Contains(strs(s["Resource"]), resource) {
			return true
		}
	}
	return false
}

func webIdentityStatements(st []map[string]any) []map[string]any {
	var out []map[string]any
	for _, s := range st {
		if s["Effect"] == "Allow" && slices.Contains(strs(s["Action"]), "sts:GetWebIdentityToken") {
			out = append(out, s)
		}
	}
	return out
}

func TestTheRoutingTableIsHeldByTheLibrary(t *testing.T) {
	// The Outline function is made: every callee of the table exists.
	rec, _ := mustLambda(t, estateWith(withOutlineDocument, func(a *arp.LambdaArgs) { a.WebIdentityAudience = contractAudience }))
	backup, _ := mustBackup(t, nil)
	calls := arp.ModuleCalls()
	if len(calls) < 10 {
		t.Fatalf("the routing table has %d rows: a shrunk table proves nothing", len(calls))
	}
	issuerWeb := webIdentityStatements(contractPolicy(t, rec, "issuer"))
	if len(issuerWeb) != 1 {
		t.Fatalf("the issuer holds %d web identity statements", len(issuerWeb))
	}
	var invokes, tokens int
	for _, c := range calls {
		c := c
		t.Run(c.Caller+"->"+c.Callee+"/"+c.Alias, func(t *testing.T) {
			caller := contractPolicy(t, rec, c.Caller)
			if c.Alias != "" {
				invokes++
				// (1) the alias exists on the callee.
				aliases := rec
				if c.Callee == arp.RoleBackup {
					aliases = backup
				}
				if !aliases.has("aws:lambda/alias:Alias", "staging-"+c.Callee+"-alias-"+c.Alias) {
					t.Errorf("%s has no alias %s, which %s invokes", c.Callee, c.Alias, c.Caller)
				}
				// (2) the caller may invoke it, qualified.
				if arn := fnArn("sluis-staging-"+c.Callee) + ":" + c.Alias; !contractAllows(caller, "lambda:InvokeFunction", arn) {
					t.Errorf("%s may not invoke %s", c.Caller, arn)
				}
			}
			if c.WebIdentity {
				tokens++
				// (3) the caller may ask STS for its token, as the issuer may.
				got := webIdentityStatements(caller)
				if len(got) != 1 {
					t.Fatalf("%s holds %d sts:GetWebIdentityToken statements, want 1", c.Caller, len(got))
				}
				for _, k := range []string{"Resource", "Condition"} {
					if !reflect.DeepEqual(got[0][k], issuerWeb[0][k]) {
						t.Errorf("%s: %s = %v, the issuer's is %v", c.Caller, k, got[0][k], issuerWeb[0][k])
					}
				}
				if !strings.Contains(strings.Join(strs(issuerWeb[0]["Resource"]), ""), "*") || issuerWeb[0]["Condition"] == nil {
					t.Error("the issuer's statement lost its shape: the comparison above proves nothing")
				}
			}
		})
	}
	if invokes == 0 || tokens == 0 {
		t.Errorf("the table has %d invoke rows and %d web identity rows", invokes, tokens)
	}
}

// A role that appears in no row as a caller with a web identity holds no token
// grant: the permission is exactly the table's.
func TestOnlyTheCallersOfTheTableHoldAWebIdentityGrant(t *testing.T) {
	rec, _ := mustLambda(t, estateWith(func(a *arp.LambdaArgs) { a.WebIdentityAudience = contractAudience }))
	want := map[string]bool{"issuer": true} // it mints for its own telemetry
	for _, c := range arp.ModuleCalls() {
		if c.WebIdentity {
			want[c.Caller] = true
		}
	}
	for _, r := range append([]string{"issuer"}, moduleRoles...) {
		if got := len(webIdentityStatements(contractPolicy(t, rec, r))) > 0; got != want[r] {
			t.Errorf("%s holds sts:GetWebIdentityToken = %v, the routing table says %v", r, got, want[r])
		}
	}
}
