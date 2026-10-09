package sluispulumi_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	yaml "go.yaml.in/yaml/v3"

	policyconfig "github.com/truvity/policy/config"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

const (
	bucket  = "acme-sluis"
	table   = "acme-sluis"
	cluster = "acme"
)

type opts struct {
	versioning bool
	tableKey   pulumi.StringInput
	noState    bool
	boundary   string
	serveSA    string
	instance   string
	paramKey   string
}

// stack is the three components wired the way a stack would wire them.
func stack(t *testing.T, o opts) (*recorder, map[string]string, error) {
	t.Helper()
	if o.boundary == "" {
		o.boundary = arnp + "iam::" + account + ":policy/boundary"
	}
	if o.serveSA == "" {
		o.serveSA = "sluis"
	}
	return run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		store, err := arp.NewStorage(ctx, "staging", &arp.StorageArgs{BucketName: bucket, Versioning: o.versioning})
		if err != nil {
			return err
		}
		id := &arp.KubernetesIdentityArgs{
			ClusterName: cluster, ClusterArn: arnp + "eks:eu-west-1:" + account + ":cluster/" + cluster, AccountID: account,
			Namespace: "sluis", PermissionsBoundaryArn: o.boundary,
			ServiceAccount: o.serveSA, Storage: store.Grant(),
			Region: "eu-west-1", Instance: o.instance, ParameterKeyArn: o.paramKey,
		}
		collect("bucketName", store.BucketName)
		collect("bucketArn", store.BucketArn)
		if !o.noState {
			st, err := arp.NewState(ctx, "staging", &arp.StateArgs{TableName: table, KeyArn: o.tableKey})
			if err != nil {
				return err
			}
			id.State = st.Grant()
			collect("tableName", st.TableName)
			collect("tableArn", st.TableArn)
		}
		ids, err := arp.NewKubernetesIdentity(ctx, "staging", id)
		if err != nil {
			return err
		}
		collect("roleArn", ids.RoleArn)
		collect("roleName", ids.RoleName)
		return nil
	})
}

func mustStack(t *testing.T, o opts) (*recorder, map[string]string) {
	t.Helper()
	rec, out, err := stack(t, o)
	if err != nil {
		t.Fatal(err)
	}
	return rec, out
}

func TestTheBucketIsEncryptedClosedAndProtected(t *testing.T) {
	rec, out := mustStack(t, opts{})
	b := rec.one(t, "aws:s3/bucket:Bucket", "staging-bucket")
	if prop(b, "bucket").StringValue() != bucket || prop(b, "forceDestroy").IsBool() && prop(b, "forceDestroy").BoolValue() {
		t.Errorf("bucket inputs: %v", b.Inputs)
	}
	if !rec.isProtected("aws:s3/bucket:Bucket", "staging-bucket") {
		t.Error("the bucket is not protected")
	}
	if out["bucketName"] != bucket || out["bucketArn"] != arnp+"s3:::"+bucket {
		t.Errorf("outputs: %v", out)
	}
	sse := rec.one(t, "aws:s3/bucketServerSideEncryptionConfigurationV2:BucketServerSideEncryptionConfigurationV2", "staging-bucket-encryption")
	rule := prop(sse, "rules").ArrayValue()[0].ObjectValue()
	if rule["applyServerSideEncryptionByDefault"].ObjectValue()["sseAlgorithm"].StringValue() != "AES256" {
		t.Errorf("encryption: %v", rule)
	}
	pab := rec.one(t, "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock", "staging-bucket-public-access")
	for _, k := range []string{"blockPublicAcls", "blockPublicPolicy", "ignorePublicAcls", "restrictPublicBuckets"} {
		if !prop(pab, k).BoolValue() {
			t.Errorf("public access block: %s is not set", k)
		}
	}
}

