package sluispulumi_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// The grants a module function's role needs to do what its document says
// (v1.76.0-rc.5): the secrets key, the blob credential's key, the minter of a
// preset's blobs, the tables the issuer lists, the controllers' run-now.

var (
	secretsKeyArn = arnp + "kms:eu-west-1:" + account + ":key/acme-secrets"
	ssmBase       = arnp + "ssm:" + region + ":" + account + ":parameter/sluis/staging/"
)

// rolePolicies are the statements of the role of every function the estate
// makes, by role: the issuer's and each module function's.
func rolePolicies(t *testing.T, rec *recorder) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{
		"issuer": statements(t, prop(rec.one(t, policyType, "staging-issuer-policy"), "policy").StringValue()),
	}
	for _, m := range moduleRoles {
		out[m] = statements(t, prop(rec.one(t, policyType, "staging-"+m+"-policy"), "policy").StringValue())
	}
	return out
}

func sidOf(st []map[string]any, sid string) map[string]any {
	for _, s := range st {
		if s["Sid"] == sid {
			return s
		}
	}
	return nil
}

// withResource are the statements that name the resource.
func withResource(st []map[string]any, resource string) []map[string]any {
	var out []map[string]any
	for _, s := range st {
		if slices.Contains(strs(s["Resource"]), resource) {
			out = append(out, s)
		}
	}
	return out
}

