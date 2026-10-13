package compile_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/policy/compile"
)

// exampleConventions are an installation's decisions in the shape a real
// one writes them: a `k8s` cluster tier with three rungs, three database
// levels, OpenBAO-style mirrors of the cluster admins, two host logins held
// by rung, and a directory of its own at example.com.
func exampleConventions() compile.Conventions {
	return compile.Conventions{
		ClusterThing: "k8s",
		ClusterRoles: map[string]string{
			"breakglass": "admin", "admin": "admin", "deployer": "viewer",
			"developer": "viewer", "viewer": "viewer", "auditor": "auditor",
		},
		ClusterViewerRole: "viewer",
		ClusterAdminRole:  "admin",
		DatabaseLevels: []compile.DatabaseLevel{
			{Name: "admin", From: "admin"},
			{Name: "observer", From: "viewer"},
			{Name: "read", From: "auditor"},
		},
		UnitDatabaseLevel:  "admin",
		UnitDatabaseScopes: []string{"dev"},
		ViewerImpliedBy:    []string{compile.ProjectDeployer, compile.ProjectApprover, "admin"},
		MachineReaderRoles: []string{"metrics-gate"},
		AdminMirrors: []compile.AdminMirror{
			{Thing: "vault", Role: "operator", FromScope: "ops"},
			{Thing: "vault", Role: "writer"},
		},
		RungGrants: []compile.RungGrant{
			{Thing: "ssh", Role: "admin", Rungs: []string{"sre", "admin"}},
			{Thing: "builders", Role: "user", Rungs: []string{"engineer"}, Scopes: []string{"dev"}},
		},
		OwnDirectory: func(address string) bool { return strings.HasSuffix(address, "@example.com") },
	}
}

func exampleModel() compile.Model {
	return compile.Model{
		Rungs: map[string]compile.Rung{
			"admin":    {Groups: []string{"role-admin@example.com"}, SessionDuration: "PT4H"},
			"sre":      {Groups: []string{"role-sre@example.com"}, SessionDuration: "PT8H"},
			"engineer": {Groups: []string{"team-eng@example.com"}, Users: []string{"ada@example.com"}, SessionDuration: "PT10H"},
			"auditor":  {Groups: []string{"role-audit@example.com"}, SessionDuration: "PT2H"},
		},
		Scopes: map[string]map[string]string{
			"ops":    {"admin": "admin", "sre": "admin", "auditor": "auditor"},
			"dev":    {"admin": "admin", "engineer": "developer", "auditor": "auditor"},
			"backup": {"admin": "admin"},
		},
		ScopeFacts: map[string]compile.ScopeFacts{
			"ops": {Kubernetes: true},
			"dev": {Kubernetes: true, Database: true},
		},
		Projects: map[string]compile.Project{
			"shop": {
				Scopes:       []string{"dev"},
				ViewerScopes: []string{"dev"},
				Deployers:    []string{"team-shop@example.com"},
			},
			"ledger": {
				Scopes:       []string{"dev"},
				SecretScopes: []string{"ops"},
				ViewerScopes: []string{"ops"},
				Deployers:    []string{"team-ledger@example.com"},
				Approvers:    []string{"role-approvers@example.com"},
				Viewers:      []string{"team-finance@example.com"},
			},
		},
		Applications: map[string]compile.Population{
			"all:sluis:viewer": {Groups: []string{"all@example.com", "all@globex.example"}},
			"dev:ssh:admin":    {Groups: []string{"team-ops@globex.example"}},
		},
		GitHub: map[string]compile.GitHubOrg{
			"acme": {
				Members: []string{"all@example.com"},
				Teams: map[string]compile.GitHubTeam{
					"team-shop": {Members: []string{"team-shop@example.com"}, Maintainers: []string{"role-admin@example.com"}},
				},
				Ignore: []string{"bot-login", "bot-login"},
			},
		},
		Slack: map[string]compile.SlackWorkspace{
			"main": {Channels: map[string]compile.SlackChannel{
				"shop": {Members: []string{"team-shop@example.com"}},
			}},
		},
		Jobs: map[string]compile.GitHubJob{
			"acme:repo-shop:release": {
				Repository: "acme/shop",
				Ref:        "refs/tags/v*",
				Events:     []string{"push"},
				Workflows:  []compile.GitHubJobWorkflow{{Caller: ".github/workflows/release.yaml", Reusable: "acme/ci/.github/workflows/go.yaml"}},
			},
		},
		Workloads: map[string]compile.Workload{
			"dev:shop:metrics-gate": {Cluster: "dev", Namespace: "shop", Name: "gate", Why: "reads the metrics"},
			"all:otel:writer": {AWS: compile.WorkloadAWSRoles{
				{Account: "111122223333", Role: "writer", Path: "/"},
			}, Why: "writes telemetry"},
		},
		Employees: map[string][]string{"ada": {"ada@example.com", "ada@globex.example", "ada@example.com"}},
	}
}

