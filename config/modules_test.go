package config_test

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/config"
	internalconfig "github.com/truvity/sluis/internal/config"
)

// moduleFixtures are the installations the per-module documents are rendered
// from: a small Lambda estate (one controller, no directory, no Cloudflare) and
// a larger one (both controllers, a workspace, Cloudflare presets, a module
// function under a name of its own). The names are the files in testdata/:
// <name>.installation.yaml in, <name>.<module>.yaml out.
var moduleFixtures = []string{"modules-small", "modules-full"}

func renderModules(t *testing.T, name string) (*config.Installation, *config.ModuleDocuments) {
	t.Helper()
	in, err := config.LoadInstallation(filepath.Join("testdata", name+".installation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := config.RenderModules(in, config.ModuleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return in, docs
}

// every returns the documents by the suffix of their golden file.
func every(d *config.ModuleDocuments) map[string][]byte {
	out := map[string][]byte{"issuer": d.Issuer, "policy": d.Policy}
	for m, doc := range d.Modules {
		out[m] = doc
	}
	return out
}

// Each module's document, the issuer's and the policy are a file in git for
// two installations: a change to what a function reads is a diff here.
func TestRenderModulesMatchesTheGoldenDocuments(t *testing.T) {
	for _, name := range moduleFixtures {
		t.Run(name, func(t *testing.T) {
			_, docs := renderModules(t, name)
			got := every(docs)
			for _, suffix := range slices.Sorted(maps.Keys(got)) {
				file := filepath.Join("testdata", name+"."+suffix+".yaml")
				if *update {
					if err := os.WriteFile(file, got[suffix], 0o644); err != nil { //nolint:gosec // a fixture holds no secret
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got[suffix], want) {
					t.Errorf("%s is not what RenderModules writes: run `go test ./config -update` and review the diff\n--- got ---\n%s", file, got[suffix])
				}
			}
			// A golden file with no document is a stale one.
			files, _ := filepath.Glob(filepath.Join("testdata", name+".*.yaml"))
			for _, f := range files {
				suffix := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), name+"."), ".yaml")
				if suffix == "installation" {
					continue
				}
				if _, ok := got[suffix]; !ok && !*update {
					t.Errorf("%s has no document: delete it", f)
				}
			}
		})
	}
}

// Each document validates against the schema of its kind, and loads as its
// function loads it.
func TestEveryModuleDocumentValidatesAgainstItsSchema(t *testing.T) {
	kinds := map[string]string{
		config.ModuleIssuer: "sluis", config.ModuleGitHub: "controller-github", config.ModuleGitHubMint: "controller-github",
		config.ModuleSlack: "controller-slack", config.ModuleGoogle: "sluis-google", config.ModuleCloudflare: "sluis",
	}
	for _, name := range moduleFixtures {
		_, docs := renderModules(t, name)
		for module, doc := range every(docs) {
			kind, ok := kinds[module]
			if module == "policy" {
				kind, ok = "policy", true
			}
			if !ok {
				t.Errorf("%s: %s is not a known document", name, module)
				continue
			}
			var generic any
			if err := yaml.Unmarshal(doc, &generic); err != nil {
				t.Fatalf("%s %s: %v", name, module, err)
			}
			if err := config.Validate(kind, generic); err != nil {
				t.Errorf("%s %s against %s: %v", name, module, kind, err)
			}
			if module != config.ModuleIssuer && module != "policy" {
				if err := config.ValidateModule(module, doc, docs.Policy); err != nil {
					t.Errorf("%s %s: %v", name, module, err)
				}
			}
		}
	}
}

// A module with nothing to do has no document: the Cloudflare module of an
// installation that declares no preset.
func TestTheCloudflareModuleHasADocumentOnlyWithPresets(t *testing.T) {
	_, small := renderModules(t, "modules-small")
	if _, has := small.Modules[config.ModuleCloudflare]; has {
		t.Error("the Cloudflare module has a document and no preset")
	}
	if strings.Contains(string(small.Issuer), "cloudflare") {
		t.Errorf("the issuer calls a Cloudflare module that does not exist:\n%s", small.Issuer)
	}
	for _, m := range []string{config.ModuleGitHub, config.ModuleGitHubMint, config.ModuleSlack, config.ModuleGoogle} {
		if _, has := small.Modules[m]; !has {
			t.Errorf("no document for %s", m)
		}
	}
	_, full := renderModules(t, "modules-full")
	if _, has := full.Modules[config.ModuleCloudflare]; !has {
		t.Error("no Cloudflare document with a preset")
	}
}

