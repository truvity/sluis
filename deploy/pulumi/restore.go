package sluispulumi

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// RestoreType is the Pulumi type token of the Restore component.
const RestoreType = "sluis:aws:Restore"

// RestoreMargin is how much of an invocation's deadline the restore function
// keeps back before it pauses and continues itself (restorejob.Margin in the
// binary). The function's timeout is held to at least twice this, so that a
// slice has time to work.
const RestoreMargin = 90 * time.Second

// MinRestoreTimeoutSeconds is the shortest timeout of the restore function.
const MinRestoreTimeoutSeconds = int(2 * RestoreMargin / time.Second)

// RestoreInvokers are the principals allowed to invoke the restore function:
// the administrators through `live-admin` and the break-glass roles through
// `live-breakglass`. Nobody else is named in the function's resource policy.
type RestoreInvokers struct {
	// AdminRoleArns are the administrators' roles. At least one.
	AdminRoleArns []string
	// BreakglassRoleArns are the break-glass roles. At least one.
	BreakglassRoleArns []string
}

// RestoreArgs is the restore function: the same release zip, installation and
// archive as the backup function, with `backup.role: restore`.
type RestoreArgs struct {
	BackupCommon
	// RestoreInvokers are the principals the function's resource policy allows.
	// Required.
	RestoreInvokers RestoreInvokers
	// Recipients are the age recipients every backup of the instance is
	// encrypted to, rendered as `backup.recipients`: Backup.Recipients of
	// NewBackup for the same instance. The restore compares a backup's sidecar
	// with them, and an unverified restore (`allowUnverified`) is refused without
	// them. Optional, and recommended: unset, only a verified restore can run.
	Recipients pulumi.StringArrayInput
	// Verify is `backup.verify`: how the restore proves, against the audit
	// trail, that a backup is the one the backup function made. Unset, every
	// restore is refused unless it is started with `allowUnverified`.
	Verify *RestoreVerifyArgs
}

// RestoreVerifyArgs is `backup.verify` of the restore document
// (docs/reference/sluis/backup.md, Provenance).
type RestoreVerifyArgs struct {
	// Query is the audit installation's query service: https, or http on a
	// loopback address. Required.
	Query string
	// TokenFile is the file in the function's environment that holds the bearer
	// token the query service accepts, such as one a layer writes. The library
	// provisions no token. Required.
	TokenFile string
	// Profile is the audit profile that keeps the `activity` category; the
	// binary's default (`activity`) when empty.
	Profile string
	// Observer is the id the audit writer stamps on the backup function's
	// records (the observer its `senders` table maps the backup role to). A
	// record stamped by any other is refused. Required unless AnyObserver.
	Observer string
	// AnyObserver accepts a record stamped by anyone: whoever may send to the
	// audit queue can then vouch for a forged backup, and a start logs a
	// warning. The explicit opt-out of Observer.
	AnyObserver bool
}

// validate holds the arguments to what the restore role's document accepts.
func (v *RestoreVerifyArgs) validate() error {
	if v == nil {
		return nil
	}
	var errs []error
	if err := verifyQueryURL(v.Query); err != nil {
		errs = append(errs, fmt.Errorf("Verify.Query %q: %w", v.Query, err))
	}
	if v.TokenFile == "" {
		errs = append(errs, errors.New("Verify.TokenFile is required: the query service answers nobody it cannot name"))
	}
	switch {
	case v.Observer == "" && !v.AnyObserver:
		errs = append(errs, errors.New("Verify.Observer is required: the id the audit writer stamps on the backup function's records "+
			"(or AnyObserver, which lets any sender of audit records vouch for a backup)"))
	case v.Observer != "" && v.AnyObserver:
		errs = append(errs, errors.New("Verify.Observer and Verify.AnyObserver are one or the other"))
	}
	return errors.Join(errs...)
}

// render is the `backup.verify` block.
func (v *RestoreVerifyArgs) render() map[string]any {
	out := map[string]any{"query": v.Query, "tokenFile": v.TokenFile}
	if v.Profile != "" {
		out["profile"] = v.Profile
	}
	if v.Observer != "" {
		out["observer"] = v.Observer
	}
	if v.AnyObserver {
		out["anyObserver"] = true
	}
	return out
}