func TestTheBucketPolicyDeniesPlainHTTP(t *testing.T) {
	rec, _ := mustStack(t, opts{})
	p := rec.one(t, "aws:s3/bucketPolicy:BucketPolicy", "staging-bucket-policy")
	st := statements(t, prop(p, "policy").StringValue())
	if len(st) != 1 || st[0]["Effect"] != "Deny" || st[0]["Principal"] != "*" || st[0]["Action"] != "s3:*" {
		t.Fatalf("policy: %v", st)
	}
	want := []string{arnp + "s3:::" + bucket, arnp + "s3:::" + bucket + "/*"}
	if got := strs(st[0]["Resource"]); !reflect.DeepEqual(got, want) {
		t.Errorf("resources %v, want %v", got, want)
	}
	cond := st[0]["Condition"].(map[string]any)["Bool"].(map[string]any)
	if cond["aws:SecureTransport"] != "false" {
		t.Errorf("condition: %v", cond)
	}
}

func TestVersioningIsOptional(t *testing.T) {
	rec, _ := mustStack(t, opts{})
	if len(rec.ofType("aws:s3/bucketVersioningV2:BucketVersioningV2")) != 0 {
		t.Error("versioning was turned on unasked")
	}
	rec, _ = mustStack(t, opts{versioning: true})
	v := rec.one(t, "aws:s3/bucketVersioningV2:BucketVersioningV2", "staging-bucket-versioning")
	if prop(v, "versioningConfiguration").ObjectValue()["status"].StringValue() != "Enabled" {
		t.Errorf("versioning: %v", v.Inputs)
	}
}

func TestTheTableIsTheAdaptersAndProtected(t *testing.T) {
	rec, out := mustStack(t, opts{})
	d := rec.one(t, "aws:dynamodb/table:Table", "staging-table")
	if prop(d, "name").StringValue() != table || prop(d, "billingMode").StringValue() != "PAY_PER_REQUEST" ||
		prop(d, "hashKey").StringValue() != "pk" || prop(d, "rangeKey").StringValue() != "sk" {
		t.Errorf("table inputs: %v", d.Inputs)
	}
	attrs := map[string]string{}
	for _, a := range prop(d, "attributes").ArrayValue() {
		attrs[a.ObjectValue()["name"].StringValue()] = a.ObjectValue()["type"].StringValue()
	}
	if !reflect.DeepEqual(attrs, map[string]string{"pk": "S", "sk": "S"}) {
		t.Errorf("attributes %v: only the keys are declared, and both are strings", attrs)
	}
	ttl := prop(d, "ttl").ObjectValue()
	if ttl["attributeName"].StringValue() != "expires" || !ttl["enabled"].BoolValue() {
		t.Errorf("ttl: %v", ttl)
	}
	if !prop(d, "pointInTimeRecovery").ObjectValue()["enabled"].BoolValue() || !prop(d, "deletionProtectionEnabled").BoolValue() {
		t.Errorf("recovery / deletion protection: %v", d.Inputs)
	}
	if prop(d, "serverSideEncryption").HasValue() {
		t.Errorf("a key was set unasked: %v", prop(d, "serverSideEncryption"))
	}
	if !rec.isProtected("aws:dynamodb/table:Table", "staging-table") {
		t.Error("the table is not protected")
	}
	if out["tableName"] != table || out["tableArn"] != arnp+"dynamodb:eu-west-1:"+account+":table/"+table {
		t.Errorf("outputs: %v", out)
	}
}

