package config_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/config"
	internalconfig "github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/policy"
)

var update = flag.Bool("update", false, "rewrite the golden documents")

// fixtures are the installations the golden documents are rendered from: the
// first is shaped like a Lambda estate, the second like a Kubernetes one. The
// names are the files in testdata/: <name>.installation.yaml in,
// <name>.sluis.yaml and <name>.policy.yaml out.
var fixtures = []string{"example", "truvity"}

func render(t *testing.T, name string) (service, policy []byte) {
	t.Helper()
	in, err := config.LoadInstallation(filepath.Join("testdata", name+".installation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	service, policy, err = config.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	return service, policy
}

// The rendered documents of a realistic installation are a file in git: a
// change to what is rendered is a diff here, reviewed. Run with -update to
// write them.
func TestRenderMatchesTheGoldenDocuments(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			service, policy := render(t, name)
			for suffix, got := range map[string][]byte{"sluis": service, "policy": policy} {
				file := filepath.Join("testdata", name+"."+suffix+".yaml")
				if *update {
					if err := os.WriteFile(file, got, 0o644); err != nil { //nolint:gosec // a fixture holds no secret
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s is not what Render writes: run `go test ./config -update` and review the diff\n--- got ---\n%s", file, got)
				}
			}
		})
	}
}

// Two renders of one installation are the same bytes, and Render leaves its
// input as it found it.
func TestRenderIsDeterministicAndLeavesTheInstallationAlone(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			in, err := config.LoadInstallation(filepath.Join("testdata", name+".installation.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			before, _ := yaml.Marshal(in)
			s1, p1, err := config.Render(in)
			if err != nil {
				t.Fatal(err)
			}
			s2, p2, err := config.Render(in)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(s1, s2) || !bytes.Equal(p1, p2) {
				t.Error("two renders of one installation differ")
			}
			if after, _ := yaml.Marshal(in); !bytes.Equal(before, after) {
				t.Error("Render changed the installation it was given")
			}
		})
	}
}

// What Render writes is what the service loads: the public Load, as an estate's
// own tooling would call it, reads both and the policy it names.
func TestWhatRenderWritesLoads(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			service, policy := render(t, name)
			dir := t.TempDir()
			sf, pf := filepath.Join(dir, "sluis.yaml"), filepath.Join(dir, "policy.yaml")
			if err := os.WriteFile(sf, service, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(pf, policy, 0o600); err != nil {
				t.Fatal(err)
			}
			svc, err := config.Load[config.Sluis](sf)
			if err != nil {
				t.Fatal(err)
			}
			pol, err := config.Load[config.PolicyDocument](pf)
			if err != nil {
				t.Fatal(err)
			}
			if svc.APIVersion != config.APIVersion("sluis") || pol.APIVersion != config.APIVersion("policy") {
				t.Errorf("apiVersions %q, %q", svc.APIVersion, pol.APIVersion)
			}
			// And the documents are valid against the schemas an estate's CI holds them to.
			for file, schema := range map[string]string{sf: "sluis", pf: "policy"} {
				raw, _ := os.ReadFile(file)
				var doc any
				if err := yaml.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				if err := config.Validate(schema, doc); err != nil {
					t.Errorf("%s: %v", schema, err)
				}
			}
		})
	}
}

// The Truvity-shaped installation is the one the chart's tests render: pin the
// numbers the shape was chosen for, so a fixture that quietly shrank is seen.
func TestTheTruvityFixtureHasTheShapeItClaims(t *testing.T) {
	_, policy := render(t, "truvity")
	var doc struct {
		Clients map[string]any `yaml:"clients"`
		GitHub  map[string]any `yaml:"github"`
	}
	if err := yaml.Unmarshal(policy, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Clients) != 20 {
		t.Errorf("clients: %d, want 20", len(doc.Clients))
	}
	if len(doc.GitHub) != 2 {
		t.Errorf("github organisations: %d, want 2", len(doc.GitHub))
	}
	service, _ := render(t, "truvity")
	for _, want := range []string{"preset: k8s-aws", "adapter: openbao", "root: sluis", "namespace: staging", "kmsWrapped:", "verifyOnly:", "inCluster: true"} {
		if !strings.Contains(string(service), want) {
			t.Errorf("the service document has no %q", want)
		}
	}
}