func groupsByName(p *compile.Policy) map[string]compile.Group {
	out := make(map[string]compile.Group, len(p.Groups))
	for i := range p.Groups {
		out[p.Groups[i].Name] = p.Groups[i]
	}

	return out
}

func TestRosterDerivesTheModel(t *testing.T) {
	p := compile.Roster(exampleModel(), exampleConventions())
	if p == nil {
		t.Fatal("Roster returned nil for a model with a matrix")
	}

	groups := groupsByName(p)

	want := map[string]struct {
		members, emails, rungs []string
	}{
		"rung:admin":          {members: []string{"role-admin@example.com"}},
		"rung:engineer":       {members: []string{"team-eng@example.com"}, emails: []string{"ada@example.com"}},
		"ops:k8s:admin":       {members: []string{"role-admin@example.com", "role-sre@example.com"}, rungs: []string{"admin", "sre"}},
		"ops:k8s:auditor":     {members: []string{"role-audit@example.com"}, rungs: []string{"auditor"}},
		"dev:k8s:viewer":      {members: []string{"team-eng@example.com"}, emails: []string{"ada@example.com"}, rungs: []string{"engineer"}},
		"dev:shop:deployer":   {members: []string{"team-shop@example.com"}},
		"dev:shop:approver":   {members: []string{"team-shop@example.com"}},
		"dev:shop:admin":      {members: []string{"role-admin@example.com", "team-shop@example.com"}, rungs: []string{"admin"}},
		"dev:shop:read":       {members: []string{"role-audit@example.com"}, rungs: []string{"auditor"}},
		"dev:shop:viewer":     {members: []string{"team-eng@example.com"}, emails: []string{"ada@example.com"}, rungs: []string{"engineer"}},
		"ops:ledger:approver": {members: []string{"role-approvers@example.com"}},
		"ops:ledger:viewer":   {members: []string{"team-finance@example.com"}},
		"all:vault:operator":  {members: []string{"role-admin@example.com", "role-sre@example.com"}},
		"dev:vault:writer":    {members: []string{"role-admin@example.com"}},
		"ops:ssh:admin":       {members: []string{"role-admin@example.com", "role-sre@example.com"}, rungs: []string{"admin", "sre"}},
		"dev:ssh:admin": {
			members: []string{"role-admin@example.com", "role-sre@example.com", "team-ops@globex.example"},
			rungs:   []string{"admin", "sre"},
		},
		"dev:builders:user":         {members: []string{"team-eng@example.com"}, emails: []string{"ada@example.com"}, rungs: []string{"engineer"}},
		"all:sluis:viewer":          {members: []string{"all@example.com"}},
		"emp:ada":                   {emails: []string{"ada@example.com", "ada@globex.example"}},
		"acme:org:member":           {members: []string{"all@example.com"}},
		"acme:team-shop:member":     {members: []string{"team-shop@example.com"}},
		"acme:team-shop:maintainer": {members: []string{"role-admin@example.com"}},
		"main:slack-shop:member":    {members: []string{"team-shop@example.com"}},
	}

	for name, w := range want {
		got, ok := groups[name]
		if !ok {
			t.Errorf("group %s is missing", name)

			continue
		}

		if !slices.Equal(got.Members, w.members) || !slices.Equal(got.Emails, w.emails) || !slices.Equal(got.Rungs, w.rungs) {
			t.Errorf("group %s = members %v emails %v rungs %v, want %v %v %v", name, got.Members, got.Emails, got.Rungs, w.members, w.emails, w.rungs)
		}
	}

	// No scope without a cluster gets a cluster tier, no running scope without
	// a database gets a level, and a project's viewer on a scope it does not
	// ask for one is not minted.
	for _, absent := range []string{"backup:k8s:admin", "ops:ledger:admin", "dev:ledger:viewer", "ops:vault:operator", "ops:builders:user"} {
		if _, ok := groups[absent]; ok {
			t.Errorf("group %s should not exist", absent)
		}
	}

	if len(p.Deferred) != 1 || p.Deferred[0].Group != "all:sluis:viewer" || p.Deferred[0].Subject != "all@globex.example" {
		t.Errorf("deferred = %+v, want the partner directory's installation-wide grant", p.Deferred)
	}

	if !strings.Contains(p.Deferred[0].Reason, "<workspace>:sluis:viewer") {
		t.Errorf("deferred reason %q does not name the scoped grant", p.Deferred[0].Reason)
	}

	wantLifetimes := []compile.Lifetime{
		{Group: policy.LifetimeDefault, Duration: "2h"},
		{Group: "rung:admin", Duration: "4h"},
		{Group: "rung:auditor", Duration: "2h"},
		{Group: "rung:engineer", Duration: "10h"},
		{Group: "rung:sre", Duration: "8h"},
	}
	if !slices.Equal(p.Lifetimes, wantLifetimes) {
		t.Errorf("lifetimes = %+v, want %+v", p.Lifetimes, wantLifetimes)
	}

	job := groups["acme:repo-shop:release"]
	if len(job.GitHub) != 1 || job.GitHub[0].RefType != compile.GitHubRefTag ||
		job.GitHub[0].WorkflowRef != "acme/shop/.github/workflows/release.yaml@refs/tags/v*" ||
		job.GitHub[0].JobWorkflowRef != "acme/ci/.github/workflows/go.yaml@*" {
		t.Errorf("job matchers = %+v", job.GitHub)
	}

	wantSA := compile.ServiceAccountMatcher{Cluster: "dev", Namespace: "shop", Name: "gate"}
	if sa := groups["dev:shop:metrics-gate"].ServiceAccounts; len(sa) != 1 || sa[0] != wantSA {
		t.Errorf("workload matchers = %+v", sa)
	}

	if aws := groups["all:otel:writer"].AWS; len(aws) != 1 || aws[0] != (compile.AWSMatcher{Account: "111122223333", Role: "writer", Path: "/"}) {
		t.Errorf("aws matchers = %+v", aws)
	}

	if len(p.GitHub) != 1 || !slices.Equal(p.GitHub[0].Ignore, []string{"bot-login"}) ||
		!slices.Equal(p.GitHub[0].Members, []string{"acme:org:member"}) {
		t.Errorf("github table = %+v", p.GitHub)
	}

	if len(p.Slack) != 1 || p.Slack[0].Channels[0].Mode != policy.SlackModeExtend {
		t.Errorf("slack table = %+v, want the mode written out", p.Slack)
	}

	if len(p.People) != 1 || !slices.Equal(p.People[0].Addresses, []string{"ada@example.com", "ada@globex.example"}) {
		t.Errorf("people = %+v", p.People)
	}

	if !slices.IsSortedFunc(p.Groups, func(a, b compile.Group) int { return strings.Compare(a.Name, b.Name) }) {
		t.Error("groups are not sorted")
	}
}