// keyedParameters are the parameters (PARAMETER_ARN) the statements let a key
// be used for, sorted.
func keyedParameters(st []map[string]any) []string {
	var out []string
	for _, s := range st {
		cond, _ := s["Condition"].(map[string]any)
		like, _ := cond["StringLike"].(map[string]any)
		out = append(out, strs(like["kms:EncryptionContext:PARAMETER_ARN"])...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// noRoleRepeatsASid is the invariant of a policy: a Sid is unique, so a
// statement added under a Sid the policy has is silently dropped by appendNew.
func noRoleRepeatsASid(t *testing.T, shape string, rec *recorder) {
	t.Helper()
	for role, st := range rolePolicies(t, rec) {
		seen := map[any]bool{}
		for _, s := range st {
			if seen[s["Sid"]] {
				t.Errorf("%s: the %s role's policy repeats the Sid %v", shape, role, s["Sid"])
			}
			seen[s["Sid"]] = true
		}
	}
}

// r2v5 is r2 at the address layout v5 keeps its credential at.
func r2v5() *arp.ExternalBlobs {
	b := r2()
	b.CredentialsRef = "internal/google/blobs-r2"
	return b
}

// parametersOf are the parameter ARNs a module's own statements name.
func parametersOf(m string) []string {
	return []string{ssmBase + "external/" + m + "/*", ssmBase + "internal/" + m + "/*"}
}

// G1. With Keys.Secrets, the module roles use the secrets key through SSM, for
// exactly the parameters they use: their own, and the sign-in client's for the
// Google module. Nothing of another module's.
func TestEveryModuleRoleUsesTheSecretsKeyForItsOwnParametersOnly(t *testing.T) {
	rec, _ := mustLambda(t, v5Estate(func(a *arp.LambdaArgs) { noDomain(a); a.Keys = suppliedKeys() }))
	pol := rolePolicies(t, rec)
	for _, m := range moduleRoles {
		keyed := withResource(pol[m], secretsKeyArn)
		if len(keyed) == 0 {
			t.Errorf("%s: no grant on the secrets key", m)
			continue
		}
		var acts []string
		for _, s := range keyed {
			if s["Effect"] != "Allow" {
				t.Errorf("%s: %v", m, s)
			}
			cond := s["Condition"].(map[string]any)["StringLike"].(map[string]any)
			if cond["kms:ViaService"] != "ssm.*.amazonaws.com" {
				t.Errorf("%s: the key is not through SSM only: %v", m, cond)
			}
			acts = append(acts, strs(s["Action"])...)
		}
		slices.Sort(acts)
		acts = slices.Compact(acts)
		if got := strings.Join(acts, ","); got != "kms:Decrypt,kms:Encrypt,kms:GenerateDataKey" {
			t.Errorf("%s: the secrets key is used with %s", m, got)
		}
		want := parametersOf(m)
		if m == "google" {
			want = append(want, ssmBase+"internal/oidc/signin/*")
		}
		slices.Sort(want)
		if got := keyedParameters(keyed); !slices.Equal(got, want) {
			t.Errorf("%s: the secrets key is used for\n%v\nwant exactly\n%v", m, got, want)
		}
	}
	// The issuer keeps its own grant (the whole root, under SluisKeysSecrets).
	if sidOf(pol["issuer"], "SluisKeysSecrets") == nil {
		t.Error("the issuer lost SluisKeysSecrets")
	}
}

// G3. A statement the module's own parameters have is never the one that drops
// the blob credential's key statement: both are in the policy, and the parameter
// key covers the credential's parameter as well as the role's own.
func TestTheBlobCredentialsKeyIsNotDroppedBehindTheModulesOwn(t *testing.T) {
	credential := ssmBase + "internal/google/blobs-r2"
	shapes := map[string]func(*arp.LambdaArgs){
		"parameter key": func(a *arp.LambdaArgs) { a.ParameterKeyArn = paramKey },
		"secrets key":   func(a *arp.LambdaArgs) { a.Keys = suppliedKeys() },
	}
	for name, mutate := range shapes {
		t.Run(name, func(t *testing.T) {
			rec, _ := mustLambda(t, v5Estate(func(a *arp.LambdaArgs) {
				noDomain(a)
				a.Storage = &arp.StorageGrant{External: r2v5()}
				mutate(a)
			}))
			noRoleRepeatsASid(t, name, rec)
			key := paramKey
			if name == "secrets key" {
				key = secretsKeyArn
			}
			for role, st := range rolePolicies(t, rec) {
				if sidOf(st, "SluisBlobCredentials") == nil {
					t.Errorf("%s: no read of the blob credential", role)
				}
				if role == "issuer" && name == "secrets key" {
					continue // SluisKeysSecrets covers the whole root
				}
				covered := keyedParameters(withResource(st, key))
				if !slices.Contains(covered, credential) {
					t.Errorf("%s: the key is not granted for the credential %s: %v", role, credential, covered)
				}
				// And the role's own parameters are still keyed.
				if role != "issuer" && !slices.Contains(covered, parametersOf(role)[1]) {
					t.Errorf("%s: the key is no longer granted for the role's own parameters: %v", role, covered)
				}
				if role == "issuer" && !slices.Contains(covered, ssmBase+"internal/oidc/*") {
					t.Errorf("issuer: the key is no longer granted for its own parameters: %v", covered)
				}
			}
		})
	}
}

// No role of any estate shape repeats a Sid.
func TestNoRolePolicyOfAnyShapeRepeatsASid(t *testing.T) {
	shapes := map[string]func(*arp.LambdaArgs){
		"plain":         func(a *arp.LambdaArgs) { noDomain(a) },
		"parameter key": func(a *arp.LambdaArgs) { noDomain(a); a.ParameterKeyArn = paramKey },
		"external":      func(a *arp.LambdaArgs) { noDomain(a); a.Storage = &arp.StorageGrant{External: r2v5()} },
		"external and key": func(a *arp.LambdaArgs) {
			noDomain(a)
			a.Storage = &arp.StorageGrant{External: r2v5()}
			a.ParameterKeyArn = paramKey
		},
		"keys": func(a *arp.LambdaArgs) { noDomain(a); a.Keys = suppliedKeys() },
	}
	for name, mutate := range shapes {
		rec, _ := mustLambda(t, v5Estate(mutate))
		noRoleRepeatsASid(t, name, rec)
	}
	// The layout v4 issuer too: its external blobs' key statement and its own.
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		noDomain(a)
		a.Storage = &arp.StorageGrant{External: r2()}
		a.ParameterKeyArn = paramKey
	}})
	st := statements(t, prop(rec.one(t, policyType, "staging-issuer-policy"), "policy").StringValue())
	seen := map[any]bool{}
	for _, s := range st {
		if seen[s["Sid"]] {
			t.Errorf("the layout v4 issuer repeats the Sid %v", s["Sid"])
		}
		seen[s["Sid"]] = true
	}
	for _, s := range []string{"SluisBlobCredentialsKey", "SluisParameterKey"} {
		if sidOf(st, s) == nil {
			t.Errorf("layout v4 issuer lost %s", s)
		}
	}
}

