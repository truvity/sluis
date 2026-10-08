package auditpulumi_test

import (
	"strings"
	"testing"

	auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"
)

const r2Endpoint = "https://account.r2.example.test"

// onR2 is an operational installation whose archive is on an S3-compatible store.
func onR2(a *auditpulumi.Args) {
	operational(a)
	a.Keys, a.Observe = auditpulumi.KeysArgs{}, nil
	a.Archive = auditpulumi.ArchiveArgs{BucketName: "acme-audit", Endpoint: r2Endpoint, PathStyle: true}
}

// An operational installation on R2: the writer, the dedupe table and the queue,
// no bucket, no key, no notary. The store's credentials are an SSM parameter the
// writer may read, and nothing else of S3 or KMS.
func TestAnOperationalInstallationOnAnS3CompatibleStore(t *testing.T) {
	rec, out, err := build(t, onR2)
	if err != nil {
		t.Fatal(err)
	}
	if out["preset"] != "operational" {
		t.Errorf("preset = %q", out["preset"])
	}
	for _, typ := range []string{"aws:s3/bucket:Bucket", "aws:kms/key:Key", "aws:kms/alias:Alias", "aws:scheduler/schedule:Schedule"} {
		if got := rec.ofType(typ); len(got) != 0 {
			t.Errorf("declared %v on an S3-compatible store with no notary", got)
		}
	}
	if len(rec.calls) == 0 {
		t.Fatal("no invoke at all")
	}
	for _, c := range rec.calls {
		if c.Token == aliasLookup {
			t.Errorf("an alias was looked up: operational on R2 has no key")
		}
	}
	if out["bucketName"] != "acme-audit" || out["bucketArn"] != "" || out["archiveKeyArn"] != "" || out["sealKeyArn"] != "" {
		t.Errorf("outputs: %v", out)
	}
	wantPath := "/audit/audit/internal/archive"
	if out["credentialsPath"] != wantPath {
		t.Errorf("credentials path = %q, want %q", out["credentialsPath"], wantPath)
	}

	g := grants(policy(t, rec, "audit-writer"))
	for action := range g {
		if strings.HasPrefix(action, "s3:") || strings.HasPrefix(action, "kms:") {
			t.Errorf("the writer on an S3-compatible store may %s", action)
		}
	}
	if got := g["ssm:GetParameter"]; len(got) != 1 || !strings.HasSuffix(got[0], ":parameter"+wantPath) {
		t.Errorf("the writer's ssm:GetParameter = %v, want the credentials only", got)
	}
	if _, ok := g["ssm:PutParameter"]; ok {
		t.Error("the writer may write the credentials")
	}

	// The configuration names the endpoint, the region auto and where the
	// credentials are -- and no secret -- and validates against the binary's schema.
	body := layerFiles(t, rec, "audit-writer")["audit.yaml"]
	for _, want := range []string{"endpoint: " + r2Endpoint, "region: auto", "pathStyle: true", "lockMode: none",
		"root: /audit/audit", "address: internal/archive"} {
		if !strings.Contains(body, want) {
			t.Errorf("the writer's configuration lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "kmsKey") || strings.Contains(strings.ToLower(body), "secretaccesskey") {
		t.Errorf("a key or a secret in the configuration:\n%s", body)
	}
	validateConfigs(t, rec, map[string]string{"audit-writer": "audit-writer-lambda"})
}

// A standard installation can still use the notary on an S3-compatible store:
// the notary reads the credentials and signs with the estate's seal key.
func TestAStandardInstallationOnAnS3CompatibleStoreSealsWithTheEstatesKey(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Archive = auditpulumi.ArchiveArgs{BucketName: "acme-audit", Endpoint: r2Endpoint, StoreRegion: "weur"}
		a.Keys.Archive = ""
		a.Observe = nil
	})
	if err != nil {
		t.Fatal(err)
	}
	g := grants(policy(t, rec, "audit-notary"))
	if len(g["kms:Sign"]) != 1 || g["kms:Sign"][0] != out["sealKeyArn"] {
		t.Errorf("the notary's Sign = %v", g["kms:Sign"])
	}
	if len(g["ssm:GetParameter"]) != 1 {
		t.Errorf("the notary's credentials grant = %v", g["ssm:GetParameter"])
	}
	for action := range g {
		if strings.HasPrefix(action, "s3:") {
			t.Errorf("the notary may %s on an S3-compatible store", action)
		}
	}
	if n := layerFiles(t, rec, "audit-notary")["audit.yaml"]; !strings.Contains(n, "region: weur") || !strings.Contains(n, "seal: alias/audit-seal") {
		t.Errorf("notary configuration:\n%s", n)
	}
	validateConfigs(t, rec, map[string]string{"audit-writer": "audit-writer-lambda", "audit-notary": "audit-notary"})
}

