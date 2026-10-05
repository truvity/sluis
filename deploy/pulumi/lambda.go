package sluispulumi

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/apigatewayv2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/kms"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ssm"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/sluis/internal/config"
)

// LambdaType is the Pulumi type token of the Lambda component.
const LambdaType = "sluis:aws:Lambda"

// DefaultSigningKeyAlias is the token-signing key's alias when
// LambdaArgs.SigningKeyAlias is empty.
const DefaultSigningKeyAlias = "alias/sluis-signing"

// DefaultSigningKeyRS256Alias is the RSA signing key's alias when
// LambdaArgs.SigningKeyRS256Alias is empty.
const DefaultSigningKeyRS256Alias = "alias/sluis-signing-rs256"

// DefaultWrappedSigningKeyAlias is the symmetric key's alias when
// WrappedSigningArgs.KeyAlias is empty and the library creates the key.
const DefaultWrappedSigningKeyAlias = "alias/sluis-signing-wrapped"

// StateSecretParameterName is the SSM parameter of the issuer's state secret,
// `/sluis/<instance>/private/config/issuer/state-secret`.
func StateSecretParameterName(instance string) string {
	return ConfigParameterPrefix(instance) + "/" + stateSecretName
}

// RecoveryPasswordParameterName is the SSM parameter of the recovery password,
// `/sluis/<instance>/private/config/recovery/password`: a SecureString the
// library generates and keeps across applies, which the http document names
// (`recovery.passwordSecret`) and its `secrets` source reads.
func RecoveryPasswordParameterName(instance string) string {
	return ConfigParameterPrefix(instance) + "/" + recoveryPasswordName
}

// DefaultSchedule is the controllers' tick when LambdaArgs.Schedule.Rate is empty.
const DefaultSchedule = "rate(5 minutes)"

// LambdaArgs is the whole Lambda shape of sluis: ONE function from the release
// zip with ONE role, the HTTP API in front of it, the token-signing key and one
// schedule per controller target. The function serves the issuer and the console
// and runs the controllers' passes; the controllers' code runs with the one
// role's permissions (owner decision S1a, 2026-10-05).
type LambdaArgs struct {
	// Region and AccountID name the SSM parameters and the functions in the
	// roles' policies. Required.
	Region    string
	AccountID string
	// Instance is the installation's name (`hive`, `kernel`): its SSM root is
	// `/sluis/<instance>` (layout v3), so two installations share an account.
	// Lower-case letters, digits and dashes. Required.
	Instance string

	// Package is the released zip, `sluis-lambda_<version>_linux_arm64.zip`, with
	// `bootstrap` at its root: a path on disk or an https URL, read when the
	// stack is evaluated. It is the functions' code byte for byte: nothing is
	// added to it. Required.
	Package string
	// PackageSHA256 is the zip's SHA-256, in hex, from the release's checksums.
	// Required: a package that does not have it is refused.
	PackageSHA256 string
	// PackageVersion is the release the package is, when its file name does not
	// say (sluis-lambda_<version>_linux_<arch>.zip). A package older than this
	// library is refused: it cannot read the configuration layer.
	PackageVersion string

	// Config is the service document (v3, apiVersion sluis.truvity.github.io/
	// sluis/v3: the serve keys and, under `controllers`, the controllers the
	// function runs), which the configuration layer holds at
	// /opt/sluis/sluis.yaml; the function's SLUIS_CONFIG names it. Required. It
	// holds no secret: a secret is named, and its `secrets` source reads it from
	// /sluis/<instance>/private/config/.
	//
	// The library writes what is its own into it: the apiVersion, `policy.file`,
	// `secrets` ({source: ssm, root: /sluis/<instance>, region}),
	// `recovery.passwordSecret`, `recovery.enabled` (from Recovery), the state
	// secret's name under `signingKey.kms` or `.kmsWrapped`, and, when
	// `adapters.trigger` is `invoke`, the function it invokes for a run-now
	// (this very function: `github` and `slack` settings). A different value
	// written for one of them is refused.
	Config string
	// Policy is the policy document (apiVersion sluis.truvity.github.io/policy/v2,
	// or a v1 policy file): the layer holds it at /opt/sluis/policy.yaml.
	// PolicyPath is instead a file or a directory of layers, rendered by sluis's
	// own renderer (`sluisctl policy render`). Exactly one is required.
	Policy     string
	PolicyPath string

	// Storage is the blob bucket (Storage.Grant()). Required.
	Storage *StorageGrant
	// State is the DynamoDB table (State.Grant()). Required: the function keeps
	// State in it.
	State *StateGrant
	// AuditQueueArn is the audit stack's ingest queue; the function may send to
	// it. Required.
	AuditQueueArn pulumi.StringInput

	// ParameterKeyArn is the customer-managed key SecureString parameters under
	// /sluis are encrypted with. Default none: the AWS-managed key, which needs no
	// grant. With a key, the function may use it through SSM only.
	ParameterKeyArn string

	// SigningKeyAlias is the token-signing key's alias. Default
	// DefaultSigningKeyAlias. It must start with "alias/".
	SigningKeyAlias string
	// SigningKeyRS256Alias is the alias of the second signing key, `RSA_3072`
	// and `SIGN_VERIFY`, which both estates sign RS256 tokens with beside the
	// ES384 key. Default DefaultSigningKeyRS256Alias. It must start with
	// "alias/". DisableSigningKeyRS256 leaves the key out (default: created).
	SigningKeyRS256Alias   string
	DisableSigningKeyRS256 bool

	// WrappedSigning switches token signing to the `kms-wrapped` adapter: ONE
	// symmetric KMS key, from which the issuer generates and wraps its signing
	// key pairs (`signingKey.kmsWrapped` in the function's configuration
	// names the key). Nil keeps remote signing with the two asymmetric keys
	// above. With it set the asymmetric keys are not declared: a stack that
	// signed remotely before unprotects them (`pulumi state unprotect`) and the
	// next apply schedules their deletion.
	WrappedSigning *WrappedSigningArgs

	// FunctionNamePrefix starts the names of what is not the function: the
	// configuration layer (`<prefix>-config`), the API, the schedules
	// (`<prefix>-<kind>-<target>`, `<prefix>-exports`, `<prefix>-directory-
	// refresh`) and the scheduler's role. Default "sluis".
	FunctionNamePrefix string
	// FunctionName is the function's name, and the name of its role, its role
	// policy and its log group (`/aws/lambda/<name>`). Default FunctionNamePrefix:
	// ONE function, `sluis`. An installation that ran the three functions of
	// v1.62 and wants this one to be the function it already has, with its role,
	// log group and API integration kept in place and nothing replaced, sets it to
	// `<prefix>-http` (the resources keep their logical names, so Pulumi's state
	// carries over). Left at the default, the function, its role and its log group
	// are replaced under the new name (the new ones are created before the old are
	// deleted) and the old log group's events go with it.
	FunctionName string
	// Function tunes the function.
	Function FunctionArgs

	// LogRetentionDays is the log group's retention. Default 30.
	LogRetentionDays int
	// PermissionsBoundaryArn is the boundary of every role. Default none.
	PermissionsBoundaryArn string

	// Recovery is the recovery sign-in. Default (nil): enabled.
	Recovery *RecoveryArgs
	// API is the HTTP API in front of the function. Required.
	API APIArgs
	// Schedule is the controllers' tick: one schedule per target.
	Schedule ScheduleArgs
	// Exports is the schedule that runs the exports.
	Exports ExportsArgs
	// DirectoryRefresh is the schedule that refreshes the directory's snapshots.
	DirectoryRefresh DirectoryRefreshArgs
	// WebIdentityAudience restricts the audience of the outbound web identity
	// token the function's role may ask STS for (`sts:IdentityTokenAudience`),
	// normally the console's URL. Empty allows any audience. The role holds
	// `sts:GetWebIdentityToken` either way (the controllers read the console with
	// it), and the account must have outbound identity federation enabled.
	WebIdentityAudience string
	// Telemetry is the OpenTelemetry layer. Nil: no layer and no OTEL
	// environment, which is how an estate whose collector is not ready runs.
	Telemetry *TelemetryArgs

	// Tags are put on everything that takes tags. Default none.
	Tags map[string]string

	// AllowEndpoints lets the documents name a service endpoint
	// (`secrets.endpoint`, `ports.dynamodb.endpoint`, any `endpoint`), for a
	// test against LocalStack. Off, a document naming one is refused: an
	// endpoint the documents point at is where the function reads its secrets
	// and its State from, and a forged one would serve forged secrets.
	AllowEndpoints bool
}