func TestATableTakesACustomerManagedKeyAndTheRolesMayUseItOnlyThroughDynamoDB(t *testing.T) {
	cmk := arnp + "kms:eu-west-1:" + account + ":key/state"
	rec, _ := mustStack(t, opts{tableKey: pulumi.String(cmk)})
	d := rec.one(t, "aws:dynamodb/table:Table", "staging-table")
	sse := prop(d, "serverSideEncryption").ObjectValue()
	if !sse["enabled"].BoolValue() || sse["kmsKeyArn"].StringValue() != cmk {
		t.Errorf("sse: %v", sse)
	}
	p := rec.one(t, "aws:iam/policy:Policy", "staging-sluis-policy")
	var found bool
	for _, s := range statements(t, prop(p, "policy").StringValue()) {
		if s["Sid"] == "SluisStateKey" {
			found = true
			if !reflect.DeepEqual(strs(s["Resource"]), []string{cmk}) {
				t.Errorf("resource: %v", s["Resource"])
			}
			via := s["Condition"].(map[string]any)["StringLike"].(map[string]any)["kms:ViaService"]
			if via != "dynamodb.*.amazonaws.com" {
				t.Errorf("condition: %v", s["Condition"])
			}
		}
	}
	if !found {
		t.Error("no grant on the table's key")
	}
}

func TestTheOneRoleHasItsPolicyAttachmentAndAssociation(t *testing.T) {
	rec, out := mustStack(t, opts{})
	role := "staging-sluis"
	r := rec.one(t, "aws:iam/role:Role", role+"-role")
	if prop(r, "name").StringValue() != role || prop(r, "permissionsBoundary").StringValue() != arnp+"iam::"+account+":policy/boundary" {
		t.Errorf("%s: %v", role, r.Inputs)
	}
	pol := rec.one(t, "aws:iam/policy:Policy", role+"-policy")
	if prop(pol, "name").StringValue() != role {
		t.Errorf("policy: %v", pol.Inputs)
	}
	att := rec.one(t, "aws:iam/rolePolicyAttachment:RolePolicyAttachment", role+"-attachment")
	if prop(att, "role").StringValue() != role || prop(att, "policyArn").StringValue() != arnp+"iam::"+account+":policy/"+role {
		t.Errorf("attachment: %v", att.Inputs)
	}
	pia := rec.one(t, "aws:eks/podIdentityAssociation:PodIdentityAssociation", role+"-pia")
	if prop(pia, "clusterName").StringValue() != cluster || prop(pia, "namespace").StringValue() != "sluis" ||
		prop(pia, "serviceAccount").StringValue() != "sluis" || prop(pia, "roleArn").StringValue() != arnp+"iam::"+account+":role/"+role {
		t.Errorf("association: %v", pia.Inputs)
	}
	if n := len(rec.ofType("aws:eks/podIdentityAssociation:PodIdentityAssociation")); n != 1 {
		t.Errorf("%d associations, want the one pod's", n)
	}
	if n := len(rec.ofType("aws:iam/role:Role")); n != 1 {
		t.Errorf("%d roles, want one: the controllers run in the same pod", n)
	}
	if out["roleArn"] != arnp+"iam::"+account+":role/staging-sluis" || out["roleName"] != "staging-sluis" {
		t.Errorf("outputs: %v", out)
	}
}

func TestTheTrustPolicyNamesTheClusterTheNamespaceAndOneServiceAccount(t *testing.T) {
	rec, _ := mustStack(t, opts{})
	r := rec.one(t, "aws:iam/role:Role", "staging-sluis-role")
	st := statements(t, prop(r, "assumeRolePolicy").StringValue())
	if len(st) != 1 || st[0]["Effect"] != "Allow" {
		t.Fatalf("trust: %v", st)
	}
	if st[0]["Principal"].(map[string]any)["Service"] != "pods.eks.amazonaws.com" {
		t.Errorf("principal: %v", st[0]["Principal"])
	}
	if got := strs(st[0]["Action"]); !reflect.DeepEqual(got, []string{"sts:AssumeRole", "sts:TagSession"}) {
		t.Errorf("actions %v", got)
	}
	c := st[0]["Condition"].(map[string]any)
	eq := c["StringEquals"].(map[string]any)
	if eq["aws:SourceAccount"] != account || eq["aws:RequestTag/kubernetes-namespace"] != "sluis" ||
		eq["aws:RequestTag/kubernetes-service-account"] != "sluis" {
		t.Errorf("StringEquals: %v", eq)
	}
	if c["ArnEquals"].(map[string]any)["aws:SourceArn"] != arnp+"eks:eu-west-1:"+account+":cluster/"+cluster {
		t.Errorf("ArnEquals: %v", c["ArnEquals"])
	}
}