// The issuer calls each module by its function, unqualified: the caller's class
// picks the alias.
func TestTheIssuerCallsEachModuleByItsFunction(t *testing.T) {
	in, docs := renderModules(t, "modules-full")
	svc := load[internalconfig.Sluis](t, docs.Issuer)
	want := map[string]string{
		"github": "sluis-staging-github", "slack": "sluis-staging-chat", "google": "sluis-staging-google",
		"cloudflare": "sluis-staging-cloudflare",
	}
	got := map[string]string{}
	for m, r := range svc.Targets() {
		got[m] = r.Function
	}
	if !equal(got, want) {
		t.Errorf("targets %v, want %v", got, want)
	}
	for m, f := range want {
		if n := config.FunctionNameOf(in, m); n != f {
			t.Errorf("%s: FunctionNameOf %q, want %q", m, n, f)
		}
	}
	if n := config.FunctionNameOf(in, "github-mint"); n != "sluis-staging-token-mint" {
		t.Errorf("github-mint: %q", n)
	}
	// What the modules own is not in the issuer's document.
	for _, key := range []string{"\ncontrollers:", "\ndirectory:", "accounts:", "presets:"} {
		if strings.Contains(string(docs.Issuer), key) {
			t.Errorf("the issuer's document holds %s", key)
		}
	}
	// A run-now starts the controller's function.
	trigger := svc.Adapters["trigger"]
	if trigger.Settings["github"] != "sluis-staging-github" || trigger.Settings["slack"] != "sluis-staging-chat" {
		t.Errorf("trigger %v", trigger)
	}
}

