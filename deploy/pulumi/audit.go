package sluispulumi

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"go.yaml.in/yaml/v3"

	auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"
)

// AuditArgs is where sluis's audit records go. The library installs an audit
// installation of its own by default (the audit Pulumi library of this
// repository, at the same release), points the service at its ingest queue and
// lets the function's role send to it and nobody else; Use sends the records to
// an installation that exists and installs nothing; Enabled false installs and
// sends nothing.
//
// What the runtime does without an installation: the service's `audit` adapter
// is `log`, so every record is validated against the catalogue and written to
// the log, and kept nowhere else. That is also what a service with no `audit`
// adapter does when it has no queue URL to publish to.
type AuditArgs struct {
	// Enabled false leaves audit out: nothing is installed, the function is not
	// granted sqs:SendMessage on any queue, and the service document names the
	// `log` audit adapter. Unset (nil) or true installs, or uses Use. Exclusive
	// with Use.
	Enabled *bool

	// Use sends the records to an audit installation that already exists
	// (another stack's, or another account's) and installs nothing. The queue
	// URL must be known when the program runs: it is written into the
	// configuration layer.
	Use *AuditUse

	// Name is the installation's component name: it is in every role, queue and
	// function name of it (`<name>-writer`, `<name>-ingest`) and in the Pulumi
	// URNs. Default `audit-<instance>`. 1 to 32 characters of a-z, 0-9 and -,
	// starting with a letter.
	Name string

	// WriterPackage and WriterPackageSHA256 are (left empty: this library's own
	// release, as LambdaArgs.Package) the release's
	// `audit-writer-lambda_<version>_linux_arm64.zip` and its digest from the
	// release's checksums.txt, as for LambdaArgs.Package.
	// The zip must be of the release this library is (the audit library checks
	// it, see Guards).
	WriterPackage       string
	WriterPackageSHA256 string
	// CatalogueDir is the directory the release's
	// `sluis-audit-catalogue_<version>.tar.gz` unpacks to: sluis's roster
	// catalogue and every schema it references. It is the catalogue the audit
	// writer registers, delivered in the writer's package (the service publishes
	// to SQS and registers nothing). Required to install.
	CatalogueDir string

	// Profiles are the installation's destinations: each name is a profile of
	// the archive (the `profiles` the catalogue's actions name, `security` for
	// sluis's own) and its value the framework profiles it is composed from.
	// Default `security: [history]`, the lowest preset (operational). The
	// preset is derived from them. Exclusive with DeploymentYAML.
	Profiles map[string][]string
	// DeploymentYAML is the full deployment document, for what Profiles cannot
	// say (categories, a preset per profile). It names the profiles, not the
	// storage: that is Presets. Exclusive with Profiles.
	DeploymentYAML string

	// Presets are the install presets this installation uses, each with a store
	// of its own (operational, standard, attested): the same map as
	// auditpulumi.Args.Presets, which the library passes through. Every profile's
	// preset (the highest min_preset of its framework profiles, or the stronger
	// one it asks for) must be one of them. Separate from the blob store: each is
	// a bucket of its own, an AWS one the library creates (Create) or uses, or one
	// the estate made on an S3-compatible store.
	//
	// Unset with the default Profiles, it is the operational preset on a bucket
	// the library creates (`<name>-<account>-<region>-operational`). With
	// Profiles or DeploymentYAML given, name the presets they need.
	Presets map[string]AuditPreset
	// Archive is the tuning of the AWS buckets the library creates: encryption,
	// the attested bucket's lock and the lifecycle steps.
	Archive AuditArchiveArgs
	// Keys are the estate's keys by alias (the library creates none). The
	// archive's own key is Keys.Archive; leave it out and set nothing else and
	// the AWS bucket is encrypted with SSE-S3.
	Keys auditpulumi.KeysArgs
	// State is the installation's state store; see auditpulumi.StateArgs.
	State auditpulumi.StateArgs

	// Notary, Alerts, Telemetry and Observe are the audit library's own; they
	// are for the presets above operational, which the profiles ask for. Leave them out under operational (the library refuses them there).
	Notary    auditpulumi.NotaryArgs
	Alerts    auditpulumi.AlertsArgs
	Telemetry *auditpulumi.TelemetryArgs
	Observe   *auditpulumi.ObserveArgs
	// Redrivers are principals allowed to redrive the dead-letter queue.
	Redrivers []pulumi.StringInput
	// Guards are the audit library's pre-deploy guards.
	Guards auditpulumi.GuardArgs
}

