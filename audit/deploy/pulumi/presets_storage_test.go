package auditpulumi_test

import (
	"sort"
	"strings"
	"testing"

	auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"
	"github.com/truvity/sluis/audit/profile"
)

// finalDeployment is the document the functions read, parsed and held to its
// presets as they will.
func finalDeployment(t *testing.T, rec *recorder) *profile.Deployment {
	t.Helper()
	body := layerFiles(t, rec, "audit-writer")["deployment.yaml"]
	d, err := profile.ParseDeployment([]byte(body))
	if err != nil {
		t.Fatalf("the shipped deployment document does not parse: %v\n%s", err, body)
	}
	frameworks, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CheckStorage(frameworks); err != nil {
		t.Fatalf("the shipped deployment document is refused by its own presets: %v\n%s", err, body)
	}
	if d.APIVersion != profile.DeploymentAPIVersion {
		t.Errorf("apiVersion = %q", d.APIVersion)
	}
	return d
}

// A standard bucket and an attested one, each under a prefix of its own: two
// buckets, a lock on the attested one alone, a lifecycle per prefix, and grants
// scoped to the prefix.
func TestAStandardAndAnAttestedPresetMakeTwoBucketsAndOnlyTheAttestedOneIsLocked(t *testing.T) {
	rec, out, err := build(t, attested(func(a *auditpulumi.Args) {
		a.Presets["standard"] = auditpulumi.PresetStorage{Bucket: "acme-audit-standard", Prefix: "standard/", Create: true}
		a.Presets["attested"] = auditpulumi.PresetStorage{Bucket: "acme-audit-attested", Prefix: "attested/", Create: true}
	}))
	if err != nil {
		t.Fatal(err)
	}
	buckets := map[string]string{}
	for _, b := range rec.ofType("aws:s3/bucket:Bucket") {
		buckets[b.Name] = prop(b, "bucket").StringValue()
	}
	if len(buckets) != 2 || buckets["audit-archive-standard"] != "acme-audit-standard" || buckets["audit-archive-attested"] != "acme-audit-attested" {
		t.Fatalf("buckets: %v", buckets)
	}
	if out["bucketNames"] != "standard=acme-audit-standard,attested=acme-audit-attested" && out["bucketNames"] != "attested=acme-audit-attested,standard=acme-audit-standard" { //nolint:lll // a table row
		t.Errorf("bucketNames = %q", out["bucketNames"])
	}
	if out["presets"] != "standard,attested" {
		t.Errorf("presets = %q", out["presets"])
	}

	locks := rec.ofType(lockType)
	if len(locks) != 1 || locks[0].Name != "audit-archive-attested" {
		t.Fatalf("locks: %v", locks)
	}
	if mode := prop(locks[0], "rule").ObjectValue()["defaultRetention"].ObjectValue()["mode"].StringValue(); mode != "COMPLIANCE" {
		t.Errorf("lock mode = %s", mode)
	}

	// The lifecycle is per prefix of each bucket, for the profiles kept in it.
	prefixes := func(bucket string) []string {
		lc := rec.one(t, "aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration", bucket)
		var out []string
		for _, r := range prop(lc, "rules").ArrayValue() {
			if tr := r.ObjectValue()["transitions"]; tr.IsArray() {
				out = append(out, r.ObjectValue()["filter"].ObjectValue()["prefix"].StringValue())
			}
		}
		sort.Strings(out)
		return out
	}
	if got := strings.Join(prefixes("audit-archive-standard"), " "); got != "standard/records/billing-nl/ standard/records/security/" {
		t.Errorf("standard lifecycle prefixes: %s", got)
	}
	if got := strings.Join(prefixes("audit-archive-attested"), " "); got != "attested/records/pay/" {
		t.Errorf("attested lifecycle prefixes: %s", got)
	}

	// The writer's grants are on each bucket's own prefix, with retention on the
	// attested one only, and the listing is held to the prefix.
	w := policy(t, rec, "audit-writer")
	g := grants(w)
	for _, want := range []string{"acme-audit-standard/standard/records/*", "acme-audit-attested/attested/records/*"} {
		if !hasResource(g, "s3:PutObject", want) {
			t.Errorf("the writer cannot put %s: %v", want, g["s3:PutObject"])
		}
	}
	for _, r := range g["s3:PutObjectRetention"] {
		if !strings.Contains(r, "acme-audit-attested/attested/") {
			t.Errorf("retention on %s", r)
		}
	}
	lists := map[string]string{}
	for _, st := range w {
		if strings.Join(strs(st["Action"]), "") != "s3:ListBucket" {
			continue
		}
		cond := st["Condition"].(map[string]any)["StringLike"].(map[string]any)["s3:prefix"]
		lists[strs(st["Resource"])[0]] = strings.Join(strs(cond), ",")
	}
	if lists[arnp+"s3:::acme-audit-standard"] != "standard/*" || lists[arnp+"s3:::acme-audit-attested"] != "attested/*" {
		t.Errorf("list conditions: %v", lists)
	}
	// The notary seals into each, and reads the rest.
	n := grants(policy(t, rec, "audit-notary"))
	if !hasResource(n, "s3:PutObject", "acme-audit-standard/standard/seals/*") || !hasResource(n, "s3:PutObject", "acme-audit-attested/attested/keys/*") {
		t.Errorf("the notary's puts: %v", n["s3:PutObject"])
	}

	// Observe reads both.
	r := grants(policy(t, rec, "audit-observe-reader"))
	for _, want := range []string{"acme-audit-standard/standard/records/*", "acme-audit-attested/attested/seals/*"} {
		if !hasResource(r, "s3:GetObject", want) {
			t.Errorf("observe cannot read %s", want)
		}
	}

	d := finalDeployment(t, rec)
	if d.Presets[profile.Standard].Bucket != "acme-audit-standard" || d.Presets[profile.Attested].Prefix != "attested/" {
		t.Errorf("document presets: %+v", d.Presets)
	}
	for _, fn := range []string{"audit-writer", "audit-notary"} {
		if strings.Contains(layerFiles(t, rec, fn)["audit.yaml"], "lockMode") {
			t.Errorf("%s carries a lock mode", fn)
		}
	}
	validateConfigs(t, rec, map[string]string{"audit-writer": "audit-writer-lambda", "audit-notary": "audit-notary"})
}