// verifyQueryURL refuses a query-service address that is not https, or http on
// a loopback host: the token and the answer cross it. The binary holds
// `backup.verify.query` to the same rule at start.
func verifyQueryURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("not an https address")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		h := u.Hostname()
		if h == "localhost" {
			return nil
		}
		if ip, err := netip.ParseAddr(h); err == nil && ip.IsLoopback() {
			return nil
		}
		return errors.New("http is for a loopback address only: use https")
	}
	return errors.New("not an https address")
}

// Restore is the component. Its fields are the outputs.
type Restore struct {
	pulumi.ResourceState

	// FunctionArn, FunctionName, RoleArn, RoleName and ConfigLayerArn are the
	// function, its role and the layer with the `backup.role: restore` document.
	FunctionArn    pulumi.StringOutput
	FunctionName   pulumi.StringOutput
	RoleArn        pulumi.StringOutput
	RoleName       pulumi.StringOutput
	ConfigLayerArn pulumi.StringOutput
	// AliasArns holds `live-admin` and `live-breakglass`; the function has no
	// other alias and no unqualified invoke is allowed.
	AliasArns pulumi.StringMapOutput
	// ArchiveKeyArn is the key behind ArchiveKeyAlias; empty when it is not set.
	//
	// Deprecated: the archive uses no KMS key.
	ArchiveKeyArn pulumi.StringOutput
	// IdentityParameter is the SSM parameter the function reads its age identity
	// from, `/sluis/<instance>/internal/backup/restore-identity`. NewBackup of the
	// same instance creates it.
	IdentityParameter pulumi.StringOutput
	// AlarmNames is the one alarm: any invocation.
	AlarmNames pulumi.StringArrayOutput
}

// validate holds the invokers to what the resource policy may name.
func (r RestoreInvokers) validate() error {
	var errs []error
	seen := map[string]string{}
	for _, c := range []struct {
		class string
		arns  []string
	}{{ClassAdmin, r.AdminRoleArns}, {ClassBreakglass, r.BreakglassRoleArns}} {
		class, arns := c.class, c.arns
		if len(arns) == 0 {
			errs = append(errs, fmt.Errorf("RestoreInvokers: no %s role: the restore function is invoked by administrators and break-glass roles only", class))
		}
		for _, a := range arns {
			switch {
			case !strings.HasPrefix(a, "arn:") || !strings.Contains(a, ":role/") || strings.ContainsAny(a, "*?"):
				errs = append(errs, fmt.Errorf("RestoreInvokers: %q is not the ARN of one role: a wildcard, an account or a user is refused", a))
			case seen[a] != "":
				errs = append(errs, fmt.Errorf("RestoreInvokers: %q is both %s and %s: the alias is the class", a, seen[a], class))
			}
			seen[a] = class
		}
	}
	return errors.Join(errs...)
}

// restoreAliasArns are the function's alias ARNs, from its name: the role grants
// its own continuation on them without waiting for the aliases (no cycle).
func (p *backupPlan) restoreAliasArns() []string {
	base := arnPrefix + "lambda:" + p.Region + ":" + p.AccountID + ":function:" + p.FunctionName + ":"
	return []string{base + CallerAlias(ClassAdmin), base + CallerAlias(ClassBreakglass)}
}

