package sluispulumi_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

const (
	region     = "eu-central-1"
	truststore = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
)

// zipFile writes a release zip: `bootstrap` at its root.
func zipFile(t *testing.T, extra map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{"bootstrap": "#!binary\n"}
	for k, v := range extra {
		files[k] = v
	}
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "sluis-lambda_1.63.0_linux_arm64.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// sha is a file's SHA-256, as the release's checksums give it.
func sha(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

const minimalPolicy = "apiVersion: sluis.truvity.github.io/policy/v2\ngroups:\n  all:access-roster:operator: {}\n"

type estate struct {
	pkg                 string
	config              string
	policy              string
	orgs, workspaces    []string
	rate                string
	telemetry           bool
	keepDefaultEndpoint bool
	mutate              func(*arp.LambdaArgs)
}

func (e estate) args(t *testing.T) *arp.LambdaArgs {
	t.Helper()
	if e.pkg == "" {
		e.pkg = zipFile(t, nil)
	}
	if e.config == "" {
		e.config = "issuerURL: https://access.example.test\ncontrollers:\n  github: {consoleURL: \"https://access.example.test/console\"}\n" +
			"  slack: {consoleURL: \"https://access.example.test/console\"}\n"
	}
	if e.policy == "" {
		e.policy = minimalPolicy
	}
	pkgSHA := ""
	if _, err := os.Stat(e.pkg); err == nil {
		pkgSHA = sha(t, e.pkg)
	}
	a := &arp.LambdaArgs{
		Region: region, AccountID: account, Instance: "staging",
		Package: e.pkg, PackageSHA256: pkgSHA, Config: e.config, Policy: e.policy,
		Storage:       &arp.StorageGrant{BucketArn: pulumi.String(arnp + "s3:::" + bucket)},
		State:         &arp.StateGrant{TableArn: pulumi.String(arnp + "dynamodb:" + region + ":" + account + ":table/" + table)},
		AuditQueueArn: pulumi.String(arnp + "sqs:" + region + ":" + account + ":audit-ingest"),
		API: arp.APIArgs{
			DomainName:           "access.example.test",
			CertificateArn:       pulumi.String(arnp + "acm:" + region + ":" + account + ":certificate/origin"),
			TruststorePEM:        truststore,
			TruststoreBucketName: "acme-sluis-truststore",
			KeepDefaultEndpoint:  e.keepDefaultEndpoint,
		},
		Schedule: arp.ScheduleArgs{GitHubOrgs: e.orgs, SlackWorkspaces: e.workspaces, Rate: e.rate},
	}
	if e.telemetry {
		a.Telemetry = &arp.TelemetryArgs{
			LayerArn: pulumi.String(arnp + "lambda:" + region + ":" + account + ":layer:otlp-lambda:1"),
			Env:      map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otlp.example.test"},
		}
	}
	if e.mutate != nil {
		e.mutate(a)
	}
	return a
}

func buildLambda(t *testing.T, e estate) (*recorder, map[string]string, error) {
	t.Helper()
	args := e.args(t)
	return run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		l, err := arp.NewLambda(ctx, "staging", args)
		if err != nil {
			return err
		}
		collect("signingKeyArn", l.SigningKeyArn)
		collect("signingKeyAlias", l.SigningKeyAlias)
		collect("signingKeyRS256Arn", l.SigningKeyRS256Arn)
		collect("signingKeyRS256Alias", l.SigningKeyRS256Alias)
		collect("signingKeyRS256ID", l.SigningKeyRS256ID)
		collect("wrappedSigningKeyArn", l.WrappedSigningKeyArn)
		collect("wrappedSigningKeyAlias", l.WrappedSigningKeyAlias)
		collect("functionArn", l.FunctionArn)
		collect("functionName", l.FunctionName)
		collect("liveAliasArn", l.LiveAliasArn)
		collect("liveVersion", l.LiveVersion)
		collect("codeMatches", l.CodeSha256Matches.ApplyT(strconv.FormatBool).(pulumi.StringOutput))
		collect("roleArn", l.RoleArn)
		collect("apiUrl", l.APIURL)
		collect("accessLogGroupName", l.AccessLogGroupName)
		collect("domainTarget", l.DomainTarget)
		collect("domainHostedZoneID", l.DomainHostedZoneID)
		collect("truststoreUri", l.TruststoreURI)
		collect("schedulerRoleArn", l.SchedulerRoleArn)
		collect("stateSecretParameter", l.StateSecretParameter)
		collect("recoveryPasswordParameter", l.RecoveryPasswordParameter)
		collect("configLayerArn", l.ConfigLayerArn)
		collect("declaredParameters", l.DeclaredParameters.ApplyT(func(v []string) string { return strings.Join(v, ",") }).(pulumi.StringOutput))
		collect("auditQueueUrl", l.AuditQueueURL)
		collect("auditQueueArn", l.AuditQueueArn)
		if l.Audit != nil {
			collect("auditPresets", l.Audit.Presets.ApplyT(func(p []string) string { return strings.Join(p, ",") }).(pulumi.StringOutput))
		}
		return nil
	})
}

func mustLambda(t *testing.T, e estate) (*recorder, map[string]string) {
	t.Helper()
	rec, out, err := buildLambda(t, e)
	if err != nil {
		t.Fatal(err)
	}
	return rec, out
}

const (
	fnType     = "aws:lambda/function:Function"
	policyType = "aws:iam/rolePolicy:RolePolicy"
)

// rolePolicy is the grants of the function's one role.
func rolePolicy(t *testing.T, rec *recorder) map[string][]string {
	t.Helper()
	return grants(statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()))
}

// packagePath is the file a function's code is: the release zip, as it is.
func packagePath(t *testing.T, f declared) string {
	t.Helper()
	code := prop(f, "code")
	if !code.IsArchive() || !code.ArchiveValue().IsPath() {
		t.Fatalf("%s: code is not the zip itself: %v", f.Name, code)
	}
	return code.ArchiveValue().Path
}

const layerType = "aws:lambda/layerVersion:LayerVersion"

// layerFiles is what the configuration layer holds, by path.
func layerFiles(t *testing.T, rec *recorder) map[string]string {
	t.Helper()
	return archiveFiles(t, rec.one(t, layerType, "staging-config"))
}

// archiveFiles is what an archive of assets holds, by path.
func archiveFiles(t *testing.T, f declared) map[string]string {
	t.Helper()
	code := prop(f, "code")
	if !code.IsArchive() {
		t.Fatalf("%s: code is not an archive: %v", f.Name, code)
	}
	assets, ok := code.ArchiveValue().GetAssets()
	if !ok {
		t.Fatalf("%s: the archive is not a map of assets", f.Name)
	}
	out := map[string]string{}
	for path, a := range assets {
		asset, ok := a.(*resource.Asset)
		if !ok {
			t.Fatalf("%s: %s is not an asset", f.Name, path)
		}
		switch {
		case asset.IsText():
			out[path] = asset.Text
		case asset.IsPath():
			raw, err := os.ReadFile(asset.Path)
			if err != nil {
				t.Fatal(err)
			}
			out[path] = string(raw)
		default:
			t.Fatalf("%s: %s is neither text nor a file", f.Name, path)
		}
	}
	return out
}