// What a role may do is asserted whole: an extra action fails here.
func TestEachRoleGetsExactlyTheStorageAndTheTable(t *testing.T) {
	rec, _ := mustStack(t, opts{})
	bucketArn := arnp + "s3:::" + bucket
	tableArn := arnp + "dynamodb:eu-west-1:" + account + ":table/" + table
	want := map[string][]string{
		"s3:GetObject":           {bucketArn + "/*"},
		"s3:PutObject":           {bucketArn + "/*"},
		"s3:DeleteObject":        {bucketArn + "/*"},
		"s3:ListBucket":          {bucketArn},
		"dynamodb:GetItem":       {tableArn},
		"dynamodb:PutItem":       {tableArn},
		"dynamodb:UpdateItem":    {tableArn},
		"dynamodb:DeleteItem":    {tableArn},
		"dynamodb:Query":         {tableArn},
		"dynamodb:Scan":          {tableArn},
		"dynamodb:DescribeTable": {tableArn},
	}
	p := rec.one(t, "aws:iam/policy:Policy", "staging-sluis-policy")
	got := grants(statements(t, prop(p, "policy").StringValue()))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("grants:\n got %v\nwant %v", got, want)
	}
}

func TestWithoutStateNoRoleCarriesADynamoDBGrant(t *testing.T) {
	rec, _ := mustStack(t, opts{noState: true})
	p := rec.one(t, "aws:iam/policy:Policy", "staging-sluis-policy")
	doc := prop(p, "policy").StringValue()
	if strings.Contains(doc, "dynamodb") {
		t.Errorf("a DynamoDB grant without a table: %s", doc)
	}
	if got := len(statements(t, doc)); got != 2 {
		t.Errorf("%d statements, want the 2 of the storage", got)
	}
	if len(rec.ofType("aws:dynamodb/table:Table")) != 0 {
		t.Error("a table was made")
	}
}

