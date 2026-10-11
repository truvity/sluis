package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
)

const explainFixture = "testdata/explain.policy.yaml"

type explained struct {
	Audience string   `json:"audience"`
	Kept     []string `json:"groups_kept"`
	Dropped  []struct {
		Group  string `json:"group"`
		Reason string `json:"reason"`
	} `json:"groups_dropped"`
	Claims map[string]any `json:"claims"`
}

func runExplain(t *testing.T, args ...string) (explained, error) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	saved := stdout
	stdout = file
	defer func() { stdout = saved }()

	err = explainCommand(append([]string{"--policy", explainFixture, "--format", "json"}, args...))
	if err != nil {
		return explained{}, err
	}
	raw, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	var out explained
	if err = json.NewDecoder(bytes.NewReader(raw)).Decode(&out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	return out, nil
}

func filtersOf(t *testing.T, claims map[string]any) any {
	t.Helper()
	va, _ := claims["vm_access"].(map[string]any)
	return va["logs_extra_stream_filters"]
}

// A rendered policy.yaml carries apiVersion, exchange, apps and controllers
// beside the tables; the command loads it as the issuer does.
func TestExplainLoadsARenderedPolicyDocument(t *testing.T) {
	out, err := runExplain(t, "--audience", "logs", "--email", "a@example.test")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if out.Audience != "logs" || len(out.Kept) != 0 {
		t.Errorf("a person in no group: %+v", out)
	}
}

func TestExplainNamespaceOnlyPersonGetsExactlyTheirFragment(t *testing.T) {
	out, err := runExplain(t, "--audience", "logs", "--email", "a@example.test", "--directory-group", "dms-team@example.test")
	if err != nil {
		t.Fatal(err)
	}
	want := `_stream:{"k8s.cluster.name"="prod","kubernetes.pod_namespace"="dms"}`
	if got := filtersOf(t, out.Claims); got != want {
		t.Errorf("filters = %v, want %s", got, want)
	}
	if !slices.Equal(out.Kept, []string{"prod:logs:dms"}) {
		t.Errorf("kept = %v", out.Kept)
	}
	if _, ok := out.Claims["groups"]; ok {
		t.Error("groups is printed beside the claims, not inside them")
	}
}

func TestExplainTwoFragmentsAreJoined(t *testing.T) {
	out, err := runExplain(t, "--audience", "logs", "--email", "a@example.test",
		"--directory-group", "kyc-team@example.test", "--directory-group", "dms-team@example.test")
	if err != nil {
		t.Fatal(err)
	}
	want := `_stream:{"k8s.cluster.name"="prod","kubernetes.pod_namespace"="dms" or "k8s.cluster.name"="prod","kubernetes.pod_namespace"="kyc"}`
	if got := filtersOf(t, out.Claims); got != want {
		t.Errorf("filters = %v, want %s", got, want)
	}
}

func TestExplainRefusesAnUnknownAudience(t *testing.T) {
	_, err := runExplain(t, "--audience", "nowhere", "--email", "a@example.test")
	var usage usageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

func TestExplainScopingDropsAGroupOutsideTheAudience(t *testing.T) {
	out, err := runExplain(t, "--audience", "logs", "--email", "a@example.test",
		"--directory-group", "dms-team@example.test", "--directory-group", "grafana-admins@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Kept, []string{"prod:logs:dms"}) {
		t.Errorf("kept = %v", out.Kept)
	}
	if len(out.Dropped) != 1 || out.Dropped[0].Group != "prod:grafana:admin" || out.Dropped[0].Reason == "" {
		t.Errorf("dropped = %+v", out.Dropped)
	}
}

func TestExplainAWorkloadSubject(t *testing.T) {
	out, err := runExplain(t, "--audience", "logs", "--subject", "prod:k8s:acme-jobs:reader")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Kept, []string{"prod:workload:reader"}) {
		t.Errorf("kept = %v", out.Kept)
	}
	if _, err = runExplain(t, "--audience", "logs", "--subject", "nonsense"); err == nil {
		t.Error("a subject that is no workload was accepted")
	}
}

func TestExplainNeedsExactlyOneCaller(t *testing.T) {
	for _, args := range [][]string{
		{"--audience", "logs"},
		{"--audience", "logs", "--email", "a@example.test", "--subject", "k8s:a:b"},
	} {
		if _, err := runExplain(t, args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
