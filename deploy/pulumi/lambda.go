package sluispulumi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
)

// LambdaType is the Pulumi type token of the Lambda component.
const LambdaType = "sluis:aws:Lambda"

// The roles of the one binary (env SLUIS_ROLE).
const (
	RoleHTTP   = "http"
	RoleGitHub = "github"
	RoleSlack  = "slack"
)

// DefaultSigningKeyAlias is the token-signing key's alias when
// LambdaArgs.SigningKeyAlias is empty.
const DefaultSigningKeyAlias = "alias/sluis-signing"

// DefaultSigningKeyRS256Alias is the RSA signing key's alias when
// LambdaArgs.SigningKeyRS256Alias is empty.
const DefaultSigningKeyRS256Alias = "alias/sluis-signing-rs256"

// StateSecretParameterName is the SSM parameter of the issuer's state secret.
const StateSecretParameterName = PrivateParameterPrefix + "/issuer/state-secret"

// DefaultSchedule is the controllers' tick when LambdaArgs.Schedule.Rate is empty.
const DefaultSchedule = "rate(5 minutes)"

// LambdaArgs is the whole Lambda shape of sluis: three functions from one zip,
// each with a role of its own, the HTTP API in front of the http function, the
// token-signing key and one schedule per controller target.
type LambdaArgs struct {
	// Region and AccountID name the SSM parameters and the functions in the
	// roles' policies. Required.
	Region    string
	AccountID string

	// Package is the released zip, `sluis-lambda_<version>_linux_arm64.zip`, with
	// `bootstrap` at its root: a path on disk or an https URL, read when the
	// stack is evaluated. Required.
	Package string
	// PackageSHA256 is the zip's expected SHA-256, in hex. Optional, and worth
	// setting with a URL.
	PackageSHA256 string

	// Config, GitHubConfig and SlackConfig are the three functions' configuration
	// files (a `serve`, a `controller-github` and a `controller-slack`
	// configuration), which the package holds at config/sluis.yaml,
	// config/github.yaml and config/slack.yaml; each function's
	// SLUIS_CONFIG_FILE names its own. Required. They hold no secret: a secret is
	// an `ssm:/sluis/private/...` value of a function's Env.
	//
	// The http file's `adapters.trigger.settings` names the two controller
	// functions (`github: sluis-github`, `slack: sluis-slack`, that is
	// FunctionNamePrefix + "-github" and "-slack"); the functions take no
	// environment variable for it.
	Config, GitHubConfig, SlackConfig string
	// Catalogues are catalogue files the package holds at config/<name>, by file
	// name. CataloguePaths are files on disk, read when the stack is evaluated
	// and merged in under their base names; a name in both is refused unless the
	// contents are identical, and an unreadable or empty file is refused before
	// anything is created.
	//
	// Config and the catalogues are part of the function package, so a change to
	// any of them changes the package and redeploys the three functions on the
	// next `pulumi up`.
	Catalogues     map[string]string
	CataloguePaths []string

	// Storage is the blob bucket (Storage.Grant()). Required.
	Storage *StorageGrant
	// State is the DynamoDB table (State.Grant()). Required: all three functions
	// keep State in it.
	State *StateGrant
	// AuditQueueArn is the audit stack's ingest queue; each function may send to
	// it. Required.
	AuditQueueArn pulumi.StringInput

	// ParameterKeyArn is the customer-managed key SecureString parameters under
	// /sluis are encrypted with. Default none: the AWS-managed key, which needs no
	// grant. With a key, each function may use it through SSM only.
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

	// FunctionNamePrefix starts the functions' and roles' names:
	// `<prefix>-http`, `<prefix>-github` and `<prefix>-slack`. Default "sluis".
	FunctionNamePrefix string
	// HTTP, GitHub and Slack tune one function each.
	HTTP, GitHub, Slack FunctionArgs
	// Env is set on all three functions, beside SLUIS_ROLE and
	// SLUIS_CONFIG_FILE, which the library owns.
	Env map[string]string

	// LogRetentionDays is each function's log group's retention. Default 30.
	LogRetentionDays int
	// PermissionsBoundaryArn is the boundary of every role. Default none.
	PermissionsBoundaryArn string

	// API is the HTTP API in front of the http function. Required.
	API APIArgs
	// Schedule is the controllers' tick: one schedule per target.
	Schedule ScheduleArgs
	// Telemetry is the OpenTelemetry layer. Nil: no layer and no OTEL
	// environment, which is how an estate whose collector is not ready runs.
	Telemetry *TelemetryArgs

	// Tags are put on everything that takes tags. Default none.
	Tags map[string]string
}

