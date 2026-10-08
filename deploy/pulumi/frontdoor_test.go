package sluispulumi_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// noDomain is the estate with the API alone: no domain, certificate or
// truststore, which are an edge module's.
func noDomain(a *arp.LambdaArgs) {
	a.API = arp.APIArgs{KeepDefaultEndpoint: a.API.KeepDefaultEndpoint}
}

// The core builds the function, the API and what is behind it, and no front
// door: no custom domain, no mapping, no truststore, no certificate.
func TestCoreWithoutAnEdgeBuildsNoFrontDoor(t *testing.T) {
	rec, out := mustLambda(t, estate{orgs: []string{"acme"}, mutate: noDomain})
	for _, typ := range []string{
		"aws:apigatewayv2/domainName:DomainName", "aws:apigatewayv2/apiMapping:ApiMapping",
		"aws:s3/bucket:Bucket", "aws:s3/bucketObjectv2:BucketObjectv2", "aws:s3/bucketVersioningV2:BucketVersioningV2",
		"aws:s3/bucketPolicy:BucketPolicy", "aws:acm/certificate:Certificate",
	} {
		if n := len(rec.ofType(typ)); n != 0 {
			t.Errorf("%d %s declared; a front door is the edge's", n, typ)
		}
	}
	for _, typ := range []string{
		fnType, "aws:apigatewayv2/api:Api", "aws:apigatewayv2/integration:Integration", "aws:apigatewayv2/route:Route",
		"aws:apigatewayv2/stage:Stage", "aws:lambda/permission:Permission", policyType, "aws:scheduler/schedule:Schedule",
	} {
		if len(rec.ofType(typ)) == 0 {
			t.Errorf("no %s declared", typ)
		}
	}
	api := rec.one(t, "aws:apigatewayv2/api:Api", "staging-api")
	if !prop(api, "disableExecuteApiEndpoint").BoolValue() {
		t.Errorf("the default endpoint is on with no front door in front of it by default: %v", api.Inputs)
	}
	if out["domainTarget"] != "" || out["domainHostedZoneID"] != "" || out["truststoreUri"] != "" || out["apiUrl"] == "" {
		t.Errorf("outputs: %v", out)
	}
}

// The function, the storage and the IAM are the same with and without the
// deprecated domain inputs: what an edge is added to does not change.
func TestCoreFunctionAndIAMDoNotDependOnTheDomain(t *testing.T) {
	with, _ := mustLambda(t, estate{orgs: []string{"acme"}})
	without, _ := mustLambda(t, estate{orgs: []string{"acme"}, mutate: noDomain})
	for _, typ := range []string{fnType, policyType, "aws:iam/role:Role", "aws:lambda/layerVersion:LayerVersion", "aws:kms/key:Key"} {
		a, b := with.ofType(typ), without.ofType(typ)
		// Resources register concurrently: compare by name, not by order.
		sort.SliceStable(a, func(i, j int) bool { return a[i].Name < a[j].Name })
		sort.SliceStable(b, func(i, j int) bool { return b[i].Name < b[j].Name })
		if len(a) != len(b) {
			t.Errorf("%s: %d with the domain, %d without", typ, len(a), len(b))
			continue
		}
		for i := range a {
			if a[i].Name != b[i].Name || !a[i].Inputs.DeepEquals(b[i].Inputs) {
				t.Errorf("%s %s differs with the domain inputs", typ, a[i].Name)
			}
		}
	}
}

// KeepDefaultEndpoint is the core's, because it is the API's.
func TestKeepDefaultEndpointWithoutADomain(t *testing.T) {
	rec, _ := mustLambda(t, estate{keepDefaultEndpoint: true, mutate: noDomain})
	if prop(rec.one(t, "aws:apigatewayv2/api:Api", "staging-api"), "disableExecuteApiEndpoint").BoolValue() {
		t.Error("KeepDefaultEndpoint did not keep it")
	}
}

