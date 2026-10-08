package sluispulumi_test

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

func r2() *arp.ExternalBlobs {
	return &arp.ExternalBlobs{
		Bucket: "acme-blobs", Endpoint: "https://acct.r2.example.test", CredentialsRef: "internal/blobs/r2",
	}
}

func suppliedKeys() *arp.KeysArgs {
	return &arp.KeysArgs{Sign: "alias/acme-sign", Secrets: "alias/acme-secrets"}
}

func statementBySid(t *testing.T, rec *recorder, sid string) map[string]any {
	t.Helper()
	for _, s := range statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()) {
		if s["Sid"] == sid {
			return s
		}
	}
	return nil
}

// The estate supplies the keys: the library creates none, grants on the keys
// behind the aliases under the runtime's context, and names the aliases in the
// service document.
func TestSuppliedKeysCreateNoKeyAndGrantUnderTheContext(t *testing.T) {
	rec, out := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { noDomain(a); a.Keys = suppliedKeys() }})
	for _, typ := range []string{"aws:kms/key:Key", "aws:kms/alias:Alias"} {
		if n := len(rec.ofType(typ)); n != 0 {
			t.Errorf("%d %s declared with supplied keys", n, typ)
		}
	}
	sign := statementBySid(t, rec, "SluisKeysSign")
	if sign == nil {
		t.Fatal("no grant on the sign key")
	}
	if r := strs(sign["Resource"]); len(r) != 1 || r[0] != arnp+"kms:eu-west-1:"+account+":key/acme-sign" {
		t.Errorf("resource: %v", sign["Resource"])
	}
	if got := strs(sign["Action"]); strings.Join(got, ",") != "kms:Encrypt,kms:Decrypt,kms:GenerateDataKey" {
		t.Errorf("actions: %v", got)
	}
	cond := sign["Condition"].(map[string]any)
	eq := cond["StringEquals"].(map[string]any)
	if eq["kms:EncryptionContext:instance"] != "staging" || eq["kms:EncryptionContext:purpose"] != "sign" {
		t.Errorf("context: %v", eq)
	}
	if got := strs(cond["ForAllValues:StringEquals"].(map[string]any)["kms:EncryptionContextKeys"]); strings.Join(got, ",") != "instance,purpose" {
		t.Errorf("context keys: %v", got)
	}
	if _, has := rolePolicy(t, rec)["kms:Sign"]; has {
		t.Error("kms:Sign is granted: the runtime wraps locally and does not sign with KMS")
	}
	secrets := statementBySid(t, rec, "SluisKeysSecrets")
	if secrets == nil || secrets["Condition"].(map[string]any)["StringEquals"].(map[string]any)["kms:EncryptionContext:purpose"] != "conceal" ||
		strs(secrets["Resource"])[0] != arnp+"kms:eu-west-1:"+account+":key/acme-secrets" {
		t.Errorf("secrets grant: %v", secrets)
	}
	// The older ring entries keep opening until the estate says they are gone.
	if legacy := statementBySid(t, rec, "SluisWrappedSigning"); legacy == nil ||
		legacy["Condition"].(map[string]any)["StringEquals"].(map[string]any)["kms:EncryptionContext:purpose"] != "sluis-signing" {
		t.Errorf("legacy signing grant: %v", legacy)
	}
	doc := layerFiles(t, rec)["sluis/sluis.yaml"]
	for _, want := range []string{"instance: staging", "adapter: kms", "sign: alias/acme-sign", "conceal: alias/acme-secrets"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the service document lacks %q:\n%s", want, doc)
		}
	}
	if out["signingKeyArn"] != arnp+"kms:eu-west-1:"+account+":key/acme-sign" {
		t.Errorf("signingKeyArn %q", out["signingKeyArn"])
	}
}

