package chart_test

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"sigs.k8s.io/yaml"
)

// The chart renders a workload per module, and binds the permissions to the
// right accounts: the controllers' Role to the github and slack ServiceAccounts
// only, system:auth-delegator to the modules that answer calls. The issuer's
// document names each of those by `<module>.remote`.
func TestTheChartRendersAWorkloadPerModule(t *testing.T) {
	docs := render(t, filepath.Join("..", "cases", "sluis", "all-modules", "values.yaml"), "default")
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
			t.Errorf("a ServiceAccount %q named for the release, which runs no workload of its own", n)
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
			url, _ := dig(doc, m, "remote", "url")
			aud, _ := dig(doc, m, "remote", "audience")
			file, _ := dig(doc, m, "remote", "tokenFile")
			if url != "http://sluis-"+m+".default.svc:8081" || aud != m || file != "/var/run/secrets/sluis/"+m+"/token" {
				t.Errorf("%s.remote is %v / %v / %v", m, url, aud, file)
			}
		}
		if _, ok := doc["calls"]; ok {
			t.Error("the issuer's document carries calls: routing is one `<module>.remote` per module")
		}
		if _, ok := dig(doc, "slack", "remote"); ok {
			t.Error("the issuer's document routes to slack, which answers no calls")
		}
	}
}

// With no module enabled the chart still renders the issuer, as a Deployment of
// its own with a budget, and nothing for the modules that are off.
func TestTheIssuerIsRenderedAloneByDefault(t *testing.T) {
	docs := render(t, filepath.Join("..", "cases", "sluis", "minimal", "values.yaml"), "default")
	got := map[string][]string{}
	for _, d := range docs {
		k, _ := d["kind"].(string)
		n, _ := dig(d, "metadata", "name")
		got[k] = append(got[k], n.(string))
	}
	for _, kind := range []string{"Deployment", "PodDisruptionBudget"} {
		if want := []string{"sluis-issuer"}; !reflect.DeepEqual(got[kind], want) {
			t.Errorf("%s %v, want %v", kind, got[kind], want)
		}
	}
	for _, kind := range []string{"HorizontalPodAutoscaler", "NetworkPolicy"} {
		if len(got[kind]) != 0 {
			t.Errorf("%s %v rendered, and neither is on by default", kind, got[kind])
		}
	}
}

// The controls around the modules: a PodDisruptionBudget per Deployment, a
// HorizontalPodAutoscaler for the issuer alone (which then sets no replicas),
// and a NetworkPolicy per module that takes /rpc from the issuer's pods only.
func TestTheModulesAreBudgetedScaledAndFenced(t *testing.T) {
	docs := render(t, filepath.Join("..", "cases", "sluis", "hardened", "values.yaml"), "default")
	byKind := map[string]map[string]map[string]any{}
	for _, d := range docs {
		k, _ := d["kind"].(string)
		n, _ := dig(d, "metadata", "name")
		if byKind[k] == nil {
			byKind[k] = map[string]map[string]any{}
		}
		byKind[k][n.(string)] = d
	}
	modules := []string{"cloudflare", "github", "google", "issuer", "slack"}
	for _, m := range modules {
		name := "sluis-" + m
		for _, kind := range []string{"Deployment", "PodDisruptionBudget", "NetworkPolicy"} {
			if byKind[kind][name] == nil {
				t.Errorf("no %s %s", kind, name)
			}
		}
	}
	// A budget names the module's pods, and says one of the two.
	for _, m := range modules {
		pdb := byKind["PodDisruptionBudget"]["sluis-"+m]
		if c, _ := dig(pdb, "spec", "selector", "matchLabels", "app.kubernetes.io/component"); c != m {
			t.Errorf("the %s budget selects component %v", m, c)
		}
		_, min := dig(pdb, "spec", "minAvailable")
		_, max := dig(pdb, "spec", "maxUnavailable")
		if m == "github" && !(min && !max) {
			t.Errorf("github asked for minAvailable: %v", pdb["spec"])
		}
		if m != "github" && !(max && !min) {
			t.Errorf("%s takes the default maxUnavailable: %v", m, pdb["spec"])
		}
	}
	// Only the issuer scales, and its Deployment leaves the count to the HPA.
	if len(byKind["HorizontalPodAutoscaler"]) != 1 || byKind["HorizontalPodAutoscaler"]["sluis-issuer"] == nil {
		t.Errorf("HPAs %v, want the issuer's alone", byKind["HorizontalPodAutoscaler"])
	}
	if target, _ := dig(byKind["HorizontalPodAutoscaler"]["sluis-issuer"], "spec", "scaleTargetRef", "name"); target != "sluis-issuer" {
		t.Errorf("the HPA scales %v", target)
	}
	if _, has := dig(byKind["Deployment"]["sluis-issuer"], "spec", "replicas"); has {
		t.Error("the issuer's Deployment sets replicas, which the HPA owns")
	}
	if r, _ := dig(byKind["Deployment"]["sluis-github"], "spec", "replicas"); r != float64(2) {
		t.Errorf("the github Deployment runs %v replicas, want 2", r)
	}
	// /rpc comes from the issuer's pods and nowhere else; a module that is not
	// called takes nothing.
	for _, m := range []string{"cloudflare", "github", "google"} {
		np := byKind["NetworkPolicy"]["sluis-"+m]
		ingress, _ := dig(np, "spec", "ingress")
		rules, _ := ingress.([]any)
		if len(rules) != 1 {
			t.Errorf("%s takes %d ingress rules, want one", m, len(rules))
			continue
		}
		rule := rules[0].(map[string]any)
		from := rule["from"].([]any)
		if len(from) != 1 {
			t.Errorf("%s: /rpc from %d peers, want the issuer's pods", m, len(from))
			continue
		}
		if c, _ := dig(from[0].(map[string]any), "podSelector", "matchLabels", "app.kubernetes.io/component"); c != "issuer" {
			t.Errorf("%s takes /rpc from component %v, want issuer", m, c)
		}
		if _, ns := from[0].(map[string]any)["namespaceSelector"]; ns {
			t.Errorf("%s takes /rpc from a namespace", m)
		}
		ports := rule["ports"].([]any)
		if len(ports) != 1 || ports[0].(map[string]any)["port"] != float64(8081) {
			t.Errorf("%s takes /rpc on %v, want 8081 alone", m, ports)
		}
	}
	slack, _ := dig(byKind["NetworkPolicy"]["sluis-slack"], "spec", "ingress")
	if rules, ok := slack.([]any); !ok || len(rules) != 0 {
		t.Errorf("slack answers no calls and takes ingress %v", slack)
	}
	// Egress is per module: the issuer reaches the modules it calls, the
	// controllers the issuer's console, and nobody else a peer.
	peers := func(m string) []string {
		egress, _ := dig(byKind["NetworkPolicy"]["sluis-"+m], "spec", "egress")
		var out []string
		for _, r := range egress.([]any) {
			to, _ := r.(map[string]any)["to"].([]any)
			for _, t := range to {
				if c, ok := dig(t.(map[string]any), "podSelector", "matchLabels", "app.kubernetes.io/component"); ok {
					out = append(out, c.(string))
				}
			}
		}
		sort.Strings(out)
		return out
	}
	for m, want := range map[string][]string{
		"issuer":     {"cloudflare", "github", "google"},
		"github":     {"issuer"},
		"slack":      {"issuer"},
		"google":     nil,
		"cloudflare": nil,
	} {
		if got := peers(m); !reflect.DeepEqual(got, want) {
			t.Errorf("%s reaches the pods of %v, want %v", m, got, want)
		}
	}
}