// WrappedSigningArgs is the symmetric key of the `kms-wrapped` signing adapter.
// The function, and only it, may use it, and only to generate a data key
// pair and to decrypt a private key, with the encryption context
// purpose=sluis-signing (and no keys beside purpose, alg and kid).
type WrappedSigningArgs struct {
	// KeyArn is an existing symmetric key (SYMMETRIC_DEFAULT, ENCRYPT_DECRYPT),
	// which other workloads may share (an auto-unseal key). Unset, the library
	// creates the key, with rotation enabled, protected, and a key policy that
	// reserves the signing encryption context to the signing roles. With KeyArn
	// the library creates no key and leaves its policy alone, and the estate MUST
	// merge WrappedKeyPolicyStatements into it (docs/deployment/aws.md): without
	// it any principal that may kms:Decrypt on the key can unwrap a signing key
	// read from the State and forge tokens. A multi-Region key (mrk-...) is
	// accepted: the statements go in EVERY replica's key policy, since a wrapped
	// key made with the primary decrypts on any replica. Principals with
	// kms:PutKeyPolicy on a shared key are inside the signing trust boundary.
	KeyArn pulumi.StringInput
	// AdditionalSigningRoleArns are the roles beside the function's that
	// sign with the key, for the key policy: the Kubernetes serve role, when
	// KubernetesIdentityArgs.WrappedSigningKeyArn names this key. Ignored with
	// KeyArn.
	AdditionalSigningRoleArns []string
	// KeyAlias is the created key's alias. Default DefaultWrappedSigningKeyAlias.
	// It must start with "alias/". Ignored with KeyArn.
	KeyAlias string
}

// ExportsArgs is the exports schedule: one EventBridge schedule invoking the
// function with `{"kind":"exports"}`.
type ExportsArgs struct {
	// Disabled leaves the schedule out.
	Disabled bool
	// Rate is the schedule expression. Default DefaultExportsSchedule.
	Rate string
}

// DefaultExportsSchedule is the exports' tick when ExportsArgs.Rate is empty.
const DefaultExportsSchedule = "rate(15 minutes)"

// DirectoryRefreshArgs is the directory refresh schedule: one EventBridge
// schedule invoking the function with `{"kind":"refresh"}`.
//
// Lambda has no loop to take a new snapshot of each connected directory, and a
// snapshot older than the freshness window (30 minutes) stops being
// authoritative, which sign-in refuses on. A request that finds one stale
// refreshes it too, so the schedule keeps it warm rather than being the only
// guarantee.
type DirectoryRefreshArgs struct {
	// Disabled leaves the schedule out.
	Disabled bool
	// Rate is the schedule expression. Default DefaultDirectoryRefreshSchedule.
	Rate string
}

// DefaultDirectoryRefreshSchedule is the directory refresh's tick when
// DirectoryRefreshArgs.Rate is empty: the hub's refresh interval.
const DefaultDirectoryRefreshSchedule = "rate(15 minutes)"

// FunctionArgs tunes the function.
type FunctionArgs struct {
	// MemoryMB defaults to 512: one setting for a request and for a pass.
	MemoryMB int
	// TimeoutSeconds defaults to 300, which a pass over a whole organisation
	// needs. API Gateway cuts a request at 30 seconds whatever this says.
	TimeoutSeconds int
}

// RecoveryArgs is the recovery sign-in: the way in for the day no directory can
// vouch for anybody, which on Lambda is a generated password at
// /sluis/<instance>/private/config/recovery/password.
//
// The password and its parameter exist whatever Enabled says, so that turning
// recovery off and on again is a configuration change and never a rotation.
type RecoveryArgs struct {
	// Enabled writes `recovery.enabled` into the function's configuration.
	// Default nil: recovery is on, which is what a first installation needs (the
	// console is signed in to with it until a directory is connected). Turn it
	// off once a directory works, with a pointer to false: the sign-in is then
	// refused with a message and the parameter is kept. A `recovery.enabled`
	// already in Config must agree with it.
	Enabled *bool
}

// APIArgs is the HTTP API (payload format 2.0) in front of the function, and its
// custom domain.
type APIArgs struct {
	// DomainName is the custom domain. Required.
	DomainName string
	// CertificateArn is the ACM certificate for DomainName, in the function's
	// region. The caller supplies it, e.g. a Cloudflare Origin CA certificate
	// imported to ACM. Required.
	CertificateArn pulumi.StringInput
	// TruststorePEM is the PEM bundle of the CAs a client certificate must chain
	// to: mutual TLS on the custom domain, for Cloudflare's authenticated origin
	// pulls. The library uploads it to its own bucket. Required.
	TruststorePEM string
	// TruststoreBucketName is the bucket the truststore goes in: its own, and not
	// the blob bucket, which the functions can write. Required, and global.
	TruststoreBucketName string
	// KeepDefaultEndpoint leaves the default `execute-api` endpoint enabled, for
	// the cutover's acceptance suite to run against APIURL before the DNS
	// switch. Default false: only the custom domain serves, and the default
	// endpoint would be a way round the client certificate.
	KeepDefaultEndpoint bool
}

// ScheduleArgs is the controllers' ticks.
type ScheduleArgs struct {
	// GitHubOrgs and SlackWorkspaces are the targets: one schedule each, which
	// invokes the function with `{"kind":"tick","target":"<id>"}`; the function
	// runs the pass under the controller the policy says the target belongs to.
	GitHubOrgs      []string
	SlackWorkspaces []string
	// Rate is the EventBridge Scheduler expression. Default DefaultSchedule.
	Rate string
}

// TelemetryArgs is the observability otlp-lambda layer, which is optional: an
// estate whose collector is not ready leaves it nil.
type TelemetryArgs struct {
	// LayerArn is the layer version, published in this account and region.
	// Required with Telemetry.
	LayerArn pulumi.StringInput
	// Env is the OTEL_* and layer settings (the endpoint, the protocol): with
	// SLUIS_CONFIG, the whole of the function's environment. The library adds
	// OTEL_SERVICE_NAME, the function's name, unless it is here.
	Env map[string]string
}

