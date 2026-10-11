// Package chart holds the chart to the rule that makes a configuration file
// worth having: what the chart renders IS what the values say, and what it
// renders is what the binary will accept.
package chart_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/truvity/sluis/internal/config"
)

// What each ConfigMap is named for: where its config lives in the values, and
// which schema its binary validates it against.
//
// The issuer's service document, always; the other modules' documents are
// derived from the values and held by modules_test.go; and the backup module's, which the chart renders only when it is asked for
// (`backup.enabled` or `restore.enabled`), for the backup CronJob and the
// restore Job alike.
var components = map[string]struct {
	path     []string
	schema   string
	optional bool
}{
	"issuer": {[]string{"config"}, "sluis", false},
	"backup": {[]string{"backup", "config"}, "sluis-backup", true},
}

func helm(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("ACCESS_ROSTER_REQUIRE_HELM") != "" {
			t.Fatal("helm is required here (ACCESS_ROSTER_REQUIRE_HELM is set) and is not on the PATH")
		}
		t.Skip("helm is not on the PATH")
	}
	return path
}

func render(t *testing.T, values, namespace string) []map[string]any {
	t.Helper()
	cmd := exec.Command(helm(t), "template", "sluis", filepath.Join("..", "..", "charts", "sluis"),
		"--namespace", namespace, "-f", values)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template -f %s: %v\n%s", values, err, stderr.String())
	}
	var docs []map[string]any
	for _, part := range strings.Split(string(out), "\n---\n") {
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(part), &doc); err != nil {
			t.Fatalf("a rendered document is not YAML: %v\n%s", err, part)
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

func dig(doc any, path ...string) (any, bool) {
	for _, key := range path {
		m, ok := doc.(map[string]any)
		if !ok {
			return nil, false
		}
		if doc, ok = m[key]; !ok {
			return nil, false
		}
	}
	return doc, true
}

// merge is Helm's: maps merge key by key, anything else replaces, and a null
// deletes.
func merge(base, over map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if v == nil {
			delete(out, k)
			continue
		}
		bm, bok := out[k].(map[string]any)
		om, ook := v.(map[string]any)
		if bok && ook {
			out[k] = merge(bm, om)
			continue
		}
		out[k] = v
	}
	return out
}

