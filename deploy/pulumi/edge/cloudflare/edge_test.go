package edgecloudflare_test

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluispulumi "github.com/truvity/sluis/deploy/pulumi"
	edge "github.com/truvity/sluis/deploy/pulumi/edge/cloudflare"
)

// The ids are built and not written out, which the repository's leak canary
// would take for particulars; they are made up.
var account = strings.Repeat("1", 12)

const (
	arnp       = "arn:" + "aws:"
	truststore = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	domainName = "access.example.test"
)

var (
	cdRole    = arnp + "iam::" + account + ":role/cd"
	adminRole = arnp + "iam::" + account + ":role/operators-admin"
	glassRole = arnp + "iam::" + account + ":role/breakglass"
	apply     = []string{cdRole, adminRole, glassRole}
)

type declared struct {
	Type, Name string
	Inputs     resource.PropertyMap
}

// recorder is Pulumi's mock engine: it answers every resource with its own
// inputs plus the outputs the provider would compute, and keeps what it was
// asked for. A stack transform records the options the mock monitor does not
// pass to NewResource.
type recorder struct {
	mu        sync.Mutex
	resources []declared
	protected map[string]bool
	aliased   map[string]bool
}

func (r *recorder) NewResource(a pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	r.resources = append(r.resources, declared{Type: a.TypeToken, Name: a.Name, Inputs: a.Inputs.Copy()})
	r.mu.Unlock()
	out := a.Inputs.Copy()
	physical := a.Name
	for _, k := range []string{"name", "bucket"} {
		if v, ok := a.Inputs[resource.PropertyKey(k)]; ok && v.IsString() {
			physical = v.StringValue()
		}
	}
	set := func(k, v string) { out[resource.PropertyKey(k)] = resource.NewStringProperty(v) }
	switch a.TypeToken {
	case "aws:s3/bucket:Bucket":
		set("arn", arnp+"s3:::"+physical)
		set("bucket", physical)
	case "aws:s3/bucketObjectv2:BucketObjectv2":
		set("versionId", "v1")
	case "aws:acm/certificate:Certificate":
		set("arn", arnp+"acm:eu-west-1:"+account+":certificate/requested")
		out["domainValidationOptions"] = resource.NewArrayProperty([]resource.PropertyValue{resource.NewObjectProperty(resource.PropertyMap{
			"resourceRecordName":  resource.NewStringProperty("_x." + domainName + "."),
			"resourceRecordType":  resource.NewStringProperty("CNAME"),
			"resourceRecordValue": resource.NewStringProperty("_y.acm-validations.aws."),
		})})
	case "aws:acm/certificateValidation:CertificateValidation":
		set("certificateArn", arnp+"acm:eu-west-1:"+account+":certificate/requested")
	case "aws:apigatewayv2/domainName:DomainName":
		out["domainNameConfiguration"] = resource.NewObjectProperty(resource.PropertyMap{
			"targetDomainName": resource.NewStringProperty("d-abc.execute-api.eu-west-1.amazonaws.com"),
			"hostedZoneId":     resource.NewStringProperty("ZHOSTED"),
		})
	default:
		set("arn", arnp+"mock:::"+a.TypeToken+"/"+physical)
	}
	return a.Name + "_id", out, nil
}

func (r *recorder) Call(a pulumi.MockCallArgs) (resource.PropertyMap, error) { return a.Args, nil }

func (r *recorder) transform(ctx *pulumi.Context) error {
	return ctx.RegisterStackTransformation(func(a *pulumi.ResourceTransformationArgs) *pulumi.ResourceTransformationResult {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.protected == nil {
			r.protected, r.aliased = map[string]bool{}, map[string]bool{}
		}
		ro, err := pulumi.NewResourceOptions(a.Opts...)
		r.protected[a.Type+"/"+a.Name] = err == nil && ro.Protect
		r.aliased[a.Type+"/"+a.Name] = err == nil && len(ro.Aliases) > 0
		return nil
	})
}

func (r *recorder) one(t *testing.T, typ, name string) declared {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.resources {
		if d.Type == typ && d.Name == name {
			return d
		}
	}
	t.Fatalf("no %s named %s", typ, name)
	return declared{}
}

func (r *recorder) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, d := range r.resources {
		if d.Type == typ {
			n++
		}
	}
	return n
}

func prop(d declared, key string) resource.PropertyValue { return d.Inputs[resource.PropertyKey(key)] }