func TestNoGrantIsOnAWildcardResourceOrAWildcardAction(t *testing.T) {
	rec, _ := mustStack(t, opts{})
	for _, d := range rec.ofType("aws:iam/policy:Policy") {
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
}

// With Instance the pod's role has the Lambda role's SSM grants: the same rules,
// scoped to /sluis/<instance>/ and nothing above it, and no wildcard but a
// trailing /*.
func TestWithAnInstanceTheRoleHasTheSSMGrantsOfTheLambdaRoleUnderItsRoot(t *testing.T) {
	key := arnp + "kms:eu-west-1:" + account + ":key/params"
	rec, _ := mustStack(t, opts{instance: "staging", paramKey: key})
	p := rec.one(t, "aws:iam/policy:Policy", "staging-sluis-policy")
	g := grants(statements(t, prop(p, "policy").StringValue()))
	ssmArn := arnp + "ssm:eu-west-1:" + account + ":parameter"
	v4creds := []string{ssmArn + "/sluis/staging/internal/credentials", ssmArn + "/sluis/staging/internal/credentials/*"}
	v4cfg := []string{ssmArn + "/sluis/staging/internal/config", ssmArn + "/sluis/staging/internal/config/*"}
	external := []string{ssmArn + "/sluis/staging/external", ssmArn + "/sluis/staging/external/*"}
	writes := append(append([]string{}, v4creds...), external...)
	if got := g["ssm:PutParameter"]; !reflect.DeepEqual(sortedCopy(got), sortedCopy(writes)) {
		t.Errorf("writes %v, want %v", got, writes)
	}
	if got := g["ssm:GetParametersByPath"]; !reflect.DeepEqual(sortedCopy(got), sortedCopy(append(append([]string{}, writes...), v4cfg...))) {
		t.Errorf("reads %v", got)
	}
	for a, res := range g {
		for _, r := range res {
			path, ok := strings.CutPrefix(r, ssmArn)
			if !ok {
				continue
			}
			if !strings.HasPrefix(strings.TrimSuffix(path, "/*"), "/sluis/staging/") || strings.Contains(strings.TrimSuffix(path, "/*"), "*") {
				t.Errorf("%s: %s is outside /sluis/staging/ or has a wildcard", a, r)
			}
		}
	}
	n := 0
	for _, s := range statements(t, prop(p, "policy").StringValue()) {
		if s["Sid"] == "SluisParameterKey" {
			n++
			if s["Condition"].(map[string]any)["StringLike"].(map[string]any)["kms:ViaService"] != "ssm.*.amazonaws.com" {
				t.Errorf("the parameter key is not through SSM only: %v", s)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d parameter-key statements", n)
	}
	// Without an instance, no SSM at all.
	rec, _ = mustStack(t, opts{})
	doc := prop(rec.one(t, "aws:iam/policy:Policy", "staging-sluis-policy"), "policy").StringValue()
	if strings.Contains(doc, "ssm:") {
		t.Errorf("an SSM grant without an Instance: %s", doc)
	}
	if _, _, err := stack(t, opts{instance: "private"}); err == nil {
		t.Error("an instance named private was accepted")
	}
}

func TestTheRequiredInputsAreRequired(t *testing.T) {
	_, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		_, err := arp.NewKubernetesIdentity(ctx, "x", &arp.KubernetesIdentityArgs{})
		return err
	})
	if err == nil {
		t.Fatal("empty arguments were accepted")
	}
	for _, want := range []string{"ClusterName", "ClusterArn", "AccountID", "Namespace", "ServiceAccount"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %s: %v", want, err)
		}
	}
	_, _, err = run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		_, err := arp.NewState(ctx, "x", &arp.StateArgs{})
		return err
	})
	if err == nil {
		t.Error("a table with no name was accepted")
	}
}

