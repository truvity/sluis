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
	files := map[string]string{"bootstrap": "#!binary\n", "config/sluis.yaml": "stale: true\n"}
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
	a := &arp.LambdaArgs{
		Region: region, AccountID: account,
		Package: e.pkg, Config: e.config, Catalogues: e.catalogues, CataloguePaths: e.cataloguePaths,
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
		collect("httpFunctionArn", l.HTTPFunctionArn)
		collect("githubFunctionArn", l.GitHubFunctionArn)
		collect("slackFunctionArn", l.SlackFunctionArn)
		collect("apiUrl", l.APIURL)
		collect("domainTarget", l.DomainTarget)
		collect("domainHostedZoneID", l.DomainHostedZoneID)
		collect("truststoreUri", l.TruststoreURI)
		collect("exportReadPolicy", l.ExportReadPolicyJSON)
		collect("schedulerRoleArn", l.SchedulerRoleArn)
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
		if env["SLUIS_ROLE"].StringValue() != r || env["SLUIS_CONFIG_FILE"].StringValue() != "/var/task/config/sluis.yaml" {
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
	if got := httpG["kms:Sign"]; !reflect.DeepEqual(got, []string{out["signingKeyArn"]}) {
		t.Errorf("http signs with %v, want %s", got, out["signingKeyArn"])
	}
	for _, d := range rec.ofType(policyType) {
		for _, s := range statements(t, prop(d, "policy").StringValue()) {
			for _, r := range strs(s["Resource"]) {
				if r == "*" {
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

	// A signing key and no sealer.
	keys := rec.ofType("aws:kms/key:Key")
	if len(keys) != 1 || keys[0].Name != "kernel-signing-key" {
		t.Fatalf("keys: %v", rec.names())
	}
	k := keys[0]
	if prop(k, "keyUsage").StringValue() != "SIGN_VERIFY" || prop(k, "customerMasterKeySpec").StringValue() != "ECC_NIST_P384" {
		t.Errorf("signing key: %v", k.Inputs)
	}
	if out["signingKeyAlias"] != "alias/sluis-signing" {
		t.Errorf("alias %q", out["signingKeyAlias"])
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
		orgs: []string{"truvity", "trust-form"}, workspaces: []string{"T0TRUVITY"}, rate: "rate(2 minutes)", telemetry: true,
		catalogues: map[string]string{"github-apps.yaml": "apps: []\n"},
		mutate:     func(a *arp.LambdaArgs) { a.API.DomainName = "access.truvity.xyz" },
	})
	shape(t, rec, out, "access.truvity.xyz")

	want := map[string]string{
		"sluis-github-truvity":    `{"kind":"tick","target":"truvity"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-github",
		"sluis-github-trust-form": `{"kind":"tick","target":"trust-form"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-github",
		"sluis-slack-T0TRUVITY":   `{"kind":"tick","target":"T0TRUVITY"} ` + arnp + "lambda:eu-west-1:" + account + ":function:sluis-slack",
	}
	if got := schedules(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("schedules:\n got %v\nwant %v", got, want)
	}
	for _, s := range rec.ofType("aws:scheduler/schedule:Schedule") {
		if prop(s, "scheduleExpression").StringValue() != "rate(2 minutes)" {
			t.Errorf("%s: %v", s.Name, s.Inputs)
		}
		if prop(s, "target").ObjectValue()["roleArn"].StringValue() != out["schedulerRoleArn"] {
			t.Errorf("%s: not the scheduler's role", s.Name)
		}
	}
	// The scheduler's role invokes the two controllers and nothing else.
	sp := grants(statements(t, prop(rec.one(t, policyType, "kernel-scheduler-policy"), "policy").StringValue()))
	if len(sp) != 1 || len(sp["lambda:InvokeFunction"]) != 2 {
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
	for _, p := range []string{"bootstrap", "config/sluis.yaml", "config/github-apps.yaml"} {
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
			a.API.DomainName = "access.excavador.xyz"
			a.SigningKeyAlias = ""
		},
	})
	shape(t, rec, out, "access.excavador.xyz")
	if got := schedules(t, rec); len(got) != 1 {
		t.Errorf("schedules: %v", got)
	}
	for _, s := range rec.ofType("aws:scheduler/schedule:Schedule") {
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
	if g := rolePolicy(t, rec, "http"); len(g["kms:Sign"]) != 1 || len(g["kms:GetPublicKey"]) != 1 {
		t.Errorf("http grants %v", g)
	}
	dom := rec.one(t, "aws:apigatewayv2/domainName:DomainName", "kernel-domain")
	if prop(dom, "domainName").StringValue() != "access.excavador.xyz" || !prop(dom, "mutualTlsAuthentication").IsObject() {
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
	if base["config/sluis.yaml"] != "a: 1\n" {
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
		"no queue":          func(a *arp.LambdaArgs) { a.AuditQueueArn = nil },
		"no storage":        func(a *arp.LambdaArgs) { a.Storage = nil },
		"no state":          func(a *arp.LambdaArgs) { a.State = nil },
		"no certificate":    func(a *arp.LambdaArgs) { a.API.CertificateArn = nil },
		"no truststore":     func(a *arp.LambdaArgs) { a.API.TruststorePEM = "" },
		"no domain":         func(a *arp.LambdaArgs) { a.API.DomainName = "" },
		"bad alias":         func(a *arp.LambdaArgs) { a.SigningKeyAlias = "signing" },
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
			Storage: store.Grant(), SigningKeyArn: pulumi.String(signing),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	pol := func(n string) map[string][]string {
		return grants(statements(t, prop(rec.one(t, "aws:iam/policy:Policy", n), "policy").StringValue()))
	}
	if g := pol("kernel-sluis-serve-policy"); len(g["kms:Sign"]) != 1 {
		t.Errorf("serve: %v", g)
	}
	if g := pol("kernel-sluis-github-policy"); len(g["kms:Sign"]) != 0 || len(g["kms:Decrypt"]) != 0 {
		t.Errorf("github: %v", g)
	}
}
