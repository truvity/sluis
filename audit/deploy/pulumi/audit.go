// Package auditpulumi is the AWS shape of the audit trail as a Pulumi Go
// library: the archive bucket and its keys, the ingest queue, the writer and
// notary Lambda functions with the role of each, the schedule that invokes the
// notary, and the alarms that say when any of it is not working. The functions'
// code is the release's zip, checked against its digest; their configuration is a
// layer of its own.
//
// It is a library, not a program: a stack calls New with the arguments below
// and gets a component with the outputs a deployment needs. It creates nothing
// by being imported, this repository deploys nothing with it, and it is a
// module of its own (github.com/truvity/sluis/audit/deploy/pulumi) so that Pulumi is
// not in the root module's dependency graph.
//
//	a, err := auditpulumi.New(ctx, "audit", &auditpulumi.Args{
//		Presets: map[string]auditpulumi.PresetStorage{
//			"standard": {Bucket: "acme-audit-standard", Prefix: "standard/", Create: true},
//		},
//		Writer:  auditpulumi.WriterArgs{Package: writerZip, PackageSHA256: writerSHA, DeploymentYAML: deployment},
//		Notary:  auditpulumi.NotaryArgs{Package: notaryZip, PackageSHA256: notarySHA},
//	})
//
// docs/reference/audit/aws-pulumi-library.md is the guide: the shape, every input and output, the
// switch from GOVERNANCE to COMPLIANCE, the role names and how alarms reach
// alert-ingress.
package auditpulumi

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/dynamodb"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/eks"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sns"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sqs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/sluis/audit/deploy/pulumi/artifact"
	"github.com/truvity/sluis/audit/profile"
)

// ComponentType is the Pulumi type token of the component.
const ComponentType = "truvity:audit:Audit"

// Audit is the component. Its fields are the outputs.
type Audit struct {
	pulumi.ResourceState

	// BucketNames are the preset's buckets by preset name, every configured
	// preset's. BucketArns are the AWS presets' (a preset at an endpoint has none:
	// the bucket is the store's).
	BucketNames pulumi.StringMapOutput
	BucketArns  pulumi.StringMapOutput
	// ArchiveKeyArn is the symmetric key objects are encrypted with; SealKeyArn
	// is the P-384 key seals are signed with, and SealKeyAlias its alias, which
	// is what the notary's configuration names. Both are the estate's keys
	// (Keys.Archive, Keys.Seal), resolved from their aliases.
	ArchiveKeyArn pulumi.StringOutput
	SealKeyArn    pulumi.StringOutput
	SealKeyAlias  pulumi.StringOutput
	// QueueURL and QueueArn are the ingest queue a receiver or an application
	// sends to; DlqURL and DlqArn its dead-letter queue.
	QueueURL pulumi.StringOutput
	QueueArn pulumi.StringOutput
	DlqURL   pulumi.StringOutput
	DlqArn   pulumi.StringOutput
	// DedupeTableName is the DynamoDB table of written record ids.
	DedupeTableName pulumi.StringOutput
	// WriterFunctionArn and NotaryFunctionArn are the functions.
	WriterFunctionArn pulumi.StringOutput
	NotaryFunctionArn pulumi.StringOutput
	// WriterCodeSha256Matches and NotaryCodeSha256Matches are true when the code
	// Lambda reports for the function has the SHA-256 of the release zip the
	// library verified, which is the assurance that what runs is what the
	// release's checksums name. False for a function that is not installed.
	WriterCodeSha256Matches pulumi.BoolOutput
	// WriterLiveAliasArn and NotaryLiveAliasArn are the aliases (`live`) the
	// writer's event source and the notary's schedule invoke, and
	// WriterLiveVersion and NotaryLiveVersion the function versions they point
	// at. Empty for a function that is not installed.
	WriterLiveAliasArn, NotaryLiveAliasArn pulumi.StringOutput
	WriterLiveVersion, NotaryLiveVersion   pulumi.StringOutput
	NotaryCodeSha256Matches                pulumi.BoolOutput
	// WriterRoleArn and NotaryRoleArn are the roles the functions run as: the
	// ARNs a roster or gitops grant names, and the identities the OTLP door
	// sees. Every output of a part that is turned off (Ingest.Disabled,
	// Notary.Disabled) or not applicable (ArchiveKeyArn with Encryption "s3") is
	// the empty string. ObserveReaderRoleArn is the cross-account read role, empty without
	// Args.Observe.
	WriterRoleArn        pulumi.StringOutput
	NotaryRoleArn        pulumi.StringOutput
	ObserveReaderRoleArn pulumi.StringOutput
	// ArchiveWriterRoleArn is the IRSA write role, empty without Args.ArchiveWriter.
	ArchiveWriterRoleArn pulumi.StringOutput
	// QueryRoleArn is the Pod Identity role of audit-query, empty without Args.Query.
	QueryRoleArn pulumi.StringOutput
	// ArchiveCredentialsPaths are the SSM parameters the functions read the
	// credentials of each preset at an endpoint from, by preset name: write a
	// SecureString there, a JSON object {"accessKeyID": ..., "secretAccessKey": ...},
	// before the first record (docs/guides/audit/operate/archive-on-r2.md); for a preset with
	// CredentialsPreset it is the MINTER credential's address (a cloudflare-minter/v1
	// document). A preset on AWS S3
	// has none: the roles are the credential.
	ArchiveCredentialsPaths pulumi.StringMapOutput
	// SecretsRoot is the SSM parameter path the writer reads the secrets its
	// configuration names from: create the SecureStrings under it. Empty when the
	// configuration names none (see WriterArgs.Secrets).
	SecretsRoot pulumi.StringOutput
	// Presets are the install presets the installation configures, weakest first.
	Presets pulumi.StringArrayOutput
	// DeploymentYAML is the deployment document the functions read: the profiles
	// of Writer.DeploymentYAML and the presets built from Args.Presets.
	DeploymentYAML pulumi.StringOutput
	// AlarmTopicArn is the SNS topic every alarm publishes to.
	AlarmTopicArn pulumi.StringOutput
	// ScheduleArn is the notary's schedule.
	ScheduleArn pulumi.StringOutput
}