// A Truvity-like shape: the standard preset on a bucket the library creates, a
// key alias of its own looked up (never created), and the notary on.
func TestAStandardPresetOnS3WithAKeyAliasAndTheNotary(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Keys.Archive = ""
		a.Presets["standard"] = auditpulumi.PresetStorage{
			Bucket: "acme-audit-standard", Prefix: "standard/", Region: "eu-west-1", KeyAlias: "alias/acme-standard", Create: true,
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	rec.one(t, "aws:lambda/function:Function", "audit-notary")
	if out["sealKeyArn"] == "" || out["topic"] == "" {
		t.Errorf("no notary or alarms: %v", out)
	}
	looked := 0
	for _, c := range rec.calls {
		if c.Token == aliasLookup {
			looked++
		}
	}
	if looked != 2 { // the seal key and the preset's key
		t.Errorf("%d alias lookups, want 2", looked)
	}
	for _, typ := range []string{"aws:kms/key:Key", "aws:kms/alias:Alias"} {
		if n := len(rec.ofType(typ)); n != 0 {
			t.Errorf("created %d %s", n, typ)
		}
	}
	want := arnp + "kms:eu-west-1:" + account + ":key/acme-standard"
	for _, role := range []string{"audit-writer", "audit-notary", "audit-observe-reader"} {
		g := grants(policy(t, rec, role))
		if len(g["kms:Decrypt"]) != 1 || g["kms:Decrypt"][0] != want {
			t.Errorf("%s decrypts under %v, want %s", role, g["kms:Decrypt"], want)
		}
	}
	// With no archive key, no default is named in the configuration.
	if w := layerFiles(t, rec, "audit-writer")["audit.yaml"]; strings.Contains(w, "kmsKey") {
		t.Errorf("a default key without Keys.Archive:\n%s", w)
	}
	d := finalDeployment(t, rec)
	if s := d.Presets[profile.Standard]; s.KeyAlias != "alias/acme-standard" || s.Region != "eu-west-1" || s.Endpoint != "" {
		t.Errorf("document preset: %+v", s)
	}
}

// A bucket that is not Create'd is the estate's: the library declares none, writes
// no lifecycle, and only grants.
func TestAnExistingBucketIsOnlyGrantedAccessTo(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Presets["standard"] = auditpulumi.PresetStorage{Bucket: "acme-existing", Prefix: "standard/"}
		a.Archive.Encryption, a.Keys.Archive = auditpulumi.EncryptionAWSManaged, ""
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"aws:s3/bucket:Bucket", "aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration", lockType} {
		if n := len(rec.ofType(typ)); n != 0 {
			t.Errorf("declared %d %s for a bucket that is not created", n, typ)
		}
	}
	if out["bucketNames"] != "standard=acme-existing" || out["bucketArns"] != "standard="+arnp+"s3:::acme-existing" {
		t.Errorf("outputs: %q %q", out["bucketNames"], out["bucketArns"])
	}
	for _, role := range []string{"audit-writer", "audit-notary", "audit-observe-reader"} {
		g := grants(policy(t, rec, role))
		if !hasResource(g, "s3:GetObject", "acme-existing/standard/records/*") {
			t.Errorf("%s cannot read the existing bucket: %v", role, g["s3:GetObject"])
		}
	}
}

func TestPresetStorageThatCannotWorkIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*auditpulumi.Args)
		says string
	}{
		"a key alias that is an ARN": {func(a *auditpulumi.Args) {
			a.Presets["standard"] = auditpulumi.PresetStorage{Bucket: "acme-audit", KeyAlias: givenKey, Create: true}
		}, `Presets["standard"].KeyAlias`},
		"a key alias with SSE-S3": {func(a *auditpulumi.Args) {
			a.Archive.Encryption, a.Keys.Archive = auditpulumi.EncryptionS3, ""
			a.Presets["standard"] = auditpulumi.PresetStorage{Bucket: "acme-audit", KeyAlias: "alias/acme-standard", Create: true}
		}, "KeyAlias is set"},
		"a prefix without a slash": {func(a *auditpulumi.Args) {
			a.Presets["standard"] = auditpulumi.PresetStorage{Bucket: "acme-audit", Prefix: "standard", Create: true}
		}, "Prefix"},
		"a bucket name": {func(a *auditpulumi.Args) {
			a.Presets["standard"] = auditpulumi.PresetStorage{Bucket: "Acme_Audit", Create: true}
		}, "not a bucket name"},
		"one created bucket for two presets": {attested(func(a *auditpulumi.Args) {
			a.Presets["attested"] = auditpulumi.PresetStorage{Bucket: "acme-audit", Prefix: "attested/", Create: true}
			a.Presets["standard"] = auditpulumi.PresetStorage{Bucket: "acme-audit", Prefix: "standard/", Create: true}
		}), "one has Create"},
		"the attested bucket shared": {attested(func(a *auditpulumi.Args) {
			a.Presets["attested"] = auditpulumi.PresetStorage{Bucket: "acme-locked", Prefix: "attested/"}
			a.Presets["standard"] = auditpulumi.PresetStorage{Bucket: "acme-locked", Prefix: "standard/"}
			a.Archive = auditpulumi.ArchiveArgs{}
		}), "Object Lock is a property of the bucket"},
		"overlapping prefixes": {func(a *auditpulumi.Args) {
			a.Writer.DeploymentYAML = "profiles:\n  activity:\n    frameworks: [history]\n  security:\n    frameworks: [security]\n"
			a.Presets["standard"] = auditpulumi.PresetStorage{Bucket: "acme-shared", Prefix: "audit/"}
			a.Presets["operational"] = auditpulumi.PresetStorage{Bucket: "acme-shared", Prefix: "audit/ops/"}
		}, "overlap"},
		"a profile whose preset is not configured": {func(a *auditpulumi.Args) {
			a.Writer.DeploymentYAML = profilesOf("pci-dss")
		}, "presets.attested"},
		"no document and a notary": {func(a *auditpulumi.Args) { a.Writer.DeploymentYAML = ""; a.Ingest.Disabled = true }, "DeploymentYAML is required"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _, err := build(t, c.edit)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("got %v, want a refusal naming %q", err, c.says)
			}
			if len(rec.ofType("aws:s3/bucket:Bucket")) != 0 || len(rec.ofType("aws:iam/role:Role")) != 0 {
				t.Error("resources were declared before the arguments were refused")
			}
		})
	}
}

// An installation with the ingest side off and no document still has its
// presets validated.
func TestPresetsAreValidatedWithoutAProfilesDocument(t *testing.T) {
	off := func(a *auditpulumi.Args) {
		a.Ingest.Disabled, a.Writer = true, auditpulumi.WriterArgs{}
		a.Notary, a.Keys.Seal, a.Alerts = auditpulumi.NotaryArgs{Disabled: true}, "", auditpulumi.AlertsArgs{}
		a.Presets = map[string]auditpulumi.PresetStorage{"operational": {Bucket: "acme-audit", Create: true}}
	}
	rec, out, err := build(t, off)
	if err != nil {
		t.Fatal(err)
	}
	rec.one(t, "aws:s3/bucket:Bucket", "audit-archive-operational")
	if out["presets"] != "operational" {
		t.Errorf("presets = %q", out["presets"])
	}
	if _, _, err := build(t, func(a *auditpulumi.Args) {
		off(a)
		a.Presets["operational"] = auditpulumi.PresetStorage{Bucket: "acme-audit", KeyAlias: "1234abcd-12ab-34cd-56ef-1234567890ab", Create: true}
	}); err == nil || !strings.Contains(err.Error(), "KeyAlias") {
		t.Errorf("a bad preset without a document: %v", err)
	}
}