// AuditPreset is one install preset's store: auditpulumi.PresetStorage, and
// the choice to take an S3-compatible store from the blobs.
type AuditPreset struct {
	auditpulumi.PresetStorage
	// ReuseBlobStore takes Endpoint, Region and PathStyle from Storage.Blobs (the
	// blobs are on an S3-compatible store already): the bucket stays a different
	// one, and the credentials stay the installation's own. Exclusive with
	// Endpoint; refused when Storage has no Blobs.
	ReuseBlobStore bool
}

// AuditArchiveArgs is the tuning of the AWS buckets the library creates.
type AuditArchiveArgs struct {
	// Encryption is `kms`, `s3` or `aws-managed` (AWS only). Default `kms` when
	// Keys.Archive is set, `s3` otherwise.
	Encryption string

	// ObjectLockMode, AcknowledgeCompliance, DefaultRetentionDays,
	// GlacierIRDays and DeepArchiveDays are the audit archive's own (AWS only);
	// see auditpulumi.ArchiveArgs. The lock is the attested bucket's alone.
	ObjectLockMode        string
	AcknowledgeCompliance bool
	DefaultRetentionDays  int
	GlacierIRDays         int
	DeepArchiveDays       int
}

// AuditUse is an audit installation that exists: the outputs of its ingest queue.
type AuditUse struct {
	// QueueURL is the ingest queue's URL, `https://sqs.<region>.amazonaws.com/
	// <account>/<name>`. Required. The queue must be in the function's region
	// (the adapter signs for it).
	QueueURL string
	// QueueArn is the queue's ARN, which the function's role is granted
	// sqs:SendMessage on. Default: the ARN the URL names.
	QueueArn string
}

// DefaultAuditProfiles are the profiles installed when AuditArgs.Profiles and
// DeploymentYAML are unset: sluis's catalogue names the `security` profile on
// its actions, and the `history` framework profile keeps the installation
// operational (the writer, the archive, deduplication and the queue intake).
var DefaultAuditProfiles = map[string][]string{"security": {"history"}}

type auditMode int

const (
	// auditLegacy is LambdaArgs.AuditQueueArn: the estate installs audit itself
	// and writes the queue URL into its Installation.
	auditLegacy auditMode = iota
	auditInstall
	auditUse
	auditOff
)

// auditPlan is what LambdaArgs.Audit resolves to, before anything is created.
type auditPlan struct {
	mode      auditMode
	name      string
	queueURL  string
	queueArn  string
	queueName string
}

var queueURLPattern = regexp.MustCompile(`^https://sqs\.([a-z0-9-]+)\.amazonaws\.com/([0-9]{12})/([A-Za-z0-9_-]{1,80}(?:\.fifo)?)$`)

