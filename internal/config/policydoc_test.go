package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	policyconfig "github.com/truvity/policy/config"
	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/config/schema"
	"github.com/truvity/sluis/policy"
)

func writeIn(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func fullPolicy(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "policy.full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The full example is a document the loader accepts, and the schema has no
// top-level key the example leaves out.
func TestTheFullPolicyDocumentLoads(t *testing.T) {
	d, err := config.Load[config.PolicyDocument](filepath.Join("testdata", "policy.full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if d.APIVersion != config.APIVersion("policy") || d.Policy.Version != 1 || len(d.Policy.Groups) != 3 {
		t.Errorf("tables: %+v", d.Policy)
	}
	if len(d.Clusters()) != 1 || d.AWS().Audience == "" || d.GitHubOwners()[0] != "example-org" {
		t.Errorf("exchange: %+v", d.Exchange)
	}
	if len(d.GitHubCatalogue().Apps) != 1 || len(d.SlackCatalogue().Apps) != 1 || len(d.RunnerTiers()) != 2 {
		t.Errorf("apps: %+v", d.Apps)
	}
	if d.EnabledOrgs()[0] != "example-org" || d.EnabledWorkspaces()[0] != "example" {
		t.Errorf("controllers: %+v", d.Controllers)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(fullPolicy(t), &doc); err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	body, _ := schema.Schema("policy")
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	for key := range s.Properties {
		if _, set := doc[key]; !set {
			t.Errorf("the full example does not set %s", key)
		}
	}
}

// The canonical encoding is a document the loader reads back to the same
// policy, and encoding that again gives the same bytes: a render is stable.
func TestTheCanonicalEncodingRoundTrips(t *testing.T) {
	d, err := config.Load[config.PolicyDocument](filepath.Join("testdata", "policy.full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := d.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(first, []byte("apiVersion: sluis.truvity.github.io/policy/v2\n")) {
		t.Errorf("the canonical document does not start with its apiVersion:\n%s", first)
	}
	back, err := config.Load[config.PolicyDocument](writeIn(t, t.TempDir(), "policy.yaml", string(first)))
	if err != nil {
		t.Fatalf("the canonical document does not load: %v\n%s", err, first)
	}
	second, _ := back.Encode()
	if !bytes.Equal(first, second) {
		t.Errorf("encoding is not stable:\n%s\n---\n%s", first, second)
	}
	a, _ := d.Policy.Digest()
	b, _ := back.Policy.Digest()
	if a != b {
		t.Error("the tables changed in the round trip")
	}
	if !reflect.DeepEqual(d.Exchange, back.Exchange) || !reflect.DeepEqual(d.Apps, back.Apps) ||
		!reflect.DeepEqual(d.Controllers, back.Controllers) {
		t.Error("a section changed in the round trip")
	}
}

// The schema and the Go types describe the same keys, both ways, at every
// depth: a key the type has that the schema lacks would be refused before the
// type could read it, and one the schema has that the type lacks would be
// accepted and dropped.
func TestThePolicySchemaAndTheTypesDescribeTheSameKeys(t *testing.T) {
	body, _ := schema.Schema("policy")
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	props := root["properties"].(map[string]any)
	sections := map[string]reflect.Type{
		"exchange":    reflect.TypeFor[config.PolicyExchange](),
		"apps":        reflect.TypeFor[config.PolicyApps](),
		"controllers": reflect.TypeFor[config.PolicyControllers](),
		"cloudflare":  reflect.TypeFor[config.PolicyCloudflare](),
	}
	tables := reflect.TypeFor[policy.Policy]()
	for i := range tables.NumField() {
		name, _, _ := strings.Cut(tables.Field(i).Tag.Get("yaml"), ",")
		if name == "version" {
			continue
		}
		sections[name] = tables.Field(i).Type
	}
	want := slices.Sorted(func(yield func(string) bool) {
		for k := range sections {
			if !yield(k) {
				return
			}
		}
	})
	got := []string{}
	for k := range props {
		if k != "apiVersion" {
			got = append(got, k)
		}
	}
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Fatalf("top-level keys: the types have %v, the schema %v", want, got)
	}
	for key, typ := range sections {
		compareKeys(t, key, props[key].(map[string]any), typ)
	}
}

// compareKeys walks a schema node and a type together.
func compareKeys(t *testing.T, at string, node map[string]any, typ reflect.Type) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Slice:
		if items, ok := node["items"].(map[string]any); ok {
			compareKeys(t, at+"[]", items, typ.Elem())
		}
		return
	case reflect.Map:
		if value, ok := node["additionalProperties"].(map[string]any); ok {
			compareKeys(t, at+".*", value, typ.Elem())
		}
		return
	case reflect.Struct:
	default:
		return
	}
	props, ok := node["properties"].(map[string]any)
	if !ok {
		// A shape written as a choice (`groups: all | [...]`, a role as a
		// list or an object) or a duration: the type decodes it itself.
		return
	}
	fields := map[string]reflect.Type{}
	for i := range typ.NumField() {
		f := typ.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "" {
			tag = f.Tag.Get("json")
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		fields[name] = f.Type
	}
	for name, ft := range fields {
		sub, has := props[name].(map[string]any)
		if !has {
			t.Errorf("%s.%s: the type has it and the schema does not", at, name)
			continue
		}
		compareKeys(t, at+"."+name, sub, ft)
	}
	for name := range props {
		if _, has := fields[name]; !has {
			t.Errorf("%s.%s: the schema has it and the type does not", at, name)
		}
	}
}

// What the checks refuse, each named in the error.
func TestThePolicyDocumentChecksItsReferences(t *testing.T) {
	base := "apiVersion: sluis.truvity.github.io/policy/v2\ngroups:\n  all:access-roster:operator: {}\n"
	for name, c := range map[string]struct{ body, want string }{
		"a grant naming an undeclared group": {
			base + "apps: {github: {catalogue: [{id: a, org: o, permissions: {contents: read}, " +
				"grants: [{group: 'x:y:z', repositories: ['*'], permissions: {contents: read}}]}]}}\n",
			"grants name groups the policy does not declare"},
		"a Slack App for an undeclared workspace": {
			base + "apps: {slack: {catalogue: [{id: a, workspace: nowhere, botScopes: ['chat:write']}]}}\n",
			"nowhere"},
		"an enabled organisation nobody binds": {
			base + "controllers: {github: {enabledOrgs: [acme]}}\n", "controllers.github.enabledOrgs names acme"},
		"an enabled workspace nobody declares": {
			base + "controllers: {slack: {enabledWorkspaces: [acme]}}\n", "controllers.slack.enabledWorkspaces names acme"},
		"two clusters on one issuer": {
			base + "exchange: {clusters: [{name: a, issuer: 'https://i'}, {name: b, issuer: 'https://i'}]}\n", "both claim the issuer"},
		"an AWS account with no audience": {
			base + "exchange: {aws: {accounts: [{account: '111122223333', name: a, issuer: 'https://i.example'}]}}\n", "an audience is required"},
		"an AWS issuer that is not https": {
			base + "exchange: {aws: {audience: x, accounts: [{account: '111122223333', name: a, issuer: 'http://i.example'}]}}\n", "not an https URL"},
		"a client requiring an undeclared group": {
			base + "clients: {c: {kind: public, requires: ['x:y:z']}}\n", "x:y:z"},
	} {
		_, err := config.Load[config.PolicyDocument](write(t, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, c.want, err)
		}
	}
}

// A misspelt key fails, in the tables as in the sections, and a key v2 retired
// says where it went.
func TestThePolicyDocumentIsStrict(t *testing.T) {
	for name, body := range map[string]string{
		"a misspelt table":       "apiVersion: sluis.truvity.github.io/policy/v2\ngruops: {}\n",
		"a misspelt matcher key": "apiVersion: sluis.truvity.github.io/policy/v2\ngroups: {a:b:c: {matchers: [{emial: a@b.c}]}}\n",
		"a misspelt section key": "apiVersion: sluis.truvity.github.io/policy/v2\nexchange: {clustres: []}\n",
		"a misspelt catalogue key": "apiVersion: sluis.truvity.github.io/policy/v2\n" +
			"apps: {github: {catalogue: [{id: a, org: o, permissions: {contents: read}, permision: x}]}}\n",
		"an unknown apiVersion":      "apiVersion: sluis.truvity.github.io/policy/v3\n",
		"another kind":               "apiVersion: sluis.truvity.github.io/serve/v2\n",
		"a secret in a cluster row":  "apiVersion: sluis.truvity.github.io/policy/v2\nexchange: {clusters: [{name: a, issuer: 'https://i', token: x}]}\n",
		"a v1 version beside v2":     "apiVersion: sluis.truvity.github.io/policy/v2\nversion: 1\n",
		"an access document section": "apiVersion: sluis.truvity.github.io/policy/v2\naccess: {groups: []}\n",
	} {
		var ce *policyconfig.Error
		if _, err := config.Load[config.PolicyDocument](write(t, body)); !errors.As(err, &ce) {
			t.Errorf("%s: want a configuration error, got %v", name, err)
		}
	}
	_, err := config.Load[config.PolicyDocument](write(t, "apiVersion: sluis.truvity.github.io/policy/v2\nversion: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "in place of version: 1") {
		t.Errorf("a retired key must say where it went: %v", err)
	}
}

// A v1 policy file, and an access document, are each one layer: a policy
// document of tables alone.
func TestAV1PolicyFileLoadsAsADocument(t *testing.T) {
	d, err := config.Load[config.PolicyDocument](write(t, "version: 1\ngroups:\n  all:access-roster:operator: {}\n"))
	if err != nil || d.APIVersion != config.APIVersion("policy") || len(d.Policy.Groups) != 1 || d.Exchange != nil {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := config.Load[config.PolicyDocument](write(t, "version: 1\ngruops: {}\n")); err == nil {
		t.Error("a v1 file with a misspelt table was accepted")
	}
}

// A v2 service document that still names a key v2 retired is refused, and the
// refusal says where the key went.
func TestARetiredKeyInAV2ServiceDocumentSaysWhereItWent(t *testing.T) {
	for _, c := range []struct {
		load func(string) error
		body string
		want []string
	}{
		{func(f string) error { _, err := config.Load[config.Serve](f); return err },
			"apiVersion: sluis.truvity.github.io/serve/v2\nissuerURL: https://a.example\npolicyDir: /p\nexchange: {clustersFile: /c}\ngithub: {owners: [a]}\n",
			[]string{"policyDir: the policy is one rendered document", "exchange.clustersFile: moved to the policy document: exchange.clusters", "github: moved"}},
		{func(f string) error { _, err := config.Load[config.ControllerGitHub](f); return err },
			"apiVersion: sluis.truvity.github.io/controller-github/v2\nconsoleURL: http://c:1\nenabledOrgs: [a]\n",
			[]string{"controllers.github.enabledOrgs"}},
		{func(f string) error { _, err := config.Load[config.ControllerSlack](f); return err },
			"apiVersion: sluis.truvity.github.io/controller-slack/v2\nconsoleURL: http://c:1\nenabledWorkspaces: [a]\n",
			[]string{"controllers.slack.enabledWorkspaces"}},
	} {
		err := c.load(write(t, c.body))
		for _, want := range c.want {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("want %q in %v", want, err)
			}
		}
	}
}

// A v1 serve document, with every file v1 named, converts: the service runs
// on it unchanged, and the policy document is what those files said.
func TestAV1ServeDocumentConvertsWithItsFiles(t *testing.T) {
	dir := t.TempDir()
	policyDir := filepath.Join(dir, "policy")
	if err := os.Mkdir(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeIn(t, policyDir, "policy.yaml", "version: 1\ngroups:\n  all:access-roster:operator: {}\nslack: {workspaces: {example: {}}}\n")
	writeIn(t, policyDir, "access.yaml", "version: 1\naccess:\n  groups:\n    - name: all:shop:viewer\n      emails: [ada@example.com]\n")
	clusters := writeIn(t, dir, "clusters.yaml", "clusters:\n  - {name: devel, issuer: 'https://k.example'}\n")
	aws := writeIn(t, dir, "aws.yaml", "audience: x\nmaxAge: 5m\naccounts:\n  - {account: '111122223333', name: apps, issuer: 'https://i.example'}\n")
	gh := writeIn(t, dir, "gh.yaml", "apps:\n  - {id: renovate, org: example-org, permissions: {contents: read}, "+
		"grants: [{group: 'all:access-roster:operator', repositories: ['*'], permissions: {contents: read}}]}\n")
	sl := writeIn(t, dir, "slack.yaml", "apps:\n  - {id: alerts, workspace: example, botScopes: ['chat:write']}\n")
	overlay := writeIn(t, dir, "overlay.yaml", "workspaces:\n  - {backend: google, admin: a@example.com, keySecret: directory/acme/key}\n")
	serve := writeIn(t, dir, "serve.yaml", "issuerURL: https://access.example\n"+
		"policyDir: "+policyDir+"\noverlayFile: "+overlay+"\n"+
		"exchange: {audience: sluis, clustersFile: "+clusters+", awsFile: "+aws+"}\n"+
		"api: {audience: directory-roster}\n"+
		"github: {owners: [example-org], runnerTiers: [small], catalogueFile: "+gh+"}\n"+
		"slack: {catalogueFile: "+sl+"}\n")
	c, err := config.LoadConfig[config.Serve](serve, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, p := c.Service, c.Policy
	if s.APIVersion != config.APIVersion("serve") || s.Exchange.Audience != "sluis" ||
		len(s.Directory.Workspaces) != 1 || s.Directory.Workspaces[0].KeySecret != "directory/declared-0/key" {
		t.Errorf("the service document: %+v", s)
	}
	if len(p.Policy.Groups) != 2 || p.Clusters()[0].Name != "devel" || p.AWS().MaxAge.D().String() != "5m0s" ||
		p.GitHubOwners()[0] != "example-org" || p.RunnerTiers()[0] != "small" ||
		len(p.GitHubCatalogue().Apps) != 1 || len(p.SlackCatalogue().Apps) != 1 {
		t.Errorf("the policy document: %+v %+v", p.Exchange, p.Apps)
	}
	// The service document alone does not read the policy's files: migrate
	// reads it on a workstation where they are not.
	if err := os.RemoveAll(policyDir); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load[config.Serve](serve); err != nil {
		t.Errorf("the service document alone read the policy: %v", err)
	}
}

func TestAV1ControllerDocumentConverts(t *testing.T) {
	dir := t.TempDir()
	writeIn(t, dir, "policy.yaml", "version: 1\ngroups: {a:b:c: {}}\ngithub: {example-org: {members: [a:b:c]}}\nslack: {workspaces: {example: {}}}\n")
	gh := writeIn(t, t.TempDir(), "c.yaml", "policyDir: "+dir+"\nconsoleURL: http://c:1/console\nenabledOrgs: [example-org]\n")
	c, err := config.LoadConfig[config.ControllerGitHub](gh, nil)
	if err != nil || c.Policy.EnabledOrgs()[0] != "example-org" || c.Service.APIVersion != config.APIVersion("controller-github") {
		t.Fatalf("%+v %v", c, err)
	}
	sl := writeIn(t, t.TempDir(), "c.yaml", "policyDir: "+dir+"\nconsoleURL: http://c:1/console\nenabledWorkspaces: [example]\n")
	d, err := config.LoadConfig[config.ControllerSlack](sl, nil)
	if err != nil || d.Policy.EnabledWorkspaces()[0] != "example" {
		t.Fatalf("%+v %v", d, err)
	}
	bad := writeIn(t, t.TempDir(), "c.yaml", "policyDir: "+dir+"\nconsoleURL: http://c:1/console\nenabledOrgs: [acme]\n")
	if _, err := config.LoadConfig[config.ControllerGitHub](bad, nil); err == nil || !strings.Contains(err.Error(), "acme") {
		t.Errorf("an enabled organisation the policy does not bind was accepted: %v", err)
	}
}

// A v2 service document names its policy document, and nothing else.
func TestAV2ServeDocumentReadsItsPolicyDocument(t *testing.T) {
	dir := t.TempDir()
	pol := writeIn(t, dir, "policy.yaml", string(fullPolicy(t)))
	serve := writeIn(t, dir, "serve.yaml", "apiVersion: sluis.truvity.github.io/serve/v2\nissuerURL: https://access.example\npolicy: {file: "+pol+"}\n")
	c, err := config.LoadConfig[config.Serve](serve, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Policy == nil || len(c.Policy.Policy.Groups) != 3 {
		t.Errorf("the policy document was not read: %+v", c.Policy)
	}
	fallback := policy.Policy{Version: 1, Groups: map[string]policy.Group{"all:access-roster:operator": {}}}
	none := writeIn(t, dir, "none.yaml", "apiVersion: sluis.truvity.github.io/serve/v2\nissuerURL: https://access.example\n")
	if c, err = config.LoadConfig[config.Serve](none, &fallback); err != nil || len(c.Policy.Policy.Groups) != 1 {
		t.Errorf("a document naming no policy decides by the fallback: %+v %v", c.Policy, err)
	}
}

// Render merges every kind of layer into one document and holds it to the
// same checks.
func TestRenderMergesEveryKindOfLayer(t *testing.T) {
	dir := t.TempDir()
	writeIn(t, dir, "10-policy.yaml", "version: 1\ngroups:\n  all:access-roster:operator: {}\ngithub: {example-org: {members: ['all:access-roster:operator']}}\n")
	writeIn(t, dir, "20-access.yaml", "version: 1\naccess:\n  groups:\n    - name: all:shop:viewer\n      emails: [ada@example.com]\n")
	writeIn(t, dir, "30-clusters.yaml", "apiVersion: sluis.truvity.github.io/policy/v2\n"+
		"exchange: {clusters: [{name: devel, issuer: 'https://k.example'}], github: {owners: [a]}}\n")
	writeIn(t, dir, "40-more.yaml", "apiVersion: sluis.truvity.github.io/policy/v2\nexchange: {github: {owners: [a, b]}}\n"+
		"controllers: {github: {enabledOrgs: [example-org]}}\n")
	writeIn(t, dir, "README.md", "not a layer")
	d, err := config.Render(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Policy.Groups) != 2 || len(d.Clusters()) != 1 || !slices.Equal(d.GitHubOwners(), []string{"a", "b"}) || d.EnabledOrgs()[0] != "example-org" {
		t.Errorf("render: %+v %+v %+v", d.Policy.Groups, d.Exchange, d.Controllers)
	}
	writeIn(t, dir, "50-clash.yaml", "version: 1\ngroups:\n  all:shop:viewer: {}\n")
	if _, err := config.Render(dir); err == nil || !strings.Contains(err.Error(), "50-clash.yaml") {
		t.Errorf("a group declared twice must name the file: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "50-clash.yaml")); err != nil {
		t.Fatal(err)
	}
	writeIn(t, dir, "60-bad.yaml", "apiVersion: sluis.truvity.github.io/policy/v2\ncontrollers: {slack: {enabledWorkspaces: [nowhere]}}\n")
	if _, err := config.Render(dir); err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("a render is held to the loader's checks: %v", err)
	}
}

// With no --config, SLUIS_CONFIG names the document; --config wins.
func TestTheCommandLineFallsBackToSLUISCONFIG(t *testing.T) {
	var out bytes.Buffer
	t.Setenv("SLUIS_CONFIG", "/env.yaml")
	if file, _, err := config.Command("sluis serve", "serve", nil, &out); err != nil || file != "/env.yaml" {
		t.Errorf("env: %q %v", file, err)
	}
	if file, _, err := config.Command("sluis serve", "serve", []string{"--config", "/flag.yaml"}, &out); err != nil || file != "/flag.yaml" {
		t.Errorf("flag: %q %v", file, err)
	}
}

// exchange.aws: what the AWS account file used to be held to at start.
func TestTheAWSRowsAreHeldToTheirRules(t *testing.T) {
	row := func(extra string) string {
		return "apiVersion: sluis.truvity.github.io/policy/v2\nexchange:\n  aws:\n    audience: a\n    accounts:\n      - account: \"111122223333\"\n" +
			"        name: apps\n        issuer: https://x.example\n" + extra
	}
	good := row("        orgId: o-abc1234567\n        algs: [ES384]\n" +
		"      - {account: \"444455556666\", name: data, issuer: \"https://z.example/\", jwksUri: \"https://keys.example/jwks.json\"}\n")
	d, err := config.Load[config.PolicyDocument](write(t, good))
	if err != nil {
		t.Fatal(err)
	}
	if a := d.AWS(); len(a.Accounts) != 2 || a.Accounts[1].Issuer != "https://z.example" {
		t.Errorf("rows = %+v", a.Accounts)
	}
	for name, body := range map[string]string{
		"short account":     strings.Replace(row(""), "111122223333", "1234", 1),
		"http issuer":       strings.Replace(row(""), "https://x", "http://x", 1),
		"no name":           strings.Replace(row(""), "name: apps", "name: \" \"", 1),
		"bad alg":           row("        algs: [ES256]\n"),
		"unknown key":       row("        orgid: o-1\n"),
		"maxAge too long":   strings.Replace(row(""), "audience: a\n", "audience: a\n    maxAge: 2h\n", 1),
		"duplicate account": row("      - {account: \"111122223333\", name: b, issuer: \"https://y.example\"}\n"),
		"duplicate issuer":  row("      - {account: \"444455556666\", name: b, issuer: \"https://x.example/\"}\n"),
		"duplicate name":    row("      - {account: \"444455556666\", name: apps, issuer: \"https://y.example\"}\n"),
		"http jwksUri":      row("        jwksUri: http://x.example/k\n"),
	} {
		if _, err := config.Load[config.PolicyDocument](write(t, body)); err == nil {
			t.Errorf("%s: loaded, want a refusal", name)
		}
	}
}

// exchange.clusters: a row that would verify nothing, and two rows for one
// issuer, are refused.
func TestTheClusterRowsAreHeldToTheirRules(t *testing.T) {
	for name, rows := range map[string]string{
		"no name":                 "[{issuer: 'https://one.example'}]",
		"no issuer":               "[{name: mgmt}]",
		"two rows for one issuer": "[{name: mgmt, issuer: 'https://one.example'}, {name: devel, issuer: 'https://one.example'}]",
	} {
		if _, err := config.Load[config.PolicyDocument](write(t, "apiVersion: sluis.truvity.github.io/policy/v2\nexchange: {clusters: "+rows+"}\n")); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// directory.workspaces: the three keys that cannot be discovered are required,
// and a misspelt key is refused rather than declaring nothing.
func TestADeclaredWorkspaceIsHeldToItsShape(t *testing.T) {
	head := "apiVersion: sluis.truvity.github.io/serve/v2\nissuerURL: https://a.example\ndirectory:\n  workspaces:\n"
	whole := head + "    - {backend: google, admin: a@b.c, keySecret: directory/acme/key, id: C0, serve: [b.c]}\n"
	if _, err := config.Load[config.Serve](write(t, whole)); err != nil {
		t.Errorf("a whole declaration was refused: %v", err)
	}
	for name, ws := range map[string]string{
		"no backend":  "{admin: a@b.c, keySecret: directory/acme/key}",
		"no admin":    "{backend: google, keySecret: directory/acme/key}",
		"no key":      "{backend: google, admin: a@b.c}",
		"unknown key": "{backend: google, adminEmail: a@b.c, admin: a@b.c, keySecret: directory/acme/key}",
	} {
		if _, err := config.Load[config.Serve](write(t, head+"    - "+ws+"\n")); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A v1 serve document's secrets, named by a variable or a file, get the names
// v2 gives them, and the converted document remembers where v1 said each was.
func TestAV1ServeDocumentsSecretsAreNamedAndLocated(t *testing.T) {
	dir := t.TempDir()
	overlay := writeIn(t, dir, "overlay.yaml", "workspaces:\n  - {id: C0acme, backend: google, admin: a@example.com, keyFile: /keys/acme.json}\n")
	serve := writeIn(t, dir, "serve.yaml", "issuerURL: https://access.example\n"+
		"overlayFile: "+overlay+"\n"+
		"valkey: {address: 'v:6379', passwordEnv: OLD_VALKEY}\n"+
		"oauthClient: {idFile: /oauth/id, secretEnv: OLD_OAUTH}\n"+
		"adminPasswordEnv: OLD_ADMIN\n"+
		"clientSecretsDir: /clients\n"+
		"signingKey: {kms: {keys: [alias/a], stateSecretFile: /state}}\n")
	s, err := config.Load[config.Serve](serve)
	if err != nil {
		t.Fatal(err)
	}
	if s.Valkey.LoginSecret != "valkey/password" || s.OAuthClient.Provider != "default" ||
		s.Recovery.LoginSecret != "recovery/password" || s.SigningKey.KMS.StateSecret != "issuer/state-secret" ||
		s.Directory.Workspaces[0].KeySecret != "directory/C0acme/key" {
		t.Errorf("the names: %+v %+v %+v %+v", s.Valkey, s.OAuthClient, s.Recovery, s.SigningKey.KMS)
	}
	locations, clientDir := s.LegacySecrets()
	want := map[string]config.SecretLocation{
		"valkey/password":                        {Env: "OLD_VALKEY"},
		"providers/google/default/client-id":     {File: "/oauth/id"},
		"providers/google/default/client-secret": {Env: "OLD_OAUTH"},
		"recovery/password":                      {Env: "OLD_ADMIN"},
		"issuer/state-secret":                    {File: "/state"},
		"directory/C0acme/key":                   {File: "/keys/acme.json"},
	}
	if !reflect.DeepEqual(locations, want) || clientDir != "/clients" || !s.Converted() {
		t.Errorf("the locations: %+v %q", locations, clientDir)
	}
}

func TestTheRetiredLambdaVariablesAreRefused(t *testing.T) {
	err := config.RefuseRetired("serve", []string{"SLUIS_SECRET_FILES=[]", "OAUTH=ssm:/sluis/private/config/oauth/client-secret"})
	if err == nil || !strings.Contains(err.Error(), "SLUIS_SECRET_FILES") || !strings.Contains(err.Error(), "OAUTH (now") {
		t.Errorf("%v", err)
	}
	if err := config.RefuseRetired("controller-github", []string{"SLUIS_CONFIG_FILE=/var/task/config/github.yaml"}); err == nil {
		t.Error("SLUIS_CONFIG_FILE was accepted")
	}
}
