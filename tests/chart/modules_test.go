package chart_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/truvity/sluis/internal/config"
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
	if got, want := names("Service", nil), []string{"sluis", "sluis-cloudflare", "sluis-github", "sluis-google", "sluis-slack"}; !reflect.DeepEqual(got, want) {
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
	if want := []string{"sluis-cloudflare", "sluis-github", "sluis-google", "sluis-slack"}; !reflect.DeepEqual(delegated, want) {
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
		for _, m := range []string{"github", "slack", "google", "cloudflare"} {
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
		_, hasMin := dig(pdb, "spec", "minAvailable")
		_, hasMax := dig(pdb, "spec", "maxUnavailable")
		if m == "github" && (!hasMin || hasMax) {
			t.Errorf("github asked for minAvailable: %v", pdb["spec"])
		}
		if m != "github" && (!hasMax || hasMin) {
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
	// /rpc comes from the issuer's pods and nowhere else.
	for _, m := range []string{"cloudflare", "github", "google", "slack"} {
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
		"issuer":     {"cloudflare", "github", "google", "slack"},
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

// The github and slack modules listen on /rpc for the issuer and nobody else:
// the document names the listener and the one account let in, the Deployment
// opens the port, the Service reaches it, and the issuer's pod presents a token
// minted for each module's audience.
func TestTheGitHubAndSlackModulesServeTheIssuerOnRPC(t *testing.T) {
	docs := render(t, filepath.Join("..", "cases", "sluis", "all-modules", "values.yaml"), "default")
	byName := map[string]map[string]any{}
	for _, d := range docs {
		k, _ := d["kind"].(string)
		n, _ := dig(d, "metadata", "name")
		byName[k+"/"+n.(string)] = d
	}
	document := func(module string) map[string]any {
		raw, _ := dig(byName["ConfigMap/sluis-"+module+"-config"], "data", "config.yaml")
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(raw.(string)), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	for _, m := range []string{"github", "slack", "cloudflare"} {
		doc := document(m)
		if got, _ := dig(doc, m, "serve", "address"); got != ":8081" {
			t.Errorf("%s.serve.address is %v", m, got)
		}
		if got, _ := dig(doc, m, "serve", "audience"); got != m {
			t.Errorf("%s.serve.audience is %v", m, got)
		}
		issuers, _ := dig(doc, m, "serve", "issuers")
		if !reflect.DeepEqual(issuers, []any{"system:serviceaccount:default:sluis-issuer"}) {
			t.Errorf("%s.serve.issuers is %v, want the issuer's account alone", m, issuers)
		}
		if _, ok := dig(doc, m, "remote"); ok {
			t.Errorf("the %s module's own document routes it to itself", m)
		}
		// The Deployment opens the port, the Service reaches it, and the policy
		// lets the issuer's pods in.
		ports, _ := dig(byName["Deployment/sluis-"+m], "spec", "template", "spec", "containers")
		var rpc bool
		for _, p := range ports.([]any)[0].(map[string]any)["ports"].([]any) {
			rpc = rpc || p.(map[string]any)["name"] == "rpc"
		}
		if !rpc {
			t.Errorf("the %s container has no rpc port", m)
		}
		if port, _ := dig(byName["Service/sluis-"+m], "spec", "ports"); port.([]any)[0].(map[string]any)["port"] != float64(8081) {
			t.Errorf("the %s Service ports are %v", m, port)
		}
	}
	// The issuer's pod projects a token for each audience it calls.
	volumes, _ := dig(byName["Deployment/sluis-issuer"], "spec", "template", "spec", "volumes")
	audiences := map[string]string{}
	for _, v := range volumes.([]any) {
		vol := v.(map[string]any)
		if aud, ok := dig(vol, "projected", "sources"); ok {
			if a, ok := dig(aud.([]any)[0].(map[string]any), "serviceAccountToken", "audience"); ok {
				audiences[vol["name"].(string)] = a.(string)
			}
		}
	}
	for _, m := range []string{"github", "slack"} {
		if audiences["call-"+m] != m {
			t.Errorf("the issuer projects %q for %s, want %q", audiences["call-"+m], m, m)
		}
	}
}

// The Outline module runs on its own, as the Slack module does: a Deployment,
// ServiceAccount, document, budget, Service on /rpc and auth-delegator binding
// of its own, a sluis-outline/v1 document the module loads, the token it
// presents to the console in its pod alone, and the issuer routing the
// console's Outline pages to it by `outline.remote` with a call token for its
// audience. The issuer never runs it, and the controllers' Role is not its.
func TestTheOutlineModuleRunsOnItsOwn(t *testing.T) {
	docs := render(t, filepath.Join("..", "cases", "sluis", "outline-module", "values.yaml"), "default")
	byName := map[string]map[string]any{}
	for _, d := range docs {
		k, _ := d["kind"].(string)
		n, _ := dig(d, "metadata", "name")
		byName[k+"/"+n.(string)] = d
	}
	for _, want := range []string{
		"Deployment/sluis-outline", "ServiceAccount/sluis-outline", "ConfigMap/sluis-outline-config",
		"PodDisruptionBudget/sluis-outline", "Service/sluis-outline", "NetworkPolicy/sluis-outline",
		"ClusterRoleBinding/sluis-default-outline-auth-delegator",
	} {
		if byName[want] == nil {
			t.Errorf("no %s", want)
		}
	}
	if byName["Role/sluis-controllers"] != nil {
		t.Error("the controllers' Role is rendered, and the Outline module reports into no ConfigMap")
	}
	subjects, _ := dig(byName["ClusterRoleBinding/sluis-default-outline-auth-delegator"], "subjects")
	if s, _ := subjects.([]any); len(s) != 1 || s[0].(map[string]any)["name"] != "sluis-outline" {
		t.Errorf("system:auth-delegator is bound to %v, want sluis-outline", subjects)
	}

	// The module's document: its apiVersion, and what the chart fills.
	raw, _ := dig(byName["ConfigMap/sluis-outline-config"], "data", "config.yaml")
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte(raw.(string)), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadOutline(file)
	if err != nil {
		t.Fatalf("the Outline module's document does not load: %v", err)
	}
	if loaded.APIVersion != config.APIVersion("sluis-outline") {
		t.Errorf("apiVersion %q", loaded.APIVersion)
	}
	if loaded.ConsoleURL != "http://sluis.default.svc:8080/console" {
		t.Errorf("consoleURL %q, want this release's console", loaded.ConsoleURL)
	}
	if loaded.Policy == nil || loaded.Policy.File != "/var/run/access-issuer/policy/policy.yaml" {
		t.Errorf("policy %+v, want the policy the chart mounts", loaded.Policy)
	}
	if loaded.TokenFile != "" {
		t.Errorf("tokenFile %q, want the module's default, where the chart mounts the token", loaded.TokenFile)
	}
	if loaded.Outline == nil {
		t.Fatal("the Outline module's document has no outline section: the chart writes outline.serve")
	}
	if serve := loaded.Outline.Serve; serve == nil || serve.Address != ":8081" || serve.Audience != "outline" ||
		!reflect.DeepEqual(serve.Issuers, []string{"system:serviceaccount:default:sluis-issuer"}) {
		t.Errorf("outline.serve is %+v, want :8081, outline, the issuer's account alone", serve)
	}

	// The module's pod: its own command, the rpc port, and the token it presents
	// to the console, minted for the exchange audience.
	pod, _ := dig(byName["Deployment/sluis-outline"], "spec", "template", "spec")
	container := pod.(map[string]any)["containers"].([]any)[0].(map[string]any)
	if args := container["args"].([]any); args[0] != "outline" {
		t.Errorf("the outline container runs %v", args)
	}
	var rpc bool
	for _, p := range container["ports"].([]any) {
		rpc = rpc || (p.(map[string]any)["name"] == "rpc" && p.(map[string]any)["containerPort"] == float64(8081))
	}
	if !rpc {
		t.Error("the outline container has no rpc port on 8081")
	}
	mounts := map[string]string{}
	for _, m := range container["volumeMounts"].([]any) {
		mounts[m.(map[string]any)["name"].(string)] = m.(map[string]any)["mountPath"].(string)
	}
	if mounts["outline-token"] != filepath.Dir(config.OutlineTokenFile) {
		t.Errorf("the outline-token volume is mounted at %q, want %q", mounts["outline-token"], filepath.Dir(config.OutlineTokenFile))
	}
	audiences := func(deployment string) map[string]string {
		volumes, _ := dig(byName["Deployment/"+deployment], "spec", "template", "spec", "volumes")
		out := map[string]string{}
		for _, v := range volumes.([]any) {
			vol := v.(map[string]any)
			if src, ok := dig(vol, "projected", "sources"); ok {
				if a, ok := dig(src.([]any)[0].(map[string]any), "serviceAccountToken", "audience"); ok {
					out[vol["name"].(string)] = a.(string)
				}
			}
		}
		return out
	}
	if got := audiences("sluis-outline")["outline-token"]; got != "sluis" {
		t.Errorf("the module's token is minted for %q, want the exchange audience sluis", got)
	}
	if got := audiences("sluis-issuer")["call-outline"]; got != "outline" {
		t.Errorf("the issuer's call token for the module is minted for %q, want outline", got)
	}
	if _, ok := audiences("sluis-issuer")["outline-token"]; ok {
		t.Error("the issuer's pod mounts the Outline module's own token: the issuer never runs it")
	}

	// The issuer's document routes the console's Outline pages to the module.
	raw, _ = dig(byName["ConfigMap/sluis-issuer-config"], "data", "config.yaml")
	var issuer map[string]any
	if err := yaml.Unmarshal([]byte(raw.(string)), &issuer); err != nil {
		t.Fatal(err)
	}
	remote, _ := dig(issuer, "outline", "remote")
	want := map[string]any{
		"url": "http://sluis-outline.default.svc:8081", "audience": "outline", "tokenFile": "/var/run/secrets/sluis/outline/token",
	}
	if !reflect.DeepEqual(remote, want) {
		t.Errorf("outline.remote is %v, want %v", remote, want)
	}
	if section, _ := issuer["outline"].(map[string]any); len(section) != 1 {
		t.Errorf("the issuer's outline section is %v, want the remote alone", section)
	}

	// The fences: /rpc from the issuer's pods only, the console from the
	// module's, and the issuer reaching the module.
	from, _ := dig(byName["NetworkPolicy/sluis-outline"], "spec", "ingress")
	if rules := from.([]any); len(rules) != 1 {
		t.Errorf("the module takes %d ingress rules, want one", len(rules))
	} else {
		peer := rules[0].(map[string]any)["from"].([]any)[0].(map[string]any)
		if c, _ := dig(peer, "podSelector", "matchLabels", "app.kubernetes.io/component"); c != "issuer" {
			t.Errorf("the module takes /rpc from component %v, want issuer", c)
		}
	}
	peers := func(name string) []string {
		egress, _ := dig(byName["NetworkPolicy/"+name], "spec", "egress")
		var out []string
		for _, r := range egress.([]any) {
			to, _ := r.(map[string]any)["to"].([]any)
			for _, t := range to {
				if c, ok := dig(t.(map[string]any), "podSelector", "matchLabels", "app.kubernetes.io/component"); ok {
					out = append(out, c.(string))
				}
			}
		}
		return out
	}
	if got := peers("sluis-outline"); !reflect.DeepEqual(got, []string{"issuer"}) {
		t.Errorf("the module reaches the pods of %v, want the issuer's console", got)
	}
	if got := peers("sluis-issuer"); !reflect.DeepEqual(got, []string{"outline"}) {
		t.Errorf("the issuer reaches the pods of %v, want the module", got)
	}
	ingress, _ := dig(byName["NetworkPolicy/sluis-issuer"], "spec", "ingress")
	var callers any
	for _, r := range ingress.([]any) {
		sources, _ := r.(map[string]any)["from"].([]any) // the gateway's rule is empty without a namespace
		for _, f := range sources {
			if exprs, ok := dig(f.(map[string]any), "podSelector", "matchExpressions"); ok {
				callers = exprs.([]any)[0].(map[string]any)["values"]
			}
		}
	}
	if !reflect.DeepEqual(callers, []any{"outline"}) {
		t.Errorf("the issuer takes its console from the pods of %v, want the module's", callers)
	}
}