// planAudit resolves LambdaArgs.Audit and AuditQueueArn. Region, AccountID and
// Instance are set by the time it runs for an installation.
func (a *LambdaArgs) planAudit() (*auditPlan, error) {
	au := a.Audit
	switch {
	case au == nil && a.AuditQueueArn != nil:
		return &auditPlan{mode: auditLegacy}, nil
	case au != nil && a.AuditQueueArn != nil:
		return nil, errors.New("sluispulumi: LambdaArgs.AuditQueueArn and LambdaArgs.Audit are both set: AuditQueueArn is the older way " +
			"(the estate installs audit itself and names the queue in its installation); say it once, with Audit")
	case a.Installation == nil:
		if au != nil {
			return nil, errors.New("sluispulumi: LambdaArgs.Audit needs LambdaArgs.Installation: the library writes the audit adapter into " +
				"the service document it renders. With the deprecated Config, set AuditQueueArn")
		}
		return &auditPlan{mode: auditLegacy}, nil
	}
	if au == nil {
		au = &AuditArgs{}
	}
	if au.Enabled != nil && !*au.Enabled {
		if au.Use != nil {
			return nil, errors.New("sluispulumi: LambdaArgs.Audit.Enabled is false and Use is set: send the records to an installation, or to none")
		}
		return &auditPlan{mode: auditOff}, nil
	}
	if a.Region == "" || a.AccountID == "" || a.Instance == "" {
		return nil, errors.New("sluispulumi: LambdaArgs.Audit: Region, AccountID and Instance are required (they name the queue)")
	}
	if u := au.Use; u != nil {
		m := queueURLPattern.FindStringSubmatch(u.QueueURL)
		if m == nil {
			return nil, fmt.Errorf("sluispulumi: LambdaArgs.Audit.Use.QueueURL %q must be an SQS queue URL "+
				"(https://sqs.<region>.amazonaws.com/<account>/<name>)", u.QueueURL)
		}
		if m[1] != a.Region {
			return nil, fmt.Errorf("sluispulumi: LambdaArgs.Audit.Use.QueueURL is in %s and the function is in %s: the sqs audit adapter "+
				"signs for the function's region", m[1], a.Region)
		}
		arn := u.QueueArn
		if arn == "" {
			arn = arnPrefix + "sqs:" + m[1] + ":" + m[2] + ":" + m[3]
		}
		if !strings.HasPrefix(arn, "arn:") {
			return nil, fmt.Errorf("sluispulumi: LambdaArgs.Audit.Use.QueueArn %q is not an ARN", arn)
		}
		return &auditPlan{mode: auditUse, queueURL: u.QueueURL, queueArn: arn, queueName: m[3]}, nil
	}
	name := au.Name
	if name == "" {
		name = "audit-" + a.Instance
	}
	if err := auditpulumi.CheckName(name); err != nil {
		return nil, fmt.Errorf("sluispulumi: LambdaArgs.Audit.Name: %w", err)
	}
	var missing []string
	for k, v := range map[string]string{
		"CatalogueDir": au.CatalogueDir,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("sluispulumi: LambdaArgs.Audit installs audit by default and is missing %v: the directory of "+
			"sluis's audit catalogue (the release's sluis-audit-catalogue bundle). To send the records to an installation that "+
			"exists set Audit.Use; for none set Audit.Enabled to false", sortedStrings(missing))
	}
	if au.Profiles != nil && strings.TrimSpace(au.DeploymentYAML) != "" {
		return nil, errors.New("sluispulumi: LambdaArgs.Audit.Profiles and DeploymentYAML are both set: the first is the short form of the second")
	}
	for _, name := range sortedKeys(au.Presets) {
		if pr := au.Presets[name]; pr.ReuseBlobStore && pr.Endpoint != "" {
			return nil, fmt.Errorf("sluispulumi: LambdaArgs.Audit.Presets[%s].ReuseBlobStore and Endpoint are both set: say the store once", name)
		}
	}
	if au.Presets == nil && (au.Profiles != nil || strings.TrimSpace(au.DeploymentYAML) != "") {
		return nil, errors.New("sluispulumi: LambdaArgs.Audit.Presets is required with Profiles or DeploymentYAML: name the install presets " +
			"(operational, standard, attested) the profiles are kept under, each with its store")
	}
	qn := name + "-ingest"
	return &auditPlan{
		mode: auditInstall, name: name, queueName: qn,
		queueURL: "https://sqs." + a.Region + ".amazonaws.com/" + a.AccountID + "/" + qn,
		queueArn: arnPrefix + "sqs:" + a.Region + ":" + a.AccountID + ":" + qn,
	}, nil
}

// deploymentDocument is the deployment document of the profiles.
func deploymentDocument(profiles map[string][]string) (string, error) {
	type entry struct {
		Frameworks []string `yaml:"frameworks"`
	}
	doc := struct {
		APIVersion string           `yaml:"apiVersion"`
		Profiles   map[string]entry `yaml:"profiles"`
	}{APIVersion: "audit.truvity.github.io/audit-deployment/v2", Profiles: map[string]entry{}}
	for name, fw := range profiles {
		doc.Profiles[name] = entry{Frameworks: append([]string{}, fw...)}
	}
	raw, err := yaml.Marshal(doc)
	return string(raw), err
}

