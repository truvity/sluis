package sluispulumi

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/kms"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/audit/deploy/pulumi/artifact"
	sluisconfig "github.com/truvity/sluis/config"
)

// BackupType is the Pulumi type token of the Backup component.
const BackupType = "sluis:aws:Backup"

// The caller classes of a module call on Lambda: the callee is invoked through
// the alias `live-<class>`, and the class is the alias, never the payload
// (internal/modcall/lambdacall).
const (
	// ClassIssuer is the issuer calling a module's function.
	ClassIssuer     = "issuer"
	ClassConsole    = "console"
	ClassAdmin      = "admin"
	ClassBreakglass = "breakglass"
)

// CallerAlias is the alias a caller of the class invokes: `live-<class>`.
func CallerAlias(class string) string { return LiveAlias + "-" + class }

const (
	// DefaultBackupSchedule is the daily run: 02:00 UTC.
	DefaultBackupSchedule = "cron(0 2 * * ? *)"
	// DefaultBackupResumeRate is the rate of the resume schedule NewBackup no
	// longer creates.
	//
	// Deprecated: since backup format 2 a run does not pause, the schedule's
	// only answer was `idle`, and an apply deletes it. Nothing reads this.
	DefaultBackupResumeRate = "rate(5 minutes)"
	// DefaultBackupTimeoutSeconds is the backup and restore functions' timeout:
	// a run pauses itself under three minutes left and the next continues it.
	DefaultBackupTimeoutSeconds = 900
	// DefaultBackupMaxAge is `backup.retention.maxAge` when it is not set, the
	// binary's own default; the archive bucket's Object Lock never exceeds it.
	DefaultBackupMaxAge = 720 * time.Hour
	// NoBackupAlarmHours is how long the alarm waits for a completed backup.
	NoBackupAlarmHours = 36

	backupDocumentVersion = "sluis-backup/v1"
)

// ArchiveArgs is the bucket backups are written to (`backup.target`).
type ArchiveArgs struct {
	// Bucket is the bucket's name. Required. It exists, unless Create is set.
	Bucket string
	// Prefix is put before `backup/<instance>/` in the bucket (`backup.target.prefix`).
	Prefix string
	// Region is the bucket's region. Default: the function's.
	Region string
	// AccountID is the account that owns a bucket that exists. Default: the
	// function's own. Another account is the cross-account case: the roles'
	// policies allow, and the bucket's owner applies [ArchiveBucketPolicyStatements]
	// (the output ArchiveBucketPolicy) to the bucket and names the roles in the
	// key policy of the bucket's key.
	AccountID string
	// Versioned says the bucket is versioned, which a bucket under Object Lock
	// is. Nil is true. The roles then also get the version actions.
	Versioned *bool
	// SSEKeyArn is the key of the bucket's server-side encryption
	// (`backup.target.kmsKey`). Default: none, the bucket's own default.
	SSEKeyArn string
	// RestoreRoleArn is the restore function's role, named in the cross-account
	// bucket policy output when it is given. Default: the role NewRestore
	// creates for this instance, by its name.
	RestoreRoleArn string
	// Create makes the bucket: versioned, encrypted, private, TLS only and under
	// Object Lock. Only NewBackup creates; NewRestore refuses it.
	Create *ArchiveBucketArgs
	// BreakglassRoleArns are the roles that alone may bypass the bucket's lock:
	// the bucket's policy denies s3:BypassGovernanceRetention,
	// s3:PutBucketObjectLockConfiguration and s3:DeleteObjectVersion to every
	// other principal (ArchiveBucketGuardStatement), so that a role with a broad S3
	// allow (a power user, an automation role) cannot shorten the lock, switch
	// it off or delete a backup's version. Role ARNs, a `*` only in the hash of
	// an SSO role's name (CheckKeyAdmin). Required with Create; with an existing
	// bucket it adds the same statement to the ArchiveBucketPolicy output, for the
	// bucket's owner to apply. Include the role that applies the stack, which
	// changes the lock configuration.
	BreakglassRoleArns []string
}

// ArchiveBucketArgs is the bucket NewBackup creates.
type ArchiveBucketArgs struct {
	// Mode is the Object Lock mode of the default retention: COMPLIANCE, which
	// nobody can shorten (the root included) and needs AcknowledgeCompliance, or
	// GOVERNANCE, which a role holding s3:BypassGovernanceRetention can shorten.
	// Required in effect: left empty it is COMPLIANCE (it was GOVERNANCE before
	// v1.78), so a stack that relied on the old default is refused until it says
	// GOVERNANCE (what the bucket has) or acknowledges COMPLIANCE. The lock of a
	// bucket that exists is not changed by editing this on the quiet: Pulumi
	// applies a different Mode to the bucket's lock configuration, which is
	// why the choice is made in the arguments and never by a default.
	Mode                  string
	AcknowledgeCompliance bool
	// RetentionDays is the default retention. Default: `retention.maxAge` in
	// whole days (30). It must not exceed `retention.maxAge`: the prune deletes
	// a backup older than that, and a lock longer than the age would make the
	// delete fail until it ended.
	RetentionDays int
	// Tags are put on the bucket.
	Tags map[string]string
}