// githubWithPresetBlobs is a github module document whose blobs are minted from
// a Cloudflare preset in the module's own process.
const githubWithPresetBlobs = `apiVersion: sluis.truvity.github.io/controller-github/v2
ports:
  blob:
    adapter: s3
    s3:
      bucket: acme-blobs
      endpoint: https://acct.r2.example.test
      credentials:
        preset: r2
`

// G4. A module whose document opens blobs from a preset holds the minter's
// read and what it writes; no other role does, and the key admits it.
func TestAModuleThatMintsItsBlobCredentialsHoldsTheMinter(t *testing.T) {
	mutate := func(a *arp.LambdaArgs) {
		noDomain(a)
		a.ParameterKeyArn = paramKey
		a.Modules = map[string]arp.ModuleArgs{"github": {Document: githubWithPresetBlobs}}
	}
	rec, _ := mustLambda(t, v5Estate(mutate))
	pol := rolePolicies(t, rec)
	minter := sidOf(pol["github"], "SluisCrossCloudflareMinter")
	if minter == nil || !slices.Equal(strs(minter["Resource"]), []string{ssmBase + "internal/cloudflare/*/minter"}) {
		t.Fatalf("github: the minter read is %v", minter)
	}
	for _, a := range strs(minter["Action"]) {
		if strings.Contains(a, "Put") || strings.Contains(a, "Delete") {
			t.Errorf("the minter credential is written: %s", a)
		}
	}
	minted := sidOf(pol["github"], "SluisCrossCloudflareMinted")
	wantMinted := []string{ssmBase + "external/cloudflare/*", ssmBase + "internal/cloudflare/minted/*"}
	if minted == nil || !slices.Equal(sortedCopy(strs(minted["Resource"])), wantMinted) || !slices.Contains(strs(minted["Action"]), "ssm:PutParameter") {
		t.Errorf("github: what the minter writes is %v", minted)
	}
	keyed := keyedParameters(withResource(pol["github"], paramKey))
	for _, want := range append(wantMinted, ssmBase+"internal/cloudflare/*/minter") {
		if !slices.Contains(keyed, want) {
			t.Errorf("github: the parameter key is not granted for %s: %v", want, keyed)
		}
	}
	for _, role := range []string{"issuer", "slack", "google", "cloudflare"} {
		for _, sid := range []string{"SluisCrossCloudflareMinter", "SluisCrossCloudflareMinted", "SluisParameterKeyMinted"} {
			if sidOf(pol[role], sid) != nil {
				t.Errorf("%s holds %s", role, sid)
			}
		}
	}
	noRoleRepeatsASid(t, "preset blobs", rec)

	// The shared key's policy admits exactly that role to the minter's
	// parameters and what it writes.
	a := v5Estate(mutate).args(t)
	roles, err := a.Roles()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		if got := len(r.MinterRefs) > 0; got != (r.Role.Name == "github") || r.OpensPresetBlobs != got {
			t.Errorf("key role %s: MinterRefs %v, OpensPresetBlobs %v", r.Role.Name, r.MinterRefs, r.OpensPresetBlobs)
		}
	}
	ks, err := a.KeyPolicyStatements(arp.KeyPolicyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{"SluisKeyMinterGithub", "SluisKeyMintedGithub"} {
		if sidOf(ks, sid) == nil {
			t.Errorf("the key policy lacks %s", sid)
		}
	}
	for _, s := range ks {
		if sid, _ := s["Sid"].(string); (strings.HasPrefix(sid, "SluisKeyMinter") || strings.HasPrefix(sid, "SluisKeyMinted")) && !strings.HasSuffix(sid, "Github") {
			t.Errorf("the key policy admits another role to the minter: %s", sid)
		}
	}
}

