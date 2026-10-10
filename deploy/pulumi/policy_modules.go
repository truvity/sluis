package sluispulumi

import "fmt"

// ModuleEnv is what a role's statements are rendered from.
type ModuleEnv struct {
	Region, Account string
	Modules         ModuleSet
	// BucketArn is the blob bucket; empty when blobs are external.
	BucketArn string
	// TableArns is each module's table ARN. A role needs the ARN of every table
	// it is granted.
	TableArns map[Module]string
	// TableKeyArn is the customer-managed key the tables are encrypted with, if any.
	TableKeyArn string
	// ParameterKeyArn is the key the SecureString parameters are encrypted with, if any.
	ParameterKeyArn string
	// QueueArn is the audit queue; empty when audit is off.
	QueueArn string
	// LogGroupArns is each role's log group, by role name.
	LogGroupArns map[string]string
	// FunctionArns is each role's function ARN, by role name.
	FunctionArns map[string]string
	// Roles is every role of the installation; it resolves which function hosts
	// a module for an invoke grant.
	Roles []Role
}

var (
	ddbReadActions  = []string{ddbGetItem, ddbQuery, ddbScan, ddbDescribeTable}
	ddbWriteActions = []string{ddbGetItem, ddbPutItem, ddbUpdateItem, ddbDeleteItem, ddbQuery, ddbScan, ddbDescribeTable}
	// ddbMaintenanceDenied are the writes a role other than restore is denied on
	// the maintenance partition. BatchWriteItem and TransactWriteItems are not
	// granted to any role; they are named so that a grant added later does not
	// open the flag.
	ddbMaintenanceDenied = []string{ddbPutItem, ddbUpdateItem, ddbDeleteItem, "dynamodb:BatchWriteItem", "dynamodb:TransactWriteItems"}
	ssmReadActions       = []string{ssmGetParameter, ssmGetParameters}
	ssmOwnActions        = []string{ssmGetParameter, ssmGetParameters, ssmGetParameterHistory, ssmPutParameter, ssmDeleteParameter}
)