// auditInstallArgs is the audit library's arguments for the plan: the function's
// role is the one sender, the profiles and preset are passed through, and the
// archive is the choice made.
func (a *LambdaArgs) auditInstallArgs(p *auditPlan, role pulumi.StringInput) (*auditpulumi.Args, error) {
	au := a.Audit
	if au == nil {
		au = &AuditArgs{}
	}
	deployment := au.DeploymentYAML
	if strings.TrimSpace(deployment) == "" {
		profiles := au.Profiles
		if profiles == nil {
			profiles = DefaultAuditProfiles
		}
		var err error
		if deployment, err = deploymentDocument(profiles); err != nil {
			return nil, err
		}
	}
	ar := au.Archive
	archive := auditpulumi.ArchiveArgs{
		Encryption:     ar.Encryption,
		ObjectLockMode: ar.ObjectLockMode, AcknowledgeCompliance: ar.AcknowledgeCompliance,
		DefaultRetentionDays: ar.DefaultRetentionDays, GlacierIRDays: ar.GlacierIRDays, DeepArchiveDays: ar.DeepArchiveDays,
	}
	presets := map[string]auditpulumi.PresetStorage{}
	if au.Presets == nil {
		presets[auditpulumi.PresetOperational] = auditpulumi.PresetStorage{Create: true}
	}
	for name, pr := range au.Presets {
		st := pr.PresetStorage
		if pr.ReuseBlobStore {
			b := a.Storage.External
			if b == nil {
				return nil, fmt.Errorf("sluispulumi: LambdaArgs.Audit.Presets[%s].ReuseBlobStore is set and the blobs are not on an S3-compatible store (StorageArgs.Blobs)", name) //nolint:lll // a table row
			}
			st.Endpoint, st.PathStyle = b.Endpoint, b.PathStyle
			if st.Region == "" && b.Region != "" {
				st.Region = b.Region
			}
			if b.Bucket != "" && b.Bucket == st.Bucket {
				return nil, fmt.Errorf("sluispulumi: LambdaArgs.Audit.Presets[%s].Bucket is the blob bucket %q: the audit archive is a bucket of its own", name, b.Bucket)
			}
		}
		presets[name] = st
	}
	for name, st := range presets {
		if st.Endpoint == "" && st.Create && st.Bucket == "" {
			st.Bucket = auditpulumi.PresetBucketName(auditpulumi.ArchiveBucketName(p.name, a.AccountID, a.Region), name)
			presets[name] = st
		}
		if st.Endpoint != "" && st.Bucket == "" {
			return nil, fmt.Errorf("sluispulumi: LambdaArgs.Audit.Presets[%s].Bucket is required on an S3-compatible store: the bucket is the estate's, "+
				"and a different one from the blob bucket", name)
		}
	}
	created := false
	for _, st := range presets {
		created = created || st.Create
	}
	if created && archive.Encryption == "" && au.Keys.Archive == "" {
		archive.Encryption = auditpulumi.EncryptionS3
	}
	senders := []pulumi.StringInput{role}
	var artifacts *auditpulumi.ArtifactsArgs
	if a.Artifacts != nil {
		// The same bucket; the prefix is audit's own unless the estate chose one.
		artifacts = &auditpulumi.ArtifactsArgs{Bucket: a.Artifacts.Bucket}
		if a.Artifacts.Prefix != "sluis/" {
			artifacts.Prefix = a.Artifacts.Prefix
		}
	}
	return &auditpulumi.Args{
		Artifacts: artifacts, Release: a.Release,
		Tags: a.Tags, AccountID: a.AccountID, Region: a.Region,
		LogRetentionDays: a.LogRetentionDays,
		Keys:             au.Keys,
		State:            au.State,
		Archive:          archive,
		Presets:          presets,
		Ingest:           auditpulumi.IngestArgs{Senders: senders, Redrivers: au.Redrivers},
		Writer: auditpulumi.WriterArgs{
			Package: au.WriterPackage, PackageSHA256: au.WriterPackageSHA256,
			DeploymentYAML: deployment,
			// The roster catalogue travels in the writer's package: the service
			// publishes to SQS and registers nothing.
			CatalogueDirs: []string{au.CatalogueDir},
		},
		Notary: au.Notary, Alerts: au.Alerts, Telemetry: au.Telemetry, Observe: au.Observe,
		Guards: au.Guards,
	}, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
