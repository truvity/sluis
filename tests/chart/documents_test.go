package chart_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/truvity/sluis/config"
)

// The documents `sluisctl render` writes for the Truvity-shaped installation
// (config/testdata), and the case that hands them to the chart.
var (
	truvityService = filepath.Join("..", "..", "config", "testdata", "truvity.sluis.yaml")
	truvityPolicy  = filepath.Join("..", "..", "config", "testdata", "truvity.policy.yaml")
	truvityCase    = caseValues("documents-truvity")
)

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // a path in this repository
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// helmTemplate runs `helm template` and returns what it printed and the error
// it failed with, for a test that expects a refusal.
func helmTemplate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(helm(t), append([]string{"template", "sluis", chartDir, "--namespace", "sluis"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return stderr.String(), err
	}
	return string(out), nil
}

// With the rendered documents the chart passes them through unchanged: the
// ConfigMaps hold exactly the bytes `sluisctl render` wrote (no apiVersion
// added, nothing renamed, reordered or dropped), and the case's values hold
// the same bytes as the files.
func TestTheChartPutsTheRenderedDocumentsIntoItsConfigMapsUnchanged(t *testing.T) {
	values := load(t, truvityCase)
	for key, file := range map[string]string{"service": truvityService, "policy": truvityPolicy} {
		if got, _ := dig(values, "documents", key); got != read(t, file) {
			t.Fatalf("documents.%s of the case is not %s: render the installation again and copy the file in", key, file)
		}
	}
	var service, policy string
	for _, doc := range render(t, truvityCase, "sluis") {
		if doc["kind"] != "ConfigMap" {
			continue
		}
		if data, ok := dig(doc, "data", "config.yaml"); ok {
			service = data.(string)
		}
		if data, ok := dig(doc, "data", "policy.yaml"); ok {
			policy = data.(string)
		}
	}
	if service != read(t, truvityService) {
		t.Errorf("the service ConfigMap is not the rendered document:\n%s", service)
	}
	if policy != read(t, truvityPolicy) {
		t.Errorf("the policy ConfigMap is not the rendered document:\n%s", policy)
	}
}

// What the chart renders around the documents follows the documents: the
// controllers the service document names, no signing Certificate (KMS signs),
// no Secret projected for a confidential client (OpenBao delivers it), and the
// documents are files the binary accepts.
func TestTheChartFollowsTheRenderedDocuments(t *testing.T) {
	var deployment map[string]any
	for _, doc := range render(t, truvityCase, "sluis") {
		if doc["kind"] == "Deployment" {
			deployment = doc
		}
		if name, _ := dig(doc, "metadata", "name"); doc["kind"] == "Certificate" && strings.Contains(name.(string), "signing") {
			t.Errorf("a signing Certificate %v is rendered with KMS signing", name)
		}
	}
	if deployment == nil {
		t.Fatal("no Deployment")
	}
	volumes, _ := volumeNames(t, deployment)
	for _, v := range volumes {
		if strings.Contains(v, "client") || strings.Contains(v, "inputs") {
			t.Errorf("a volume %q is projected for secrets OpenBao delivers", v)
		}
	}
	for _, v := range []string{"openbao-ca", "verify-keys"} {
		found := false
		for _, n := range volumes {
			found = found || strings.Contains(n, v)
		}
		if !found {
			t.Errorf("no %s volume among %v", v, volumes)
		}
	}
	var service any
	if err := yaml.Unmarshal([]byte(read(t, truvityService)), &service); err != nil {
		t.Fatal(err)
	}
	if err := config.Validate("sluis", service); err != nil {
		t.Error(err)
	}
}

// The chart holds the documents to what it mounts and never rewrites them: a
// disagreement is a refusal naming the key, and the fix is in the installation.
func TestTheChartRefusesDocumentsThatDisagreeWithWhatItMounts(t *testing.T) {
	service, policy := read(t, truvityService), read(t, truvityPolicy)
	dir := t.TempDir()
	n := 0
	write := func(name, body string) string {
		n++
		p := filepath.Join(dir, strings.Replace(name, ".", "-"+strings.Repeat("x", n)+".", 1))
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := []string{"-f", truvityCase}
	with := func(svc, pol string) []string {
		return append(append([]string{}, base...),
			"--set-file", "documents.service="+write("sluis.yaml", svc),
			"--set-file", "documents.policy="+write("policy.yaml", pol))
	}
	//nolint:lll // a table of one-line cases
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"another release":                      {with(strings.Replace(service, "release: sluis", "release: other", 1), policy), "config.release"},
		"another policy file":                  {with(strings.Replace(service, "policy.yaml", "elsewhere.yaml", 1), policy), "config.policy.file"},
		"another console URL":                  {with(strings.Replace(service, "publicURL: https://access.example.test/console", "publicURL: https://access.example.test/ui", 1), policy), "config.publicURL"},
		"a controller reading another console": {with(strings.Replace(service, "http://sluis.sluis.svc:8080/console", "http://elsewhere.svc:8080/console", 1), policy), "consoleURL"},
		"an audit token elsewhere":             {with(strings.Replace(service, "/var/run/audit/token", "/tmp/token", 1), policy), "audit.tokenFile"},
		"an http OpenBao":                      {with(strings.Replace(service, "https://openbao.example.test", "http://openbao.example.test", 1), policy), "must be an https URL"},
		"a token value in adapter settings":    {with(strings.Replace(service, "      root: sluis\n", "      root: sluis\n      token: s3cr3t\n", 1), policy), "adapters.secrets.settings.token"},
		"a nested password":                    {with(strings.Replace(service, "        method: kubernetes\n", "        method: kubernetes\n        password: x\n", 1), policy), "settings.auth.password"},
		"the policy as the service":            {with(service, service), "documents.policy"},
		"the service as the policy":            {with(policy, policy), "documents.service"},
		"a verify-only key at another path":    {with(strings.Replace(service, "verify-keys/0.pem", "verify-keys/9.pem", 1), policy), "verifyOnly"},
		"only one document":                    {append(append([]string{}, base...), "--set", "documents.policy="), "go together"},
		"the policy said twice":                {append(append([]string{}, base...), "--set", "policy.groups.x.members[0]=a@example.test"), "say each once"},
		"the clusters said twice":              {append(append([]string{}, base...), "--set", "exchange.clusters[0].name=a,exchange.clusters[0].issuer=https://a.example"), "say each once"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := helmTemplate(t, c.args...)
			if err == nil {
				t.Fatalf("rendered:\n%.400s", out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("the refusal does not name %q:\n%s", c.want, out)
			}
		})
	}
}

// Values-mode keeps working for one minor and says it is deprecated; the
// documents mode says nothing.
func TestValuesModeIsDeprecatedAndDocumentsModeIsNot(t *testing.T) {
	notes := func(values string) string {
		cmd := exec.Command(helm(t), "install", "sluis", chartDir, "--dry-run=client", "--namespace", "sluis", "-f", values)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("helm install --dry-run: %v\n%s", err, stderr.String())
		}
		return string(out)
	}
	if out := notes(caseValues("minimal")); !strings.Contains(out, "DEPRECATED") || !strings.Contains(out, "documents.service") {
		t.Errorf("values-mode says nothing about its deprecation:\n%s", out)
	}
	if out := notes(truvityCase); strings.Contains(out, "DEPRECATED") {
		t.Errorf("documents mode says it is deprecated:\n%s", out)
	}
}