// The deprecated inputs still build the domain, mutual TLS and the truststore
// bucket exactly as before (the first test of this file's neighbours reads them
// in detail); half of them are a mistake.
func TestDeprecatedDomainInputsAreAllOrNone(t *testing.T) {
	rec, out := mustLambda(t, estate{})
	if len(rec.ofType("aws:apigatewayv2/domainName:DomainName")) != 1 || out["truststoreUri"] != "s3://acme-sluis-truststore/truststore/client-ca.pem" {
		t.Errorf("the deprecated inputs no longer build the domain: %v", out)
	}
	for name, mutate := range map[string]func(*arp.LambdaArgs){
		"only a bucket":      func(a *arp.LambdaArgs) { noDomain(a); a.API.TruststoreBucketName = "acme-sluis-truststore" },
		"only a domain":      func(a *arp.LambdaArgs) { noDomain(a); a.API.DomainName = "access.example.test" },
		"only a certificate": func(a *arp.LambdaArgs) { noDomain(a); a.API.CertificateArn = pulumi.String("arn") },
		"only a truststore":  func(a *arp.LambdaArgs) { noDomain(a); a.API.TruststorePEM = truststore },
	} {
		_, _, err := buildLambda(t, estate{mutate: mutate})
		if err == nil || !strings.Contains(err.Error(), "required and empty") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// The front door an edge takes is the API and its stage.
func TestFrontDoor(t *testing.T) {
	var name string
	_, _, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		l, err := arp.NewLambda(ctx, "staging", estate{mutate: noDomain}.args(t))
		if err != nil {
			return err
		}
		fd := l.FrontDoor()
		name = fd.Name
		if fd.Component != pulumi.Resource(l) {
			t.Error("the component is not the Lambda")
		}
		collect("id", fd.APIID)
		collect("stage", fd.StageName)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "staging" {
		t.Errorf("name %q", name)
	}
}

// ProtectedPrefixes put a Deny on writes under a prefix, for everyone but the
// listed identities, in the bucket's one policy.
func TestStorageProtectedPrefix(t *testing.T) {
	cd := arnp + "iam::" + account + ":role/cd"
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		_, err := arp.NewStorage(ctx, "staging", &arp.StorageArgs{
			BucketName: bucket, Versioning: true,
			ProtectedPrefixes: []arp.ProtectedPrefix{{Prefix: "truststore/", AllowedPrincipalArns: []string{cd}}},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	st := statements(t, prop(rec.one(t, "aws:s3/bucketPolicy:BucketPolicy", "staging-bucket-policy"), "policy").StringValue())
	if len(st) != 2 || st[0]["Sid"] != "DenyPlainHTTP" || st[1]["Effect"] != "Deny" {
		t.Fatalf("statements: %v", st)
	}
	if r := strs(st[1]["Resource"]); len(r) != 1 || r[0] != arnp+"s3:::"+bucket+"/truststore/*" {
		t.Errorf("resource: %v", r)
	}
	cond := st[1]["Condition"].(map[string]any)["ArnNotEquals"].(map[string]any)["aws:PrincipalArn"]
	if p := strs(cond); len(p) != 1 || p[0] != cd {
		t.Errorf("principals: %v", cond)
	}
}

func TestStorageProtectedPrefixIsRefusedWhenItGuardsNothing(t *testing.T) {
	cd := arnp + "iam::" + account + ":role/cd"
	for name, p := range map[string][]arp.ProtectedPrefix{
		"no prefix":         {{AllowedPrincipalArns: []string{cd}}},
		"no trailing slash": {{Prefix: "truststore", AllowedPrincipalArns: []string{cd}}},
		"leading slash":     {{Prefix: "/truststore/", AllowedPrincipalArns: []string{cd}}},
		"nobody allowed":    {{Prefix: "truststore/"}},
		"not an ARN":        {{Prefix: "truststore/", AllowedPrincipalArns: []string{"cd"}}},
		"twice":             {{Prefix: "a/", AllowedPrincipalArns: []string{cd}}, {Prefix: "a/", AllowedPrincipalArns: []string{cd}}},
	} {
		_, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
			_, err := arp.NewStorage(ctx, "staging", &arp.StorageArgs{BucketName: bucket, ProtectedPrefixes: p})
			return err
		})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
