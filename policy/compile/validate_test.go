package compile_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/policy/compile"
)

func TestValidateGitHubJobs(t *testing.T) {
	good := compile.GitHubJob{
		Repository: "acme/shop",
		Ref:        "refs/heads/main",
		Events:     []string{"push", "pull_request_target"},
		Workflows:  []compile.GitHubJobWorkflow{{Caller: ".github/workflows/ci.yaml"}},
	}

	if errs := compile.ValidateGitHubJobs("jobs", map[string]compile.GitHubJob{"acme:shop:ci": good}); len(errs) != 0 {
		t.Fatalf("a valid job was refused: %v", errs)
	}

	cases := map[string]func(j *compile.GitHubJob) string{
		"<scope>:<thing>:<role>": func(*compile.GitHubJob) string { return "shop-ci" },
		"is not <owner>/<name>":  func(j *compile.GitHubJob) string { j.Repository = "acme"; return "" },
		"is not one branch":      func(j *compile.GitHubJob) string { j.Ref = "refs/heads/feat*"; return "" },
		"cannot run on a tag":    func(j *compile.GitHubJob) string { j.Ref = "refs/tags/v*"; j.Events = []string{"schedule"}; return "" },
		"a proposed change":      func(j *compile.GitHubJob) string { j.Events = []string{"pull_request"}; return "" },
		"events is empty":        func(j *compile.GitHubJob) string { j.Events = nil; return "" },
		"is not a file under":    func(j *compile.GitHubJob) string { j.Workflows[0].Caller = "ci.yaml"; return "" },
		"without a ref": func(j *compile.GitHubJob) string {
			j.Workflows[0].Reusable = "acme/ci/.github/workflows/go.yaml@main"
			return ""
		},
	}

	for want, mutate := range cases {
		job := good
		job.Workflows = append([]compile.GitHubJobWorkflow(nil), good.Workflows...)
		name := "acme:shop:ci"

		if renamed := mutate(&job); renamed != "" {
			name = renamed
		}

		errs := compile.ValidateGitHubJobs("jobs", map[string]compile.GitHubJob{name: job})
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), want) || !strings.HasPrefix(errs[0].Error(), "jobs["+name+"]: ") {
			t.Errorf("errs = %v, want one saying %q", errs, want)
		}
	}
}

func TestGitHubAppsAndCatalog(t *testing.T) {
	apps := []compile.GitHubApp{{
		ID: "deployer", Org: "acme", Description: "deploys", Installation: "selected",
		Permissions: map[string]string{"contents": "write"},
	}}

	declared, errs := compile.ValidateGitHubApps("apps", apps)
	if len(errs) != 0 {
		t.Fatalf("valid App refused: %v", errs)
	}

	grants := map[string][]compile.GitHubAppGrant{
		"deployer": {{Group: "acme:shop:ci", Repositories: []string{"shop"}, Permissions: map[string]string{"contents": "read"}}},
		"ghost":    {{Group: "acme:shop:ci", Repositories: []string{"*"}, Permissions: map[string]string{"contents": "read"}}},
	}

	granted, errs := compile.ValidateGitHubAppGrants("grants", grants, declared)
	if !granted["acme:shop:ci"] || len(errs) != 1 || !strings.Contains(errs[0].Error(), "grants[ghost]: no App has that id") {
		t.Errorf("granted %v, errs %v", granted, errs)
	}

	catalog, err := compile.GitHubCatalog("apps", "grants", apps, grants, func(string) bool { return true })
	if err != nil || len(catalog) != 1 || catalog[0].Name != "deployer"+compile.GitHubAppNameSuffix || len(catalog[0].Grants) != 1 {
		t.Fatalf("catalog = %+v, %v", catalog, err)
	}

	grants["deployer"][0].Permissions["contents"] = "admin"

	if _, err := compile.GitHubCatalog("apps", "grants", apps, grants, func(string) bool { return false }); err == nil ||
		!strings.Contains(err.Error(), "declares no such group") || !strings.Contains(err.Error(), "more than the App's write") {
		t.Errorf("err = %v, want an undeclared group and a wider permission refused", err)
	}

	apps[0].Webhook = &compile.GitHubAppWebhook{URL: "http://hooks.example.com"}
	if _, errs := compile.ValidateGitHubApps("apps", apps); len(errs) != 3 {
		t.Errorf("errs = %v, want no events, no export and a plain-http URL", errs)
	}
}