func TestAnS3CompatibleStoreRefusesWhatIsAnAWSBucketsSetting(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*auditpulumi.Args)
		want string
	}{
		"object lock": {func(a *auditpulumi.Args) { onR2(a); a.Archive.ObjectLockMode = auditpulumi.Governance }, "Object Lock is refused"},
		"attested": {func(a *auditpulumi.Args) {
			onR2(a)
			a.Preset = auditpulumi.PresetAttested
			a.Notary, a.Keys.Seal = auditpulumi.NotaryArgs{Disabled: true}, ""
		}, "compliance Object Lock"},
		"encryption":     {func(a *auditpulumi.Args) { onR2(a); a.Archive.Encryption = auditpulumi.EncryptionS3 }, "Archive.Encryption"},
		"retention":      {func(a *auditpulumi.Args) { onR2(a); a.Archive.DefaultRetentionDays = 30 }, "DefaultRetentionDays"},
		"an archive key": {func(a *auditpulumi.Args) { onR2(a); a.Keys.Archive = "alias/x" }, "Keys.Archive"},
		"a destination key": {func(a *auditpulumi.Args) {
			onR2(a)
			a.Archive.Profiles = nil
			a.Writer.DeploymentYAML = "profiles:\n  activity:\n    frameworks: [history]\n    key_alias: alias/acme-activity\n"
		}, "names key_alias"},
		"observe": {func(a *auditpulumi.Args) {
			onR2(a)
			a.Observe = &auditpulumi.ObserveArgs{IRSA: irsa("audit", "o")}
		}, "IAM roles over an AWS S3 bucket"},
		"http":                     {func(a *auditpulumi.Args) { onR2(a); a.Archive.Endpoint = "http://store.example.test" }, "https"},
		"a path":                   {func(a *auditpulumi.Args) { onR2(a); a.Archive.Endpoint = r2Endpoint + "/bucket" }, "https"},
		"credentials elsewhere":    {func(a *auditpulumi.Args) { onR2(a); a.Archive.CredentialsAddress = "private/archive" }, "below internal/"},
		"endpoint settings on AWS": {func(a *auditpulumi.Args) { a.Archive.PathStyle = true }, "for a store at"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := build(t, c.edit)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to say %q", err, c.want)
			}
		})
	}
}

// ---- the keys