// ModuleRoleStatements is the statement list of one function's role, per the
// pattern every role follows: its own modules' parameters, tables and blob
// prefixes, the key only through SSM and only for those parameters, the audit
// queue and its own log group, and then CrossGrants. The same input renders the
// same list.
//
// The maintenance flag is a record in each module's own table (partition
// [MaintenancePartition]), so a role reads it through the grant on its own
// table and needs no grant on another module's. Only the restore role writes
// it: every other role carries an explicit deny of the writes of that
// partition on its tables, which holds whatever else an Allow says.
func ModuleRoleStatements(env ModuleEnv, role Role) ([]map[string]any, error) {
	if err := env.Modules.validate(); err != nil {
		return nil, err
	}
	if role.Name == "" || len(role.Hosts) == 0 {
		return nil, fmt.Errorf("sluispulumi: a role needs a name and at least one module")
	}
	for _, m := range role.Hosts {
		if !validModule(m) {
			return nil, fmt.Errorf("sluispulumi: role %q hosts unknown module %q", role.Name, m)
		}
	}
	hosts := sortedModules(role.Hosts)
	var st []statement

	if lg := env.LogGroupArns[role.Name]; lg != "" {
		st = append(st, statement{
			"Sid": sidLogs, "Effect": "Allow",
			"Action": []string{logsCreateStream, logsPutEvents}, "Resource": lg + ":*",
		})
	}
	if env.QueueArn != "" {
		st = append(st, statement{"Sid": sidAudit, "Effect": "Allow", "Action": sqsSendMessage, "Resource": env.QueueArn})
	}

	// Parameters of the hosted modules, and the key for exactly those.
	var arns []string
	for _, m := range hosts {
		internal, external := env.Modules.InternalPrefix(m), env.Modules.ExternalPrefix(m)
		st = append(st, statement{
			"Sid": "SluisParameters" + capitalize(m), "Effect": "Allow", "Action": ssmOwnActions,
			"Resource": parameterArnsUnder(env.Region, env.Account, internal, external),
		})
		arns = append(arns, parameterArnsUnder(env.Region, env.Account, internal, external)...)
	}
	st = append(st, parameterKeyStatements(env.ParameterKeyArn,
		[]string{kmsEncrypt, kmsDecrypt, "kms:GenerateDataKey"}, arns)...)

	// Tables and blobs of the hosted modules.
	own := map[Module]bool{}
	var tables []string
	for _, m := range hosts {
		own[m] = true
		arn, err := env.tableArn(m)
		if err != nil {
			return nil, err
		}
		tables = append(tables, arn)
		st = append(st, tableStatement("SluisTable"+capitalize(m), arn, ddbWriteActions))
		st = append(st, env.blobStatements(m, "Sluis"+capitalize(m), []string{s3GetObject, s3PutObject, s3DeleteObject})...)
	}

	// Cross-grants.
	keyActions := map[string]bool{}
	var crossArns []string
	for _, g := range CrossGrants {
		if g.Role != role.Name {
			continue
		}
		for _, m := range sortedModules(g.Modules) {
			if own[m] {
				continue
			}
			sts, key, err := env.crossStatements(g.Verb, m)
			if err != nil {
				return nil, err
			}
			st = append(st, sts...)
			if g.Verb != CrossInvoke {
				arn, err := env.tableArn(m)
				if err != nil {
					return nil, err
				}
				tables = append(tables, arn)
			}
			for _, a := range key {
				keyActions[a] = true
			}
			if g.Verb == CrossReadAll || g.Verb == CrossWriteAll {
				crossArns = append(crossArns, parameterArnsUnder(env.Region, env.Account,
					env.Modules.InternalPrefix(m), env.Modules.ExternalPrefix(m))...)
			}
		}
	}
	// The maintenance flag is written by the restore role alone: every other
	// role is denied the writes of that partition on every table it holds, its
	// own and a peer's.
	if role.Name != RoleRestore {
		st = append(st, statement{
			"Sid": sidMaintenanceDeny, "Effect": "Deny", "Action": ddbMaintenanceDenied, "Resource": tablesOf(tables),
			"Condition": map[string]any{
				"ForAnyValue:StringEquals": map[string]any{"dynamodb:LeadingKeys": []string{MaintenancePartition}},
			},
		})
	}

	if len(keyActions) > 0 {
		var acts []string
		for a := range keyActions {
			acts = append(acts, a)
		}
		st = append(st, parameterKeyStatementsCross(env, acts, crossArns)...)
	}
	if env.TableKeyArn != "" && len(tables) > 0 {
		st = append(st, statement{
			"Sid": sidKey, "Effect": "Allow",
			"Action":   []string{kmsEncrypt, kmsDecrypt, "kms:GenerateDataKey", "kms:DescribeKey"},
			"Resource": env.TableKeyArn,
			"Condition": map[string]any{
				"StringLike": map[string]any{"kms:ViaService": "dynamodb.*.amazonaws.com"},
			},
		})
	}
	return st, nil
}

// ModuleRolePolicy is ModuleRoleStatements rendered as a policy document.
func ModuleRolePolicy(env ModuleEnv, role Role) (string, error) {
	st, err := ModuleRoleStatements(env, role)
	if err != nil {
		return "", err
	}
	return document(st)
}

// RestoreInvokeStatement is the resource policy of the restore function: only
// the named principals may invoke it.
func RestoreInvokeStatement(functionArn string, principalArns []string) map[string]any {
	return statement{
		"Sid": "SluisRestoreInvoke", "Effect": "Allow",
		"Principal": map[string]any{"AWS": principalArns},
		"Action":    lambdaInvokeFunction, "Resource": functionArn,
	}
}

func (env ModuleEnv) tableArn(m Module) (string, error) {
	arn := env.TableArns[m]
	if arn == "" {
		return "", fmt.Errorf("sluispulumi: no table ARN for module %q", m)
	}
	return arn, nil
}

