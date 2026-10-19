package sluispulumi_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// The telemetry contract. With Telemetry set, every Lambda function the library
// creates (the issuer's, each module's, the backup's and the restore's) carries
// the telemetry layer first and the configuration layer last, the OTEL_*
// environment with OTEL_SERVICE_NAME its own name, and its role may ask STS for
// a web identity token for the layer's audience. Without Telemetry, none of it.
// The functions are found, not listed: a function added to the library is held
// to the contract at once.

const telemetryAudience = "https://access.example.test"

func telemetryLayer() *arp.TelemetryArgs {
	return &arp.TelemetryArgs{
		LayerArn: pulumi.String("arn:aws:lambda:eu-west-1:111122223333:layer:otlp-lambda:7"),
		Env:      map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otlp.example.test"},
	}
}

// telemetryAudiencesOf is the audiences of the role's web identity statements.
func telemetryAudiencesOf(st []map[string]any) []string {
	var out []string
	for _, s := range webIdentityStatements(st) {
		cond, _ := s["Condition"].(map[string]any)
		all, _ := cond["ForAllValues:StringEquals"].(map[string]any)
		out = append(out, strs(all["sts:IdentityTokenAudience"])...)
	}
	return out
}

// holdsTelemetry checks one recorded stack: every function in it.
func holdsTelemetry(t *testing.T, rec *recorder, want bool) int {
	t.Helper()
	fns := rec.ofType(fnType)
	for _, f := range fns {
		// `staging-<role>-fn` has `staging-<role>-policy`; the issuer is `staging-issuer`.
		policy := strings.TrimSuffix(f.Name, "-fn")
		st := statements(t, prop(rec.one(t, policyType, policy+"-policy"), "policy").StringValue())
		layers := prop(f, "layers").ArrayValue()
		last := layers[len(layers)-1].StringValue()
		env := prop(f, "environment").ObjectValue()["variables"].ObjectValue()
		name := prop(f, "name").StringValue()
		auds := telemetryAudiencesOf(st)
		if !want {
			if len(layers) != 1 {
				t.Errorf("%s: %d layers without Telemetry", f.Name, len(layers))
			}
			for k := range env {
				if strings.HasPrefix(string(k), "OTEL_") {
					t.Errorf("%s: %s without Telemetry", f.Name, k)
				}
			}
			if slices.Contains(auds, telemetryAudience+"/otlp") {
				t.Errorf("%s: the telemetry audience is granted without Telemetry: %v", f.Name, auds)
			}
			continue
		}
		if len(layers) != 2 || !strings.Contains(layers[0].StringValue(), "otlp-lambda") {
			t.Errorf("%s: layers %v, want the telemetry layer first", f.Name, layers)
		}
		if strings.Contains(last, "otlp-lambda") || last == layers[0].StringValue() {
			t.Errorf("%s: the configuration layer is not last: %v", f.Name, layers)
		}
		if got := env["OTEL_SERVICE_NAME"].StringValue(); got != name {
			t.Errorf("%s: OTEL_SERVICE_NAME = %q, want the function's name %q", f.Name, got, name)
		}
		if env["OTEL_EXPORTER_OTLP_ENDPOINT"].StringValue() != "https://otlp.example.test" {
			t.Errorf("%s: no OTEL endpoint: %v", f.Name, env)
		}
		if env["SLUIS_CONFIG"].StringValue() != "/opt/sluis/sluis.yaml" {
			t.Errorf("%s: SLUIS_CONFIG = %v", f.Name, env["SLUIS_CONFIG"])
		}
		if !slices.Contains(auds, telemetryAudience+"/otlp") {
			t.Errorf("%s: the role may not ask STS for the telemetry audience: %v", f.Name, auds)
		}
	}
	return len(fns)
}

