package sluispulumi_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/storage/state"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

const ssmParameterType = "aws:ssm/parameter:Parameter"

func policyOf(t *testing.T, e estate) []map[string]any {
	t.Helper()
	rec, _ := mustLambda(t, e)
	return statements(t, prop(rec.one(t, policyType, "staging-issuer-policy"), "policy").StringValue())
}

func resourcesOf(st []map[string]any) []string {
	var out []string
	for _, s := range st {
		out = append(out, strs(s["Resource"])...)
	}
	return out
}

func sids(st []map[string]any) []string {
	var out []string
	for _, s := range st {
		out = append(out, s["Sid"].(string))
	}
	return out
}

var paramRoot = arnp + "ssm:" + region + ":" + account + ":parameter/sluis/staging"

// The issuer's role is its module's grants: it hosts the oidc module (its own
// table, parameters and blob prefix), reads the providers' tables and the Google
// snapshots, and invokes their functions; no path outside the modules.
func TestTheIssuerRoleHoldsItsModuleGrantsOnly(t *testing.T) {
	st := policyOf(t, estateWith())
	res := resourcesOf(st)
	for _, want := range []string{
		tableArn("sluis-staging-oidc"), tableArn("sluis-staging-google"),
		paramRoot + "/internal/oidc/*", paramRoot + "/external/oidc/*",
		arnp + "s3:::" + bucket + "/oidc/*", arnp + "s3:::" + bucket + "/google/*",
		fnArn("sluis-staging-github") + ":live-issuer",
		fnArn("sluis-staging-slack") + ":live-console", fnArn("sluis-staging-google") + ":live-issuer",
		fnArn("sluis-staging-cloudflare") + ":live-issuer", fnArn("sluis-staging-backup") + ":live-console",
	} {
		if !slices.Contains(res, want) {
			t.Errorf("no grant on %s", want)
		}
	}
	for _, r := range res {
		switch {
		case r == tableArn(table), strings.HasPrefix(r, paramRoot+"/internal/config"), strings.HasPrefix(r, paramRoot+"/internal/credentials"),
			r == paramRoot+"/external/*", r == arnp+"s3:::"+bucket+"/*", r == paramRoot+"/internal/*",
			strings.Contains(r, "table/sluis-staging-cloudflare"), strings.Contains(r, "/internal/cloudflare"),
			// The providers' secrets are theirs, not the issuer's.
			strings.HasPrefix(r, paramRoot+"/internal/google"), strings.HasPrefix(r, paramRoot+"/internal/github"),
			strings.HasPrefix(r, paramRoot+"/internal/slack"), strings.HasPrefix(r, paramRoot+"/external/google"),
			// Every invoke is of an alias, never the function.
			strings.Contains(r, ":function:") && !strings.Contains(r[strings.Index(r, ":function:")+1:], ":live"):
			t.Errorf("an unhosted resource is granted: %s", r)
		}
	}
	for _, r := range res {
		if r == tableArn("sluis-staging-backup") {
			t.Errorf("the backup table is named: %v", r)
		}
	}
	// The rest of the role.
	for _, sid := range []string{"SluisLogs", "SluisRunAPass", "SluisWebIdentity"} {
		if !slices.Contains(sids(st), sid) {
			t.Errorf("%s is gone", sid)
		}
	}
}

// The Cloudflare minter's module is hosted by the cloudflare function whatever
// the installation declares; with presets the issuer's role reads the minter's
// parameters for the blob credentials and never the table.
func TestTheCloudflareFunctionHostsTheCloudflareModuleWithPresets(t *testing.T) {
	in := exampleInstallation(t)
	withCloudflare(in)
	rec, _ := mustLambda(t, withInstallation(in, nil))
	cf := resourcesOf(statements(t, prop(rec.one(t, policyType, "staging-cloudflare-policy"), "policy").StringValue()))
	if !slices.Contains(cf, tableArn("sluis-staging-cloudflare")) {
		t.Errorf("no table for the minter: %v", cf)
	}
	if !slices.Contains(cf, arnp+"ssm:"+region+":"+account+":parameter/sluis/example/internal/cloudflare/*") {
		t.Errorf("no parameters for the minter: %v", cf)
	}
	for _, r := range resourcesOf(statements(t, prop(rec.one(t, policyType, "staging-issuer-policy"), "policy").StringValue())) {
		if r == tableArn("sluis-staging-cloudflare") || r == arnp+"ssm:"+region+":"+account+":parameter/sluis/example/internal/cloudflare/*" {
			t.Errorf("the issuer holds the minter's state: %s", r)
		}
	}
}