// BackupRetentionArgs is `backup.retention`.
type BackupRetentionArgs struct {
	// Keep is how many of the newest backups always stay. Default: 7.
	Keep int
	// MaxAge is how old a backup beyond Keep may be before it is deleted.
	// Default: 720h.
	MaxAge time.Duration
}

// BackupFunctionArgs sizes a backup or restore function.
type BackupFunctionArgs struct {
	// MemoryMB defaults to 512.
	MemoryMB int
	// TimeoutSeconds defaults to [DefaultBackupTimeoutSeconds].
	TimeoutSeconds int
}

// BackupCommon is what the backup function and the restore function share: the
// same release zip, the same installation and the same archive.
type BackupCommon struct {
	// Region and AccountID name the resources in the roles' policies. Required.
	Region    string
	AccountID string
	// Instance is the installation's name; the archive path is
	// `backup/<instance>/` and the SSM root `/sluis/<instance>`. Required.
	Instance string

	// Package, PackageSHA256, PackageVersion, Artifacts and Release are
	// LambdaArgs': the release zip, deployed byte for byte. Package empty is
	// the library's own release.
	Package        string
	PackageSHA256  string
	PackageVersion string
	Artifacts      *ArtifactsArgs
	Release        *ReleaseArgs

	// Storage is the blob store of the installation (Storage.Grant()); the
	// roles get its module prefixes. With External, no IAM on it.
	Storage *StorageGrant
	// BlobBucketName and BlobPrefix are `ports.blob.s3`; required unless
	// Storage.External, which carries its own.
	BlobBucketName string
	BlobPrefix     string
	// State is the per-module tables (States.Grant()): all six. Required.
	State *StateGrant
	// TableNames overrides a module's table name; the default is
	// `sluis-<instance>-<module>` (the same as LambdaArgs.TableNames).
	TableNames map[Module]string
	// ParameterKeyArn is the customer key the SecureString parameters are
	// encrypted with, written as `secrets.kmsKeyId`. Required: the restore
	// identity is one of them, and the AWS-managed key is not a custodian of it.
	ParameterKeyArn string
	// MinterRefs are the Cloudflare minter parameters a process reads for
	// `ports.blob.s3.credentials.preset` without hosting the module, as
	// ModuleEnv.MinterRefs.
	MinterRefs []string

	// ArchiveKeyAlias was the alias (alias/<name>) of the KMS key that sealed each
	// backup's data key.
	//
	// Deprecated: backups are encrypted with age (format v2) to the installation's
	// recipients; no KMS key is used for the archive, the roles get no grant on
	// it, and it is optional. While it is set it is still held to an alias, is
	// rendered as `backup.key` for a binary of format v1, and the outputs
	// ArchiveKeyArn keep naming the key behind it for the estates that export them.
	ArchiveKeyAlias string
	// Archive is the bucket.
	Archive ArchiveArgs
	// Retention is `backup.retention`. Default: the binary's.
	Retention *BackupRetentionArgs

	// AuditQueueURL is the audit queue the function sends to
	// (`adapters.audit` sqs); its role may send to it. Default: none, the audit
	// records go to the log.
	AuditQueueURL string

	// FunctionName names the function, its role, its log group and its alarms.
	// Default `sluis-<instance>-backup` or `sluis-<instance>-restore`.
	FunctionName string
	Function     BackupFunctionArgs
	// LogRetentionDays defaults to 30.
	LogRetentionDays int
	// PermissionsBoundaryArn is the boundary of the roles.
	PermissionsBoundaryArn string
	// AlarmActionArns are notified (SNS topics) when an alarm fires. Default none.
	AlarmActionArns []string
	// Tags are put on everything that takes tags.
	Tags map[string]string
	// CodeSigningConfigArn is LambdaArgs.CodeSigningConfigArn, for these functions.
	CodeSigningConfigArn pulumi.StringInput

	// Telemetry is the OpenTelemetry layer, as LambdaArgs.Telemetry: the same
	// installation value gives every function of the library the same
	// telemetry. Nil: no layer and no OTEL environment.
	Telemetry *TelemetryArgs
	// AdditionalWebIdentityAudiences are the audiences the role may ask STS to
	// mint a web identity token for, for the telemetry layer (as
	// LambdaArgs.AdditionalWebIdentityAudiences; this role calls no console, so
	// it holds these alone). Allowed with Telemetry only. Empty entries and
	// duplicates are refused.
	AdditionalWebIdentityAudiences []string
}