func TestTheRolePrefixDefaultsToTheComponentsName(t *testing.T) {
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		s, err := arp.NewStorage(ctx, "ar", &arp.StorageArgs{BucketName: bucket})
		if err != nil {
			return err
		}
		_, err = arp.NewKubernetesIdentity(ctx, "ar", &arp.KubernetesIdentityArgs{
			ClusterName: cluster, ClusterArn: "c", AccountID: account, Namespace: "n",
			ServiceAccount: "s", Storage: s.Grant(),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r := rec.one(t, "aws:iam/role:Role", "ar-sluis-role")
	if prop(r, "name").StringValue() != "ar-sluis" || prop(r, "permissionsBoundary").HasValue() {
		t.Errorf("role: %v", r.Inputs)
	}
}

// ---- the configuration

func schemaOf(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "config", name+".schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The minimum each binary's file needs beside the ports.
var required = map[string]map[string]any{
	"serve": {"apiVersion": "sluis.truvity.github.io/serve/v2", "issuerURL": "https://access.example.test"},
	"controller-github": {
		"apiVersion": "sluis.truvity.github.io/controller-github/v2", "consoleURL": "https://access.example.test",
		"policy": map[string]any{"file": "/etc/sluis/policy/policy.yaml"},
	},
	"controller-slack": {
		"apiVersion": "sluis.truvity.github.io/controller-slack/v2", "consoleURL": "https://access.example.test",
		"policy": map[string]any{"file": "/etc/sluis/policy/policy.yaml"},
	},
}

func validate(t *testing.T, name string, ports map[string]any) error {
	t.Helper()
	doc := map[string]any{}
	for k, v := range required[name] {
		doc[k] = v
	}
	for k, v := range ports {
		doc[k] = v
	}
	// Through YAML, as the binary reads it.
	raw, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var back any
	if err := yaml.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	return policyconfig.Validate(back, schemaOf(t, name))
}

// The rendered block is held to the schema of each binary that reads it, so the
// library cannot drift from the code it configures: a key renamed in
// schemas/config fails here.
func TestTheRenderedPortsValidateAgainstEveryBinarysSchema(t *testing.T) {
	for _, p := range []arp.PortsArgs{
		{BucketName: bucket, KeyID: "alias/staging-sluis", TableName: table, Region: "eu-west-1"},
		{BucketName: bucket, KeyID: "alias/x", TableName: table},
		{BucketName: bucket, KeyID: "alias/x", TableName: table, Adapter: "dynamodb", BlobPrefix: "/roster/"},
		{BucketName: bucket, KeyID: "alias/x"},
		{BucketName: bucket, TableName: table},
	} {
		ports, err := arp.RenderPorts(p)
		if err != nil {
			t.Fatal(err)
		}
		for name := range required {
			if err := validate(t, name, ports); err != nil {
				t.Errorf("%s: %+v does not validate: %v\n%v", name, p, err, ports)
			}
		}
	}
}

func TestThePortsNameTheBucketTheKeyTheTableAndTheRegion(t *testing.T) {
	y, err := arp.RenderPortsYAML(arp.PortsArgs{BucketName: bucket, KeyID: "alias/staging-sluis", TableName: table, Region: "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Ports struct {
			Adapter  string
			DynamoDB map[string]any `yaml:"dynamodb"`
			Blob     struct {
				Adapter string
				S3      map[string]any
			}
		}
	}
	if err := yaml.Unmarshal([]byte(y), &doc); err != nil {
		t.Fatal(err)
	}
	p := doc.Ports
	if p.Adapter != "dynamodb" || !reflect.DeepEqual(p.DynamoDB, map[string]any{"table": table, "region": "eu-west-1"}) ||
		p.Blob.Adapter != "s3" || !reflect.DeepEqual(p.Blob.S3, map[string]any{"bucket": bucket, "region": "eu-west-1"}) {
		t.Errorf("ports:\n%s", y)
	}
	if strings.Contains(y, "create") || strings.Contains(y, "arn:") {
		t.Errorf("a create flag or an ARN in the block:\n%s", y)
	}
}

func TestWithoutATableNoStateAdapterIsRendered(t *testing.T) {
	ports, err := arp.RenderPorts(arp.PortsArgs{BucketName: bucket, KeyID: "alias/x"})
	if err != nil {
		t.Fatal(err)
	}
	inner := ports["ports"].(map[string]any)
	keys := make([]string, 0, len(inner))
	for k := range inner {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"blob"}) {
		t.Errorf("keys %v", keys)
	}
}

func TestWithoutAKeyNoSealerIsRendered(t *testing.T) {
	ports, err := arp.RenderPorts(arp.PortsArgs{BucketName: bucket, TableName: table})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ports["ports"].(map[string]any)["sealer"]; ok {
		t.Errorf("a sealer block without a key: %v", ports)
	}
}

func TestPortsThatCannotBeRenderedAreRefused(t *testing.T) {
	for name, p := range map[string]arp.PortsArgs{
		"no bucket":         {KeyID: "alias/x", TableName: table},
		"dynamodb no table": {BucketName: bucket, KeyID: "alias/x", Adapter: "dynamodb"},
		"nats":              {BucketName: bucket, KeyID: "alias/x", Adapter: "nats"},
	} {
		if _, err := arp.RenderPorts(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The schema is the oracle, and it does refuse: the test above would pass on a
// validator that accepts anything.
func TestTheSchemaRefusesAWrongPortsBlock(t *testing.T) {
	bad := map[string]any{"ports": map[string]any{"adapter": "dynamodb"}}
	if err := validate(t, "serve", bad); err == nil {
		t.Error("the schema accepted the dynamodb adapter with no table")
	}
	bad = map[string]any{"ports": map[string]any{"blob": map[string]any{"adapter": "s3", "s3": map[string]any{"bucketName": "x"}}}}
	if err := validate(t, "serve", bad); err == nil {
		t.Error("the schema accepted a blob block with a wrong key")
	}
}