// sidMaintenanceDeny is the Sid of the deny of the maintenance partition.
const sidMaintenanceDeny = "SluisMaintenanceDeny"

// tablesOf is the resource list of a deny: the tables, sorted and without
// repeats, as one string when there is one.
func tablesOf(arns []string) any {
	out := sortedStrings(arns)
	var uniq []string
	for _, a := range out {
		if len(uniq) == 0 || uniq[len(uniq)-1] != a {
			uniq = append(uniq, a)
		}
	}
	if len(uniq) == 1 {
		return uniq[0]
	}
	return uniq
}

func tableStatement(sid, tableArn string, actions []string) statement {
	return statement{"Sid": sid, "Effect": "Allow", "Action": actions, "Resource": tableArn}
}

// blobStatements grants the module's prefix of the bucket and the listing of
// exactly that prefix.
func (env ModuleEnv) blobStatements(m Module, sid string, actions []string) []statement {
	if env.BucketArn == "" {
		return nil
	}
	return []statement{{
		"Sid": sid + "Blobs", "Effect": "Allow", "Action": actions,
		"Resource": env.BucketArn + "/" + BlobPrefix(m) + "*",
	}, {
		"Sid": sid + "BlobList", "Effect": "Allow", "Action": s3ListBucket, "Resource": env.BucketArn,
		"Condition": map[string]any{"StringLike": map[string]any{"s3:prefix": []string{BlobPrefix(m) + "*"}}},
	}}
}

// crossStatements renders one cross-grant on one module; the key actions it
// needs through SSM are returned for the one key statement.
func (env ModuleEnv) crossStatements(v CrossVerb, m Module) ([]statement, []string, error) {
	sid := "SluisCross" + capitalize(m)
	switch v {
	case CrossReadTable:
		arn, err := env.tableArn(m)
		if err != nil {
			return nil, nil, err
		}
		return []statement{tableStatement(sid+"Table", arn, ddbReadActions)}, nil, nil
	case CrossInvoke:
		for _, r := range env.Roles {
			if r.hosts(m) {
				arn := env.FunctionArns[r.Name]
				if arn == "" {
					return nil, nil, fmt.Errorf("sluispulumi: no function ARN for role %q", r.Name)
				}
				return []statement{{"Sid": sid + "Invoke", "Effect": "Allow", "Action": lambdaInvokeFunction, "Resource": arn}}, nil, nil
			}
		}
		return nil, nil, nil
	case CrossReadAll, CrossWriteAll:
		arn, err := env.tableArn(m)
		if err != nil {
			return nil, nil, err
		}
		tact, bact, sact, kact := ddbReadActions, []string{s3GetObject}, ssmReadActions, []string{kmsDecrypt}
		if v == CrossWriteAll {
			tact, bact = ddbWriteActions, []string{s3GetObject, s3PutObject, s3DeleteObject}
			sact = ssmOwnActions
			kact = []string{kmsEncrypt, kmsDecrypt, "kms:GenerateDataKey"}
		}
		out := []statement{
			tableStatement(sid+"Table", arn, tact),
			{"Sid": sid + "Parameters", "Effect": "Allow", "Action": sact,
				"Resource": parameterArnsUnder(env.Region, env.Account, env.Modules.InternalPrefix(m), env.Modules.ExternalPrefix(m))},
		}
		out = append(out, env.blobStatements(m, sid, bact)...)
		return out, kact, nil
	}
	return nil, nil, fmt.Errorf("sluispulumi: unknown cross verb %q", v)
}

// parameterKeyStatementsCross is the key statement of the cross-grants, beside
// the one the role's own parameters have; the two never share a Sid.
func parameterKeyStatementsCross(env ModuleEnv, actions, arns []string) []statement {
	out := parameterKeyStatements(env.ParameterKeyArn, sortedStrings(actions), arns)
	for _, s := range out {
		s["Sid"] = sidParamKy + "Cross"
	}
	return out
}
