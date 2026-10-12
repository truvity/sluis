package sluispulumi_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sqs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluisconfig "github.com/truvity/sluis/config"
	arp "github.com/truvity/sluis/deploy/pulumi"
)

// v5Install is the example installation on layout v5: it names no table of the
// v4 layout and no queue.
func v5Install(t *testing.T) *sluisconfig.Installation {
	t.Helper()
	in := exampleInstallation(t)
	in.AWS.AuditQueueURL, in.AWS.Table = "", ""
	return in
}

func v5Audit(t *testing.T, au *arp.AuditArgs) estate {
	t.Helper()
	return withInstallation(v5Install(t), func(a *arp.LambdaArgs) {
		a.AuditQueueArn = nil
		a.Audit = au
		withLayout(arp.LayoutV5).mutate(a)
	})
}

func roleArnOf(name string) string { return arnp + "iam::" + account + ":role/" + name }

// indexOf is the position of the declared resource, in the order the engine was
// told of them: a resource is declared once everything it depends on is.
func indexOf(t *testing.T, rec *recorder, typ string, match func(declared) bool) int {
	t.Helper()
	for i, d := range rec.allDeclared() {
		if d.Type == typ && match(d) {
			return i
		}
	}
	t.Fatalf("no %s matching; have %v", typ, rec.names())
	return -1
}

// The senders a queue policy may name are the roles the stack creates, and the
// old names that exist already: not a restore function the stack does not make.
func TestCreatedSendersAreTheRolesTheStackCreates(t *testing.T) {
	base := arp.RoleArgs{Region: region, AccountID: account, Instance: "staging", Layout: arp.LayoutV5}
	names := func(r arp.RoleArgs) []string {
		got, err := r.CreatedSenders()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, arn := range got {
			out = append(out, strings.TrimPrefix(arn, roleArnOf("sluis-staging-")))
		}
		return out
	}
	if got, want := names(base), []string{"cloudflare", "github", "google", "issuer", "slack"}; !slices.Equal(got, want) {
		t.Errorf("created senders %v, want %v", got, want)
	}
	// Every role is a principal of the key, created or not.
	all, err := base.Senders()
	if err != nil || len(all) != 7 {
		t.Errorf("senders %v (%v), want the seven roles", all, err)
	}
	bk := base
	bk.Backup = true
	if got := names(bk); !slices.Contains(got, "backup") || slices.Contains(got, "restore") {
		t.Errorf("with the backup declared: %v", got)
	}
	bk.Restore = true
	if got := names(bk); !slices.Contains(got, "restore") {
		t.Errorf("with the restore declared: %v", got)
	}
	sw := base
	sw.TransitionRoles = []arp.TransitionRole{{Role: arp.RoleIssuer, OldName: "sluis-old"}}
	got, _ := sw.CreatedSenders()
	if !slices.Contains(got, roleArnOf("sluis-old")) || !slices.Contains(got, roleArnOf("sluis-staging-issuer")) {
		t.Errorf("a transition keeps both names: %v", got)
	}
	// v4 has the issuer's function and nothing else.
	v4 := arp.RoleArgs{Region: region, AccountID: account, Instance: "staging"}
	if got, _ := v4.CreatedSenders(); !slices.Equal(got, []string{roleArnOf("sluis-staging-issuer")}) {
		t.Errorf("layout v4: %v", got)
	}
	// A module the installation renders no document for has no function, so no role.
	in := v5Install(t)
	r := arp.RoleArgs{Region: in.AWS.Region, AccountID: account, Instance: in.Instance, Layout: arp.LayoutV5, Installation: in}
	roles, err := r.FunctionRoles()
	if err != nil {
		t.Fatal(err)
	}
	for _, fr := range roles {
		if fr.Role == arp.RoleCloudflare && fr.Created {
			t.Error("the installation has no Cloudflare preset and its function is Created")
		}
		if fr.Role == arp.RoleGitHub && !fr.Created {
			t.Error("the GitHub function is not Created")
		}
	}
}

// The queue policy the library writes when it installs audit is applied after
// the module functions' roles: SQS refuses a policy that names a role that does
// not exist yet.
func TestTheLibrarysQueuePolicyWaitsForTheModuleRoles(t *testing.T) {
	for i := 0; i < 3; i++ {
		rec, _ := mustLambda(t, v5Audit(t, installAudit(t)))
		policyAt := indexOf(t, rec, "aws:sqs/queuePolicy:QueuePolicy", func(d declared) bool { return d.Name == "audit-example-ingest" })
		for _, m := range []string{"github", "google", "slack"} {
			roleAt := indexOf(t, rec, "aws:iam/role:Role", func(d declared) bool { return prop(d, "name").StringValue() == "sluis-example-"+m })
			if roleAt > policyAt {
				t.Errorf("the queue policy is declared before the %s role", m)
			}
		}
		qp := prop(rec.one(t, "aws:sqs/queuePolicy:QueuePolicy", "audit-example-ingest"), "policy").StringValue()
		if !strings.Contains(qp, roleArnOf("sluis-example-github")) {
			t.Errorf("the policy does not name the GitHub role:\n%s", qp)
		}
	}
}

// A stack that makes the queue before the roles takes the senders as outputs,
// and its queue policy is applied after the roles it names, the restore
// function (not created) left out.
func TestDeferredSendersOrderTheQueuePolicyAfterTheRoles(t *testing.T) {
	in := v5Install(t)
	args := v5Audit(t, &arp.AuditArgs{Use: &arp.AuditUse{QueueURL: queueURL}}).args(t)
	roles := arp.RoleArgs{Region: in.AWS.Region, AccountID: account, Instance: in.Instance, Layout: arp.LayoutV5, Installation: in,
		FunctionName: args.FunctionName}
	rec, _, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		s, err := roles.DeferredSenders(ctx)
		if err != nil {
			return err
		}
		defer s.Reject(nil)
		policy := pulumi.StringArray(s.Inputs).ToStringArrayOutput().ApplyT(func(arns []string) string {
			slices.Sort(arns)
			return strings.Join(arns, ",")
		}).(pulumi.StringOutput)
		if _, err := sqs.NewQueuePolicy(ctx, "foundation-queue-policy", &sqs.QueuePolicyArgs{QueueUrl: pulumi.String(queueURL), Policy: policy}); err != nil {
			return err
		}
		l, err := arp.NewLambda(ctx, "staging", args)
		if err != nil {
			return err
		}
		s.Lambda(l)
		collect("policy", policy)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	policyAt := indexOf(t, rec, "aws:sqs/queuePolicy:QueuePolicy", func(d declared) bool { return d.Name == "foundation-queue-policy" })
	for _, m := range []string{"github", "google", "slack"} {
		roleAt := indexOf(t, rec, "aws:iam/role:Role", func(d declared) bool { return prop(d, "name").StringValue() == "sluis-example-"+m })
		if roleAt > policyAt {
			t.Errorf("the queue policy is declared before the %s role", m)
		}
	}
	doc := prop(rec.one(t, "aws:sqs/queuePolicy:QueuePolicy", "foundation-queue-policy"), "policy").StringValue()
	if !strings.Contains(doc, roleArnOf("sluis-example-github")) || !strings.Contains(doc, roleArnOf(args.FunctionName)) {
		t.Errorf("senders %s", doc)
	}
	if strings.Contains(doc, "restore") || strings.Contains(doc, "backup") {
		t.Errorf("a role the stack does not create is named: %s", doc)
	}
}
