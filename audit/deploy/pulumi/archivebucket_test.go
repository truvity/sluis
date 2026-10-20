package auditpulumi_test

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"
)

const archiveKeyArn = arnp + "kms:eu-west-1:" + "111111111111" + ":key/1234abcd-12ab-34cd-56ef-1234567890ab"

func archiveBucketArgs() *auditpulumi.ArchiveBucketArgs {
	return &auditpulumi.ArchiveBucketArgs{
		BucketName: "example-audit-archive",
		KeyArn:     archiveKeyArn,
		Profiles:   []string{"security", "attested"},
		Tags:       map[string]string{"component": "audit"},
	}
}

func runArchiveBucket(t *testing.T, args *auditpulumi.ArchiveBucketArgs) (*recorder, error) {
	t.Helper()
	rec := &recorder{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := auditpulumi.NewArchiveBucket(ctx, "audit-archive", args)
		return err
	}, pulumi.WithMocks("audit-test", "test", rec))
	return rec, err
}

func TestArchiveBucketDeclaresTheBucketAndItsSixSettings(t *testing.T) {
	rec, err := runArchiveBucket(t, archiveBucketArgs())
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, d := range rec.resources {
		if d.Name != "audit-archive" {
			t.Errorf("%s is named %q, want the one name", d.Type, d.Name)
		}
		if d.ImportID != "" {
			t.Errorf("%s imports %q", d.Type, d.ImportID)
		}
		types = append(types, d.Type)
	}
	sort.Strings(types)
	want := []string{
		auditpulumi.ArchiveBucketType, auditpulumi.ArchiveEncryptionType, auditpulumi.ArchiveLifecycleType,
		auditpulumi.ArchiveOwnershipType, auditpulumi.ArchivePolicyType, auditpulumi.ArchivePublicBlockType,
		auditpulumi.ArchiveVersioningType,
	}
	sort.Strings(want)
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("resources = %v, want %v", types, want)
	}
}

func TestArchiveBucketLifecycleHasNoExpiry(t *testing.T) {
	rec, err := runArchiveBucket(t, archiveBucketArgs())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rec.resources {
		switch d.Type {
		case auditpulumi.ArchiveLifecycleType:
			s := d.Inputs.Mappable()
			raw, jerr := json.Marshal(s)
			if jerr != nil {
				t.Fatal(jerr)
			}
			text := string(raw)
			for _, w := range []string{"records-security", "records/attested/", "GLACIER_IR", "DEEP_ARCHIVE", "abort-incomplete-multipart-uploads"} {
				if !strings.Contains(text, w) {
					t.Errorf("lifecycle lacks %q: %s", w, text)
				}
			}
			if strings.Contains(strings.ToLower(text), "expiration") {
				t.Errorf("lifecycle expires objects: %s", text)
			}
		case auditpulumi.ArchivePolicyType:
			if p := d.Inputs["policy"].StringValue(); !strings.Contains(p, `"Sid":"OnlyOverTLS"`) || !strings.Contains(p, "example-audit-archive/*") {
				t.Errorf("policy = %s", p)
			}
		}
	}
}

func TestArchiveBucketAliasHookIsAskedPerResource(t *testing.T) {
	args := archiveBucketArgs()
	asked := map[string]bool{}
	args.Aliases = func(typ string) []pulumi.Alias {
		asked[typ] = true
		return []pulumi.Alias{{Name: pulumi.String("old")}}
	}
	if _, err := runArchiveBucket(t, args); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 7 {
		t.Fatalf("hook asked for %d types, want 7: %v", len(asked), asked)
	}
}

func TestArchiveBucketRefusesWhatItCannotDeclare(t *testing.T) {
	for name, mut := range map[string]func(*auditpulumi.ArchiveBucketArgs){
		"no bucket":      func(a *auditpulumi.ArchiveBucketArgs) { a.BucketName = "" },
		"alias key":      func(a *auditpulumi.ArchiveBucketArgs) { a.KeyArn = arnp + "kms:eu-west-1:111111111111:alias/x" },
		"no profiles":    func(a *auditpulumi.ArchiveBucketArgs) { a.Profiles = nil },
		"duplicate":      func(a *auditpulumi.ArchiveBucketArgs) { a.Profiles = []string{"a", "a"} },
		"deep before IR": func(a *auditpulumi.ArchiveBucketArgs) { a.GlacierIRDays, a.DeepArchiveDays = 90, 60 },
	} {
		a := archiveBucketArgs()
		mut(a)
		if _, err := runArchiveBucket(t, a); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
