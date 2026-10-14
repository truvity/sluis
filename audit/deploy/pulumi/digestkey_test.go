package auditpulumi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"
)

// runDigestKey declares the key under the mocks and returns what was declared.
func runDigestKey(t *testing.T, name string, args *auditpulumi.DigestKeyArgs) (*recorder, error) {
	t.Helper()
	rec := &recorder{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := auditpulumi.NewDigestKey(ctx, name, args)
		return err
	}, pulumi.WithMocks("audit-test", "test", rec))
	return rec, err
}

const digestDescription = "audit installation digest chain signing key, example"

// The resource set and the policy are the golden: a change is a change to the
// file, which a reviewer reads. SLUIS_UPDATE_GOLDEN=1 rewrites it.
func TestDigestKeyIsItsGolden(t *testing.T) {
	rec, err := runDigestKey(t, "audit-digest-example", &auditpulumi.DigestKeyArgs{
		Description: digestDescription, BackupAccountID: account,
	})
	if err != nil {
		t.Fatal(err)
	}
	var set []map[string]any
	for _, d := range rec.resources {
		if !strings.HasPrefix(d.Type, "aws:kms/") {
			continue
		}
		in := map[string]any{}
		for k, v := range d.Inputs {
			if string(k) == "policy" {
				var doc any
				if err := json.Unmarshal([]byte(v.StringValue()), &doc); err != nil {
					t.Fatal(err)
				}
				in["policy"] = doc
				continue
			}
			in[string(k)] = fmt.Sprint(v.V)
		}
		set = append(set, map[string]any{"type": d.Type, "name": d.Name, "import": d.ImportID, "inputs": in})
	}
	sort.Slice(set, func(i, j int) bool { return set[i]["type"].(string) < set[j]["type"].(string) })
	raw, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(bytes.ReplaceAll(raw, []byte(account), []byte("<account>")), '\n')
	path := filepath.Join("testdata", "digestkey.golden.json")
	if os.Getenv("SLUIS_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (SLUIS_UPDATE_GOLDEN=1 writes it)", err)
	}
	if !bytes.Equal(raw, want) {
		t.Errorf("the digest key is not its golden %s:\n%s", path, raw)
	}
}

// The policy text is what gitops hand-built: indented by two spaces, one
// statement, the backup account's root and nobody else.
func TestDigestKeyPolicyIsTheEstatesText(t *testing.T) {
	want := `{
  "Statement": [
    {
      "Action": "kms:*",
      "Effect": "Allow",
      "Principal": {
        "AWS": "arn:` + "aws:iam::" + account + `:root"
      },
      "Resource": "*",
      "Sid": "BackupAccountKeyAdmin"
    }
  ],
  "Version": "2012-10-17"
}`
	if got := auditpulumi.DigestKeyPolicy(account); got != want {
		t.Errorf("policy:\n%s\nwant:\n%s", got, want)
	}
}

func TestDigestKeyHooksKeepNamesAndImports(t *testing.T) {
	rec, err := runDigestKey(t, "audit-digest-example", &auditpulumi.DigestKeyArgs{
		Description: digestDescription, BackupAccountID: account,
		AliasName:      "alias/legacy-digest",
		KeyLogicalName: "legacy-key", AliasLogicalName: "legacy-key-alias",
		KeyAliases:   []pulumi.Alias{{Name: pulumi.String("old-key")}},
		AliasAliases: []pulumi.Alias{{Name: pulumi.String("old-key-alias")}},
		KeyImport:    "1234abcd-12ab-34cd-56ef-1234567890ab", AliasImport: "alias/legacy-digest",
	})
	if err != nil {
		t.Fatal(err)
	}
	k := rec.one(t, "aws:kms/key:Key", "legacy-key")
	if k.ImportID != "1234abcd-12ab-34cd-56ef-1234567890ab" || k.Inputs["description"].StringValue() != digestDescription {
		t.Errorf("key: import %q, description %q", k.ImportID, k.Inputs["description"].StringValue())
	}
	a := rec.one(t, "aws:kms/alias:Alias", "legacy-key-alias")
	if a.ImportID != "alias/legacy-digest" || a.Inputs["name"].StringValue() != "alias/legacy-digest" {
		t.Errorf("alias: import %q, name %q", a.ImportID, a.Inputs["name"].StringValue())
	}
}

