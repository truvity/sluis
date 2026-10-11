package chart_test

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"sigs.k8s.io/yaml"
)

// `split: true` renders a workload per enabled module, and binds the
// permissions to the right accounts: the controllers' Role to the github and
// slack ServiceAccounts only, system:auth-delegator to the modules that
// answer calls. The issuer's document names each of those by URL.
func TestSplitRendersAWorkloadPerModule(t *testing.T) {
	docs := render(t, filepath.Join("..", "cases", "sluis", "split-minimal", "values.yaml"), "default")
	byKind := map[string][]map[string]any{}
	for _, d := range docs {
		k, _ := d["kind"].(string)
		byKind[k] = append(byKind[k], d)
	}
	names := func(kind string, filter func(map[string]any) bool) []string {
		var out []string
		for _, d := range byKind[kind] {
			if filter == nil || filter(d) {
				n, _ := dig(d, "metadata", "name")
				out = append(out, n.(string))
			}
		}
		sort.Strings(out)
		return out
	}
	modules := func(suffix string) []string {
		var out []string
		for _, m := range []string{"cloudflare", "github", "google", "issuer", "slack"} {
			out = append(out, "sluis-"+m+suffix)
		}
		return out
	}
	if got, want := names("Deployment", nil), modules(""); !reflect.DeepEqual(got, want) {
		t.Errorf("Deployments %v, want %v", got, want)
	}
	if got, want := names("ConfigMap", func(d map[string]any) bool {
		_, ok := dig(d, "data", "config.yaml")
		return ok
	}), modules("-config"); !reflect.DeepEqual(got, want) {
		t.Errorf("document ConfigMaps %v, want %v", got, want)
	}
	// The release's own account is not rendered: each module has its own.
	for _, n := range names("ServiceAccount", nil) {
		if n == "sluis" {
			t.Errorf("the single Deployment's ServiceAccount %q rendered with split", n)
		}
	}
	// /rpc is answered by the modules that are called, and the release's
	// Service stays on the issuer.
	if got, want := names("Service", nil), []string{"sluis", "sluis-cloudflare", "sluis-github", "sluis-google"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Services %v, want %v", got, want)
	}
	for _, d := range byKind["Service"] {
		if n, _ := dig(d, "metadata", "name"); n == "sluis" {
			if c, _ := dig(d, "spec", "selector", "app.kubernetes.io/component"); c != "issuer" {
				t.Errorf("the release's Service selects component %v, want issuer", c)
			}
		}
	}
	// The controllers' Role is bound to the github and slack accounts, and
	// to nothing else.
	var subjects []string
	for _, d := range byKind["RoleBinding"] {
		if n, _ := dig(d, "metadata", "name"); n != "sluis-controllers" {
			continue
		}
		for _, s := range d["subjects"].([]any) {
			subjects = append(subjects, s.(map[string]any)["name"].(string))
		}
	}
	sort.Strings(subjects)
	if want := []string{"sluis-github", "sluis-slack"}; !reflect.DeepEqual(subjects, want) {
		t.Errorf("the controllers' Role is bound to %v, want %v", subjects, want)
	}
	// The callees' accounts may do a TokenReview; no other module's may.
	var delegated []string
	for _, d := range byKind["ClusterRoleBinding"] {
		if ref, _ := dig(d, "roleRef", "name"); ref != "system:auth-delegator" {
			continue
		}
		for _, s := range d["subjects"].([]any) {
			delegated = append(delegated, s.(map[string]any)["name"].(string))
		}
	}
	sort.Strings(delegated)
	if want := []string{"sluis-cloudflare", "sluis-github", "sluis-google"}; !reflect.DeepEqual(delegated, want) {
		t.Errorf("system:auth-delegator is bound to %v, want %v", delegated, want)
	}
	// The issuer's document: no controllers, and a call target and an
	// audience per callee.
	for _, d := range byKind["ConfigMap"] {
		if n, _ := dig(d, "metadata", "name"); n != "sluis-issuer-config" {
			continue
		}
		raw, _ := dig(d, "data", "config.yaml")
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(raw.(string)), &doc); err != nil {
			t.Fatal(err)
		}
		if _, ok := doc["controllers"]; ok {
			t.Error("the issuer's document carries controllers")
		}
		for _, m := range []string{"github", "google", "cloudflare"} {
			url, _ := dig(doc, "calls", "modules", m, "url")
			aud, _ := dig(doc, "calls", "modules", m, "audience")
			if url != "http://sluis-"+m+".default.svc:8081" || aud != m {
				t.Errorf("calls.modules.%s is %v / %v", m, url, aud)
			}
		}
		if _, ok := dig(doc, "calls", "modules", "slack"); ok {
			t.Error("calls.modules names slack, which answers no calls")
		}
	}
}
