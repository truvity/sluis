package sluispulumi

import (
	"fmt"
	"slices"
)

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
	// SecretsKeyArn is the key LambdaArgs.Keys.Secrets names, once resolved: the
	// key for the role's parameters, set instead of ParameterKeyArn (the two are
	// exclusive). The role's grant on it is the ParameterKeyArn one: through SSM,
	// for exactly the parameters the role uses.
	SecretsKeyArn string
	// QueueArn is the audit queue; empty when audit is off.
	QueueArn string
	// LogGroupArns is each role's log group, by role name.
	LogGroupArns map[string]string
	// FunctionArns is each role's function ARN, unqualified, by role name. A
	// CrossInvoke grant is rendered on the alias of its caller class under this
	// ARN, never on the ARN itself.
	FunctionArns map[string]string
	// MinterRefs are the Cloudflare minter parameters (internal addresses,
	// `internal/cloudflare/<account>/minter`, a `*` allowed for the account) that
	// a role reads for `ports.blob.s3.credentials.preset` without hosting the
	// Cloudflare module: a read of exactly those parameters and nothing else of
	// the module. A role that hosts the module has them already.
	MinterRefs []string
	// OpensPresetBlobs says the role's own document opens blobs with
	// `ports.blob.s3.credentials.preset`, so the role runs the minter in its
	// process: it reads the minter parameters (MinterRefs) and writes what it
	// mints. The backup role holds the read whatever the document says.
	OpensPresetBlobs bool
	// Roles is every role of the installation.
	Roles []Role
	// WebIdentityAudience is the audience of the web identity token the roles of
	// [ConsoleCallers] may ask STS for: the console's. Empty allows any audience,
	// as on the issuer's role.
	WebIdentityAudience string
	// TelemetryAudiences are the audiences every role may ask STS for a web
	// identity token for, because the function runs the telemetry layer, which
	// authenticates to the collector with it (LambdaArgs.AdditionalWebIdentityAudiences,
	// with Telemetry). A role of [ConsoleCallers] holds them after the console's
	// audience in one statement. Empty: the roles hold no more than before.
	TelemetryAudiences []string
}