func TestLegacySigningContextCanBeDropped(t *testing.T) {
	off := false
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		noDomain(a)
		a.Keys = &arp.KeysArgs{Sign: "alias/acme-sign", LegacySigningContext: &off}
	}})
	if statementBySid(t, rec, "SluisWrappedSigning") != nil {
		t.Error("the legacy signing grant remains")
	}
	if statementBySid(t, rec, "SluisKeysSign") == nil || statementBySid(t, rec, "SluisKeysSecrets") != nil {
		t.Error("the sign grant is missing, or a secrets grant appeared with no secrets key")
	}
}

// The keys the library creates are the deprecated path, unchanged.
func TestLibraryCreatedKeysStayWhenNoKeysAreSupplied(t *testing.T) {
	rec, _ := mustLambda(t, estate{mutate: noDomain})
	if n := len(rec.ofType("aws:kms/key:Key")); n != 2 {
		t.Errorf("%d keys, want the two the library creates", n)
	}
	if statementBySid(t, rec, "SluisKeysSign") != nil {
		t.Error("a supplied-key grant without supplied keys")
	}
}

func TestSuppliedKeysAreRefused(t *testing.T) {
	for name, mutate := range map[string]func(*arp.LambdaArgs){
		"no sign alias":    func(a *arp.LambdaArgs) { a.Keys = &arp.KeysArgs{} },
		"an ARN":           func(a *arp.LambdaArgs) { a.Keys = &arp.KeysArgs{Sign: arnp + "kms:eu-west-1:" + account + ":key/x"} },
		"an AWS alias":     func(a *arp.LambdaArgs) { a.Keys = &arp.KeysArgs{Sign: "alias/aws/ssm"} },
		"with created key": func(a *arp.LambdaArgs) { a.Keys = suppliedKeys(); a.SigningKeyAlias = "alias/other" },
		"with wrapped":     func(a *arp.LambdaArgs) { a.Keys = suppliedKeys(); a.WrappedSigning = &arp.WrappedSigningArgs{} },
		"config names keys": func(a *arp.LambdaArgs) {
			a.Keys = suppliedKeys()
			a.Config += "keys: {adapter: kms, sign: alias/x}\n"
		},
	} {
		if _, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { noDomain(a); mutate(a) }}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Blobs on R2: no bucket, no S3 grant, the endpoint and the credentials' address
// in the document, and the read of exactly that parameter.
func TestExternalBlobsOnAnS3CompatibleEndpoint(t *testing.T) {
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		noDomain(a)
		a.Storage = &arp.StorageGrant{External: r2()}
	}})
	for action := range rolePolicy(t, rec) {
		if strings.HasPrefix(action, "s3:") {
			t.Errorf("an S3 grant (%s) for blobs that are not in S3", action)
		}
	}
	creds := statementBySid(t, rec, "SluisBlobCredentials")
	if creds == nil || creds["Action"] != "ssm:GetParameter" ||
		creds["Resource"] != arnp+"ssm:"+region+":"+account+":parameter/sluis/staging/internal/blobs/r2" {
		t.Errorf("credentials grant: %v", creds)
	}
	doc := layerFiles(t, rec)["sluis/sluis.yaml"]
	for _, want := range []string{
		"endpoint: https://acct.r2.example.test", "bucket: acme-blobs", "region: auto", "credentialsRef: internal/blobs/r2", "adapter: s3",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the service document lacks %q:\n%s", want, doc)
		}
	}
	// DynamoDB stays on AWS.
	if len(statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue())) == 0 || rolePolicy(t, rec)["dynamodb:GetItem"] == nil {
		t.Error("the State grant is gone")
	}
}

func TestExternalStorageCreatesNoBucket(t *testing.T) {
	rec, out, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		s, err := arp.NewStorage(ctx, "staging", &arp.StorageArgs{Blobs: r2()})
		if err != nil {
			return err
		}
		if g := s.Grant(); g.External == nil || g.BucketArn != nil {
			t.Errorf("grant: %+v", g)
		}
		collect("bucketName", s.BucketName)
		collect("bucketArn", s.BucketArn)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.ofType("aws:s3/bucket:Bucket")); n != 0 {
		t.Errorf("%d buckets", n)
	}
	if out["bucketName"] != "acme-blobs" || out["bucketArn"] != "" {
		t.Errorf("outputs: %v", out)
	}
}