// Lambda is the component. Its fields are the outputs.
type Lambda struct {
	pulumi.ResourceState

	// SigningKeyArn, SigningKeyID and SigningKeyAlias are the token-signing key.
	SigningKeyArn   pulumi.StringOutput
	SigningKeyID    pulumi.StringOutput
	SigningKeyAlias pulumi.StringOutput
	// SigningKeyRS256Arn, SigningKeyRS256ID and SigningKeyRS256Alias are the RSA
	// signing key; empty with DisableSigningKeyRS256.
	SigningKeyRS256Arn   pulumi.StringOutput
	SigningKeyRS256ID    pulumi.StringOutput
	SigningKeyRS256Alias pulumi.StringOutput
	// WrappedSigningKeyArn and WrappedSigningKeyAlias are the symmetric key of
	// WrappedSigning: the key the library created, or KeyArn; the alias is empty
	// with KeyArn and without WrappedSigning.
	WrappedSigningKeyArn   pulumi.StringOutput
	WrappedSigningKeyAlias pulumi.StringOutput

	// The function and its role.
	FunctionArn, FunctionName pulumi.StringOutput
	RoleArn, RoleName         pulumi.StringOutput

	// APIID and APIURL are the HTTP API and its default endpoint (which answers
	// only with API.KeepDefaultEndpoint).
	APIID  pulumi.StringOutput
	APIURL pulumi.StringOutput
	// DomainTarget and DomainHostedZoneID are what DNS for the custom domain
	// points at (a CNAME, or an alias record).
	DomainTarget       pulumi.StringOutput
	DomainHostedZoneID pulumi.StringOutput
	// TruststoreBucketName and TruststoreURI are where the client-CA bundle is.
	TruststoreBucketName pulumi.StringOutput
	TruststoreURI        pulumi.StringOutput

	// SchedulerRoleArn is the role EventBridge Scheduler assumes.
	SchedulerRoleArn pulumi.StringOutput
	// ScheduleNames are the schedules, in the order GitHub then Slack targets.
	ScheduleNames pulumi.StringArrayOutput

	// ConfigLayerArn is the configuration layer's version ARN: the service
	// document and the policy, published immutable and mounted LAST. A change
	// publishes a new version and updates the function; an old version is kept,
	// so re-pointing the function at it is a rollback.
	ConfigLayerArn pulumi.StringOutput

	// StateSecretParameter is the name of the SSM SecureString that holds the
	// issuer's OAuth-state secret, `/sluis/<instance>/private/config/issuer/state-secret`: 32
	// random bytes, base64. The library generates it and keeps it across applies.
	StateSecretParameter pulumi.StringOutput

	// RecoveryPasswordParameter is the name of the SSM SecureString that holds the
	// recovery password, `/sluis/<instance>/private/config/recovery/password`: 40 random
	// letters and digits with no look-alikes. Only the name is an output, never the
	// value; an operator reads it with `aws ssm get-parameter --with-decryption`.
	RecoveryPasswordParameter pulumi.StringOutput

	// ExportReadPolicyJSON is the IAM policy document a consumer's External
	// Secrets Operator role attaches: read on /sluis/<instance>/export/* and
	// nothing else.
	ExportReadPolicyJSON pulumi.StringOutput
}

var functionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// A target is a login or a workspace key, or `github:links` (the link check).
var targetID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,40}$`)

// remoteSigning is whether the two asymmetric signing keys are created: always,
// but with WrappedSigning, which replaces them.
func (a *LambdaArgs) remoteSigning() bool { return a.WrappedSigning == nil }

func (a *LambdaArgs) validate() (LambdaArgs, error) {
	if a == nil {
		return LambdaArgs{}, errors.New("sluispulumi: LambdaArgs is nil")
	}
	out := *a
	var missing []string
	for k, v := range map[string]string{
		"Region": out.Region, "AccountID": out.AccountID, "Instance": out.Instance,
		"Package": out.Package, "PackageSHA256": out.PackageSHA256, "Config": strings.TrimSpace(out.Config),
		"API.DomainName": out.API.DomainName, "API.TruststorePEM": strings.TrimSpace(out.API.TruststorePEM),
		"API.TruststoreBucketName": out.API.TruststoreBucketName,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	for k, nilIn := range map[string]bool{
		"API.CertificateArn": out.API.CertificateArn == nil, "AuditQueueArn": out.AuditQueueArn == nil,
		"Storage": out.Storage == nil || out.Storage.BucketArn == nil, "State": out.State == nil || out.State.TableArn == nil,
	} {
		if nilIn {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return out, fmt.Errorf("sluispulumi: LambdaArgs: required and empty: %v", sortedStrings(missing))
	}
	if !validInstance(out.Instance) {
		return out, fmt.Errorf("sluispulumi: LambdaArgs.Instance %q is lower-case letters, digits and dashes, at most 32, "+
			"and not private or export", out.Instance)
	}
	if (strings.TrimSpace(out.Policy) == "") == (out.PolicyPath == "") {
		return out, errors.New("sluispulumi: LambdaArgs: set exactly one of Policy and PolicyPath")
	}
	if err := checkVersion(out.Package, out.PackageVersion); err != nil {
		return out, err
	}
	if out.SigningKeyAlias == "" {
		out.SigningKeyAlias = DefaultSigningKeyAlias
	}
	if out.SigningKeyRS256Alias == "" {
		out.SigningKeyRS256Alias = DefaultSigningKeyRS256Alias
	}
	if w := out.WrappedSigning; w != nil {
		if w.KeyAlias == "" {
			cp := *w
			cp.KeyAlias = DefaultWrappedSigningKeyAlias
			out.WrappedSigning = &cp
			w = &cp
		}
		if w.KeyArn == nil && (!strings.HasPrefix(w.KeyAlias, "alias/") || len(w.KeyAlias) == len("alias/")) {
			return out, fmt.Errorf("sluispulumi: LambdaArgs.WrappedSigning.KeyAlias %q must start with \"alias/\"", w.KeyAlias)
		}
	}
	if out.remoteSigning() {
		if !out.DisableSigningKeyRS256 && (!strings.HasPrefix(out.SigningKeyRS256Alias, "alias/") || len(out.SigningKeyRS256Alias) == len("alias/")) {
			return out, fmt.Errorf("sluispulumi: LambdaArgs.SigningKeyRS256Alias %q must start with \"alias/\"", out.SigningKeyRS256Alias)
		}
		if !out.DisableSigningKeyRS256 && out.SigningKeyRS256Alias == out.SigningKeyAlias {
			return out, errors.New("sluispulumi: LambdaArgs.SigningKeyRS256Alias is the ES384 key's alias too")
		}
		if !strings.HasPrefix(out.SigningKeyAlias, "alias/") || len(out.SigningKeyAlias) == len("alias/") {
			return out, fmt.Errorf("sluispulumi: LambdaArgs.SigningKeyAlias %q must start with \"alias/\"", out.SigningKeyAlias)
		}
	}
	if out.FunctionNamePrefix == "" {
		out.FunctionNamePrefix = "sluis"
	}
	if out.FunctionName == "" {
		out.FunctionName = out.FunctionNamePrefix
	}
	if !functionNamePattern.MatchString(out.FunctionName) {
		return out, fmt.Errorf("sluispulumi: LambdaArgs.FunctionName %q is letters, digits, - and _, at most 64", out.FunctionName)
	}
	if out.LogRetentionDays == 0 {
		out.LogRetentionDays = 30
	}
	if out.Function.MemoryMB == 0 {
		out.Function.MemoryMB = 512
	}
	if out.Function.TimeoutSeconds == 0 {
		out.Function.TimeoutSeconds = 300
	}
	if out.Exports.Rate == "" {
		out.Exports.Rate = DefaultExportsSchedule
	}
	if r := out.Exports.Rate; !strings.HasPrefix(r, "rate(") && !strings.HasPrefix(r, "cron(") {
		return out, fmt.Errorf("sluispulumi: Exports.Rate %q is not an EventBridge Scheduler expression", r)
	}
	if out.DirectoryRefresh.Rate == "" {
		out.DirectoryRefresh.Rate = DefaultDirectoryRefreshSchedule
	}
	if r := out.DirectoryRefresh.Rate; !strings.HasPrefix(r, "rate(") && !strings.HasPrefix(r, "cron(") {
		return out, fmt.Errorf("sluispulumi: DirectoryRefresh.Rate %q is not an EventBridge Scheduler expression", r)
	}
	if out.Schedule.Rate == "" {
		out.Schedule.Rate = DefaultSchedule
	}
	if r := out.Schedule.Rate; !strings.HasPrefix(r, "rate(") && !strings.HasPrefix(r, "cron(") && !strings.HasPrefix(r, "at(") {
		return out, fmt.Errorf("sluispulumi: Schedule.Rate %q is not an EventBridge Scheduler expression", r)
	}
	for kind, ids := range map[string][]string{"GitHubOrgs": out.Schedule.GitHubOrgs, "SlackWorkspaces": out.Schedule.SlackWorkspaces} {
		seen := map[string]bool{}
		for _, id := range ids {
			if !targetID.MatchString(id) {
				return out, fmt.Errorf("sluispulumi: Schedule.%s: %q is not a target id (letters, digits, - _ ., at most 40)", kind, id)
			}
			if seen[id] {
				return out, fmt.Errorf("sluispulumi: Schedule.%s: %q is listed twice", kind, id)
			}
			seen[id] = true
		}
	}
	if t := out.Telemetry; t != nil {
		if t.LayerArn == nil {
			return out, errors.New("sluispulumi: Telemetry.LayerArn is required with Telemetry")
		}
		for k := range t.Env {
			if !telemetryVariable(k) {
				return out, fmt.Errorf("sluispulumi: Telemetry.Env: %s is not a telemetry setting: the environment holds OTEL_*, "+
					"the telemetry layer's own (ACCESS_ROSTER_*, OPENTELEMETRY_*) and AWS_LAMBDA_EXEC_WRAPPER, and nothing else", k)
			}
		}
	}
	return out, nil
}

// NewLambda creates the Lambda shape. The function is the release zip's
// `bootstrap` (provided.al2023, arm64, no VPC), with ONE role:
//
//   - logs to its own group; S3 on the blob bucket; DynamoDB on the table; SSM
//     read and write under /sluis/<instance>/private/credentials/* and
//     /sluis/<instance>/export/* (and read of what it wrote there, for the exports
//     pass); SSM read under /sluis/<instance>/private/config/* (the secrets its
//     document names); sqs:SendMessage on the audit queue;
//   - kms:Sign and kms:GetPublicKey on the signing keys (remote signing) or, with
//     WrappedSigning, kms:GenerateDataKeyPairWithoutPlaintext and kms:Decrypt on
//     the symmetric key under the encryption context purpose=sluis-signing;
//   - lambda:InvokeFunction on itself (run a pass now) and sts:GetWebIdentityToken
//     (the controllers read the console with it).
//
// The controllers' code runs with this role: there is no per-role isolation
// between the issuer and a controller (owner decision S1a, 2026-10-05).
//
// The API is an HTTP API with payload format 2.0 and a $default route to the
// function, behind a regional custom domain with mutual TLS. The controllers'
// passes, the exports and the directory refresh are invoked by EventBridge
// schedules, through a role of their own that may invoke only this function.
func NewLambda(ctx *pulumi.Context, name string, args *LambdaArgs, opts ...pulumi.ResourceOption) (*Lambda, error) {
	a, err := args.validate()
	if err != nil {
		return nil, err
	}
	pkg, err := loadPackage(a.Package, a.PackageSHA256)
	if err != nil {
		return nil, err
	}
	docs, err := renderDocuments(&a)
	if err != nil {
		return nil, err
	}
	out := &Lambda{}
	if err := ctx.RegisterComponentResource(LambdaType, name, out, opts...); err != nil {
		return nil, err
	}
	child := pulumi.Parent(out)
	tags := tagMap(a.Tags)

	// ---- the signing keys: remote (two asymmetric keys, KMS signs) or wrapped
	// (one symmetric key, the issuer signs with the key pairs it generates under it)
	rsEmpty := pulumi.String("").ToStringOutput()
	sgArn, sgID, sgAlias := rsEmpty, rsEmpty, rsEmpty
	rsArn, rsID, rsAlias := rsEmpty, rsEmpty, rsEmpty
	var signingArns []pulumi.StringInput
	if a.remoteSigning() {
		key, err := kms.NewKey(ctx, name+"-signing-key", &kms.KeyArgs{
			Description:           pulumi.String(name + " token signing: the issuer signs its tokens with it"),
			KeyUsage:              pulumi.String("SIGN_VERIFY"),
			CustomerMasterKeySpec: pulumi.String("ECC_NIST_P384"),
			DeletionWindowInDays:  pulumi.Int(30),
			Tags:                  tags,
		}, child, pulumi.Protect(true))
		if err != nil {
			return nil, fmt.Errorf("sluis signing key: %w", err)
		}
		alias, err := kms.NewAlias(ctx, name+"-signing-alias", &kms.AliasArgs{
			Name: pulumi.String(a.SigningKeyAlias), TargetKeyId: key.KeyId,
		}, child)
		if err != nil {
			return nil, fmt.Errorf("sluis signing alias: %w", err)
		}
		signingArns = append(signingArns, key.Arn)
		sgArn, sgID, sgAlias = key.Arn, key.KeyId, alias.Name
		if !a.DisableSigningKeyRS256 {
			rsKey, err := kms.NewKey(ctx, name+"-signing-key-rs256", &kms.KeyArgs{
				Description:           pulumi.String(name + " token signing: the issuer signs its RS256 tokens with it"),
				KeyUsage:              pulumi.String("SIGN_VERIFY"),
				CustomerMasterKeySpec: pulumi.String("RSA_3072"),
				DeletionWindowInDays:  pulumi.Int(30),
				Tags:                  tags,
			}, child, pulumi.Protect(true))
			if err != nil {
				return nil, fmt.Errorf("sluis RS256 signing key: %w", err)
			}
			rsAl, err := kms.NewAlias(ctx, name+"-signing-alias-rs256", &kms.AliasArgs{
				Name: pulumi.String(a.SigningKeyRS256Alias), TargetKeyId: rsKey.KeyId,
			}, child)
			if err != nil {
				return nil, fmt.Errorf("sluis RS256 signing alias: %w", err)
			}
			signingArns = append(signingArns, rsKey.Arn)
			rsArn, rsID, rsAlias = rsKey.Arn, rsKey.KeyId, rsAl.Name
		}
	}
	// The wrapped key. The http role is named by its (deterministic) ARN in the
	// key policy, which is what keeps the key from depending on the role.
	var wrappedArn pulumi.StringInput
	wrappedKeyArn, wrappedAlias := rsEmpty, rsEmpty
	if w := a.WrappedSigning; w != nil {
		if w.KeyArn != nil {
			wrappedArn = w.KeyArn
			wrappedKeyArn = pulumi.StringInput(w.KeyArn).ToStringOutput()
		} else {
			roles := append([]string{arnPrefix + "iam::" + a.AccountID + ":role/" + a.FunctionName},
				w.AdditionalSigningRoleArns...)
			policy, err := wrappedKeyPolicy(a.AccountID, roles)
			if err != nil {
				return nil, err
			}
			wk, err := kms.NewKey(ctx, name+"-signing-key-wrapped", &kms.KeyArgs{
				Description: pulumi.String(name + " token signing: wraps the key pairs the issuer signs its tokens with"),
				KeyUsage:    pulumi.String("ENCRYPT_DECRYPT"),
				// Automatic rotation is of the key material; the key id, which every
				// wrapped private key records, stays.
				CustomerMasterKeySpec: pulumi.String("SYMMETRIC_DEFAULT"),
				EnableKeyRotation:     pulumi.Bool(true),
				Policy:                pulumi.String(policy),
				DeletionWindowInDays:  pulumi.Int(30),
				Tags:                  tags,
			}, child, pulumi.Protect(true))
			if err != nil {
				return nil, fmt.Errorf("sluis wrapped signing key: %w", err)
			}
			wa, err := kms.NewAlias(ctx, name+"-signing-alias-wrapped", &kms.AliasArgs{
				Name: pulumi.String(w.KeyAlias), TargetKeyId: wk.KeyId,
			}, child)
			if err != nil {
				return nil, fmt.Errorf("sluis wrapped signing alias: %w", err)
			}
			wrappedArn, wrappedKeyArn, wrappedAlias = wk.Arn, wk.Arn, wa.Name
		}
	}

	// ---- the configuration layer: the two documents, immutable. A change is a
	// new version (and the functions move to it, every instance at once); the
	// old one is kept (SkipDestroy), which is what makes re-pointing a function
	// at it a rollback.
	layer, err := lambda.NewLayerVersion(ctx, name+"-config", &lambda.LayerVersionArgs{
		LayerName:               pulumi.String(a.FunctionNamePrefix + "-config"),
		Description:             pulumi.String(name + " configuration: the service document and the policy, at " + LayerRoot),
		CompatibleRuntimes:      pulumi.StringArray{pulumi.String("provided.al2023")},
		CompatibleArchitectures: pulumi.StringArray{pulumi.String("arm64")},
		Code: pulumi.NewAssetArchive(map[string]any{
			"sluis/" + docSluis + ".yaml":  pulumi.NewStringAsset(docs[docSluis]),
			"sluis/" + docPolicy + ".yaml": pulumi.NewStringAsset(docs[docPolicy]),
		}),
		SkipDestroy: pulumi.Bool(true),
	}, child)
	if err != nil {
		return nil, fmt.Errorf("sluis configuration layer: %w", err)
	}

	// ---- the function. The resources keep the logical names they had as the
	// `http` function's (`<name>-http`, `<name>-http-role`, `<name>-http-policy`),
	// so that with FunctionName `<prefix>-http` Pulumi's state carries over and
	// nothing is replaced.
	fnName := a.FunctionName
	fnArn := arnPrefix + "lambda:" + a.Region + ":" + a.AccountID + ":function:" + fnName
	logs, err := cloudwatch.NewLogGroup(ctx, name+"-http", &cloudwatch.LogGroupArgs{
		Name:            pulumi.String("/aws/lambda/" + fnName),
		RetentionInDays: pulumi.Int(a.LogRetentionDays),
		Tags:            tags,
	}, child)
	if err != nil {
		return nil, fmt.Errorf("sluis log group: %w", err)
	}
	role, err := newFunctionRole(ctx, name, fnName, &a, signingArns, wrappedArn, logs.Arn, fnArn, tags, child)
	if err != nil {
		return nil, fmt.Errorf("sluis role: %w", err)
	}
	env := pulumi.StringMap{}
	if t := a.Telemetry; t != nil {
		if _, has := t.Env["OTEL_SERVICE_NAME"]; !has {
			env["OTEL_SERVICE_NAME"] = pulumi.String(fnName)
		}
		for k, v := range t.Env {
			env[k] = pulumi.String(v)
		}
	}
	env[config.EnvConfig] = pulumi.String(DocumentPath(docSluis))

	// The configuration layer is LAST: a layer later in the list wins a path an
	// earlier one also writes, and nothing may write over the documents.
	layers := pulumi.StringArray{}
	if a.Telemetry != nil {
		layers = append(layers, a.Telemetry.LayerArn)
	}
	layers = append(layers, layer.Arn)
	fn, err := lambda.NewFunction(ctx, name+"-http", &lambda.FunctionArgs{
		Name:          pulumi.String(fnName),
		Role:          role.Arn,
		Runtime:       pulumi.String("provided.al2023"),
		Handler:       pulumi.String("bootstrap"),
		Architectures: pulumi.StringArray{pulumi.String("arm64")},
		Code:          pulumi.NewFileArchive(pkg),
		MemorySize:    pulumi.Int(a.Function.MemoryMB),
		Timeout:       pulumi.Int(a.Function.TimeoutSeconds),
		Layers:        layers,
		Environment:   &lambda.FunctionEnvironmentArgs{Variables: env},
		LoggingConfig: &lambda.FunctionLoggingConfigArgs{LogFormat: pulumi.String("Text"), LogGroup: logs.Name},
		Tags:          tags,
		// No VpcConfig: the function reaches DynamoDB, S3, SSM, SQS and KMS over
		// its public regional endpoints with the role's credentials.
	}, child, pulumi.DependsOn([]pulumi.Resource{logs}))
	if err != nil {
		return nil, fmt.Errorf("sluis function: %w", err)
	}
	// A pass that failed is the next tick's: no retry, so that "run a pass now"
	// and a tick never run twice because of a transient error.
	if _, err := lambda.NewFunctionEventInvokeConfig(ctx, name+"-http", &lambda.FunctionEventInvokeConfigArgs{
		FunctionName: fn.Name, MaximumRetryAttempts: pulumi.Int(0),
	}, child); err != nil {
		return nil, fmt.Errorf("sluis invoke config: %w", err)
	}

	// ---- the API
	api, domain, truststore, err := newAPI(ctx, name, &a, fn, tags, child)
	if err != nil {
		return nil, err
	}

	// ---- the schedules
	schedRole, schedNames, err := newSchedules(ctx, name, &a, fn, tags, child)
	if err != nil {
		return nil, err
	}

	// ---- the issuer's state secret (it HMAC-signs OAuth flow state; the signing
	// key is KMS's and cannot). Generated once: RandomBytes with no keepers is
	// stable, so an apply never rotates it, and the value is secret in state and
	// in `pulumi up`'s output.
	stateSecret, err := random.NewRandomBytes(ctx, name+"-state-secret", &random.RandomBytesArgs{Length: pulumi.Int(32)}, child)
	if err != nil {
		return nil, fmt.Errorf("sluis state secret: %w", err)
	}
	// Overwrite: `sluis migrate ssm-layout` may have copied the same value to
	// the v3 path first.
	pargs := &ssm.ParameterArgs{
		Name:      pulumi.String(StateSecretParameterName(a.Instance)),
		Type:      pulumi.String("SecureString"),
		Value:     pulumi.ToSecret(stateSecret.Base64).(pulumi.StringOutput),
		Overwrite: pulumi.Bool(true),
		Tags:      tags,
	}
	if a.ParameterKeyArn != "" {
		pargs.KeyId = pulumi.String(a.ParameterKeyArn)
	}
	stateParam, err := ssm.NewParameter(ctx, name+"-state-secret", pargs, child)
	if err != nil {
		return nil, fmt.Errorf("sluis state secret parameter: %w", err)
	}

	// ---- the recovery password: generated once and kept (no keepers, so an apply
	// never rotates it; `pulumi up --replace` on the RandomPassword does), secret
	// in state and in `pulumi up`'s output, and stored where the function
	// reads it at cold start. Letters and digits only, and the look-alikes (0 O 1 l
	// I) are swapped for fixed other letters, so that it can be read off a screen
	// and typed without a guess; 40 characters of a 57-letter alphabet is well
	// over 200 bits, which the swap does not dent.
	recoveryPassword, err := random.NewRandomPassword(ctx, name+"-recovery-password", &random.RandomPasswordArgs{
		Length: pulumi.Int(recoveryPasswordLength), Special: pulumi.Bool(false),
	}, child)
	if err != nil {
		return nil, fmt.Errorf("sluis recovery password: %w", err)
	}
	rargs := &ssm.ParameterArgs{
		Name:      pulumi.String(RecoveryPasswordParameterName(a.Instance)),
		Type:      pulumi.String("SecureString"),
		Value:     pulumi.ToSecret(recoveryPassword.Result.ApplyT(unambiguous)).(pulumi.StringOutput),
		Overwrite: pulumi.Bool(true),
		Tags:      tags,
	}
	if a.ParameterKeyArn != "" {
		rargs.KeyId = pulumi.String(a.ParameterKeyArn)
	}
	recoveryParam, err := ssm.NewParameter(ctx, name+"-recovery-password", rargs, child)
	if err != nil {
		return nil, fmt.Errorf("sluis recovery password parameter: %w", err)
	}

	exportPolicy, err := ExportReadPolicy(a.Region, a.AccountID, a.Instance, a.ParameterKeyArn)
	if err != nil {
		return nil, err
	}

	out.SigningKeyArn, out.SigningKeyID, out.SigningKeyAlias = sgArn, sgID, sgAlias
	out.WrappedSigningKeyArn, out.WrappedSigningKeyAlias = wrappedKeyArn, wrappedAlias
	out.SigningKeyRS256Arn, out.SigningKeyRS256ID, out.SigningKeyRS256Alias = rsArn, rsID, rsAlias
	out.FunctionArn, out.FunctionName = fn.Arn, fn.Name
	out.RoleArn, out.RoleName = role.Arn, role.Name
	out.APIID, out.APIURL = api.ID().ToStringOutput(), api.ApiEndpoint
	out.DomainTarget = domain.DomainNameConfiguration.ApplyT(func(c apigatewayv2.DomainNameDomainNameConfiguration) string {
		if c.TargetDomainName == nil {
			return ""
		}
		return *c.TargetDomainName
	}).(pulumi.StringOutput)
	out.DomainHostedZoneID = domain.DomainNameConfiguration.ApplyT(func(c apigatewayv2.DomainNameDomainNameConfiguration) string {
		if c.HostedZoneId == nil {
			return ""
		}
		return *c.HostedZoneId
	}).(pulumi.StringOutput)
	out.TruststoreBucketName = truststore.Bucket
	out.TruststoreURI = pulumi.Sprintf("s3://%s/%s", truststore.Bucket, truststoreKey)
	out.SchedulerRoleArn = schedRole.Arn
	out.StateSecretParameter = stateParam.Name
	out.RecoveryPasswordParameter = recoveryParam.Name
	out.ScheduleNames = schedNames
	out.ConfigLayerArn = layer.Arn
	out.ExportReadPolicyJSON = pulumi.String(exportPolicy).ToStringOutput()

	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{
		"signingKeyArn": out.SigningKeyArn, "signingKeyId": out.SigningKeyID, "signingKeyAlias": out.SigningKeyAlias,
		"wrappedSigningKeyArn": out.WrappedSigningKeyArn, "wrappedSigningKeyAlias": out.WrappedSigningKeyAlias,
		"signingKeyRs256Arn": out.SigningKeyRS256Arn, "signingKeyRs256Id": out.SigningKeyRS256ID, "signingKeyRs256Alias": out.SigningKeyRS256Alias,
		"functionArn": out.FunctionArn, "functionName": out.FunctionName, "roleArn": out.RoleArn, "roleName": out.RoleName,
		"apiId": out.APIID, "apiUrl": out.APIURL,
		"domainTarget": out.DomainTarget, "domainHostedZoneId": out.DomainHostedZoneID,
		"truststoreBucketName": out.TruststoreBucketName, "truststoreUri": out.TruststoreURI,
		"schedulerRoleArn": out.SchedulerRoleArn, "scheduleNames": out.ScheduleNames,
		"exportReadPolicyJson": out.ExportReadPolicyJSON, "stateSecretParameter": out.StateSecretParameter,
		"recoveryPasswordParameter": out.RecoveryPasswordParameter, "configLayerArn": out.ConfigLayerArn,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// recoveryPasswordLength is the generated recovery password's length.
const recoveryPasswordLength = 40

// unambiguous swaps the characters that are read wrongly off a screen for
// fixed others.
func unambiguous(s string) string {
	return strings.NewReplacer("0", "x", "O", "X", "1", "y", "l", "Y", "I", "z").Replace(s)
}

func stringsOf(v []any) []string {
	out := make([]string, 0, len(v))
	for _, e := range v {
		out = append(out, e.(string))
	}
	return out
}

func lambdaTrust() string {
	raw, _ := json.Marshal(map[string]any{
		"Version": polVersion,
		"Statement": []map[string]any{{
			"Effect": "Allow", "Principal": map[string]any{"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole",
		}},
	})
	return string(raw)
}

// newFunctionRole is the function's role and its inline policy, both named
// after the function. The function's own ARN is computed from its name (it
// may invoke itself for a run-now), which is what keeps the grant from being a
// cycle.
func newFunctionRole(ctx *pulumi.Context, name, fnName string, a *LambdaArgs, signingKeyArns []pulumi.StringInput,
	wrappedKeyArn, logGroupArn pulumi.StringInput, selfArn string, tags pulumi.StringMapInput, opts ...pulumi.ResourceOption) (*iam.Role, error) {
	rargs := &iam.RoleArgs{Name: pulumi.String(fnName), AssumeRolePolicy: pulumi.String(lambdaTrust()), Tags: tags}
	if a.PermissionsBoundaryArn != "" {
		rargs.PermissionsBoundary = pulumi.String(a.PermissionsBoundaryArn)
	}
	r, err := iam.NewRole(ctx, name+"-http-role", rargs, opts...)
	if err != nil {
		return nil, err
	}
	stateKey := pulumi.StringInput(pulumi.String(""))
	if a.State.KeyArn != nil {
		stateKey = a.State.KeyArn
	}
	wrapped := pulumi.StringInput(pulumi.String(""))
	if wrappedKeyArn != nil {
		wrapped = wrappedKeyArn
	}
	inputs := []any{a.Storage.BucketArn, a.State.TableArn, stateKey, a.AuditQueueArn, logGroupArn, wrapped}
	for _, k := range signingKeyArns {
		inputs = append(inputs, k)
	}
	doc := pulumi.All(inputs...).ApplyT(func(v []any) (string, error) {
		return functionPolicy(functionPolicyIn{
			region: a.Region, account: a.AccountID,
			bucketArn: v[0].(string), tableArn: v[1].(string), tableKey: v[2].(string),
			queueArn: v[3].(string), logGroupArn: v[4].(string), wrappedKeyArn: v[5].(string), signingKeyArns: stringsOf(v[6:]),
			parameterKeyArn: a.ParameterKeyArn, instance: a.Instance, exports: !a.Exports.Disabled,
			invokeFunctionArns: []string{selfArn},
			webIdentityAud:     a.WebIdentityAudience,
		})
	}).(pulumi.StringOutput)
	if _, err := iam.NewRolePolicy(ctx, name+"-http-policy", &iam.RolePolicyArgs{
		Name: pulumi.String(fnName), Role: r.Name, Policy: doc,
	}, opts...); err != nil {
		return nil, err
	}
	return r, nil
}

const truststoreKey = "truststore/client-ca.pem"

// newAPI is the HTTP API, its integration with the function, the custom
// domain with mutual TLS and the truststore in its own bucket.
func newAPI(ctx *pulumi.Context, name string, a *LambdaArgs, http *lambda.Function, tags pulumi.StringMapInput,
	opts ...pulumi.ResourceOption) (*apigatewayv2.Api, *apigatewayv2.DomainName, *s3.Bucket, error) {
	bucket, err := s3.NewBucket(ctx, name+"-truststore", &s3.BucketArgs{
		Bucket: pulumi.String(a.API.TruststoreBucketName), Tags: tags,
	}, append(opts, pulumi.Protect(true))...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sluis truststore bucket: %w", err)
	}
	if _, err := s3.NewBucketServerSideEncryptionConfigurationV2(ctx, name+"-truststore-encryption",
		&s3.BucketServerSideEncryptionConfigurationV2Args{
			Bucket: bucket.ID(),
			Rules: s3.BucketServerSideEncryptionConfigurationV2RuleArray{
				&s3.BucketServerSideEncryptionConfigurationV2RuleArgs{
					ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationV2RuleApplyServerSideEncryptionByDefaultArgs{
						SseAlgorithm: pulumi.String("AES256"),
					},
				},
			},
		}, opts...); err != nil {
		return nil, nil, nil, err
	}
	// Versioned: the domain names the version of the truststore it was given.
	versioning, err := s3.NewBucketVersioningV2(ctx, name+"-truststore-versioning", &s3.BucketVersioningV2Args{
		Bucket:                  bucket.ID(),
		VersioningConfiguration: &s3.BucketVersioningV2VersioningConfigurationArgs{Status: pulumi.String("Enabled")},
	}, opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	block, err := s3.NewBucketPublicAccessBlock(ctx, name+"-truststore-public-access", &s3.BucketPublicAccessBlockArgs{
		Bucket: bucket.ID(), BlockPublicAcls: pulumi.Bool(true), BlockPublicPolicy: pulumi.Bool(true),
		IgnorePublicAcls: pulumi.Bool(true), RestrictPublicBuckets: pulumi.Bool(true),
	}, opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := s3.NewBucketPolicy(ctx, name+"-truststore-policy", &s3.BucketPolicyArgs{
		Bucket: bucket.ID(),
		Policy: bucket.Arn.ApplyT(func(arn string) (string, error) {
			return document([]statement{{
				"Sid": "DenyPlainHTTP", "Effect": "Deny", "Principal": "*", "Action": "s3:*",
				"Resource":  []string{arn, arn + "/*"},
				"Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "false"}},
			}})
		}).(pulumi.StringOutput),
	}, append(opts, pulumi.DependsOn([]pulumi.Resource{block}))...); err != nil {
		return nil, nil, nil, err
	}
	obj, err := s3.NewBucketObjectv2(ctx, name+"-truststore-pem", &s3.BucketObjectv2Args{
		Bucket:      bucket.ID(),
		Key:         pulumi.String(truststoreKey),
		Content:     pulumi.String(a.API.TruststorePEM),
		ContentType: pulumi.String("application/x-pem-file"),
	}, append(opts, pulumi.DependsOn([]pulumi.Resource{versioning}))...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sluis truststore object: %w", err)
	}

	api, err := apigatewayv2.NewApi(ctx, name+"-api", &apigatewayv2.ApiArgs{
		Name:                      pulumi.String(a.FunctionNamePrefix),
		ProtocolType:              pulumi.String("HTTP"),
		DisableExecuteApiEndpoint: pulumi.Bool(!a.API.KeepDefaultEndpoint),
		Tags:                      tags,
	}, opts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sluis api: %w", err)
	}
	integ, err := apigatewayv2.NewIntegration(ctx, name+"-api-integration", &apigatewayv2.IntegrationArgs{
		ApiId:                api.ID(),
		IntegrationType:      pulumi.String("AWS_PROXY"),
		IntegrationUri:       http.Arn,
		IntegrationMethod:    pulumi.String("POST"),
		PayloadFormatVersion: pulumi.String("2.0"),
	}, opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := apigatewayv2.NewRoute(ctx, name+"-api-route", &apigatewayv2.RouteArgs{
		ApiId:    api.ID(),
		RouteKey: pulumi.String("$default"),
		Target:   pulumi.Sprintf("integrations/%s", integ.ID()),
	}, opts...); err != nil {
		return nil, nil, nil, err
	}
	stage, err := apigatewayv2.NewStage(ctx, name+"-api-stage", &apigatewayv2.StageArgs{
		ApiId: api.ID(), Name: pulumi.String("$default"), AutoDeploy: pulumi.Bool(true), Tags: tags,
	}, opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := lambda.NewPermission(ctx, name+"-api-invoke", &lambda.PermissionArgs{
		Action:    pulumi.String("lambda:InvokeFunction"),
		Function:  http.Name,
		Principal: pulumi.String("apigateway.amazonaws.com"),
		SourceArn: pulumi.Sprintf("%s/*/*", api.ExecutionArn),
	}, opts...); err != nil {
		return nil, nil, nil, err
	}
	domain, err := apigatewayv2.NewDomainName(ctx, name+"-domain", &apigatewayv2.DomainNameArgs{
		DomainName: pulumi.String(a.API.DomainName),
		DomainNameConfiguration: &apigatewayv2.DomainNameDomainNameConfigurationArgs{
			CertificateArn: a.API.CertificateArn,
			EndpointType:   pulumi.String("REGIONAL"),
			SecurityPolicy: pulumi.String("TLS_1_2"),
		},
		MutualTlsAuthentication: &apigatewayv2.DomainNameMutualTlsAuthenticationArgs{
			TruststoreUri:     pulumi.Sprintf("s3://%s/%s", bucket.Bucket, obj.Key),
			TruststoreVersion: obj.VersionId,
		},
		Tags: tags,
	}, opts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sluis custom domain: %w", err)
	}
	if _, err := apigatewayv2.NewApiMapping(ctx, name+"-domain-mapping", &apigatewayv2.ApiMappingArgs{
		ApiId: api.ID(), DomainName: domain.DomainName, Stage: stage.Name,
	}, opts...); err != nil {
		return nil, nil, nil, err
	}
	return api, domain, bucket, nil
}

// newSchedules is the scheduler's role, which may invoke the function and
// nothing else, and one schedule per target, one for the exports and one for
// the directory refresh.
func newSchedules(ctx *pulumi.Context, name string, a *LambdaArgs, fn *lambda.Function,
	tags pulumi.StringMapInput, opts ...pulumi.ResourceOption) (*iam.Role, pulumi.StringArrayOutput, error) {
	var none pulumi.StringArrayOutput
	trust, _ := json.Marshal(map[string]any{
		"Version": polVersion,
		"Statement": []map[string]any{{
			"Effect": "Allow", "Principal": map[string]any{"Service": "scheduler.amazonaws.com"}, "Action": "sts:AssumeRole",
			"Condition": map[string]any{"StringEquals": map[string]any{"aws:SourceAccount": a.AccountID}},
		}},
	})
	rargs := &iam.RoleArgs{Name: pulumi.String(a.FunctionNamePrefix + "-scheduler"), AssumeRolePolicy: pulumi.String(string(trust)), Tags: tags}
	if a.PermissionsBoundaryArn != "" {
		rargs.PermissionsBoundary = pulumi.String(a.PermissionsBoundaryArn)
	}
	role, err := iam.NewRole(ctx, name+"-scheduler-role", rargs, opts...)
	if err != nil {
		return nil, none, err
	}
	if _, err := iam.NewRolePolicy(ctx, name+"-scheduler-policy", &iam.RolePolicyArgs{
		Name: pulumi.String(a.FunctionNamePrefix + "-scheduler"), Role: role.Name,
		Policy: fn.Arn.ApplyT(func(arn string) (string, error) {
			return document([]statement{{
				"Sid": "SluisTick", "Effect": "Allow", "Action": lambdaInvokeFunction,
				"Resource": []string{arn},
			}})
		}).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, none, err
	}
	var names pulumi.StringArray
	type target struct{ kind, id string }
	var targets []target
	for _, id := range a.Schedule.GitHubOrgs {
		targets = append(targets, target{"github", id})
	}
	for _, id := range a.Schedule.SlackWorkspaces {
		targets = append(targets, target{"slack", id})
	}
	for _, t := range targets {
		payload, err := json.Marshal(map[string]string{"kind": "tick", "target": t.id})
		if err != nil {
			return nil, none, err
		}
		sname := a.FunctionNamePrefix + "-" + t.kind + "-" + strings.ReplaceAll(t.id, ":", "-")
		if len(sname) > 64 {
			return nil, none, fmt.Errorf("sluispulumi: the schedule name %q is longer than 64 characters", sname)
		}
		if _, err := scheduler.NewSchedule(ctx, name+"-"+t.kind+"-"+strings.ReplaceAll(t.id, ":", "-"), &scheduler.ScheduleArgs{
			Name:                       pulumi.String(sname),
			Description:                pulumi.Sprintf("Ticks the %s controller for %s.", t.kind, t.id),
			ScheduleExpression:         pulumi.String(a.Schedule.Rate),
			ScheduleExpressionTimezone: pulumi.String("UTC"),
			FlexibleTimeWindow:         &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
			Target: &scheduler.ScheduleTargetArgs{
				Arn:     fn.Arn,
				RoleArn: role.Arn,
				Input:   pulumi.String(string(payload)),
				RetryPolicy: &scheduler.ScheduleTargetRetryPolicyArgs{
					MaximumRetryAttempts: pulumi.Int(0), MaximumEventAgeInSeconds: pulumi.Int(3600),
				},
			},
		}, opts...); err != nil {
			return nil, none, fmt.Errorf("sluis schedule %s: %w", sname, err)
		}
		names = append(names, pulumi.String(sname))
	}
	if !a.Exports.Disabled {
		ename := a.FunctionNamePrefix + "-exports"
		if _, err := scheduler.NewSchedule(ctx, name+"-exports", &scheduler.ScheduleArgs{
			Name:                       pulumi.String(ename),
			Description:                pulumi.String("Runs the exports."),
			ScheduleExpression:         pulumi.String(a.Exports.Rate),
			ScheduleExpressionTimezone: pulumi.String("UTC"),
			FlexibleTimeWindow:         &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
			Target: &scheduler.ScheduleTargetArgs{
				Arn: fn.Arn, RoleArn: role.Arn, Input: pulumi.String(`{"kind":"exports"}`),
				RetryPolicy: &scheduler.ScheduleTargetRetryPolicyArgs{
					MaximumRetryAttempts: pulumi.Int(0), MaximumEventAgeInSeconds: pulumi.Int(3600),
				},
			},
		}, opts...); err != nil {
			return nil, none, fmt.Errorf("sluis exports schedule: %w", err)
		}
		names = append(names, pulumi.String(ename))
	}
	if !a.DirectoryRefresh.Disabled {
		rname := a.FunctionNamePrefix + "-directory-refresh"
		if _, err := scheduler.NewSchedule(ctx, name+"-directory-refresh", &scheduler.ScheduleArgs{
			Name:                       pulumi.String(rname),
			Description:                pulumi.String("Refreshes the directory snapshots."),
			ScheduleExpression:         pulumi.String(a.DirectoryRefresh.Rate),
			ScheduleExpressionTimezone: pulumi.String("UTC"),
			FlexibleTimeWindow:         &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
			Target: &scheduler.ScheduleTargetArgs{
				Arn: fn.Arn, RoleArn: role.Arn, Input: pulumi.String(`{"kind":"refresh"}`),
				RetryPolicy: &scheduler.ScheduleTargetRetryPolicyArgs{
					MaximumRetryAttempts: pulumi.Int(0), MaximumEventAgeInSeconds: pulumi.Int(3600),
				},
			},
		}, opts...); err != nil {
			return nil, none, fmt.Errorf("sluis directory refresh schedule: %w", err)
		}
		names = append(names, pulumi.String(rname))
	}
	return role, names.ToStringArrayOutput(), nil
}

// telemetryVariable is whether a variable may be set through Telemetry.Env: the
// OpenTelemetry SDK's own, the telemetry layer's own, and the exec wrapper a
// layer installs itself with. Never sluis's (SLUIS_*), never the loader's
// (LD_*) and never another AWS_* variable.
func telemetryVariable(k string) bool {
	switch {
	case k == "AWS_LAMBDA_EXEC_WRAPPER":
		return true
	case strings.HasPrefix(k, "OTEL_"), strings.HasPrefix(k, "ACCESS_ROSTER_"), strings.HasPrefix(k, "OPENTELEMETRY_"):
		return true
	}
	return false
}