var (
	// ddbSnapshotActions are a read by key, for the issuer's view of a snapshot.
	// DescribeTable too: the readiness probe describes every table the document
	// lists (internal/port/dynamodb.Tables.Ping), the peer's among them.
	ddbSnapshotActions = []string{ddbGetItem, ddbQuery, ddbDescribeTable}
	ddbReadActions     = []string{ddbGetItem, ddbQuery, ddbScan, ddbDescribeTable}
	ddbWriteActions    = []string{ddbGetItem, ddbPutItem, ddbUpdateItem, ddbDeleteItem, ddbQuery, ddbScan, ddbDescribeTable}
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
	if role.Name == "" || (len(role.Hosts) == 0 && !role.hasCrossGrants()) {
		return nil, fmt.Errorf("sluispulumi: a role needs a name and at least one module, or a cross-grant")
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
		// A listing is authorised against the bare path ARN, not against the
		// parameters under it, so `/*` above never covers it. The module lists
		// its own two paths and nothing wider: no sibling module, no root.
		st = append(st, statement{
			"Sid": "SluisParametersList" + capitalize(m), "Effect": "Allow",
			"Action":   []string{ssmGetParametersByPath},
			"Resource": parameterBareArns(env.Region, env.Account, internal, external),
		})
		arns = append(arns, parameterArnsUnder(env.Region, env.Account, internal, external)...)
	}
	if len(arns) > 0 {
		// A role that hosts no module has no parameter of its own to use the key on.
		st = append(st, parameterKeyStatements(env.parameterKey(),
			[]string{kmsEncrypt, kmsDecrypt, "kms:GenerateDataKey"}, arns)...)
	}

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
	if own[ModuleGoogle] {
		// The Google module holds the OAuth client (consent, sign-in, reopening a
		// workspace), whose secrets layout v5 keeps in the issuer's namespace
		// (`internal/oidc/signin/<provider>/...`): the module reads those, and
		// nothing else of oidc.
		refs := parameterArnsUnder(env.Region, env.Account, signInClientPrefix(env.Modules))
		st = append(st, statement{
			"Sid": "SluisSignInClient", "Effect": "Allow", "Action": ssmReadActions, "Resource": refs,
		})
		keyActions[kmsDecrypt] = true
		crossArns = append(crossArns, refs...)
	}
	if len(env.MinterRefs) > 0 && !own[ModuleCloudflare] && (own[ModuleBackup] || env.OpensPresetBlobs) {
		// credentials.preset: the minter's credential, read and never written. The
		// issuer has none: it asks the cloudflare function to mint (v1.76 E-FIN). The
		// cloudflare function, which hosts the minter, has it already. The backup
		// role holds it, and so does a role whose own document opens blobs from a
		// preset (OpensPresetBlobs): its process mints in place.
		var refs []string
		for _, r := range sortedStrings(env.MinterRefs) {
			refs = append(refs, arnPrefix+"ssm:"+env.Region+":"+env.Account+":parameter"+env.Modules.rootOf()+"/"+r)
		}
		st = append(st, statement{
			"Sid": "SluisCrossCloudflareMinter", "Effect": "Allow", "Action": ssmReadActions, "Resource": refs,
		})
		keyActions[kmsDecrypt] = true
		crossArns = append(crossArns, refs...)
	}
	if len(env.MinterRefs) > 0 && !own[ModuleCloudflare] && env.OpensPresetBlobs {
		// What the minter writes: the record of the tokens it minted
		// (internal/cloudflare/minted/<preset>) and the credential it stores where
		// the blobs adapter reads it (external/cloudflare/<preset>). A role that
		// mints holds nothing else of the module.
		minted := parameterArnsUnder(env.Region, env.Account,
			env.Modules.InternalPrefix(ModuleCloudflare)+"/minted", env.Modules.ExternalPrefix(ModuleCloudflare))
		st = append(st, statement{
			"Sid": "SluisCrossCloudflareMinted", "Effect": "Allow", "Action": ssmOwnActions, "Resource": minted,
		})
		for _, k := range parameterKeyStatements(env.parameterKey(),
			[]string{kmsEncrypt, kmsDecrypt, "kms:GenerateDataKey"}, minted) {
			k["Sid"] = sidParamKy + "Minted"
			st = append(st, k)
		}
	}
	for _, g := range CrossGrants {
		if g.Role != role.Name {
			continue
		}
		if g.Verb == CrossInvoke {
			sts, err := env.invokeStatements(g)
			if err != nil {
				return nil, err
			}
			st = append(st, sts...)
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
			if g.Verb != CrossReadBlobs {
				arn, err := env.tableArn(m)
				if err != nil {
					return nil, err
				}
				tables = append(tables, arn)
			}
			for _, a := range key {
				keyActions[a] = true
			}
			if g.Verb == CrossReadAll || g.Verb == CrossReadModule || g.Verb == CrossWriteAll {
				crossArns = append(crossArns, parameterArnsUnder(env.Region, env.Account,
					env.Modules.InternalPrefix(m), env.Modules.ExternalPrefix(m))...)
			}
		}
	}
	// A controller's "run a pass now" starts its own function (the `invoke`
	// trigger of its document), on the alias the adapter always qualifies with:
	// the grant is that alias of this function's ARN and nothing else.
	if own[ModuleGitHub] || own[ModuleSlack] {
		if arn := env.FunctionArns[role.Name]; arn != "" {
			st = append(st, statement{
				"Sid": sidInvokeOwnController, "Effect": "Allow", "Action": lambdaInvokeFunction,
				"Resource": arn + ":" + CallerAlias(ClassConsole),
			})
		}
	}
	// A role that calls the console proves itself with its web identity token.
	// A role whose function runs the telemetry layer holds the layer's audiences.
	switch {
	case slices.Contains(ConsoleCallers, role.Name):
		st = append(st, webIdentityStatement(env.WebIdentityAudience, env.TelemetryAudiences))
	case len(env.TelemetryAudiences) > 0:
		st = append(st, webIdentityStatement(env.TelemetryAudiences[0], env.TelemetryAudiences[1:]))
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

// parameterKey is the key the role's SecureString parameters are encrypted
// with: the ParameterKeyArn, or the Keys.Secrets key (the two are exclusive).
func (env ModuleEnv) parameterKey() string {
	if env.ParameterKeyArn != "" {
		return env.ParameterKeyArn
	}
	return env.SecretsKeyArn
}

func (env ModuleEnv) tableArn(m Module) (string, error) {
	arn := env.TableArns[m]
	if arn == "" {
		return "", fmt.Errorf("sluispulumi: no table ARN for module %q", m)
	}
	return arn, nil
}

// sidInvokeOwnController is the Sid of the grant to invoke the controller's own
// function for a run-now.
const sidInvokeOwnController = "SluisInvokeOwnController"

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
	case CrossReadSnapshot:
		arn, err := env.tableArn(m)
		if err != nil {
			return nil, nil, err
		}
		return []statement{tableStatement(sid+"Table", arn, ddbSnapshotActions)}, nil, nil
	case CrossReadBlobs:
		return env.blobStatements(m, sid, []string{s3GetObject}), nil, nil
	case CrossReadAll, CrossReadModule, CrossWriteAll:
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

// invokeStatements renders a [CrossInvoke] grant: one statement, on the alias
// `live-<class>` of each function, which is the ARN a module call invokes. The
// unqualified ARN would let the role run $LATEST, and a version no alias names.
func (env ModuleEnv) invokeStatements(g CrossGrant) ([]statement, error) {
	if g.Class == "" || len(g.Functions) == 0 {
		return nil, fmt.Errorf("sluispulumi: the invoke grant of %q needs a caller class and a function", g.Role)
	}
	var arns []string
	for _, f := range g.Functions {
		arn := env.FunctionArns[f]
		if arn == "" {
			return nil, fmt.Errorf("sluispulumi: no function ARN for role %q, which %q invokes", f, g.Role)
		}
		arns = append(arns, arn+":"+CallerAlias(g.Class))
	}
	return []statement{{
		"Sid": "SluisCrossInvoke" + capitalize(Module(g.Class)), "Effect": "Allow",
		"Action": lambdaInvokeFunction, "Resource": sortedStrings(arns),
	}}, nil
}

// hasCrossGrants reports whether the role has a data grant beyond its own modules.
func (r Role) hasCrossGrants() bool {
	for _, g := range CrossGrants {
		if g.Role == r.Name && g.Verb != CrossInvoke {
			return true
		}
	}
	return false
}

// parameterKeyStatementsCross is the key statement of the cross-grants, beside
// the one the role's own parameters have; the two never share a Sid.
func parameterKeyStatementsCross(env ModuleEnv, actions, arns []string) []statement {
	out := parameterKeyStatements(env.parameterKey(), sortedStrings(actions), arns)
	for _, s := range out {
		s["Sid"] = sidParamKy + "Cross"
	}
	return out
}

// MaintenanceOperatorStatements are the statements of the principal that runs
// `sluis maintenance on|off|status`: an operator's role, never a function's.
// It reads, writes and deletes the one maintenance item of each table
// (partition [MaintenancePartition]) and nothing else of them, so that it can
// set and lift the migration freeze and cannot read or change a session, a
// record or a secret. The function roles keep their [sidMaintenanceDeny];
// this is the other half of that deny.
//
// tables are the table ARNs: the legacy table on layout v4, and the module
// tables on v5 (env.TableArns, in module order). The table key is used only
// through DynamoDB.
func MaintenanceOperatorStatements(tables []string, tableKeyArn string) ([]map[string]any, error) {
	if len(tables) == 0 {
		return nil, fmt.Errorf("sluispulumi: MaintenanceOperatorStatements: no table")
	}
	st := []statement{{
		"Sid": "SluisMaintenanceOperator", "Effect": "Allow",
		"Action":   []string{ddbGetItem, ddbPutItem, ddbDeleteItem},
		"Resource": tablesOf(tables),
		"Condition": map[string]any{
			"ForAllValues:StringEquals": map[string]any{"dynamodb:LeadingKeys": []string{MaintenancePartition}},
		},
	}}
	if tableKeyArn != "" {
		st = append(st, statement{
			"Sid": "SluisMaintenanceOperatorKey", "Effect": "Allow",
			"Action":   []string{kmsEncrypt, kmsDecrypt, "kms:GenerateDataKey", "kms:DescribeKey"},
			"Resource": tableKeyArn,
			"Condition": map[string]any{
				"StringLike": map[string]any{"kms:ViaService": "dynamodb.*.amazonaws.com"},
			},
		})
	}
	return st, nil
}

// OperatorAuditStatements are the statements that let the principal that runs
// an audited operator command (`sluis maintenance on|off`, `sluis migrate v5
// copy|prune`) deliver the command's audit record. Those commands refuse when
// the record cannot be delivered, so the operator's role needs what the
// function roles have: sqs:SendMessage on the audit queue, and nothing more.
// If the queue is encrypted with a customer-managed key, the operator also
// needs kms:GenerateDataKey and kms:Decrypt on it through SQS.
func OperatorAuditStatements(queueArn string) ([]map[string]any, error) {
	if queueArn == "" {
		return nil, fmt.Errorf("sluispulumi: OperatorAuditStatements: no queue ARN")
	}
	return []map[string]any{{
		"Sid": "SluisOperatorAudit", "Effect": "Allow", "Action": []string{sqsSendMessage}, "Resource": queueArn,
	}}, nil
}

// signInClientPrefix is the path of the sign-in client's secrets, below which
// the Google module reads (`internal/oidc/signin`).
func signInClientPrefix(m ModuleSet) string { return m.InternalPrefix(ModuleOIDC) + "/signin" }