func TestExternalStorageIsRefused(t *testing.T) {
	for name, args := range map[string]arp.StorageArgs{
		"both":        {BucketName: bucket, Blobs: r2()},
		"neither":     {},
		"http":        {Blobs: &arp.ExternalBlobs{Bucket: "b", Endpoint: "http://x.example.test", CredentialsRef: "internal/blobs/r2"}},
		"no endpoint": {Blobs: &arp.ExternalBlobs{Bucket: "b", CredentialsRef: "internal/blobs/r2"}},
		"no ref":      {Blobs: &arp.ExternalBlobs{Bucket: "b", Endpoint: "https://x.example.test"}},
		"wildcard":    {Blobs: &arp.ExternalBlobs{Bucket: "b", Endpoint: "https://x.example.test", CredentialsRef: "internal/blobs/*"}},
		"external":    {Blobs: &arp.ExternalBlobs{Bucket: "b", Endpoint: "https://x.example.test", CredentialsRef: "external/blobs/r2"}},
		"versioning":  {Blobs: r2(), Versioning: true},
	} {
		_, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
			_, err := arp.NewStorage(ctx, "staging", &args)
			return err
		})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A document may not carry its own blobs beside the library's.
	if _, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		noDomain(a)
		a.Storage = &arp.StorageGrant{External: r2()}
		a.Config += "ports: {blob: {adapter: s3, s3: {bucket: other}}}\n"
	}}); err == nil {
		t.Error("a document with its own ports.blob was accepted")
	}
	// And an endpoint of the document's own is still refused.
	if _, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		noDomain(a)
		a.Config += "ports: {dynamodb: {table: x, endpoint: \"https://elsewhere.example.test\"}}\n"
	}}); err == nil {
		t.Error("a document endpoint was accepted")
	}
}