// Adopt takes an existing bucket: it is imported by name and its settings are
// declared, and no lifecycle resource is, so the rules it has are left alone.
func TestAdoptImportsTheBucketAndDeclaresNoLifecycle(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Presets["standard"] = auditpulumi.PresetStorage{
			Bucket: "acme-audit-existing", Prefix: "standard/", Adopt: true, AcknowledgeLifecycle: true,
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.ofType("aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration")); n != 0 {
		t.Errorf("%d lifecycle configurations declared for an adopted bucket", n)
	}
	b := rec.one(t, "aws:s3/bucket:Bucket", "audit-archive-standard")
	if b.ImportID != "acme-audit-existing" || prop(b, "bucket").StringValue() != "acme-audit-existing" {
		t.Errorf("bucket import id %q, inputs %v", b.ImportID, b.Inputs)
	}
	if prop(b, "tags").IsObject() {
		t.Errorf("the adopted bucket's tags are declared: %v", prop(b, "tags"))
	}
	for _, typ := range []string{
		"aws:s3/bucketVersioning:BucketVersioning",
		"aws:s3/bucketServerSideEncryptionConfiguration:BucketServerSideEncryptionConfiguration",
		"aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock",
		"aws:s3/bucketOwnershipControls:BucketOwnershipControls",
		"aws:s3/bucketPolicy:BucketPolicy",
	} {
		r := rec.one(t, typ, "audit-archive-standard")
		if r.ImportID != "acme-audit-existing" {
			t.Errorf("%s: import id %q, want the bucket name", typ, r.ImportID)
		}
	}
	if out["bucketNames"] != "standard=acme-audit-existing" {
		t.Errorf("bucketNames = %q", out["bucketNames"])
	}
	// The roles are granted the bucket as for any other.
	if g := grants(policy(t, rec, "audit-writer")); !hasResource(g, "s3:PutObject", "acme-audit-existing/standard/records/*") {
		t.Errorf("the writer's puts: %v", g["s3:PutObject"])
	}
}

// The Create path imports nothing and still writes the derived lifecycle.
func TestCreateStillCreatesAndWritesItsLifecycle(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rec.resources {
		if d.ImportID != "" {
			t.Errorf("%s %s imports %q", d.Type, d.Name, d.ImportID)
		}
	}
	rec.one(t, "aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration", "audit-archive-standard")
}

func TestAdoptIsRefusedWhereItCannotWork(t *testing.T) {
	for name, tc := range map[string]struct {
		store auditpulumi.PresetStorage
		want  string
	}{
		"with Create":         {auditpulumi.PresetStorage{Bucket: "acme-audit", Create: true, Adopt: true}, "Choose one"},
		"with an endpoint":    {auditpulumi.PresetStorage{Bucket: "acme-audit", Adopt: true, Endpoint: "https://store.example.com"}, "Adopt is set with"},
		"without Adopt":       {auditpulumi.PresetStorage{Bucket: "acme-audit", AcknowledgeLifecycle: true}, "without Presets[\"standard\"].Adopt"},
		"unacknowledged keep": {auditpulumi.PresetStorage{Bucket: "acme-audit", Adopt: true}, "AcknowledgeLifecycle"},
	} {
		_, _, err := build(t, func(a *auditpulumi.Args) { a.Presets["standard"] = tc.store })
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// Two presets cannot share a bucket one of them adopts.
func TestAdoptedBucketIsOnePresets(t *testing.T) {
	_, _, err := build(t, attested(func(a *auditpulumi.Args) {
		a.Presets["standard"] = auditpulumi.PresetStorage{Bucket: "acme-shared", Prefix: "standard/", Adopt: true, AcknowledgeLifecycle: true}
		a.Presets["attested"] = auditpulumi.PresetStorage{Bucket: "acme-shared", Prefix: "attested/", Adopt: true, AcknowledgeLifecycle: true}
	}))
	if err == nil || !strings.Contains(err.Error(), "both name the bucket") {
		t.Errorf("err = %v", err)
	}
}
