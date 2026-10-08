package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	policyconfig "github.com/truvity/policy/config"
	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/config/schema"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A schema in schemas/config is what schema.Schema writes. The binaries read
// the committed file, and a chart's tests validate against it, so a change to
// the builder that is not followed by `just config-schemas` fails here as well
// as in the drift check.
func TestTheCommittedSchemasAreTheGeneratedOnes(t *testing.T) {
	for _, name := range schema.Names {
		want, ok := schema.Schema(name)
		if !ok {
			t.Fatalf("%s: no schema is built", name)
		}
		got, err := os.ReadFile(filepath.Join("..", "..", "schemas", "config", name+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is not what `just config-schemas` writes", name)
		}
	}
	got, err := os.ReadFile(filepath.Join("..", "..", "charts", "sluis", "values.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, schema.Values()) {
		t.Error("the chart's values.schema.json is not what `just config-schemas` writes")
	}
}

// Each type is held to its schema in both directions: a file that sets every
// key must decode into the type with no key left over, and writing the type
// back must give the file again. A key added to the schema and not the type is
// refused by the first; one added to the type and not the schema, by the
// second.
func TestTheTypesAndTheSchemasDescribeTheSameKeys(t *testing.T) {
	for _, c := range []struct {
		name string
		into any
	}{
		{"sluis", &config.Sluis{}},
		{"serve", &config.Serve{}},
		{"controller-github", &config.ControllerGitHub{}},
		{"controller-slack", &config.ControllerSlack{}},
	} { // The policy document is held to its type in policydoc_test.go.
		t.Run(c.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", c.name+".full.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var doc any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if err := config.Validate(c.name, doc); err != nil {
				t.Fatalf("the full example does not validate: %v", err)
			}
			asJSON, _ := json.Marshal(doc)
			dec := json.NewDecoder(bytes.NewReader(asJSON))
			dec.DisallowUnknownFields()
			if err := dec.Decode(c.into); err != nil {
				t.Fatalf("the type does not hold a key the example sets: %v", err)
			}
			back, err := json.Marshal(c.into)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			_ = json.Unmarshal(asJSON, &want)
			_ = json.Unmarshal(back, &got)
			if !jsonEqual(canonical(want), canonical(got)) {
				t.Errorf("the type does not write back what the example sets:\n want %s\n  got %s", asJSON, back)
			}
			// And the example sets EVERY property the schema has: a key left out
			// of it is one no test holds to the type.
			var s struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			body, _ := schema.Schema(c.name)
			if err := json.Unmarshal(body, &s); err != nil {
				t.Fatal(err)
			}
			for key := range s.Properties {
				if _, set := doc.(map[string]any)[key]; !set {
					t.Errorf("the full example does not set %s", key)
				}
			}
		})
	}
}

// canonical writes every duration as the span it is, so that "15m" and
// "15m0s" are the same value.
func canonical(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			out[k] = canonical(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = canonical(x)
		}
		return out
	case string:
		if d, err := time.ParseDuration(t); err == nil {
			return d.String()
		}
	}
	return v
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

const minimalIssuer = "issuerURL: https://access.example\n"

func TestAValidFileLoads(t *testing.T) {
	f, err := config.Load[config.Serve](write(t, minimalIssuer))
	if err != nil {
		t.Fatal(err)
	}
	if f.IssuerURL != "https://access.example" {
		t.Errorf("not decoded: %+v", f)
	}
	g, err := config.Load[config.ControllerGitHub](write(t, "policyDir: /p\nconsoleURL: http://c:8080/console\ninterval: 5m\nenabledOrgs: [a]\n"))
	if err != nil || g.Interval.D().String() != "5m0s" || g.APIVersion != config.APIVersion("controller-github") {
		t.Errorf("controller-github: %v %+v", err, g)
	}
	if _, err := config.Load[config.ControllerSlack](write(t, "policyDir: /p\nconsoleURL: http://c:8080/console\n")); err != nil {
		t.Errorf("controller-slack: %v", err)
	}
}

// What a typo must do: fail, and say which key.
func TestAnUnknownKeyIsRefusedAndNamed(t *testing.T) {
	_, err := config.Load[config.Serve](write(t, minimalIssuer+"valkey2: {}\nlisten: {adr: ':1'}\n"))
	var ce *policyconfig.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want a configuration error, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"'valkey2'", "listen: additional properties 'adr'"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not name %q: %s", want, msg)
		}
	}
}