// shape holds what is true of every estate: the Truvity one and the example one
// differ in their targets, their domain and their telemetry, and in nothing here.
func shape(t *testing.T, rec *recorder, out map[string]string, domain string) {
	t.Helper()

	// ONE function from the package, arm64, provided.al2023, no VPC.
	if n := len(rec.ofType(fnType)); n != 1 {
		t.Fatalf("%d functions, want 1", n)
	}
	f := rec.one(t, fnType, "staging-http")
	if prop(f, "name").StringValue() != "sluis" || prop(f, "runtime").StringValue() != "provided.al2023" ||
		prop(f, "handler").StringValue() != "bootstrap" {
		t.Errorf("%v", f.Inputs)
	}
	if prop(f, "memorySize").NumberValue() != 512 || prop(f, "timeout").NumberValue() != 300 {
		t.Errorf("memory %v timeout %v: one setting, with room for a controller's pass", prop(f, "memorySize"), prop(f, "timeout"))
	}
	if a := prop(f, "architectures").ArrayValue(); len(a) != 1 || a[0].StringValue() != "arm64" {
		t.Errorf("architectures: %v", a)
	}
	if prop(f, "vpcConfig").HasValue() {
		t.Error("the function is in a VPC")
	}
	env := prop(f, "environment").ObjectValue()["variables"].ObjectValue()
	if env["SLUIS_CONFIG"].StringValue() != "/opt/sluis/sluis.yaml" {
		t.Errorf("env: %v", env)
	}
	for k := range env {
		if k != "SLUIS_CONFIG" && !strings.HasPrefix(string(k), "OTEL_") {
			t.Errorf("%s in the environment", k)
		}
	}
	// The configuration layer is mounted last.
	if l := prop(f, "layers").ArrayValue(); len(l) == 0 || l[len(l)-1].StringValue() != out["configLayerArn"] {
		t.Errorf("layers: %v, want the configuration layer last", l)
	}
	packagePath(t, f)
	// Nothing of the three functions of v1.62 is left.
	for _, n := range []string{"staging-github", "staging-slack"} {
		if rec.has(fnType, n) || rec.has("aws:iam/role:Role", n+"-role") || rec.has(policyType, n+"-policy") {
			t.Errorf("%s is still declared", n)
		}
	}
	files := layerFiles(t, rec)
	if got := keysOf(files); !reflect.DeepEqual(got, []string{"sluis/policy.yaml", "sluis/sluis.yaml"}) {
		t.Errorf("the layer holds %v", got)
	}
	layer := rec.one(t, layerType, "staging-config")
	if !prop(layer, "skipDestroy").BoolValue() || prop(layer, "layerName").StringValue() != "sluis-config" {
		t.Errorf("layer: %v", layer.Inputs)
	}

	// ONE role, with the union of what the three had.
	if n := len(rec.ofType("aws:iam/role:Role")); n != 2 { // the function's and the scheduler's
		t.Errorf("%d roles, want the function's and the scheduler's", n)
	}
	role := rec.one(t, "aws:iam/role:Role", "staging-http-role")
	if prop(role, "name").StringValue() != "sluis" {
		t.Errorf("role: %v", role.Inputs)
	}
	g := rolePolicy(t, rec)
	for _, a := range []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket", "dynamodb:GetItem", "dynamodb:PutItem",
		"dynamodb:Query", "ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssm:PutParameter", "ssm:DeleteParameter",
		"sqs:SendMessage", "logs:CreateLogStream", "logs:PutLogEvents", "kms:Sign", "kms:GetPublicKey", "lambda:InvokeFunction",
		"sts:GetWebIdentityToken"} {
		if len(g[a]) == 0 {
			t.Errorf("the role lacks %s", a)
		}
	}
	if got := g["sqs:SendMessage"]; !reflect.DeepEqual(got, []string{arnp + "sqs:" + region + ":" + account + ":audit-ingest"}) {
		t.Errorf("sqs: %v", got)
	}
	ssmArn := arnp + "ssm:" + region + ":" + account + ":parameter"
	// It reads its credentials and config, and the external documents.
	wantRead := v4Reads(ssmArn)
	if got := g["ssm:GetParametersByPath"]; !reflect.DeepEqual(sortedCopy(got), sortedCopy(wantRead)) {
		t.Errorf("reads %v", got)
	}
	for _, res := range g["ssm:PutParameter"] {
		if !strings.Contains(res, "/sluis/staging/internal/credentials") && !strings.Contains(res, "/sluis/staging/external") {
			t.Errorf("may put %s", res)
		}
	}
	if got := g["lambda:InvokeFunction"]; !reflect.DeepEqual(got, []string{arnp + "lambda:" + region + ":" + account + ":function:sluis:live"}) {
		t.Errorf("may invoke %v: itself only", got)
	}
	wantKeys := []string{out["signingKeyArn"], out["signingKeyRS256Arn"]}
	for _, a := range []string{"kms:Sign", "kms:GetPublicKey"} {
		if got := g[a]; !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("%s on %v, want %v", a, got, wantKeys)
		}
	}
	// The one role signs, so nothing denies it the key ring.
	for _, s := range statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()) {
		if s["Effect"] == "Deny" && s["Sid"] != "SluisMaintenanceDeny" {
			t.Errorf("the one role has a denial: %v", s)
		}
	}
	for _, d := range rec.ofType(policyType) {
		for _, s := range statements(t, prop(d, "policy").StringValue()) {
			for _, r := range strs(s["Resource"]) {
				if r == "*" && s["Action"] != "sts:GetWebIdentityToken" {
					t.Errorf("%s: a grant on every resource: %v", d.Name, s)
				}
			}
			for _, a := range strs(s["Action"]) {
				if strings.HasSuffix(a, "*") {
					t.Errorf("%s: a wildcard action: %v", d.Name, s)
				}
			}
		}
	}

	// Two signing keys, ES384 and RS256, and no sealer.
	keys := rec.ofType("aws:kms/key:Key")
	if len(keys) != 2 {
		t.Fatalf("keys: %v", rec.names())
	}
	for name, spec := range map[string]string{"staging-signing-key": "ECC_NIST_P384", "staging-signing-key-rs256": "RSA_3072"} {
		k := rec.one(t, "aws:kms/key:Key", name)
		if prop(k, "keyUsage").StringValue() != "SIGN_VERIFY" || prop(k, "customerMasterKeySpec").StringValue() != spec {
			t.Errorf("%s: %v", name, k.Inputs)
		}
		if !rec.isProtected("aws:kms/key:Key", name) {
			t.Errorf("%s is not protected", name)
		}
	}
	if out["signingKeyAlias"] != "alias/sluis-signing" || out["signingKeyRS256Alias"] != "alias/sluis-signing-rs256" || out["signingKeyRS256ID"] == "" {
		t.Errorf("aliases %q %q", out["signingKeyAlias"], out["signingKeyRS256Alias"])
	}
	if rec.has("aws:kms/key:Key", "staging-sealer-key") || rec.has("aws:kms/alias:Alias", "staging-sealer-alias") {
		t.Error("the sealer key is still declared")
	}

	// The API: payload 2.0, the default endpoint off, mutual TLS on the domain.
	api := rec.one(t, "aws:apigatewayv2/api:Api", "staging-api")
	if prop(api, "protocolType").StringValue() != "HTTP" || !prop(api, "disableExecuteApiEndpoint").BoolValue() {
		t.Errorf("api: %v", api.Inputs)
	}
	integ := rec.one(t, "aws:apigatewayv2/integration:Integration", "staging-api-integration")
	if prop(integ, "payloadFormatVersion").StringValue() != "2.0" || prop(integ, "integrationType").StringValue() != "AWS_PROXY" ||
		prop(integ, "integrationUri").StringValue() != out["functionArn"]+":live" {
		t.Errorf("integration: %v", integ.Inputs)
	}
	dom := rec.one(t, "aws:apigatewayv2/domainName:DomainName", "staging-domain")
	if prop(dom, "domainName").StringValue() != domain {
		t.Errorf("domain: %v", dom.Inputs)
	}
	mtls := prop(dom, "mutualTlsAuthentication").ObjectValue()
	if mtls["truststoreUri"].StringValue() != "s3://acme-sluis-truststore/truststore/client-ca.pem" || mtls["truststoreVersion"].StringValue() != "v1" {
		t.Errorf("mutual TLS: %v", mtls)
	}
	cfg := prop(dom, "domainNameConfiguration").ObjectValue()
	if cfg["endpointType"].StringValue() != "REGIONAL" || cfg["securityPolicy"].StringValue() != "TLS_1_2" ||
		!strings.Contains(cfg["certificateArn"].StringValue(), "certificate/origin") {
		t.Errorf("domain configuration: %v", cfg)
	}
	obj := rec.one(t, "aws:s3/bucketObjectv2:BucketObjectv2", "staging-truststore-pem")
	if prop(obj, "content").StringValue() != truststore {
		t.Errorf("truststore content: %q", prop(obj, "content").StringValue())
	}
	if out["truststoreUri"] != "s3://acme-sluis-truststore/truststore/client-ca.pem" || out["domainTarget"] == "" ||
		out["domainHostedZoneID"] == "" || out["apiUrl"] == "" {
		t.Errorf("outputs: %v", out)
	}
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)
	return out
}

func schedules(t *testing.T, rec *recorder) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, s := range rec.ofType("aws:scheduler/schedule:Schedule") {
		tgt := prop(s, "target").ObjectValue()
		got[prop(s, "name").StringValue()] = tgt["input"].StringValue() + " " + tgt["arn"].StringValue()
		if prop(s, "scheduleExpression").StringValue() == "" {
			t.Errorf("%s has no expression", s.Name)
		}
	}
	return got
}