func installation(t *testing.T, name string) *config.Installation {
	t.Helper()
	in, err := config.LoadInstallation(filepath.Join("testdata", name+".installation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// A value the shape fixes is not a choice, and what is wrong is named by its
// key, never quoted.
func TestRenderRefusesWhatTheShapeFixes(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*config.Installation)
		want   string
	}{
		"another secrets root": {func(in *config.Installation) {
			in.Secrets = &config.Secrets{Source: "ssm", Root: "/sluis/other"}
		}, "secrets.root"},
		"secrets from a file on Lambda": {func(in *config.Installation) {
			in.Secrets = &config.Secrets{Source: "file", Root: "/x"}
		}, "secrets.source"},
		"another recovery secret": {func(in *config.Installation) {
			in.Recovery = &config.Recovery{LoginSecret: "elsewhere"}
		}, "recovery.passwordSecret"},
		"no region": {func(in *config.Installation) { in.AWS.Region = "" }, "aws.region"},
		"a console client nobody declares": {func(in *config.Installation) {
			in.Console = &config.Console{Client: "ghost"}
		}, "console.client"},
		"an unknown concern": {func(in *config.Installation) {
			in.Adapters = map[string]config.AdapterChoice{"queue": {Adapter: "x"}}
		}, "adapters.queue"},
		"a bad instance": {func(in *config.Installation) { in.Instance = "private" }, "instance"},
		"no issuer":      {func(in *config.Installation) { in.Issuer.URL = "" }, "issuer.url"},
	} {
		t.Run(name, func(t *testing.T) {
			in := installation(t, "example")
			c.change(in)
			_, _, err := config.Render(in)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want one naming %q", err, c.want)
			}
		})
	}
}

// A Lambda installation keeps the layout and the grace of its secrets: the
// shape fixes the source, the root and the region, not the storage layout.
func TestRenderKeepsTheSecretsLayoutAndGraceOnLambda(t *testing.T) {
	in := installation(t, "example")
	grace := config.Duration(12 * time.Hour)
	in.Secrets = &config.Secrets{Layout: "v4", Grace: &grace}
	service, _, err := config.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Secrets map[string]any `yaml:"secrets"`
	}
	if err := yaml.Unmarshal(service, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Secrets["layout"] != "v4" || doc.Secrets["grace"] != "12h0m0s" && doc.Secrets["grace"] != "12h" {
		t.Errorf("secrets %v, want layout v4 and grace 12h\n%s", doc.Secrets, service)
	}
	if doc.Secrets["source"] != "ssm" || doc.Secrets["root"] != "/sluis/example" {
		t.Errorf("secrets %v lost what the shape fixes", doc.Secrets)
	}
}

// What the policy checks across its sections is held on the installation's
// way through: an organisation the controller may change must be bound.
func TestRenderHoldsTheOutputsToTheLoader(t *testing.T) {
	in := installation(t, "example")
	in.Controllers.GitHub.EnabledOrgs = []string{"unbound-org"}
	if _, _, err := config.Render(in); err == nil || !strings.Contains(err.Error(), "unbound-org") {
		t.Errorf("a controller that may change an unbound organisation was rendered: %v", err)
	}
}

// An adapter the installation names replaces what the resources stand for, and
// the others are still filled in.
func TestANamedAdapterWinsOverAResource(t *testing.T) {
	in := installation(t, "example")
	in.Adapters = map[string]config.AdapterChoice{"audit": {Adapter: "log"}}
	service, _, err := config.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(service)
	if !strings.Contains(s, "adapter: log") || strings.Contains(s, "adapter: sqs") {
		t.Errorf("audit was not replaced:\n%s", s)
	}
	if !strings.Contains(s, "adapter: s3") {
		t.Errorf("blobs were not filled in:\n%s", s)
	}
}