// New creates the component and everything under it. name is the installation's
// name, and is in every physical name the library gives: `<name>-writer`,
// `<name>-ingest`, and so on, so one account may hold several installations.
func New(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*Audit, error) {
	a, err := args.withDefaults(name)
	if err != nil {
		return nil, err
	}
	out := &Audit{}
	if err := ctx.RegisterComponentResource(ComponentType, name, out, opts...); err != nil {
		return nil, err
	}
	child := pulumi.Parent(out)
	tags := pulumi.ToStringMap(a.Tags)
	ingest, notary := !a.Ingest.Disabled, !a.Notary.Disabled
	// The library creates no key. The estate's keys are looked up by alias, as
	// invokes made through the component's own provider, and their ARNs are what
	// the policies name.
	keyArns, err := lookupKeys(ctx, a, child)
	if err != nil {
		return nil, err
	}
	// What the installation keeps in its state store: the archive's credentials on
	// an S3-compatible store (read by every function that opens the archive), and
	// the per-tenant secrets behind pseudonyms (the writer's, which creates them).
	endpoints := a.endpointStores()
	needState := (len(endpoints) > 0 && (ingest || notary)) || (a.Keys.Pseudonym != "" && ingest)

	// The account is looked up through the component's own provider: the invoke
	// has the component as its parent, so it resolves the provider the caller
	// gave New (pulumi.Provider, pulumi.Providers) or inherited. A stack with the
	// default providers disabled has no other one. AccountID skips the lookup.
	accountID := a.AccountID
	if accountID == "" && (a.Writer.Secrets != nil || needState) {
		identity, err := aws.GetCallerIdentity(ctx, nil, child)
		if err != nil {
			return nil, fmt.Errorf("auditpulumi: the caller's account (pass the AWS provider with pulumi.Provider, or set Args.AccountID): %w", err)
		}
		accountID = identity.AccountId
	}
	// What the writer may read of SSM, when its configuration names secrets.
	var grant *secretGrant
	if s := a.Writer.Secrets; s != nil && ingest {
		region := a.Region
		if region == "" {
			r, err := aws.GetRegion(ctx, nil, child)
			if err != nil {
				return nil, fmt.Errorf("auditpulumi: the region for the SSM grant (pass the AWS provider with pulumi.Provider, or set Args.Region): %w", err)
			}
			region = r.Region
		}
		grant = &secretGrant{Root: s.Root, Region: region, Account: accountID, KeyArn: s.KeyArn}
	}

	// What each function may read and write of the state store.
	var writerState, notaryState *stateGrant
	if needState {
		region := a.Region
		if region == "" {
			r, err := aws.GetRegion(ctx, nil, child)
			if err != nil {
				return nil, fmt.Errorf("auditpulumi: the region for the SSM grant (pass the AWS provider with pulumi.Provider, or set Args.Region): %w", err)
			}
			region = r.Region
		}
		// A store with static credentials is read at that one address. One that
		// mints its credentials reads the minter at its one address and reads and
		// writes the record of the tokens it minted at one, never anything wider.
		var credentials, records []string
		for _, e := range endpoints {
			if e.minted() {
				credentials = append(credentials, e.minterPath(a.State.Root))
				records = append(records, e.recordPath(a.State.Root))
				continue
			}
			credentials = append(credentials, e.credentialsPath(a.State.Root))
		}
		base := stateGrant{Region: region, Account: accountID, KeyArn: a.State.KeyArn}
		if ingest {
			g := base
			g.Read = append(g.Read, credentials...)
			g.Write = append(g.Write, records...)
			if a.Keys.Pseudonym != "" {
				g.Write = append(g.Write, a.State.Root+"/"+pseudonymStateAddress+"/*")
			}
			writerState = &g
		}
		if notary && len(credentials) > 0 {
			g := base
			g.Read = append(g.Read, credentials...)
			g.Write = append(g.Write, records...)
			notaryState = &g
		}
	}

	// The released packages are read and checked, and what each function's
	// configuration layer holds is rendered, before anything is created: an
	// argument that cannot be rendered, a zip that is not the one named, or a
	// binary that is not this library's release fails the preview, and so does a
	// catalogue the archive already holds with other content. All of that is
	// refused here, ahead of the function, because a function that fails its
	// init drains the ingest queue into the dead-letter queue.
	var writerPkg, notaryPkg *releasePackage
	var writerLayer, notaryLayer map[string]string
	pkgs := map[string]*releasePackage{}
	if ingest {
		if writerPkg, err = loadPackage("Writer", a.Writer.Package, a.Writer.PackageSHA256, "audit-writer-lambda", a.Release); err != nil {
			return nil, err
		}
		pkgs["Writer"] = writerPkg
		if writerLayer, err = writerFiles(name, a); err != nil {
			return nil, err
		}
	}
	if notary {
		if notaryPkg, err = loadPackage("Notary", a.Notary.Package, a.Notary.PackageSHA256, "audit-notary-lambda", a.Release); err != nil {
			return nil, err
		}
		pkgs["Notary"] = notaryPkg
		if notaryLayer, err = notaryFiles(name, a); err != nil {
			return nil, err
		}
	}
	if err := checkVersions(a.Guards, pkgs); err != nil {
		return nil, err
	}
	if err := checkCatalogues(ctx, a, child); err != nil {
		return nil, err
	}

	// ---- the roles come first: the seal key's policy names the notary's.
	var writerRole, notaryRole *iam.Role
	if ingest {
		if writerRole, err = newRole(ctx, name+"-writer", a.RolePath, assumeRoleJSON("lambda.amazonaws.com"), tags, child); err != nil {
			return nil, err
		}
	}
	if notary {
		if notaryRole, err = newRole(ctx, name+"-notary", a.RolePath, assumeRoleJSON("lambda.amazonaws.com"), tags, child); err != nil {
			return nil, err
		}
	}

	// ---- the archive's keys and the seal key are the estate's, looked up by alias
	// A preset that names a key alias is encrypted under the key behind it. The
	// library creates no key for it (ADR 0041): the alias is the estate's, and it is
	// resolved with the same lookup as Keys (lookupAlias), so that the roles are
	// granted the key it points at. A preset at an endpoint names none (the store
	// has no KMS to encrypt under: refused by withDefaults).
	presetKeys := map[profile.Preset]string{}
	for _, st := range a.awsStores() {
		if st.KeyAlias == "" {
			continue
		}
		arn, err := lookupAlias(ctx, "Presets["+string(st.Preset)+"].KeyAlias", st.KeyAlias, child)
		if err != nil {
			return nil, err
		}
		presetKeys[st.Preset] = arn
	}
	// defaultKeyArn is the archive key (Keys.Archive's, or Archive.KeyArn), or the
	// empty string with SSE-S3, with the AWS-managed key and for every preset at an
	// endpoint: none needs an IAM grant.
	defaultKeyArn := ""
	switch {
	case a.Archive.Encryption != EncryptionKMS:
	case keyArns.Archive != "":
		defaultKeyArn = keyArns.Archive
	case a.Archive.KeyArn != "":
		defaultKeyArn = a.Archive.KeyArn
	}
	// grantKeys is every key the archive's objects are under: the archive key where
	// an AWS preset names none of its own, and the keys behind the presets' aliases.
	var grantKeys []string
	for _, st := range a.awsStores() {
		if arn, ok := presetKeys[st.Preset]; ok {
			grantKeys = append(grantKeys, arn)
		} else if defaultKeyArn != "" {
			grantKeys = append(grantKeys, defaultKeyArn)
		}
	}
	grantKeys = dedupe(grantKeys)

	// ---- the archive: a bucket for each preset the library creates
	var refs []bucketRef
	bucketNames, bucketArns := pulumi.StringMap{}, pulumi.StringMap{}
	for _, st := range a.stores { //nolint:gocritic // read-only configuration, copied once per preset
		bucketNames[string(st.Preset)] = pulumi.String(st.Bucket)
		if st.external() {
			continue
		}
		var arn pulumi.StringInput = pulumi.String(bucketARN(st.Bucket))
		if st.managed() {
			key := presetKeys[st.Preset]
			if key == "" {
				key = defaultKeyArn
			}
			bucket, err := newArchive(ctx, name, a, st, pulumi.String(key).ToStringOutput(), tags, child)
			if err != nil {
				return nil, err
			}
			arn = bucket.Arn
		}
		bucketArns[string(st.Preset)] = arn
		refs = append(refs, bucketRef{Arn: arn, Prefix: st.Prefix, Locked: st.Locked})
	}

	// ---- the queue, its dead-letter queue and the deduplication table
	var queue, dlq *sqs.Queue
	var table *dynamodb.Table
	if ingest {
		if queue, dlq, err = newQueues(ctx, name, a, tags, child); err != nil {
			return nil, err
		}
		table, err = dynamodb.NewTable(ctx, name+"-dedupe", &dynamodb.TableArgs{
			Name:        pulumi.String(dedupeTable(name)),
			BillingMode: pulumi.String("PAY_PER_REQUEST"),
			HashKey:     pulumi.String("pk"),
			Attributes:  dynamodb.TableAttributeArray{&dynamodb.TableAttributeArgs{Name: pulumi.String("pk"), Type: pulumi.String("S")}},
			// The attribute the writer sets on each item; DynamoDB deletes an item
			// after it, lazily, which is why the store also checks the time itself.
			Ttl:  &dynamodb.TableTtlArgs{AttributeName: pulumi.String("expires_at"), Enabled: pulumi.Bool(true)},
			Tags: tags,
		}, child)
		if err != nil {
			return nil, err
		}
	}

	audience := ""
	if a.Telemetry != nil {
		audience = a.Telemetry.STSAudience
	}

	// ---- the writer: function, policy, and the queue feeding it
	var writerFn *lambda.Function
	var writerLive *lambda.Alias
	var writerLogsGroup *cloudwatch.LogGroup
	if ingest {
		var writerLogs *cloudwatch.LogGroup
		writerFn, writerLive, writerLogs, err = newFunction(ctx, functionSpec{
			Name: name + "-writer", Service: writerService, Role: writerRole, Package: writerPkg,
			MemoryMB: a.Writer.MemoryMB, TimeoutSeconds: a.Writer.TimeoutSeconds,
			Config: writerLayer,
		}, a, tags, child)
		if err != nil {
			return nil, err
		}
		writerLogsGroup = writerLogs
		if _, err := iam.NewRolePolicy(ctx, name+"-writer", &iam.RolePolicyArgs{
			Role: writerRole.Name,
			Policy: applyPolicy(refs, []any{table.Arn, queue.Arn, writerLogs.Arn}, func(b []bucketGrant, v []any) string {
				return writerPolicy(b, grantKeys, v[0].(string), v[1].(string), v[2].(string), audience, grant,
					append(writerKeyStatements(keyArns, a.instance(name)), writerState.statements()...))
			}),
		}, child); err != nil {
			return nil, err
		}
		if _, err := lambda.NewEventSourceMapping(ctx, name+"-writer", &lambda.EventSourceMappingArgs{
			EventSourceArn:                 queue.Arn,
			FunctionName:                   writerLive.Arn,
			BatchSize:                      pulumi.Int(a.Writer.BatchSize),
			MaximumBatchingWindowInSeconds: pulumi.Int(a.Writer.MaxBatchingWindowSeconds),
			// Partial batch responses: a message that was archived is not redelivered
			// because another in its batch failed.
			FunctionResponseTypes: pulumi.StringArray{pulumi.String("ReportBatchItemFailures")},
			ScalingConfig:         &lambda.EventSourceMappingScalingConfigArgs{MaximumConcurrency: pulumi.Int(a.Writer.MaxConcurrency)},
		}, child); err != nil {
			return nil, err
		}
	}

	// ---- the notary: function, policy, and its schedule
	var notaryFn *lambda.Function
	var notaryLive *lambda.Alias
	var schedule *scheduler.Schedule
	if notary {
		var notaryLogs *cloudwatch.LogGroup
		notaryFn, notaryLive, notaryLogs, err = newFunction(ctx, functionSpec{
			Name: name + "-notary", Service: notaryService, Role: notaryRole, Package: notaryPkg,
			MemoryMB: a.Notary.MemoryMB, TimeoutSeconds: a.Notary.TimeoutSeconds,
			Config: notaryLayer,
		}, a, tags, child)
		if err != nil {
			return nil, err
		}
		if _, err := iam.NewRolePolicy(ctx, name+"-notary", &iam.RolePolicyArgs{
			Role: notaryRole.Name,
			Policy: applyPolicy(refs, []any{notaryLogs.Arn}, func(b []bucketGrant, v []any) string {
				return notaryPolicy(b, grantKeys, keyArns.Seal, v[0].(string), audience, notaryState.statements())
			}),
		}, child); err != nil {
			return nil, err
		}
		if schedule, err = newSchedule(ctx, name, a, notaryFn, notaryLive, tags, child); err != nil {
			return nil, err
		}
	}

	// ---- alarms
	// The operational preset has none: nobody is paged for an installation that
	// asked for no more than the write path.
	var topic *sns.Topic
	if a.features.Alarms {
		topic, err = newAlarms(ctx, name, a, alarmTargets{
			Queue: queue, Dlq: dlq, Writer: writerFn, Notary: notaryFn, WriterLogs: writerLogsGroup,
		}, tags, child)
		if err != nil {
			return nil, err
		}
	}

	// ---- the read role for observe, and the write role for a workload outside AWS
	empty := pulumi.String("").ToStringOutput()
	observeArn, archiveWriterArn := empty, empty
	if a.Observe != nil {
		role, err := newObserveReader(ctx, name, a, refs, grantKeys, tags, child)
		if err != nil {
			return nil, err
		}
		observeArn = role.Arn
	}
	queryArn := empty
	if a.Query != nil {
		qa := empty
		if ingest {
			qa = queue.Arn
		}
		role, err := newQuery(ctx, name, a, refs, grantKeys, qa, tags, child)
		if err != nil {
			return nil, err
		}
		queryArn = role.Arn
	}
	if a.ArchiveWriter != nil {
		role, err := newArchiveWriter(ctx, name, a, refs, grantKeys, tags, child)
		if err != nil {
			return nil, err
		}
		archiveWriterArn = role.Arn
	}

	// Outputs of a part that is not there are empty strings.
	pick := func(on bool, o func() pulumi.StringOutput) pulumi.StringOutput {
		if !on {
			return empty
		}
		return o()
	}
	out.BucketNames, out.BucketArns = bucketNames.ToStringMapOutput(), bucketArns.ToStringMapOutput()
	out.ArchiveKeyArn = pulumi.String(defaultKeyArn).ToStringOutput()
	out.SealKeyArn = pick(notary, func() pulumi.StringOutput { return pulumi.String(keyArns.Seal).ToStringOutput() })
	out.SealKeyAlias = pick(notary, func() pulumi.StringOutput { return pulumi.String(a.Keys.Seal).ToStringOutput() })
	credentialsPaths := pulumi.StringMap{}
	if ingest || notary {
		for _, e := range endpoints {
			if e.minted() {
				credentialsPaths[string(e.Preset)] = pulumi.String(e.minterPath(a.State.Root))
				continue
			}
			credentialsPaths[string(e.Preset)] = pulumi.String(e.credentialsPath(a.State.Root))
		}
	}
	out.ArchiveCredentialsPaths = credentialsPaths.ToStringMapOutput()
	out.QueueURL = pick(ingest, func() pulumi.StringOutput { return queue.Url })
	out.QueueArn = pick(ingest, func() pulumi.StringOutput { return queue.Arn })
	out.DlqURL = pick(ingest, func() pulumi.StringOutput { return dlq.Url })
	out.DlqArn = pick(ingest, func() pulumi.StringOutput { return dlq.Arn })
	out.DedupeTableName = pick(ingest, func() pulumi.StringOutput { return table.Name })
	out.WriterFunctionArn = pick(ingest, func() pulumi.StringOutput { return writerFn.Arn })
	out.NotaryFunctionArn = pick(notary, func() pulumi.StringOutput { return notaryFn.Arn })
	out.WriterLiveAliasArn = pick(ingest, func() pulumi.StringOutput { return writerLive.Arn })
	out.NotaryLiveAliasArn = pick(notary, func() pulumi.StringOutput { return notaryLive.Arn })
	out.WriterLiveVersion = pick(ingest, func() pulumi.StringOutput { return writerLive.FunctionVersion })
	out.NotaryLiveVersion = pick(notary, func() pulumi.StringOutput { return notaryLive.FunctionVersion })
	out.WriterCodeSha256Matches, out.NotaryCodeSha256Matches = pulumi.Bool(false).ToBoolOutput(), pulumi.Bool(false).ToBoolOutput()
	if ingest {
		out.WriterCodeSha256Matches = artifact.Matches(writerFn.CodeSha256, writerPkg.CodeSHA256)
	}
	if notary {
		out.NotaryCodeSha256Matches = artifact.Matches(notaryFn.CodeSha256, notaryPkg.CodeSHA256)
	}
	out.WriterRoleArn = pick(ingest, func() pulumi.StringOutput { return writerRole.Arn })
	out.NotaryRoleArn = pick(notary, func() pulumi.StringOutput { return notaryRole.Arn })
	out.ObserveReaderRoleArn, out.ArchiveWriterRoleArn, out.QueryRoleArn = observeArn, archiveWriterArn, queryArn
	out.SecretsRoot = pulumi.String("").ToStringOutput()
	if grant != nil {
		out.SecretsRoot = pulumi.String(grant.Root).ToStringOutput()
	}
	presetNames := pulumi.StringArray{}
	for _, st := range a.stores {
		presetNames = append(presetNames, pulumi.String(st.Preset))
	}
	out.Presets = presetNames.ToStringArrayOutput()
	out.DeploymentYAML = pulumi.String(a.deploymentYAML).ToStringOutput()
	out.AlarmTopicArn = pick(topic != nil, func() pulumi.StringOutput { return topic.Arn })
	out.ScheduleArn = pick(notary, func() pulumi.StringOutput { return schedule.Arn })
	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{
		"bucketNames": out.BucketNames, "bucketArns": out.BucketArns,
		"archiveKeyArn": out.ArchiveKeyArn, "sealKeyArn": out.SealKeyArn, "sealKeyAlias": out.SealKeyAlias,
		"queueUrl": out.QueueURL, "queueArn": out.QueueArn, "dlqUrl": out.DlqURL, "dlqArn": out.DlqArn,
		"dedupeTableName":   out.DedupeTableName,
		"writerFunctionArn": out.WriterFunctionArn, "notaryFunctionArn": out.NotaryFunctionArn,
		"writerLiveAliasArn": out.WriterLiveAliasArn, "notaryLiveAliasArn": out.NotaryLiveAliasArn,
		"writerLiveVersion": out.WriterLiveVersion, "notaryLiveVersion": out.NotaryLiveVersion,
		"writerCodeSha256Matches": out.WriterCodeSha256Matches, "notaryCodeSha256Matches": out.NotaryCodeSha256Matches,
		"writerRoleArn": out.WriterRoleArn, "notaryRoleArn": out.NotaryRoleArn, "observeReaderRoleArn": out.ObserveReaderRoleArn,
		"archiveWriterRoleArn": out.ArchiveWriterRoleArn, "queryRoleArn": out.QueryRoleArn,
		"secretsRoot": out.SecretsRoot, "archiveCredentialsPaths": out.ArchiveCredentialsPaths,
		"presets": out.Presets, "deploymentYaml": out.DeploymentYAML, "alarmTopicArn": out.AlarmTopicArn, "scheduleArn": out.ScheduleArn,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func newRole(ctx *pulumi.Context, name, path, assume string, tags pulumi.StringMap, opts ...pulumi.ResourceOption) (*iam.Role, error) {
	return iam.NewRole(ctx, name, &iam.RoleArgs{
		Name: pulumi.String(name), Path: pulumi.String(path), AssumeRolePolicy: pulumi.String(assume), Tags: tags,
	}, opts...)
}

// bucketRef is an AWS preset's bucket as the roles are granted it. The ARN is
// the created bucket's, or, for an existing bucket, built from its name.
type bucketRef struct {
	Arn    pulumi.StringInput
	Prefix string
	Locked bool
}

// bucketARN is the ARN of an existing bucket.
func bucketARN(bucket string) string { return "arn:" + "aws:s3:::" + bucket }

// applyPolicy builds a policy from the buckets' ARNs, which exist only once the
// buckets do, and the other outputs it names.
func applyPolicy(refs []bucketRef, others []any, build func([]bucketGrant, []any) string) pulumi.StringOutput {
	all := make([]any, 0, len(refs)+len(others))
	for _, r := range refs {
		all = append(all, r.Arn)
	}
	all = append(all, others...)
	return pulumi.All(all...).ApplyT(func(v []any) string {
		b := make([]bucketGrant, len(refs))
		for i, r := range refs {
			b[i] = bucketGrant{Arn: v[i].(string), Prefix: r.Prefix, Locked: r.Locked}
		}
		return build(b, v[len(refs):])
	}).(pulumi.StringOutput)
}

// resourceName is the name of a preset's bucket resource and its parts.
func archiveResource(name string, st presetStore) string {
	return name + "-archive-" + string(st.Preset)
}

// newArchive is a preset's bucket: versioned in every case, with Object Lock for
// the attested preset (in the mode ArchiveArgs says), encrypted under the
// preset's key or the archive key, closed to the public and to plain HTTP, with
// the lifecycle of ADR 0065 written per profile prefix.
//
// The bucket's own `objectLockEnabled` is never set: it is ForceNew, so a bucket
// created with it off could only get the lock by being replaced. Object Lock is
// a separate resource instead, which S3 accepts on an existing versioned bucket.
func newArchive(ctx *pulumi.Context, name string, a *Args, st presetStore, keyArn pulumi.StringOutput, tags pulumi.StringMap,
	opts ...pulumi.ResourceOption) (*s3.Bucket, error) {
	ar := a.Archive
	res := archiveResource(name, st)
	// The bucket is protected from a stack's own destroy: Pulumi refuses to delete
	// it until the protection is lifted by hand, which is a decision and not an
	// accident. It is the only thing between a COMPLIANCE bucket and a destroy
	// that S3 would refuse later anyway.
	bopts := append([]pulumi.ResourceOption{pulumi.Protect(true)}, opts...)
	bargs := &s3.BucketArgs{
		Bucket: pulumi.String(st.Bucket),
		// A bucket with objects in it cannot be emptied by a destroy; never offer to.
		ForceDestroy: pulumi.Bool(false),
		Tags:         tags,
	}
	if st.Adopt {
		// An existing bucket: imported by its name, with only the name as input (its
		// tags are the estate's), and left in place by a destroy of the stack. Each
		// setting below is imported by the same name.
		bargs = &s3.BucketArgs{Bucket: pulumi.String(st.Bucket)}
		bopts = append(bopts, pulumi.Import(pulumi.ID(st.Bucket)), pulumi.RetainOnDelete(true))
		opts = append([]pulumi.ResourceOption{pulumi.Import(pulumi.ID(st.Bucket)), pulumi.RetainOnDelete(true)}, opts...)
	}
	bucket, err := s3.NewBucket(ctx, res, bargs, bopts...)
	if err != nil {
		return nil, err
	}
	versioning, err := s3.NewBucketVersioning(ctx, res, &s3.BucketVersioningArgs{
		Bucket:                  bucket.ID(),
		VersioningConfiguration: &s3.BucketVersioningVersioningConfigurationArgs{Status: pulumi.String("Enabled")},
	}, opts...)
	if err != nil {
		return nil, err
	}
	if st.Locked && !st.Adopt {
		lock := &s3.BucketObjectLockConfigurationArgs{
			Bucket: bucket.ID(), ObjectLockEnabled: pulumi.String("Enabled"),
		}
		if ar.DefaultRetentionDays > 0 {
			lock.Rule = &s3.BucketObjectLockConfigurationRuleArgs{
				DefaultRetention: &s3.BucketObjectLockConfigurationRuleDefaultRetentionArgs{
					Mode: pulumi.String(ar.ObjectLockMode), Days: pulumi.Int(ar.DefaultRetentionDays),
				},
			}
		}
		if _, err := s3.NewBucketObjectLockConfiguration(ctx, res, lock,
			append([]pulumi.ResourceOption{pulumi.DependsOn([]pulumi.Resource{versioning})}, opts...)...); err != nil {
			return nil, err
		}
	}
	// SSE-KMS under the preset's key or the archive key (Keys.Archive, or Archive.KeyArn), SSE-KMS under
	// the AWS-managed key aws/s3 (no KmsMasterKeyId: S3 uses it), or SSE-S3
	// (Archive.Encryption "s3"): then there is no key, and a bucket key has
	// nothing to amortise.
	sse := &s3.BucketServerSideEncryptionConfigurationRuleArgs{
		ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationRuleApplyServerSideEncryptionByDefaultArgs{
			SseAlgorithm: pulumi.String("AES256"),
		},
	}
	if ar.Encryption != EncryptionS3 {
		def := &s3.BucketServerSideEncryptionConfigurationRuleApplyServerSideEncryptionByDefaultArgs{
			SseAlgorithm: pulumi.String("aws:kms"),
		}
		if ar.Encryption == EncryptionKMS {
			def.KmsMasterKeyId = keyArn
		}
		sse = &s3.BucketServerSideEncryptionConfigurationRuleArgs{
			ApplyServerSideEncryptionByDefault: def,
			// One data key per bucket and period instead of one KMS call per
			// object: the writer puts an object per batch.
			BucketKeyEnabled: pulumi.Bool(true),
		}
	}
	if _, err := s3.NewBucketServerSideEncryptionConfiguration(ctx, res, &s3.BucketServerSideEncryptionConfigurationArgs{
		Bucket: bucket.ID(),
		Rules:  s3.BucketServerSideEncryptionConfigurationRuleArray{sse},
	}, opts...); err != nil {
		return nil, err
	}
	if _, err := s3.NewBucketPublicAccessBlock(ctx, res, &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}, opts...); err != nil {
		return nil, err
	}
	if _, err := s3.NewBucketOwnershipControls(ctx, res, &s3.BucketOwnershipControlsArgs{
		Bucket: bucket.ID(),
		Rule:   &s3.BucketOwnershipControlsRuleArgs{ObjectOwnership: pulumi.String("BucketOwnerEnforced")},
	}, opts...); err != nil {
		return nil, err
	}
	if _, err := s3.NewBucketPolicy(ctx, res, &s3.BucketPolicyArgs{
		Bucket: bucket.ID(),
		Policy: bucket.Arn.ApplyT(func(arn string) string {
			return policyJSON(statement{
				"Sid": "OnlyOverTLS", "Effect": "Deny", "Principal": "*", "Action": "s3:*",
				"Resource":  []string{arn, arn + "/*"},
				"Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "false"}},
			})
		}).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, err
	}

	// An adopted bucket's lifecycle is the estate's: no lifecycle resource is
	// declared, so the rules it has stay (and "never expire" is having none).
	if st.Adopt {
		return bucket, nil
	}

	// ADR 0065: Glacier Instant Retrieval after GlacierIRDays, which observe's
	// reindex and `audit verify` read without a restore; Deep Archive after
	// DeepArchiveDays, for what nobody expects to read before its retention ends.
	// A rule per profile of the preset, because the profile is the first component
	// of the key (below the preset's prefix) so that lifecycle can differ by it. Objects below the storage class's
	// minimum billable size stay where they are (the S3 default).
	rules := s3.BucketLifecycleConfigurationRuleArray{}
	for _, lp := range st.Profiles {
		p := lp.Name
		rule := &s3.BucketLifecycleConfigurationRuleArgs{
			Id:     pulumi.String("records-" + p),
			Status: pulumi.String("Enabled"),
			Filter: &s3.BucketLifecycleConfigurationRuleFilterArgs{Prefix: pulumi.String(st.key("records/" + p + "/"))},
		}
		// A transition must come before the expiration the profile's retention
		// sets; one that would not is left out.
		days := lp.RetentionDays
		transitions := s3.BucketLifecycleConfigurationRuleTransitionArray{}
		for _, t := range []struct {
			after int
			class string
		}{{ar.GlacierIRDays, "GLACIER_IR"}, {ar.DeepArchiveDays, "DEEP_ARCHIVE"}} {
			if days == 0 || t.after < days {
				transitions = append(transitions, &s3.BucketLifecycleConfigurationRuleTransitionArgs{
					Days: pulumi.Int(t.after), StorageClass: pulumi.String(t.class),
				})
			}
		}
		rule.Transitions = transitions
		if days > 0 {
			rule.Expiration = &s3.BucketLifecycleConfigurationRuleExpirationArgs{Days: pulumi.Int(days)}
			rule.NoncurrentVersionExpiration = &s3.BucketLifecycleConfigurationRuleNoncurrentVersionExpirationArgs{
				NoncurrentDays: pulumi.Int(days),
			}
		}
		rules = append(rules, rule)
	}
	rules = append(rules, &s3.BucketLifecycleConfigurationRuleArgs{
		Id:     pulumi.String("abort-incomplete-multipart-uploads"),
		Status: pulumi.String("Enabled"),
		Filter: &s3.BucketLifecycleConfigurationRuleFilterArgs{Prefix: pulumi.String(st.Prefix)},
		AbortIncompleteMultipartUpload: &s3.BucketLifecycleConfigurationRuleAbortIncompleteMultipartUploadArgs{
			DaysAfterInitiation: pulumi.Int(7),
		},
	})
	if _, err := s3.NewBucketLifecycleConfiguration(ctx, res, &s3.BucketLifecycleConfigurationArgs{
		Bucket: bucket.ID(), Rules: rules,
	}, append([]pulumi.ResourceOption{pulumi.DependsOn([]pulumi.Resource{versioning})}, opts...)...); err != nil {
		return nil, err
	}
	return bucket, nil
}

// newQueues is the ingest queue and the dead-letter queue its messages move to
// after MaxReceiveCount deliveries. Both are encrypted with SQS-managed keys: a
// queue holds records for as long as the writer is behind, and a customer key
// would put the senders' key permissions into the design for no gain here.
func newQueues(ctx *pulumi.Context, name string, a *Args, tags pulumi.StringMap, opts ...pulumi.ResourceOption) (*sqs.Queue, *sqs.Queue, error) {
	retention := a.Ingest.RetentionDays * 24 * 3600
	dlq, err := sqs.NewQueue(ctx, name+"-ingest-dlq", &sqs.QueueArgs{
		Name:                    pulumi.String(name + "-ingest-dlq"),
		MessageRetentionSeconds: pulumi.Int(14 * 24 * 3600),
		SqsManagedSseEnabled:    pulumi.Bool(true),
		Tags:                    tags,
	}, opts...)
	if err != nil {
		return nil, nil, err
	}
	queue, err := sqs.NewQueue(ctx, name+"-ingest", &sqs.QueueArgs{
		Name:                    pulumi.String(name + "-ingest"),
		MessageRetentionSeconds: pulumi.Int(retention),
		// Six times the function's timeout, which is what Lambda asks of an event
		// source queue: a message must not become visible again while its
		// invocation is still running.
		VisibilityTimeoutSeconds: pulumi.Int(6 * a.Writer.TimeoutSeconds),
		SqsManagedSseEnabled:     pulumi.Bool(true),
		RedrivePolicy: dlq.Arn.ApplyT(func(arn string) (string, error) {
			return fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":%d}`, arn, a.Ingest.MaxReceiveCount), nil
		}).(pulumi.StringOutput),
		Tags: tags,
	}, opts...)
	if err != nil {
		return nil, nil, err
	}
	if _, err := sqs.NewRedriveAllowPolicy(ctx, name+"-ingest-dlq", &sqs.RedriveAllowPolicyArgs{
		QueueUrl: dlq.Url,
		RedriveAllowPolicy: queue.Arn.ApplyT(func(arn string) string {
			return fmt.Sprintf(`{"redrivePermission":"byQueue","sourceQueueArns":[%q]}`, arn)
		}).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, nil, err
	}
	// Who may send. Always TLS only; and the senders named, and nobody else:
	// the queue carries no verified identity of its caller, so this list is what
	// the trail's authenticity on this path rests on. The deny is what makes it
	// the whole list: without it an identity policy anywhere in the account
	// would be enough to send. aws:PrincipalArn is the role's ARN for an assumed
	// role, the user's for a user.
	senders := pulumi.StringArray{}
	for _, s := range a.Ingest.Senders {
		senders = append(senders, s)
	}
	for _, s := range a.Ingest.Redrivers {
		senders = append(senders, s)
	}
	if _, err := sqs.NewQueuePolicy(ctx, name+"-ingest", &sqs.QueuePolicyArgs{
		QueueUrl: queue.Url,
		Policy: pulumi.All(queue.Arn, senders).ApplyT(func(v []any) (string, error) {
			arn, who := v[0].(string), v[1].([]string)
			if err := checkPrincipals(who); err != nil {
				return "", err
			}
			st := []statement{{
				"Sid": "OnlyOverTLS", "Effect": "Deny", "Principal": "*", "Action": "sqs:*", "Resource": arn,
				"Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "false"}},
			}}
			if len(who) > 0 {
				st = append(st, statement{
					"Sid": "Senders", "Effect": "Allow", "Principal": map[string]any{"AWS": who},
					"Action": []string{"sqs:SendMessage"}, "Resource": arn,
				}, statement{
					"Sid": "OnlyTheSenders", "Effect": "Deny", "Principal": "*",
					"Action": []string{"sqs:SendMessage"}, "Resource": arn,
					"Condition": map[string]any{"ArnNotEquals": map[string]any{"aws:PrincipalArn": who}},
				})
			}
			return policyJSON(st...), nil
		}).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, nil, err
	}
	return queue, dlq, nil
}

// functionSpec is what differs between the two functions.
type functionSpec struct {
	Name, Service  string
	Role           *iam.Role
	Package        *releasePackage
	MemoryMB       int
	TimeoutSeconds int
	// Config is what the configuration layer holds, by path under /opt/audit.
	Config map[string]string
}

// newFunction is one Lambda function: the release's zip, unchanged, on
// provided.al2023, on arm64, outside any VPC, with the configuration as an
// immutable layer version and the OTLP extension as a second layer when there is
// one.
//
// A zip and not an image: the binaries are static and a few MB and nothing here
// needs a registry. The code is the release's zip as it was published, checked
// against the digest the caller gave, so what runs is what the release's
// checksums name. The configuration is rendered from the stack's own arguments
// and published as a layer version of its own, so it needs no fetch at cold
// start, and the function and its configuration are two things with two
// versions, and a record of which ran together (the writer says so in its
// start-up record, from AUDIT_CONFIG_LAYER). Layers are at most five per
// function; this is one, and the extension is the other.
func newFunction(ctx *pulumi.Context, s functionSpec, a *Args, tags pulumi.StringMap,
	opts ...pulumi.ResourceOption) (*lambda.Function, *lambda.Alias, *cloudwatch.LogGroup, error) {
	logs, err := cloudwatch.NewLogGroup(ctx, s.Name, &cloudwatch.LogGroupArgs{
		Name:            pulumi.String("/aws/lambda/" + s.Name),
		RetentionInDays: pulumi.Int(a.LogRetentionDays),
		Tags:            tags,
	}, opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	layerArgs := &lambda.LayerVersionArgs{
		LayerName:               pulumi.String(s.Name + "-config"),
		Description:             pulumi.String("The configuration of " + s.Name + ", at /opt/" + layerRoot + "/. Immutable: a change is a new version."),
		CompatibleArchitectures: pulumi.StringArray{pulumi.String("arm64")},
		CompatibleRuntimes:      pulumi.StringArray{pulumi.String("provided.al2023")},
		// Not destroyed on replacement: an older function version, or a rollback,
		// points at the version it ran with, and the version is the evidence of
		// what that configuration was.
		SkipDestroy: pulumi.Bool(true),
	}
	var code *artifact.Object
	if a.Artifacts != nil {
		// The artifacts bucket: the package as it is, and the layer as a zip of
		// its own, byte for byte the same on every run so that its key is.
		var err error
		if code, err = uploadPackage(ctx, s.Name+"-code", a.Artifacts, s.Package, opts...); err != nil {
			return nil, nil, nil, err
		}
		files := map[string][]byte{}
		for path, body := range s.Config {
			files[layerRoot+"/"+path] = []byte(body)
		}
		layerZip, err := artifact.Zip(files)
		if err != nil {
			return nil, nil, nil, err
		}
		sum := artifact.Sum(layerZip)
		local, err := artifact.WriteTemp("audit-layer-", "config.zip", layerZip)
		if err != nil {
			return nil, nil, nil, err
		}
		obj, err := artifact.Upload(ctx, s.Name+"-config-code", *a.Artifacts,
			a.Artifacts.Key(s.Package.Version, sum, s.Name+"-config.zip"), local, sum, opts...)
		if err != nil {
			return nil, nil, nil, err
		}
		layerArgs.S3Bucket, layerArgs.S3Key, layerArgs.S3ObjectVersion = pulumi.String(obj.Bucket), pulumi.String(obj.Key), obj.VersionID
		layerArgs.SourceCodeHash = pulumi.String(obj.CodeSHA256)
	} else {
		layerFiles := map[string]any{}
		for path, body := range s.Config {
			layerFiles[layerRoot+"/"+path] = pulumi.NewStringAsset(body)
		}
		layerArgs.Code = pulumi.NewAssetArchive(layerFiles)
	}
	config, err := lambda.NewLayerVersion(ctx, s.Name+"-config", layerArgs, opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	// The configuration layer is last: layers extract in order and a later one
	// wins a path, so nothing after it can shadow /opt/audit/*.
	layers := pulumi.StringArray{}
	if a.Telemetry != nil {
		layers = append(layers, a.Telemetry.ExtensionLayerArn)
	}
	layers = append(layers, config.Arn)
	args := &lambda.FunctionArgs{
		Name:          pulumi.String(s.Name),
		Role:          s.Role.Arn,
		Runtime:       pulumi.String("provided.al2023"),
		Handler:       pulumi.String("bootstrap"),
		Architectures: pulumi.StringArray{pulumi.String("arm64")},
		// What Lambda reports for the code, so that the plan names the release's
		// bytes and a refresh finds nothing to change.
		SourceCodeHash: pulumi.String(s.Package.CodeSHA256),
		MemorySize:     pulumi.Int(s.MemoryMB),
		Timeout:        pulumi.Int(s.TimeoutSeconds),
		Layers:         layers,
		Environment:    &lambda.FunctionEnvironmentArgs{Variables: functionEnv(a.Telemetry, s.Service, config.Arn)},
		LoggingConfig: &lambda.FunctionLoggingConfigArgs{
			LogFormat: pulumi.String("Text"), LogGroup: logs.Name,
		},
		Tags: tags,
		// No VpcConfig: the functions run outside a VPC (a decision of the AWS design). They
		// reach S3, DynamoDB, SQS and KMS over the public regional endpoints with
		// the role's credentials, and the OTLP door over the internet.
	}
	if code != nil {
		args.S3Bucket, args.S3Key, args.S3ObjectVersion = pulumi.String(code.Bucket), pulumi.String(code.Key), code.VersionID
	} else {
		args.Code = pulumi.NewFileArchive(s.Package.Path)
	}
	args.Publish = pulumi.Bool(true) // a version on each change of the code or the configuration
	fn, err := lambda.NewFunction(ctx, s.Name, args, append([]pulumi.ResourceOption{pulumi.DependsOn([]pulumi.Resource{logs, config})}, opts...)...)
	if err != nil {
		return nil, nil, nil, err
	}
	// The alias is what the writer's event source and the notary's schedule use,
	// so a change moves them to the new version together. Canary rollouts through
	// CodeDeploy come later.
	live, err := lambda.NewAlias(ctx, s.Name+"-live", &lambda.AliasArgs{
		Name:            pulumi.String(LiveAlias),
		Description:     pulumi.String("The version callers invoke."),
		FunctionName:    fn.Name,
		FunctionVersion: fn.Version,
	}, opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	return fn, live, logs, nil
}

// LiveAlias is the alias of each function that its callers use.
const LiveAlias = "live"

// writerFiles is what the writer's configuration layer holds, by path under
// /opt/audit: its configuration, the profile document, and the catalogues with
// their data schemas.
func writerFiles(name string, a *Args) (map[string]string, error) {
	cfg, err := writerConfig(name, a)
	if err != nil {
		return nil, err
	}
	files := map[string]string{configFile: string(cfg), deploymentFile: a.deploymentYAML}
	catalogues, err := catalogueLayerFiles(&a.Writer)
	if err != nil {
		return nil, err
	}
	for p, body := range catalogues {
		files[p] = body
	}
	return files, nil
}

func notaryFiles(name string, a *Args) (map[string]string, error) {
	cfg, err := notaryConfig(name, a)
	if err != nil {
		return nil, err
	}
	return map[string]string{configFile: string(cfg), deploymentFile: a.deploymentYAML}, nil
}

// newSchedule invokes the notary on a schedule, with a role of its own that may
// invoke that one function. It does not retry: a run that failed is the Errors
// alarm's, and the next run seals whatever is missing, because a run is
// idempotent.
func newSchedule(ctx *pulumi.Context, name string, a *Args, fn *lambda.Function, live *lambda.Alias, tags pulumi.StringMap,
	opts ...pulumi.ResourceOption) (*scheduler.Schedule, error) {
	role, err := newRole(ctx, name+"-scheduler", a.RolePath, assumeRoleJSON("scheduler.amazonaws.com"), tags, opts...)
	if err != nil {
		return nil, err
	}
	if _, err := iam.NewRolePolicy(ctx, name+"-scheduler", &iam.RolePolicyArgs{
		Role:   role.Name,
		Policy: live.Arn.ApplyT(invokePolicy).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, err
	}
	if _, err := lambda.NewFunctionEventInvokeConfig(ctx, name+"-notary", &lambda.FunctionEventInvokeConfigArgs{
		FunctionName: fn.Name, Qualifier: live.Name, MaximumRetryAttempts: pulumi.Int(0),
	}, opts...); err != nil {
		return nil, err
	}
	return scheduler.NewSchedule(ctx, name+"-notary", &scheduler.ScheduleArgs{
		Name:                       pulumi.String(name + "-notary"),
		Description:                pulumi.String("Seals the closed hours of the archive."),
		ScheduleExpression:         pulumi.String(a.Notary.Schedule),
		ScheduleExpressionTimezone: pulumi.String("UTC"),
		FlexibleTimeWindow:         &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
		Target: &scheduler.ScheduleTargetArgs{
			Arn:     live.Arn,
			RoleArn: role.Arn,
			Input:   pulumi.String("{}"),
			RetryPolicy: &scheduler.ScheduleTargetRetryPolicyArgs{
				MaximumRetryAttempts: pulumi.Int(0), MaximumEventAgeInSeconds: pulumi.Int(3600),
			},
		},
	}, opts...)
}

// newObserveReader is the role audit-observe assumes to follow the archive: from
// another account (a principal), from a Kubernetes workload (IRSA or EKS Pod
// Identity), or any of them that are given.
func newObserveReader(ctx *pulumi.Context, name string, a *Args, refs []bucketRef, archiveKeyArns []string, tags pulumi.StringMap,
	opts ...pulumi.ResourceOption) (*iam.Role, error) {
	o := a.Observe
	principal := pulumi.String("").ToStringOutput()
	if o.TrustedPrincipalArn != nil {
		principal = o.TrustedPrincipalArn.ToStringOutput()
	}
	provider := pulumi.String("").ToStringOutput()
	if o.IRSA != nil {
		provider = o.IRSA.OIDCProviderArn.ToStringOutput()
	}
	cluster := pulumi.String("").ToStringOutput()
	if o.PodIdentity != nil {
		cluster = o.PodIdentity.ClusterArn.ToStringOutput()
	}
	role, err := iam.NewRole(ctx, name+"-observe-reader", &iam.RoleArgs{
		Name: pulumi.String(name + "-observe-reader"), Path: pulumi.String(a.RolePath), Tags: tags,
		PermissionsBoundary: boundary(o.PodIdentity),
		AssumeRolePolicy: pulumi.All(principal, provider, cluster).ApplyT(func(v []any) (string, error) {
			var extra []statement
			if o.PodIdentity != nil {
				s, err := podIdentityTrustStatement(v[2].(string), o.PodIdentity.Namespace, o.PodIdentity.ServiceAccount)
				if err != nil {
					return "", err
				}
				extra = append(extra, s)
			}
			return trustPolicy(v[0].(string), o.ExternalID, o.IRSA, v[1].(string), extra...), nil
		}).(pulumi.StringOutput),
	}, opts...)
	if err != nil {
		return nil, err
	}
	// A policy with no statement is invalid: with every preset at an endpoint the
	// role has nothing to be granted.
	if len(refs) > 0 {
		if _, err := iam.NewRolePolicy(ctx, name+"-observe-reader", &iam.RolePolicyArgs{
			Role: role.Name,
			Policy: applyPolicy(refs, nil, func(b []bucketGrant, _ []any) string {
				return observeReaderPolicy(b, archiveKeyArns)
			}),
		}, opts...); err != nil {
			return nil, err
		}
	}
	if o.PodIdentity != nil {
		if err := newPodIdentityAssociation(ctx, name+"-observe", o.PodIdentity, role, opts...); err != nil {
			return nil, err
		}
	}
	return role, nil
}

// newQuery is the role audit-query runs as on EKS, `<name>-query`: the observe
// reader's rights, plus the queue when RecordReads, bound to its ServiceAccount
// by a Pod Identity association.
func newQuery(ctx *pulumi.Context, name string, a *Args, refs []bucketRef, archiveKeyArns []string, queueArn pulumi.StringOutput,
	tags pulumi.StringMap, opts ...pulumi.ResourceOption) (*iam.Role, error) {
	q := a.Query
	role, err := iam.NewRole(ctx, name+"-query", &iam.RoleArgs{
		Name: pulumi.String(name + "-query"), Path: pulumi.String(a.RolePath), Tags: tags,
		PermissionsBoundary: boundary(&q.PodIdentity),
		AssumeRolePolicy: q.PodIdentity.ClusterArn.ToStringOutput().ApplyT(func(arn string) (string, error) {
			s, err := podIdentityTrustStatement(arn, q.PodIdentity.Namespace, q.PodIdentity.ServiceAccount)
			if err != nil {
				return "", err
			}
			return policyJSON(s), nil
		}).(pulumi.StringOutput),
	}, opts...)
	if err != nil {
		return nil, err
	}
	if len(refs) > 0 || q.RecordReads {
		if _, err := iam.NewRolePolicy(ctx, name+"-query", &iam.RolePolicyArgs{
			Role: role.Name,
			Policy: applyPolicy(refs, []any{queueArn}, func(b []bucketGrant, v []any) string {
				qa := ""
				if q.RecordReads {
					qa = v[0].(string)
				}
				return queryPolicy(b, archiveKeyArns, qa)
			}),
		}, opts...); err != nil {
			return nil, err
		}
	}
	if err := newPodIdentityAssociation(ctx, name+"-query", &q.PodIdentity, role, opts...); err != nil {
		return nil, err
	}
	return role, nil
}

func boundary(p *PodIdentityArgs) pulumi.StringPtrInput {
	if p == nil || p.PermissionsBoundaryArn == "" {
		return nil
	}
	return pulumi.String(p.PermissionsBoundaryArn)
}

// newPodIdentityAssociation binds the role to the ServiceAccount: `<prefix>-pia`.
// A ServiceAccount takes one association.
func newPodIdentityAssociation(ctx *pulumi.Context, prefix string, p *PodIdentityArgs, role *iam.Role, opts ...pulumi.ResourceOption) error {
	args := &eks.PodIdentityAssociationArgs{
		ClusterName: p.ClusterName, Namespace: pulumi.String(p.Namespace),
		ServiceAccount: pulumi.String(p.ServiceAccount), RoleArn: role.Arn,
	}
	if p.Region != "" {
		args.Region = pulumi.String(p.Region)
	}
	_, err := eks.NewPodIdentityAssociation(ctx, prefix+"-pia", args, opts...)
	return err
}

// newArchiveWriter is the role a workload outside AWS assumes (IRSA) to write the
// archive prefixes it is given: `<name>-archive-writer`.
func newArchiveWriter(ctx *pulumi.Context, name string, a *Args, refs []bucketRef, archiveKeyArns []string, tags pulumi.StringMap,
	opts ...pulumi.ResourceOption) (*iam.Role, error) {
	w := a.ArchiveWriter
	role, err := iam.NewRole(ctx, name+"-archive-writer", &iam.RoleArgs{
		Name: pulumi.String(name + "-archive-writer"), Path: pulumi.String(a.RolePath), Tags: tags,
		AssumeRolePolicy: w.IRSA.OIDCProviderArn.ToStringOutput().ApplyT(func(p string) string {
			return trustPolicy("", "", &w.IRSA, p)
		}).(pulumi.StringOutput),
	}, opts...)
	if err != nil {
		return nil, err
	}
	if len(refs) > 0 {
		if _, err := iam.NewRolePolicy(ctx, name+"-archive-writer", &iam.RolePolicyArgs{
			Role: role.Name,
			Policy: applyPolicy(refs, nil, func(b []bucketGrant, _ []any) string {
				return archiveWriterPolicy(b, archiveKeyArns, w.Prefixes)
			}),
		}, opts...); err != nil {
			return nil, err
		}
	}
	return role, nil
}

// uploadPackage puts the verified release zip in the artifacts bucket, as it is.
func uploadPackage(ctx *pulumi.Context, resName string, art *ArtifactsArgs, p *releasePackage, opts ...pulumi.ResourceOption) (*artifact.Object, error) {
	return artifact.Upload(ctx, resName, *art, art.Key(p.Version, p.SHA256, p.Name), p.Path, p.SHA256, opts...)
}