func TestAMissingRequiredKeyIsRefusedAndNamed(t *testing.T) {
	if _, err := config.Load[config.Serve](write(t, "store: memory\n")); err == nil || !strings.Contains(err.Error(), "issuerURL") {
		t.Fatalf("want a refusal naming issuerURL, got %v", err)
	}
	if _, err := config.Load[config.ControllerGitHub](write(t, "consoleURL: http://c:8080\n")); err == nil || !strings.Contains(err.Error(), "policyDir") {
		t.Fatalf("want a refusal naming policyDir, got %v", err)
	}
	if _, err := config.Load[config.ControllerSlack](write(t, "policyDir: /p\n")); err == nil || !strings.Contains(err.Error(), "consoleURL") {
		t.Fatalf("want a refusal naming consoleURL, got %v", err)
	}
}

// A secret is never in the file: not under a key of its own, which no schema
// has, and not inside a URL or an address, which would have been the easy
// place.
func TestASecretInTheFileIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"a valkey password key":      minimalIssuer + "valkey: {address: 'v:6379', password: hunter2}\n",
		"a valkey address with auth": minimalIssuer + "valkey: {address: ':hunter2@v:6379'}\n",
		"an oauth secret key":        minimalIssuer + "oauthClient: {secret: hunter2}\n",
		"an admin password key":      minimalIssuer + "adminPassword: hunter2\n",
		"a password in the issuer":   "issuerURL: https://u:hunter2@access.example\n",
		"a password in the writer":   minimalIssuer + "audit: {writer: 'http://u:hunter2@audit:8080'}\n",
	} {
		_, err := config.Load[config.Serve](write(t, body))
		if err == nil {
			t.Errorf("%s was accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the error quotes the secret: %v", name, err)
		}
	}
	// A variable NAME is not a value: a value that is not a name is refused.
	if _, err := config.Load[config.Serve](write(t, minimalIssuer+"valkey: {address: 'v:6379', passwordEnv: 'hunter 2!'}\n")); err == nil {
		t.Error("a password value in a ...Env field was accepted")
	}
}

func TestAnEmptyOrMissingFileIsRefused(t *testing.T) {
	if _, err := config.Load[config.Serve](write(t, "")); err == nil {
		t.Error("an empty file was accepted")
	}
	if _, err := config.Load[config.Serve](filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("a missing file was accepted")
	}
}