func TestValidateWorkloads(t *testing.T) {
	tables := compile.WorkloadTables{Workloads: "workloads", Scopes: "the matrix", Accounts: "accounts"}
	accounts := map[string]compile.AWSAccount{"111122223333": {Name: "main", Issuer: "https://issuer.example.com"}}
	scopes := map[string]bool{"dev": true}

	ok := map[string]compile.Workload{
		"dev:shop:reader": {Cluster: "dev", Namespace: "shop", Name: "reader", Why: "reads"},
		"all:otel:writer": {AWS: compile.WorkloadAWSRoles{{Account: "111122223333", Role: "writer", Path: "/"}}, Why: "writes"},
	}
	if errs := compile.ValidateWorkloads(tables, ok, "k8s", scopes, accounts); len(errs) != 0 {
		t.Fatalf("valid workloads refused: %v", errs)
	}

	for want, w := range map[string]map[string]compile.Workload{
		"comes from the matrix":    {"dev:k8s:admin": {Cluster: "dev", Namespace: "a", Name: "b", Why: "x"}},
		"is not in the matrix":     {"prod:shop:reader": {Cluster: "dev", Namespace: "a", Name: "b", Why: "x"}},
		"is not a Kubernetes name": {"dev:shop:reader": {Cluster: "dev", Namespace: "A", Name: "b", Why: "x"}},
		"say what it does":         {"dev:shop:reader": {Cluster: "dev", Namespace: "a", Name: "b"}},
		"is not in accounts":       {"all:otel:writer": {AWS: compile.WorkloadAWSRoles{{Account: "444455556666", Role: "w", Path: "/"}}, Why: "x"}},
		"never both":               {"all:otel:writer": {Cluster: "dev", AWS: compile.WorkloadAWSRoles{{Account: "111122223333", Role: "w", Path: "/"}}, Why: "x"}},
		"is not an exact IAM role": {"all:otel:writer": {AWS: compile.WorkloadAWSRoles{{Account: "111122223333", Role: "w*", Path: "/"}}, Why: "x"}},
		"an internal group name":   {"reader": {Cluster: "dev", Namespace: "a", Name: "b", Why: "x"}},
	} {
		errs := compile.ValidateWorkloads(tables, w, "k8s", scopes, accounts)
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), want) {
			t.Errorf("errs = %v, want one saying %q", errs, want)
		}
	}

	bad := map[string]compile.AWSAccount{"12345": {Name: "", Issuer: "http://x", Algs: []string{"HS256"}}}
	if errs := compile.ValidateAWSAccounts("accounts", bad); len(errs) != 4 {
		t.Errorf("errs = %v, want the id, the name, the issuer and the algorithm refused", errs)
	}

	if got := compile.WorkloadGroups(ok); len(got) != 1 || got[0] != "dev:shop:reader" {
		t.Errorf("WorkloadGroups = %v, want the ServiceAccount workload only", got)
	}
}