// FunctionArgs tunes one function.
type FunctionArgs struct {
	// MemoryMB defaults to 512.
	MemoryMB int
	// TimeoutSeconds defaults to 30 for http, and to 300 for a controller (a
	// pass over a whole organisation).
	TimeoutSeconds int
	// Env is set on this function alone.
	Env map[string]string
}

// APIArgs is the HTTP API (payload format 2.0) and its custom domain.
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
	// invokes the github or slack function with `{"kind":"tick","target":"<id>"}`.
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
	// Env is the OTEL_* and layer settings (the endpoint, the protocol). The
	// library adds OTEL_SERVICE_NAME, the function's name, unless it is here.
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

	// The functions and their roles.
	HTTPFunctionArn, GitHubFunctionArn, SlackFunctionArn    pulumi.StringOutput
	HTTPFunctionName, GitHubFunctionName, SlackFunctionName pulumi.StringOutput
	HTTPRoleArn, GitHubRoleArn, SlackRoleArn                pulumi.StringOutput
	HTTPRoleName, GitHubRoleName, SlackRoleName             pulumi.StringOutput

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

	// StateSecretParameter is the name of the SSM SecureString that holds the
	// issuer's OAuth-state secret, `/sluis/private/issuer/state-secret`: 32
	// random bytes, base64. The library generates it and keeps it across applies.
	StateSecretParameter pulumi.StringOutput

	// ExportReadPolicyJSON is the IAM policy document a consumer's External
	// Secrets Operator role attaches: read on /sluis/export/* and nothing else.
	ExportReadPolicyJSON pulumi.StringOutput
}