func TestRosterWithoutAMatrixIsNil(t *testing.T) {
	if p := compile.Roster(compile.Model{}, exampleConventions()); p != nil {
		t.Fatalf("Roster = %+v, want nil without a matrix", p)
	}
}

func TestRosterZeroConventions(t *testing.T) {
	p := compile.Roster(exampleModel(), compile.Conventions{})
	groups := groupsByName(p)

	for name := range groups {
		if strings.Contains(name, ":k8s:") || strings.Contains(name, ":vault:") || strings.Contains(name, ":ssh:admin") && name != "dev:ssh:admin" {
			t.Errorf("group %s needs a convention the zero value does not state", name)
		}
	}

	// Without a directory predicate every address is the installation's own.
	if len(p.Deferred) != 0 {
		t.Errorf("deferred = %+v, want none", p.Deferred)
	}

	if got := groups["all:sluis:viewer"].Members; !slices.Equal(got, []string{"all@example.com", "all@globex.example"}) {
		t.Errorf("all:sluis:viewer = %v", got)
	}
}

func TestDuration(t *testing.T) {
	for in, want := range map[string]string{"PT8H": "8h", "PT12H": "12h"} {
		if got, err := compile.Duration(in); err != nil || got != want {
			t.Errorf("Duration(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	// A minute shape is refused with the day ones: the letter M is a month
	// as well, and an installation that never writes one is the test.
	for _, in := range []string{"8h", "P1D", "PT", "", "PT90M"} {
		if _, err := compile.Duration(in); err == nil {
			t.Errorf("Duration(%q) accepted", in)
		}
	}
}
