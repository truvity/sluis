package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/policy"
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
	want := []any{`"k8s.cluster.name"="prod","kubernetes.pod_namespace"="dms"`}
	if got := filtersOf(t, out.Claims); !reflect.DeepEqual(got, want) {
		t.Errorf("filters = %v, want %v", got, want)
	}
	if !slices.Equal(out.Kept, []string{"prod:logs:dms"}) {
		t.Errorf("kept = %v", out.Kept)
	}
	if _, ok := out.Claims["groups"]; ok {
		t.Error("groups is printed beside the claims, not inside them")
	}
}

func TestExplainTwoFragmentsAreConcatenated(t *testing.T) {
	out, err := runExplain(t, "--audience", "logs", "--email", "a@example.test",
		"--directory-group", "kyc-team@example.test", "--directory-group", "dms-team@example.test")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{
		`"k8s.cluster.name"="prod","kubernetes.pod_namespace"="dms"`,
		`"k8s.cluster.name"="prod","kubernetes.pod_namespace"="kyc"`,
	}
	if got := filtersOf(t, out.Claims); !reflect.DeepEqual(got, want) {
		t.Errorf("filters = %v, want %v", got, want)
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

// --policy reads a file and --live asks the issuer: asking for both is a
// mistake, not a precedence rule, and neither is a mistake too.
func TestExplainPolicyAndLiveAreExclusive(t *testing.T) {
	for name, args := range map[string][]string{
		"both":                  {"--policy", explainFixture, "--live", "--audience", "logs", "--email", "a@example.test"},
		"neither":               {"--audience", "logs", "--email", "a@example.test"},
		"issuer without live":   {"--policy", explainFixture, "--issuer", "https://issuer.example", "--audience", "logs", "--email", "a@example.test"},
		"directory group, live": {"--live", "--audience", "logs", "--email", "a@example.test", "--directory-group", "dms-team@example.test"},
		"live with no audience": {"--live", "--email", "a@example.test"},
		"live with no caller":   {"--live", "--audience", "logs"},
		"live with both":        {"--live", "--audience", "logs", "--email", "a@example.test", "--subject", "k8s:a:b"},
	} {
		err := explainCommand(args)
		var usage usageError
		if !errors.As(err, &usage) {
			t.Errorf("%s: err = %v, want a usage error", name, err)
		}
	}
}

// The live form prints what the offline form prints for the same facts, byte
// for byte, in both formats; and it asks the issuer as the signed-in person.
func TestExplainLiveOutputIsTheOfflineOutput(t *testing.T) {
	doc, err := config.Load[config.PolicyDocument](explainFixture)
	if err != nil {
		t.Fatal(err)
	}
	groups := []string{"dms-team@example.test", "grafana-admins@example.test"}
	facts := policy.ExplainInput("a@example.test", groups)
	want, err := doc.Policy.Explain("logs", facts)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"yaml", "json"} {
		issuer := newClientsIssuer(t, http.StatusOK, string(reply))
		offline, err := captureStdoutErr(t, func() error {
			return run([]string{"explain", "--policy", explainFixture, "--format", format, "--audience", "logs",
				"--email", "a@example.test", "--directory-group", groups[0], "--directory-group", groups[1]})
		})
		if err != nil {
			t.Fatal(err)
		}
		live, err := captureStdoutErr(t, func() error {
			return run([]string{"explain", "--live", "--issuer", issuer.URL, "--format", format, "--audience", "logs", "--email", "a@example.test"})
		})
		if err != nil {
			t.Fatal(err)
		}
		if live != offline {
			t.Errorf("%s: live and offline differ\nlive:\n%s\noffline:\n%s", format, live, offline)
		}
		if len(issuer.paths) != 1 || issuer.paths[0] != "POST /.access/policy/explain" || issuer.auth[0] != "Bearer the-bearer" {
			t.Errorf("%s: requests %v auth %v", format, issuer.paths, issuer.auth)
		}
		if b := issuer.bodies[0]; len(b) != 2 || b["audience"] != "logs" || b["email"] != "a@example.test" {
			t.Errorf("%s: body %v", format, b)
		}
	}
}

func TestExplainLiveSendsAWorkloadSubject(t *testing.T) {
	issuer := newClientsIssuer(t, http.StatusOK, `{"audience":"logs","held":[],"groups_kept":[],"groups_dropped":[],"claims":{},"lifetime":"","admitted":false,"admitted_by":""}`)
	if _, err := captureStdoutErr(t, func() error {
		return run([]string{"explain", "--live", "--issuer", issuer.URL, "--audience", "logs", "--subject", "prod:k8s:acme-jobs:reader"})
	}); err != nil {
		t.Fatal(err)
	}
	if b := issuer.bodies[0]; len(b) != 2 || b["subject"] != "prod:k8s:acme-jobs:reader" {
		t.Errorf("body %v", b)
	}
}

// An issuer that refuses the caller says so, and nothing is printed.
func TestExplainLiveSaysWhenTheCallerIsNotAnOperator(t *testing.T) {
	issuer := newClientsIssuer(t, http.StatusForbidden, "explaining a token is an operator's")
	out, err := captureStdoutErr(t, func() error {
		return run([]string{"explain", "--live", "--issuer", issuer.URL, "--audience", "logs", "--email", "a@example.test"})
	})
	if !errors.Is(err, errNotGranted) {
		t.Errorf("err = %v, want errNotGranted", err)
	}
	if out != "" {
		t.Errorf("printed %q", out)
	}
}
