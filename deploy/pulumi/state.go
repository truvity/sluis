package sluispulumi

import (
	"errors"
	"fmt"
	"time"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/dynamodb"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// StateType is the Pulumi type token of the State component.
const StateType = "sluis:aws:State"

// StateArgs is the DynamoDB table of the DynamoDB adapter of the State, Index
// and Trigger ports (internal/port/dynamodb).
type StateArgs struct {
	// TableName is the table's name. Required: it is in the processes'
	// configuration (`ports.dynamodb.table`).
	TableName string

	// KeyArn is a customer-managed KMS key to encrypt the table with. Default:
	// none, and the table is encrypted with the AWS-owned key, which costs
	// nothing and needs no grant. With a key, the roles are granted its use
	// through DynamoDB only.
	KeyArn pulumi.StringInput

	// Tags are put on the table. Default none.
	Tags map[string]string
}

// State is the component. Its fields are the outputs.
type State struct {
	pulumi.ResourceState

	// TableName and TableArn are the table.
	TableName pulumi.StringOutput
	TableArn  pulumi.StringOutput

	keyArn pulumi.StringInput
}

// StateGrant is what a policy needs to name the table: its ARN and, when the
// table is encrypted with a customer-managed key, the key's.
type StateGrant struct {
	// TableArn is the one table of layout v4: the legacy table. Nil when the
	// installation has per-module tables only (States without Legacy).
	TableArn pulumi.StringInput
	// Tables is the ARN of each module's table (States.Grant()). A role is
	// granted the tables of the modules it hosts, never one table for all.
	Tables map[Module]pulumi.StringInput
	// KeyArn is nil for the AWS-owned key.
	KeyArn pulumi.StringInput
}

// Grant is the table as KubernetesIdentityArgs.State takes it.
func (s *State) Grant() *StateGrant {
	return &StateGrant{TableArn: s.TableArn, KeyArn: s.keyArn}
}

// NewState creates the table.
//
// The key schema is the adapter's: a string partition key `pk` (the first
// segment of the key) and a string sort key `sk` (the whole key). The other
// attributes (`v`, `rev`, `k`) are not key attributes and DynamoDB takes them
// schemaless, so they are not declared. `expires` is the TTL attribute, in epoch
// seconds; the adapter judges expiry itself on every read and DynamoDB's sweep
// is only housekeeping. Billing is on-demand, point-in-time recovery is on, the
// table is protected and carries DynamoDB's own deletion protection.
func NewState(ctx *pulumi.Context, name string, args *StateArgs, opts ...pulumi.ResourceOption) (*State, error) {
	if args == nil {
		return nil, errors.New("sluispulumi: StateArgs is nil")
	}
	if args.TableName == "" {
		return nil, errors.New("sluispulumi: StateArgs.TableName is required")
	}
	return newState(ctx, name, args, nil, opts...)
}

// retiring is the legacy table on its way out: the apply that backs it up
// unprotects it. unprotect resolves once the backup exists, so the table's update
// waits for it.
type retiring struct {
	unprotect pulumi.BoolInput
}

func newState(ctx *pulumi.Context, name string, args *StateArgs, retire *retiring, opts ...pulumi.ResourceOption) (*State, error) {
	out := &State{keyArn: args.KeyArn}
	if err := ctx.RegisterComponentResource(StateType, name, out, opts...); err != nil {
		return nil, err
	}

	table, err := newTable(ctx, name+"-table", args.TableName, args.KeyArn, args.Tags, retire, pulumi.Parent(out))
	if err != nil {
		return nil, fmt.Errorf("sluis state table: %w", err)
	}
	out.TableName = table.Name
	out.TableArn = table.Arn
	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{
		"tableName": out.TableName,
		"tableArn":  out.TableArn,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// newTable declares one state table: the same shape for the legacy table and
// every module's.
func newTable(ctx *pulumi.Context, resName, tableName string, keyArn pulumi.StringInput,
	tags map[string]string, retire *retiring, opts ...pulumi.ResourceOption) (*dynamodb.Table, error) {
	targs := &dynamodb.TableArgs{
		Name:        pulumi.String(tableName),
		BillingMode: pulumi.String("PAY_PER_REQUEST"),
		HashKey:     pulumi.String("pk"),
		RangeKey:    pulumi.String("sk"),
		Attributes: dynamodb.TableAttributeArray{
			&dynamodb.TableAttributeArgs{Name: pulumi.String("pk"), Type: pulumi.String("S")},
			&dynamodb.TableAttributeArgs{Name: pulumi.String("sk"), Type: pulumi.String("S")},
		},
		Ttl: &dynamodb.TableTtlArgs{
			AttributeName: pulumi.String("expires"),
			Enabled:       pulumi.Bool(true),
		},
		PointInTimeRecovery:       &dynamodb.TablePointInTimeRecoveryArgs{Enabled: pulumi.Bool(true)},
		DeletionProtectionEnabled: pulumi.Bool(true),
		Tags:                      tagMap(tags),
	}
	if keyArn != nil {
		targs.ServerSideEncryption = &dynamodb.TableServerSideEncryptionArgs{
			Enabled:   pulumi.Bool(true),
			KmsKeyArn: keyArn,
		}
	}
	if retire != nil {
		// Deletion protection off, and the resource unprotected in the state, so
		// that removing it from the program later is a delete the engine allows.
		targs.DeletionProtectionEnabled = retire.unprotect
		return dynamodb.NewTable(ctx, resName, targs, append(opts, pulumi.Protect(false))...)
	}
	return dynamodb.NewTable(ctx, resName, targs, append(opts, pulumi.Protect(true))...)
}

// StatesType is the Pulumi type token of the States component.
const StatesType = "sluis:aws:States"

// LegacyStateArgs is the one table of layout v4, adopted.
type LegacyStateArgs struct {
	// Name is the name NewState was called with. It is kept so the table's URN
	// does not change: adoption is the same resource, not a new one.
	Name string
	// StateArgs are the table's arguments, as they were.
	StateArgs
}

// StatesArgs is the tables of layout v5: one per module.
type StatesArgs struct {
	// Modules names the installation's instance and, optionally, a table name
	// per module. The default name is `sluis-<instance>-<module>`.
	Modules ModuleSet
	// Only limits the modules that get a table. Default: every module.
	Only []Module
	// KeyArn is a customer-managed KMS key for every table. Default: none.
	KeyArn pulumi.StringInput
	// Tags are put on every table.
	Tags map[string]string

	// Legacy is the single table of layout v4, kept while the installation
	// migrates. It is declared exactly as NewState would (same name, same
	// arguments), so it stays one resource, protected, with DynamoDB's deletion
	// protection. Leaving it out of the arguments does not delete it: the
	// resource is protected, so the engine refuses the update. Retire it with
	// Compat.RetireLegacyTable, in two applies (see there). Nil: an installation
	// born on v5, or one whose table is gone.
	Legacy *LegacyStateArgs
	// Compat is the same CompatArgs the function takes. With RetireLegacyTable
	// and Legacy set, this apply exports the legacy table to a
	// bucket the library creates (kept 30 days), then turns off the table's
	// deletion protection and unprotects it in the state; the stack output
	// `legacyTableBackupArn` is the export's ARN. The apply after, Legacy is left
	// out and the table is deleted. The engine cannot unprotect and delete one
	// resource in one update, so these are two applies; an update that drops
	// Legacy first is refused by the engine as a protected delete.
	Compat *CompatArgs
}

// States is the component. Its fields are the outputs.
type States struct {
	pulumi.ResourceState

	// Tables and TableArns are each module's table.
	Tables    map[Module]pulumi.StringOutput
	TableArns map[Module]pulumi.StringOutput
	// Legacy is the adopted layout v4 table; nil without one.
	Legacy *State
	// LegacyBackupArn is the ARN of the export of the legacy table taken by the
	// apply that retires it; empty in any other.
	LegacyBackupArn pulumi.StringOutput

	keyArn pulumi.StringInput
}

// Grant is the tables as LambdaArgs.State and KubernetesIdentityArgs.State take
// them. TableArn is the legacy table's while there is one, which is what layout
// v4 keeps running on.
func (s *States) Grant() *StateGrant {
	g := &StateGrant{KeyArn: s.keyArn, Tables: map[Module]pulumi.StringInput{}}
	for m, arn := range s.TableArns {
		g.Tables[m] = arn
	}
	if s.Legacy != nil {
		g.TableArn = s.Legacy.TableArn
	}
	return g
}

// NewStates creates a table for each module (on-demand, point-in-time recovery,
// TTL on `expires`, deletion protection, protected) and adopts the legacy table.
// The names are the ones `ports.dynamodb.tables` carries (PortsArgs.Tables).
// Nothing in an existing stack is replaced or deleted: the legacy table is
// unchanged and the module tables are new.
func NewStates(ctx *pulumi.Context, name string, args *StatesArgs, opts ...pulumi.ResourceOption) (*States, error) {
	if args == nil {
		return nil, errors.New("sluispulumi: StatesArgs is nil")
	}
	if err := args.Modules.validate(); err != nil {
		return nil, err
	}
	if err := args.Compat.validate(); err != nil {
		return nil, err
	}
	mods := args.Only
	if len(mods) == 0 {
		mods = Modules()
	}
	names := map[string]Module{}
	for _, m := range mods {
		if !validModule(m) {
			return nil, fmt.Errorf("sluispulumi: StatesArgs.Only names unknown module %q", m)
		}
		n := args.Modules.TableName(m)
		if other, dup := names[n]; dup {
			return nil, fmt.Errorf("sluispulumi: modules %q and %q have the same table name %q", other, m, n)
		}
		names[n] = m
	}
	if l := args.Legacy; l != nil {
		if l.Name == "" || l.TableName == "" {
			return nil, errors.New("sluispulumi: StatesArgs.Legacy needs the Name NewState was called with and its TableName")
		}
		if m, clash := names[l.TableName]; clash {
			return nil, fmt.Errorf("sluispulumi: the legacy table %q is also the table of module %q", l.TableName, m)
		}
	}
	out := &States{keyArn: args.KeyArn, Tables: map[Module]pulumi.StringOutput{}, TableArns: map[Module]pulumi.StringOutput{}}
	if err := ctx.RegisterComponentResource(StatesType, name, out, opts...); err != nil {
		return nil, err
	}
	// The legacy table is not parented: its URN is what it was.
	if l := args.Legacy; l != nil {
		var retire *retiring
		if args.Compat != nil && args.Compat.RetireLegacyTable {
			bk, err := newLegacyBackup(ctx, name, l, args.Tags, pulumi.Parent(out))
			if err != nil {
				return nil, fmt.Errorf("sluis legacy table backup: %w", err)
			}
			out.LegacyBackupArn = bk.Arn
			// The table is unprotected once the export exists, and not before.
			retire = &retiring{unprotect: bk.Arn.ApplyT(func(string) bool { return false }).(pulumi.BoolOutput)}
			ctx.Export(LegacyBackupArnOutput, bk.Arn)
		}
		legacy, err := newState(ctx, l.Name, &l.StateArgs, retire, opts...)
		if err != nil {
			return nil, err
		}
		out.Legacy = legacy
	}
	outputs := pulumi.Map{}
	for _, m := range sortedModules(mods) {
		t, err := newTable(ctx, name+"-"+string(m)+"-table", args.Modules.TableName(m), args.KeyArn, args.Tags, nil, pulumi.Parent(out))
		if err != nil {
			return nil, fmt.Errorf("sluis state table of %s: %w", m, err)
		}
		out.Tables[m], out.TableArns[m] = t.Name, t.Arn
		outputs["table-"+string(m)] = t.Name
	}
	if err := ctx.RegisterResourceOutputs(out, outputs); err != nil {
		return nil, err
	}
	return out, nil
}

// LegacyBackupArnOutput is the stack output that carries the ARN of the legacy
// table's export (Compat.RetireLegacyTable).
const LegacyBackupArnOutput = "legacyTableBackupArn"

// LegacyBackupRetentionDays is how long the export of the legacy table is kept: the
// bucket's lifecycle expires it after that, and nothing has to be deleted by hand.
const LegacyBackupRetentionDays = 30

// legacyBackupPrefix is the key prefix of the export, `pre-v5-retire-<table>-<date>/`.
// The date is the day the library first ran the retirement: the prefix is ignored
// on later applies, so a new day does not take a second export.
func legacyBackupPrefix(table string, now time.Time) string {
	return "pre-v5-retire-" + table + "-" + now.UTC().Format("2006-01-02") + "/"
}

// newLegacyBackup exports the legacy table to a bucket of its own, and expires
// the export after LegacyBackupRetentionDays. DynamoDB has no on-demand backup
// resource in the provider; the export is its point-in-time recovery written to
// S3 (the table keeps PITR on), complete when the resource returns, and it can be
// imported into a new table with `aws dynamodb import-table`. The bucket is
// private, encrypted, and destroyed with the stack (ForceDestroy).
func newLegacyBackup(ctx *pulumi.Context, name string, l *LegacyStateArgs, tags map[string]string,
	opts ...pulumi.ResourceOption) (*dynamodb.TableExport, error) {
	bucket, err := s3.NewBucket(ctx, name+"-legacy-backup", &s3.BucketArgs{
		ForceDestroy: pulumi.Bool(true),
		Tags:         tagMap(tags),
	}, opts...)
	if err != nil {
		return nil, err
	}
	enc, err := s3.NewBucketServerSideEncryptionConfigurationV2(ctx, name+"-legacy-backup-encryption",
		&s3.BucketServerSideEncryptionConfigurationV2Args{
			Bucket: bucket.ID(),
			Rules: s3.BucketServerSideEncryptionConfigurationV2RuleArray{
				&s3.BucketServerSideEncryptionConfigurationV2RuleArgs{
					ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationV2RuleApplyServerSideEncryptionByDefaultArgs{
						SseAlgorithm: pulumi.String("AES256"),
					},
				},
			},
		}, opts...)
	if err != nil {
		return nil, err
	}
	block, err := s3.NewBucketPublicAccessBlock(ctx, name+"-legacy-backup-public-access", &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}, opts...)
	if err != nil {
		return nil, err
	}
	life, err := s3.NewBucketLifecycleConfiguration(ctx, name+"-legacy-backup-lifecycle", &s3.BucketLifecycleConfigurationArgs{
		Bucket: bucket.ID(),
		Rules: s3.BucketLifecycleConfigurationRuleArray{
			&s3.BucketLifecycleConfigurationRuleArgs{
				Id: pulumi.String("expire-the-export"), Status: pulumi.String("Enabled"),
				Filter:     &s3.BucketLifecycleConfigurationRuleFilterArgs{Prefix: pulumi.String("pre-v5-retire-")},
				Expiration: &s3.BucketLifecycleConfigurationRuleExpirationArgs{Days: pulumi.Int(LegacyBackupRetentionDays)},
				AbortIncompleteMultipartUpload: &s3.BucketLifecycleConfigurationRuleAbortIncompleteMultipartUploadArgs{
					DaysAfterInitiation: pulumi.Int(7),
				},
			},
		},
	}, opts...)
	if err != nil {
		return nil, err
	}
	// The table is looked up, not read from the resource: the resource's update is
	// what waits for this export.
	// The export reads point-in-time recovery, so it is refused, naming the
	// setting, while the live table has none. The table is declared with it on
	// (newTable), so an earlier apply of this library has turned it on.
	tbl := dynamodb.LookupTableOutput(ctx, dynamodb.LookupTableOutputArgs{Name: pulumi.String(l.TableName)})
	arn := pulumi.All(tbl.Arn(), tbl.PointInTimeRecovery().Enabled()).ApplyT(func(v []any) (string, error) {
		if on, _ := v[1].(bool); !on {
			return "", fmt.Errorf("sluispulumi: Compat.RetireLegacyTable exports the legacy table %q from point-in-time recovery, "+
				"and the live table has it off: apply once without RetireLegacyTable (the table is declared with "+
				"pointInTimeRecovery enabled), wait until it is enabled, then retire", l.TableName)
		}
		return v[0].(string), nil
	}).(pulumi.StringOutput)
	return dynamodb.NewTableExport(ctx, name+"-legacy-backup-export", &dynamodb.TableExportArgs{
		TableArn:       arn,
		S3Bucket:       bucket.Bucket,
		S3Prefix:       pulumi.String(legacyBackupPrefix(l.TableName, time.Now())),
		S3SseAlgorithm: pulumi.String("AES256"),
		ExportFormat:   pulumi.String("DYNAMODB_JSON"),
	}, append(opts, pulumi.IgnoreChanges([]string{"s3Prefix"}),
		pulumi.DependsOn([]pulumi.Resource{enc, block, life}))...)
}

// tableArnOf is the legacy table's ARN, or an empty one.
func tableArnOf(g *StateGrant) pulumi.StringInput {
	if g == nil || g.TableArn == nil {
		return pulumi.String("")
	}
	return g.TableArn
}

// moduleTableArns is the grant's per-module ARNs as one output.
func moduleTableArns(g *StateGrant) pulumi.StringMapInput {
	m := pulumi.StringMap{}
	if g != nil {
		for k, v := range g.Tables {
			m[string(k)] = v
		}
	}
	return m
}

func moduleTablesOf(v any) map[Module]string {
	out := map[Module]string{}
	for k, arn := range v.(map[string]string) {
		out[Module(k)] = arn
	}
	return out
}