func TestEndpoints(t *testing.T) {
	proxied := compile.Endpoints{Hostname: "app.example.com", Prefix: "/console", Proxied: true}
	if got := proxied.RedirectURIs(); len(got) != 1 || got[0] != "https://app.example.com/console/oauth2/callback" {
		t.Errorf("proxied redirect = %v", got)
	}

	if got := proxied.SignedOutURIs(); len(got) != 1 || got[0] != "https://app.example.com/console/" {
		t.Errorf("proxied signed out = %v", got)
	}

	moved := proxied
	moved.SignInPrefix = "/sso"

	if got := moved.RedirectURIs(); got[0] != "https://app.example.com/sso/callback" {
		t.Errorf("moved prefix redirect = %v", got)
	}

	issuer := compile.Endpoints{Hostname: "id.example.com", Prefix: "/console", ServedByIssuer: true}
	if got := issuer.RedirectURIs(); len(got) != 1 || got[0] != "https://id.example.com/console/" || issuer.SignedOutURIs() != nil {
		t.Errorf("issuer console = %v / %v", got, issuer.SignedOutURIs())
	}

	own := compile.Endpoints{Hostname: "cd.example.com"}
	if own.RedirectURIs() != nil {
		t.Errorf("a client running its own flow got a derived redirect: %v", own.RedirectURIs())
	}

	cli := compile.Endpoints{Loopback: true, Redirects: []string{"http://127.0.0.1/callback"}}
	if err := cli.ValidateURLs(); err != nil || cli.SignedOutURIs() != nil {
		t.Errorf("loopback client: %v, %v", err, cli.SignedOutURIs())
	}

	logout := compile.Endpoints{Hostname: "app.example.com", BackChannelLogout: "/logout"}
	if got := logout.BackChannelLogoutURI(); got != "https://app.example.com/logout" {
		t.Errorf("logout = %q", got)
	}

	for want, e := range map[string]compile.Endpoints{
		"host-relative":            {Hostname: "app.example.com", Redirects: []string{"callback"}},
		"loopback: true":           {Hostname: "app.example.com", Redirects: []string{"http://localhost/cb"}},
		"one row names one":        {Hostname: "app.example.com", SignedOut: []string{"https://other.example.com/"}},
		"is not https":             {Hostname: "app.example.com", Redirects: []string{"http://app.example.com/cb"}},
		"landing page that is not": {Hostname: "app.example.com", SignedOut: []string{"http://app.example.com/bye"}},
		"a loopback redirect":      {Hostname: "app.example.com", Redirects: []string{"http://[::1]/cb"}},
	} {
		if err := e.ValidateURLs(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want one saying %q", err, want)
		}
	}

	// The loopback exception stays, and a back-channel endpoint inside the
	// cluster is not a browser redirect.
	for name, e := range map[string]compile.Endpoints{
		"loopback v4":  {Loopback: true, Redirects: []string{"http://127.0.0.1/cb"}},
		"loopback v6":  {Loopback: true, Redirects: []string{"http://[::1]:8080/cb"}},
		"localhost":    {Loopback: true, Redirects: []string{"http://localhost/cb"}},
		"https":        {Hostname: "app.example.com", Redirects: []string{"https://app.example.com/cb"}, SignedOut: []string{"https://app.example.com/bye"}},
		"upper scheme": {Hostname: "app.example.com", Redirects: []string{"HTTPS://app.example.com/cb"}},
		"in-cluster":   {Hostname: "app.example.com", BackChannelLogout: "http://app.example.com/logout"},
	} {
		if err := e.ValidateURLs(); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}

	both := compile.Endpoints{Hostname: "app.example.com", Redirects: []string{"/"}}
	if err := both.ValidateLanding(); err == nil || !strings.Contains(err.Error(), "both a redirect and a signed-out landing page") {
		t.Errorf("err = %v, want the redirect that is also the landing page refused", err)
	}

	if err := compile.ValidateGrantNames([]string{"dev:shop:deployer", "rung:admin"}); err == nil || !strings.Contains(err.Error(), "rung:admin") {
		t.Errorf("err = %v, want the rung refused", err)
	}
}

func TestValidateAudiencesAndClaims(t *testing.T) {
	p := compile.Roster(exampleModel(), exampleConventions())

	clients := []compile.AudienceClient{
		{Name: "console", Kind: policy.KindConfidential, Requires: []string{"dev:k8s:viewer"}, SecretName: "console-client"},
		{Name: "cli", Kind: policy.KindPublic, Requires: []string{"dev:k8s:viewer"}, SigningAlg: "RS256"},
	}
	if err := compile.ValidateAudiences(p, clients, nil, nil); err != nil {
		t.Fatalf("valid audiences refused: %v", err)
	}

	clients[1].SigningAlg = "none"
	if err := compile.ValidateAudiences(p, clients, nil, nil); err == nil {
		t.Error("an unknown signing algorithm was accepted")
	}

	if err := compile.ValidateAudiences(nil, clients, nil, nil); err != nil {
		t.Errorf("a nil policy is no check, got %v", err)
	}

	claims := map[string]policy.Fragment{"nobody:has:this": {}}
	if err := compile.ValidateClaims(p, claims); err == nil || !strings.HasPrefix(err.Error(), "claims: ") {
		t.Errorf("err = %v, want a fragment for an undeclared group refused", err)
	}
}
