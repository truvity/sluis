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
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/truvity/sluis/internal/config"
)

// What each ConfigMap is named for: where its config lives in the values, and
// which schema its binary validates it against.
//
// There is one: the one process's service document, controllers' sections
// included.
var components = map[string]struct {
	path   []string
	schema string
}{
	"serve": {[]string{"config"}, "sluis"},
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

// The chart passes `config` through: the ConfigMap it renders for a component
// holds exactly the block the values gave, with nothing added but the
// apiVersion the chart writes, renamed or dropped, and that block is a file
// its binary accepts.
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
				want := merge(map[string]any{"apiVersion": config.APIVersion(c.schema)}, given.(map[string]any))
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s is not %s of the values:\n got %v\nwant %v", name, strings.Join(c.path, "."), got, want)
				}
				if err := config.Validate(c.schema, got); err != nil {
					t.Errorf("%s is not a file %s accepts: %v", name, c.schema, err)
				}
			}

			// The other way: the service document always reaches a ConfigMap.
			for component := range components {
				if !rendered[component] {
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

// The controllers are not Deployments of their own: whatever the values name,
// the chart renders ONE Deployment, running `sluis serve`, and every volume it
// mounts is one it defines. A controller the document names has its token, its
// credentials and its records mounted where the binary's defaults look, and one
// it does not name has none of them.
func TestTheControllersRunInTheOneDeployment(t *testing.T) {
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
		var deployments []map[string]any
		for _, doc := range render(t, shape, namespace) {
			if doc["kind"] == "Deployment" {
				deployments = append(deployments, doc)
			}
		}
		if len(deployments) != 1 {
			t.Errorf("%s: %d Deployments, want one", shape, len(deployments))
			continue
		}
		spec, _ := dig(deployments[0], "spec", "template", "spec")
		pod, _ := spec.(map[string]any)
		containers, _ := pod["containers"].([]any)
		if len(containers) != 1 {
			t.Errorf("%s: %d containers, want one", shape, len(containers))
			continue
		}
		container := containers[0].(map[string]any)
		if args, _ := container["args"].([]any); len(args) == 0 || args[0] != "serve" {
			t.Errorf("%s: the container runs %v, want `serve`", shape, args)
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
				t.Errorf("%s: the container mounts %s, which the pod does not define", shape, name)
			}
			mounted[name] = mount["mountPath"].(string)
		}
		controllers, _ := dig(values, "config", "controllers")
		on, _ := controllers.(map[string]any)
		for kind, want := range map[string][]string{
			"github": {"github-token", "github-apps", "github-records"},
			"slack":  {"slack-token", "slack-credentials", "slack-workspaces"},
		} {
			_, named := on[kind]
			for _, name := range want {
				if _, has := mounted[name]; has != named {
					t.Errorf("%s: controllers.%s named is %v and %s mounted is %v", shape, kind, named, name, has)
				}
			}
			if named {
				checked[kind] = true
			}
		}
	}
	for _, kind := range []string{"github", "slack"} {
		if !checked[kind] {
			t.Errorf("no case runs the %s controller: the sweep proved nothing", kind)
		}
	}
}