// The Kubernetes pod's role has no S3 grant for external blobs either.
func TestKubernetesIdentityWithExternalBlobs(t *testing.T) {
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		_, err := arp.NewKubernetesIdentity(ctx, "staging", &arp.KubernetesIdentityArgs{
			ClusterName: cluster, ClusterArn: arnp + "eks:eu-west-1:" + account + ":cluster/" + cluster, AccountID: account,
			Namespace: "sluis", ServiceAccount: "sluis", Storage: &arp.StorageGrant{External: r2()},
			Region: "eu-west-1", Instance: "acme", PermissionsBoundaryArn: arnp + "iam::" + account + ":policy/boundary",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := prop(rec.ofType("aws:iam/policy:Policy")[0], "policy").StringValue()
	if strings.Contains(doc, "s3:") || !strings.Contains(doc, "parameter/sluis/acme/internal/blobs/r2") {
		t.Errorf("policy: %s", doc)
	}
}

func TestExternalReadPolicyNamesExactAddresses(t *testing.T) {
	key := arnp + "kms:eu-west-1:" + account + ":key/acme-secrets"
	_, err := arp.ExternalReadPolicy(arp.ExternalReadPolicyArgs{
		Region: region, AccountID: account, Instance: "acme", SecretsKeyArn: key,
		Addresses: []string{"external/github/acme-bot", "external/slack/T0ACME"},
	})
	if err == nil {
		t.Fatal("an upper-case id was accepted")
	}
	doc, err := arp.ExternalReadPolicy(arp.ExternalReadPolicyArgs{
		Region: region, AccountID: account, Instance: "acme", SecretsKeyArn: key,
		Addresses: []string{"external/slack/t0acme", "external/github/acme-bot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	st := statements(t, doc)
	if len(st) != 2 {
		t.Fatalf("statements: %v", st)
	}
	ssm := arnp + "ssm:" + region + ":" + account + ":parameter/sluis/acme/"
	want := []string{ssm + "external/github/acme-bot", ssm + "external/slack/t0acme"}
	if got := strs(st[0]["Resource"]); strings.Join(got, ",") != strings.Join(want, ",") || st[0]["Action"] != "ssm:GetParameter" {
		t.Errorf("parameters: %v %v", st[0]["Action"], got)
	}
	if strings.Contains(doc, "*") && !strings.Contains(doc, "ForAllValues") {
		t.Errorf("a wildcard in %s", doc)
	}
	for _, s := range st {
		for _, r := range strs(s["Resource"]) {
			if strings.Contains(r, "*") {
				t.Errorf("wildcard resource %q", r)
			}
		}
	}
	k := st[1]
	eq := k["Condition"].(map[string]any)["StringEquals"].(map[string]any)
	if k["Action"] != "kms:Decrypt" || strs(k["Resource"])[0] != key ||
		eq["kms:EncryptionContext:instance"] != "acme" || eq["kms:EncryptionContext:purpose"] != "conceal" {
		t.Errorf("key grant: %v", k)
	}
}

func TestExternalReadPolicyRefusesWhatIsNotExact(t *testing.T) {
	base := arp.ExternalReadPolicyArgs{Region: region, AccountID: account, Instance: "acme", Addresses: []string{"external/github/bot"}}
	for name, mutate := range map[string]func(*arp.ExternalReadPolicyArgs){
		"none":         func(a *arp.ExternalReadPolicyArgs) { a.Addresses = nil },
		"wildcard":     func(a *arp.ExternalReadPolicyArgs) { a.Addresses = []string{"external/github/*"} },
		"prefix":       func(a *arp.ExternalReadPolicyArgs) { a.Addresses = []string{"external/github"} },
		"private":      func(a *arp.ExternalReadPolicyArgs) { a.Addresses = []string{"private/config/x"} },
		"internal":     func(a *arp.ExternalReadPolicyArgs) { a.Addresses = []string{"internal/blobs/r2"} },
		"dot dot":      func(a *arp.ExternalReadPolicyArgs) { a.Addresses = []string{"external/github/.."} },
		"twice":        func(a *arp.ExternalReadPolicyArgs) { a.Addresses = []string{"external/a/b", "external/a/b"} },
		"bad instance": func(a *arp.ExternalReadPolicyArgs) { a.Instance = "private" },
		"two keys":     func(a *arp.ExternalReadPolicyArgs) { a.SecretsKeyArn = "k"; a.ParameterKeyArn = "p" },
	} {
		a := base
		mutate(&a)
		if _, err := arp.ExternalReadPolicy(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestExternalReaderAttachesThePolicyToARole(t *testing.T) {
	rec, out, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		r, err := arp.NewExternalReader(ctx, "eso", &arp.ExternalReaderArgs{
			RoleName: pulumi.String("consumer"), Region: region, AccountID: account, Instance: "acme",
			Addresses: []string{"external/github/bot"}, SecretsKeyArn: pulumi.String(arnp + "kms:eu-west-1:" + account + ":key/k"),
		})
		if err != nil {
			return err
		}
		collect("policy", r.PolicyJSON)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rp := rec.one(t, policyType, "eso-policy")
	if prop(rp, "role").StringValue() != "consumer" || prop(rp, "name").StringValue() != "sluis-external-eso" ||
		prop(rp, "policy").StringValue() != out["policy"] || !strings.Contains(out["policy"], "external/github/bot") {
		t.Errorf("attachment: %v", rp.Inputs)
	}
	if _, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		_, err := arp.NewExternalReader(ctx, "eso", &arp.ExternalReaderArgs{
			RoleName: pulumi.String("consumer"), Region: region, AccountID: account, Instance: "acme", Addresses: []string{"external/*"},
		})
		return err
	}); err == nil {
		t.Error("a wildcard address was accepted")
	}
}