// A module opens its own table and no other, reads its secrets as layout v5 and
// names the policy file the issuer does where it reads one.
func TestAModuleDocumentOpensItsOwnTableAndReadsLayoutV5(t *testing.T) {
	in, err := config.LoadInstallation(filepath.Join("testdata", "modules-full.installation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := config.RenderModules(in, config.ModuleOptions{Tables: map[string]string{"slack": "chat-table"}})
	if err != nil {
		t.Fatal(err)
	}
	wantTable := map[string]string{
		config.ModuleGitHub: "github:sluis-staging-github", config.ModuleGitHubMint: "github:sluis-staging-github",
		config.ModuleSlack: "slack:chat-table", config.ModuleGoogle: "google:sluis-staging-google",
		config.ModuleCloudflare: "cloudflare:sluis-staging-cloudflare",
	}
	issuer := load[internalconfig.Sluis](t, docs.Issuer)
	for module, doc := range docs.Modules {
		var generic struct {
			Policy  *struct{ File string }
			Secrets struct{ Source, Layout, Root string }
			Ports   struct {
				Adapter  string
				DynamoDB struct{ Tables map[string]string } `yaml:"dynamodb"`
			}
		}
		if err := yaml.Unmarshal(doc, &generic); err != nil {
			t.Fatal(err)
		}
		if len(generic.Ports.DynamoDB.Tables) != 1 {
			t.Errorf("%s opens %v: one table, its own", module, generic.Ports.DynamoDB.Tables)
		}
		for m, n := range generic.Ports.DynamoDB.Tables {
			if m+":"+n != wantTable[module] {
				t.Errorf("%s opens %s:%s, want %s", module, m, n, wantTable[module])
			}
		}
		if generic.Secrets.Layout != "v5" || generic.Secrets.Source != "ssm" || generic.Secrets.Root != config.SSMRoot("staging") {
			t.Errorf("%s secrets %+v", module, generic.Secrets)
		}
		switch module {
		case config.ModuleGoogle:
			if generic.Policy != nil {
				t.Error("the Google module names a policy it does not read")
			}
		default:
			if generic.Policy == nil || generic.Policy.File != issuer.Policy.File {
				t.Errorf("%s policy %v, want the issuer's %s", module, generic.Policy, issuer.Policy.File)
			}
		}
	}
	// The mint-only function asks nothing of the console and so holds no proof.
	if strings.Contains(string(docs.Modules[config.ModuleGitHubMint]), "console:") || !strings.Contains(string(docs.Modules[config.ModuleGitHub]), "console:") {
		t.Error("the console proof is the controller function's and not the mint's")
	}
}

// What a function declares it reads is its own: the sets are disjoint, so a
// module's role and check never name another's secrets. (The blob store's
// credential is the one secret two modules share; the pulumi library's test
// holds it.)
func TestTheDeclaredSecretsOfTheModulesAreDisjoint(t *testing.T) {
	_, docs := renderModules(t, "modules-full")
	owners := map[string]string{}
	count := map[string]int{}
	check := func(module string, doc []byte) {
		secrets, err := config.ModuleSecrets(module, doc, docs.Policy)
		if err != nil {
			t.Fatalf("%s: %v", module, err)
		}
		count[module] = len(secrets)
		for _, s := range secrets {
			if s.Kind == "blob-credentials" {
				continue
			}
			key := s.Kind + "/" + s.Subject + "/" + s.Name
			if other, dup := owners[key]; dup {
				t.Errorf("%s declares %s, which %s declares too", module, key, other)
			}
			owners[key] = module
		}
	}
	check(config.ModuleIssuer, docs.Issuer)
	for m, doc := range docs.Modules {
		check(m, doc)
	}
	if count[config.ModuleGoogle] != 1 || count[config.ModuleCloudflare] != 1 {
		t.Errorf("google declares %d, cloudflare %d: a workspace key and a minter each", count[config.ModuleGoogle], count[config.ModuleCloudflare])
	}
	if count[config.ModuleGitHub]+count[config.ModuleGitHubMint]+count[config.ModuleSlack] != 0 {
		t.Errorf("a controller declares a secret: %v", count)
	}
	// The issuer's are the rest: no workspace key and no minter.
	for key, module := range owners {
		if module == config.ModuleIssuer && (strings.HasPrefix(key, "workspace-key/") || strings.HasPrefix(key, "cloudflare-minter/")) {
			t.Errorf("the issuer declares %s", key)
		}
	}
}

func TestRenderModulesIsDeterministicAndLeavesTheInstallationAlone(t *testing.T) {
	for _, name := range moduleFixtures {
		in, first := renderModules(t, name)
		before, _ := yaml.Marshal(in)
		second, err := config.RenderModules(in, config.ModuleOptions{})
		if err != nil {
			t.Fatal(err)
		}
		after, _ := yaml.Marshal(in)
		if !bytes.Equal(before, after) {
			t.Errorf("%s: RenderModules changed the installation", name)
		}
		a, b := every(first), every(second)
		for k := range a {
			if !bytes.Equal(a[k], b[k]) {
				t.Errorf("%s: two renders of %s differ", name, k)
			}
		}
	}
}

func TestRenderModulesRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*config.Installation)
		want   string
	}{
		"a kubernetes installation": {func(in *config.Installation) { in.Shape = config.ShapeKubernetes }, "Lambda"},
		"the layout v4 table":       {func(in *config.Installation) { in.AWS.Table = "t" }, "layout v4"},
		"layout v4 secrets":         {func(in *config.Installation) { in.Secrets = &config.Secrets{Layout: "v4"} }, "layout v5"},
		"a function that is none":   {func(in *config.Installation) { in.AWS.Functions = map[string]string{"nonesuch": "x"} }, "not a function"},
		"a bad function name":       {func(in *config.Installation) { in.AWS.Functions = map[string]string{"slack": "a b"} }, "letters, digits"},
		"a name taken twice": {func(in *config.Installation) {
			in.AWS.Functions = map[string]string{"slack": "sluis-staging-github"}
		}, "both"},
		"the issuer's name taken": {func(in *config.Installation) { in.AWS.Functions = map[string]string{"google": "sluis-staging-issuer"} }, "both"},
		"a remote of its own": {func(in *config.Installation) {
			in.Cloudflare.Remote = &internalconfig.CloudflareRemote{Function: "x"}
		}, "cloudflare.remote"},
	} {
		in, err := config.LoadInstallation(filepath.Join("testdata", "modules-full.installation.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		c.change(in)
		if _, err := config.RenderModules(in, config.ModuleOptions{}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error naming %q", name, err, c.want)
		}
	}
}

func load[T internalconfig.Service](t *testing.T, doc []byte) *T {
	t.Helper()
	file := filepath.Join(t.TempDir(), "doc.yaml")
	if err := os.WriteFile(file, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := internalconfig.Load[T](file)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func equal(a, b map[string]string) bool { return maps.Equal(a, b) }