func TestEveryModuleFunctionCarriesTheTelemetry(t *testing.T) {
	rec, _ := mustLambda(t, estateWith(func(a *arp.LambdaArgs) {
		a.WebIdentityAudience = contractAudience
		a.Telemetry = telemetryLayer()
		a.AdditionalWebIdentityAudiences = []string{telemetryAudience + "/otlp"}
	}))
	// The issuer and the module functions: more than the issuer alone.
	if n := holdsTelemetry(t, rec, true); n < 5 {
		t.Fatalf("%d functions: a short list proves nothing", n)
	}
	// The console callers keep the console's audience first.
	for _, role := range arp.ConsoleCallers {
		auds := telemetryAudiencesOf(contractPolicy(t, rec, role))
		if len(auds) == 0 || auds[0] != contractAudience {
			t.Errorf("%s: audiences %v, want the console's first", role, auds)
		}
	}
}

func TestNoFunctionCarriesTelemetryWithoutIt(t *testing.T) {
	rec, _ := mustLambda(t, estateWith(func(a *arp.LambdaArgs) {
		a.WebIdentityAudience = contractAudience
		// Audiences are the issuer's; the module roles hold them with Telemetry only.
		a.AdditionalWebIdentityAudiences = nil
	}))
	if n := holdsTelemetry(t, rec, false); n < 5 {
		t.Fatalf("%d functions: a short list proves nothing", n)
	}
}

func TestModuleRolesHoldTheTelemetryAudiencesOnlyWithTelemetry(t *testing.T) {
	audiences := []string{telemetryAudience + "/otlp"}
	with, _ := mustLambda(t, estateWith(func(a *arp.LambdaArgs) {
		a.WebIdentityAudience = contractAudience
		a.Telemetry = telemetryLayer()
		a.AdditionalWebIdentityAudiences = audiences
	}))
	without, _ := mustLambda(t, estateWith(func(a *arp.LambdaArgs) {
		a.WebIdentityAudience = contractAudience
		a.AdditionalWebIdentityAudiences = audiences
	}))
	for _, role := range []string{"slack", "google", "cloudflare"} {
		if got := telemetryAudiencesOf(contractPolicy(t, with, role)); !slices.Contains(got, audiences[0]) {
			t.Errorf("%s with Telemetry: %v", role, got)
		}
		if got := telemetryAudiencesOf(contractPolicy(t, without, role)); slices.Contains(got, audiences[0]) {
			t.Errorf("%s without Telemetry holds the audience: %v", role, got)
		}
	}
}

func TestTheBackupAndRestoreFunctionsCarryTheTelemetry(t *testing.T) {
	set := func(c *arp.BackupCommon) {
		c.Telemetry = telemetryLayer()
		c.AdditionalWebIdentityAudiences = []string{telemetryAudience + "/otlp"}
	}
	backup, _ := mustBackup(t, func(a *arp.BackupArgs) { set(&a.BackupCommon) })
	restore, _ := mustRestore(t, func(a *arp.RestoreArgs) { set(&a.BackupCommon) })
	for _, rec := range []*recorder{backup, restore} {
		if n := holdsTelemetry(t, rec, true); n != 1 {
			t.Errorf("%d functions", n)
		}
	}
	backup, _ = mustBackup(t, nil)
	restore, _ = mustRestore(t, nil)
	for _, rec := range []*recorder{backup, restore} {
		holdsTelemetry(t, rec, false)
	}
}

func TestBackupTelemetryIsValidated(t *testing.T) {
	for name, mutate := range map[string]func(*arp.BackupCommon){
		"layer without arn": func(c *arp.BackupCommon) { c.Telemetry = &arp.TelemetryArgs{} },
		"env that is not OTEL": func(c *arp.BackupCommon) {
			c.Telemetry = telemetryLayer()
			c.Telemetry.Env = map[string]string{"AWS_REGION": "x"}
		},
		"audience without layer": func(c *arp.BackupCommon) { c.AdditionalWebIdentityAudiences = []string{"a"} },
		"empty audience": func(c *arp.BackupCommon) {
			c.Telemetry = telemetryLayer()
			c.AdditionalWebIdentityAudiences = []string{" "}
		},
		"duplicate audience": func(c *arp.BackupCommon) {
			c.Telemetry = telemetryLayer()
			c.AdditionalWebIdentityAudiences = []string{"a", "a"}
		},
	} {
		if _, _, err := buildBackup(t, func(a *arp.BackupArgs) { mutate(&a.BackupCommon) }); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
