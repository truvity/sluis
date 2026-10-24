package chart_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

var (
	chartDir   = filepath.Join("..", "..", "charts", "sluis")
	alertsCase = filepath.Join("..", "cases", "sluis", "alerts", "values.yaml")
	rulesTest  = filepath.Join("..", "rules", "sluis-alerts.test.yaml")
)

// renderArgs renders the chart with the given extra `helm template` arguments
// and returns every document.
func renderArgs(t *testing.T, args ...string) []map[string]any {
	t.Helper()
	cmd := exec.Command(helm(t), append([]string{"template", "sluis", chartDir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template %v: %v\n%s", args, err, stderr.String())
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

func rulesOf(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	groups, _ := dig(doc, "spec", "groups")
	var out []map[string]any
	for _, g := range groups.([]any) {
		for _, r := range g.(map[string]any)["rules"].([]any) {
			out = append(out, r.(map[string]any))
		}
	}
	return out
}

// The number of rules the chart ships, which is the number written in its
// template. A guard that sweeps nothing proves nothing, so each check below
// counts what it saw against it.
const (
	templateRuleCount = 11
	ruleCount         = templateRuleCount
)

// `renders: alerts` renders the rule object and nothing else, and none of the
// service's own validation applies: the values here name no issuer URL.
func TestAlertsModeRendersOnlyTheRules(t *testing.T) {
	docs := renderArgs(t, "-f", alertsCase)
	if len(docs) != 1 || docs[0]["kind"] != "VMRule" {
		t.Fatalf("%d documents, first a %v; want one VMRule", len(docs), docs[0]["kind"])
	}
	rules := rulesOf(t, docs[0])
	if len(rules) != ruleCount {
		t.Fatalf("%d rules, want %d", len(rules), ruleCount)
	}
	for _, r := range rules {
		labels := r["labels"].(map[string]any)
		// ruleLabels pass through to every rule, beside the rule's own severity.
		if labels["k8s_cluster_name"] != "example" || labels["team"] != "platform" || labels["severity"] == nil {
			t.Errorf("%v: labels %v", r["alert"], labels)
		}
		ann := r["annotations"].(map[string]any)
		if ann["summary"] == nil || ann["description"] == nil || ann["runbook_url"] == nil {
			t.Errorf("%v: annotations %v", r["alert"], ann)
		}
	}
}

func TestAlertsCanBeAPrometheusRuleAndOneRuleCanBeOff(t *testing.T) {
	docs := renderArgs(t, "-f", alertsCase,
		"--set", "alerts.format=prometheusrule", "--set", "alerts.rules.seatsShort.enabled=false",
		"--set", "alerts.runbookBaseUrl=")
	if docs[0]["kind"] != "PrometheusRule" || len(rulesOf(t, docs[0])) != ruleCount-1 {
		t.Fatalf("%v with %d rules", docs[0]["kind"], len(rulesOf(t, docs[0])))
	}
	for _, r := range rulesOf(t, docs[0]) {
		if _, ok := r["annotations"].(map[string]any)["runbook_url"]; ok {
			t.Errorf("%v has a runbook link with no base URL", r["alert"])
		}
	}
}

// `renders: dashboards` renders a GrafanaFolder and the GrafanaDashboards in
// it, for the grafana-operator, and nothing else.
func TestDashboardsModeRendersTheFolderAndTheDashboards(t *testing.T) {
	docs := renderArgs(t, "--namespace", "sluis", "-f", filepath.Join("..", "cases", "sluis", "dashboards", "values.yaml"))
	selector := func(d map[string]any) string {
		v, _ := dig(d, "spec", "instanceSelector", "matchLabels", "observability.truvity.io/grafana")
		s, _ := v.(string)
		return s
	}
	var folders int
	uids := map[string]bool{}
	for _, d := range docs {
		if ns, _ := dig(d, "metadata", "namespace"); ns != "sluis" {
			t.Errorf("%v in namespace %v, want sluis", d["kind"], ns)
		}
		if selector(d) != "shared" {
			t.Errorf("%v: instanceSelector %v, want the shared Grafana", d["kind"], selector(d))
		}
		if v, _ := dig(d, "spec", "allowCrossNamespaceImport"); v != true {
			t.Errorf("%v lacks allowCrossNamespaceImport", d["kind"])
		}
		switch d["kind"] {
		case "GrafanaFolder":
			folders++
			if v, _ := dig(d, "spec", "uid"); v != "prod-sluis" {
				t.Errorf("folder uid %v", v)
			}
			if v, _ := dig(d, "spec", "parentFolderUID"); v != "prod" {
				t.Errorf("parentFolderUID %v", v)
			}
			if v, _ := dig(d, "metadata", "annotations", "argocd.argoproj.io/sync-wave"); v != "0" {
				t.Errorf("folder sync wave %v, want 0", v)
			}
		case "GrafanaDashboard":
			if v, _ := dig(d, "spec", "folderUID"); v != "prod-sluis" {
				t.Errorf("dashboard folderUID %v", v)
			}
			if v, _ := dig(d, "metadata", "annotations", "argocd.argoproj.io/sync-wave"); v != "1" {
				t.Errorf("dashboard sync wave %v, want 1", v)
			}
			body, _ := dig(d, "spec", "json")
			var dash map[string]any
			if err := yaml.Unmarshal([]byte(body.(string)), &dash); err != nil || dash["title"] == nil {
				t.Fatalf("not a dashboard: %v", err)
			}
			uid, _ := dash["uid"].(string)
			if uid == "" || len(uid) > 40 || !strings.HasPrefix(uid, "prod-sluis-") {
				t.Errorf("dashboard uid %q: want prod-sluis-... of at most 40 characters", uid)
			}
			uids[uid] = true
			// The Lambda dashboard reads the log store; the others the metrics store.
			want, other := "prod-metrics", "prod-logs"
			if uid == "prod-sluis-lambda" {
				want, other = other, want
			}
			if !strings.Contains(body.(string), `"value": "`+want+`"`) || strings.Contains(body.(string), `"value": "`+other+`"`) {
				t.Errorf("%s: the datasource variable's default is not %s", uid, want)
			}
		default:
			t.Errorf("a %v in dashboards mode", d["kind"])
		}
	}
	if folders != 1 || !uids["prod-sluis-overview"] || !uids["prod-sluis-audit"] || !uids["prod-sluis-lambda"] || len(uids) != 3 {
		t.Errorf("%d folders and dashboards %v; want one folder, sluis-overview, audit-overview and sluis-lambda", folders, uids)
	}
}

// A folder uid that makes a dashboard uid longer than Grafana allows, and a
// missing required value, are refused at render time.
func TestDashboardsModeRefusesWhatGrafanaWould(t *testing.T) {
	cmd := func(args ...string) error {
		a := append([]string{"template", "sluis", chartDir, "-f", filepath.Join("..", "cases", "sluis", "dashboards", "values.yaml")}, args...)
		return exec.Command(helm(t), a...).Run() //nolint:gosec // the helm binary the test found
	}
	if err := cmd("--set", "dashboards.clusterFolderUID=0123456789012345678901234"); err != nil {
		t.Errorf("a 25-character folder uid must render: %v", err)
	}
	for name, set := range map[string]string{
		"a 26-character folder uid": "dashboards.clusterFolderUID=01234567890123456789012345",
		"no folder uid":             "dashboards.clusterFolderUID=",
		"no metrics datasource":     "dashboards.datasources.metrics=",
		"no logs datasource":        "dashboards.datasources.logs=",
	} {
		if cmd("--set", set) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The audit dashboard is a copy of the audit chart's own: Helm cannot read
// outside a chart, so the two are held byte-equal here instead of drifting.
func TestTheAuditDashboardIsTheAuditCharts(t *testing.T) {
	a, err := os.ReadFile(filepath.Join(chartDir, "dashboards", "audit-overview.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "charts", "audit", "dashboards", "audit-overview.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("charts/sluis/dashboards/audit-overview.json differs from charts/audit/dashboards/audit-overview.json: copy the audit chart's file over it")
	}
}

// The Lambda function's series carry the namespace in another label: the
// selector and the aggregations follow `alerts.namespaceLabel`.
func TestAlertsFollowTheNamespaceLabel(t *testing.T) {
	docs := renderArgs(t, "-f", alertsCase, "--set", "alerts.namespaceLabel=k8s_namespace_name", "--set", "alerts.namespace=lambda")
	for _, r := range rulesOf(t, docs[0]) {
		expr := r["expr"].(string)
		if !strings.Contains(expr, `k8s_namespace_name="lambda"`) || strings.Contains(expr, `namespace="sluis"`) || strings.Contains(expr, ", namespace") {
			t.Errorf("%v: expr %s", r["alert"], expr)
		}
	}
}

// The default mode renders no alert and no dashboard, so installing the chart
// as before changes nothing.
func TestTheAppModeRendersNoAlertsAndNoDashboards(t *testing.T) {
	docs := render(t, filepath.Join("..", "cases", "sluis", "minimal", "values.yaml"), "default")
	if len(docs) == 0 {
		t.Fatal("the app rendered nothing")
	}
	for _, d := range docs {
		if d["kind"] == "VMRule" || d["kind"] == "PrometheusRule" {
			t.Errorf("the app renders a %v", d["kind"])
		}
		if d["kind"] == "GrafanaFolder" || d["kind"] == "GrafanaDashboard" {
			t.Errorf("the app renders a %v", d["kind"])
		}
	}
}

// Every rule carries the reason for its threshold in a comment above it, in the
// template, which is where the next person to change one reads.
func TestEveryRuleStatesItsThreshold(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(chartDir, "templates", "alerts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	found := 0
	for i, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "- alert: ") {
			continue
		}
		found++
		n := 0
		for j := i - 1; j >= 0 && strings.HasPrefix(strings.TrimSpace(lines[j]), "#"); j-- {
			n++
		}
		if n < 3 {
			t.Errorf("%s has %d comment lines above it; state the threshold and why", strings.TrimSpace(l), n)
		}
	}
	if found != templateRuleCount {
		t.Fatalf("found %d rules in the template, expected %d: a guard must not pass an empty sweep", found, templateRuleCount)
	}
}

// vmalert-tool is the engine of the estate's own ruler. ACCESS_ROSTER_VMALERT_TOOL
// names it; `just telemetry` fetches a pinned, checksummed release and sets it.
func vmalertTool(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("ACCESS_ROSTER_VMALERT_TOOL"); path != "" {
		return path
	}
	if os.Getenv("ACCESS_ROSTER_REQUIRE_VMALERT") != "" {
		t.Fatal("vmalert-tool is required here (ACCESS_ROSTER_REQUIRE_VMALERT is set) and ACCESS_ROSTER_VMALERT_TOOL is not")
	}
	t.Skip("ACCESS_ROSTER_VMALERT_TOOL is not set; `just telemetry` runs the rule tests")
	return ""
}

// The rules are run against the series in tests/rules, and each rule has a test
// that fires it and one that must stay silent.
func TestTheRulesFireOnWhatTheyShouldAndNotOnWhatTheyShouldNot(t *testing.T) {
	tool := vmalertTool(t)
	// The rotation rule's real threshold is 350 days, which a unit test cannot
	// span at a one-minute step: the test runs it at an hour.
	docs := renderArgs(t, "-f", alertsCase,
		"--set", "alerts.rules.signingKeyRotationStalled.maxAgeSeconds=3600",
		"--set", "alerts.rules.signingKeyRotationStalled.for=10m")
	spec, _ := dig(docs[0], "spec")
	body, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rules.yaml"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	tests, err := os.ReadFile(rulesTest)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sluis-alerts.test.yaml")
	if err := os.WriteFile(file, tests, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(tool, "unittest", "--disableAlertgroupLabel", "--files", file).CombinedOutput() //nolint:gosec // a pinned tool path from the recipe
	if err != nil {
		t.Fatalf("vmalert-tool unittest:\n%s", out)
	}
	t.Logf("%s", out)

	// Both kinds of case, for every rule.
	var cases struct {
		Tests []struct {
			Name  string `json:"name"`
			Alert []struct {
				AlertName string `json:"alertname"`
				Exp       []any  `json:"exp_alerts"`
			} `json:"alert_rule_test"`
		} `json:"tests"`
	}
	if err := yaml.Unmarshal(tests, &cases); err != nil {
		t.Fatal(err)
	}
	fires, silent := map[string]bool{}, map[string]bool{}
	for _, c := range cases.Tests {
		for _, a := range c.Alert {
			if len(a.Exp) > 0 {
				fires[a.AlertName] = true
			} else {
				silent[a.AlertName] = true
			}
		}
	}
	rules := rulesOf(t, docs[0])
	if len(rules) != ruleCount {
		t.Fatalf("%d rules rendered, want %d", len(rules), ruleCount)
	}
	for _, r := range rules {
		name := r["alert"].(string)
		if !fires[name] || !silent[name] {
			t.Errorf("%s needs a test that fires it and a negative one that must not (fires: %v, silent: %v)", name, fires[name], silent[name])
		}
	}
	if len(fires) == 0 {
		t.Fatal("no test fires any rule: the unit tests prove nothing")
	}
}

// With a collector named, each module's pod reports as `sluis-<module>`, and the retired
// SLUIS_LEGACY_METRICS switch is not rendered.
func TestTelemetryEnvNamesSluis(t *testing.T) {
	env := func(args ...string) map[string]string {
		out := map[string]string{}
		for _, d := range renderArgs(t, append([]string{"-f", filepath.Join("..", "cases", "sluis", "telemetry", "values.yaml")}, args...)...) {
			if d["kind"] != "Deployment" {
				continue
			}
			// The issuer's is the one the test reads; the others' are held by modules_test.go.
			if n, _ := dig(d, "metadata", "labels", "app.kubernetes.io/component"); n != "issuer" {
				continue
			}
			spec, _ := dig(d, "spec", "template", "spec", "containers")
			for _, c := range spec.([]any) {
				list, _ := c.(map[string]any)["env"].([]any)
				for _, e := range list {
					m := e.(map[string]any)
					if v, ok := m["value"].(string); ok {
						out[m["name"].(string)] = v
					}
				}
			}
		}
		return out
	}
	got := env()
	if got["OTEL_SERVICE_NAME"] != "sluis-issuer" {
		t.Errorf("default: OTEL_SERVICE_NAME=%q, want sluis-issuer", got["OTEL_SERVICE_NAME"])
	}
	if _, ok := got["SLUIS_LEGACY_METRICS"]; ok {
		t.Errorf("SLUIS_LEGACY_METRICS is rendered; the old metric names are gone")
	}
}