// BackupScheduleArgs is the schedule of the backup function.
type BackupScheduleArgs struct {
	// Daily is the run: default [DefaultBackupSchedule].
	Daily string
	// ResumeRate is accepted and ignored.
	//
	// Deprecated: the `<function>-resume` schedule is gone (a backup run of
	// format 2 does not pause), so an apply deletes it. The field stays so
	// that no estate stops compiling; any value, valid or not, is ignored.
	ResumeRate string
	// Paused declares the daily schedule DISABLED.
	Paused bool
}

// BackupArgs is the backup function.
type BackupArgs struct {
	BackupCommon
	Schedule BackupScheduleArgs
	// ExtraRecipients are further age recipients (`age1...`) every backup is
	// encrypted to, beside the installation's own generated one: an offline
	// break-glass key whose identity is kept off the platform. Each is validated
	// as an age X25519 recipient; an invalid one is refused. An offline recipient
	// is recommended: it opens the backups when the installation, its parameter
	// store and the restore function are all gone.
	ExtraRecipients []string
}

// Backup is the component. Its fields are the outputs.
type Backup struct {
	pulumi.ResourceState

	// FunctionArn, FunctionName, RoleArn, RoleName are the function and its role.
	FunctionArn  pulumi.StringOutput
	FunctionName pulumi.StringOutput
	RoleArn      pulumi.StringOutput
	RoleName     pulumi.StringOutput
	// LiveAliasArn is the alias the schedules invoke. AliasArns holds it and one
	// `live-<class>` alias per caller class, by alias name.
	LiveAliasArn pulumi.StringOutput
	AliasArns    pulumi.StringMapOutput
	// ConfigLayerArn is the layer that holds the `sluis-backup/v1` document.
	ConfigLayerArn pulumi.StringOutput
	// SchedulerRoleArn, ScheduleNames and ScheduleDLQArn are the schedules.
	SchedulerRoleArn pulumi.StringOutput
	ScheduleNames    pulumi.StringArrayOutput
	ScheduleDLQArn   pulumi.StringOutput
	// ArchiveBucketName and ArchiveBucketArn are the archive.
	ArchiveBucketName pulumi.StringOutput
	ArchiveBucketArn  pulumi.StringOutput
	// ArchiveBucketPolicy is the policy document the bucket's owner applies to a
	// bucket in another account (ArchiveBucketPolicyStatements); the same
	// grants on a bucket in this account are in the roles' policies.
	ArchiveBucketPolicy pulumi.StringOutput
	// ArchiveKeyArn is the key behind ArchiveKeyAlias; empty when it is not set.
	//
	// Deprecated: the archive uses no KMS key.
	ArchiveKeyArn pulumi.StringOutput
	// RestoreRecipient is the age recipient (`age1...`) of the installation's
	// generated restore keypair: public, and the first of the recipients in the
	// backup document. Its identity is the parameter RestoreIdentityParameter,
	// read by the restore role only.
	RestoreRecipient pulumi.StringOutput
	// Recipients are every recipient the backups are encrypted to: RestoreRecipient,
	// then ExtraRecipients.
	Recipients pulumi.StringArrayOutput
	// RestoreIdentityParameter is the SSM parameter that holds the age identity,
	// `/sluis/<instance>/internal/backup/restore-identity`.
	RestoreIdentityParameter pulumi.StringOutput
	// AlarmNames are the alarms: no completed backup in 36 hours, a failed run,
	// a message in the schedules' dead-letter queue.
	AlarmNames pulumi.StringArrayOutput
}

// backupPlan is BackupCommon once validated and defaulted.
type backupPlan struct {
	BackupCommon
	role      string
	modules   ModuleSet
	archive   ArchiveGrant // BucketArn is filled when known
	prefix    string       // the archive prefix with its slash
	retention BackupRetentionArgs
	lockDays  int
	auditArn  string
}