// The schema holds an installation to its keys: a misspelt one is refused,
// naming it, before anything is rendered.
func TestTheSchemaRefusesAnUnknownKey(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("testdata", "example.installation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "installation.yaml")
	if err := os.WriteFile(file, append(raw, []byte("\nissuer2: {}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadInstallation(file); err == nil || !strings.Contains(err.Error(), "issuer2") {
		t.Errorf("error %v, want one naming issuer2", err)
	}
}

// The preset follows the shape when none is named. The presets that name an
// adapter which is not built are refused at render, not at start.
func TestThePresetFollowsTheShape(t *testing.T) {
	for _, c := range []struct {
		shape  config.Shape
		aws    bool
		openb  bool
		preset string
		ok     bool
	}{
		{config.ShapeLambda, true, false, "aws-hybrid", true},
		{config.ShapeKubernetes, true, false, "k8s-aws", true},
		{config.ShapeKubernetes, false, true, "k8s-openbao", false},
		{config.ShapeKubernetes, false, false, "k8s-minimal", false},
		{config.ShapeServer, false, false, "server", false},
	} {
		in := &config.Installation{Instance: "t", Shape: c.shape, Issuer: config.Issuer{URL: "https://access.example.test"}}
		if c.aws {
			in.AWS = &config.AWS{Region: "eu-central-1"}
		}
		if c.openb {
			in.OpenBao = &config.OpenBao{
				Address: "https://openbao.example.test", Root: "sluis",
				Auth: config.OpenBaoLogin{Method: "kubernetes", Role: "sluis"},
			}
		}
		service, _, err := config.Render(in)
		if !c.ok {
			if err == nil || !strings.Contains(err.Error(), "preset "+c.preset) {
				t.Errorf("%s: want a refusal naming the preset, got %v", c.preset, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.preset, err)
		}
		if !strings.Contains(string(service), "preset: "+c.preset+"\n") {
			t.Errorf("shape %s: want preset %s:\n%s", c.shape, c.preset, service)
		}
	}
}

// The installation type and its schema describe the same keys, in both
// directions: a file that sets every key the schema has decodes with none left
// over, and writing the type back gives the file again. A key added to the
// schema and not the type is refused by the first; one added to the type and
// not the schema, by the second.
func TestTheInstallationTypeAndItsSchemaDescribeTheSameKeys(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "full.installation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateInstallation(doc); err != nil {
		t.Fatalf("the full example does not validate: %v", err)
	}
	asJSON, _ := json.Marshal(doc)
	var in config.Installation
	dec := json.NewDecoder(bytes.NewReader(asJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		t.Fatalf("the type does not hold a key the example sets: %v", err)
	}
	back, err := json.Marshal(&in)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	_ = json.Unmarshal(asJSON, &want)
	_ = json.Unmarshal(back, &got)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("the type does not write back what the example sets:\n want %s\n  got %s", asJSON, back)
	}
	// And the example sets EVERY property the schema has, at the top level and
	// in each of the installation's own sections.
	var schema struct {
		Properties map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(config.InstallationSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	for key, section := range schema.Properties {
		set, ok := doc[key]
		if !ok {
			t.Errorf("the full example does not set %s", key)
			continue
		}
		own := map[string]bool{"issuer": true, "aws": true, "openbao": true, "exchange": true, "controllers": true, "cloudflare": true}
		if m, isMap := set.(map[string]any); isMap && own[key] {
			for sub := range section.Properties {
				if _, ok := m[sub]; !ok {
					t.Errorf("the full example does not set %s.%s", key, sub)
				}
			}
		}
	}
}

// The adapters table is free-form settings, and what Render carries into the
// document is held to the rules a document keeps: no credential value, https
// for an OpenBao, and nothing that contradicts the shape or the preset.
func TestRenderRefusesAnAdapterTableThatBreaksTheRules(t *testing.T) {
	choice := func(adapter string, settings map[string]any) map[string]config.AdapterChoice {
		return map[string]config.AdapterChoice{"secrets": {Adapter: adapter, Settings: settings}}
	}
	for name, c := range map[string]struct {
		base   string
		change func(*config.Installation)
		want   string
	}{
		"a token value in settings": {"truvity", func(in *config.Installation) {
			in.Adapters = choice("openbao", map[string]any{"address": "https://openbao.example.test", "token": "x", "root": "sluis"})
		}, "adapters.secrets.settings.token"},
		"a nested password": {"truvity", func(in *config.Installation) {
			in.Adapters = choice("openbao", map[string]any{"address": "https://openbao.example.test", "auth": map[string]any{"password": "x"}})
		}, "settings.auth.password"},
		"a secret key name": {"example", func(in *config.Installation) {
			in.Adapters = map[string]config.AdapterChoice{"state": {Adapter: "dynamodb", Settings: map[string]any{"table": "t", "secretAccessKey": "x"}}}
		}, "secretAccessKey"},
		"an http OpenBao": {"truvity", func(in *config.Installation) {
			in.Adapters = choice("openbao", map[string]any{"address": "http://openbao.example.test", "root": "sluis"})
		}, "https"},
		"secrets other than ssm on Lambda": {"example", func(in *config.Installation) {
			in.Adapters = choice("openbao", map[string]any{"address": "https://openbao.example.test", "root": "sluis"})
		}, "shape lambda keeps its secrets in ssm"},
		"signing by file on Lambda": {"example", func(in *config.Installation) {
			in.Adapters = map[string]config.AdapterChoice{"signing": {Adapter: "file"}}
		}, "shape lambda signs with"},
		"an OpenBao on Lambda": {"example", func(in *config.Installation) {
			in.OpenBao = &config.OpenBao{Address: "https://openbao.example.test", Root: "sluis", Auth: config.OpenBaoLogin{Method: "jwt", Role: "r"}}
		}, "openbao is set"},
		"an adapter that contradicts the preset": {"truvity", func(in *config.Installation) {
			in.Adapters = map[string]config.AdapterChoice{"state": {Adapter: "postgres"}}
		}, "contradicts preset"},
		"ssm secrets beside an openbao adapter": {"truvity", func(in *config.Installation) {
			in.Secrets = &config.Secrets{Source: "ssm", Root: "/sluis/staging"}
		}, "secrets.source is ssm"},
	} {
		t.Run(name, func(t *testing.T) {
			in := installation(t, c.base)
			c.change(in)
			if _, _, err := config.Render(in); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want one naming %q", err, c.want)
			}
		})
	}
	// The allowed override still works: OpenBao secrets on k8s-aws (the fixture), a ...File and a ...Secret key.
	in := installation(t, "truvity")
	in.Adapters = choice("openbao", map[string]any{
		"address": "https://openbao.example.test", "root": "sluis", "stateSecret": "x/y",
		"auth": map[string]any{"method": "jwt", "role": "r", "tokenFile": "/t"},
	})
	if _, _, err := config.Render(in); err != nil {
		t.Errorf("an allowed override was refused: %v", err)
	}
}

// Cloudflare: the accounts and presets go to the service document, the grants to
// the policy document, and a grant for a preset nobody declared is refused.
func TestCloudflareIsSplitBetweenTheTwoDocuments(t *testing.T) {
	in := installation(t, "example")
	in.Access.Groups["all:infra:dns-editors"] = policy.Group{}
	in.Cloudflare = &config.Cloudflare{Grants: []internalconfig.CloudflareGrant{{Group: "all:infra:dns-editors", Presets: []string{"dns"}}}}
	in.Cloudflare.Accounts = map[string]internalconfig.CloudflareAccount{
		"main": {ID: "0123456789abcdef0123456789abcdef", Minter: "internal/cloudflare/main/minter"},
	}
	in.Cloudflare.Presets = map[string]internalconfig.CloudflarePreset{"dns": {
		Account: "main", Prototype: "proto-dns-0001", Description: "DNS",
		Lifetime: internalconfig.Duration(15 * time.Minute), Rotation: internalconfig.Duration(5 * time.Minute),
	}}
	service, pol, err := config.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(service), "cloudflare:") || strings.Contains(string(service), "grants:") {
		t.Errorf("service document:\n%s", service)
	}
	if !strings.Contains(string(pol), "cloudflare:") || !strings.Contains(string(pol), "dns-editors") {
		t.Errorf("policy document:\n%s", pol)
	}
	in.Cloudflare.Grants[0].Presets = []string{"missing"}
	if _, _, err = config.Render(in); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("a grant for an undeclared preset: %v", err)
	}
}
