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

// zipFile writes a release zip: `bootstrap` at its root and a stale config.
func zipFile(t *testing.T, extra map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{"bootstrap": "#!binary\n", "config/sluis.yaml": "stale: true\n", "config/github.yaml": "stale: true\n"}
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
	p := filepath.Join(t.TempDir(), "sluis-lambda_1.0.0_linux_arm64.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type estate struct {
	pkg                 string
	config              string
	githubConfig        string
	catalogues          map[string]string
	cataloguePaths      []string
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
		e.githubConfig = "policyDir: /var/task/config\n"
	}
	a := &arp.LambdaArgs{
		Region: region, AccountID: account,
		Package: e.pkg, Config: e.config, GitHubConfig: e.githubConfig,
		SlackConfig: "policyDir: /var/task/config\\n", Catalogues: e.catalogues, CataloguePaths: e.cataloguePaths,
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

// packageFiles is what a function's package holds, by path: the text of a string
// asset and the content of a file asset.
func packageFiles(t *testing.T, f declared) map[string]string {
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
	var pkgs []map[string]string
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
		file := map[string]string{"http": "sluis", "github": "github", "slack": "slack"}[r]
		if env["SLUIS_ROLE"].StringValue() != r || env["SLUIS_CONFIG_FILE"].StringValue() != "/var/task/config/"+file+".yaml" {
			t.Errorf("%s env: %v", r, env)
		}
		pkgs = append(pkgs, packageFiles(t, f))
	}
	if !reflect.DeepEqual(pkgs[0], pkgs[1]) || !reflect.DeepEqual(pkgs[1], pkgs[2]) {
		t.Error("the three functions do not share one package")
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
		wantPriv := []string{ssmArn + "/sluis/private", ssmArn + "/sluis/private/*"}
		if got := g["ssm:GetParametersByPath"]; !reflect.DeepEqual(got, wantPriv) {
			t.Errorf("%s reads %v", r, got)
		}
		for _, res := range g["ssm:PutParameter"] {
			if !strings.Contains(res, "/sluis/private") && !strings.Contains(res, "/sluis/export") {
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
			if !strings.Contains(r, ":parameter/sluis/export") {
				t.Errorf("export policy names %s", r)
			}
		}
	}
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
		catalogues: map[string]string{"github-apps.yaml": "apps: []\n"},
		mutate:     func(a *arp.LambdaArgs) { a.API.DomainName = "access.one.example.test" },
	})
	shape(t, rec, out, "access.one.example.test")

	want := map[string]string{
		"sluis-github-truvity":      `{"kind":"tick","target":"truvity"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-github",
		"sluis-github-trust-form":   `{"kind":"tick","target":"trust-form"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-github",
		"sluis-github-github-links": `{"kind":"tick","target":"github:links"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-github",
		"sluis-slack-T0TRUVITY":     `{"kind":"tick","target":"T0TRUVITY"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-slack",
		"sluis-exports":             `{"kind":"exports"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-http",
	}
	if got := schedules(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("schedules:\n got %v\nwant %v", got, want)
	}
	for _, s := range rec.ofType("aws:scheduler/schedule:Schedule") {
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
		if l := prop(f, "layers").ArrayValue(); len(l) != 1 || !strings.Contains(l[0].StringValue(), "otlp-lambda") {
			t.Errorf("%s layers: %v", r, l)
		}
		env := prop(f, "environment").ObjectValue()["variables"].ObjectValue()
		if env["OTEL_EXPORTER_OTLP_ENDPOINT"].StringValue() != "https://otlp.example.test" || env["OTEL_SERVICE_NAME"].StringValue() != "sluis-"+r {
			t.Errorf("%s env: %v", r, env)
		}
	}
	pkg := packageFiles(t, rec.one(t, fnType, "kernel-http"))
	for _, p := range []string{"bootstrap", "config/sluis.yaml", "config/github.yaml", "config/slack.yaml", "config/github-apps.yaml"} {
		if _, ok := pkg[p]; !ok {
			t.Errorf("the package lacks %s; has %v", p, keysOf(pkg))
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
	if got := schedules(t, rec); len(got) != 2 {
		t.Errorf("schedules: %v", got)
	}
	for _, s := range rec.ofType("aws:scheduler/schedule:Schedule") {
		if prop(s, "name").StringValue() == "sluis-exports" {
			continue
		}
		if prop(s, "scheduleExpression").StringValue() != "rate(5 minutes)" {
			t.Errorf("default rate: %v", s.Inputs)
		}
	}
	for _, r := range []string{"http", "github", "slack"} {
		f := rec.one(t, fnType, "kernel-"+r)
		if prop(f, "layers").IsArray() && len(prop(f, "layers").ArrayValue()) != 0 {
			t.Errorf("%s has a layer without Telemetry: %v", r, f.Inputs)
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

func TestAChangedConfigOrCatalogueChangesThePackage(t *testing.T) {
	pkg := zipFile(t, nil)
	files := func(e estate) map[string]string {
		e.pkg = pkg
		rec, _ := mustLambda(t, e)
		return packageFiles(t, rec.one(t, fnType, "kernel-http"))
	}
	base := files(estate{config: "a: 1\n", catalogues: map[string]string{"c.yaml": "x: 1\n"}})
	if base["config/sluis.yaml"] != "a: 1\nrecovery:\n  passwordFile: /tmp/sluis/recovery-password\n" {
		t.Fatalf("the stale config in the zip won over the estate's: %q", base["config/sluis.yaml"])
	}
	if base["bootstrap"] != "#!binary\n" {
		t.Errorf("bootstrap: %q", base["bootstrap"])
	}
	if same := files(estate{config: "a: 1\n", catalogues: map[string]string{"c.yaml": "x: 1\n"}}); !reflect.DeepEqual(base, same) {
		t.Error("the same inputs gave another package")
	}
	if changed := files(estate{config: "a: 2\n", catalogues: map[string]string{"c.yaml": "x: 1\n"}}); reflect.DeepEqual(base, changed) {
		t.Error("a changed config left the package as it was")
	}
	if changed := files(estate{config: "a: 1\n", githubConfig: "b: 2\n", catalogues: map[string]string{"c.yaml": "x: 1\n"}}); reflect.DeepEqual(base, changed) {
		t.Error("a changed controller config left the package as it was")
	}
	if changed := files(estate{config: "a: 1\n", catalogues: map[string]string{"c.yaml": "x: 2\n"}}); reflect.DeepEqual(base, changed) {
		t.Error("a changed catalogue left the package as it was")
	}
}

func TestACatalogueOnDiskIsInThePackageUnderItsBaseName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "roster.yaml")
	if err := os.WriteFile(p, []byte("k: v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, _ := mustLambda(t, estate{cataloguePaths: []string{p}})
	if got := packageFiles(t, rec.one(t, fnType, "kernel-slack"))["config/roster.yaml"]; got != "k: v\n" {
		t.Errorf("catalogue: %q", got)
	}
	empty := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]estate{
		"empty file":      {cataloguePaths: []string{empty}},
		"missing file":    {cataloguePaths: []string{filepath.Join(t.TempDir(), "nope.yaml")}},
		"conflict":        {cataloguePaths: []string{p}, catalogues: map[string]string{"roster.yaml": "other\n"}},
		"a path":          {catalogues: map[string]string{"../x.yaml": "a: b"}},
		"over the config": {catalogues: map[string]string{"sluis.yaml": "a: b"}},
	} {
		if _, _, err := buildLambda(t, e); err == nil {
			t.Errorf("%s: accepted", name)
		}
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
	rec, _ := mustLambda(t, estate{pkg: srv.URL + "/sluis-lambda_1.0.0_linux_arm64.zip"})
	if packageFiles(t, rec.one(t, fnType, "kernel-http"))["bootstrap"] == "" {
		t.Error("no bootstrap from the URL")
	}
	sum := sha256.Sum256(raw)
	good := estate{pkg: srv.URL + "/x.zip", mutate: func(a *arp.LambdaArgs) { a.PackageSHA256 = hex.EncodeToString(sum[:]) }}
	if _, _, err := buildLambda(t, good); err != nil {
		t.Errorf("a right digest: %v", err)
	}
	bad := estate{pkg: srv.URL + "/x.zip", mutate: func(a *arp.LambdaArgs) { a.PackageSHA256 = strings.Repeat("0", 64) }}
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
	p := filepath.Join(t.TempDir(), "x.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildLambda(t, estate{pkg: p}); err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("err: %v", err)
	}
}

func TestTheLambdaInputsAreRequiredAndChecked(t *testing.T) {
	for name, mutate := range map[string]func(*arp.LambdaArgs){
		"no region":         func(a *arp.LambdaArgs) { a.Region = "" },
		"no package":        func(a *arp.LambdaArgs) { a.Package = "" },
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
		p := filepath.Join(t.TempDir(), "x.zip")
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
		if prop(p, "name").StringValue() != "/sluis/private/config/issuer/state-secret" || prop(p, "type").StringValue() != "SecureString" {
			t.Errorf("parameter: %v", p.Inputs)
		}
		if !prop(p, "value").IsSecret() {
			t.Error("the secret's value is not marked secret")
		}
		if got := prop(p, "keyId"); got.HasValue() != (k != "") || (k != "" && got.StringValue() != key) {
			t.Errorf("keyId %v with ParameterKeyArn %q", prop(p, "keyId"), k)
		}
	}
	_, out := mustLambda(t, estate{})
	if out["stateSecretParameter"] != "/sluis/private/config/issuer/state-secret" {
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
	if len(sp["lambda:InvokeFunction"]) != 2 {
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

func TestTheSecretFilesAreTheEnvironmentTheAppReads(t *testing.T) {
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		a.GitHub.SecretFiles = []arp.SecretFile{{Parameter: "/sluis/private/github/app-key", Path: "/tmp/sluis/github-app.pem"}}
		a.GitHub.Env = map[string]string{"GITHUB_APP_KEY": "ssm:/sluis/private/github/app-key"}
	}})
	env := func(r string) map[resource.PropertyKey]resource.PropertyValue {
		return prop(rec.one(t, fnType, "kernel-"+r), "environment").ObjectValue()["variables"].ObjectValue()
	}
	wantHTTP := `[{"parameter":"/sluis/private/config/issuer/state-secret","path":"/tmp/sluis/state-secret"},` +
		`{"parameter":"/sluis/private/config/recovery/password","path":"/tmp/sluis/recovery-password"}]`
	if got := env("http")["SLUIS_SECRET_FILES"].StringValue(); got != wantHTTP {
		t.Errorf("http: %s", got)
	}
	if got := env("github")["SLUIS_SECRET_FILES"].StringValue(); got != `[{"parameter":"/sluis/private/github/app-key","path":"/tmp/sluis/github-app.pem"}]` {
		t.Errorf("github: %s", got)
	}
	if env("github")["GITHUB_APP_KEY"].StringValue() != "ssm:/sluis/private/github/app-key" {
		t.Error("an ssm: mapping did not pass through")
	}
	if env("slack")["SLUIS_SECRET_FILES"].HasValue() {
		t.Error("slack lists no secret files")
	}
	for name, mutate := range map[string]func(*arp.LambdaArgs){
		"outside /tmp": func(a *arp.LambdaArgs) {
			a.Slack.SecretFiles = []arp.SecretFile{{Parameter: "/sluis/private/x", Path: "/var/task/x"}}
		},
		"traversal": func(a *arp.LambdaArgs) {
			a.Slack.SecretFiles = []arp.SecretFile{{Parameter: "/sluis/private/x", Path: "/tmp/../x"}}
		},
		"outside /sluis": func(a *arp.LambdaArgs) {
			a.Slack.SecretFiles = []arp.SecretFile{{Parameter: "/other/x", Path: "/tmp/x"}}
		},
		"the library's own": func(a *arp.LambdaArgs) { a.HTTP.Env = map[string]string{"SLUIS_SECRET_FILES": "[]"} },
	} {
		if _, _, err := buildLambda(t, estate{mutate: mutate}); err == nil {
			t.Errorf("%s: accepted", name)
		}
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

func TestWrappedSigningCanKeepTheRemoteKeysWhileAStackMoves(t *testing.T) {
	rec, out := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		a.WrappedSigning = &arp.WrappedSigningArgs{KeepRemoteSigningKeys: true, KeyAlias: "alias/acme-wrapped"}
	}})
	if n := len(rec.ofType("aws:kms/key:Key")); n != 3 {
		t.Errorf("keys: %v", rec.names())
	}
	g := rolePolicy(t, rec, "http")
	if len(g["kms:Sign"]) != 2 || len(g["kms:Decrypt"]) != 1 || out["signingKeyArn"] == "" {
		t.Errorf("http: %v %v", g, out)
	}
	if prop(rec.one(t, "aws:kms/alias:Alias", "kernel-signing-alias-wrapped"), "name").StringValue() != "alias/acme-wrapped" {
		t.Error("alias")
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
		if prop(p, "name").StringValue() != "/sluis/private/config/recovery/password" || prop(p, "type").StringValue() != "SecureString" {
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
	if out["recoveryPasswordParameter"] != "/sluis/private/config/recovery/password" {
		t.Errorf("output: %q", out["recoveryPasswordParameter"])
	}
	env := prop(rec.one(t, fnType, "kernel-http"), "environment").ObjectValue()["variables"].ObjectValue()
	wantFile := `"parameter":"/sluis/private/config/recovery/password","path":"/tmp/sluis/recovery-password"`
	if got := env["SLUIS_SECRET_FILES"].StringValue(); !strings.Contains(got, wantFile) {
		t.Errorf("http secret files: %s", got)
	}
	for _, role := range []string{"github", "slack"} {
		e := prop(rec.one(t, fnType, "kernel-"+role), "environment").ObjectValue()["variables"].ObjectValue()
		if f := e["SLUIS_SECRET_FILES"]; f.HasValue() && strings.Contains(f.StringValue(), "recovery") {
			t.Errorf("%s was given the recovery password", role)
		}
	}
}

// The toggle is a configuration key, the library owns the file's path, and
// turning recovery off keeps the parameter.
func TestRecoveryEnabledIsWrittenIntoTheHTTPConfiguration(t *testing.T) {
	off := false
	on := true
	configOf := func(e estate) string {
		rec, _ := mustLambda(t, e)
		return packageFiles(t, rec.one(t, fnType, "kernel-http"))["config/sluis.yaml"]
	}
	got := configOf(estate{config: "issuerURL: https://x\n"})
	if strings.Contains(got, "enabled") || !strings.Contains(got, "passwordFile: /tmp/sluis/recovery-password") {
		t.Errorf("default: %q, want the file named and the hub's own default (on) left alone", got)
	}
	for want, e := range map[string]*bool{"enabled: false": &off, "enabled: true": &on} {
		got = configOf(estate{config: "# the issuer\nissuerURL: https://x\n", mutate: func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: e} }})
		if !strings.Contains(got, want) || !strings.Contains(got, "# the issuer") || !strings.Contains(got, "issuerURL: https://x") {
			t.Errorf("Recovery.Enabled %v: %q", *e, got)
		}
	}
	// A key the operator already wrote is kept, and may not disagree.
	got = configOf(estate{config: "recovery:\n  enabled: false\n  audience: x\n"})
	if !strings.Contains(got, "enabled: false") || !strings.Contains(got, "audience: x") {
		t.Errorf("an operator's recovery.enabled was lost: %q", got)
	}
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: &off} }})
	rec.one(t, "aws:ssm/parameter:Parameter", "kernel-recovery-password")
	for name, e := range map[string]estate{
		"disagrees": {config: "recovery: {enabled: true}\n", mutate: func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: &off} }},
		"own file":  {config: "recovery: {passwordFile: /tmp/other}\n"},
		"not a map": {config: "- a\n"},
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
	cfg := []string{ssmArn + "/sluis/private/config", ssmArn + "/sluis/private/config/*"}
	creds := []string{ssmArn + "/sluis/private/credentials", ssmArn + "/sluis/private/credentials/*"}
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
	for _, role := range []string{"github", "slack"} {
		d := denied(role)
		for _, a := range actions {
			if !reflect.DeepEqual(d[a], cfg) {
				t.Errorf("%s: %s is denied on %v, want %v", role, a, d[a], cfg)
			}
		}
		g := rolePolicy(t, rec, role)
		if !reflect.DeepEqual(g["ssm:PutParameter"][0], ssmArn+"/sluis/private") {
			t.Errorf("%s keeps its credentials access: %v", role, g["ssm:PutParameter"])
		}
	}
	h := rolePolicy(t, rec, "http")
	if len(denied("http")) != 0 {
		t.Errorf("http is denied something: %v", denied("http"))
	}
	if got := h["ssm:GetParameter"]; !reflect.DeepEqual(got, []string{ssmArn + "/sluis/private", ssmArn + "/sluis/private/*"}) {
		t.Errorf("http reads %v, want all of /sluis/private (config/* included)", got)
	}
	want := append(append([]string{}, creds...), ssmArn+"/sluis/export", ssmArn+"/sluis/export/*")
	for _, a := range []string{"ssm:PutParameter", "ssm:DeleteParameter"} {
		if !reflect.DeepEqual(h[a], want) {
			t.Errorf("http %s on %v, want %v", a, h[a], want)
		}
	}
}
