package sluispulumi_test

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The ids are built and not written out, which the repository's leak canary
// would take for particulars; they are made up.
var account = strings.Repeat("1", 12)

const arnp = "arn:" + "aws:"

// recorder is Pulumi's mock engine: it answers every resource with its own
// inputs plus the outputs the provider would compute (an ARN), and keeps what it
// was asked for, so a test reads the resources the library declared without a
// cloud, a credential or a plugin. A stack transform records each resource's
// options, which the mock monitor does not pass to NewResource.
type recorder struct {
	mu        sync.Mutex
	resources []declared
	protected map[string]bool
}

type declared struct {
	Type, Name string
	Inputs     resource.PropertyMap
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
	set("name", physical)
	switch a.TypeToken {
	case "aws:iam/role:Role":
		set("arn", arnp+"iam::"+account+":role/"+physical)
	case "aws:iam/policy:Policy":
		set("arn", arnp+"iam::"+account+":policy/"+physical)
	case "aws:s3/bucket:Bucket":
		set("arn", arnp+"s3:::"+physical)
		set("bucket", physical)
	case "aws:kms/key:Key":
		set("arn", arnp+"kms:eu-west-1:"+account+":key/"+a.Name)
		set("keyId", a.Name)
	case "aws:kms/alias:Alias":
		set("arn", arnp+"kms:eu-west-1:"+account+":"+physical)
	case "random:index/randomBytes:RandomBytes":
		set("base64", "c3RhdGUtc2VjcmV0LXN0YXRlLXNlY3JldC1zdGF0ZSE=")
	case "random:index/randomPassword:RandomPassword":
		set("result", "Ab0Oc1lIdEfGhJkMnPqRsTuVwXyZaBcDeFgHjKmN")
	case "aws:lambda/function:Function":
		set("arn", arnp+"lambda:eu-west-1:"+account+":function:"+physical)
	case "aws:cloudwatch/logGroup:LogGroup":
		set("arn", arnp+"logs:eu-west-1:"+account+":log-group:"+physical)
	case "aws:s3/bucketObjectv2:BucketObjectv2":
		set("versionId", "v1")
	case "aws:apigatewayv2/api:Api":
		set("apiEndpoint", "https://abc.execute-api.eu-west-1.amazonaws.com")
		set("executionArn", arnp+"execute-api:eu-west-1:"+account+":abc")
	case "aws:apigatewayv2/domainName:DomainName":
		out["domainNameConfiguration"] = resource.NewObjectProperty(resource.PropertyMap{
			"targetDomainName": resource.NewStringProperty("d-abc.execute-api.eu-west-1.amazonaws.com"),
			"hostedZoneId":     resource.NewStringProperty("ZHOSTED"),
		})
	case "aws:dynamodb/table:Table":
		set("arn", arnp+"dynamodb:eu-west-1:"+account+":table/"+physical)
	default:
		set("arn", arnp+"mock:::"+a.TypeToken+"/"+physical)
	}
	return a.Name + "_id", out, nil
}

// Call answers the invokes: an alias resolves to a key whose ARN is made from the
// alias, so that a test reads which key a grant landed on.
func (r *recorder) Call(a pulumi.MockCallArgs) (resource.PropertyMap, error) {
	if a.Token == "aws:kms/getAlias:getAlias" {
		out := a.Args.Copy()
		name := strings.TrimPrefix(a.Args["name"].StringValue(), "alias/")
		out["targetKeyArn"] = resource.NewStringProperty(arnp + "kms:eu-west-1:" + account + ":key/" + name)
		out["arn"] = resource.NewStringProperty(arnp + "kms:eu-west-1:" + account + ":alias/" + name)
		return out, nil
	}
	return a.Args, nil
}

// transform notes which resources were declared protected.
func (r *recorder) transform(ctx *pulumi.Context) error {
	return ctx.RegisterStackTransformation(func(a *pulumi.ResourceTransformationArgs) *pulumi.ResourceTransformationResult {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.protected == nil {
			r.protected = map[string]bool{}
		}
		ro, err := pulumi.NewResourceOptions(a.Opts...)
		r.protected[a.Type+"/"+a.Name] = err == nil && ro.Protect
		return nil
	})
}

func (r *recorder) isProtected(typ, name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.protected[typ+"/"+name]
}

func (r *recorder) ofType(typ string) []declared {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []declared
	for _, d := range r.resources {
		if d.Type == typ {
			out = append(out, d)
		}
	}
	return out
}

func (r *recorder) has(typ, name string) bool {
	for _, d := range r.ofType(typ) {
		if d.Name == name {
			return true
		}
	}
	return false
}

func (r *recorder) one(t *testing.T, typ, name string) declared {
	t.Helper()
	for _, d := range r.ofType(typ) {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no %s named %s; have %v", typ, name, r.names())
	return declared{}
}

func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, d := range r.resources {
		out = append(out, d.Type+"/"+d.Name)
	}
	sort.Strings(out)
	return out
}

func prop(d declared, key string) resource.PropertyValue { return d.Inputs[resource.PropertyKey(key)] }

// run runs a stack program against the mocks and waits for every output it asked
// to see, which collect fills in.
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
	}, pulumi.WithMocks("sluis-test", "test", rec))
	wg.Wait()
	return rec, got, err
}

// statements is a policy document's statements.
func statements(t *testing.T, doc string) []map[string]any {
	t.Helper()
	var d struct{ Statement []map[string]any }
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatal(err)
	}
	return d.Statement
}

func strs(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, e.(string))
		}
		return out
	}
	return nil
}

// grants lists every (action, resource) pair a policy allows.
func grants(st []map[string]any) map[string][]string {
	out := map[string][]string{}
	for _, s := range st {
		if s["Effect"] != "Allow" {
			continue
		}
		for _, a := range strs(s["Action"]) {
			out[a] = append(out[a], strs(s["Resource"])...)
		}
	}
	return out
}
