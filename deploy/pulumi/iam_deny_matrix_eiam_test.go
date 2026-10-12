//nolint:lll // fixtures and sentences of the isolation rows are one-line tables
package sluispulumi

import (
	"slices"
	"testing"

	"github.com/truvity/sluis/deploy/pulumi/internal/iameval"
)

// The v1.76 module-isolation rows (E-IAM). The issuer holds no provider
// module's state, parameters or functions beyond the Google snapshot and the
// qualified invokes, and a provider module's role holds nothing of a peer. The
// rows are generated from the module constants and the function list, never
// written out per module, and each one names its expected answer.

// eiamProviders are the modules that moved behind their own function.
var eiamProviders = []Module{ModuleGitHub, ModuleSlack, ModuleGoogle, ModuleCloudflare}

// eiamMinMatrix pins the sweep: a shrunk sweep proves nothing.
const eiamMinMatrix = 500

func eiamRows(t *testing.T) (rows []mxRow, identity map[string]iameval.Policy, key iameval.Policy) {
	t.Helper()
	identity, key = mxPolicies(t)
	// The module roles again, rendered as a deployment with a minter would: the
	// minter reference is given to every one of them, and only the cloudflare
	// function may end up holding it.
	menv := mxEnv(moduleRoles())
	menv.MinterRefs = []string{mxMinter}
	for _, r := range ModuleFunctions() {
		st, err := ModuleRoleStatements(menv, moduleRole(r))
		if err != nil {
			t.Fatal(err)
		}
		p, err := iameval.Parse(st)
		if err != nil {
			t.Fatal(err)
		}
		identity[r] = p
	}

	add := func(role, group, name, action, resource string, want bool, ctx func(iameval.Request) iameval.Request) {
		req := iameval.Request{Principal: mxRoleArn(role), Action: action, Resource: resource}
		if ctx != nil {
			req = ctx(req)
		}
		rows = append(rows, mxRow{role: role, group: group, name: name, req: req, want: want})
	}
	leading := func(r iameval.Request) iameval.Request { return r.WithContext("dynamodb:LeadingKeys", "rec.k") }
	prefix := func(p string) func(iameval.Request) iameval.Request {
		return func(r iameval.Request) iameval.Request { return r.WithContext("s3:prefix", p) }
	}
	viaSSM := func(param string) func(iameval.Request) iameval.Request {
		return func(r iameval.Request) iameval.Request {
			return r.WithContext("kms:ViaService", mxSsmVia).WithEncryptionContext(map[string]string{"PARAMETER_ARN": param})
		}
	}
	minter := arnPrefix + "ssm:" + mxRegion + ":" + mxAccount + ":parameter/sluis/" + mxInst + "/" + mxMinter

	// The issuer.
	const iss = "issuer"
	for _, m := range eiamProviders {
		ms, tbl := string(m), mxTable(m)
		for _, a := range []string{"PutItem", "UpdateItem", "DeleteItem", "BatchWriteItem"} {
			add(iss, "eiam-ddb", ms+" "+a, "dynamodb:"+a, tbl, false, leading)
		}
		google := m == ModuleGoogle
		add(iss, "eiam-ddb", ms+" GetItem", "dynamodb:GetItem", tbl, google, leading)
		add(iss, "eiam-ddb", ms+" Query", "dynamodb:Query", tbl, google, leading)
		add(iss, "eiam-ddb", ms+" Scan", "dynamodb:Scan", tbl, false, nil)
	}
	for _, m := range []Module{ModuleGitHub, ModuleSlack, ModuleGoogle} {
		ms, obj := string(m), mxBucket+"/"+string(m)+"/obj"
		add(iss, "eiam-s3", ms+" PutObject", "s3:PutObject", obj, false, nil)
		add(iss, "eiam-s3", ms+" DeleteObject", "s3:DeleteObject", obj, false, nil)
		add(iss, "eiam-s3", ms+" GetObject", "s3:GetObject", obj, m == ModuleGoogle, nil)
		add(iss, "eiam-s3", ms+" ListBucket", "s3:ListBucket", mxBucket, m == ModuleGoogle, prefix(ms+"/"))
	}
	for _, m := range Modules() {
		if m == ModuleOIDC {
			continue
		}
		for _, ns := range []string{"internal", "external"} {
			p := mxParam(ns, string(m), "k")
			name := ns + "/" + string(m)
			for _, a := range []string{"GetParameter", "GetParameters", "GetParametersByPath", "PutParameter", "DeleteParameter"} {
				add(iss, "eiam-ssm", name+" "+a, "ssm:"+a, p, false, nil)
			}
			for _, a := range []string{"Decrypt", "GenerateDataKey", "Encrypt"} {
				add(iss, "eiam-kms", name+" "+a, "kms:"+a, mxKey, false, viaSSM(p))
				// And the key policy alone refuses it.
				rows = append(rows, mxRow{role: iss, group: "eiam-kms-ceiling", name: name + " " + a,
					req: viaSSM(p)(iameval.Request{Principal: mxRoleArn(iss), Action: "kms:" + a, Resource: mxKey}), ceiling: true})
			}
		}
	}
	for _, a := range []string{"GetParameter", "GetParameters", "PutParameter", "DeleteParameter"} {
		add(iss, "eiam-ssm", "minter credential "+a, "ssm:"+a, minter, false, nil)
	}
	for _, a := range []string{"Decrypt", "GenerateDataKey"} {
		add(iss, "eiam-kms", "minter credential "+a, "kms:"+a, mxKey, false, viaSSM(minter))
	}
	console := map[string]bool{"github": true, "slack": true, "google": true, "cloudflare": true, "backup": true}
	issuerClass := map[string]bool{"github": true, "google": true, "cloudflare": true}
	for _, f := range mxFunctions {
		fn := mxFn(f)
		if f == "issuer" || f == "restore" {
			continue
		}
		add(iss, "eiam-invoke", f+" unqualified", "lambda:InvokeFunction", fn, false, nil)
		add(iss, "eiam-invoke", f+" $LATEST", "lambda:InvokeFunction", fn+":$LATEST", false, nil)
		add(iss, "eiam-invoke", f+" live-console", "lambda:InvokeFunction", fn+":live-console", console[f], nil)
		add(iss, "eiam-invoke", f+" live-issuer", "lambda:InvokeFunction", fn+":live-issuer", issuerClass[f], nil)
		for _, alias := range []string{"live", "live-backup", "live-admin", "live-breakglass", "live-" + f, "prev", "1"} {
			add(iss, "eiam-invoke", f+" alias "+alias, "lambda:InvokeFunction", fn+":"+alias, false, nil)
		}
	}

	// The routing table (ModuleCalls), row for row: a caller invokes the alias
	// the table names, on every callee that alias is named for and no other, and
	// holds sts:GetWebIdentityToken exactly when a row says it proves itself
	// with the token.
	for _, c := range ModuleCalls() {
		if c.Alias == "" {
			continue
		}
		add(c.Caller, "eiam-table-invoke", c.Caller+"->"+c.Callee+" "+c.Alias, "lambda:InvokeFunction", mxFn(c.Callee)+":"+c.Alias, true, nil)
	}
	for _, r := range append([]string{RoleIssuer}, ModuleFunctions()...) {
		if r == RoleIssuer {
			continue
		}
		var web bool
		for _, c := range ModuleCalls() {
			web = web || (c.Caller == r && c.WebIdentity)
		}
		add(r, "eiam-table-token", r+" GetWebIdentityToken", "sts:GetWebIdentityToken", "*", web, nil)
	}

	// The converse: each provider's role.
	oidcTable := mxTable(ModuleOIDC)
	for _, self := range eiamProviders {
		role := string(self)
		for _, f := range mxFunctions {
			for _, q := range []string{"", ":$LATEST", ":live", ":live-issuer", ":live-console", ":1"} {
				add(role, "eiam-converse-invoke", "invoke "+f+q, "lambda:InvokeFunction", mxFn(f)+q, false, nil)
			}
		}
		for _, peer := range Modules() {
			if peer == self {
				continue
			}
			ps := string(peer)
			for _, a := range []string{"GetItem", "Query", "PutItem", "UpdateItem", "DeleteItem", "Scan"} {
				add(role, "eiam-converse-ddb", ps+" "+a, "dynamodb:"+a, mxTable(peer), false, leading)
			}
			for _, a := range []string{"GetObject", "PutObject", "DeleteObject"} {
				add(role, "eiam-converse-s3", ps+" "+a, "s3:"+a, mxBucket+"/"+ps+"/obj", false, nil)
			}
			for _, ns := range []string{"internal", "external"} {
				p := mxParam(ns, ps, "k")
				for _, a := range []string{"GetParameter", "PutParameter", "DeleteParameter"} {
					add(role, "eiam-converse-ssm", ns+"/"+ps+" "+a, "ssm:"+a, p, false, nil)
				}
				add(role, "eiam-converse-kms", ns+"/"+ps+" Decrypt", "kms:Decrypt", mxKey, false, viaSSM(p))
			}
		}
		add(role, "eiam-converse-ddb", "oidc GetItem", "dynamodb:GetItem", oidcTable, false, leading)
		add(role, "eiam-converse-ddb", "oidc Query", "dynamodb:Query", oidcTable, false, leading)
		// The minter's statement belongs to the cloudflare role alone.
		cf := self == ModuleCloudflare
		add(role, "eiam-minter", "minter GetParameter", "ssm:GetParameter", minter, cf, nil)
		add(role, "eiam-minter", "minter Decrypt", "kms:Decrypt", mxKey, cf, viaSSM(minter))
		add(role, "eiam-minter", "minter PutParameter", "ssm:PutParameter", minter, cf, nil)
	}
	return rows, identity, key
}