// tablesOfDocument is the module -> table name map a function's document lists
// in ports.dynamodb.tables.
func tablesOfDocument(t *testing.T, doc string) map[string]string {
	t.Helper()
	out := map[string]string{}
	tables, _ := portsDynamoOf(t, doc)["tables"].(map[string]any)
	for m, v := range tables {
		out[m] = fmt.Sprint(v)
	}
	return out
}

// functionDocuments are the service documents of every function, by role.
func functionDocuments(t *testing.T, rec *recorder) map[string]string {
	t.Helper()
	out := map[string]string{"issuer": layerFiles(t, rec)["sluis/sluis.yaml"]}
	for _, m := range moduleRoles {
		if rec.has(layerType, "staging-"+m+"-config") {
			out[m] = archiveFiles(t, rec.one(t, layerType, "staging-"+m+"-config"))["sluis/sluis.yaml"]
		}
	}
	return out
}

// G2. Every table a function's document lists is covered by its role for what
// the function does to it: the readiness probe describes every one, and a table
// the role reads is read by key.
func TestEveryTableAFunctionListsIsCoveredByItsRole(t *testing.T) {
	for name, mk := range map[string]func() estate{
		"config": func() estate { return v5Estate(noDomain) },
		"installation": func() estate {
			return moduleEstate(t, moduleInstallation(t, "modules-full"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := mustLambda(t, mk())
			pol := rolePolicies(t, rec)
			docs := functionDocuments(t, rec)
			for role, doc := range docs {
				g := grants(pol[role])
				for m, table := range tablesOfDocument(t, doc) {
					arn := tableArn(table)
					for _, action := range []string{"dynamodb:DescribeTable"} {
						if !slices.Contains(g[action], arn) {
							t.Errorf("%s lists the %s table %s and may not %s it", role, m, table, action)
						}
					}
					if m != "oidc" && role == "issuer" {
						// A snapshot is read by key, never scanned.
						if !slices.Contains(g["dynamodb:GetItem"], arn) || !slices.Contains(g["dynamodb:Query"], arn) || slices.Contains(g["dynamodb:Scan"], arn) {
							t.Errorf("issuer: the %s table is not read by key and by key alone", m)
						}
					}
				}
			}
			// The issuer lists the tables it opens and no other: a peer's table is
			// the peer's.
			if got := tablesOfDocument(t, docs["issuer"]); len(got) != 2 || got["oidc"] == "" || got["google"] == "" {
				t.Errorf("the issuer lists %v", got)
			}
		})
	}
}

// G6. A controller's run-now starts its own function: the document says so, and
// the role may invoke that function's console alias and nothing else.
func TestEveryControllerNotifiesItsOwnFunctionAndMayInvokeIt(t *testing.T) {
	rec, _ := mustLambda(t, moduleEstate(t, moduleInstallation(t, "modules-full")))
	pol := rolePolicies(t, rec)
	docs := functionDocuments(t, rec)
	for _, m := range []string{"github", "slack"} {
		var doc struct {
			Adapters struct {
				Trigger struct {
					Adapter  string            `yaml:"adapter"`
					Settings map[string]string `yaml:"settings"`
				} `yaml:"trigger"`
			} `yaml:"adapters"`
		}
		if err := yaml.Unmarshal([]byte(docs[m]), &doc); err != nil {
			t.Fatal(err)
		}
		fn := prop(rec.one(t, fnType, "staging-"+m+"-fn"), "name").StringValue()
		if tr := doc.Adapters.Trigger; tr.Adapter != "invoke" || len(tr.Settings) != 1 || tr.Settings[m] != fn {
			t.Errorf("%s: adapters.trigger is %+v, want invoke of %s", m, tr, fn)
		}
		want := []string{fnArn(fn) + ":live-console"}
		if g := grants(pol[m])["lambda:InvokeFunction"]; !slices.Equal(g, want) {
			t.Errorf("%s may invoke %v, want exactly %v", m, g, want)
		}
		// The alias the adapter qualifies with exists on the function.
		rec.one(t, aliasType, "staging-"+m+"-alias-live-console")
	}
	for _, m := range []string{"google", "cloudflare"} {
		if strings.Contains(docs[m], "adapter: invoke") {
			t.Errorf("%s: notifies through invoke but hosts no controller", m)
		}
		if g := grants(pol[m])["lambda:InvokeFunction"]; len(g) != 0 {
			t.Errorf("%s may invoke %v", m, g)
		}
	}
	// A renamed function is the one the document names and the grant covers.
	rec, _ = mustLambda(t, moduleEstate(t, moduleInstallation(t, "modules-full"), func(a *arp.LambdaArgs) {
		a.Modules = map[string]arp.ModuleArgs{"github": {FunctionName: "acme-gh"}}
	}))
	doc := functionDocuments(t, rec)["github"]
	if !strings.Contains(doc, "github: acme-gh") {
		t.Errorf("the renamed controller notifies another function:\n%s", doc)
	}
	if g := grants(rolePolicies(t, rec)["github"])["lambda:InvokeFunction"]; len(g) != 1 || !strings.HasSuffix(g[0], ":function:acme-gh:live-console") {
		t.Errorf("the renamed controller may invoke %v", g)
	}
}

// handledEvents are the event kinds each function's handler takes from a
// schedule (internal/lambdaapp: the controllers take tick and run; the http
// function the issuer's clients and refresh, and the Cloudflare tick; the Google
// module the refresh). A schedule the library makes for a kind its function does
// not take fails at every tick, in production, where no test of the function sees it.
var handledEvents = map[string][]string{
	"issuer":     {"clients", "refresh", "cloudflare", "tick", "run", "check", "backup"},
	"github":     {"tick", "run", "check"},
	"slack":      {"tick", "run", "check"},
	"google":     {"refresh", "check"},
	"cloudflare": {"cloudflare", "check"},
}

// Every schedule the library makes sends its function an event kind that
// function takes.
func TestEveryScheduleSendsAKindItsFunctionTakes(t *testing.T) {
	in := exampleInstallation(t)
	withCloudflare(in)
	in.AWS.Table = ""
	cloudflare := withInstallation(in, func(a *arp.LambdaArgs) { withLayout(arp.LayoutV5).mutate(a) })
	for name, e := range map[string]estate{"controllers and directory": scheduled(v5Estate()), "cloudflare": cloudflare} {
		rec, _ := mustLambda(t, e)
		seen := map[string]bool{}
		for _, s := range rec.ofType(schedType) {
			tgt := prop(s, "target").ObjectValue()
			var payload struct{ Kind string }
			if err := json.Unmarshal([]byte(tgt["input"].StringValue()), &payload); err != nil {
				t.Fatalf("%s: %v", s.Name, err)
			}
			fn := strings.TrimSuffix(tgt["arn"].StringValue(), ":live")
			role := fn[strings.LastIndex(fn, "-")+1:]
			if role == "http" {
				role = "issuer" // the issuer's function is the installation's http function
			}
			if !slices.Contains(handledEvents[role], payload.Kind) {
				t.Errorf("%s: %s sends the %s function an event of kind %q, which it does not take", name, prop(s, "name").StringValue(), role, payload.Kind)
			}
			seen[role+"/"+payload.Kind] = true
		}
		if len(seen) < 2 {
			t.Errorf("%s: only %v was swept", name, seen)
		}
	}
}