func TestKeysAreTheEstatesAndRefusedWhereTheInstallationHasNoUseForThem(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*auditpulumi.Args)
		want string
	}{
		"an ARN":                {func(a *auditpulumi.Args) { a.Keys.Seal = arnp + "kms:eu-west-1:" + account + ":key/x" }, "is an ARN"},
		"an AWS alias":          {func(a *auditpulumi.Args) { a.Keys.Archive = "alias/aws/s3" }, "AWS managed alias"},
		"a bare name":           {func(a *auditpulumi.Args) { a.Keys.Seal = "audit-seal" }, "must be a KMS alias"},
		"no seal key":           {func(a *auditpulumi.Args) { a.Keys.Seal = "" }, "Keys.Seal is required"},
		"no archive key":        {func(a *auditpulumi.Args) { a.Keys.Archive = "" }, "Keys.Archive is required"},
		"both archive keys":     {func(a *auditpulumi.Args) { a.Archive.KeyArn = givenKey }, "both name the archive key"},
		"a seal key, no notary": {func(a *auditpulumi.Args) { operational(a); a.Keys.Seal = "alias/audit-seal" }, "has no notary"},
		"a pseudonym key at standard": {func(a *auditpulumi.Args) {
			a.Writer.DeploymentYAML = profilesOf("history")
			a.Notary, a.Keys.Seal = auditpulumi.NotaryArgs{Disabled: true}, ""
			a.Alerts = auditpulumi.AlertsArgs{}
			a.Archive.ObjectLockMode, a.Archive.DefaultRetentionDays = "", 0
			a.Keys.Pseudonym = "alias/audit-pseudonym"
		}, "provisions no pseudonym keys"},
		"a conceal key alone": {func(a *auditpulumi.Args) { a.Keys.Conceal = "alias/audit-conceal" }, "no Keys.Pseudonym"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := build(t, c.edit)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to say %q", err, c.want)
			}
		})
	}
}

// A standard installation whose profile pseudonymises is given the pseudonym and
// conceal keys: the writer's grants are conditioned on the encryption context
// the storage KMS backend sends, and the pseudonym secrets live in the state store.
func TestTheWriterIsGrantedThePseudonymAndConcealKeysByContext(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Keys.Pseudonym, a.Keys.Conceal, a.Keys.Instance = "alias/audit-pseudonym", "alias/audit-conceal", "acme"
	})
	if err != nil {
		t.Fatal(err)
	}
	var pseudonym, conceal map[string]any
	for _, s := range policy(t, rec, "audit-writer") {
		for _, r := range strs(s["Resource"]) {
			switch {
			case strings.HasSuffix(r, ":key/audit-pseudonym"):
				pseudonym = s
			case strings.HasSuffix(r, ":key/audit-conceal"):
				conceal = s
			}
		}
	}
	if pseudonym == nil || conceal == nil {
		t.Fatalf("the writer has no grant on the pseudonym (%v) or conceal (%v) key", pseudonym, conceal)
	}
	eq := func(s map[string]any) map[string]any {
		return s["Condition"].(map[string]any)["StringEquals"].(map[string]any)
	}
	if eq(pseudonym)["kms:EncryptionContext:purpose"] != "pseudonym" {
		t.Errorf("pseudonym condition: %v", pseudonym["Condition"])
	}
	if c := eq(conceal); c["kms:EncryptionContext:purpose"] != "conceal" || c["kms:EncryptionContext:instance"] != "acme" {
		t.Errorf("conceal condition: %v", conceal["Condition"])
	}
	for _, s := range []map[string]any{pseudonym, conceal} {
		for _, a := range strs(s["Action"]) {
			if a == "kms:Sign" || a == "kms:*" {
				t.Errorf("the writer may %s", a)
			}
		}
	}
	g := grants(policy(t, rec, "audit-writer"))
	if len(g["ssm:PutParameter"]) != 1 || !strings.HasSuffix(g["ssm:PutParameter"][0], ":parameter/audit/audit/internal/pseudonym/*") {
		t.Errorf("the writer's state grant = %v", g["ssm:PutParameter"])
	}
	body := layerFiles(t, rec, "audit-writer")["audit.yaml"]
	for _, want := range []string{"adapter: kms", "instance: acme", "pseudonym: alias/audit-pseudonym", "conceal: alias/audit-conceal",
		"address: internal/pseudonym"} {
		if !strings.Contains(body, want) {
			t.Errorf("the writer's configuration lacks %q:\n%s", want, body)
		}
	}
	// The notary has no use of either key.
	for _, s := range policy(t, rec, "audit-notary") {
		for _, r := range strs(s["Resource"]) {
			if strings.Contains(r, "pseudonym") || strings.Contains(r, "conceal") {
				t.Errorf("the notary is granted %v", s)
			}
		}
	}
	validateConfigs(t, rec, map[string]string{"audit-writer": "audit-writer-lambda", "audit-notary": "audit-notary"})
}