func (c *BackupCommon) plan(role string) (*backupPlan, error) {
	if c == nil {
		return nil, errors.New("sluispulumi: the arguments are nil")
	}
	p := &backupPlan{BackupCommon: *c, role: role}
	var errs []error
	req := func(name, v string) {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	req("Region", p.Region)
	req("AccountID", p.AccountID)
	req("Instance", p.Instance)
	req("Archive.Bucket", p.Archive.Bucket)
	if p.ParameterKeyArn == "" {
		errs = append(errs, errors.New("ParameterKeyArn is required: the customer-managed key the installation's SecureStrings are encrypted with. "+
			"NewBackup stores the restore identity, the age key that opens every backup, as a SecureString, and the restore and backup functions write "+
			"the restored credentials; left to SSM they are encrypted with the AWS-managed key alias/aws/ssm, which every role with ssm:GetParameter can decrypt"))
	}
	if p.Instance != "" && !validInstance(p.Instance) {
		errs = append(errs, fmt.Errorf("the Instance %q is lower-case letters, digits and dashes, at most 32, and not private or export", p.Instance))
	}
	if a := p.ArchiveKeyAlias; a != "" && (strings.HasPrefix(a, "arn:") || !aliasPattern.MatchString(a) || strings.HasPrefix(a, "alias/aws/")) {
		errs = append(errs, fmt.Errorf("ArchiveKeyAlias %q is not a customer alias (alias/<name>): a key is named by alias, never by ARN or key id", a))
	}
	if p.Storage == nil || (p.Storage.BucketArn == nil && p.Storage.External == nil) {
		errs = append(errs, errors.New("Storage is required (Storage.Grant())"))
	} else if p.Storage.External == nil && p.BlobBucketName == "" {
		errs = append(errs, errors.New("BlobBucketName is required (ports.blob.s3.bucket)"))
	}
	if p.State == nil {
		errs = append(errs, errors.New("State is required (States.Grant())"))
	} else {
		for _, m := range Modules() {
			if p.State.Tables[m] == nil {
				errs = append(errs, fmt.Errorf("State.Tables has no table for module %q: the role reads or writes all six", m))
			}
		}
	}
	if err := p.Telemetry.validate(); err != nil {
		errs = append(errs, err)
	}
	if len(p.AdditionalWebIdentityAudiences) > 0 {
		if p.Telemetry == nil {
			errs = append(errs, errors.New("AdditionalWebIdentityAudiences is for the telemetry layer: set Telemetry"))
		}
		seen := map[string]bool{}
		for i, aud := range p.AdditionalWebIdentityAudiences {
			switch {
			case strings.TrimSpace(aud) == "":
				errs = append(errs, fmt.Errorf("AdditionalWebIdentityAudiences[%d] is empty", i))
			case seen[aud]:
				errs = append(errs, fmt.Errorf("AdditionalWebIdentityAudiences[%d] %q is a duplicate", i, aud))
			}
			seen[aud] = true
		}
	}
	p.modules = ModuleSet{Instance: p.Instance, Tables: p.TableNames}
	if p.Instance != "" {
		if err := p.modules.validate(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("sluispulumi: %s arguments: %w", role, err)
	}
	if p.Archive.Region == "" {
		p.Archive.Region = p.Region
	}
	if p.Archive.AccountID == "" {
		p.Archive.AccountID = p.AccountID
	}
	if p.Archive.Prefix != "" {
		p.prefix = strings.Trim(p.Archive.Prefix, "/") + "/"
	}
	versioned := p.Archive.Versioned == nil || *p.Archive.Versioned
	if c := p.Archive.Create; c != nil {
		if !versioned {
			return nil, errors.New("sluispulumi: Archive.Create makes a bucket under Object Lock, which is versioned: Versioned false contradicts it")
		}
		if p.Archive.AccountID != p.AccountID {
			return nil, errors.New("sluispulumi: Archive.Create makes a bucket in the function's account and Archive.AccountID is another")
		}
	}
	p.retention = BackupRetentionArgs{Keep: 7, MaxAge: DefaultBackupMaxAge}
	if r := p.Retention; r != nil {
		if r.Keep < 0 || r.Keep > 1000 || r.MaxAge < 0 {
			return nil, errors.New("sluispulumi: Retention: Keep is 1 to 1000 and MaxAge is not negative")
		}
		if r.Keep > 0 {
			p.retention.Keep = r.Keep
		}
		if r.MaxAge > 0 {
			p.retention.MaxAge = r.MaxAge
		}
	}
	if c := p.Archive.Create; c != nil {
		days := int(p.retention.MaxAge / (24 * time.Hour))
		if c.RetentionDays != 0 {
			days = c.RetentionDays
		}
		switch {
		case days < 1:
			return nil, fmt.Errorf("sluispulumi: Archive.Create: the default retention is %d days and Object Lock takes at least 1 (Retention.MaxAge is %s)",
				days, p.retention.MaxAge)
		case time.Duration(days)*24*time.Hour > p.retention.MaxAge:
			return nil, fmt.Errorf("sluispulumi: Archive.Create: the default retention of %d days exceeds Retention.MaxAge %s: "+
				"a backup could not be deleted when the prune wants it gone", days, p.retention.MaxAge)
		}
		p.lockDays = days
		switch c.Mode {
		case "GOVERNANCE":
		case "", "COMPLIANCE":
			if !c.AcknowledgeCompliance {
				return nil, errors.New("sluispulumi: Archive.Create.Mode is COMPLIANCE (the default since v1.78; GOVERNANCE must be asked for) and it cannot be " +
					"shortened or removed by anybody, the account's root included: set AcknowledgeCompliance to say so, or Mode GOVERNANCE " +
					"to keep a bucket that was made under it (the lock of an existing bucket is not changed unless the arguments say so)")
			}
		default:
			return nil, fmt.Errorf("sluispulumi: Archive.Create.Mode %q is GOVERNANCE or COMPLIANCE", c.Mode)
		}
		if len(p.Archive.BreakglassRoleArns) == 0 {
			return nil, errors.New("sluispulumi: Archive.BreakglassRoleArns is required with Archive.Create: the bucket's policy denies the bypass of " +
				"the lock, its configuration and the deletion of versions to every principal but these roles (the break-glass role, and the role that applies the stack)")
		}
	}
	if err := checkKeyAdmins("Archive.BreakglassRoleArns", p.Archive.BreakglassRoleArns); err != nil {
		return nil, err
	}
	if p.AuditQueueURL != "" {
		arn, err := queueArnOf(p.AuditQueueURL)
		if err != nil {
			return nil, err
		}
		p.auditArn = arn
	}
	if p.FunctionName == "" {
		p.FunctionName = "sluis-" + p.Instance + "-" + role
	}
	if !functionNamePattern.MatchString(p.FunctionName) {
		return nil, fmt.Errorf("sluispulumi: FunctionName %q is letters, digits, - and _, at most 64", p.FunctionName)
	}
	if p.Function.MemoryMB == 0 {
		p.Function.MemoryMB = 512
	}
	if p.Function.TimeoutSeconds == 0 {
		p.Function.TimeoutSeconds = DefaultBackupTimeoutSeconds
	}
	if p.Function.TimeoutSeconds < 60 || p.Function.TimeoutSeconds > 900 {
		return nil, fmt.Errorf("sluispulumi: Function.TimeoutSeconds %d is 60 to 900", p.Function.TimeoutSeconds)
	}
	if p.LogRetentionDays == 0 {
		p.LogRetentionDays = 30
	}
	if err := resolvePackage(&p.BackupCommon); err != nil {
		return nil, err
	}
	if err := checkVersion(p.Package, p.PackageVersion); err != nil {
		return nil, err
	}
	if p.Artifacts != nil {
		norm, err := p.Artifacts.Normalize("sluis")
		if err != nil {
			return nil, fmt.Errorf("sluispulumi: %w", err)
		}
		p.Artifacts = &norm
	}
	p.archive = ArchiveGrant{
		Prefix: p.prefix, Instance: p.Instance, SSEKeyArn: p.Archive.SSEKeyArn, Versioned: versioned,
	}
	return p, nil
}

// resolvePackage fills what LambdaArgs.validate fills when Package is empty: the
// library's own release.
func resolvePackage(c *BackupCommon) error {
	if c.Package != "" {
		if c.PackageSHA256 == "" && (c.Release == nil || !c.Release.ResolveChecksums) {
			return errors.New("sluispulumi: PackageSHA256 is required (or Release.ResolveChecksums)")
		}
		return nil
	}
	v, err := artifact.ReleaseOf(c.Release, libraryModule, readBuildInfo, "Package")
	if err != nil {
		return fmt.Errorf("sluispulumi: Package: %w", err)
	}
	var base string
	if c.Release != nil {
		base = c.Release.BaseURL
	}
	c.Package = artifact.AssetURL(base, v, sluisconfig.ZipName(sluisconfig.ModuleBackup, v))
	if c.PackageVersion == "" {
		c.PackageVersion = v
	}
	if c.PackageSHA256 == "" {
		cp := artifact.Release{}
		if c.Release != nil {
			cp = *c.Release
		}
		cp.ResolveChecksums, cp.Version = true, v
		c.Release = &cp
	}
	return nil
}

// queueArnOf is the ARN of the SQS queue at a URL: https://sqs.<region>.amazonaws.com/<account>/<name>.
func queueArnOf(url string) (string, error) {
	rest, ok := strings.CutPrefix(url, "https://sqs.")
	if !ok {
		return "", fmt.Errorf("sluispulumi: AuditQueueURL %q is not an SQS queue URL", url)
	}
	host, path, _ := strings.Cut(rest, "/")
	region, _, _ := strings.Cut(host, ".")
	parts := strings.Split(path, "/")
	if region == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("sluispulumi: AuditQueueURL %q is not an SQS queue URL", url)
	}
	return arnPrefix + "sqs:" + region + ":" + parts[0] + ":" + parts[1], nil
}

// durationText is a duration as the document spells it: whole hours as `720h`.
func durationText(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return d.String()
}

// document renders the `sluis-backup/v1` document of the role. The backup
// function's lists its age recipients; the restore function's names the secret
// that holds the identity.
func (p *backupPlan) document(recipients []string) (string, error) {
	tables := map[Module]string{}
	for _, m := range Modules() {
		tables[m] = p.modules.TableName(m)
	}
	ports, err := RenderPorts(PortsArgs{Region: p.Region, BucketName: orBlank(p.BlobBucketName, p.Storage), BlobPrefix: p.BlobPrefix, Tables: tables})
	if err != nil {
		return "", err
	}
	pb := ports["ports"].(map[string]any)
	if b := p.Storage.External; b != nil {
		pb["blob"] = b.portsBlob(RoleBackup)
	}
	secrets := map[string]any{"source": "ssm", "root": SSMRoot(p.Instance), "region": p.Region, "layout": string(LayoutV5)}
	if p.ParameterKeyArn != "" {
		secrets["kmsKeyId"] = p.ParameterKeyArn
	}
	target := map[string]any{"bucket": p.Archive.Bucket, "region": p.Archive.Region}
	if pre := strings.Trim(p.Archive.Prefix, "/"); pre != "" {
		target["prefix"] = pre
	}
	if p.Archive.SSEKeyArn != "" {
		target["kmsKey"] = p.Archive.SSEKeyArn
	}
	bk := map[string]any{
		"region": p.Region, "target": target,
		"retention": map[string]any{"keep": p.retention.Keep, "maxAge": durationText(p.retention.MaxAge)},
	}
	if p.ArchiveKeyAlias != "" {
		// Deprecated, and rendered only while it is set: a binary of format v1
		// opens its archive key from it. Format v2 names no key.
		bk["key"] = p.ArchiveKeyAlias
	}
	if p.role == RoleRestore {
		bk["role"] = RoleRestore
		bk["identity"] = RestoreIdentityName
	} else {
		rs := make([]any, 0, len(recipients))
		for _, r := range recipients {
			rs = append(rs, r)
		}
		bk["recipients"] = rs
	}
	audit := map[string]any{"adapter": "log"}
	if p.AuditQueueURL != "" {
		audit = map[string]any{"adapter": "sqs", "settings": map[string]any{"queueURL": p.AuditQueueURL, "region": p.Region}}
	}
	doc := map[string]any{
		"apiVersion": sluisconfig.Group + "/" + backupDocumentVersion,
		"instance":   p.Instance,
		"secrets":    secrets,
		"ports":      pb,
		"platform":   map[string]any{"aws": true, "runtime": "lambda"},
		"preset":     "aws-serverless",
		// The preset's trigger is `invoke`, which starts a controller function; the
		// backup and the restore notify none and the document names no function.
		"adapters": map[string]any{"audit": audit, "trigger": map[string]any{"adapter": "memory"}},
		"backup":   bk,
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("sluispulumi: render the %s document: %w", backupDocumentVersion, err)
	}
	if err := sluisconfig.Validate("sluis-backup", doc); err != nil {
		return "", fmt.Errorf("sluispulumi: the %s document: %w", backupDocumentVersion, err)
	}
	return string(raw), nil
}

func orBlank(name string, s *StorageGrant) string {
	if s != nil && s.External != nil {
		return s.External.Bucket
	}
	return name
}

// moduleEnv is the role's input once the ARNs are known.
type backupRefs struct {
	bucketArn, tableKey, logGroup, queueArn, archiveKey, archiveBucket string
	tables                                                             map[Module]string
}

func (p *backupPlan) env(r backupRefs) ModuleEnv {
	return ModuleEnv{
		Region: p.Region, Account: p.AccountID, Modules: p.modules,
		BucketArn: r.bucketArn, BlobKeyPrefix: p.BlobPrefix, TableArns: r.tables, TableKeyArn: r.tableKey,
		ParameterKeyArn: p.ParameterKeyArn, QueueArn: r.queueArn,
		LogGroupArns: map[string]string{p.role: r.logGroup}, MinterRefs: p.MinterRefs,
		TelemetryAudiences: p.telemetryAudiences(),
	}
}

func (p *backupPlan) grant(r backupRefs) ArchiveGrant {
	g := p.archive
	g.BucketArn, g.KeyArn = r.archiveBucket, r.archiveKey
	return g
}

// telemetryAudiences are the audiences the role asks STS for, with Telemetry.
func (p *backupPlan) telemetryAudiences() []string {
	if p.Telemetry == nil {
		return nil
	}
	return p.AdditionalWebIdentityAudiences
}

// statementsFor renders the role's statements from the resolved references.
type statementsFor func(env ModuleEnv, g ArchiveGrant) ([]map[string]any, error)

// moduleFunctionArgs maps the plan onto the generic builder. The role's policy
// is the statements rendered from the resolved references. doc is the document
// when it is known at plan; docOut is it when it holds an apply-time value.
func (p *backupPlan) moduleFunctionArgs(name, doc string, docOut pulumi.StringInput, archiveBucketArn pulumi.StringInput,
	callers, noRetry []string, omitLive bool, statements statementsFor) *ModuleFunctionArgs {
	docs := map[string]string{}
	if docOut == nil {
		docs[docSluis] = doc
	}
	return &ModuleFunctionArgs{
		Name: name, FunctionName: p.FunctionName,
		Package: p.Package, PackageSHA256: p.PackageSHA256, PackageVersion: p.PackageVersion, Release: p.Release,
		Artifacts:           p.Artifacts,
		Documents:           docs,
		SluisDocumentOutput: docOut,
		DocumentVersion:     backupDocumentVersion,
		MemoryMB:            p.Function.MemoryMB, TimeoutSeconds: p.Function.TimeoutSeconds,
		LogRetentionDays: p.LogRetentionDays, PermissionsBoundaryArn: p.PermissionsBoundaryArn, Tags: p.Tags,
		Callers: callers, NoRetry: noRetry, OmitLive: omitLive,
		CodeSigningConfigArn: p.CodeSigningConfigArn,
		Telemetry:            p.Telemetry,
		Policy: func(logGroup pulumi.StringOutput) pulumi.StringInput {
			bucket := p.Storage.BucketArn
			if bucket == nil {
				bucket = pulumi.String("")
			}
			tableKey := pulumi.StringInput(pulumi.String(""))
			if p.State.KeyArn != nil {
				tableKey = p.State.KeyArn
			}
			queue := pulumi.String(p.auditArn)
			return pulumi.All(bucket, tableKey, logGroup, queue, archiveBucketArn, moduleTableArns(p.State)).ApplyT(func(v []any) (string, error) {
				r := backupRefs{
					bucketArn: v[0].(string), tableKey: v[1].(string), logGroup: v[2].(string), queueArn: v[3].(string),
					archiveBucket: v[4].(string), tables: moduleTablesOf(v[5]),
				}
				st, err := statements(p.env(r), p.grant(r))
				if err != nil {
					return "", err
				}
				return document(st)
			}).(pulumi.StringOutput)
		},
	}
}

// buildFunction makes the function of the role on the generic builder. With
// recipients (the backup role) the document is an output: the generated
// recipient is known at apply.
func buildFunction(ctx *pulumi.Context, name string, p *backupPlan, archiveBucketArn pulumi.StringInput,
	callers, noRetry []string, omitLive bool, statements statementsFor, recipients pulumi.StringArrayInput, owner pulumi.Resource) (*backupFn, error) {
	var doc string
	var docOut pulumi.StringInput
	var err error
	if recipients == nil {
		doc, err = p.document(nil)
	} else {
		// Rendered once now with a stand-in recipient, so that the document the
		// schema refuses is refused at plan; the real one is rendered at apply.
		if _, err = p.document([]string{standInRecipient()}); err == nil {
			docOut = recipients.ToStringArrayOutput().ApplyT(func(rs []string) (string, error) { return p.document(rs) }).(pulumi.StringOutput)
		}
	}
	if err != nil {
		return nil, err
	}
	fn, err := newModuleFunction(ctx, p.moduleFunctionArgs(name, doc, docOut, archiveBucketArn, callers, noRetry, omitLive, statements), pulumi.Parent(owner))
	if err != nil {
		return nil, err
	}
	// The former archive key is only looked up to keep the output of the estates
	// that still export it.
	key := pulumi.String("").ToStringOutput()
	if p.ArchiveKeyAlias != "" {
		key = kms.LookupAliasOutput(ctx, kms.LookupAliasOutputArgs{Name: pulumi.String(p.ArchiveKeyAlias)}, pulumi.Parent(owner)).TargetKeyArn()
	}
	return &backupFn{moduleFunction: fn, archiveKey: key}, nil
}

// standInRecipient is a valid age recipient that stands in for the generated
// one while the document is checked at plan. It is never rendered into a
// function.
func standInRecipient() string {
	_, rcpt, _ := AgeKeypairFromSeed(make([]byte, 32))
	return rcpt
}

// backupFn is a module function with the archive key its role names.
type backupFn struct {
	*moduleFunction
	archiveKey pulumi.StringOutput
}

// NewBackup creates the backup function: the release zip with a `sluis-backup/v1`
// document in its configuration layer, a role that reads every module and
// writes the backup table and the archive path, one alias per caller class, a
// daily run and a resume schedule with a dead-letter queue, and three alarms.
// With Archive.Create it also creates the archive bucket.
func NewBackup(ctx *pulumi.Context, name string, args *BackupArgs, opts ...pulumi.ResourceOption) (*Backup, error) {
	if args == nil {
		return nil, errors.New("sluispulumi: BackupArgs is nil")
	}
	p, err := args.plan(RoleBackup)
	if err != nil {
		return nil, err
	}
	extra, err := ParseBackupRecipients(args.ExtraRecipients)
	if err != nil {
		return nil, err
	}
	sch := args.Schedule
	if sch.Daily == "" {
		sch.Daily = DefaultBackupSchedule
	}
	if !strings.HasPrefix(sch.Daily, "rate(") && !strings.HasPrefix(sch.Daily, "cron(") {
		return nil, fmt.Errorf("sluispulumi: Schedule.Daily %q is not an EventBridge Scheduler expression", sch.Daily)
	}
	out := &Backup{}
	if err := ctx.RegisterComponentResource(BackupType, name, out, opts...); err != nil {
		return nil, err
	}
	parent := pulumi.Parent(out)

	// ---- the archive bucket: created, or named.
	bucketName := pulumi.String(p.Archive.Bucket).ToStringOutput()
	var bucketArn pulumi.StringInput = pulumi.String(arnPrefix + "s3:::" + p.Archive.Bucket)
	if p.Archive.Create != nil {
		b, err := newArchiveBucket(ctx, name, p, parent)
		if err != nil {
			return nil, err
		}
		bucketName, bucketArn = b.Bucket, b.Arn
	}
	out.ArchiveBucketName, out.ArchiveBucketArn = bucketName, bucketArn.ToStringOutput()

	// ---- the restore keypair: the recipient goes into the backup document, the
	// identity into the parameter only the restore role reads.
	ident, err := newRestoreIdentity(ctx, name, p, parent)
	if err != nil {
		return nil, err
	}
	recipients := ident.Recipient.ApplyT(func(r string) []string {
		return dedupeStrings(append([]string{r}, extra...))
	}).(pulumi.StringArrayOutput)
	out.RestoreRecipient, out.Recipients, out.RestoreIdentityParameter = ident.Recipient, recipients, ident.ParameterName

	fn, err := buildFunction(ctx, name, p, bucketArn, []string{ClassConsole, ClassAdmin, ClassBreakglass}, []string{LiveAlias}, false,
		BackupRoleStatements, recipients, out)
	if err != nil {
		return nil, err
	}
	live := fn.aliases[LiveAlias]
	out.FunctionArn, out.FunctionName = fn.fn.Arn, fn.fn.Name
	out.RoleArn, out.RoleName = fn.role.Arn, fn.role.Name
	out.LiveAliasArn, out.AliasArns = live.Arn, fn.aliasArns()
	out.ConfigLayerArn, out.ArchiveKeyArn = fn.layer.Arn, fn.archiveKey

	// ---- the cross-account bucket policy, as a document for the bucket's owner
	restoreRole := p.Archive.RestoreRoleArn
	if restoreRole == "" {
		restoreRole = arnPrefix + "iam::" + p.AccountID + ":role/sluis-" + p.Instance + "-" + RoleRestore
	}
	out.ArchiveBucketPolicy = pulumi.All(fn.role.Arn, bucketArn, fn.archiveKey).ApplyT(func(v []any) (string, error) {
		g := p.archive
		g.BucketArn, g.KeyArn = v[1].(string), v[2].(string)
		st, err := ArchiveBucketPolicyStatements(g, v[0].(string), restoreRole)
		if err != nil {
			return "", err
		}
		if len(p.Archive.BreakglassRoleArns) > 0 {
			guard, err := ArchiveBucketGuardStatement(g.BucketArn, p.Archive.BreakglassRoleArns)
			if err != nil {
				return "", err
			}
			st = append(st, guard)
		}
		return document(st)
	}).(pulumi.StringOutput)

	// ---- the schedules and their dead-letter queue
	if err := newBackupSchedules(ctx, name, p, sch, out, parent); err != nil {
		return nil, err
	}

	// ---- the alarms
	alarms, err := newBackupAlarms(ctx, name, p, fn, parent)
	if err != nil {
		return nil, err
	}
	out.AlarmNames = alarms.ToStringArrayOutput()

	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{
		"functionArn": out.FunctionArn, "functionName": out.FunctionName, "roleArn": out.RoleArn, "roleName": out.RoleName,
		"liveAliasArn": out.LiveAliasArn, "aliasArns": out.AliasArns, "configLayerArn": out.ConfigLayerArn,
		"schedulerRoleArn": out.SchedulerRoleArn, "scheduleNames": out.ScheduleNames, "scheduleDlqArn": out.ScheduleDLQArn,
		"archiveBucketName": out.ArchiveBucketName, "archiveBucketArn": out.ArchiveBucketArn,
		"archiveBucketPolicy": out.ArchiveBucketPolicy, "archiveKeyArn": out.ArchiveKeyArn, "alarmNames": out.AlarmNames,
		"restoreRecipient": out.RestoreRecipient, "recipients": out.Recipients, "restoreIdentityParameter": out.RestoreIdentityParameter,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// dedupeStrings drops repeats, keeping the first of each.
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
