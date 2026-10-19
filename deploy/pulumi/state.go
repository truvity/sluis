package sluispulumi

import (
	"errors"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/dynamodb"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// StateGrant is what a policy needs to name the tables: their ARNs and, when the
// tables are encrypted with a customer-managed key, the key's.
type StateGrant struct {
	// Tables is the ARN of each module's table (States.Grant()). A role is
	// granted the tables of the modules it hosts, never one table for all.
	Tables map[Module]pulumi.StringInput
	// KeyArn is nil for the AWS-owned key.
	KeyArn pulumi.StringInput
}

// newTable declares one module's state table.
//
// The key schema is the DynamoDB adapter's (internal/port/dynamodb): a string
// partition key `pk` (the first segment of the key) and a string sort key `sk`
// (the whole key). The other attributes (`v`, `rev`, `k`) are not key
// attributes and DynamoDB takes them schemaless, so they are not declared.
// `expires` is the TTL attribute, in epoch seconds; the adapter judges expiry
// itself on every read and DynamoDB's sweep is only housekeeping. Billing is
// on-demand, point-in-time recovery is on, the table is protected and carries
// DynamoDB's own deletion protection.
func newTable(ctx *pulumi.Context, resName, tableName string, keyArn pulumi.StringInput,
	tags map[string]string, opts ...pulumi.ResourceOption) (*dynamodb.Table, error) {
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
	return dynamodb.NewTable(ctx, resName, targs, append(opts, pulumi.Protect(true))...)
}

// StatesType is the Pulumi type token of the States component.
const StatesType = "sluis:aws:States"

// StatesArgs is the DynamoDB tables of the State, Index and Trigger ports
// (internal/port/dynamodb): one per module.
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
}

// States is the component. Its fields are the outputs.
type States struct {
	pulumi.ResourceState

	// Tables and TableArns are each module's table.
	Tables    map[Module]pulumi.StringOutput
	TableArns map[Module]pulumi.StringOutput

	keyArn pulumi.StringInput
}

// Grant is the tables as LambdaArgs.State, BackupArgs.State and
// KubernetesIdentityArgs.State take them.
func (s *States) Grant() *StateGrant {
	g := &StateGrant{KeyArn: s.keyArn, Tables: map[Module]pulumi.StringInput{}}
	for m, arn := range s.TableArns {
		g.Tables[m] = arn
	}
	return g
}

// NewStates creates a table for each module (on-demand, point-in-time recovery,
// TTL on `expires`, deletion protection, protected; see newTable). The names
// are the ones `ports.dynamodb.tables` carries (PortsArgs.Tables).
func NewStates(ctx *pulumi.Context, name string, args *StatesArgs, opts ...pulumi.ResourceOption) (*States, error) {
	if args == nil {
		return nil, errors.New("sluispulumi: StatesArgs is nil")
	}
	if err := args.Modules.validate(); err != nil {
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
	out := &States{keyArn: args.KeyArn, Tables: map[Module]pulumi.StringOutput{}, TableArns: map[Module]pulumi.StringOutput{}}
	if err := ctx.RegisterComponentResource(StatesType, name, out, opts...); err != nil {
		return nil, err
	}
	outputs := pulumi.Map{}
	for _, m := range sortedModules(mods) {
		t, err := newTable(ctx, name+"-"+string(m)+"-table", args.Modules.TableName(m), args.KeyArn, args.Tags, pulumi.Parent(out))
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