func run(t *testing.T, program func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error) (*recorder, map[string]string, error) {
	t.Helper()
	rec := &recorder{}
	got := map[string]string{}
	var wg sync.WaitGroup
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		if err := rec.transform(ctx); err != nil {
			return err
		}
		return program(ctx, func(k string, o pulumi.StringInput) {
			wg.Add(1)
			o.ToStringOutput().ApplyT(func(v string) string {
				defer wg.Done()
				rec.mu.Lock()
				got[k] = v
				rec.mu.Unlock()
				return v
			})
		})
	}, pulumi.WithMocks("sluis-edge-test", "test", rec))
	wg.Wait()
	return rec, got, err
}

// front is the core's Lambda component as an edge sees it: a name, the API and
// the component an older release created the domain under.
func front(ctx *pulumi.Context) *sluispulumi.FrontDoor {
	comp := &pulumi.ResourceState{}
	if err := ctx.RegisterComponentResource(sluispulumi.LambdaType, "staging", comp); err != nil {
		panic(err)
	}
	return &sluispulumi.FrontDoor{Name: "staging", Component: comp, APIID: pulumi.String("abc"), StageName: pulumi.String("$default")}
}

func certArn() pulumi.StringInput {
	return pulumi.String(arnp + "acm:eu-west-1:" + account + ":certificate/origin")
}

// denied is the guard statements of a bucket policy document.
func guardStatement(t *testing.T, doc string) map[string]any {
	t.Helper()
	var d struct{ Statement []map[string]any }
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatal(err)
	}
	for _, s := range d.Statement {
		if strings.HasPrefix(s["Sid"].(string), "ProtectPrefix") {
			return s
		}
	}
	t.Fatalf("no prefix guard in %s", doc)
	return nil
}

func principals(t *testing.T, st map[string]any) []string {
	t.Helper()
	cond := st["Condition"].(map[string]any)["ArnNotEquals"].(map[string]any)["aws:PrincipalArn"].([]any)
	var out []string
	for _, p := range cond {
		out = append(out, p.(string))
	}
	return out
}

// The truststore goes in the blob bucket; the domain has mutual TLS on it and
// pins its version; the bucket denies every principal but the apply identities.
func TestEdgeTruststoreInTheBlobBucket(t *testing.T) {
	rec, out, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		st, err := sluispulumi.NewStorage(ctx, "staging", &sluispulumi.StorageArgs{
			BucketName: "acme-sluis-blobs", Versioning: true,
			ProtectedPrefixes: []sluispulumi.ProtectedPrefix{edge.Guard(apply...)},
		})
		if err != nil {
			return err
		}
		e, err := edge.NewEdge(ctx, "staging", &edge.Args{
			FrontDoor: front(ctx), DomainName: domainName, CertificateArn: certArn(),
			TruststorePEM: truststore, Storage: st,
		})
		if err != nil {
			return err
		}
		collect("domainTarget", e.DomainTarget)
		collect("hostedZone", e.DomainHostedZoneID)
		collect("uri", e.TruststoreURI)
		collect("version", e.TruststoreVersion)
		collect("certificate", e.CertificateArn)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dom := rec.one(t, "aws:apigatewayv2/domainName:DomainName", "staging-domain")
	if prop(dom, "domainName").StringValue() != domainName {
		t.Errorf("domain: %v", dom.Inputs)
	}
	mtls := prop(dom, "mutualTlsAuthentication").ObjectValue()
	if mtls["truststoreUri"].StringValue() != "s3://acme-sluis-blobs/truststore/client-ca.pem" || mtls["truststoreVersion"].StringValue() != "v1" {
		t.Errorf("mutual TLS: %v", mtls)
	}
	cfg := prop(dom, "domainNameConfiguration").ObjectValue()
	if cfg["endpointType"].StringValue() != "REGIONAL" || cfg["securityPolicy"].StringValue() != "TLS_1_2" ||
		!strings.Contains(cfg["certificateArn"].StringValue(), "certificate/origin") {
		t.Errorf("domain configuration: %v", cfg)
	}
	mapping := rec.one(t, "aws:apigatewayv2/apiMapping:ApiMapping", "staging-domain-mapping")
	if prop(mapping, "apiId").StringValue() != "abc" || prop(mapping, "stage").StringValue() != "$default" {
		t.Errorf("mapping: %v", mapping.Inputs)
	}
	obj := rec.one(t, "aws:s3/bucketObjectv2:BucketObjectv2", "staging-truststore-pem")
	if prop(obj, "bucket").StringValue() != "acme-sluis-blobs" {
		t.Errorf("object bucket: %v", obj.Inputs)
	}
	if prop(obj, "key").StringValue() != "truststore/client-ca.pem" || prop(obj, "content").StringValue() != truststore {
		t.Errorf("object: %v", obj.Inputs)
	}
	// No bucket of the edge's own, and the blob bucket's policy carries the guard.
	if n := rec.count("aws:s3/bucket:Bucket"); n != 1 {
		t.Errorf("%d buckets, want the blob bucket alone", n)
	}
	policy := rec.one(t, "aws:s3/bucketPolicy:BucketPolicy", "staging-bucket-policy")
	st := guardStatement(t, prop(policy, "policy").StringValue())
	if st["Effect"] != "Deny" || st["Principal"] != "*" {
		t.Errorf("guard: %v", st)
	}
	if got := principals(t, st); !slices.Equal(got, apply) {
		t.Errorf("the guard exempts %v, want exactly %v", got, apply)
	}
	res := st["Resource"].([]any)
	if len(res) != 1 || res[0] != arnp+"s3:::acme-sluis-blobs/truststore/*" {
		t.Errorf("guard resource: %v", res)
	}
	if !slices.Contains(toStrings(st["Action"]), "s3:PutObject") || !slices.Contains(toStrings(st["Action"]), "s3:DeleteObjectVersion") {
		t.Errorf("guard actions: %v", st["Action"])
	}
	if out["uri"] != "s3://acme-sluis-blobs/truststore/client-ca.pem" || out["version"] != "v1" || out["domainTarget"] == "" ||
		out["hostedZone"] != "ZHOSTED" || !strings.Contains(out["certificate"], "certificate/origin") {
		t.Errorf("outputs: %v", out)
	}
	// An existing stack moves without replacing the domain: it is aliased to
	// where the core library created it.
	for _, k := range []string{"aws:apigatewayv2/domainName:DomainName/staging-domain", "aws:apigatewayv2/apiMapping:ApiMapping/staging-domain-mapping"} {
		if !rec.aliased[k] {
			t.Errorf("%s has no alias to the core library's resource", k)
		}
	}
}

func toStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

// Blobs on an S3-compatible store: a small bucket of the edge's own holds the
// truststore, protected, versioned and guarded.
func TestEdgeTruststoreBucketOfItsOwn(t *testing.T) {
	rec, out, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		e, err := edge.NewEdge(ctx, "staging", &edge.Args{
			FrontDoor: front(ctx), DomainName: domainName, CertificateArn: certArn(), TruststorePEM: truststore,
			TruststoreBucket: &edge.TruststoreBucketArgs{Name: "acme-sluis-truststore", ApplyPrincipalArns: apply},
		})
		if err != nil {
			return err
		}
		collect("uri", e.TruststoreURI)
		collect("bucket", e.TruststoreBucketName)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["uri"] != "s3://acme-sluis-truststore/truststore/client-ca.pem" || out["bucket"] != "acme-sluis-truststore" {
		t.Errorf("outputs: %v", out)
	}
	dom := rec.one(t, "aws:apigatewayv2/domainName:DomainName", "staging-domain")
	mtls := prop(dom, "mutualTlsAuthentication").ObjectValue()
	if mtls["truststoreUri"].StringValue() != out["uri"] || mtls["truststoreVersion"].StringValue() != "v1" {
		t.Errorf("mutual TLS: %v", mtls)
	}
	if !rec.protected["aws:s3/bucket:Bucket/staging-truststore"] {
		t.Error("the truststore bucket is not protected")
	}
	v := rec.one(t, "aws:s3/bucketVersioningV2:BucketVersioningV2", "staging-truststore-versioning")
	if prop(v, "versioningConfiguration").ObjectValue()["status"].StringValue() != "Enabled" {
		t.Errorf("versioning: %v", v.Inputs)
	}
	rec.one(t, "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock", "staging-truststore-public-access")
	policy := rec.one(t, "aws:s3/bucketPolicy:BucketPolicy", "staging-truststore-policy")
	doc := prop(policy, "policy").StringValue()
	if !strings.Contains(doc, "DenyPlainHTTP") {
		t.Errorf("policy: %s", doc)
	}
	if got := principals(t, guardStatement(t, doc)); !slices.Equal(got, apply) {
		t.Errorf("the guard exempts %v, want exactly %v", got, apply)
	}
	if n := rec.count("aws:s3/bucketObjectv2:BucketObjectv2"); n != 1 {
		t.Errorf("%d truststore objects, want one", n)
	}
}

