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
	rec, out := mustLambda(t, scheduled(v5Estate()))
	live := liveOf
	want := map[string]string{
		"sluis-github-acme":         `{"controller":"github","kind":"tick","target":"acme"} ` + live("sluis-staging-github"),
		"sluis-github-github-links": `{"controller":"github","kind":"tick","target":"github:links"} ` + live("sluis-staging-github"),
		"sluis-slack-T1":            `{"controller":"slack","kind":"tick","target":"T1"} ` + live("sluis-staging-slack"),
		"sluis-directory-refresh":   `{"kind":"refresh"} ` + live("sluis-staging-google"),
		"sluis-issuer-clients":      `{"kind":"clients"} ` + live("sluis"),
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
	wantARNs := []string{live("sluis-staging-github"), live("sluis-staging-google"), live("sluis-staging-slack"), live("sluis")}
	if len(pol) != 1 || !slices.Equal(pol["lambda:InvokeFunction"], wantARNs) {
		t.Errorf("scheduler grants %v, want exactly %v", pol, wantARNs)
	}
	for _, a := range pol["lambda:InvokeFunction"] {
		if !strings.HasSuffix(a, ":live") {
			t.Errorf("scheduler grant %s is not a live alias", a)
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
	in.AWS.Table = ""
	e := withInstallation(in, func(a *arp.LambdaArgs) { withLayout(arp.LayoutV5).mutate(a) })
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

// A preview from the one-function layout to the per-module one retargets the
// schedules in place (same logical and physical names) and creates only the
// clients schedule; the scheduler's role and policy are not touched.
func TestThePreviewShapeOfTheSchedules(t *testing.T) {
	before, _ := mustLambda(t, scheduled(withLayout(arp.LayoutV4)))
	after, _ := mustLambda(t, scheduled(v5Estate()))
	s := previewShape(before, after, schedType)
	if len(s.replaced) != 0 || len(s.deleted) != 0 || !slices.Equal(s.created, []string{"staging-issuer-clients"}) {
		t.Errorf("schedules: %s %v", s, s.created)
	}
	// Four are in both with the same name; their target is what changes.
	moved := 0
	b, a := targetsOf(t, before), targetsOf(t, after)
	for name, tgt := range b {
		if a[name] == "" {
			t.Errorf("%s is gone", name)
		}
		if a[name] != tgt {
			moved++
		}
	}
	if moved != 4 {
		t.Errorf("%d schedules retargeted, want 4 (github x2, slack, google)", moved)
	}
	for _, typ := range []string{"aws:iam/role:Role", policyType} {
		if s := previewShape(before, after, typ); slices.ContainsFunc(append(s.deleted, s.replaced...),
			func(n string) bool { return strings.Contains(n, "scheduler") }) {
			t.Errorf("%s: %s touches the scheduler", typ, s)
		}
	}
	// The v4 layout has no clients schedule and its role holds the one alias.
	pol := grants(statements(t, prop(before.one(t, policyType, "staging-scheduler-policy"), "policy").StringValue()))
	if got := pol["lambda:InvokeFunction"]; len(got) != 1 || got[0] != liveOf("sluis") {
		t.Errorf("v4 scheduler grants %v", got)
	}
}

func TestIssuerClientsScheduleIsConfigurable(t *testing.T) {
	rec, _ := mustLambda(t, scheduled(v5Estate(func(a *arp.LambdaArgs) { a.IssuerClients = arp.IssuerClientsArgs{Rate: "rate(1 hour)", Paused: true} })))
	s := rec.one(t, schedType, "staging-issuer-clients")
	if prop(s, "scheduleExpression").StringValue() != "rate(1 hour)" || prop(s, "state").StringValue() != "DISABLED" {
		t.Errorf("clients schedule %v", s.Inputs)
	}

	rec, _ = mustLambda(t, scheduled(v5Estate(func(a *arp.LambdaArgs) { a.IssuerClients.Disabled = true })))
	if rec.has(schedType, "staging-issuer-clients") {
		t.Error("Disabled left the clients schedule in")
	}
	policy := prop(rec.one(t, policyType, "staging-scheduler-policy"), "policy").StringValue()
	if got := grants(statements(t, policy))["lambda:InvokeFunction"]; slices.Contains(got, liveOf("sluis")) {
		t.Errorf("the scheduler still holds the issuer's alias: %v", got)
	}

	// No schedule at all: no grant to hold.
	rec, _ = mustLambda(t, v5Estate(func(a *arp.LambdaArgs) { a.IssuerClients.Disabled, a.DirectoryRefresh.Disabled = true, true }))
	if rec.has(policyType, "staging-scheduler-policy") {
		t.Error("a scheduler with nothing to invoke has a policy")
	}

	for name, f := range map[string]func(*arp.LambdaArgs){
		"both":       func(a *arp.LambdaArgs) { a.IssuerClients.Disabled, a.IssuerClients.Paused = true, true },
		"not a rate": func(a *arp.LambdaArgs) { a.IssuerClients.Rate = "hourly" },
	} {
		if _, _, err := buildLambda(t, v5Estate(f)); err == nil || !strings.Contains(err.Error(), "IssuerClients") {
			t.Errorf("%s: error %v", name, err)
		}
	}
	// The v4 layout never makes it.
	rec, _ = mustLambda(t, withLayout(arp.LayoutV4))
	if rec.has(schedType, "staging-issuer-clients") {
		t.Error("v4 made the clients schedule")
	}
}
