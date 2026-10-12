package sluispulumi_test

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// sluisFunctions are the functions NewLambda makes: the issuer's and the modules'.
func sluisFunctions(rec *recorder) []declared {
	var out []declared
	for _, d := range rec.ofType(fnType) {
		if strings.HasPrefix(prop(d, "name").StringValue(), "sluis-") {
			out = append(out, d)
		}
	}
	return out
}

// With audit installed by the library, the audit writer is updated before the
// functions: each of them depends on it, and nothing else of the component does.
func TestInstalledAuditWriterGoesBeforeTheFunctions(t *testing.T) {
	rec, _ := mustLambda(t, v5Audit(t, installAudit(t)))
	fns := sluisFunctions(rec)
	if len(fns) != 4 { // the issuer and the GitHub, Google and Slack functions of the installation
		t.Fatalf("%d sluis functions: %v", len(fns), rec.names())
	}
	for _, f := range fns {
		if n := rec.optionsOf(fnType, f.Name).DependsOnInputs; n != 1 {
			t.Errorf("%s waits for %d writer dependencies, want 1", f.Name, n)
		}
	}
	for _, d := range rec.allDeclared() {
		if d.Type != fnType && rec.optionsOf(d.Type, d.Name).DependsOnInputs != 0 {
			t.Errorf("%s %s waits for the writer: only the functions do", d.Type, d.Name)
		}
	}
	if w := rec.one(t, fnType, "audit-example-writer"); rec.optionsOf(fnType, w.Name).DependsOnInputs != 0 {
		t.Error("the writer waits for itself")
	}
}

// An installation that exists in the same stack: the estate hands the library the
// writer, and every function waits for it. Without it the functions do not wait.
func TestAuditUseTakesTheWriterAsADependency(t *testing.T) {
	order := func(dependsOn bool) (writerAt, issuerAt int, rec *recorder) {
		args := v5Audit(t, &arp.AuditArgs{Use: &arp.AuditUse{QueueURL: queueURL}}).args(t)
		rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
			// A writer whose declaration is held back until the Lambda has been declared.
			gate, resolve, _ := ctx.NewOutput()
			writer, err := lambda.NewFunction(ctx, "their-writer", &lambda.FunctionArgs{
				Name: gate.ApplyT(func(any) string { return "their-writer" }).(pulumi.StringOutput), Role: pulumi.String(roleArnOf("their-writer")),
			})
			if err != nil {
				return err
			}
			if dependsOn {
				args.Audit.Use.DependsOn = pulumi.ResourceArray{pulumi.NewResourceInput(writer)}
			}
			if _, err := arp.NewLambda(ctx, "staging", args); err != nil {
				return err
			}
			resolve("go")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		writerAt = indexOf(t, rec, fnType, func(d declared) bool { return d.Name == "their-writer" })
		issuerAt = indexOf(t, rec, fnType, func(d declared) bool { return d.Name == "staging-issuer" })
		return writerAt, issuerAt, rec
	}
	w, i, rec := order(true)
	if w > i {
		t.Error("the issuer's function is declared before the audit writer it was told to follow")
	}
	for _, f := range sluisFunctions(rec) {
		if wi := indexOf(t, rec, fnType, func(d declared) bool { return d.Name == f.Name }); w > wi {
			t.Errorf("%s is declared before the audit writer", f.Name)
		}
		if rec.optionsOf(fnType, f.Name).DependsOnInputs != 1 {
			t.Errorf("%s: no dependency on the writer", f.Name)
		}
	}
	// The control: with no DependsOn the functions do not wait for the held-back writer.
	w, i, rec = order(false)
	if w < i {
		t.Error("the control does not discriminate: the writer was declared first without a dependency")
	}
	for _, f := range sluisFunctions(rec) {
		if rec.optionsOf(fnType, f.Name).DependsOnInputs != 0 {
			t.Errorf("%s waits without being told to", f.Name)
		}
	}
}
