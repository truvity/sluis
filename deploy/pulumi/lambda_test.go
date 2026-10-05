package sluispulumi_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
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
	p := filepath.Join(t.TempDir(), "sluis-lambda_1.62.0_linux_arm64.zip")
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
	githubConfig        string
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
		e.config = "issuerURL: https://access.example.test\n"
	}
	if e.githubConfig == "" {
		e.githubConfig = "consoleURL: https://access.example.test/console\n"
	}
	if e.policy == "" {
		e.policy = minimalPolicy
	}
	pkgSHA := ""
	if !strings.Contains(e.pkg, "://") {
		pkgSHA = sha(t, e.pkg)
	}
	a := &arp.LambdaArgs{
		Region: region, AccountID: account, Instance: "kernel",
		Package: e.pkg, PackageSHA256: pkgSHA, Config: e.config, GitHubConfig: e.githubConfig,
		SlackConfig: "consoleURL: https://access.example.test/console\n", Policy: e.policy,
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
		l, err := arp.NewLambda(ctx, "kernel", args)
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
		collect("httpFunctionArn", l.HTTPFunctionArn)
		collect("githubFunctionArn", l.GitHubFunctionArn)
		collect("slackFunctionArn", l.SlackFunctionArn)
		collect("apiUrl", l.APIURL)
		collect("domainTarget", l.DomainTarget)
		collect("domainHostedZoneID", l.DomainHostedZoneID)
		collect("truststoreUri", l.TruststoreURI)
		collect("exportReadPolicy", l.ExportReadPolicyJSON)
		collect("schedulerRoleArn", l.SchedulerRoleArn)
		collect("stateSecretParameter", l.StateSecretParameter)
		collect("recoveryPasswordParameter", l.RecoveryPasswordParameter)
		collect("configLayerArn", l.ConfigLayerArn)
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

func rolePolicy(t *testing.T, rec *recorder, role string) map[string][]string {
	t.Helper()
	return grants(statements(t, prop(rec.one(t, policyType, "kernel-"+role+"-policy"), "policy").StringValue()))
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
	return archiveFiles(t, rec.one(t, layerType, "kernel-config"))
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

// shape holds what is true of every estate: the Truvity one and the hive one
// differ in their targets, their domain and their telemetry, and in nothing here.
func shape(t *testing.T, rec *recorder, out map[string]string, domain string) {
	t.Helper()
	roles := []string{"http", "github", "slack"}

	// Three functions from one package, arm64, provided.al2023, no VPC.
	if n := len(rec.ofType(fnType)); n != 3 {
		t.Fatalf("%d functions, want 3", n)
	}
	var pkgs []string
	for _, r := range roles {
		f := rec.one(t, fnType, "kernel-"+r)
		if prop(f, "name").StringValue() != "sluis-"+r || prop(f, "runtime").StringValue() != "provided.al2023" ||
			prop(f, "handler").StringValue() != "bootstrap" {
			t.Errorf("%s: %v", r, f.Inputs)
		}
		if a := prop(f, "architectures").ArrayValue(); len(a) != 1 || a[0].StringValue() != "arm64" {
			t.Errorf("%s architectures: %v", r, a)
		}
		if prop(f, "vpcConfig").HasValue() {
			t.Errorf("%s is in a VPC", r)
		}
		env := prop(f, "environment").ObjectValue()["variables"].ObjectValue()
		if env["SLUIS_ROLE"].StringValue() != r || env["SLUIS_CONFIG"].StringValue() != "/opt/sluis/"+r+".yaml" {
			t.Errorf("%s env: %v", r, env)
		}
		for k := range env {
			if k != "SLUIS_ROLE" && k != "SLUIS_CONFIG" && !strings.HasPrefix(string(k), "OTEL_") {
				t.Errorf("%s: %s in the environment", r, k)
			}
		}
		// The configuration layer is mounted last.
		if l := prop(f, "layers").ArrayValue(); len(l) == 0 || l[len(l)-1].StringValue() != out["configLayerArn"] {
			t.Errorf("%s layers: %v, want the configuration layer last", r, l)
		}
		pkgs = append(pkgs, packagePath(t, f))
	}
	if pkgs[0] != pkgs[1] || pkgs[1] != pkgs[2] {
		t.Error("the three functions do not share one package")
	}
	files := layerFiles(t, rec)
	if got := keysOf(files); !reflect.DeepEqual(got, []string{"sluis/github.yaml", "sluis/http.yaml", "sluis/policy.yaml", "sluis/slack.yaml"}) {
		t.Errorf("the layer holds %v", got)
	}
	layer := rec.one(t, layerType, "kernel-config")
	if !prop(layer, "skipDestroy").BoolValue() || prop(layer, "layerName").StringValue() != "sluis-config" {
		t.Errorf("layer: %v", layer.Inputs)
	}

	// One role per function: distinct, and only http signs and runs a pass.
	seen := map[string]bool{}
	for _, r := range roles {
		role := rec.one(t, "aws:iam/role:Role", "kernel-"+r+"-role")
		if seen[prop(role, "name").StringValue()] {
			t.Errorf("%s shares a role", r)
		}
		seen[prop(role, "name").StringValue()] = true
		g := rolePolicy(t, rec, r)
		for _, a := range []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket", "dynamodb:GetItem", "dynamodb:PutItem",
			"dynamodb:Query", "ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssm:PutParameter", "ssm:DeleteParameter",
			"sqs:SendMessage", "logs:CreateLogStream", "logs:PutLogEvents"} {
			if len(g[a]) == 0 {
				t.Errorf("%s lacks %s", r, a)
			}
		}
		for _, a := range []string{"kms:Sign", "kms:GetPublicKey", "lambda:InvokeFunction"} {
			if (len(g[a]) > 0) != (r == "http") {
				t.Errorf("%s: %s granted=%v", r, a, len(g[a]) > 0)
			}
		}
		if got := g["sqs:SendMessage"]; !reflect.DeepEqual(got, []string{arnp + "sqs:" + region + ":" + account + ":audit-ingest"}) {
			t.Errorf("%s sqs: %v", r, got)
		}
		ssmArn := arnp + "ssm:" + region + ":" + account + ":parameter"
		wantRead := []string{ssmArn + "/sluis/kernel/private/credentials", ssmArn + "/sluis/kernel/private/credentials/*",
			ssmArn + "/sluis/kernel/export", ssmArn + "/sluis/kernel/export/*"}
		if r == "http" {
			wantRead = append(wantRead, ssmArn+"/sluis/kernel/private/config", ssmArn+"/sluis/kernel/private/config/*")
		}
		if got := g["ssm:GetParametersByPath"]; !reflect.DeepEqual(sortedCopy(got), sortedCopy(wantRead)) {
			t.Errorf("%s reads %v", r, got)
		}
		for _, res := range g["ssm:PutParameter"] {
			if !strings.Contains(res, "/sluis/kernel/private/credentials") && !strings.Contains(res, "/sluis/kernel/export") {
				t.Errorf("%s may put %s", r, res)
			}
		}
	}
	httpG := rolePolicy(t, rec, "http")
	if got := httpG["lambda:InvokeFunction"]; !reflect.DeepEqual(got, []string{
		arnp + "lambda:" + region + ":" + account + ":function:sluis-github", arnp + "lambda:" + region + ":" + account + ":function:sluis-slack"}) {
		t.Errorf("http may invoke %v", got)
	}
	wantKeys := []string{out["signingKeyArn"], out["signingKeyRS256Arn"]}
	for _, a := range []string{"kms:Sign", "kms:GetPublicKey"} {
		if got := httpG[a]; !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("http %s on %v, want %v", a, got, wantKeys)
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
	for name, spec := range map[string]string{"kernel-signing-key": "ECC_NIST_P384", "kernel-signing-key-rs256": "RSA_3072"} {
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
	if rec.has("aws:kms/key:Key", "kernel-sealer-key") || rec.has("aws:kms/alias:Alias", "kernel-sealer-alias") {
		t.Error("the sealer key is still declared")
	}

	// The API: payload 2.0, the default endpoint off, mutual TLS on the domain.
	api := rec.one(t, "aws:apigatewayv2/api:Api", "kernel-api")
	if prop(api, "protocolType").StringValue() != "HTTP" || !prop(api, "disableExecuteApiEndpoint").BoolValue() {
		t.Errorf("api: %v", api.Inputs)
	}
	integ := rec.one(t, "aws:apigatewayv2/integration:Integration", "kernel-api-integration")
	if prop(integ, "payloadFormatVersion").StringValue() != "2.0" || prop(integ, "integrationType").StringValue() != "AWS_PROXY" ||
		prop(integ, "integrationUri").StringValue() != out["httpFunctionArn"] {
		t.Errorf("integration: %v", integ.Inputs)
	}
	dom := rec.one(t, "aws:apigatewayv2/domainName:DomainName", "kernel-domain")
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
	obj := rec.one(t, "aws:s3/bucketObjectv2:BucketObjectv2", "kernel-truststore-pem")
	if prop(obj, "content").StringValue() != truststore {
		t.Errorf("truststore content: %q", prop(obj, "content").StringValue())
	}
	if out["truststoreUri"] != "s3://acme-sluis-truststore/truststore/client-ca.pem" || out["domainTarget"] == "" ||
		out["domainHostedZoneID"] == "" || out["apiUrl"] == "" {
		t.Errorf("outputs: %v", out)
	}

	// The consumer's policy reads /sluis/export/* and nothing else.
	eg := grants(statements(t, out["exportReadPolicy"]))
	if len(eg) != 3 {
		t.Errorf("export policy grants %v", eg)
	}
	for a, res := range eg {
		if !strings.HasPrefix(a, "ssm:Get") {
			t.Errorf("export policy grants %s", a)
		}
		for _, r := range res {
			if !strings.Contains(r, ":parameter/sluis/kernel/export") {
				t.Errorf("export policy names %s", r)
			}
		}
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
		"sluis-github-truvity":      `{"kind":"tick","target":"truvity"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-github",
		"sluis-github-trust-form":   `{"kind":"tick","target":"trust-form"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-github",
		"sluis-github-github-links": `{"kind":"tick","target":"github:links"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-github",
		"sluis-slack-T0TRUVITY":     `{"kind":"tick","target":"T0TRUVITY"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-slack",
		"sluis-exports":             `{"kind":"exports"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-http",
		"sluis-directory-refresh":   `{"kind":"refresh"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-http",
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
		if prop(s, "name").StringValue() == "sluis-exports" {
			if prop(s, "scheduleExpression").StringValue() != "rate(15 minutes)" {
				t.Errorf("exports rate: %v", s.Inputs)
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
	// The scheduler's role invokes the two controllers and the exports' function.
	sp := grants(statements(t, prop(rec.one(t, policyType, "kernel-scheduler-policy"), "policy").StringValue()))
	if len(sp) != 1 || len(sp["lambda:InvokeFunction"]) != 3 {
		t.Errorf("scheduler grants %v", sp)
	}

	// Telemetry: the layer and the settings on all three.
	for _, r := range []string{"http", "github", "slack"} {
		f := rec.one(t, fnType, "kernel-"+r)
		if l := prop(f, "layers").ArrayValue(); len(l) != 2 || !strings.Contains(l[0].StringValue(), "otlp-lambda") {
			t.Errorf("%s layers: %v", r, l)
		}
		env := prop(f, "environment").ObjectValue()["variables"].ObjectValue()
		if env["OTEL_EXPORTER_OTLP_ENDPOINT"].StringValue() != "https://otlp.example.test" || env["OTEL_SERVICE_NAME"].StringValue() != "sluis-"+r {
			t.Errorf("%s env: %v", r, env)
		}
	}
}

func TestTheHiveShapeIsExpressible(t *testing.T) {
	// Hive: one GitHub org, Slack none, the default tick, and the telemetry
	// layer not ready yet.
	rec, out := mustLambda(t, estate{
		orgs: []string{"opwerm"}, keepDefaultEndpoint: false,
		mutate: func(a *arp.LambdaArgs) {
			a.API.DomainName = "access.two.example.test"
			a.SigningKeyAlias = ""
		},
	})
	shape(t, rec, out, "access.two.example.test")
	if got := schedules(t, rec); len(got) != 3 {
		t.Errorf("schedules: %v", got)
	}
	for _, s := range rec.ofType("aws:scheduler/schedule:Schedule") {
		if n := prop(s, "name").StringValue(); n == "sluis-exports" || n == "sluis-directory-refresh" {
			continue
		}
		if prop(s, "scheduleExpression").StringValue() != "rate(5 minutes)" {
			t.Errorf("default rate: %v", s.Inputs)
		}
	}
	for _, r := range []string{"http", "github", "slack"} {
		f := rec.one(t, fnType, "kernel-"+r)
		if l := prop(f, "layers").ArrayValue(); len(l) != 1 {
			t.Errorf("%s has a layer beside the configuration's without Telemetry: %v", r, l)
		}
		env := prop(f, "environment").ObjectValue()["variables"].ObjectValue()
		for k := range env {
			if strings.HasPrefix(string(k), "OTEL_") {
				t.Errorf("%s has %s without Telemetry", r, k)
			}
		}
		if prop(f, "vpcConfig").HasValue() {
			t.Errorf("%s is in a VPC", r)
		}
	}
	if n := len(rec.ofType("aws:iam/role:Role")); n != 4 {
		t.Errorf("%d roles, want three functions and the scheduler", n)
	}
	if g := rolePolicy(t, rec, "slack"); len(g["kms:Sign"]) != 0 {
		t.Error("slack signs")
	}
	if g := rolePolicy(t, rec, "http"); len(g["kms:Sign"]) != 2 || len(g["kms:GetPublicKey"]) != 2 {
		t.Errorf("http grants %v", g)
	}
	dom := rec.one(t, "aws:apigatewayv2/domainName:DomainName", "kernel-domain")
	if prop(dom, "domainName").StringValue() != "access.two.example.test" || !prop(dom, "mutualTlsAuthentication").IsObject() {
		t.Errorf("domain: %v", dom.Inputs)
	}
	if !prop(rec.one(t, "aws:apigatewayv2/api:Api", "kernel-api"), "disableExecuteApiEndpoint").BoolValue() {
		t.Error("the default endpoint is on")
	}
	if rec.has("aws:kms/key:Key", "kernel-sealer-key") {
		t.Error("a sealer key")
	}
}

func TestTheDefaultEndpointStaysOnlyWhenAsked(t *testing.T) {
	rec, _ := mustLambda(t, estate{keepDefaultEndpoint: true})
	if prop(rec.one(t, "aws:apigatewayv2/api:Api", "kernel-api"), "disableExecuteApiEndpoint").BoolValue() {
		t.Error("KeepDefaultEndpoint did not keep it")
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
		code := packagePath(t, rec.one(t, fnType, "kernel-http"))
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
		"serve":      {config: "issuerURL: https://other.example.test\n"},
		"controller": {githubConfig: "consoleURL: https://other.example.test/console\n"},
		"policy":     {policy: minimalPolicy + "  all:access-roster:viewer: {}\n"},
	} {
		c, changed := build(e)
		if reflect.DeepEqual(base, changed) {
			t.Errorf("a changed %s document left the layer as it was", name)
		}
		if c != code {
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
		"apiVersion: sluis.truvity.github.io/serve/v2", "file: /opt/sluis/policy.yaml",
		"root: /sluis/kernel", "source: ssm", "region: " + region,
		"passwordSecret: recovery/password", "stateSecret: issuer/state-secret",
	} {
		if !strings.Contains(files["sluis/http.yaml"], want) {
			t.Errorf("the http document lacks %q:\n%s", want, files["sluis/http.yaml"])
		}
	}
	for _, c := range []string{"github", "slack"} {
		if d := files["sluis/"+c+".yaml"]; !strings.Contains(d, "apiVersion: sluis.truvity.github.io/controller-"+c+"/v2") ||
			!strings.Contains(d, "file: /opt/sluis/policy.yaml") {
			t.Errorf("the %s document: %s", c, d)
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
		"another apiVersion":           {config: "apiVersion: sluis.truvity.github.io/serve/v1\nissuerURL: https://x.example\n"},
		"a controller with no console": {githubConfig: "interval: 5m\n"},
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
	old := filepath.Join(t.TempDir(), "sluis-lambda_1.61.2_linux_arm64.zip")
	if err := os.WriteFile(old, must(os.ReadFile(zipFile(t, nil))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildLambda(t, estate{pkg: old}); err == nil || !strings.Contains(err.Error(), "older than") {
		t.Errorf("a 1.61 package: %v", err)
	}
	unnamed := filepath.Join(t.TempDir(), "x.zip")
	if err := os.WriteFile(unnamed, must(os.ReadFile(zipFile(t, nil))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildLambda(t, estate{pkg: unnamed}); err == nil || !strings.Contains(err.Error(), "PackageVersion") {
		t.Errorf("an unnamed package: %v", err)
	}
	if _, _, err := buildLambda(t, estate{pkg: unnamed, mutate: func(a *arp.LambdaArgs) { a.PackageVersion = "v1.62.3" }}); err != nil {
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
	url := srv.URL + "/sluis-lambda_1.62.0_linux_arm64.zip"
	rec, _ := mustLambda(t, estate{pkg: url, mutate: func(a *arp.LambdaArgs) { a.PackageSHA256 = hex.EncodeToString(sum[:]) }})
	if got := must(os.ReadFile(packagePath(t, rec.one(t, fnType, "kernel-http")))); !bytes.Equal(got, raw) {
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
	p := filepath.Join(t.TempDir(), "sluis-lambda_1.62.0_linux_arm64.zip")
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
		"a bad instance": func(a *arp.LambdaArgs) { a.Instance = "Kernel/1" },
		"no digest":      func(a *arp.LambdaArgs) { a.PackageSHA256 = "" },
		"a wrong digest": func(a *arp.LambdaArgs) { a.PackageSHA256 = strings.Repeat("ab", 32) },
		"no package":     func(a *arp.LambdaArgs) { a.Package = "" },
		"the library's env": func(a *arp.LambdaArgs) {
			a.Telemetry = &arp.TelemetryArgs{LayerArn: pulumi.String("x"), Env: map[string]string{"SLUIS_CONFIG": "/x"}}
		},
		"no config":         func(a *arp.LambdaArgs) { a.Config = " \n" },
		"no github config":  func(a *arp.LambdaArgs) { a.GitHubConfig = "" },
		"no slack config":   func(a *arp.LambdaArgs) { a.SlackConfig = "" },
		"no queue":          func(a *arp.LambdaArgs) { a.AuditQueueArn = nil },
		"no storage":        func(a *arp.LambdaArgs) { a.Storage = nil },
		"no state":          func(a *arp.LambdaArgs) { a.State = nil },
		"no certificate":    func(a *arp.LambdaArgs) { a.API.CertificateArn = nil },
		"no truststore":     func(a *arp.LambdaArgs) { a.API.TruststorePEM = "" },
		"no domain":         func(a *arp.LambdaArgs) { a.API.DomainName = "" },
		"bad alias":         func(a *arp.LambdaArgs) { a.SigningKeyAlias = "signing" },
		"bad rs256 alias":   func(a *arp.LambdaArgs) { a.SigningKeyRS256Alias = "signing" },
		"same alias":        func(a *arp.LambdaArgs) { a.SigningKeyRS256Alias = arp.DefaultSigningKeyAlias },
		"bad rate":          func(a *arp.LambdaArgs) { a.Schedule.Rate = "5 minutes" },
		"bad target":        func(a *arp.LambdaArgs) { a.Schedule.GitHubOrgs = []string{"a b"} },
		"duplicate":         func(a *arp.LambdaArgs) { a.Schedule.SlackWorkspaces = []string{"T1", "T1"} },
		"layer without arn": func(a *arp.LambdaArgs) { a.Telemetry = &arp.TelemetryArgs{} },
	} {
		if _, _, err := buildLambda(t, estate{mutate: mutate}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAParameterKeyIsGrantedThroughSSMOnly(t *testing.T) {
	key := arnp + "kms:" + region + ":" + account + ":key/params"
	rec, out := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.ParameterKeyArn = key }})
	for _, r := range []string{"http", "github", "slack"} {
		var n int
		for _, s := range statements(t, prop(rec.one(t, policyType, "kernel-"+r+"-policy"), "policy").StringValue()) {
			if s["Sid"] != "SluisParameterKey" {
				continue
			}
			n++
			via := s["Condition"].(map[string]any)["StringLike"].(map[string]any)["kms:ViaService"]
			if strs(s["Resource"])[0] != key || via != "ssm.*.amazonaws.com" {
				t.Errorf("%s: %v", r, s)
			}
		}
		if n != 1 {
			t.Errorf("%s: %d parameter-key statements", r, n)
		}
	}
	if !strings.Contains(out["exportReadPolicy"], key) {
		t.Error("the consumer's policy cannot decrypt the exports")
	}
}

func TestThePodIdentityRolesCarryNoSealerAndOnlyServeSigns(t *testing.T) {
	signing := arnp + "kms:" + region + ":" + account + ":key/signing"
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		store, err := arp.NewStorage(ctx, "kernel", &arp.StorageArgs{BucketName: bucket})
		if err != nil {
			return err
		}
		_, err = arp.NewKubernetesIdentity(ctx, "kernel", &arp.KubernetesIdentityArgs{
			ClusterName: cluster, ClusterArn: arnp + "eks:" + region + ":" + account + ":cluster/" + cluster, AccountID: account,
			Namespace: "sluis", Serve: arp.ProcessArgs{ServiceAccount: "sluis"}, GitHub: arp.ProcessArgs{ServiceAccount: "sluis-github"},
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
	if g := pol("kernel-sluis-serve-policy"); len(g["kms:Sign"]) != 2 {
		t.Errorf("serve: %v", g)
	}
	if g := pol("kernel-sluis-github-policy"); len(g["kms:Sign"]) != 0 || len(g["kms:Decrypt"]) != 0 {
		t.Errorf("github: %v", g)
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
		p := filepath.Join(t.TempDir(), "sluis-lambda_1.62.0_linux_arm64.zip")
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
		r := rec.one(t, "random:index/randomBytes:RandomBytes", "kernel-state-secret")
		if prop(r, "length").NumberValue() != 32 || prop(r, "keepers").HasValue() {
			t.Errorf("random bytes: %v: 32 bytes and no keepers, or an apply rotates it", r.Inputs)
		}
		p := rec.one(t, "aws:ssm/parameter:Parameter", "kernel-state-secret")
		if prop(p, "name").StringValue() != "/sluis/kernel/private/config/issuer/state-secret" || prop(p, "type").StringValue() != "SecureString" {
			t.Errorf("parameter: %v", p.Inputs)
		}
		if !prop(p, "value").IsSecret() {
			t.Error("the secret's value is not marked secret")
		}
		if !prop(p, "overwrite").BoolValue() {
			t.Error("the parameter would refuse the value `sluis migrate ssm-layout` copied there first")
		}
		if got := prop(p, "keyId"); got.HasValue() != (k != "") || (k != "" && got.StringValue() != key) {
			t.Errorf("keyId %v with ParameterKeyArn %q", prop(p, "keyId"), k)
		}
	}
	_, out := mustLambda(t, estate{})
	if out["stateSecretParameter"] != "/sluis/kernel/private/config/issuer/state-secret" {
		t.Errorf("output: %q", out["stateSecretParameter"])
	}
}

func TestTheRS256KeyCanBeLeftOut(t *testing.T) {
	rec, out := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.DisableSigningKeyRS256 = true }})
	if len(rec.ofType("aws:kms/key:Key")) != 1 || out["signingKeyRS256Arn"] != "" {
		t.Errorf("keys: %v", rec.names())
	}
	if got := rolePolicy(t, rec, "http")["kms:Sign"]; len(got) != 1 {
		t.Errorf("http signs with %v", got)
	}
}

func TestOnlyTheControllersMayAskForAWebIdentityToken(t *testing.T) {
	rec, _ := mustLambda(t, estate{})
	for r, want := range map[string]bool{"http": false, "github": true, "slack": true} {
		g := rolePolicy(t, rec, r)["sts:GetWebIdentityToken"]
		if (len(g) > 0) != want {
			t.Errorf("%s: %v", r, g)
		}
		for _, s := range statements(t, prop(rec.one(t, policyType, "kernel-"+r+"-policy"), "policy").StringValue()) {
			if s["Sid"] == "SluisWebIdentity" && s["Condition"] != nil {
				t.Errorf("%s: a condition without an audience: %v", r, s)
			}
		}
	}
	rec, _ = mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.WebIdentityAudience = "https://access.example.test" }})
	for _, r := range []string{"github", "slack"} {
		var n int
		for _, s := range statements(t, prop(rec.one(t, policyType, "kernel-"+r+"-policy"), "policy").StringValue()) {
			if s["Sid"] != "SluisWebIdentity" {
				continue
			}
			n++
			c := s["Condition"].(map[string]any)["ForAllValues:StringEquals"].(map[string]any)
			if !reflect.DeepEqual(strs(c["sts:IdentityTokenAudience"]), []string{"https://access.example.test"}) {
				t.Errorf("%s: %v", r, c)
			}
		}
		if n != 1 {
			t.Errorf("%s: %d statements", r, n)
		}
	}
}

func TestTheExportsScheduleIsConfigurableAndCanBeLeftOut(t *testing.T) {
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		a.Exports = arp.ExportsArgs{Function: "github", Rate: "rate(1 hour)"}
	}})
	s := rec.one(t, "aws:scheduler/schedule:Schedule", "kernel-exports")
	tgt := prop(s, "target").ObjectValue()
	if prop(s, "scheduleExpression").StringValue() != "rate(1 hour)" || !strings.HasSuffix(tgt["arn"].StringValue(), ":function:sluis-github") ||
		tgt["input"].StringValue() != `{"kind":"exports"}` {
		t.Errorf("exports schedule: %v", s.Inputs)
	}
	sp := grants(statements(t, prop(rec.one(t, policyType, "kernel-scheduler-policy"), "policy").StringValue()))
	// The http function is invoked by the directory refresh, not by the exports.
	if len(sp["lambda:InvokeFunction"]) != 3 {
		t.Errorf("scheduler grants %v", sp)
	}
	rec, _ = mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.Exports.Disabled = true }})
	if rec.has("aws:scheduler/schedule:Schedule", "kernel-exports") {
		t.Error("a disabled exports schedule was made")
	}
	for name, mutate := range map[string]func(*arp.LambdaArgs){
		"bad function": func(a *arp.LambdaArgs) { a.Exports.Function = "nobody" },
		"bad rate":     func(a *arp.LambdaArgs) { a.Exports.Rate = "hourly" },
	} {
		if _, _, err := buildLambda(t, estate{mutate: mutate}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheDirectoryRefreshScheduleIsOnByDefaultAndConfigurable(t *testing.T) {
	rec, _ := mustLambda(t, estate{})
	s := rec.one(t, "aws:scheduler/schedule:Schedule", "kernel-directory-refresh")
	tgt := prop(s, "target").ObjectValue()
	if prop(s, "scheduleExpression").StringValue() != "rate(15 minutes)" || !strings.HasSuffix(tgt["arn"].StringValue(), ":function:sluis-http") ||
		tgt["input"].StringValue() != `{"kind":"refresh"}` {
		t.Errorf("directory refresh schedule: %v", s.Inputs)
	}
	sp := grants(statements(t, prop(rec.one(t, policyType, "kernel-scheduler-policy"), "policy").StringValue()))
	if !slices.ContainsFunc(sp["lambda:InvokeFunction"], func(a string) bool { return strings.HasSuffix(a, ":function:sluis-http") }) {
		t.Errorf("the scheduler cannot invoke the http function: %v", sp)
	}

	// Left out, with the exports elsewhere, the scheduler has no reason to
	// invoke http at all.
	rec, _ = mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		a.DirectoryRefresh = arp.DirectoryRefreshArgs{Disabled: true}
		a.Exports.Function = "github"
	}})
	if rec.has("aws:scheduler/schedule:Schedule", "kernel-directory-refresh") {
		t.Error("a disabled directory refresh schedule was made")
	}
	sp = grants(statements(t, prop(rec.one(t, policyType, "kernel-scheduler-policy"), "policy").StringValue()))
	if len(sp["lambda:InvokeFunction"]) != 2 {
		t.Errorf("scheduler grants %v", sp)
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
	k := rec.one(t, "aws:kms/key:Key", "kernel-signing-key-wrapped")
	if prop(k, "keyUsage").StringValue() != "ENCRYPT_DECRYPT" || prop(k, "customerMasterKeySpec").StringValue() != "SYMMETRIC_DEFAULT" ||
		!prop(k, "enableKeyRotation").BoolValue() {
		t.Errorf("key: %v", k.Inputs)
	}
	if !rec.isProtected("aws:kms/key:Key", "kernel-signing-key-wrapped") {
		t.Error("the wrapped signing key is not protected")
	}
	if prop(rec.one(t, "aws:kms/alias:Alias", "kernel-signing-alias-wrapped"), "name").StringValue() != "alias/sluis-signing-wrapped" {
		t.Error("alias")
	}
	if out["signingKeyArn"] != "" || out["signingKeyRS256Arn"] != "" || out["wrappedSigningKeyArn"] == "" {
		t.Errorf("outputs: %v", out)
	}

	// Only http may use it, and never to sign remotely.
	wantKey := out["wrappedSigningKeyArn"]
	for _, r := range []string{"http", "github", "slack"} {
		g := rolePolicy(t, rec, r)
		if len(g["kms:Sign"]) != 0 || len(g["kms:GetPublicKey"]) != 0 {
			t.Errorf("%s signs remotely: %v", r, g)
		}
		for _, a := range []string{"kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"} {
			if got := g[a]; (r == "http") != (len(got) > 0) || (r == "http" && !reflect.DeepEqual(got, []string{wantKey})) {
				t.Errorf("%s: %s on %v", r, a, got)
			}
		}
	}
	found := false
	for _, st := range statements(t, prop(rec.one(t, policyType, "kernel-http-policy"), "policy").StringValue()) {
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
	roleArn := arnp + "iam::" + account + ":role/sluis-http"
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
	// The github and slack roles may not write the key ring.
	for _, r := range []string{"http", "github", "slack"} {
		var denied map[string]any
		for _, st := range statements(t, prop(rec.one(t, policyType, "kernel-"+r+"-policy"), "policy").StringValue()) {
			if st["Sid"] == "SluisNoKeyringWrites" {
				denied = st
			}
		}
		if (r == "http") != (denied == nil) {
			t.Errorf("%s: keyring-write denial %v", r, denied)
		}
		if denied != nil && (denied["Effect"] != "Deny" || !reflect.DeepEqual(denied["Condition"], map[string]any{
			"ForAnyValue:StringEquals": map[string]any{"dynamodb:LeadingKeys": []any{"keyring", "keyring-index", "keyring-retired"}}})) {
			t.Errorf("%s: %v", r, denied)
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
	if got := rolePolicy(t, rec, "http")["kms:Decrypt"]; !reflect.DeepEqual(got, []string{app}) {
		t.Errorf("http decrypts with %v", got)
	}
	if got := rolePolicy(t, rec, "slack")["kms:Decrypt"]; len(got) != 0 {
		t.Errorf("slack decrypts with %v", got)
	}
}

func TestWrappedSigningAliasMustBeAnAlias(t *testing.T) {
	_, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.WrappedSigning = &arp.WrappedSigningArgs{KeyAlias: "wrapped"} }})
	if err == nil || !strings.Contains(err.Error(), "alias/") {
		t.Errorf("a bad alias: %v", err)
	}
}

func TestThePodIdentityServeRoleMayUseTheWrappedKeyAndNoOtherRoleMay(t *testing.T) {
	app := arnp + "kms:" + region + ":" + account + ":key/application"
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		store, err := arp.NewStorage(ctx, "kernel", &arp.StorageArgs{BucketName: bucket})
		if err != nil {
			return err
		}
		_, err = arp.NewKubernetesIdentity(ctx, "kernel", &arp.KubernetesIdentityArgs{
			ClusterName: cluster, ClusterArn: arnp + "eks:" + region + ":" + account + ":cluster/" + cluster, AccountID: account,
			Namespace: "sluis", Serve: arp.ProcessArgs{ServiceAccount: "sluis"}, GitHub: arp.ProcessArgs{ServiceAccount: "sluis-github"},
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
	serve := grants(pol("kernel-sluis-serve-policy"))
	if !reflect.DeepEqual(serve["kms:Decrypt"], []string{app}) || !reflect.DeepEqual(serve["kms:GenerateDataKeyPairWithoutPlaintext"], []string{app}) {
		t.Errorf("serve: %v", serve)
	}
	for _, st := range pol("kernel-sluis-serve-policy") {
		if st["Sid"] == "SluisWrappedSigning" {
			wantWrappedCondition(t, "serve", st["Condition"])
		}
	}
	if g := grants(pol("kernel-sluis-github-policy")); len(g["kms:Decrypt"]) != 0 || len(g["kms:GenerateDataKeyPairWithoutPlaintext"]) != 0 {
		t.Errorf("github: %v", g)
	}
}

func TestTheKeyPolicyNamesEverySigningRole(t *testing.T) {
	serve := arnp + "iam::" + account + ":role/acme-sluis-serve"
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		a.WrappedSigning = &arp.WrappedSigningArgs{AdditionalSigningRoleArns: []string{serve}}
	}})
	k := rec.one(t, "aws:kms/key:Key", "kernel-signing-key-wrapped")
	for _, st := range statements(t, prop(k, "policy").StringValue()) {
		if st["Sid"] == "SluisSigningContextReserved" {
			got := st["Condition"].(map[string]any)["ArnNotEquals"]
			if !reflect.DeepEqual(got, map[string]any{"aws:PrincipalArn": []any{arnp + "iam::" + account + ":role/sluis-http", serve}}) {
				t.Errorf("signing roles: %v", got)
			}
			return
		}
	}
	t.Error("no reserved-context denial")
}

func TestThePodIdentityOtherRolesMayNotWriteTheKeyRing(t *testing.T) {
	app := arnp + "kms:" + region + ":" + account + ":key/application"
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		store, err := arp.NewStorage(ctx, "kernel", &arp.StorageArgs{BucketName: bucket})
		if err != nil {
			return err
		}
		st, err := arp.NewState(ctx, "kernel", &arp.StateArgs{TableName: table})
		if err != nil {
			return err
		}
		_, err = arp.NewKubernetesIdentity(ctx, "kernel", &arp.KubernetesIdentityArgs{
			ClusterName: cluster, ClusterArn: arnp + "eks:" + region + ":" + account + ":cluster/" + cluster, AccountID: account,
			Namespace: "sluis", Serve: arp.ProcessArgs{ServiceAccount: "sluis"}, GitHub: arp.ProcessArgs{ServiceAccount: "sluis-github"},
			Storage: store.Grant(), State: st.Grant(), WrappedSigningKeyArn: pulumi.String(app),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	has := func(n string) bool {
		for _, s := range statements(t, prop(rec.one(t, "aws:iam/policy:Policy", n), "policy").StringValue()) {
			if s["Sid"] == "SluisNoKeyringWrites" {
				return true
			}
		}
		return false
	}
	if has("kernel-sluis-serve-policy") || !has("kernel-sluis-github-policy") {
		t.Error("only the serve role may write the key ring")
	}
}

func TestTheSharedKeyStatementsAreTheWholeOfTheKeyPolicyBeyondTheRootStatement(t *testing.T) {
	roles := []string{arnp + "iam::" + account + ":role/sluis-http"}
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
	k := rec.one(t, "aws:kms/key:Key", "kernel-signing-key-wrapped")
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

func TestTheControllersNeverWriteTheKeyRingWhateverTheSigning(t *testing.T) {
	rec, _ := mustLambda(t, estate{}) // remote signing
	for _, r := range []string{"http", "github", "slack"} {
		has := false
		for _, st := range statements(t, prop(rec.one(t, policyType, "kernel-"+r+"-policy"), "policy").StringValue()) {
			has = has || st["Sid"] == "SluisNoKeyringWrites"
		}
		if has != (r != "http") {
			t.Errorf("%s: keyring-write denial %v", r, has)
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
		r := rec.one(t, "random:index/randomPassword:RandomPassword", "kernel-recovery-password")
		if prop(r, "length").NumberValue() < 32 || prop(r, "special").BoolValue() || prop(r, "keepers").HasValue() {
			t.Errorf("random password: %v: at least 32 characters, no special ones, and no keepers, or an apply rotates it", r.Inputs)
		}
		p := rec.one(t, "aws:ssm/parameter:Parameter", "kernel-recovery-password")
		if prop(p, "name").StringValue() != "/sluis/kernel/private/config/recovery/password" || prop(p, "type").StringValue() != "SecureString" {
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
	if out["recoveryPasswordParameter"] != "/sluis/kernel/private/config/recovery/password" {
		t.Errorf("output: %q", out["recoveryPasswordParameter"])
	}
	files := layerFiles(t, rec)
	if !strings.Contains(files["sluis/http.yaml"], "passwordSecret: recovery/password") {
		t.Errorf("the http document does not name the password: %s", files["sluis/http.yaml"])
	}
	for _, role := range []string{"github", "slack"} {
		if strings.Contains(files["sluis/"+role+".yaml"], "recovery") {
			t.Errorf("%s was given the recovery password", role)
		}
	}
}

// The toggle is a configuration key, the library owns the secret's name, and
// turning recovery off keeps the parameter.
func TestRecoveryEnabledIsWrittenIntoTheHTTPDocument(t *testing.T) {
	off := false
	on := true
	configOf := func(e estate) string {
		rec, _ := mustLambda(t, e)
		return layerFiles(t, rec)["sluis/http.yaml"]
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
	rec.one(t, "aws:ssm/parameter:Parameter", "kernel-recovery-password")
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

// A leaked controller role must not read, replace or delete the operator's
// config (the recovery password, the state secret, the OAuth client, the declared
// clients); http reads config/* and writes only credentials/* and exports.
func TestControllersAreDeniedTheConfigPrefixAndHTTPWritesOnlyCredentials(t *testing.T) {
	rec, _ := mustLambda(t, estate{})
	ssmArn := arnp + "ssm:" + region + ":" + account + ":parameter"
	cfg := []string{ssmArn + "/sluis/kernel/private/config", ssmArn + "/sluis/kernel/private/config/*"}
	creds := []string{ssmArn + "/sluis/kernel/private/credentials", ssmArn + "/sluis/kernel/private/credentials/*"}
	export := []string{ssmArn + "/sluis/kernel/export", ssmArn + "/sluis/kernel/export/*"}
	actions := []string{"ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssm:PutParameter", "ssm:DeleteParameter"}
	denied := func(role string) map[string][]string {
		out := map[string][]string{}
		for _, s := range statements(t, prop(rec.one(t, policyType, "kernel-"+role+"-policy"), "policy").StringValue()) {
			if s["Effect"] != "Deny" {
				continue
			}
			for _, a := range strs(s["Action"]) {
				out[a] = append(out[a], strs(s["Resource"])...)
			}
		}
		return out
	}
	writes := append(append([]string{}, creds...), export...)
	for _, role := range []string{"github", "slack"} {
		d := denied(role)
		for _, a := range actions {
			if !reflect.DeepEqual(d[a], cfg) {
				t.Errorf("%s: %s is denied on %v, want %v", role, a, d[a], cfg)
			}
		}
		g := rolePolicy(t, rec, role)
		for _, a := range actions {
			if !reflect.DeepEqual(g[a], writes) {
				t.Errorf("%s: %s on %v, want %v", role, a, g[a], writes)
			}
		}
	}
	h := rolePolicy(t, rec, "http")
	if len(denied("http")) != 0 {
		t.Errorf("http is denied something: %v", denied("http"))
	}
	if got := h["ssm:GetParametersByPath"]; !reflect.DeepEqual(sortedCopy(got), sortedCopy(append(append([]string{}, writes...), cfg...))) {
		t.Errorf("http reads %v, want credentials, exports and config", got)
	}
	for _, a := range []string{"ssm:PutParameter", "ssm:DeleteParameter"} {
		if !reflect.DeepEqual(h[a], writes) {
			t.Errorf("http %s on %v, want %v", a, h[a], writes)
		}
	}
}

// A path grant covers every level below it: a controller allowed
// GetParametersByPath on <root>/private (or any parent of config/) would read the
// operator's config/* whatever the Deny on config/* says. No controller Allow
// may name a parent of <root>/private/config, and none names /sluis.
func TestNoControllerMayReadAParentOfTheConfigPrefix(t *testing.T) {
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.Exports.Function = "github" }})
	ssmArn := arnp + "ssm:" + region + ":" + account + ":parameter"
	config := "/sluis/kernel/private/config"
	for _, role := range []string{"github", "slack", "http"} {
		for _, s := range statements(t, prop(rec.one(t, policyType, "kernel-"+role+"-policy"), "policy").StringValue()) {
			if s["Effect"] != "Allow" {
				continue
			}
			for _, res := range strs(s["Resource"]) {
				path, ok := strings.CutPrefix(res, ssmArn)
				if !ok {
					continue
				}
				path = strings.TrimSuffix(path, "/*")
				if !strings.HasPrefix(path, "/sluis/kernel/") {
					t.Errorf("%s: a grant outside the installation's root: %s", role, res)
				}
				if role != "http" && (strings.HasPrefix(config, path+"/") || path == config || strings.HasPrefix(path, config+"/")) {
					t.Errorf("%s may reach config/* through %s (%v)", role, res, s["Action"])
				}
			}
		}
	}
}