// A target is a login or a workspace key, or `github:links` (the link check).
var targetID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,40}$`)

func (a *LambdaArgs) validate() (LambdaArgs, map[string]string, error) {
	if a == nil {
		return LambdaArgs{}, nil, errors.New("sluispulumi: LambdaArgs is nil")
	}
	out := *a
	var missing []string
	for k, v := range map[string]string{
		"Region": out.Region, "AccountID": out.AccountID, "Package": out.Package, "Config": strings.TrimSpace(out.Config),
		"GitHubConfig": strings.TrimSpace(out.GitHubConfig), "SlackConfig": strings.TrimSpace(out.SlackConfig),
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
		return out, nil, fmt.Errorf("sluispulumi: LambdaArgs: required and empty: %v", sortedStrings(missing))
	}
	if out.SigningKeyAlias == "" {
		out.SigningKeyAlias = DefaultSigningKeyAlias
	}
	if out.SigningKeyRS256Alias == "" {
		out.SigningKeyRS256Alias = DefaultSigningKeyRS256Alias
	}
	if !out.DisableSigningKeyRS256 && (!strings.HasPrefix(out.SigningKeyRS256Alias, "alias/") || len(out.SigningKeyRS256Alias) == len("alias/")) {
		return out, nil, fmt.Errorf("sluispulumi: LambdaArgs.SigningKeyRS256Alias %q must start with \"alias/\"", out.SigningKeyRS256Alias)
	}
	if !out.DisableSigningKeyRS256 && out.SigningKeyRS256Alias == out.SigningKeyAlias {
		return out, nil, errors.New("sluispulumi: LambdaArgs.SigningKeyRS256Alias is the ES384 key's alias too")
	}
	if !strings.HasPrefix(out.SigningKeyAlias, "alias/") || len(out.SigningKeyAlias) == len("alias/") {
		return out, nil, fmt.Errorf("sluispulumi: LambdaArgs.SigningKeyAlias %q must start with \"alias/\"", out.SigningKeyAlias)
	}
	if out.FunctionNamePrefix == "" {
		out.FunctionNamePrefix = "sluis"
	}
	if out.LogRetentionDays == 0 {
		out.LogRetentionDays = 30
	}
	for _, f := range []struct {
		fa      *FunctionArgs
		timeout int
	}{{&out.HTTP, 30}, {&out.GitHub, 300}, {&out.Slack, 300}} {
		if f.fa.MemoryMB == 0 {
			f.fa.MemoryMB = 512
		}
		if f.fa.TimeoutSeconds == 0 {
			f.fa.TimeoutSeconds = f.timeout
		}
	}
	if out.Schedule.Rate == "" {
		out.Schedule.Rate = DefaultSchedule
	}
	if r := out.Schedule.Rate; !strings.HasPrefix(r, "rate(") && !strings.HasPrefix(r, "cron(") && !strings.HasPrefix(r, "at(") {
		return out, nil, fmt.Errorf("sluispulumi: Schedule.Rate %q is not an EventBridge Scheduler expression", r)
	}
	for kind, ids := range map[string][]string{"GitHubOrgs": out.Schedule.GitHubOrgs, "SlackWorkspaces": out.Schedule.SlackWorkspaces} {
		seen := map[string]bool{}
		for _, id := range ids {
			if !targetID.MatchString(id) {
				return out, nil, fmt.Errorf("sluispulumi: Schedule.%s: %q is not a target id (letters, digits, - _ ., at most 40)", kind, id)
			}
			if seen[id] {
				return out, nil, fmt.Errorf("sluispulumi: Schedule.%s: %q is listed twice", kind, id)
			}
			seen[id] = true
		}
	}
	if t := out.Telemetry; t != nil && t.LayerArn == nil {
		return out, nil, errors.New("sluispulumi: Telemetry.LayerArn is required with Telemetry")
	}
	cats, err := mergeCatalogues(out.Catalogues, out.CataloguePaths)
	if err != nil {
		return out, nil, err
	}
	return out, cats, nil
}

func mergeCatalogues(inline map[string]string, paths []string) (map[string]string, error) {
	merged := make(map[string]string, len(inline)+len(paths))
	for k, v := range inline {
		merged[k] = v
	}
	for _, p := range paths {
		body, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("sluispulumi: CataloguePaths: %w", err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return nil, fmt.Errorf("sluispulumi: CataloguePaths: %s is empty", p)
		}
		base := filepath.Base(p)
		if prev, ok := merged[base]; ok && prev != string(body) {
			return nil, fmt.Errorf("sluispulumi: CataloguePaths: %s is also in Catalogues with other content", base)
		}
		merged[base] = string(body)
	}
	for k := range merged {
		if k == "" || strings.ContainsAny(k, "/\\") || k == "." || k == ".." || k == configName || k == githubConfigName || k == slackConfigName {
			return nil, fmt.Errorf("sluispulumi: Catalogues has %q: a catalogue is a file name, and not one of the three configuration files", k)
		}
	}
	return merged, nil
}

type fnSpec struct {
	role string
	args FunctionArgs
}

// NewLambda creates the Lambda shape. The functions are one zip's `bootstrap`
// (provided.al2023, arm64, no VPC) told apart by SLUIS_ROLE; each has its own
// role, which is why a grant for one is never a grant for another:
//
//   - all three: logs to their own group; S3 on the blob bucket; DynamoDB on the
//     table; SSM Get, GetByPath, Put and Delete under /sluis/private/* and Put and
//     Delete under /sluis/export/*; sqs:SendMessage on the audit queue;
//   - http alone: kms:Sign and kms:GetPublicKey on the signing key, and
//     lambda:InvokeFunction on the github and slack functions (run a pass now).
//
// The API is an HTTP API with payload format 2.0 and a $default route to the
// http function, behind a regional custom domain with mutual TLS. The
// controllers are invoked by one EventBridge schedule per target, through a role
// of their own that may invoke only those two functions.
func NewLambda(ctx *pulumi.Context, name string, args *LambdaArgs, opts ...pulumi.ResourceOption) (*Lambda, error) {
	a, catalogues, err := args.validate()
	if err != nil {
		return nil, err
	}
	entries, err := loadPackage(a.Package, a.PackageSHA256)
	if err != nil {
		return nil, err
	}
	added := map[string]string{
		configDir + "/" + configName:       a.Config,
		configDir + "/" + githubConfigName: a.GitHubConfig,
		configDir + "/" + slackConfigName:  a.SlackConfig,
	}
	for file, body := range catalogues {
		added[configDir+"/"+file] = body
	}
	out := &Lambda{}
	if err := ctx.RegisterComponentResource(LambdaType, name, out, opts...); err != nil {
		return nil, err
	}
	child := pulumi.Parent(out)
	tags := tagMap(a.Tags)

	// ---- the signing key
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
	signingArns := []pulumi.StringInput{key.Arn}
	rsEmpty := pulumi.String("").ToStringOutput()
	rsArn, rsID, rsAlias := rsEmpty, rsEmpty, rsEmpty
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

	// ---- the functions
	fnName := func(role string) string { return a.FunctionNamePrefix + "-" + role }
	fnArn := func(role string) string {
		return arnPrefix + "lambda:" + a.Region + ":" + a.AccountID + ":function:" + fnName(role)
	}
	specs := []fnSpec{{RoleHTTP, a.HTTP}, {RoleGitHub, a.GitHub}, {RoleSlack, a.Slack}}
	fns := map[string]*lambda.Function{}
	roles := map[string]*iam.Role{}
	for _, s := range specs {
		logs, err := cloudwatch.NewLogGroup(ctx, name+"-"+s.role, &cloudwatch.LogGroupArgs{
			Name:            pulumi.String("/aws/lambda/" + fnName(s.role)),
			RetentionInDays: pulumi.Int(a.LogRetentionDays),
			Tags:            tags,
		}, child)
		if err != nil {
			return nil, fmt.Errorf("sluis %s log group: %w", s.role, err)
		}
		role, err := newFunctionRole(ctx, name, fnName(s.role), s.role, &a, signingArns, logs.Arn, fnArn(RoleGitHub), fnArn(RoleSlack), tags, child)
		if err != nil {
			return nil, fmt.Errorf("sluis %s role: %w", s.role, err)
		}
		roles[s.role] = role

		env := pulumi.StringMap{}
		for k, v := range a.Env {
			env[k] = pulumi.String(v)
		}
		if t := a.Telemetry; t != nil {
			if _, has := t.Env["OTEL_SERVICE_NAME"]; !has {
				env["OTEL_SERVICE_NAME"] = pulumi.String(fnName(s.role))
			}
			for k, v := range t.Env {
				env[k] = pulumi.String(v)
			}
		}
		for k, v := range s.args.Env {
			env[k] = pulumi.String(v)
		}
		env["SLUIS_ROLE"] = pulumi.String(s.role)
		env["SLUIS_CONFIG_FILE"] = pulumi.String(ConfigFilePath(s.role))

		code, err := buildArchive(entries, added)
		if err != nil {
			return nil, err
		}
		layers := pulumi.StringArray{}
		if a.Telemetry != nil {
			layers = append(layers, a.Telemetry.LayerArn)
		}
		fn, err := lambda.NewFunction(ctx, name+"-"+s.role, &lambda.FunctionArgs{
			Name:          pulumi.String(fnName(s.role)),
			Role:          role.Arn,
			Runtime:       pulumi.String("provided.al2023"),
			Handler:       pulumi.String("bootstrap"),
			Architectures: pulumi.StringArray{pulumi.String("arm64")},
			Code:          code,
			MemorySize:    pulumi.Int(s.args.MemoryMB),
			Timeout:       pulumi.Int(s.args.TimeoutSeconds),
			Layers:        layers,
			Environment:   &lambda.FunctionEnvironmentArgs{Variables: env},
			LoggingConfig: &lambda.FunctionLoggingConfigArgs{LogFormat: pulumi.String("Text"), LogGroup: logs.Name},
			Tags:          tags,
			// No VpcConfig: the functions reach DynamoDB, S3, SSM, SQS and KMS over
			// their public regional endpoints with the role's credentials.
		}, child, pulumi.DependsOn([]pulumi.Resource{logs}))
		if err != nil {
			return nil, fmt.Errorf("sluis %s function: %w", s.role, err)
		}
		fns[s.role] = fn
	}
	// A pass that failed is the next tick's: no retry, so that "run a pass now"
	// and a tick never run twice because of a transient error.
	for _, role := range []string{RoleGitHub, RoleSlack} {
		if _, err := lambda.NewFunctionEventInvokeConfig(ctx, name+"-"+role, &lambda.FunctionEventInvokeConfigArgs{
			FunctionName: fns[role].Name, MaximumRetryAttempts: pulumi.Int(0),
		}, child); err != nil {
			return nil, fmt.Errorf("sluis %s invoke config: %w", role, err)
		}
	}

	// ---- the API
	api, domain, truststore, err := newAPI(ctx, name, &a, fns[RoleHTTP], tags, child)
	if err != nil {
		return nil, err
	}

	// ---- the schedules
	schedRole, schedNames, err := newSchedules(ctx, name, &a, fns[RoleGitHub], fns[RoleSlack], fnName, tags, child)
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
	pargs := &ssm.ParameterArgs{
		Name:  pulumi.String(StateSecretParameterName),
		Type:  pulumi.String("SecureString"),
		Value: pulumi.ToSecret(stateSecret.Base64).(pulumi.StringOutput),
		Tags:  tags,
	}
	if a.ParameterKeyArn != "" {
		pargs.KeyId = pulumi.String(a.ParameterKeyArn)
	}
	stateParam, err := ssm.NewParameter(ctx, name+"-state-secret", pargs, child)
	if err != nil {
		return nil, fmt.Errorf("sluis state secret parameter: %w", err)
	}

	exportPolicy, err := ExportReadPolicy(a.Region, a.AccountID, a.ParameterKeyArn)
	if err != nil {
		return nil, err
	}

	out.SigningKeyArn, out.SigningKeyID, out.SigningKeyAlias = key.Arn, key.KeyId, alias.Name
	out.SigningKeyRS256Arn, out.SigningKeyRS256ID, out.SigningKeyRS256Alias = rsArn, rsID, rsAlias
	out.HTTPFunctionArn, out.HTTPFunctionName = fns[RoleHTTP].Arn, fns[RoleHTTP].Name
	out.GitHubFunctionArn, out.GitHubFunctionName = fns[RoleGitHub].Arn, fns[RoleGitHub].Name
	out.SlackFunctionArn, out.SlackFunctionName = fns[RoleSlack].Arn, fns[RoleSlack].Name
	out.HTTPRoleArn, out.HTTPRoleName = roles[RoleHTTP].Arn, roles[RoleHTTP].Name
	out.GitHubRoleArn, out.GitHubRoleName = roles[RoleGitHub].Arn, roles[RoleGitHub].Name
	out.SlackRoleArn, out.SlackRoleName = roles[RoleSlack].Arn, roles[RoleSlack].Name
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
	out.ScheduleNames = schedNames
	out.ExportReadPolicyJSON = pulumi.String(exportPolicy).ToStringOutput()

	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{
		"signingKeyArn": out.SigningKeyArn, "signingKeyId": out.SigningKeyID, "signingKeyAlias": out.SigningKeyAlias,
		"signingKeyRs256Arn": out.SigningKeyRS256Arn, "signingKeyRs256Id": out.SigningKeyRS256ID, "signingKeyRs256Alias": out.SigningKeyRS256Alias,
		"httpFunctionArn": out.HTTPFunctionArn, "githubFunctionArn": out.GitHubFunctionArn, "slackFunctionArn": out.SlackFunctionArn,
		"httpRoleArn": out.HTTPRoleArn, "githubRoleArn": out.GitHubRoleArn, "slackRoleArn": out.SlackRoleArn,
		"apiId": out.APIID, "apiUrl": out.APIURL,
		"domainTarget": out.DomainTarget, "domainHostedZoneId": out.DomainHostedZoneID,
		"truststoreBucketName": out.TruststoreBucketName, "truststoreUri": out.TruststoreURI,
		"schedulerRoleArn": out.SchedulerRoleArn, "scheduleNames": out.ScheduleNames,
		"exportReadPolicyJson": out.ExportReadPolicyJSON, "stateSecretParameter": out.StateSecretParameter,
	}); err != nil {
		return nil, err
	}
	return out, nil
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

// newFunctionRole is the role of one function and its inline policy, both named
// after the function. The function's own ARN is computed from its name, which is
// what keeps the http role's grant on the other two from being a cycle.
func newFunctionRole(ctx *pulumi.Context, name, fnName, role string, a *LambdaArgs, signingKeyArns []pulumi.StringInput, logGroupArn pulumi.StringInput,
	githubArn, slackArn string, tags pulumi.StringMapInput, opts ...pulumi.ResourceOption) (*iam.Role, error) {
	rargs := &iam.RoleArgs{Name: pulumi.String(fnName), AssumeRolePolicy: pulumi.String(lambdaTrust()), Tags: tags}
	if a.PermissionsBoundaryArn != "" {
		rargs.PermissionsBoundary = pulumi.String(a.PermissionsBoundaryArn)
	}
	r, err := iam.NewRole(ctx, name+"-"+role+"-role", rargs, opts...)
	if err != nil {
		return nil, err
	}
	stateKey := pulumi.StringInput(pulumi.String(""))
	if a.State.KeyArn != nil {
		stateKey = a.State.KeyArn
	}
	inputs := []any{a.Storage.BucketArn, a.State.TableArn, stateKey, a.AuditQueueArn, logGroupArn}
	for _, k := range signingKeyArns {
		inputs = append(inputs, k)
	}
	doc := pulumi.All(inputs...).ApplyT(func(v []any) (string, error) {
		return functionPolicy(functionPolicyIn{
			role: role, region: a.Region, account: a.AccountID,
			bucketArn: v[0].(string), tableArn: v[1].(string), tableKey: v[2].(string),
			queueArn: v[3].(string), logGroupArn: v[4].(string), signingKeyArns: stringsOf(v[5:]),
			parameterKeyArn:    a.ParameterKeyArn,
			invokeFunctionArns: []string{githubArn, slackArn},
		})
	}).(pulumi.StringOutput)
	if _, err := iam.NewRolePolicy(ctx, name+"-"+role+"-policy", &iam.RolePolicyArgs{
		Name: pulumi.String(fnName), Role: r.Name, Policy: doc,
	}, opts...); err != nil {
		return nil, err
	}
	return r, nil
}

const truststoreKey = "truststore/client-ca.pem"

// newAPI is the HTTP API, its integration with the http function, the custom
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

// newSchedules is the scheduler's role, which may invoke the two controller
// functions and nothing else, and one schedule per target.
func newSchedules(ctx *pulumi.Context, name string, a *LambdaArgs, gh, sl *lambda.Function, fnName func(string) string,
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
		Policy: pulumi.All(gh.Arn, sl.Arn).ApplyT(func(v []any) (string, error) {
			return document([]statement{{
				"Sid": "SluisTick", "Effect": "Allow", "Action": lambdaInvokeFunction,
				"Resource": []string{v[0].(string), v[1].(string)},
			}})
		}).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, none, err
	}
	var names pulumi.StringArray
	type target struct {
		role, id string
		fn       *lambda.Function
	}
	var targets []target
	for _, id := range a.Schedule.GitHubOrgs {
		targets = append(targets, target{RoleGitHub, id, gh})
	}
	for _, id := range a.Schedule.SlackWorkspaces {
		targets = append(targets, target{RoleSlack, id, sl})
	}
	for _, t := range targets {
		payload, err := json.Marshal(map[string]string{"kind": "tick", "target": t.id})
		if err != nil {
			return nil, none, err
		}
		sname := fnName(t.role) + "-" + strings.ReplaceAll(t.id, ":", "-")
		if len(sname) > 64 {
			return nil, none, fmt.Errorf("sluispulumi: the schedule name %q is longer than 64 characters", sname)
		}
		if _, err := scheduler.NewSchedule(ctx, name+"-"+t.role+"-"+strings.ReplaceAll(t.id, ":", "-"), &scheduler.ScheduleArgs{
			Name:                       pulumi.String(sname),
			Description:                pulumi.Sprintf("Ticks the %s controller for %s.", t.role, t.id),
			ScheduleExpression:         pulumi.String(a.Schedule.Rate),
			ScheduleExpressionTimezone: pulumi.String("UTC"),
			FlexibleTimeWindow:         &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
			Target: &scheduler.ScheduleTargetArgs{
				Arn:     t.fn.Arn,
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
	return role, names.ToStringArrayOutput(), nil
}