func load(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // a path in this repository
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

// moduleDocuments are the modules' ConfigMaps, which the issuer's is not: each
// is derived from the values (modules_test.go holds what), and the two
// controllers' are files their binaries accept.
var moduleDocuments = map[string]string{
	"github":     "controller-github",
	"slack":      "controller-slack",
	"google":     "",
	"cloudflare": "",
}

// callees are the modules the issuer routes to: the chart writes each one's
// `<module>.remote` into the issuer's document.
var callees = []string{"github", "google", "cloudflare"}

// The chart passes `config` through: the ConfigMap it renders for the issuer
// holds exactly the block the values gave, with nothing added but the
// apiVersion the chart writes, the `<module>.remote` of each enabled module,
// and without the controllers, which run in their own modules; and that block
// is a file its binary accepts.
func TestTheRenderedConfigurationIsTheValuesConfiguration(t *testing.T) {
	defaults := load(t, filepath.Join("..", "..", "charts", "sluis", "values.yaml"))
	cases, err := filepath.Glob(filepath.Join("..", "cases", "sluis", "*", "values.yaml"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("no cases found: %v", err)
	}
	seen := map[string]int{}
	for _, shape := range cases {
		t.Run(filepath.Base(filepath.Dir(shape)), func(t *testing.T) {
			namespace := "default"
			if raw, err := os.ReadFile(filepath.Join(filepath.Dir(shape), "namespace")); err == nil {
				namespace = strings.TrimSpace(string(raw))
			}
			values := merge(defaults, load(t, shape))

			// A release given the rendered documents is held by documents_test.go:
			// its ConfigMaps are those documents, not the values' `config`.
			if doc, _ := dig(values, "documents", "service"); doc != nil && doc != "" {
				return
			}

			// `renders: alerts` and `renders: dashboards` release no service:
			// their cases are held by alerts_test.go, and what matters here is
			// that none of them renders a configuration.
			if mode, _ := values["renders"].(string); mode != "app" {
				for _, doc := range render(t, shape, namespace) {
					if data, _ := dig(doc, "data", "config.yaml"); data != nil {
						t.Errorf("renders %q and a %v carries a config.yaml", mode, doc["kind"])
					}
				}
				return
			}

			rendered := map[string]bool{}
			for _, doc := range render(t, shape, namespace) {
				if doc["kind"] != "ConfigMap" {
					continue
				}
				name, _ := dig(doc, "metadata", "name")
				component, ok := dig(doc, "metadata", "labels", "app.kubernetes.io/component")
				data, hasData := dig(doc, "data", "config.yaml")
				if !ok || !hasData || !strings.HasSuffix(name.(string), "-config") {
					continue
				}
				if schema, isModule := moduleDocuments[component.(string)]; isModule {
					seen[component.(string)]++
					if schema != "" {
						var doc any
						if err := yaml.Unmarshal([]byte(data.(string)), &doc); err != nil {
							t.Fatalf("%s: not YAML: %v", name, err)
						}
						if err := config.Validate(schema, doc); err != nil {
							t.Errorf("%s is not a file %s accepts: %v", name, schema, err)
						}
					}
					continue
				}
				c, known := components[component.(string)]
				if !known {
					t.Errorf("%s: a configuration for a component this test does not know", name)
					continue
				}
				rendered[component.(string)] = true
				seen[component.(string)]++

				var got any
				if err := yaml.Unmarshal([]byte(data.(string)), &got); err != nil {
					t.Fatalf("%s: not YAML: %v", name, err)
				}
				given, ok := dig(values, c.path...)
				if !ok {
					t.Errorf("%s is rendered and the values have no %s", name, strings.Join(c.path, "."))
					continue
				}
				given = withoutControllers(component.(string), given.(map[string]any))
				// The issuer's routes to its modules are the chart's, held by modules_test.go;
				// the document is compared, and validated, without them.
				got = withoutRemotes(component.(string), got)
				want := merge(map[string]any{"apiVersion": config.APIVersion(c.schema)}, given.(map[string]any))
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s is not %s of the values:\n got %v\nwant %v", name, strings.Join(c.path, "."), got, want)
				}
				if err := config.Validate(c.schema, got); err != nil {
					t.Errorf("%s is not a file %s accepts: %v", name, c.schema, err)
				}
			}

			// The other way: the issuer's document always reaches a ConfigMap.
			for component, c := range components {
				if !rendered[component] && !c.optional {
					t.Errorf("%s is configured and the chart rendered no ConfigMap for it", component)
				}
			}
		})
	}
	// A sweep that found nothing proved nothing.
	if !t.Failed() {
		for component := range components {
			if seen[component] == 0 {
				t.Errorf("no case rendered a configuration for %s", component)
			}
		}
	}
}

// withoutControllers is the issuer's block as the chart writes it: the
// controllers run in their modules, and their sections are not in its document.
// In documents mode the document is the given one, which is not edited.
func withoutControllers(component string, given map[string]any) map[string]any {
	if component != "issuer" {
		return given
	}
	out := map[string]any{}
	for k, v := range given {
		if k != "controllers" {
			out[k] = v
		}
	}
	return out
}

// withoutRemotes drops the `<module>.remote` the chart wrote into the issuer's
// document, and the section it leaves empty.
func withoutRemotes(component string, got any) any {
	doc, ok := got.(map[string]any)
	if !ok || component != "issuer" {
		return got
	}
	for _, m := range callees {
		if section, ok := doc[m].(map[string]any); ok {
			delete(section, "remote")
			if len(section) == 0 {
				delete(doc, m)
			}
		}
	}
	return doc
}

// Every example the chart ships is a values file somebody copies: its config,
// merged over the chart's defaults and a minimal install, is a file the
// service accepts.
func TestEveryShippedExampleConfigurationIsAccepted(t *testing.T) {
	examples, _ := filepath.Glob(filepath.Join("..", "..", "charts", "sluis", "examples", "*.yaml"))
	defaults := load(t, filepath.Join("..", "..", "charts", "sluis", "values.yaml"))
	minimal := load(t, filepath.Join("..", "cases", "sluis", "minimal", "values.yaml"))
	for _, example := range examples {
		values := merge(merge(defaults, minimal), load(t, example))
		cfg, ok := dig(values, "config")
		if !ok {
			t.Fatalf("%s: no config", example)
		}
		if err := config.Validate("sluis", merge(map[string]any{"apiVersion": config.APIVersion("sluis")}, cfg.(map[string]any))); err != nil {
			t.Errorf("%s: %v", example, err)
		}
		if doc, _ := dig(values, "backup", "config"); doc != nil && len(doc.(map[string]any)) > 0 {
			if err := config.Validate("sluis-backup", merge(map[string]any{"apiVersion": config.APIVersion("sluis-backup")}, doc.(map[string]any))); err != nil {
				t.Errorf("%s: backup.config: %v", example, err)
			}
		}
	}
	if len(examples) == 0 {
		t.Error("no example found: the sweep proved nothing")
	}
}

// The policy document the chart renders is one the binaries accept: every
// case's policy ConfigMap loads through the loader the service and the
// controllers call, its checks included, and carries the sections the chart's
// own values fill.
func TestTheRenderedPolicyDocumentLoads(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("..", "cases", "sluis", "*", "values.yaml"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("no cases found: %v", err)
	}
	loaded := 0
	for _, shape := range cases {
		namespace := "default"
		if raw, err := os.ReadFile(filepath.Join(filepath.Dir(shape), "namespace")); err == nil {
			namespace = strings.TrimSpace(string(raw))
		}
		for _, doc := range render(t, shape, namespace) {
			data, ok := dig(doc, "data", "policy.yaml")
			if !ok || doc["kind"] != "ConfigMap" {
				continue
			}
			file := filepath.Join(t.TempDir(), "policy.yaml")
			if err := os.WriteFile(file, []byte(data.(string)), 0o600); err != nil {
				t.Fatal(err)
			}
			p, err := config.Load[config.PolicyDocument](file)
			if err != nil {
				t.Errorf("%s: the rendered policy document does not load: %v", shape, err)
				continue
			}
			loaded++
			if filepath.Base(filepath.Dir(shape)) == "full" {
				if len(p.Clusters()) != 2 || len(p.AWS().Accounts) != 2 || len(p.GitHubCatalogue().Apps) != 1 || len(p.EnabledOrgs()) != 2 {
					t.Errorf("full: the chart's own values are not in the policy document: %+v %+v %+v", p.Exchange, p.Apps, p.Controllers)
				}
				if p.AWS().Audience != "https://access.example" {
					t.Errorf("full: exchange.aws.audience = %q, want the issuer", p.AWS().Audience)
				}
			}
		}
	}
	if loaded == 0 {
		t.Error("no case rendered a policy document: the sweep proved nothing")
	}
}

// A Deployment per module (decision D-v176-9): the issuer always, every other
// module when it is enabled, and never one that runs them all. Each runs its
// own command, every volume it mounts is one it defines, and a controller's
// token, credentials and records are mounted in the pod that runs it (its
// module's, or in documents mode the issuer's, whose document names it) and
// in no other.
func TestEachModuleRunsItsOwnCommandInItsOwnDeployment(t *testing.T) {
	cases, _ := filepath.Glob(filepath.Join("..", "cases", "sluis", "*", "values.yaml"))
	defaults := load(t, filepath.Join("..", "..", "charts", "sluis", "values.yaml"))
	checked := map[string]bool{}
	for _, shape := range cases {
		values := merge(defaults, load(t, shape))
		if mode, _ := values["renders"].(string); mode != "app" {
			continue
		}
		namespace := "default"
		if raw, err := os.ReadFile(filepath.Join(filepath.Dir(shape), "namespace")); err == nil {
			namespace = strings.TrimSpace(string(raw))
		}

		// Which controllers run where.
		controllers, _ := dig(values, "config", "controllers")
		documents := false
		if doc, _ := dig(values, "documents", "service"); doc != nil && doc != "" {
			documents = true
			var parsed map[string]any
			if err := yaml.Unmarshal([]byte(doc.(string)), &parsed); err != nil {
				t.Fatalf("%s: documents.service: %v", shape, err)
			}
			controllers = parsed["controllers"]
		}
		named, _ := controllers.(map[string]any)
		wantOn := map[string]bool{"issuer": true}
		for _, m := range []string{"github", "slack", "google", "cloudflare"} {
			if on, _ := dig(values, "modules", m, "enabled"); on == true {
				wantOn[m] = true
			}
		}

		deployments := map[string]map[string]any{}
		for _, doc := range render(t, shape, namespace) {
			if doc["kind"] != "Deployment" {
				continue
			}
			component, _ := dig(doc, "metadata", "labels", "app.kubernetes.io/component")
			deployments[component.(string)] = doc
		}
		if len(deployments) != len(wantOn) {
			t.Errorf("%s: Deployments for %v, want %v", shape, keysOf(deployments), keysOf(wantOn))
			continue
		}
		for component := range wantOn {
			deployment, ok := deployments[component]
			if !ok {
				t.Errorf("%s: no Deployment for the %s module", shape, component)
				continue
			}
			spec, _ := dig(deployment, "spec", "template", "spec")
			pod, _ := spec.(map[string]any)
			containers, _ := pod["containers"].([]any)
			if len(containers) != 1 {
				t.Errorf("%s: %s: %d containers, want one", shape, component, len(containers))
				continue
			}
			container := containers[0].(map[string]any)
			if args, _ := container["args"].([]any); len(args) == 0 || args[0] != component {
				t.Errorf("%s: the %s container runs %v, want `%s`", shape, component, args, component)
			}
			volumes := map[string]bool{}
			for _, v := range pod["volumes"].([]any) {
				volumes[v.(map[string]any)["name"].(string)] = true
			}
			mounted := map[string]string{}
			for _, m := range container["volumeMounts"].([]any) {
				mount := m.(map[string]any)
				name := mount["name"].(string)
				if !volumes[name] {
					t.Errorf("%s: the %s container mounts %s, which the pod does not define", shape, component, name)
				}
				mounted[name] = mount["mountPath"].(string)
			}
			for kind, want := range map[string][]string{
				"github": {"github-token", "github-apps", "github-records"},
				"slack":  {"slack-token", "slack-credentials", "slack-workspaces"},
			} {
				_, named := named[kind]
				runs := component == kind || (documents && component == "issuer" && named)
				for _, name := range want {
					if _, has := mounted[name]; has != runs {
						t.Errorf("%s: the %s pod runs the %s controller is %v and mounts %s is %v", shape, component, kind, runs, name, has)
					}
				}
				if runs {
					checked[kind] = true
				}
			}
		}
	}
	for _, kind := range []string{"github", "slack"} {
		if !checked[kind] {
			t.Errorf("no case runs the %s controller: the sweep proved nothing", kind)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