func fnArn(name string) string {
	return arnp + "lambda:" + region + ":" + account + ":function:" + name
}

func TestTheTablesAndTheLayoutAreValidated(t *testing.T) {
	for name, e := range map[string]estate{
		"no module tables":          estateWith(func(a *arp.LambdaArgs) { a.State.Tables = nil }),
		"a missing module table":    estateWith(func(a *arp.LambdaArgs) { delete(a.State.Tables, arp.ModuleOIDC) }),
		"an unknown module's table": estateWith(func(a *arp.LambdaArgs) { a.TableNames = map[arp.Module]string{"nope": "x"} }),
		"another secrets.layout":    estateWith(func(a *arp.LambdaArgs) { a.Config += "secrets: {layout: v4}\n" }),
		"a single table":            estateWith(func(a *arp.LambdaArgs) { a.Config += "ports: {dynamodb: {table: t}}\n" }),
	} {
		_, _, err := buildLambda(t, e)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// portsDynamoOf reads the rendered service document's ports.dynamodb.
func portsDynamoOf(t *testing.T, doc string) map[string]any {
	t.Helper()
	var m struct {
		Ports struct {
			Adapter  string         `yaml:"adapter"`
			DynamoDB map[string]any `yaml:"dynamodb"`
		} `yaml:"ports"`
	}
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	return m.Ports.DynamoDB
}

// The library writes `secrets.layout: v5` and the tables into the document,
// from a Config and from an Installation.
func TestTheDocumentNamesTheLayoutAndTheTables(t *testing.T) {
	rename := func(a *arp.LambdaArgs) { a.TableNames = map[arp.Module]string{arp.ModuleGoogle: "custom-google"} }
	for name, c := range map[string]struct {
		e        estate
		instance string
	}{
		"config": {estateWith(rename), "staging"},
		"installation": {withInstallation(exampleInstallation(t), func(a *arp.LambdaArgs) {
			estateWith(rename).mutate(a)
		}), "example"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := mustLambda(t, c.e)
			doc := layerFiles(t, rec)["sluis/sluis.yaml"]
			if s := secretsOf(t, doc); s["layout"] != "v5" || s["source"] != "ssm" {
				t.Errorf("secrets = %v", s)
			}
			d := portsDynamoOf(t, doc)
			tables, _ := d["tables"].(map[string]any)
			// The issuer's document lists the tables its role opens: its own and the
			// Google snapshot's. The readiness probe describes every one listed.
			if len(tables) != 2 || tables["oidc"] != "sluis-"+c.instance+"-oidc" || tables["google"] != "custom-google" {
				t.Errorf("tables = %v", d)
			}
			if d["table"] != nil {
				t.Errorf("a single table beside the tables: %v", d)
			}
		})
	}
}

// The state secret and the recovery password are generated once each and
// stored at the oidc module's addresses, as the secret store's document the
// issuer reads, with the recovery account's verifier beside them; nothing else.
func TestTheGeneratedParametersAreAtTheOIDCAddresses(t *testing.T) {
	rec, out := mustLambda(t, estateWith())
	byPath := map[string]declared{}
	for _, p := range rec.ofType(ssmParameterType) {
		byPath[prop(p, "name").StringValue()] = p
	}
	want := []string{
		arp.StateSecretParameterNameV5("staging"), arp.RecoveryPasswordParameterNameV5("staging"),
		arp.RecoveryVerifierParameterNameV5("staging"),
	}
	if got := slices.Sorted(mapKeys(byPath)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Fatalf("parameters %v, want %v", got, want)
	}
	for _, n := range want {
		p := byPath[n]
		// The value is the secret store's document, which is what the issuer reads.
		textOfV5Document(t, prop(p, "value"))
		if prop(p, "type").StringValue() != "SecureString" || !prop(p, "overwrite").BoolValue() {
			t.Errorf("%s: %v", n, p.Inputs)
		}
	}
	if got := arp.StateSecretParameterNameV5("staging"); got != "/sluis/staging/internal/oidc/state-secret" {
		t.Errorf("state secret = %s", got)
	}
	if got := arp.RecoveryPasswordParameterNameV5("staging"); got != "/sluis/staging/internal/oidc/recovery-password" {
		t.Errorf("recovery = %s", got)
	}
	if got := arp.ConfigParameterPrefixV5("staging"); got != "/sluis/staging/internal/oidc" {
		t.Errorf("prefix = %s", got)
	}
	// One generator each, and the recovery account's salt (docs/decisions/0076),
	// which is kept with the password.
	if n := len(rec.ofType("random:index/randomBytes:RandomBytes")) + len(rec.ofType("random:index/randomPassword:RandomPassword")); n != 3 {
		t.Errorf("%d generators", n)
	}
	if out["stateSecretParameter"] != arp.StateSecretParameterNameV5("staging") ||
		out["recoveryPasswordParameter"] != arp.RecoveryPasswordParameterNameV5("staging") {
		t.Errorf("outputs: %s, %s", out["stateSecretParameter"], out["recoveryPasswordParameter"])
	}
}

func mapKeys[K comparable, V any](m map[K]V) func(func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// A process that mints its blob credentials from a Cloudflare preset still
// reads no minter parameter when it is the issuer: the cloudflare function mints.
func TestAPresetBlobCredentialLeavesTheIssuerWithoutTheMinterParameter(t *testing.T) {
	const r2 = "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
	cfg := "issuerURL: https://x.example\n" +
		"cloudflare: {accounts: {main: {id: 0123456789abcdef0123456789abcdef, minter: internal/cloudflare/main/minter}}, " +
		"presets: {blobs: {account: main, prototype: proto-r2-0001, description: R2, lifetime: 15m, rotation: 5m, endpoint: '" + r2 + "'}}}\n" +
		"ports: {blob: {adapter: s3, s3: {bucket: b, endpoint: '" + r2 + "', credentials: {preset: blobs}}}}\n"
	e := estateWith(func(a *arp.LambdaArgs) { a.AllowEndpoints = true })
	e.config = cfg
	st := policyOf(t, e)
	// The issuer asks the cloudflare function to mint: it holds no reference to
	// the minter's parameter, and no statement for one (v1.76 E-FIN).
	for _, sid := range sids(st) {
		if sid == "SluisCrossCloudflareMinter" {
			t.Error("the issuer carries a minter cross-grant")
		}
	}
	for _, r := range resourcesOf(st) {
		if strings.Contains(r, "/internal/cloudflare") {
			t.Errorf("the issuer reaches the Cloudflare module's parameters: %s", r)
		}
	}
	// Without the preset, nothing of the module.
	for _, r := range resourcesOf(policyOf(t, estateWith())) {
		if strings.Contains(r, "/internal/cloudflare") {
			t.Errorf("a Cloudflare grant without a preset: %s", r)
		}
	}
}

// The maintenance partition is denied for writes in one statement on the
// module tables the issuer's role names, and the role still writes its other
// records.
func TestTheMaintenanceDenyCoversTheModuleTables(t *testing.T) {
	var want []string
	for _, m := range []string{"oidc", "google"} {
		want = append(want, tableArn("sluis-staging-"+m))
	}
	st := policyOf(t, estateWith())
	var denies []map[string]any
	for _, s := range st {
		// The passwords' deny (docs/decisions/0076) is the next test's.
		if s["Effect"] == "Deny" && s["Sid"] != "SluisDenyPasswords" && s["Sid"] != "SluisDenyPasswordsKey" {
			denies = append(denies, s)
		}
	}
	if len(denies) != 1 || denies[0]["Sid"] != "SluisMaintenanceDeny" {
		t.Fatalf("%v", denies)
	}
	got := strs(denies[0]["Resource"])
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("deny on %v, want %v", got, want)
	}
	cond := denies[0]["Condition"].(map[string]any)["ForAnyValue:StringEquals"].(map[string]any)
	if k := strs(cond["dynamodb:LeadingKeys"]); !slices.Equal(k, []string{"maintenance"}) {
		t.Errorf("deny on keys %v", k)
	}
	// The other records are still written: an allow of PutItem on each table.
	allowed := map[string]bool{}
	for _, s := range st {
		if s["Effect"] == "Allow" && slices.Contains(strs(s["Action"]), "dynamodb:PutItem") {
			for _, r := range strs(s["Resource"]) {
				allowed[r] = true
			}
		}
	}
	// The issuer writes its own (oidc) table and reads the providers': it is
	// denied the flag on those too, and written no record.
	for _, w := range want {
		provider := slices.Contains([]string{tableArn("sluis-staging-github"), tableArn("sluis-staging-slack"), tableArn("sluis-staging-google")}, w)
		if allowed[w] == provider {
			t.Errorf("write on %s = %v", w, allowed[w])
		}
	}
}

func secretText(t *testing.T, v resource.PropertyValue) string {
	t.Helper()
	for v.IsSecret() {
		v = v.SecretValue().Element
	}
	if !v.IsString() {
		t.Fatalf("not a string: %v", v)
	}
	return v.StringValue()
}

// textOfV5Document reads a generated parameter's value the way the issuer does:
// through the secret store's byte codec (a raw value is refused there).
func textOfV5Document(t *testing.T, v resource.PropertyValue) string {
	t.Helper()
	raw, err := state.Raw().Unmarshal([]byte(secretText(t, v)))
	if err != nil {
		t.Fatalf("the parameter is not the secret store's document: %v", err)
	}
	return string(raw)
}

// The credentials ref of external blobs begins with a module: one that does
// not, internal/blobs/r2, is an address the secrets layout does not have, and
// the library refuses it and names the one to use.
func TestABlobCredentialsRefOutsideAModuleIsRefused(t *testing.T) {
	blobs := func(ref string) func(*arp.LambdaArgs) {
		return func(a *arp.LambdaArgs) {
			noDomain(a)
			e := r2()
			e.CredentialsRef = ref
			a.Storage = &arp.StorageGrant{External: e}
		}
	}
	_, _, err := buildLambda(t, estateWith(blobs("internal/blobs/r2")))
	if err == nil || !strings.Contains(err.Error(), `"internal/google/blobs-r2"`) {
		t.Fatalf("internal/blobs/r2: %v, want a refusal naming internal/google/blobs-r2", err)
	}
	if _, _, err := buildLambda(t, estateWith(blobs("internal/google/blobs-r2"))); err != nil {
		t.Errorf("internal/google/blobs-r2: %v", err)
	}
}

// The document the library renders for an estate whose blobs are
// on an S3-compatible store (R2) is one the oidc process starts on, and the
// role of the function reads exactly the parameter that document names. The
// runtime half is the root module's own test of its config validation, run
// here over the rendered file: the library cannot import the runtime's
// internals, and a test that stopped at the library's own checks is what let
// the library, the migration and the runtime name three addresses.
func TestTheRenderedDocumentStartsTheRuntimeAndTheRoleReadsItsCredential(t *testing.T) {
	const ref = "internal/google/blobs-r2"
	rec, _ := mustLambda(t, estateWith(func(a *arp.LambdaArgs) {
		noDomain(a)
		e := r2()
		e.CredentialsRef = ref
		a.Storage = &arp.StorageGrant{External: e}
	}))
	doc := layerFiles(t, rec)["sluis/sluis.yaml"]
	if !strings.Contains(doc, "credentialsRef: "+ref) {
		t.Fatalf("the document does not name %s:\n%s", ref, doc)
	}

	// The role reads the parameter the document names, below the secrets root.
	grant := statementBySid(t, rec, "SluisBlobCredentials")
	if grant == nil {
		t.Fatal("no SluisBlobCredentials statement")
	}
	if res := resourcesOf([]map[string]any{grant}); len(res) != 1 || !strings.HasSuffix(res[0], "/"+ref) {
		t.Errorf("the grant is on %v, want the parameter at %s", grant["Resource"], ref)
	}
	if !slices.Contains(strs(grant["Action"]), "ssm:GetParameter") {
		t.Errorf("the grant has actions %v, want ssm:GetParameter", grant["Action"])
	}

	// The runtime half.
	file := filepath.Join(t.TempDir(), "sluis.yaml")
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-run", "^TestTheRenderedLayoutV5DocumentPassesTheRuntimeChecks$", "-v",
		"./internal/store")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "SLUIS_RENDERED_SERVICE="+file)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(out), "SKIP") {
		t.Fatalf("the runtime's checks over the rendered document: %v\n%s", err, out)
	}
}
