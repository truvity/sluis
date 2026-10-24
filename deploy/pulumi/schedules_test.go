package sluispulumi_test

import (
	"slices"
	"strings"
	"testing"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

const schedType = "aws:scheduler/schedule:Schedule"

// scheduled returns the estate with targets for the controllers.
func scheduled(e estate) estate {
	e.orgs, e.workspaces = []string{"acme", "github:links"}, []string{"T1"}
	return e
}

// targetsOf is each schedule's target by its name: the input and the ARN.
func targetsOf(t *testing.T, rec *recorder) map[string]string {
	t.Helper()
	return schedules(t, rec)
}

// liveOf is the `live` alias ARN the mocks give a function.
func liveOf(fn string) string {
	return arnp + "lambda:eu-west-1:" + account + ":function:" + fn + ":live"
}

// With the module functions, each schedule targets the `live` alias of the
// function that runs it, and the scheduler's one policy holds those aliases and
// no others.
func TestSchedulesTargetTheirModulesLiveAlias(t *testing.T) {
	rec, out := mustLambda(t, scheduled(estateWith()))
	live := liveOf
	want := map[string]string{
		"sluis-github-acme":         `{"controller":"github","kind":"tick","target":"acme"} ` + live("sluis-staging-github"),
		"sluis-github-github-links": `{"controller":"github","kind":"tick","target":"github:links"} ` + live("sluis-staging-github"),
		"sluis-slack-T1":            `{"controller":"slack","kind":"tick","target":"T1"} ` + live("sluis-staging-slack"),
		"sluis-directory-refresh":   `{"kind":"refresh"} ` + live("sluis-staging-google"),
		"sluis-issuer-clients":      `{"kind":"clients"} ` + live("sluis-staging-issuer"),
	}
	if got := targetsOf(t, rec); fmtMap(got) != fmtMap(want) {
		t.Errorf("schedules:\n got %v\nwant %v", got, want)
	}
	for _, s := range rec.ofType(schedType) {
		if prop(s, "target").ObjectValue()["roleArn"].StringValue() != out["schedulerRoleArn"] {
			t.Errorf("%s: not the scheduler's one role", s.Name)
		}
		if prop(s, "name").StringValue() == "sluis-issuer-clients" && prop(s, "scheduleExpression").StringValue() != "rate(15 minutes)" {
			t.Errorf("clients rate: %v", s.Inputs)
		}
	}
	if n := len(rec.ofType("aws:iam/role:Role")); n != 6 { // the issuer's, the four module functions', the scheduler's
		t.Errorf("%d roles, want one scheduler role beside the functions'", n)
	}
	pol := grants(statements(t, prop(rec.one(t, policyType, "staging-scheduler-policy"), "policy").StringValue()))
	wantARNs := []string{live("sluis-staging-github"), live("sluis-staging-google"), live("sluis-staging-issuer"), live("sluis-staging-slack")}
	if len(pol) != 1 || !slices.Equal(pol["lambda:InvokeFunction"], wantARNs) {
		t.Errorf("scheduler grants %v, want exactly %v", pol, wantARNs)
	}
	for _, a := range pol["lambda:InvokeFunction"] {
		if !strings.HasSuffix(a, ":live") {
			t.Errorf("scheduler grant %s is not a live alias", a)
		}
	}
}

// An Outline workspace ticks the Outline function's `live` alias at its own
// rate, and the scheduler's policy holds that alias.
func TestOutlineSchedulesTargetTheOutlineFunction(t *testing.T) {
	in := exampleInstallation(t)
	withOutline(in)
	e := withInstallation(in, nil)
	e.outline = []string{"wiki"}
	rec, _ := mustLambda(t, e)
	got := targetsOf(t, rec)
	if want := `{"controller":"outline","kind":"tick","target":"wiki"} ` + liveOf("sluis-example-outline"); got["sluis-outline-wiki"] != want {
		t.Errorf("the Outline schedule = %q, want %q (all: %v)", got["sluis-outline-wiki"], want, got)
	}
	for _, s := range rec.ofType(schedType) {
		if prop(s, "name").StringValue() == "sluis-outline-wiki" && prop(s, "scheduleExpression").StringValue() != arp.DefaultOutlineSchedule {
			t.Errorf("the Outline rate: %v", s.Inputs)
		}
	}
	pol := grants(statements(t, prop(rec.one(t, policyType, "staging-scheduler-policy"), "policy").StringValue()))["lambda:InvokeFunction"]
	if !slices.Contains(pol, liveOf("sluis-example-outline")) {
		t.Errorf("the scheduler may not invoke the Outline alias: %v", pol)
	}

	// A rate of its own.
	e.mutate = withInstallation(in, func(a *arp.LambdaArgs) { a.Schedule.OutlineRate = "rate(7 minutes)" }).mutate
	rec, _ = mustLambda(t, e)
	if s := rec.one(t, schedType, "staging-outline-wiki"); prop(s, "scheduleExpression").StringValue() != "rate(7 minutes)" {
		t.Errorf("the Outline rate: %v", s.Inputs)
	}
}

// An Outline workspace with no Outline function to tick is refused: the
// installation declares no `controllers.outline`, or there is no installation.
func TestOutlineWorkspacesNeedTheOutlineFunction(t *testing.T) {
	e := withInstallation(exampleInstallation(t), nil)
	e.outline = []string{"wiki"}
	for name, e := range map[string]estate{"installation": e, "documents": {outline: []string{"wiki"}}} {
		if _, _, err := buildLambda(t, e); err == nil || !strings.Contains(err.Error(), "Schedule.OutlineWorkspaces") ||
			!strings.Contains(err.Error(), "controllers.outline") {
			t.Errorf("%s: error %v, want the Outline workspaces refused", name, err)
		}
	}
}

func fmtMap(m map[string]string) string {
	var ks []string
	for k, v := range m {
		ks = append(ks, k+"="+v)
	}
	slices.Sort(ks)
	return strings.Join(ks, "\n")
}

// The rotation targets the Cloudflare function's alias.
func TestTheCloudflareRotationTargetsTheCloudflareFunction(t *testing.T) {
	in := exampleInstallation(t)
	withCloudflare(in)
	e := withInstallation(in, nil)
	rec, _ := mustLambda(t, e)
	var found bool
	for _, s := range rec.ofType(schedType) {
		if prop(s, "name").StringValue() != "sluis-cloudflare-rotation" {
			continue
		}
		found = true
		tgt := prop(s, "target").ObjectValue()
		if got := tgt["arn"].StringValue(); got != liveOf("sluis-example-cloudflare") || tgt["input"].StringValue() != `{"kind":"cloudflare"}` {
			t.Errorf("rotation target %v", tgt)
		}
	}
	if !found {
		t.Fatal("no rotation schedule")
	}
	if !slices.Contains(grants(statements(t, prop(rec.one(t, policyType, "staging-scheduler-policy"), "policy").StringValue()))["lambda:InvokeFunction"],
		liveOf("sluis-example-cloudflare")) {
		t.Error("the scheduler may not invoke the cloudflare alias")
	}
}

func TestIssuerClientsScheduleIsConfigurable(t *testing.T) {
	rec, _ := mustLambda(t, scheduled(estateWith(func(a *arp.LambdaArgs) { a.IssuerClients = arp.IssuerClientsArgs{Rate: "rate(1 hour)", Paused: true} })))
	s := rec.one(t, schedType, "staging-issuer-clients")
	if prop(s, "scheduleExpression").StringValue() != "rate(1 hour)" || prop(s, "state").StringValue() != "DISABLED" {
		t.Errorf("clients schedule %v", s.Inputs)
	}

	rec, _ = mustLambda(t, scheduled(estateWith(func(a *arp.LambdaArgs) { a.IssuerClients.Disabled = true })))
	if rec.has(schedType, "staging-issuer-clients") {
		t.Error("Disabled left the clients schedule in")
	}
	policy := prop(rec.one(t, policyType, "staging-scheduler-policy"), "policy").StringValue()
	if got := grants(statements(t, policy))["lambda:InvokeFunction"]; slices.Contains(got, liveOf("sluis-staging-issuer")) {
		t.Errorf("the scheduler still holds the issuer's alias: %v", got)
	}

	// No schedule at all: no grant to hold.
	rec, _ = mustLambda(t, estateWith(func(a *arp.LambdaArgs) { a.IssuerClients.Disabled, a.DirectoryRefresh.Disabled = true, true }))
	if rec.has(policyType, "staging-scheduler-policy") {
		t.Error("a scheduler with nothing to invoke has a policy")
	}

	for name, f := range map[string]func(*arp.LambdaArgs){
		"both":       func(a *arp.LambdaArgs) { a.IssuerClients.Disabled, a.IssuerClients.Paused = true, true },
		"not a rate": func(a *arp.LambdaArgs) { a.IssuerClients.Rate = "hourly" },
	} {
		if _, _, err := buildLambda(t, estateWith(f)); err == nil || !strings.Contains(err.Error(), "IssuerClients") {
			t.Errorf("%s: error %v", name, err)
		}
	}
}

// The scheduler's role is assumed by EventBridge Scheduler for the schedules of this
// account in the schedule group the installation's are in. EventBridge Scheduler
// passes the schedule GROUP's ARN as aws:SourceArn, not the schedule's (AWS,
// "Confused deputy prevention in EventBridge Scheduler": the value must be the
// schedule group ARN, not a schedule or a name prefix); a condition on a schedule ARN
// is never true and no schedule could assume the role.
func TestTheSchedulersTrustNamesTheScheduleGroup(t *testing.T) {
	check := func(t *testing.T, trust string) {
		t.Helper()
		sts := statements(t, trust)
		if len(sts) != 1 {
			t.Fatalf("trust: %s", trust)
		}
		cond := sts[0]["Condition"].(map[string]any)
		if eq := cond["StringEquals"].(map[string]any); eq["aws:SourceAccount"] != account {
			t.Errorf("source account: %v", cond)
		}
		want := arnp + "scheduler:" + region + ":" + account + ":schedule-group/default"
		if got := cond["ArnEquals"].(map[string]any)["aws:SourceArn"]; got != want {
			t.Errorf("source ARN = %v, want the group %s", got, want)
		}
		if cond["ArnLike"] != nil || strings.Contains(trust, ":schedule/") {
			t.Errorf("the trust names a schedule, which EventBridge Scheduler never presents: %s", trust)
		}
	}
	rec, _ := mustLambda(t, scheduled(estateWith()))
	check(t, prop(rec.one(t, "aws:iam/role:Role", "staging-scheduler-role"), "assumeRolePolicy").StringValue())
	// The schedules are in the group the trust names: none sets a group, so `default`.
	for _, s := range rec.ofType(schedType) {
		if g := prop(s, "groupName"); g.HasValue() && g.StringValue() != "default" {
			t.Errorf("schedule %s is in the group %v", s.Name, g)
		}
	}
	brec, _ := mustBackup(t, nil)
	check(t, prop(brec.one(t, "aws:iam/role:Role", "staging-backup-scheduler-role"), "assumeRolePolicy").StringValue())
	for _, s := range brec.ofType(schedType) {
		if g := prop(s, "groupName"); g.HasValue() && g.StringValue() != "default" {
			t.Errorf("backup schedule %s is in the group %v", s.Name, g)
		}
	}
}