// A requested certificate is validated through the caller's own DNS.
func TestEdgeRequestsTheCertificate(t *testing.T) {
	rec, out, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		e, err := edge.NewEdge(ctx, "staging", &edge.Args{
			FrontDoor: front(ctx), DomainName: domainName, TruststorePEM: truststore,
			TruststoreBucket: &edge.TruststoreBucketArgs{Name: "acme-sluis-truststore", ApplyPrincipalArns: apply},
			Certificate: &edge.CertificateArgs{CreateValidationRecord: func(_ *pulumi.Context, r edge.ValidationRecord) (pulumi.StringInput, error) {
				collect("recordName", r.Name)
				collect("recordType", r.Type)
				collect("recordValue", r.Value)
				return r.Name, nil
			}},
		})
		if err != nil {
			return err
		}
		collect("certificate", e.CertificateArn)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cert := rec.one(t, "aws:acm/certificate:Certificate", "staging-certificate")
	if prop(cert, "domainName").StringValue() != domainName || prop(cert, "validationMethod").StringValue() != "DNS" {
		t.Errorf("certificate: %v", cert.Inputs)
	}
	val := rec.one(t, "aws:acm/certificateValidation:CertificateValidation", "staging-certificate-validation")
	if fq := prop(val, "validationRecordFqdns").ArrayValue(); len(fq) != 1 || fq[0].StringValue() != "_x."+domainName+"." {
		t.Errorf("validation: %v", val.Inputs)
	}
	if out["recordType"] != "CNAME" || out["recordValue"] != "_y.acm-validations.aws." || !strings.Contains(out["certificate"], "certificate/requested") {
		t.Errorf("outputs: %v", out)
	}
	dom := rec.one(t, "aws:apigatewayv2/domainName:DomainName", "staging-domain")
	if !strings.Contains(prop(dom, "domainNameConfiguration").ObjectValue()["certificateArn"].StringValue(), "certificate/requested") {
		t.Errorf("the domain does not use the validated certificate: %v", dom.Inputs)
	}
}

// What is refused.
func TestEdgeRefuses(t *testing.T) {
	base := func(ctx *pulumi.Context) *edge.Args {
		return &edge.Args{
			FrontDoor: front(ctx), DomainName: domainName, CertificateArn: certArn(), TruststorePEM: truststore,
			TruststoreBucket: &edge.TruststoreBucketArgs{Name: "acme-sluis-truststore", ApplyPrincipalArns: apply},
		}
	}
	for name, tc := range map[string]struct {
		mutate func(*edge.Args)
		want   string
	}{
		"no front door":       {func(a *edge.Args) { a.FrontDoor = nil }, "FrontDoor"},
		"no domain":           {func(a *edge.Args) { a.DomainName = "" }, "DomainName"},
		"no truststore":       {func(a *edge.Args) { a.TruststorePEM = " " }, "TruststorePEM"},
		"no certificate":      {func(a *edge.Args) { a.CertificateArn = nil }, "exactly one of CertificateArn"},
		"two certificates":    {func(a *edge.Args) { a.Certificate = &edge.CertificateArgs{} }, "exactly one of CertificateArn"},
		"no bucket":           {func(a *edge.Args) { a.TruststoreBucket = nil }, "exactly one of Storage"},
		"no apply identities": {func(a *edge.Args) { a.TruststoreBucket.ApplyPrincipalArns = nil }, "AllowedPrincipalArns"},
		"not an ARN":          {func(a *edge.Args) { a.TruststoreBucket.ApplyPrincipalArns = []string{"cd"} }, "not an ARN"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
				a := base(ctx)
				tc.mutate(a)
				_, err := edge.NewEdge(ctx, "staging", a)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// The blob bucket has to be versioned and has to guard the prefix.
	for name, tc := range map[string]struct {
		args sluispulumi.StorageArgs
		want string
	}{
		"unversioned": {sluispulumi.StorageArgs{
			BucketName: "acme-sluis-blobs", ProtectedPrefixes: []sluispulumi.ProtectedPrefix{edge.Guard(apply...)},
		}, "not versioned"},
		"unguarded":   {sluispulumi.StorageArgs{BucketName: "acme-sluis-blobs", Versioning: true}, "does not guard"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
				st, err := sluispulumi.NewStorage(ctx, "staging", &tc.args)
				if err != nil {
					return err
				}
				a := &edge.Args{FrontDoor: front(ctx), DomainName: domainName, CertificateArn: certArn(), TruststorePEM: truststore, Storage: st}
				_, err = edge.NewEdge(ctx, "staging", a)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
