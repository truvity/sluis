package sluispulumi

import (
	"errors"
	"fmt"
	"sort"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/sluis/audit/deploy/pulumi/artifact"
	sluisconfig "github.com/truvity/sluis/config"
)

// ModuleCheck asks newModuleFunction for the post-deploy check of the function:
// one invocation of the `live` alias after the function, its alias and its
// role's policy exist (and After). Declared are the names whose hash is a
// trigger of the check. A function whose binary does not answer the `check`
// event leaves it unset.
type ModuleCheck struct {
	Declared []string
	After    []pulumi.Resource
}

// ModuleFunctionArgs describes one module's function for newModuleFunction.
type ModuleFunctionArgs struct {
	// Name is the logical-name prefix of every resource the function makes:
	// `<Name>-config`, `-logs`, `-role`, `-policy`, `-fn`, `-alias-<alias>`,
	// `-async-<alias>`, `-check`. It is part of the resources' identity in the
	// state, so a function moved onto the builder keeps the name it had.
	Name string
	// FunctionName names the function, its role, its policy, its log group and
	// its configuration layer (`<FunctionName>-config`).
	FunctionName string

	// Package, PackageSHA256, PackageVersion and Release name the release zip.
	Package        string
	PackageSHA256  string
	PackageVersion string
	Release        *ReleaseArgs
	// Artifacts uploads the zip and the layer to a bucket; unset, they are
	// uploaded by Pulumi as assets.
	Artifacts *ArtifactsArgs

	// Documents are the configuration layer's files, by name without the
	// extension; they are written to `sluis/<name>.yaml`. The one named
	// [docSluis] is the function's SLUIS_CONFIG. Required.
	Documents map[string]string
	// DocumentVersion is the apiVersion the layer description names.
	DocumentVersion string

	MemoryMB, TimeoutSeconds int
	// ReservedConcurrency is the function's reserved concurrency; nil is unreserved.
	ReservedConcurrency    *int
	LogRetentionDays       int
	PermissionsBoundaryArn string
	Tags                   map[string]string

	// Policy is the role's policy document, from the log group's ARN. Most
	// callers render it with [ModuleRolePolicy].
	Policy func(logGroupArn pulumi.StringOutput) pulumi.StringInput

	// Callers are the caller classes: each gets the alias `live-<class>`. The
	// function also has the alias `live`, unless OmitLive is set.
	Callers []string
	// OmitLive leaves out the alias `live`, for a function nothing invokes
	// unqualified or by schedule (the restore). It excludes Check.
	OmitLive bool
	// NoRetry are the aliases whose asynchronous invocations are not retried by
	// the platform (a run that failed is the next one's).
	NoRetry []string
	// Check, when set, adds the post-deploy check.
	Check *ModuleCheck
	// Telemetry is the OpenTelemetry layer of the function: first in its layers,
	// the configuration layer last, with the OTEL_* environment and
	// OTEL_SERVICE_NAME defaulting to FunctionName. Nil: none. The caller
	// validates it ([TelemetryArgs] validate) and grants the role's web identity
	// token for the layer's audiences.
	Telemetry *TelemetryArgs
}

// moduleFunction is what newModuleFunction makes of one function.
type moduleFunction struct {
	fn         *lambda.Function
	role       *iam.Role
	rolePolicy *iam.RolePolicy
	logs       *cloudwatch.LogGroup
	layer      *lambda.LayerVersion
	aliases    map[string]*lambda.Alias
	names      []string
}