func TestTheTruvityShapeIsExpressible(t *testing.T) {
	rec, out := mustLambda(t, estate{
		orgs: []string{"truvity", "trust-form", "github:links"}, workspaces: []string{"T0TRUVITY"}, rate: "rate(2 minutes)", telemetry: true,
		mutate: func(a *arp.LambdaArgs) { a.API.DomainName = "access.one.example.test" },
	})
	shape(t, rec, out, "access.one.example.test")

	want := map[string]string{
		"sluis-github-truvity":      `{"kind":"tick","target":"truvity"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis:live",
		"sluis-github-trust-form":   `{"kind":"tick","target":"trust-form"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis:live",
		"sluis-github-github-links": `{"kind":"tick","target":"github:links"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis:live",
		"sluis-slack-T0TRUVITY":     `{"kind":"tick","target":"T0TRUVITY"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis:live",
		"sluis-directory-refresh":   `{"kind":"refresh"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis:live",
	}
	if got := schedules(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("schedules:\n got %v\nwant %v", got, want)
	}
	for _, s := range rec.ofType("aws:scheduler/schedule:Schedule") {
		if prop(s, "name").StringValue() == "sluis-directory-refresh" {
			if prop(s, "scheduleExpression").StringValue() != "rate(15 minutes)" {
				t.Errorf("directory refresh rate: %v", s.Inputs)
			}
			continue
		}
		if prop(s, "scheduleExpression").StringValue() != "rate(2 minutes)" {
			t.Errorf("%s: %v", s.Name, s.Inputs)
		}
		if prop(s, "target").ObjectValue()["roleArn"].StringValue() != out["schedulerRoleArn"] {
			t.Errorf("%s: not the scheduler's role", s.Name)
		}
	}
	// The scheduler's role invokes the one function and nothing else.
	sp := grants(statements(t, prop(rec.one(t, policyType, "staging-scheduler-policy"), "policy").StringValue()))
	if len(sp) != 1 || len(sp["lambda:InvokeFunction"]) != 1 {
		t.Errorf("scheduler grants %v", sp)
	}

	// Telemetry: the layer and the settings on the function.
	f := rec.one(t, fnType, "staging-http")
	if l := prop(f, "layers").ArrayValue(); len(l) != 2 || !strings.Contains(l[0].StringValue(), "otlp-lambda") {
		t.Errorf("layers: %v", l)
	}
	env := prop(f, "environment").ObjectValue()["variables"].ObjectValue()
	if env["OTEL_EXPORTER_OTLP_ENDPOINT"].StringValue() != "https://otlp.example.test" || env["OTEL_SERVICE_NAME"].StringValue() != "sluis" {
		t.Errorf("env: %v", env)
	}
}

func TestTheHiveShapeIsExpressible(t *testing.T) {
	// example: one GitHub org, Slack none, the default tick, and the telemetry
	// layer not ready yet.
	rec, out := mustLambda(t, estate{
		orgs: []string{"acme"}, keepDefaultEndpoint: false,
		mutate: func(a *arp.LambdaArgs) {
			a.API.DomainName = "access.two.example.test"
			a.SigningKeyAlias = ""
		},
	})
	shape(t, rec, out, "access.two.example.test")
	if got := schedules(t, rec); len(got) != 2 {
		t.Errorf("schedules: %v", got)
	}
	for _, s := range rec.ofType("aws:scheduler/schedule:Schedule") {
		if n := prop(s, "name").StringValue(); n == "sluis-directory-refresh" {
			continue
		}
		if prop(s, "scheduleExpression").StringValue() != "rate(5 minutes)" {
			t.Errorf("default rate: %v", s.Inputs)
		}
	}
	{
		f := rec.one(t, fnType, "staging-http")
		if l := prop(f, "layers").ArrayValue(); len(l) != 1 {
			t.Errorf("a layer beside the configuration's without Telemetry: %v", l)
		}
		env := prop(f, "environment").ObjectValue()["variables"].ObjectValue()
		for k := range env {
			if strings.HasPrefix(string(k), "OTEL_") {
				t.Errorf("%s without Telemetry", k)
			}
		}
		if prop(f, "vpcConfig").HasValue() {
			t.Error("the function is in a VPC")
		}
	}
	if n := len(rec.ofType("aws:iam/role:Role")); n != 2 {
		t.Errorf("%d roles, want the function's and the scheduler", n)
	}
	if g := rolePolicy(t, rec); len(g["kms:Sign"]) != 2 || len(g["kms:GetPublicKey"]) != 2 {
		t.Errorf("the role grants %v", g)
	}
	dom := rec.one(t, "aws:apigatewayv2/domainName:DomainName", "staging-domain")
	if prop(dom, "domainName").StringValue() != "access.two.example.test" || !prop(dom, "mutualTlsAuthentication").IsObject() {
		t.Errorf("domain: %v", dom.Inputs)
	}
	if !prop(rec.one(t, "aws:apigatewayv2/api:Api", "staging-api"), "disableExecuteApiEndpoint").BoolValue() {
		t.Error("the default endpoint is on")
	}
	if rec.has("aws:kms/key:Key", "staging-sealer-key") {
		t.Error("a sealer key")
	}
}

func TestTheDefaultEndpointStaysOnlyWhenAsked(t *testing.T) {
	rec, _ := mustLambda(t, estate{keepDefaultEndpoint: true})
	if prop(rec.one(t, "aws:apigatewayv2/api:Api", "staging-api"), "disableExecuteApiEndpoint").BoolValue() {
		t.Error("KeepDefaultEndpoint did not keep it")
	}
}

const stageType = "aws:apigatewayv2/stage:Stage"

func TestAccessLogsAreOffUnlessAsked(t *testing.T) {
	rec, _ := mustLambda(t, estate{})
	if rec.has("aws:cloudwatch/logGroup:LogGroup", "staging-api-access") {
		t.Error("an access log group exists without AccessLogs")
	}
	if !prop(rec.one(t, stageType, "staging-api-stage"), "accessLogSettings").IsNull() {
		t.Error("the stage logs without AccessLogs")
	}
}

func TestAccessLogsLogPathAndStatusOnly(t *testing.T) {
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.AccessLogs = &arp.AccessLogsArgs{} }})
	lg := rec.one(t, "aws:cloudwatch/logGroup:LogGroup", "staging-api-access")
	if got := prop(lg, "retentionInDays").NumberValue(); got != 7 {
		t.Errorf("retention = %v, want 7", got)
	}
	if got := prop(lg, "name").StringValue(); got != "/aws/apigateway/sluis" {
		t.Errorf("log group name = %q", got)
	}
	set := prop(rec.one(t, stageType, "staging-api-stage"), "accessLogSettings")
	if set.IsNull() {
		t.Fatal("the stage has no access log settings")
	}
	format := set.ObjectValue()["format"].StringValue()
	const exact = `{"requestTime":"$context.requestTime","requestId":"$context.requestId","httpMethod":"$context.httpMethod",` +
		`"path":"$context.path","status":"$context.status","responseLatency":"$context.responseLatency",` +
		`"integrationLatency":"$context.integrationLatency"}`
	if format != exact {
		t.Errorf("format = %s", format)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(format), &parsed); err != nil {
		t.Fatalf("format is not JSON: %v", err)
	}
	want := []string{"requestTime", "requestId", "httpMethod", "path", "status", "responseLatency", "integrationLatency"}
	if len(parsed) != len(want) {
		t.Errorf("format has %d fields, want %v", len(parsed), want)
	}
	for _, k := range want {
		if parsed[k] == "" {
			t.Errorf("format lacks %s", k)
		}
	}
	for _, bad := range []string{"querystring", "$request.querystring", "$context.identity", "header", "routeKey", "sourceIp", "userAgent"} {
		if strings.Contains(strings.ToLower(format), strings.ToLower(bad)) {
			t.Errorf("format contains %q: %s", bad, format)
		}
	}
	if strings.Contains(format, "$context.path") && strings.Contains(format, "?") {
		t.Errorf("format carries a query separator: %s", format)
	}
}

func TestAccessLogsRetentionIsConfigurableAndValidated(t *testing.T) {
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.AccessLogs = &arp.AccessLogsArgs{RetentionDays: 3} }})
	if got := prop(rec.one(t, "aws:cloudwatch/logGroup:LogGroup", "staging-api-access"), "retentionInDays").NumberValue(); got != 3 {
		t.Errorf("retention = %v, want 3", got)
	}
	if _, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.AccessLogs = &arp.AccessLogsArgs{RetentionDays: -1} }}); err == nil {
		t.Error("a negative retention was accepted")
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The package is the release, byte for byte, whatever the documents say; a
// changed document is a new layer and leaves the package as it was.
func TestADocumentChangesTheLayerAndNeverThePackage(t *testing.T) {
	pkg := zipFile(t, nil)
	build := func(e estate) (string, map[string]string) {
		e.pkg = pkg
		rec, _ := mustLambda(t, e)
		code := packagePath(t, rec.one(t, fnType, "staging-http"))
		return code, layerFiles(t, rec)
	}
	code, base := build(estate{})
	if raw, _ := os.ReadFile(code); sha256.Sum256(raw) != sha256.Sum256(must(os.ReadFile(pkg))) {
		t.Error("the function's code is not the release zip")
	}
	_, same := build(estate{})
	if !reflect.DeepEqual(base, same) {
		t.Error("the same inputs gave another layer")
	}
	for name, e := range map[string]estate{
		"serve": {config: "issuerURL: https://other.example.test\n"},
		"controller": {config: "issuerURL: https://access.example.test\n" +
			"controllers: {github: {interval: 5m, consoleURL: \"https://access.example.test/console\"}}\n"},
		"policy": {policy: minimalPolicy + "  all:access-roster:viewer: {}\n"},
	} {
		c, changed := build(e)
		if reflect.DeepEqual(base, changed) {
			t.Errorf("a changed %s document left the layer as it was", name)
		}
		if !bytes.Equal(must(os.ReadFile(c)), must(os.ReadFile(code))) {
			t.Errorf("a changed %s document changed the package", name)
		}
	}
}

func must(raw []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return raw
}

// What the library writes into the documents, and holds them to sluis's own
// loader before anything is published.
func TestTheDocumentsAreTheLibrarysAndHeldToTheBinarysLoader(t *testing.T) {
	rec, _ := mustLambda(t, estate{config: "issuerURL: https://access.example.test\n" +
		"signingKey: {kmsWrapped: {keyId: alias/sluis-signing-wrapped}}\n"})
	files := layerFiles(t, rec)
	for _, want := range []string{
		"apiVersion: sluis.truvity.github.io/sluis/v3", "file: /opt/sluis/policy.yaml",
		"root: /sluis/staging", "source: ssm", "region: " + region,
		"passwordSecret: recovery/password", "stateSecret: issuer/state-secret",
	} {
		if !strings.Contains(files["sluis/sluis.yaml"], want) {
			t.Errorf("the service document lacks %q:\n%s", want, files["sluis/sluis.yaml"])
		}
	}
	if !strings.HasPrefix(files["sluis/policy.yaml"], "apiVersion: sluis.truvity.github.io/policy/v2\n") {
		t.Errorf("the policy: %s", files["sluis/policy.yaml"])
	}
	for name, e := range map[string]estate{
		"a v1 key":                     {config: "issuerURL: https://x.example\npolicyDir: /var/task/config\n"},
		"an unknown key":               {config: "issuerURL: https://x.example\nissuerURl: x\n"},
		"another secrets root":         {config: "issuerURL: https://x.example\nsecrets: {source: ssm, root: /sluis/other}\n"},
		"another policy file":          {config: "issuerURL: https://x.example\npolicy: {file: /var/task/policy.yaml}\n"},
		"another apiVersion":           {config: "apiVersion: sluis.truvity.github.io/serve/v2\nissuerURL: https://x.example\n"},
		"a controller key it shares":   {config: "issuerURL: https://x.example\ncontrollers: {github: {release: x}}\n"},
		"a policy the loader refuses":  {policy: minimalPolicy + "clients: {c: {kind: public, requires: [x:y:z]}}\n"},
		"a policy with a misspelt key": {policy: minimalPolicy + "gruops: {}\n"},
		"another state secret":         {config: "issuerURL: https://x.example\nsigningKey: {kms: {keys: [a], stateSecret: other}}\n"},
	} {
		if _, _, err := buildLambda(t, e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The policy is a document, or a file or directory of layers that sluis's own
// renderer makes one of.
func TestThePolicyComesFromADocumentOrARenderedDirectory(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"10-policy.yaml":   "version: 1\ngroups:\n  all:access-roster:operator: {}\n",
		"20-clusters.yaml": "apiVersion: sluis.truvity.github.io/policy/v2\nexchange: {clusters: [{name: devel, issuer: 'https://k.example'}]}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.Policy, a.PolicyPath = "", dir }})
	if p := layerFiles(t, rec)["sluis/policy.yaml"]; !strings.Contains(p, "issuer: https://k.example") || !strings.Contains(p, "all:access-roster:operator") {
		t.Errorf("the rendered policy: %s", p)
	}
	if _, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.PolicyPath = dir }}); err == nil {
		t.Error("both Policy and PolicyPath were accepted")
	}
	if _, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.Policy = "" }}); err == nil {
		t.Error("no policy was accepted")
	}
}

// A package older than the library cannot read the layer it publishes.
func TestAPackageOlderThanTheLibraryIsRefused(t *testing.T) {
	old := filepath.Join(t.TempDir(), "sluis-lambda_1.62.0_linux_arm64.zip")
	if err := os.WriteFile(old, must(os.ReadFile(zipFile(t, nil))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildLambda(t, estate{pkg: old}); err == nil || !strings.Contains(err.Error(), "older than") {
		t.Errorf("a 1.62 package: %v", err)
	}
	unnamed := filepath.Join(t.TempDir(), "x.zip")
	if err := os.WriteFile(unnamed, must(os.ReadFile(zipFile(t, nil))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildLambda(t, estate{pkg: unnamed}); err == nil || !strings.Contains(err.Error(), "PackageVersion") {
		t.Errorf("an unnamed package: %v", err)
	}
	if _, _, err := buildLambda(t, estate{pkg: unnamed, mutate: func(a *arp.LambdaArgs) { a.PackageVersion = "v1.63.0" }}); err != nil {
		t.Errorf("an unnamed package with its version: %v", err)
	}
}

func TestThePackageCanBeFetchedFromAnHTTPSURLAndItsDigestIsChecked(t *testing.T) {
	raw, err := os.ReadFile(zipFile(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
	defer srv.Close()
	// A loopback http URL stands in for the release's https URL.
	sum := sha256.Sum256(raw)
	url := srv.URL + "/sluis-lambda_1.63.0_linux_arm64.zip"
	rec, _ := mustLambda(t, estate{pkg: url, mutate: func(a *arp.LambdaArgs) { a.PackageSHA256 = hex.EncodeToString(sum[:]) }})
	if got := must(os.ReadFile(packagePath(t, rec.one(t, fnType, "staging-http")))); !bytes.Equal(got, raw) {
		t.Error("the fetched package is not the release, byte for byte")
	}
	bad := estate{pkg: url, mutate: func(a *arp.LambdaArgs) { a.PackageSHA256 = strings.Repeat("0", 64) }}
	if _, _, err := buildLambda(t, bad); err == nil {
		t.Error("a wrong digest was accepted")
	}
	if _, _, err := buildLambda(t, estate{pkg: "http://example.test/x.zip"}); err == nil {
		t.Error("a plain http URL was accepted")
	}
}

func TestAPackageWithoutBootstrapIsRefused(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("other")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	p := filepath.Join(t.TempDir(), "sluis-lambda_1.63.0_linux_arm64.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildLambda(t, estate{pkg: p}); err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("err: %v", err)
	}
}

func TestTheLambdaInputsAreRequiredAndChecked(t *testing.T) {
	for name, mutate := range map[string]func(*arp.LambdaArgs){
		"no region":      func(a *arp.LambdaArgs) { a.Region = "" },
		"no instance":    func(a *arp.LambdaArgs) { a.Instance = "" },
		"a bad instance": func(a *arp.LambdaArgs) { a.Instance = "Staging/1" },
		"no digest":      func(a *arp.LambdaArgs) { a.PackageSHA256 = "" },
		"a wrong digest": func(a *arp.LambdaArgs) { a.PackageSHA256 = strings.Repeat("ab", 32) },
		"no package":     func(a *arp.LambdaArgs) { a.Package = "" },
		"the library's env": func(a *arp.LambdaArgs) {
			a.Telemetry = &arp.TelemetryArgs{LayerArn: pulumi.String("x"), Env: map[string]string{"SLUIS_CONFIG": "/x"}}
		},
		"no config":           func(a *arp.LambdaArgs) { a.Config = " \n" },
		"a bad function name": func(a *arp.LambdaArgs) { a.FunctionName = "sluis/http" },
		"no queue":            func(a *arp.LambdaArgs) { a.AuditQueueArn = nil },
		"no storage":          func(a *arp.LambdaArgs) { a.Storage = nil },
		"no state":            func(a *arp.LambdaArgs) { a.State = nil },
		"no certificate":      func(a *arp.LambdaArgs) { a.API.CertificateArn = nil },
		"no truststore":       func(a *arp.LambdaArgs) { a.API.TruststorePEM = "" },
		"no domain":           func(a *arp.LambdaArgs) { a.API.DomainName = "" },
		"bad alias":           func(a *arp.LambdaArgs) { a.SigningKeyAlias = "signing" },
		"bad rs256 alias":     func(a *arp.LambdaArgs) { a.SigningKeyRS256Alias = "signing" },
		"same alias":          func(a *arp.LambdaArgs) { a.SigningKeyRS256Alias = arp.DefaultSigningKeyAlias },
		"bad rate":            func(a *arp.LambdaArgs) { a.Schedule.Rate = "5 minutes" },
		"bad target":          func(a *arp.LambdaArgs) { a.Schedule.GitHubOrgs = []string{"a b"} },
		"duplicate":           func(a *arp.LambdaArgs) { a.Schedule.SlackWorkspaces = []string{"T1", "T1"} },
		"layer without arn":   func(a *arp.LambdaArgs) { a.Telemetry = &arp.TelemetryArgs{} },
	} {
		if _, _, err := buildLambda(t, estate{mutate: mutate}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAParameterKeyIsGrantedThroughSSMOnly(t *testing.T) {
	key := arnp + "kms:" + region + ":" + account + ":key/params"
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.ParameterKeyArn = key }})
	var n int
	for _, s := range statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()) {
		if s["Sid"] != "SluisParameterKey" {
			continue
		}
		n++
		via := s["Condition"].(map[string]any)["StringLike"].(map[string]any)["kms:ViaService"]
		if strs(s["Resource"])[0] != key || via != "ssm.*.amazonaws.com" {
			t.Errorf("%v", s)
		}
	}
	if n != 1 {
		t.Errorf("%d parameter-key statements", n)
	}
}

func TestThePodIdentityRoleSignsAndCarriesNoSealer(t *testing.T) {
	signing := arnp + "kms:" + region + ":" + account + ":key/signing"
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		store, err := arp.NewStorage(ctx, "staging", &arp.StorageArgs{BucketName: bucket})
		if err != nil {
			return err
		}
		_, err = arp.NewKubernetesIdentity(ctx, "staging", &arp.KubernetesIdentityArgs{
			ClusterName: cluster, ClusterArn: arnp + "eks:" + region + ":" + account + ":cluster/" + cluster, AccountID: account,
			Namespace: "sluis", ServiceAccount: "sluis",
			Storage: store.Grant(), SigningKeyArns: []pulumi.StringInput{pulumi.String(signing), pulumi.String(signing + "-rs")},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	pol := func(n string) map[string][]string {
		return grants(statements(t, prop(rec.one(t, "aws:iam/policy:Policy", n), "policy").StringValue()))
	}
	if g := pol("staging-sluis-policy"); len(g["kms:Sign"]) != 2 || len(g["kms:GetPublicKey"]) != 2 {
		t.Errorf("the role: %v", g)
	}
}

func TestAPackageEntryOutsideTheRootIsRefused(t *testing.T) {
	for _, name := range []string{"../evil", "a/../../evil", "/abs"} {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, n := range []string{"bootstrap", name} {
			w, _ := zw.Create(n)
			_, _ = w.Write([]byte("x"))
		}
		_ = zw.Close()
		p := filepath.Join(t.TempDir(), "sluis-lambda_1.63.0_linux_arm64.zip")
		if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := buildLambda(t, estate{pkg: p}); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestTheStateSecretIsGeneratedOnceAndKeptSecret(t *testing.T) {
	key := arnp + "kms:" + region + ":" + account + ":key/params"
	for _, k := range []string{"", key} {
		rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.ParameterKeyArn = k }})
		r := rec.one(t, "random:index/randomBytes:RandomBytes", "staging-state-secret")
		if prop(r, "length").NumberValue() != 32 || prop(r, "keepers").HasValue() {
			t.Errorf("random bytes: %v: 32 bytes and no keepers, or an apply rotates it", r.Inputs)
		}
		p := rec.one(t, "aws:ssm/parameter:Parameter", "staging-state-secret-internal")
		if prop(p, "name").StringValue() != "/sluis/staging/internal/config/issuer/state-secret" || prop(p, "type").StringValue() != "SecureString" {
			t.Errorf("parameter: %v", p.Inputs)
		}
		if !prop(p, "value").IsSecret() {
			t.Error("the secret's value is not marked secret")
		}
		if !prop(p, "overwrite").BoolValue() {
			t.Error("the parameter would refuse a value copied there first")
		}
		if got := prop(p, "keyId"); got.HasValue() != (k != "") || (k != "" && got.StringValue() != key) {
			t.Errorf("keyId %v with ParameterKeyArn %q", prop(p, "keyId"), k)
		}
	}
	_, out := mustLambda(t, estate{})
	if out["stateSecretParameter"] != "/sluis/staging/internal/config/issuer/state-secret" {
		t.Errorf("output: %q", out["stateSecretParameter"])
	}
}

func TestTheRS256KeyCanBeLeftOut(t *testing.T) {
	rec, out := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.DisableSigningKeyRS256 = true }})
	if len(rec.ofType("aws:kms/key:Key")) != 1 || out["signingKeyRS256Arn"] != "" {
		t.Errorf("keys: %v", rec.names())
	}
	if got := rolePolicy(t, rec)["kms:Sign"]; len(got) != 1 {
		t.Errorf("the role signs with %v", got)
	}
}

func TestTheRoleMayAskForAWebIdentityTokenTheControllersReadTheConsoleWith(t *testing.T) {
	rec, _ := mustLambda(t, estate{})
	if g := rolePolicy(t, rec)["sts:GetWebIdentityToken"]; len(g) == 0 {
		t.Error("the role may not ask STS for a web identity token")
	}
	for _, s := range statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()) {
		if s["Sid"] == "SluisWebIdentity" && s["Condition"] != nil {
			t.Errorf("a condition without an audience: %v", s)
		}
	}
	rec, _ = mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.WebIdentityAudience = "https://access.example.test" }})
	var n int
	for _, s := range statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()) {
		if s["Sid"] != "SluisWebIdentity" {
			continue
		}
		n++
		c := s["Condition"].(map[string]any)["ForAllValues:StringEquals"].(map[string]any)
		if !reflect.DeepEqual(strs(c["sts:IdentityTokenAudience"]), []string{"https://access.example.test"}) {
			t.Errorf("%v", c)
		}
	}
	if n != 1 {
		t.Errorf("%d statements", n)
	}
}

// webIdentityCondition is the audiences of the role's web identity grant, nil
// when the grant has no condition.
func webIdentityCondition(t *testing.T, rec *recorder) []string {
	t.Helper()
	var got []string
	var n int
	for _, s := range statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()) {
		if s["Sid"] != "SluisWebIdentity" {
			continue
		}
		n++
		if c, ok := s["Condition"].(map[string]any); ok {
			got = strs(c["ForAllValues:StringEquals"].(map[string]any)["sts:IdentityTokenAudience"])
		}
	}
	if n != 1 {
		t.Fatalf("%d web identity statements", n)
	}
	return got
}

func TestAdditionalWebIdentityAudiencesFollowTheConsoleAudienceInOrder(t *testing.T) {
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		a.WebIdentityAudience = "https://access.example.test/console"
		a.AdditionalWebIdentityAudiences = []string{"https://issuer.example", "https://collector.example"}
	}})
	want := []string{"https://access.example.test/console", "https://issuer.example", "https://collector.example"}
	if got := webIdentityCondition(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("audiences %v, want %v", got, want)
	}
	rec, _ = mustLambda(t, withInstallation(exampleInstallation(t), func(a *arp.LambdaArgs) {
		a.AdditionalWebIdentityAudiences = []string{"https://issuer.example"}
	}))
	want = []string{"https://access.example.test/console", "https://issuer.example"}
	if got := webIdentityCondition(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("with an installation: audiences %v, want %v", got, want)
	}
}

func TestAdditionalWebIdentityAudiencesAreRefusedWhenEmptyDuplicateOrWithoutAnAudience(t *testing.T) {
	const prefix = "sluispulumi: LambdaArgs.AdditionalWebIdentityAudiences"
	for name, c := range map[string]struct {
		primary string
		extra   []string
		want    string
	}{
		"an empty entry":           {"https://a.example", []string{"https://b.example", ""}, "[1] is empty"},
		"a blank entry":            {"https://a.example", []string{"  "}, "[0] is empty"},
		"a repeated entry":         {"https://a.example", []string{"https://b.example", "https://b.example"}, "duplicate"},
		"the console audience":     {"https://a.example", []string{"https://a.example"}, "duplicate"},
		"no web identity audience": {"", []string{"https://b.example"}, "WebIdentityAudience is empty"},
	} {
		_, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
			a.WebIdentityAudience = c.primary
			a.AdditionalWebIdentityAudiences = c.extra
		}})
		if err == nil || !strings.Contains(err.Error(), prefix) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Unset, or empty, the role's policy is the document it was before the field.
func TestTheRolePolicyIsUnchangedWithoutAdditionalWebIdentityAudiences(t *testing.T) {
	for _, aud := range []string{"", "https://access.example.test/console"} {
		set := func(extra []string) string {
			rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
				a.WebIdentityAudience = aud
				a.AdditionalWebIdentityAudiences = extra
			}})
			return prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()
		}
		base := set(nil)
		if got := set([]string{}); got != base {
			t.Errorf("audience %q: an empty list changes the policy:\n%s\n%s", aud, base, got)
		}
		if aud != "" && !strings.Contains(base, `"ForAllValues:StringEquals":{"sts:IdentityTokenAudience":["`+aud+`"]}`) {
			t.Errorf("the single-audience condition changed:\n%s", base)
		}
		if aud == "" && strings.Contains(base, "Condition") && strings.Contains(base, "IdentityTokenAudience") {
			t.Errorf("a condition without an audience:\n%s", base)
		}
	}
}

func TestTheDirectoryRefreshScheduleIsOnByDefaultAndConfigurable(t *testing.T) {
	rec, _ := mustLambda(t, estate{})
	s := rec.one(t, "aws:scheduler/schedule:Schedule", "staging-directory-refresh")
	tgt := prop(s, "target").ObjectValue()
	if prop(s, "scheduleExpression").StringValue() != "rate(15 minutes)" || !strings.HasSuffix(tgt["arn"].StringValue(), ":function:sluis:live") ||
		tgt["input"].StringValue() != `{"kind":"refresh"}` {
		t.Errorf("directory refresh schedule: %v", s.Inputs)
	}
	sp := grants(statements(t, prop(rec.one(t, policyType, "staging-scheduler-policy"), "policy").StringValue()))
	if !slices.ContainsFunc(sp["lambda:InvokeFunction"], func(a string) bool { return strings.HasSuffix(a, ":function:sluis:live") }) {
		t.Errorf("the scheduler cannot invoke the function: %v", sp)
	}

	rec, _ = mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		a.DirectoryRefresh = arp.DirectoryRefreshArgs{Disabled: true}
	}})
	if rec.has("aws:scheduler/schedule:Schedule", "staging-directory-refresh") {
		t.Error("a disabled directory refresh schedule was made")
	}
	if _, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.DirectoryRefresh.Rate = "hourly" }}); err == nil {
		t.Error("a bad rate was accepted")
	}
}

// The conditions the signing role's use of the symmetric key carries: only the
// purpose the adapter uses, and no context keys beside the three it sets.
func wantWrappedCondition(t *testing.T, who string, cond any) {
	t.Helper()
	c, _ := cond.(map[string]any)
	if got := c["StringEquals"]; !reflect.DeepEqual(got, map[string]any{"kms:EncryptionContext:purpose": "sluis-signing"}) {
		t.Errorf("%s: StringEquals %v", who, got)
	}
	if got := c["ForAllValues:StringEquals"]; !reflect.DeepEqual(got, map[string]any{"kms:EncryptionContextKeys": []any{"purpose", "alg", "kid"}}) {
		t.Errorf("%s: ForAllValues:StringEquals %v", who, got)
	}
}

func TestWrappedSigningReplacesTheAsymmetricKeysWithOneSymmetricKey(t *testing.T) {
	rec, out := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.WrappedSigning = &arp.WrappedSigningArgs{} }})

	keys := rec.ofType("aws:kms/key:Key")
	if len(keys) != 1 {
		t.Fatalf("keys: %v", rec.names())
	}
	k := rec.one(t, "aws:kms/key:Key", "staging-signing-key-wrapped")
	if prop(k, "keyUsage").StringValue() != "ENCRYPT_DECRYPT" || prop(k, "customerMasterKeySpec").StringValue() != "SYMMETRIC_DEFAULT" ||
		!prop(k, "enableKeyRotation").BoolValue() {
		t.Errorf("key: %v", k.Inputs)
	}
	if !rec.isProtected("aws:kms/key:Key", "staging-signing-key-wrapped") {
		t.Error("the wrapped signing key is not protected")
	}
	if prop(rec.one(t, "aws:kms/alias:Alias", "staging-signing-alias-wrapped"), "name").StringValue() != "alias/sluis-signing-wrapped" {
		t.Error("alias")
	}
	if out["signingKeyArn"] != "" || out["signingKeyRS256Arn"] != "" || out["wrappedSigningKeyArn"] == "" {
		t.Errorf("outputs: %v", out)
	}

	// The one role may use it, and never to sign remotely.
	wantKey := out["wrappedSigningKeyArn"]
	g := rolePolicy(t, rec)
	if len(g["kms:Sign"]) != 0 || len(g["kms:GetPublicKey"]) != 0 {
		t.Errorf("the role signs remotely: %v", g)
	}
	for _, a := range []string{"kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"} {
		if got := g[a]; !reflect.DeepEqual(got, []string{wantKey}) {
			t.Errorf("%s on %v", a, got)
		}
	}
	found := false
	for _, st := range statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()) {
		if st["Sid"] != "SluisWrappedSigning" {
			continue
		}
		found = true
		wantWrappedCondition(t, "the http role", st["Condition"])
		if st["Effect"] != "Allow" || !reflect.DeepEqual(strs(st["Action"]), []string{"kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"}) {
			t.Errorf("statement: %v", st)
		}
	}
	if !found {
		t.Error("no SluisWrappedSigning statement")
	}

	// The key policy holds the role to the same conditions, whatever else is
	// attached to it, and gives it nothing else on this key.
	roleArn := arnp + "iam::" + account + ":role/sluis"
	byID := map[string]map[string]any{}
	for _, st := range statements(t, prop(k, "policy").StringValue()) {
		byID[st["Sid"].(string)] = st
	}
	if root := byID["EnableIAMPolicies"]; root == nil || root["Effect"] != "Allow" || root["Action"] != "kms:*" ||
		!reflect.DeepEqual(root["Principal"], map[string]any{"AWS": arnp + "iam::" + account + ":root"}) {
		t.Errorf("admin statement: %v", root)
	}
	for sid, want := range map[string]string{
		"SluisSigningRolePurposeOnly":     "StringNotEquals",
		"SluisSigningRoleContextKeysOnly": "ForAnyValue:StringNotEquals",
	} {
		st := byID[sid]
		if st == nil || st["Effect"] != "Deny" || !reflect.DeepEqual(strs(st["Action"]), []string{"kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"}) {
			t.Errorf("%s: %v", sid, st)
			continue
		}
		cond := st["Condition"].(map[string]any)
		if !reflect.DeepEqual(cond["ArnEquals"], map[string]any{"aws:PrincipalArn": []any{roleArn}}) || cond[want] == nil {
			t.Errorf("%s: condition %v", sid, cond)
		}
	}
	if st := byID["SluisSigningRoleNothingElse"]; st == nil || st["Effect"] != "Deny" || st["Action"] != nil ||
		!reflect.DeepEqual(strs(st["NotAction"]), []string{"kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"}) {
		t.Errorf("the deny of everything else: %v", st)
	}
	// Nobody else may use the key under the signing context: the root delegation
	// would otherwise let any role with kms:Decrypt unwrap a key read from the State.
	res := byID["SluisSigningContextReserved"]
	if res == nil || res["Effect"] != "Deny" || !reflect.DeepEqual(res["Principal"], map[string]any{"AWS": "*"}) ||
		!reflect.DeepEqual(strs(res["Action"]), []string{"kms:Decrypt", "kms:Encrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:CreateGrant"}) ||
		!reflect.DeepEqual(res["Condition"], map[string]any{
			"StringEquals": map[string]any{"kms:EncryptionContext:purpose": "sluis-signing"},
			"ArnNotEquals": map[string]any{"aws:PrincipalArn": []any{roleArn}},
		}) {
		t.Errorf("the reserved-context denial: %v", res)
	}
	// The one role signs, so it is not denied the key ring.
	for _, st := range statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()) {
		if st["Sid"] == "SluisNoKeyringWrites" {
			t.Errorf("the signing role is denied the key ring: %v", st)
		}
	}
	if len(byID) != 5 {
		t.Errorf("key policy statements: %v", byID)
	}
}

func TestWrappedSigningWithAnExistingKeyCreatesNoKey(t *testing.T) {
	app := arnp + "kms:" + region + ":" + account + ":key/application"
	rec, out := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		a.WrappedSigning = &arp.WrappedSigningArgs{KeyArn: pulumi.String(app)}
	}})
	if n := len(rec.ofType("aws:kms/key:Key")); n != 0 || len(rec.ofType("aws:kms/alias:Alias")) != 0 {
		t.Errorf("created keys: %v", rec.names())
	}
	if out["wrappedSigningKeyArn"] != app || out["wrappedSigningKeyAlias"] != "" {
		t.Errorf("outputs: %v", out)
	}
	if got := rolePolicy(t, rec)["kms:Decrypt"]; !reflect.DeepEqual(got, []string{app}) {
		t.Errorf("the role decrypts with %v", got)
	}
}

func TestWrappedSigningAliasMustBeAnAlias(t *testing.T) {
	_, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.WrappedSigning = &arp.WrappedSigningArgs{KeyAlias: "wrapped"} }})
	if err == nil || !strings.Contains(err.Error(), "alias/") {
		t.Errorf("a bad alias: %v", err)
	}
}

func TestThePodIdentityRoleMayUseTheWrappedKey(t *testing.T) {
	app := arnp + "kms:" + region + ":" + account + ":key/application"
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		store, err := arp.NewStorage(ctx, "staging", &arp.StorageArgs{BucketName: bucket})
		if err != nil {
			return err
		}
		_, err = arp.NewKubernetesIdentity(ctx, "staging", &arp.KubernetesIdentityArgs{
			ClusterName: cluster, ClusterArn: arnp + "eks:" + region + ":" + account + ":cluster/" + cluster, AccountID: account,
			Namespace: "sluis", ServiceAccount: "sluis",
			Storage: store.Grant(), WrappedSigningKeyArn: pulumi.String(app),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	pol := func(n string) []map[string]any {
		return statements(t, prop(rec.one(t, "aws:iam/policy:Policy", n), "policy").StringValue())
	}
	serve := grants(pol("staging-sluis-policy"))
	if !reflect.DeepEqual(serve["kms:Decrypt"], []string{app}) || !reflect.DeepEqual(serve["kms:GenerateDataKeyPairWithoutPlaintext"], []string{app}) {
		t.Errorf("serve: %v", serve)
	}
	for _, st := range pol("staging-sluis-policy") {
		if st["Sid"] == "SluisWrappedSigning" {
			wantWrappedCondition(t, "serve", st["Condition"])
		}
	}
}

func TestTheKeyPolicyNamesEverySigningRole(t *testing.T) {
	serve := arnp + "iam::" + account + ":role/acme-sluis"
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		a.WrappedSigning = &arp.WrappedSigningArgs{AdditionalSigningRoleArns: []string{serve}}
	}})
	k := rec.one(t, "aws:kms/key:Key", "staging-signing-key-wrapped")
	for _, st := range statements(t, prop(k, "policy").StringValue()) {
		if st["Sid"] == "SluisSigningContextReserved" {
			got := st["Condition"].(map[string]any)["ArnNotEquals"]
			if !reflect.DeepEqual(got, map[string]any{"aws:PrincipalArn": []any{arnp + "iam::" + account + ":role/sluis", serve}}) {
				t.Errorf("signing roles: %v", got)
			}
			return
		}
	}
	t.Error("no reserved-context denial")
}

func TestThePodIdentityRoleIsNotDeniedTheKeyRing(t *testing.T) {
	app := arnp + "kms:" + region + ":" + account + ":key/application"
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		store, err := arp.NewStorage(ctx, "staging", &arp.StorageArgs{BucketName: bucket})
		if err != nil {
			return err
		}
		st, err := arp.NewState(ctx, "staging", &arp.StateArgs{TableName: table})
		if err != nil {
			return err
		}
		_, err = arp.NewKubernetesIdentity(ctx, "staging", &arp.KubernetesIdentityArgs{
			ClusterName: cluster, ClusterArn: arnp + "eks:" + region + ":" + account + ":cluster/" + cluster, AccountID: account,
			Namespace: "sluis", ServiceAccount: "sluis",
			Storage: store.Grant(), State: st.Grant(), WrappedSigningKeyArn: pulumi.String(app),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statements(t, prop(rec.one(t, "aws:iam/policy:Policy", "staging-sluis-policy"), "policy").StringValue()) {
		if s["Effect"] == "Deny" && s["Sid"] != "SluisMaintenanceDeny" {
			t.Errorf("the one role signs and is denied: %v", s)
		}
	}
}

func TestTheSharedKeyStatementsAreTheWholeOfTheKeyPolicyBeyondTheRootStatement(t *testing.T) {
	roles := []string{arnp + "iam::" + account + ":role/sluis"}
	st := arp.WrappedKeyPolicyStatements(roles)
	var sids []string
	for _, s := range st {
		sids = append(sids, s["Sid"].(string))
		if s["Effect"] != "Deny" {
			t.Errorf("%v is not a denial", s["Sid"])
		}
	}
	want := []string{"SluisSigningContextReserved", "SluisSigningRolePurposeOnly", "SluisSigningRoleContextKeysOnly", "SluisSigningRoleNothingElse"}
	if !reflect.DeepEqual(sids, want) {
		t.Fatalf("statements %v, want %v", sids, want)
	}
	// What the library creates is the root statement and exactly these.
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.WrappedSigning = &arp.WrappedSigningArgs{} }})
	k := rec.one(t, "aws:kms/key:Key", "staging-signing-key-wrapped")
	got := statements(t, prop(k, "policy").StringValue())
	if len(got) != 1+len(st) {
		t.Errorf("the created key has %d statements", len(got))
	}
	// The three role denials name only the signing roles; the reserved denial is
	// the only one that reaches other principals, and only under the signing context.
	for _, s := range st[1:] {
		if s["Condition"].(map[string]any)["ArnEquals"] == nil {
			t.Errorf("%v reaches principals that are not signing roles", s["Sid"])
		}
	}
}

// The recovery password is generated once, kept as a SecureString under the
// config/ prefix, written to a file the http function reads at cold start, and
// its VALUE is in no output: only the parameter's name is.
func TestTheRecoveryPasswordIsGeneratedOnceStoredSecretAndMappedToAFile(t *testing.T) {
	key := arnp + "kms:" + region + ":" + account + ":key/params"
	for _, k := range []string{"", key} {
		rec, out := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.ParameterKeyArn = k }})
		r := rec.one(t, "random:index/randomPassword:RandomPassword", "staging-recovery-password")
		if prop(r, "length").NumberValue() < 32 || prop(r, "special").BoolValue() || prop(r, "keepers").HasValue() {
			t.Errorf("random password: %v: at least 32 characters, no special ones, and no keepers, or an apply rotates it", r.Inputs)
		}
		p := rec.one(t, "aws:ssm/parameter:Parameter", "staging-recovery-password-internal")
		if prop(p, "name").StringValue() != "/sluis/staging/internal/config/recovery/password" || prop(p, "type").StringValue() != "SecureString" {
			t.Errorf("parameter: %v", p.Inputs)
		}
		value := prop(p, "value")
		if !value.IsSecret() {
			t.Error("the password's value is not marked secret")
		}
		if got := value.SecretValue().Element.StringValue(); strings.ContainsAny(got, "0O1lI") || len(got) != 40 {
			t.Errorf("the stored password %q has a look-alike or the wrong length", got)
		}
		if got := prop(p, "keyId"); got.HasValue() != (k != "") || (k != "" && got.StringValue() != key) {
			t.Errorf("keyId %v with ParameterKeyArn %q", prop(p, "keyId"), k)
		}
		for name, v := range out {
			if strings.Contains(v, "EfGhJkMn") || strings.Contains(v, "XyZaBc") {
				t.Errorf("output %s carries the password: %q", name, v)
			}
		}
	}
	rec, out := mustLambda(t, estate{})
	if out["recoveryPasswordParameter"] != "/sluis/staging/internal/config/recovery/password" {
		t.Errorf("output: %q", out["recoveryPasswordParameter"])
	}
	files := layerFiles(t, rec)
	if !strings.Contains(files["sluis/sluis.yaml"], "passwordSecret: recovery/password") {
		t.Errorf("the service document does not name the password: %s", files["sluis/sluis.yaml"])
	}
}

// The toggle is a configuration key, the library owns the secret's name, and
// turning recovery off keeps the parameter.
func TestRecoveryEnabledIsWrittenIntoTheServiceDocument(t *testing.T) {
	off := false
	on := true
	configOf := func(e estate) string {
		rec, _ := mustLambda(t, e)
		return layerFiles(t, rec)["sluis/sluis.yaml"]
	}
	got := configOf(estate{config: "issuerURL: https://x.example\n"})
	if strings.Contains(got, "enabled") || !strings.Contains(got, "passwordSecret: recovery/password") {
		t.Errorf("default: %q, want the secret named and the hub's own default (on) left alone", got)
	}
	for want, e := range map[string]*bool{"enabled: false": &off, "enabled: true": &on} {
		got = configOf(estate{config: "issuerURL: https://x.example\n", mutate: func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: e} }})
		if !strings.Contains(got, want) || !strings.Contains(got, "issuerURL: https://x.example") {
			t.Errorf("Recovery.Enabled %v: %q", *e, got)
		}
	}
	got = configOf(estate{config: "issuerURL: https://x.example\nrecovery:\n  enabled: false\n  audience: x\n"})
	if !strings.Contains(got, "enabled: false") || !strings.Contains(got, "audience: x") {
		t.Errorf("an operator's recovery.enabled was lost: %q", got)
	}
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: &off} }})
	rec.one(t, "aws:ssm/parameter:Parameter", "staging-recovery-password-internal")
	for name, e := range map[string]estate{
		"disagrees": {config: "issuerURL: https://x.example\nrecovery: {enabled: true}\n",
			mutate: func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: &off} }},
		"own secret": {config: "issuerURL: https://x.example\nrecovery: {passwordSecret: other}\n"},
		"not a map":  {config: "- a\n"},
	} {
		if _, _, err := buildLambda(t, e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// There is one role, with the credentials and external documents it writes and
// the config it reads and never writes (config/* is the operator's and the
// stack's); it is denied nothing, since a deny would bind the signing, too.
func TestTheOneRoleReadsConfigAndWritesOnlyCredentialsAndExternalDocuments(t *testing.T) {
	rec, _ := mustLambda(t, estate{})
	ssmArn := arnp + "ssm:" + region + ":" + account + ":parameter"
	writes := v4Writes(ssmArn)
	h := rolePolicy(t, rec)
	if got := h["ssm:GetParametersByPath"]; !reflect.DeepEqual(sortedCopy(got), sortedCopy(v4Reads(ssmArn))) {
		t.Errorf("reads %v, want credentials, external documents and config", got)
	}
	for _, a := range []string{"ssm:PutParameter", "ssm:DeleteParameter"} {
		if !reflect.DeepEqual(sortedCopy(h[a]), sortedCopy(writes)) {
			t.Errorf("%s on %v, want %v", a, h[a], writes)
		}
	}
	// The rotation reads a parameter's previous revision.
	if got := h["ssm:GetParameterHistory"]; !reflect.DeepEqual(sortedCopy(got), sortedCopy(v4Writes(ssmArn))) {
		t.Errorf("history on %v", got)
	}
}

func v4Writes(ssmArn string) []string {
	return []string{
		ssmArn + "/sluis/staging/internal/credentials", ssmArn + "/sluis/staging/internal/credentials/*",
		ssmArn + "/sluis/staging/external", ssmArn + "/sluis/staging/external/*",
	}
}

func v4Reads(ssmArn string) []string {
	return append(v4Writes(ssmArn),
		ssmArn+"/sluis/staging/internal/config", ssmArn+"/sluis/staging/internal/config/*")
}

// A path grant covers every level below it: no Allow may name a parent of the
// installation's root or reach outside it, none is a wildcard inside an SSM
// path, and no ssm action is on every resource.
func TestNoGrantReachesOutsideTheInstallationsRoot(t *testing.T) {
	for range 1 {
		rec, _ := mustLambda(t, estate{})
		ssmArn := arnp + "ssm:" + region + ":" + account + ":parameter"
		for _, s := range statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()) {
			actions := strs(s["Action"])
			ssmAction := slices.ContainsFunc(actions, func(a string) bool { return strings.HasPrefix(a, "ssm:") })
			for _, res := range strs(s["Resource"]) {
				if ssmAction && res == "*" {
					t.Errorf("an ssm action on every resource: %v", s)
				}
				path, ok := strings.CutPrefix(res, ssmArn)
				if !ok {
					continue
				}
				// The only wildcard is the trailing /*.
				if strings.Contains(strings.TrimSuffix(path, "/*"), "*") {
					t.Errorf("a wildcard inside an SSM resource: %s", res)
				}
				if !strings.HasPrefix(strings.TrimSuffix(path, "/*"), "/sluis/staging/") {
					t.Errorf("a grant outside the installation's root /sluis/staging/: %s", res)
				}
			}
		}
		if slices.ContainsFunc(rolePolicy(t, rec)["ssm:GetParameter"], func(r string) bool { return strings.Contains(r, "/sluis/staging/export") }) {
			t.Error("the role reads the retired export/ prefix")
		}
	}
}

// With a customer-managed parameter key, each role may use it only for the
// parameters under the prefixes it reads or writes.
func TestTheParameterKeyIsHeldToTheRolesPrefixes(t *testing.T) {
	key := arnp + "kms:" + region + ":" + account + ":key/params"
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.ParameterKeyArn = key }})
	ssmArn := arnp + "ssm:" + region + ":" + account + ":parameter"
	want := []string{
		ssmArn + "/sluis/staging/internal/*", ssmArn + "/sluis/staging/external/*",
	}
	n := 0
	for _, s := range statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()) {
		if s["Sid"] != "SluisParameterKey" {
			continue
		}
		n++
		got := strs(s["Condition"].(map[string]any)["StringLike"].(map[string]any)["kms:EncryptionContext:PARAMETER_ARN"])
		if !reflect.DeepEqual(got, want) {
			t.Errorf("the key's parameters %v, want %v", got, want)
		}
	}
	if n != 1 {
		t.Errorf("%d parameter-key statements", n)
	}
}

// A document may not point a function at an endpoint of its own, unless the
// stack says it is a test; the instance may not be named private or export; a
// document naming remote KMS signing beside WrappedSigning is refused; and the
// telemetry environment holds telemetry settings only.
func TestWhatTheLibraryRefusesToPublish(t *testing.T) {
	for name, e := range map[string]estate{
		"a secrets endpoint":  {config: "issuerURL: https://x.example\nsecrets: {endpoint: 'https://evil.example'}\n"},
		"a dynamodb endpoint": {config: "issuerURL: https://x.example\nports: {adapter: dynamodb, dynamodb: {table: t, endpoint: 'https://evil.example'}}\n"},
		"instance private":    {mutate: func(a *arp.LambdaArgs) { a.Instance = "private" }},
		"instance export":     {mutate: func(a *arp.LambdaArgs) { a.Instance = "export" }},
		"kms beside wrapped": {config: "issuerURL: https://x.example\nsigningKey: {kms: {keys: [alias/a]}}\n",
			mutate: func(a *arp.LambdaArgs) { a.WrappedSigning = &arp.WrappedSigningArgs{} }},
	} {
		if _, _, err := buildLambda(t, e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	local := estate{config: "issuerURL: https://x.example\nsecrets: {endpoint: 'http://localhost:4566'}\n",
		mutate: func(a *arp.LambdaArgs) { a.AllowEndpoints = true }}
	if _, _, err := buildLambda(t, local); err != nil {
		t.Errorf("an endpoint with AllowEndpoints: %v", err)
	}
	for k, ok := range map[string]bool{
		"OTEL_EXPORTER_OTLP_ENDPOINT": true, "ACCESS_ROSTER_OTLP_AUDIENCE": true, "OPENTELEMETRY_COLLECTOR_CONFIG_URI": true,
		"AWS_LAMBDA_EXEC_WRAPPER": true, "SLUIS_OTLP_AUDIENCE": true, "SLUIS_ISSUER": true,
		"SLUIS_TELEMETRY_BUFFER_QUEUE_ITEMS": true, "SLUIS_CONFIG": false, "SLUIS_NOPE": false, "SLUIS_ROLE": false, "SLUIS_SECRET_FILES": false,
		"LD_PRELOAD": false, "AWS_REGION": false, "AWS_ENDPOINT_URL_SSM": false, "HTTPS_PROXY": false,
	} {
		_, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
			a.Telemetry = &arp.TelemetryArgs{LayerArn: pulumi.String("arn:layer"), Env: map[string]string{k: "x"}}
		}})
		if (err == nil) != ok {
			t.Errorf("Telemetry.Env %s: %v, want accepted=%v", k, err, ok)
		}
	}
}

// The function's code is a copy of the bytes held to the digest, never the
// caller's file: a file changed after the check is not what is uploaded.
func TestTheCodeIsTheVerifiedCopyNotTheCallersFile(t *testing.T) {
	pkg := zipFile(t, nil)
	rec, _ := mustLambda(t, estate{pkg: pkg})
	code := packagePath(t, rec.one(t, fnType, "staging-http"))
	if code == pkg {
		t.Fatal("the code is the caller's path")
	}
	if !bytes.Equal(must(os.ReadFile(code)), must(os.ReadFile(pkg))) {
		t.Error("the copy is not the package")
	}
	if fi, err := os.Stat(code); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the copy's mode: %v %v", fi, err)
	}
}

// The function is named for the installation (`sluis`), and the resources keep
// the logical names the http function had, so that FunctionName `<prefix>-http`
// keeps the existing function, role and log group in place: nothing is
// replaced. The old github and slack resources are not declared.
func TestTheFunctionNameDecidesTheNamesAndTheLogicalNamesStay(t *testing.T) {
	rec, out := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.FunctionName = "sluis-http" }})
	if got := prop(rec.one(t, fnType, "staging-http"), "name").StringValue(); got != "sluis-http" {
		t.Errorf("function name %q", got)
	}
	if got := prop(rec.one(t, "aws:iam/role:Role", "staging-http-role"), "name").StringValue(); got != "sluis-http" {
		t.Errorf("role name %q", got)
	}
	if got := prop(rec.one(t, "aws:cloudwatch/logGroup:LogGroup", "staging-http"), "name").StringValue(); got != "/aws/lambda/sluis-http" {
		t.Errorf("log group %q", got)
	}
	if got := grants(statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()))["lambda:InvokeFunction"]; len(got) != 1 ||
		!strings.HasSuffix(got[0], ":function:sluis-http:live") {
		t.Errorf("the function may invoke %v", got)
	}
	if out["functionName"] != "sluis-http" {
		t.Errorf("output: %q", out["functionName"])
	}
	// Schedules keep their prefix names, whatever the function is called.
	rec, _ = mustLambda(t, estate{orgs: []string{"acme"}, workspaces: []string{"T1"}, mutate: func(a *arp.LambdaArgs) { a.FunctionName = "sluis-http" }})
	got := schedules(t, rec)
	if len(got) != 3 || got["sluis-github-acme"] == "" || got["sluis-slack-T1"] == "" {
		t.Errorf("schedules: %v", got)
	}
}

// A run-now invokes this very function for both kinds of target: the library
// names it, and refuses another.
func TestTheInvokeTriggerNamesTheOneFunction(t *testing.T) {
	trigger := "issuerURL: https://x.example\nadapters: {trigger: {adapter: invoke%s}}\n"
	rec, _ := mustLambda(t, estate{config: strings.Replace(trigger, "%s", "", 1)})
	doc := layerFiles(t, rec)["sluis/sluis.yaml"]
	if !strings.Contains(doc, "github: sluis:live\n") || !strings.Contains(doc, "slack: sluis:live\n") {
		t.Errorf("the trigger does not name the function:\n%s", doc)
	}
	if _, _, err := buildLambda(t, estate{config: strings.Replace(trigger, "%s", ", settings: {github: sluis-github}", 1)}); err == nil {
		t.Error("a trigger naming another function was accepted")
	}
}

// A tick for each target of both kinds reaches the one function.
func TestEverySchedulePointsAtTheOneFunction(t *testing.T) {
	rec, out := mustLambda(t, estate{orgs: []string{"acme", "github:links"}, workspaces: []string{"T1"}})
	for name, v := range schedules(t, rec) {
		if !strings.HasSuffix(v, " "+out["functionArn"]+":live") && !strings.HasSuffix(v, ":function:sluis:live") {
			t.Errorf("%s: %s", name, v)
		}
	}
	if n := len(rec.ofType("aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig")); n != 1 {
		t.Errorf("%d invoke configs, want one: a pass that failed is the next tick's", n)
	}
}

// A paused schedule is declared, DISABLED, and everything else stays: its
// expression, its target, the scheduler's role and the function's grants, so that turning it on is one
// setting and no grant changes with it.
func TestAPausedScheduleIsDeclaredDisabledAndTheRoleKeepsItsGrants(t *testing.T) {
	const scheduleType = "aws:scheduler/schedule:Schedule"
	targets := estate{orgs: []string{"acme", "github:links"}, workspaces: []string{"T1"}}
	running, _ := mustLambda(t, targets)
	all := targets
	all.mutate = func(a *arp.LambdaArgs) {
		a.Schedule.Paused, a.DirectoryRefresh.Paused = true, true
	}
	paused, _ := mustLambda(t, all)

	if got, want := schedules(t, paused), schedules(t, running); !reflect.DeepEqual(got, want) || len(got) != 4 {
		t.Errorf("paused schedules %v, want the running ones %v", got, want)
	}
	for _, s := range running.ofType(scheduleType) {
		if v := prop(s, "state"); !v.IsNull() {
			t.Errorf("%s: a running schedule declares state %v: it is left to the default, as before", s.Name, v)
		}
	}
	for _, s := range paused.ofType(scheduleType) {
		if v := prop(s, "state"); !v.IsString() || v.StringValue() != "DISABLED" {
			t.Errorf("%s: state %v, want DISABLED", s.Name, v)
		}
	}
	if got, want := rolePolicy(t, paused), rolePolicy(t, running); !reflect.DeepEqual(got, want) {
		t.Errorf("pausing changed the function's grants:\n%v\n--- want ---\n%v", got, want)
	}
	sp := func(rec *recorder) string {
		return prop(rec.one(t, policyType, "staging-scheduler-policy"), "policy").StringValue()
	}
	if sp(paused) != sp(running) {
		t.Errorf("pausing changed the scheduler's role: %s", sp(paused))
	}

	// Each kind pauses on its own.
	for name, c := range map[string]struct {
		pause    func(*arp.LambdaArgs)
		disabled []string
	}{
		"ticks": {func(a *arp.LambdaArgs) { a.Schedule.Paused = true },
			[]string{"sluis-github-acme", "sluis-github-github-links", "sluis-slack-T1"}},
		"directory refresh": {func(a *arp.LambdaArgs) { a.DirectoryRefresh.Paused = true }, []string{"sluis-directory-refresh"}},
	} {
		e := targets
		e.mutate = c.pause
		rec, _ := mustLambda(t, e)
		var got []string
		for _, s := range rec.ofType(scheduleType) {
			if v := prop(s, "state"); v.IsString() && v.StringValue() == "DISABLED" {
				got = append(got, prop(s, "name").StringValue())
			}
		}
		if !reflect.DeepEqual(sortedCopy(got), sortedCopy(c.disabled)) {
			t.Errorf("%s: disabled %v, want %v", name, got, c.disabled)
		}
	}

	// Disabled leaves a schedule out and Paused declares it: one or the other.
	for name, mutate := range map[string]func(*arp.LambdaArgs){
		"directory refresh": func(a *arp.LambdaArgs) { a.DirectoryRefresh.Disabled, a.DirectoryRefresh.Paused = true, true },
	} {
		if _, _, err := buildLambda(t, estate{mutate: mutate}); err == nil || !strings.Contains(err.Error(), "set one") {
			t.Errorf("%s disabled and paused: %v", name, err)
		}
	}
}

// A reserved concurrency is declared only when the estate sets one: nil leaves
// the function unreserved, as before, and a ceiling the estate set by hand is
// kept only by writing it here.
func TestReservedConcurrencyIsTheEstatesCeiling(t *testing.T) {
	rec, _ := mustLambda(t, estate{})
	if got := prop(rec.one(t, fnType, "staging-http"), "reservedConcurrentExecutions"); !got.IsNull() {
		t.Errorf("unset declared %v", got)
	}
	twenty := 20
	rec, _ = mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.Function.ReservedConcurrency = &twenty }})
	if got := prop(rec.one(t, fnType, "staging-http"), "reservedConcurrentExecutions"); !got.IsNumber() || got.NumberValue() != 20 {
		t.Errorf("declared %v, want 20", got)
	}
	for _, bad := range []int{0, -1} {
		if _, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.Function.ReservedConcurrency = &bad }}); err == nil ||
			!strings.Contains(err.Error(), "ReservedConcurrency") {
			t.Errorf("%d: %v", bad, err)
		}
	}
}

// The first v1.75 apply adopts the v4 copies `sluis migrate secrets-layout`
// wrote: Overwrite is set, so the create does not fail with
// ParameterAlreadyExists. The value is the RandomBytes/RandomPassword's, and
// the parameter has a logical name of its own, so the old private/config
// resource is removed (deleted last), never replaced in place.
func TestTheGeneratedParametersAdoptAnExistingV4Copy(t *testing.T) {
	rec, _ := mustLambda(t, estate{})
	for _, name := range []string{"staging-state-secret-internal", "staging-recovery-password-internal"} {
		p := rec.one(t, "aws:ssm/parameter:Parameter", name)
		if !prop(p, "overwrite").BoolValue() {
			t.Errorf("%s: no overwrite: creating it would fail where `sluis migrate secrets-layout` wrote it", name)
		}
		if !strings.HasPrefix(prop(p, "name").StringValue(), "/sluis/staging/internal/config/") {
			t.Errorf("%s: name %v", name, p.Inputs)
		}
	}
	for _, old := range []string{"staging-state-secret", "staging-recovery-password"} {
		if rec.has("aws:ssm/parameter:Parameter", old) {
			t.Errorf("a parameter is still registered as %s: the old resource would be replaced, not removed", old)
		}
	}
}

const invocationType = "aws:lambda/invocation:Invocation"

// The post-deploy check is one invocation of the live alias with the check
// event, on by default, triggered by the function's version and a hash of the
// declared names.
func TestThePostDeployCheckInvokesTheLiveAliasWithTheCheckEvent(t *testing.T) {
	rec, out := mustLambda(t, estate{})
	if n := len(rec.ofType(invocationType)); n != 1 {
		t.Fatalf("%d invocations, want the one check: %v", n, rec.names())
	}
	inv := rec.one(t, invocationType, "staging-check")
	if got := prop(inv, "input").StringValue(); got != `{"kind":"check"}` {
		t.Errorf("input %q", got)
	}
	if got := prop(inv, "qualifier").StringValue(); got != arp.LiveAlias {
		t.Errorf("qualifier %q, want the live alias", got)
	}
	if got := prop(inv, "functionName").StringValue(); got != "sluis" {
		t.Errorf("function %q", got)
	}
	trig := prop(inv, "triggers").ObjectValue()
	if got := trig["version"].StringValue(); got != "7" {
		t.Errorf("version trigger %q, want the function's version", got)
	}
	if got := trig["declared"].StringValue(); got == "" || strings.Trim(got, "0123456789abcdef") != "" {
		t.Errorf("declared trigger %q is not a hex hash", got)
	}
	if len(trig) != 2 {
		t.Errorf("triggers %v: the version and the hash of the names, nothing else", trig)
	}
	// The names the hash is over are the output, and they are names only.
	if got := out["declaredParameters"]; got != "internal/config/recovery/password" {
		t.Errorf("declaredParameters %q, want the recovery password this estate declares", got)
	}
}

// A change of the declared names moves the trigger; the same names do not.
func TestThePostDeployCheckRunsAgainWhenTheDeclaredNamesChange(t *testing.T) {
	hash := func(e estate) string {
		rec, _ := mustLambda(t, e)
		return prop(rec.one(t, invocationType, "staging-check"), "triggers").ObjectValue()["declared"].StringValue()
	}
	if a, b := hash(estate{}), hash(estate{}); a != b {
		t.Errorf("the same declaration hashes %s and %s", a, b)
	}
	off := false
	if hash(estate{}) == hash(estate{mutate: func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: &off} }}) {
		t.Error("a declaration without the recovery password hashes as the one with it")
	}
}

// Check: false leaves the invocation out and keeps the list of names, for an
// estate that checks in a CI step.
func TestThePostDeployCheckIsAbsentWhenOff(t *testing.T) {
	off := false
	rec, out := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.Check = &off }})
	if n := len(rec.ofType(invocationType)); n != 0 {
		t.Errorf("%d invocations with Check: false", n)
	}
	if out["declaredParameters"] != "internal/config/recovery/password" {
		t.Errorf("declaredParameters %q with Check: false", out["declaredParameters"])
	}
}
