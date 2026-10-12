package chart_test

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// workloads returns the Deployments and CronJobs of a render by name.
func workloads(t *testing.T, rendered string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, part := range strings.Split(rendered, "\n---\n") {
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(part), &doc); err != nil || doc == nil {
			continue
		}
		kind, _ := doc["kind"].(string)
		if kind != "Deployment" && kind != "CronJob" {
			continue
		}
		md, _ := doc["metadata"].(map[string]any)
		name, _ := md["name"].(string)
		out[name] = doc
	}
	return out
}

func objectAnnotations(doc map[string]any) map[string]any {
	md, _ := doc["metadata"].(map[string]any)
	a, _ := md["annotations"].(map[string]any)
	return a
}

// podTemplate is where a workload's pods are described: spec.template for a
// Deployment, spec.jobTemplate.spec.template for a CronJob.
func podAnnotations(doc map[string]any) map[string]any {
	spec, _ := doc["spec"].(map[string]any)
	tpl, _ := spec["template"].(map[string]any)
	if jt, ok := spec["jobTemplate"].(map[string]any); ok {
		js, _ := jt["spec"].(map[string]any)
		tpl, _ = js["template"].(map[string]any)
	}
	md, _ := tpl["metadata"].(map[string]any)
	a, _ := md["annotations"].(map[string]any)
	return a
}

// A reloader reads the workload object. The annotation on the pod template is
// inert, so the tests hold the object itself, and the pods not to carry it.
func TestWorkloadAnnotationsLandOnTheObjectNotThePods(t *testing.T) {
	const optOut = "reloader.stakater.com/auto"
	overlay := `
commonAnnotations:
  example.test/team: audit
  example.test/winner: common
observe:
  annotations:
    ` + optOut + `: "false"
    example.test/winner: component
query:
  annotations:
    example.test/query: "yes"
writer:
  annotations:
    example.test/writer: "yes"
jobs:
  notary:
    annotations:
      ` + optOut + `: "false"
  verify:
    annotations:
      example.test/verify: "yes"
  purge:
    annotations:
      example.test/purge: "yes"
  clockSync:
    annotations:
      example.test/clock: "yes"
`
	out, stderr, err := helmOutput(t, "examples/direct.yaml", overlay)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, stderr)
	}
	got := workloads(t, out)
	want := map[string]map[string]string{
		"audit-observe":    {optOut: "false", "example.test/team": "audit", "example.test/winner": "component"},
		"audit-query":      {"example.test/query": "yes", "example.test/team": "audit", "example.test/winner": "common"},
		"audit":            {"example.test/writer": "yes", "example.test/team": "audit"},
		"audit-notary":     {optOut: "false", "example.test/team": "audit"},
		"audit-verify":     {"example.test/verify": "yes", "example.test/team": "audit"},
		"audit-purge":      {"example.test/purge": "yes", "example.test/team": "audit"},
		"audit-clock-sync": {"example.test/clock": "yes", "example.test/team": "audit"},
	}
	for name, annotations := range want {
		doc, ok := got[name]
		if !ok {
			t.Errorf("no workload %q in the render (have %v)", name, keys(got))
			continue
		}
		obj, pods := objectAnnotations(doc), podAnnotations(doc)
		for k, v := range annotations {
			if obj[k] != v {
				t.Errorf("%s: metadata.annotations[%q] = %v, want %q", name, k, obj[k], v)
			}
			if _, leaked := pods[k]; leaked {
				t.Errorf("%s: %q reached the pod template as well", name, k)
			}
		}
	}
}

// Nothing is rendered by default: the chart takes no side on a reloader.
func TestNoWorkloadAnnotationsByDefault(t *testing.T) {
	out, stderr, err := helmOutput(t, "examples/direct.yaml")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, stderr)
	}
	got := workloads(t, out)
	if len(got) == 0 {
		t.Fatal("the render has no workloads")
	}
	for name, doc := range got {
		if a := objectAnnotations(doc); len(a) != 0 {
			t.Errorf("%s: metadata.annotations = %v, want none", name, a)
		}
	}
	if strings.Contains(out, "reloader.stakater.com") {
		t.Error("the default render names a reloader")
	}
}

// Stream mode has a receiver and consumers, both from the writer's template;
// each takes its own component's annotations.
func TestStreamWorkloadsTakeTheirOwnAnnotations(t *testing.T) {
	overlay := `
commonAnnotations: {example.test/team: audit}
receiver:
  annotations: {example.test/role: receiver}
writer:
  annotations: {example.test/role: consumer}
`
	out, stderr, err := helmOutput(t, "testdata/values/stream.yaml", overlay)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, stderr)
	}
	got := workloads(t, out)
	for name, role := range map[string]string{"audit": "receiver", "audit-consumer": "consumer"} {
		doc, ok := got[name]
		if !ok {
			t.Fatalf("no workload %q (have %v)", name, keys(got))
		}
		a := objectAnnotations(doc)
		if a["example.test/role"] != role || a["example.test/team"] != "audit" {
			t.Errorf("%s: metadata.annotations = %v", name, a)
		}
	}
}

// A value that is not a string is refused by the schema, not rendered.
func TestWorkloadAnnotationsMustBeStrings(t *testing.T) {
	_, _, err := helmOutput(t, "examples/direct.yaml", "observe:\n  annotations:\n    x: 1\n")
	if err == nil {
		t.Fatal("a non-string annotation rendered")
	}
}

func keys[V any](m map[string]V) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