func TestDigestKeyRefusesWhatItCannotKeep(t *testing.T) {
	for name, args := range map[string]*auditpulumi.DigestKeyArgs{
		"no description": {BackupAccountID: account},
		"short account":  {Description: digestDescription, BackupAccountID: "1234"},
		"an ARN alias":   {Description: digestDescription, BackupAccountID: account, AliasName: arnp + "kms:eu-west-1:" + account + ":alias/x"},
	} {
		if _, err := runDigestKey(t, "audit-digest-example", args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// ---- the archive grants on the shared key

// gitopsArchiveStatements is the statements as the estate's repository builds
// them today (the key policy of the shared parameter key), with placeholder ids.
// It is the expected value: the library's output is the same bytes.
func gitopsArchiveStatements(accountID, region, bucket string, roles, readers []string, admin string) []map[string]any {
	const (
		polSid, polEffect, polAllow, polPrincipal, polAWS = "Sid", "Effect", "Allow", "Principal", "AWS"
		polAction, polResource, polCondition              = "Action", "Resource", "Condition"
		polStringEquals, polStringLike                    = "StringEquals", "StringLike"
		condKMSViaService                                 = "kms:ViaService"
		condKMSS3ARN                                      = "kms:EncryptionContext:aws:s3:arn"
		condPrincipalARN, condArnLike, condArnEquals      = "aws:PrincipalArn", "ArnLike", "ArnEquals"
	)
	viaS3 := map[string]any{condKMSViaService: fmt.Sprintf("s3.%s.amazonaws.com", region)}
	context := map[string]any{condKMSS3ARN: []string{bucket, bucket + "/*"}}
	actions := []string{"kms:GenerateDataKey", "kms:Decrypt", "kms:DescribeKey"}
	readActions := []string{"kms:Decrypt", "kms:DescribeKey"}
	root := fmt.Sprintf("arn:"+"aws:iam::%s:root", accountID)
	return []map[string]any{
		{
			polSid: "AuditReadersOpenTheArchiveThroughS3", polEffect: polAllow, polPrincipal: map[string]any{polAWS: root},
			polAction: readActions, polResource: "*",
			polCondition: map[string]any{condArnEquals: map[string]any{condPrincipalARN: readers}, polStringEquals: viaS3, polStringLike: context},
		},
		{
			polSid: "AuditRolesUseTheArchiveThroughS3", polEffect: polAllow, polPrincipal: map[string]any{polAWS: root},
			polAction: actions, polResource: "*",
			polCondition: map[string]any{condArnEquals: map[string]any{condPrincipalARN: roles}, polStringEquals: viaS3, polStringLike: context},
		},
		{
			polSid: "OperatorRewritesTheArchiveThroughS3", polEffect: polAllow, polPrincipal: map[string]any{polAWS: root},
			polAction: actions, polResource: "*",
			polCondition: map[string]any{condArnLike: map[string]any{condPrincipalARN: admin}, polStringEquals: viaS3, polStringLike: context},
		},
	}
}

func TestArchiveKeyPolicyStatementsAreTheEstatesBytes(t *testing.T) {
	const region = "eu-west-1"
	bucket := arnp + "s3:::acme-audit-" + account + "-" + region
	roles := []string{
		arnp + "iam::" + account + ":role/audit/acme-writer",
		arnp + "iam::" + account + ":role/audit/acme-notary",
	}
	readers := []string{
		arnp + "iam::" + account + ":role/audit/acme-observe-reader",
		arnp + "iam::" + account + ":role/audit/acme-query",
	}
	admin := arnp + "iam::" + account + ":role/aws-reserved/sso.amazonaws.com/*AWSReservedSSO_role-admin_*"

	got, err := auditpulumi.ArchiveKeyPolicyStatements(auditpulumi.ArchiveKeyPolicyArgs{
		AccountID: account, Region: region, BucketArn: bucket, Roles: roles, Readers: readers, Operator: admin,
	})
	if err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(gitopsArchiveStatements(account, region, bucket, roles, readers, admin))
	if !bytes.Equal(gb, wb) {
		t.Errorf("statements differ:\n got %s\nwant %s", gb, wb)
	}
	if len(got) != 3 || got[0]["Sid"] != "AuditReadersOpenTheArchiveThroughS3" || got[2]["Sid"] != "OperatorRewritesTheArchiveThroughS3" {
		t.Errorf("order: %v", got)
	}
	// Placed in a key policy document, the shared key's own check takes them.
	if _, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": got}); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveKeyPolicyStatementsRefuseAnIncompleteInput(t *testing.T) {
	ok := auditpulumi.ArchiveKeyPolicyArgs{
		AccountID: account, Region: "eu-west-1", BucketArn: arnp + "s3:::b",
		Roles: []string{"r"}, Readers: []string{"q"}, Operator: "o",
	}
	for name, edit := range map[string]func(*auditpulumi.ArchiveKeyPolicyArgs){
		"account": func(a *auditpulumi.ArchiveKeyPolicyArgs) { a.AccountID = "1" },
		"region":  func(a *auditpulumi.ArchiveKeyPolicyArgs) { a.Region = "" },
		"bucket":  func(a *auditpulumi.ArchiveKeyPolicyArgs) { a.BucketArn = "b" },
		"roles":   func(a *auditpulumi.ArchiveKeyPolicyArgs) { a.Roles = nil },
		"readers": func(a *auditpulumi.ArchiveKeyPolicyArgs) { a.Readers = nil },
		"admin":   func(a *auditpulumi.ArchiveKeyPolicyArgs) { a.Operator = "" },
	} {
		a := ok
		edit(&a)
		if _, err := auditpulumi.ArchiveKeyPolicyStatements(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