// newModuleFunction makes one module's function from the release zip: the
// configuration layer with its own documents, the log group, the role with its
// policy, the function (published), the aliases `live` and `live-<class>` per
// caller, the asynchronous invoke config with no retries, and the post-deploy
// check when asked for.
func newModuleFunction(ctx *pulumi.Context, a *ModuleFunctionArgs, parent pulumi.ResourceOption) (*moduleFunction, error) {
	if a == nil {
		return nil, errors.New("sluispulumi: ModuleFunctionArgs is nil")
	}
	if a.Name == "" || a.FunctionName == "" || a.Policy == nil {
		return nil, errors.New("sluispulumi: a module function needs Name, FunctionName and Policy")
	}
	if _, ok := a.Documents[docSluis]; !ok {
		return nil, fmt.Errorf("sluispulumi: the module function %s has no %q document", a.FunctionName, docSluis)
	}
	if err := a.Telemetry.validate(); err != nil {
		return nil, err
	}
	pkg, err := loadPackage(a.Package, a.PackageSHA256, packageRelease(a.Package, a.PackageVersion), a.Release)
	if err != nil {
		return nil, err
	}
	name := a.Name
	tags := tagMap(a.Tags)
	out := &moduleFunction{aliases: map[string]*lambda.Alias{}}

	// ---- the configuration layer: the documents.
	files := map[string][]byte{}
	assets := map[string]any{}
	for d, content := range a.Documents {
		files["sluis/"+d+".yaml"] = []byte(content)
		assets["sluis/"+d+".yaml"] = pulumi.NewStringAsset(content)
	}
	layerArgs := &lambda.LayerVersionArgs{
		LayerName:               pulumi.String(a.FunctionName + "-config"),
		Description:             pulumi.String(a.FunctionName + " configuration: the " + a.DocumentVersion + " document, at " + LayerRoot),
		CompatibleRuntimes:      pulumi.StringArray{pulumi.String("provided.al2023")},
		CompatibleArchitectures: pulumi.StringArray{pulumi.String("arm64")},
		SkipDestroy:             pulumi.Bool(true),
	}
	var code *artifact.Object
	if a.Artifacts != nil {
		if code, err = uploadArtifact(ctx, name+"-code", a.Artifacts, pkg, parent); err != nil {
			return nil, fmt.Errorf("sluis artifacts: %w", err)
		}
		layerZip, err := artifact.Zip(files)
		if err != nil {
			return nil, fmt.Errorf("sluis configuration layer: %w", err)
		}
		sum := artifact.Sum(layerZip)
		local, err := artifact.WriteTemp("sluis-layer-", "config.zip", layerZip)
		if err != nil {
			return nil, fmt.Errorf("sluis configuration layer: %w", err)
		}
		obj, err := artifact.Upload(ctx, name+"-config-code", *a.Artifacts, a.Artifacts.Key(pkg.Version, sum, a.FunctionName+"-config.zip"), local, sum, parent)
		if err != nil {
			return nil, fmt.Errorf("sluis artifacts: %w", err)
		}
		layerArgs.S3Bucket, layerArgs.S3Key, layerArgs.S3ObjectVersion = pulumi.String(obj.Bucket), pulumi.String(obj.Key), obj.VersionID
		layerArgs.SourceCodeHash = pulumi.String(obj.CodeSHA256)
	} else {
		layerArgs.Code = pulumi.NewAssetArchive(assets)
	}
	if out.layer, err = lambda.NewLayerVersion(ctx, name+"-config", layerArgs, parent); err != nil {
		return nil, fmt.Errorf("sluis configuration layer: %w", err)
	}

	// ---- the log group and the role
	if out.logs, err = cloudwatch.NewLogGroup(ctx, name+"-logs", &cloudwatch.LogGroupArgs{
		Name: pulumi.String("/aws/lambda/" + a.FunctionName), RetentionInDays: pulumi.Int(a.LogRetentionDays), Tags: tags,
	}, parent); err != nil {
		return nil, fmt.Errorf("sluis log group: %w", err)
	}
	rargs := &iam.RoleArgs{Name: pulumi.String(a.FunctionName), AssumeRolePolicy: pulumi.String(lambdaTrust()), Tags: tags}
	if a.PermissionsBoundaryArn != "" {
		rargs.PermissionsBoundary = pulumi.String(a.PermissionsBoundaryArn)
	}
	if out.role, err = iam.NewRole(ctx, name+"-role", rargs, parent); err != nil {
		return nil, fmt.Errorf("sluis role: %w", err)
	}
	if out.rolePolicy, err = iam.NewRolePolicy(ctx, name+"-policy", &iam.RolePolicyArgs{
		Name: pulumi.String(a.FunctionName), Role: out.role.Name, Policy: a.Policy(out.logs.Arn),
	}, parent); err != nil {
		return nil, fmt.Errorf("sluis role policy: %w", err)
	}

	// ---- the function. The configuration layer is last; the telemetry layer,
	// when there is one, is first.
	env := a.Telemetry.env(a.FunctionName)
	env[sluisconfig.EnvConfig] = pulumi.String(DocumentPath(docSluis))
	fnArgs := &lambda.FunctionArgs{
		Publish:       pulumi.Bool(true),
		Name:          pulumi.String(a.FunctionName),
		Role:          out.role.Arn,
		Runtime:       pulumi.String("provided.al2023"),
		Handler:       pulumi.String("bootstrap"),
		Architectures: pulumi.StringArray{pulumi.String("arm64")},
		MemorySize:    pulumi.Int(a.MemoryMB),
		Timeout:       pulumi.Int(a.TimeoutSeconds),
		Layers:        a.Telemetry.layers(out.layer.Arn),
		Environment:   &lambda.FunctionEnvironmentArgs{Variables: env},
		LoggingConfig: &lambda.FunctionLoggingConfigArgs{LogFormat: pulumi.String("Text"), LogGroup: out.logs.Name},
		Tags:          tags,
	}
	if a.ReservedConcurrency != nil {
		fnArgs.ReservedConcurrentExecutions = pulumi.Int(*a.ReservedConcurrency)
	}
	if code != nil {
		fnArgs.S3Bucket, fnArgs.S3Key, fnArgs.S3ObjectVersion = pulumi.String(code.Bucket), pulumi.String(code.Key), code.VersionID
		fnArgs.SourceCodeHash = pulumi.String(code.CodeSHA256)
	} else {
		fnArgs.Code = pulumi.NewFileArchive(pkg.Path)
	}
	if out.fn, err = lambda.NewFunction(ctx, name+"-fn", fnArgs, parent,
		pulumi.DependsOn([]pulumi.Resource{out.logs, out.rolePolicy})); err != nil {
		return nil, fmt.Errorf("sluis function: %w", err)
	}

	// ---- the aliases: all follow the version just published. A call carries
	// its caller class in the alias it invokes through.
	var aliasNames []string
	if !a.OmitLive {
		aliasNames = append(aliasNames, LiveAlias)
	}
	for _, c := range a.Callers {
		aliasNames = append(aliasNames, CallerAlias(c))
	}
	sort.Strings(aliasNames)
	for _, an := range aliasNames {
		al, err := lambda.NewAlias(ctx, name+"-alias-"+an, &lambda.AliasArgs{
			Name: pulumi.String(an), Description: pulumi.String("The version " + an + " callers invoke."),
			FunctionName: out.fn.Name, FunctionVersion: out.fn.Version,
		}, parent)
		if err != nil {
			return nil, fmt.Errorf("sluis alias %s: %w", an, err)
		}
		out.aliases[an] = al
		out.names = append(out.names, an)
	}
	// A run that failed is the next one's: no retry by the platform.
	for _, an := range a.NoRetry {
		al, ok := out.aliases[an]
		if !ok {
			return nil, fmt.Errorf("sluispulumi: NoRetry names the alias %q, which the function %s does not have", an, a.FunctionName)
		}
		if _, err := lambda.NewFunctionEventInvokeConfig(ctx, name+"-async-"+an, &lambda.FunctionEventInvokeConfigArgs{
			FunctionName: out.fn.Name, Qualifier: al.Name, MaximumRetryAttempts: pulumi.Int(0),
		}, parent); err != nil {
			return nil, fmt.Errorf("sluis invoke config %s: %w", an, err)
		}
	}
	if c := a.Check; c != nil {
		if a.OmitLive {
			return nil, errors.New("sluispulumi: a check invokes the live alias, which OmitLive leaves out")
		}
		after := append([]pulumi.Resource{out.aliases[LiveAlias], out.rolePolicy}, c.After...)
		if err := newCheck(ctx, name, out.aliases[LiveAlias], out.fn.Name, c.Declared, after, parent); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// aliasArns is the alias ARNs by alias name.
func (b *moduleFunction) aliasArns() pulumi.StringMapOutput {
	m := pulumi.StringMap{}
	for k, a := range b.aliases {
		m[k] = a.Arn
	}
	return m.ToStringMapOutput()
}