// What the schema alone refuses of the values the binaries used to check in
// code: a duration that is not one, a level that is not one, a tier that is not
// one, an enum, a listener that is not an address.
func TestTheSchemaRefusesWhatTheEnvironmentWasTrustedWith(t *testing.T) {
	for name, extra := range map[string]string{
		"a duration that is not one":          "lifetimes: {token: a while}\n",
		"a zero absolute lifetime":            "lifetimes: {absolute: 0}\n",
		"a log level that is not one":         "log: {level: chatty}\n",
		"a store that is not one":             "store: postgres\n",
		"a scoping mode that is not one":      "groupsScoping: sometimes\n",
		"a runner tier that is not one":       "github: {runnerTiers: ['Not A Tier']}\n",
		"a listener that is not an address":   "listen: {address: '8080'}\n",
		"a negative trusted hop count":        "audit: {writer: 'http://a:1', forwardedForTrustedHops: -1}\n",
		"a signing key list that is a string": "signingKey: {additionalFiles: /k}\n",
		"a kms key that is not a string":      "secrets: {source: ssm, root: /sluis/example, kmsKeyId: [a]}\n",
		"a kms key on a file source":          "secrets: {source: file, root: /run/secrets, kmsKeyId: alias/example}\n",
		"a layout that is not one":            "secrets: {source: ssm, root: /sluis/example, layout: v5}\n",
		"a layout on a file source":           "secrets: {source: file, root: /run/secrets, layout: v4}\n",
	} {
		if _, err := config.Load[config.Serve](write(t, minimalIssuer+extra)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestACommandLineIsTheFileAndNothingElse(t *testing.T) {
	var out bytes.Buffer
	file, done, err := config.Command("sluis serve", "serve", []string{"--config", "/etc/c.yaml"}, &out)
	if err != nil || done || file != "/etc/c.yaml" {
		t.Errorf("--config: %q %v %v", file, done, err)
	}
	if _, done, err = config.Command("sluis serve", "serve", []string{"--version"}, &out); err != nil || !done {
		t.Errorf("--version: %v %v", done, err)
	}
	if _, done, err = config.Command("sluis serve", "serve", []string{"--help"}, &out); err != nil || !done {
		t.Errorf("--help: %v %v", done, err)
	}
	for name, args := range map[string][]string{
		"no file":       {},
		"an old flag":   {"--config", "/c", "--port", "8080"},
		"an argument":   {"--config", "/c", "extra"},
		"an empty file": {"--config", ""},
	} {
		if _, _, err := config.Command("sluis serve", "serve", args, &out); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A retired variable that is still set is refused at start, naming what
// replaces it, and only when it holds something.
func TestARetiredVariableIsRefused(t *testing.T) {
	err := config.RefuseRetired("serve", []string{"PATH=/bin", "ISSUER_URL=https://x", "VALKEY_PASSWORD=hunter2", "LOG_LEVEL="})
	if err == nil {
		t.Fatal("retired variables were accepted")
	}
	for _, want := range []string{"ISSUER_URL (now issuerURL)", "VALKEY_PASSWORD (now valkey.passwordSecret"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Errorf("the refusal quotes a value or names an empty variable: %v", err)
	}
	if err := config.RefuseRetired("serve", []string{"SECRET_MANAGERS_FILE=/x"}); err == nil || !strings.Contains(err.Error(), "v1.30.0") {
		t.Errorf("the removed secret-store view's variable must still be refused, with its reason: %v", err)
	}
	if err := config.RefuseRetired("serve", []string{"PATH=/bin", "OTEL_EXPORTER_OTLP_ENDPOINT=http://c:4318", "NAMESPACE=x"}); err != nil {
		t.Errorf("OpenTelemetry's variables and the pod's own are not retired: %v", err)
	}
	if err := config.RefuseRetired("controller-github", []string{"ENABLED_ORGS=a"}); err == nil || !strings.Contains(err.Error(), "enabledOrgs") {
		t.Errorf("controller-github: %v", err)
	}
	if err := config.RefuseRetired("controller-slack", []string{"ENABLED_WORKSPACES=a"}); err == nil || !strings.Contains(err.Error(), "enabledWorkspaces") {
		t.Errorf("controller-slack: %v", err)
	}
}

// The reference documents every retired variable and what replaces it, and
// nothing it lists is retired twice: a migration table is a table somebody
// follows line by line.
func TestTheReferenceListsEveryRetiredVariable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	cell := regexp.MustCompile("`([A-Z][A-Z0-9_]+)`")
	listed := map[string]bool{}
	for _, m := range cell.FindAllStringSubmatch(doc, -1) {
		listed[m[1]] = true
	}
	for _, binary := range schema.Services {
		for name := range config.Retired(binary) {
			if !listed[name] {
				t.Errorf("%s: the reference's migration table does not list %s", binary, name)
			}
		}
	}
}

// `ports.adapter` is one of the two adapters that exist, and the default is
// the one that keeps state where it has always been kept.
func TestThePortsAdapterIsOneOfTheTwo(t *testing.T) {
	for _, adapter := range []string{"legacy", "memory"} {
		if _, err := config.Load[config.Serve](write(t, minimalIssuer+"ports: {adapter: "+adapter+"}\n")); err != nil {
			t.Errorf("adapter %s was refused: %v", adapter, err)
		}
	}
	// The adapters that were retired are refused, naming nothing they hold.
	for name, old := range map[string]string{
		"the nats adapter":   "ports: {adapter: nats, nats: {url: 'nats://n:4222'}}\n",
		"a nats section":     "ports: {adapter: memory, nats: {url: 'nats://n:4222'}}\n",
		"a sealer":           "ports: {sealer: {adapter: kms, kms: {keyId: alias/ar}}}\n",
		"a sealer of memory": "ports: {sealer: {adapter: memory}}\n",
	} {
		if _, err := config.Load[config.Serve](write(t, minimalIssuer+old)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := config.Load[config.Serve](write(t, minimalIssuer+"ports: {adapter: dynamodb}\n")); err == nil {
		t.Error("the dynamodb adapter was accepted with no table named")
	}
	if _, err := config.Load[config.Serve](write(t, minimalIssuer+"ports: {adapter: dynamodb, dynamodb: {region: eu-west-1}}\n")); err == nil {
		t.Error("a dynamodb section with no table was accepted")
	}
	ddbFile := "ports:\n  adapter: dynamodb\n  dynamodb: {table: sluis, region: eu-west-1, endpoint: 'http://localstack:4566', create: true}\n"
	if f, err := config.Load[config.Serve](write(t, minimalIssuer+ddbFile)); err != nil {
		t.Errorf("the dynamodb adapter was refused: %v", err)
	} else if d := f.Ports.DynamoDB; d == nil || d.Table != "sluis" || d.Region != "eu-west-1" || !d.Create || d.Endpoint == "" {
		t.Errorf("ports.dynamodb = %+v", d)
	}
	if _, err := config.Load[config.Serve](write(t, minimalIssuer+"ports: {adapter: dynamodb, dynamodb: {table: t, accessKey: x}}\n")); err == nil {
		t.Error("a dynamodb credential in the file was accepted")
	}
	if _, err := config.Load[config.Serve](write(t, minimalIssuer+"ports: {adapter: cassandra}\n")); err == nil {
		t.Error("an adapter that does not exist was accepted")
	}
	if _, err := config.Load[config.ControllerGitHub](write(t, "policyDir: /p\nconsoleURL: http://c:8080\nports: {adapter: cassandra}\n")); err == nil {
		t.Error("a controller accepted an adapter that does not exist")
	}
	if _, err := config.Load[config.ControllerSlack](write(t, "policyDir: /p\nconsoleURL: http://c:8080\nports: {adapter: memory}\n")); err != nil {
		t.Errorf("a controller refused the memory adapter: %v", err)
	}
}

// `ports.blob` names one adapter and its settings, and composes with any
// `ports.adapter`.
func TestThePortsBlobIsChecked(t *testing.T) {
	good := minimalIssuer + `ports:
  adapter: legacy
  blob: {adapter: s3, s3: {bucket: b, prefix: ar, region: eu-west-1, kmsKey: alias/ar, endpoint: "http://localhost:4566", pathStyle: true}}
`
	c, err := config.Load[config.Serve](write(t, good))
	if err != nil {
		t.Fatalf("a Blob over the legacy State was refused: %v", err)
	}
	if c.Ports.Blob.S3.Bucket != "b" || !c.Ports.Blob.S3.PathStyle {
		t.Errorf("decoded %+v", c.Ports.Blob)
	}
	for name, bad := range map[string]string{
		"an unknown blob adapter": "ports: {blob: {adapter: gcs}}\n",
		"s3 with no settings":     "ports: {blob: {adapter: s3}}\n",
		"s3 with no bucket":       "ports: {blob: {adapter: s3, s3: {prefix: x}}}\n",
		"an unknown s3 key":       "ports: {blob: {adapter: s3, s3: {bucket: b, accessKey: x}}}\n",
		"a blob with no adapter":  "ports: {blob: {s3: {bucket: b}}}\n",
	} {
		if _, err := config.Load[config.Serve](write(t, minimalIssuer+bad)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := config.Load[config.ControllerGitHub](write(t, "policyDir: /p\nconsoleURL: http://c:8080\n"+
		"ports: {blob: {adapter: s3, s3: {bucket: b}}}\n")); err != nil {
		t.Errorf("a controller refused ports.blob: %v", err)
	}
}

// The Audit page's query service is its own setting: with the sqs or log sink
// there is no writer, and the page must still be configurable.
func TestTheQueryURLNeedsNoWriter(t *testing.T) {
	for name, extra := range map[string]string{
		"alone": "audit: {queryURL: 'https://audit.example/sluis'}\n",
		"with the sqs sink": "audit: {queryURL: 'https://audit.example/sluis'}\n" +
			"adapters: {audit: {adapter: sqs, settings: {queueURL: 'https://sqs.example/1/q', region: eu-west-1}}}\n",
		"with the log sink": "audit: {queryURL: 'http://q:1', audience: audit}\nadapters: {audit: {adapter: log}}\n",
	} {
		f, err := config.Load[config.Serve](write(t, minimalIssuer+extra))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if f.Audit == nil || f.Audit.QueryURL == "" || f.Audit.Writer != "" {
			t.Errorf("%s: audit = %+v, want a query URL and no writer", name, f.Audit)
		}
	}
}

// The one service document: the serve keys at the top level and, under
// `controllers`, the controllers it runs. An absent controller is off.
func TestTheOneServiceDocumentLoads(t *testing.T) {
	body := "apiVersion: " + config.APIVersion("sluis") + "\n" + minimalIssuer + `publicURL: https://access.example/console
controllers:
  github: {interval: 5m, consoleURL: "http://c:8080/console"}
  slack: {}
`
	s, err := config.Load[config.Sluis](write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if s.IssuerURL != "https://access.example" || s.APIVersion != config.APIVersion("sluis") {
		t.Errorf("the serve keys were not decoded at the top level: %+v", s.Serve)
	}
	g, sl := s.GitHubController(), s.SlackController()
	if g == nil || g.Interval.D().String() != "5m0s" || g.ConsoleURL != "http://c:8080/console" {
		t.Errorf("github controller: %+v", g)
	}
	// An unset consoleURL is the console's public URL, and a controller shares
	// the process's own settings.
	if sl == nil || sl.ConsoleURL != "https://access.example/console" {
		t.Errorf("slack controller: %+v", sl)
	}
	if g.Release != s.Release || g.Policy != s.Policy {
		t.Errorf("a controller does not share the process's release and policy: %+v", g.Roster)
	}
	off, err := config.Load[config.Sluis](write(t, "apiVersion: "+config.APIVersion("sluis")+"\n"+minimalIssuer))
	if err != nil {
		t.Fatal(err)
	}
	if off.GitHubController() != nil || off.SlackController() != nil {
		t.Error("a controller that the document does not name is on")
	}
}

// N-1: a v2 serve document, and a v1 one, load as the one document with no
// controllers, so that a deployment moves its documents on its own schedule.
func TestAServeDocumentLoadsAsTheOneDocumentWithNoControllers(t *testing.T) {
	for name, body := range map[string]string{
		"v2": "apiVersion: " + config.APIVersion("serve") + "\n" + minimalIssuer,
		"v1": minimalIssuer,
	} {
		s, err := config.Load[config.Sluis](write(t, body))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if s.IssuerURL != "https://access.example" || s.Controllers != nil || s.GitHubController() != nil {
			t.Errorf("%s: %+v", name, s)
		}
	}
}

// The one document is strict: an unknown key is refused and named, in the
// controllers' sections as at the top, and a key the controllers share with
// the process is not a key of theirs.
func TestTheOneServiceDocumentIsStrict(t *testing.T) {
	head := "apiVersion: " + config.APIVersion("sluis") + "\n" + minimalIssuer
	for name, extra := range map[string]string{
		"an unknown top-level key":                   "valkey2: {}\n",
		"an unknown controller":                      "controllers: {gitlab: {}}\n",
		"an unknown key of a controller":             "controllers: {github: {bogus: 1}}\n",
		"a controller's own policy":                  "controllers: {github: {policy: {file: /p}}}\n",
		"a controller's own release":                 "controllers: {slack: {release: x}}\n",
		"a controller's probes":                      "controllers: {slack: {probes: {address: ':1'}}}\n",
		"a controller's interval that is not one":    "controllers: {github: {interval: a while}}\n",
		"a controller's console url with a password": "controllers: {github: {consoleURL: 'http://u:p@c:1'}}\n",
		"a v2 controller document's version":         "apiVersion: " + config.APIVersion("controller-github") + "\n",
	} {
		body := head + extra
		if strings.HasPrefix(extra, "apiVersion") {
			body = minimalIssuer + extra
		}
		if _, err := config.Load[config.Sluis](write(t, body)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	_, err := config.Load[config.Sluis](write(t, head+"controllers: {github: {bogus: 1}}\n"))
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("the refusal does not name the key: %v", err)
	}
}

// A v3 document names the policy as a v2 one does.
func TestTheOneServiceDocumentNamesThePolicy(t *testing.T) {
	dir := t.TempDir()
	pol := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(pol, []byte("apiVersion: "+config.APIVersion("policy")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := filepath.Join(dir, "sluis.yaml")
	body := "apiVersion: " + config.APIVersion("sluis") + "\n" + minimalIssuer + "policy: {file: " + pol + "}\n"
	if err := os.WriteFile(svc, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadConfig[config.Sluis](svc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Policy == nil {
		t.Error("the policy the document names was not read")
	}
}

// lifetimes.agent is a closed block of three durations: it loads into the
// document as written, and a key it does not have fails the file, so a
// misspelt agent lifetime cannot silently become the default.
func TestTheAgentLifetimesBlockLoadsAndIsClosed(t *testing.T) {
	head := "apiVersion: " + config.APIVersion("serve") + "\n" + minimalIssuer
	doc, err := config.Load[config.Serve](write(t, head+
		"lifetimes: {agent: {refresh: 48h, absolute: 96h, access: 10m}}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	agent := doc.Lifetimes.Agent
	if agent == nil || agent.Refresh.D() != 48*time.Hour || agent.Absolute.D() != 96*time.Hour || agent.Access.D() != 10*time.Minute {
		t.Errorf("lifetimes.agent = %+v, want refresh 48h, absolute 96h, access 10m", agent)
	}

	for name, extra := range map[string]string{
		"an unknown key":                 "lifetimes: {agent: {idle: 48h}}\n",
		"a duration that is not one":     "lifetimes: {agent: {absolute: a month}}\n",
		"a zero agent absolute lifetime": "lifetimes: {agent: {absolute: 0}}\n",
	} {
		if _, err := config.Load[config.Serve](write(t, head+extra)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// `session: agent` loads, on a declared client and on client_documents
// alike, now that an agent authorization completes only on its consent page
// and a browser sign-out spares agent sessions
// (docs/decisions/0040-agent-class-sessions.md, decisions 6 and 7). What is
// still refused stays refused: `session` on an exchange client, and
// `session: agent` beside `sign_in_exchange: true`.
func TestAgentClassLoads(t *testing.T) {
	head := "apiVersion: " + config.APIVersion("policy") + "\n" +
		"groups: { a: { members: [g@h.example] } }\n"
	for name, tc := range map[string]struct {
		body    string
		refused string
	}{
		"an agent client": {
			"clients: { mcp: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], session: agent } }\n", "",
		},
		"agent document clients": {
			"client_documents: { origins: [hosts.example], requires: [a], session: agent }\n", "",
		},
		"an interactive client": {
			"clients: { mcp: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], session: interactive } }\n", "",
		},
		"an agent client that trades its sign-in": {
			"clients: { mcp: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], session: agent, sign_in_exchange: true } }\n",
			"sign_in_exchange",
		},
	} {
		_, err := config.Load[config.PolicyDocument](write(t, head+tc.body))
		switch {
		case tc.refused == "" && err != nil:
			t.Errorf("%s: %v, want it loaded", name, err)
		case tc.refused != "" && (err == nil || !strings.Contains(err.Error(), tc.refused)):
			t.Errorf("%s: %v, want it refused for %s", name, err, tc.refused)
		}
	}
}

// The exports are retired (ADR 0041): a document that still has them is
// refused with where the contract moved, never ignored.
func TestRetiredExportsAreRefusedAndPointToTheExternalContract(t *testing.T) {
	policy := "apiVersion: sluis.truvity.github.io/policy/v2\nexports: [{source: slack-app, app: alerts, path: a/b}]\n"
	_, err := config.Load[config.PolicyDocument](write(t, policy))
	if err == nil || !strings.Contains(err.Error(), "external/") {
		t.Errorf("policy exports: %v", err)
	}
	for name, doc := range map[string]string{
		"serve exports":      minimalIssuer + "exports: [{source: slack-app, app: a, path: a/b}]\n",
		"serve ports.export": minimalIssuer + "ports: {export: {adapter: memory}}\n",
	} {
		if _, err := config.Load[config.Serve](write(t, doc)); err == nil || !strings.Contains(err.Error(), "external/") {
			t.Errorf("%s: %v", name, err)
		}
	}
	sluis := "apiVersion: sluis.truvity.github.io/sluis/v3\n" + minimalIssuer + "ports: {export: {adapter: memory}}\n"
	if _, err := config.Load[config.Sluis](write(t, sluis)); err == nil || !strings.Contains(err.Error(), "external/") {
		t.Errorf("sluis ports.export: %v", err)
	}
}