func TestIssuerAndProviderRolesAreIsolated(t *testing.T) {
	rows, identity, key := eiamRows(t)
	if len(rows) < eiamMinMatrix {
		t.Fatalf("the isolation matrix has %d rows, fewer than the %d it is pinned to", len(rows), eiamMinMatrix)
	}
	var allow, refuse int
	for _, r := range rows {
		if r.ceiling || !r.want {
			refuse++
		} else {
			allow++
		}
		t.Run(r.role+"/"+r.group+"/"+r.name, func(t *testing.T) {
			d := mxDecide(t, r, identity[r.role], key)
			if r.ceiling {
				if d.Allowed {
					t.Errorf("the key policy alone allows %s: %s", r.req.Action, d.Reason)
				}
				return
			}
			if d.Allowed != r.want {
				t.Errorf("%s on %s: allowed = %v (%s), want %v", r.req.Action, r.req.Resource, d.Allowed, d.Reason, r.want)
			}
		})
	}
	if allow < 10 {
		t.Errorf("only %d allow rows: the matrix should hold both answers", allow)
	}
	t.Logf("%d isolation rows: %d allow, %d refuse", len(rows), allow, refuse)
}

// Only the cloudflare role may carry the minter statement, whatever the
// environment hands the others.
func TestOnlyTheCloudflareFunctionCarriesTheMinterStatement(t *testing.T) {
	menv := mxEnv(moduleRoles())
	menv.MinterRefs = []string{mxMinter}
	for _, r := range ModuleFunctions() {
		st, err := ModuleRoleStatements(menv, moduleRole(r))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range st {
			if s["Sid"] == "SluisCrossCloudflareMinter" && r != string(ModuleCloudflare) && r != "backup" && r != "restore" {
				t.Errorf("role %s carries the minter cross-grant", r)
			}
		}
	}
}

func TestEveryProviderIsCoveredByTheIsolationRows(t *testing.T) {
	rows, _, _ := eiamRows(t)
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.role] = true
	}
	for _, m := range eiamProviders {
		if !seen[string(m)] {
			t.Errorf("no isolation row for the %s role", m)
		}
	}
	if !seen["issuer"] || !slices.Contains(ModuleFunctions(), "github") {
		t.Error("the sweep lost the issuer or the module functions")
	}
}