// NewRestore creates the restore function: the release zip with a
// `backup.role: restore` document (with `backup.recipients` from Recipients
// and `backup.verify` from Verify when they are given), whose role may write every module's table
// (the maintenance flag included), parameters and blob prefixes, read the
// archive and its key, and invoke its own aliases to continue a paused restore.
//
// The function has the aliases `live-admin` and `live-breakglass` and no other,
// and its resource policy allows RestoreInvokers to invoke the one alias of
// their class and nobody else. It has no reserved concurrency (the lease
// `lease.restore:run` allows one restore at a time), no schedule, and an alarm on
// any invocation. Its timeout is held to at least twice [RestoreMargin].
func NewRestore(ctx *pulumi.Context, name string, args *RestoreArgs, opts ...pulumi.ResourceOption) (*Restore, error) {
	if args == nil {
		return nil, errors.New("sluispulumi: RestoreArgs is nil")
	}
	if args.Archive.Create != nil {
		return nil, errors.New("sluispulumi: RestoreArgs.Archive.Create: the restore function reads an archive and creates none; NewBackup creates it")
	}
	if err := errors.Join(args.RestoreInvokers.validate(), args.Verify.validate()); err != nil {
		return nil, fmt.Errorf("sluispulumi: %w", err)
	}
	p, err := args.plan(RoleRestore)
	if err != nil {
		return nil, err
	}
	p.verify = args.Verify
	if p.Function.TimeoutSeconds < MinRestoreTimeoutSeconds {
		return nil, fmt.Errorf("sluispulumi: Function.TimeoutSeconds %d is under %d: "+
			"the restore keeps back %s of every slice, and a shorter timeout leaves it no time to work",
			p.Function.TimeoutSeconds, MinRestoreTimeoutSeconds, RestoreMargin)
	}
	out := &Restore{}
	if err := ctx.RegisterComponentResource(RestoreType, name, out, opts...); err != nil {
		return nil, err
	}
	parent := pulumi.Parent(out)
	self := p.restoreAliasArns()
	classes := []string{ClassAdmin, ClassBreakglass}
	noRetry := []string{CallerAlias(ClassAdmin), CallerAlias(ClassBreakglass)}
	fn, err := buildFunction(ctx, name, p, pulumi.String(arnPrefix+"s3:::"+p.Archive.Bucket), classes, noRetry, true,
		func(env ModuleEnv, g ArchiveGrant) ([]map[string]any, error) {
			return RestoreRoleStatements(env, g, self)
		}, args.Recipients, out)
	if err != nil {
		return nil, err
	}

	// ---- the resource policy: one permission per invoker, on the alias of its class.
	for _, c := range []struct {
		class string
		arns  []string
	}{{ClassAdmin, args.RestoreInvokers.AdminRoleArns}, {ClassBreakglass, args.RestoreInvokers.BreakglassRoleArns}} {
		for i, arn := range c.arns {
			if _, err := lambda.NewPermission(ctx, fmt.Sprintf("%s-invoke-%s-%d", name, c.class, i), &lambda.PermissionArgs{
				Action: pulumi.String(lambdaInvokeFunction), Function: fn.fn.Name, Principal: pulumi.String(arn),
				Qualifier: fn.aliases[CallerAlias(c.class)].Name, StatementId: pulumi.Sprintf("SluisRestoreInvoke-%s-%d", c.class, i),
			}, parent); err != nil {
				return nil, fmt.Errorf("sluis restore permission: %w", err)
			}
		}
	}

	// ---- the alarm: a restore is rare, and every one is looked at.
	alarmName := p.FunctionName + "-invoked"
	if _, err := alarm(ctx, name+"-alarm-invoked", alarmName, "The restore function was invoked.", p.AlarmActionArns, &cloudwatch.MetricAlarmArgs{
		Namespace: pulumi.String("AWS/Lambda"), MetricName: pulumi.String("Invocations"), Statistic: pulumi.String("Sum"),
		Dimensions: pulumi.StringMap{"FunctionName": fn.fn.Name},
		Period:     pulumi.Int(60), EvaluationPeriods: pulumi.Int(1), Threshold: pulumi.Float64(1),
		ComparisonOperator: pulumi.String("GreaterThanOrEqualToThreshold"), TreatMissingData: pulumi.String("notBreaching"),
	}, parent); err != nil {
		return nil, err
	}

	out.FunctionArn, out.FunctionName = fn.fn.Arn, fn.fn.Name
	out.RoleArn, out.RoleName = fn.role.Arn, fn.role.Name
	out.ConfigLayerArn, out.AliasArns, out.ArchiveKeyArn = fn.layer.Arn, fn.aliasArns(), fn.archiveKey
	out.AlarmNames = pulumi.StringArray{pulumi.String(alarmName)}.ToStringArrayOutput()
	out.IdentityParameter = pulumi.String(RestoreIdentityParameterName(p.Instance)).ToStringOutput()
	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{
		"functionArn": out.FunctionArn, "functionName": out.FunctionName, "roleArn": out.RoleArn, "roleName": out.RoleName,
		"configLayerArn": out.ConfigLayerArn, "aliasArns": out.AliasArns, "archiveKeyArn": out.ArchiveKeyArn, "alarmNames": out.AlarmNames,
		"identityParameter": out.IdentityParameter,
	}); err != nil {
		return nil, err
	}
	return out, nil
}
