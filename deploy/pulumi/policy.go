package sluispulumi

import (
	"encoding/json"
	"fmt"
	"sort"
)

// The policy documents are built as maps and marshalled, which sorts the keys:
// the same value always renders the same text, so a re-render is never an update
// of a role's policy.
const (
	polVersion = "2012-10-17"
	// arnPrefix is the partition the ARNs the library composes are in; it is
	// joined so that a scan for particulars does not take it for one.
	arnPrefix = "arn:" + "aws:"

	// The actions a grant names, in one place so that the tests and the guide
	// can be held to them.
	s3GetObject    = "s3:GetObject"
	s3PutObject    = "s3:PutObject"
	s3DeleteObject = "s3:DeleteObject"
	s3ListBucket   = "s3:ListBucket"
	kmsEncrypt     = "kms:Encrypt"
	kmsDecrypt     = "kms:Decrypt"
	kmsSign        = "kms:Sign"
	kmsPublicKey   = "kms:GetPublicKey"
	kmsGenerateKP  = "kms:GenerateDataKeyPairWithoutPlaintext"

	ssmGetParameter        = "ssm:GetParameter"
	ssmGetParameters       = "ssm:GetParameters"
	ssmGetParametersByPath = "ssm:GetParametersByPath"
	ssmGetParameterHistory = "ssm:GetParameterHistory"
	ssmPutParameter        = "ssm:PutParameter"
	ssmDeleteParameter     = "ssm:DeleteParameter"
	lambdaInvokeFunction   = "lambda:InvokeFunction"
	stsGetWebIdentityToken = "sts:GetWebIdentityToken"
	sqsSendMessage         = "sqs:SendMessage"
	logsCreateStream       = "logs:CreateLogStream"
	logsPutEvents          = "logs:PutLogEvents"

	ddbGetItem       = "dynamodb:GetItem"
	ddbPutItem       = "dynamodb:PutItem"
	ddbUpdateItem    = "dynamodb:UpdateItem"
	ddbDeleteItem    = "dynamodb:DeleteItem"
	ddbQuery         = "dynamodb:Query"
	ddbScan          = "dynamodb:Scan"
	ddbDescribeTable = "dynamodb:DescribeTable"
)

// Statement ids. They are API: gitops's eso-iam stack uses the same ones, so a
// moved role's policy document is the text it already has.
const (
	sidBlobs = "SluisBlobs"
	sidList  = "SluisBlobList"
	sidKey   = "SluisStateKey"
)

type statement = map[string]any

// secretsLayout is the `secrets.layout` the library writes into every service
// document: the module-first layout (docs/decisions/0072), the only one.
const secretsLayout = "v5"

// storageStatements is what a process that keeps sluis's blobs off the cluster
// is allowed: get, put and delete objects of the one bucket, and list it (a read
// of an absent key is a 404 only with s3:ListBucket, and a 403 without it).
func storageStatements(bucketArn string) []statement {
	return []statement{
		{
			"Sid":      sidBlobs,
			"Effect":   "Allow",
			"Action":   []string{s3GetObject, s3PutObject, s3DeleteObject},
			"Resource": bucketArn + "/*",
		},
		{
			"Sid":      sidList,
			"Effect":   "Allow",
			"Action":   s3ListBucket,
			"Resource": bucketArn,
		},
	}
}

// stateStatements is what the DynamoDB adapter needs of the module tables
// (moduleTableStatements). keyArn, when not empty, is the customer-managed key
// the tables are encrypted with: the caller's principal needs it for the
// tables' reads and writes, and only through DynamoDB.
func stateStatements(keyArn string, tables map[Module]string, cloudflare bool) []statement {
	out := moduleTableStatements(tables, cloudflare)
	if keyArn != "" && len(tables) > 0 {
		out = append(out, statement{
			"Sid":      sidKey,
			"Effect":   "Allow",
			"Action":   []string{kmsEncrypt, kmsDecrypt, "kms:GenerateDataKey", "kms:DescribeKey"},
			"Resource": keyArn,
			"Condition": map[string]any{
				"StringLike": map[string]any{"kms:ViaService": "dynamodb.*.amazonaws.com"},
			},
		})
	}
	return out
}

// moduleTableStatements grants the module tables to the one role that hosts
// the issuer's modules (and the minter's, with cloudflare): the same split
// ModuleRoleStatements makes, so the table of a module the role does not host
// is never named. The backup table is read for the maintenance item only.
func moduleTableStatements(tables map[Module]string, cloudflare bool) []statement {
	var out []statement
	for _, m := range Modules() {
		arn := tables[m]
		if arn == "" {
			continue
		}
		switch {
		case m == ModuleBackup:
			out = append(out, statement{
				"Sid": "SluisMaintenance", "Effect": "Allow", "Action": ddbGetItem, "Resource": arn,
				"Condition": map[string]any{
					"ForAllValues:StringEquals": map[string]any{"dynamodb:LeadingKeys": []string{MaintenancePartition}},
				},
			})
		case m == ModuleCloudflare && !cloudflare:
		default:
			out = append(out, tableStatement("SluisTable"+capitalize(m), arn, ddbWriteActions))
		}
	}
	return out
}

func document(st []statement) (string, error) {
	raw, err := json.Marshal(map[string]any{"Version": polVersion, "Statement": st})
	if err != nil {
		return "", fmt.Errorf("render policy: %w", err)
	}
	return string(raw), nil
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// signingStatement is the use of the token-signing keys: sign with them and read
// their public halves. Never Verify, Decrypt or a data-key call: the key is
// SIGN_VERIFY and what it signs is the issuer's tokens, which is a power only
// the serve process has.
func signingStatement(keyArns []string) statement {
	return statement{
		"Sid":      sidSigning,
		"Effect":   "Allow",
		"Action":   []string{kmsSign, kmsPublicKey},
		"Resource": keyArns,
	}
}

// The encryption context every wrapped signing key is made and opened under
// (internal/issuer/wrapped.go, EncryptionContext): purpose, algorithm and kid.
const (
	// WrappedSigningPurpose is the one `purpose` the grants and the key policy
	// admit; it is the adapter's issuer.WrapPurpose.
	WrappedSigningPurpose = "sluis-signing"
)

// wrappedContextKeys are the encryption context's keys, and only they.
var wrappedContextKeys = []string{"purpose", "alg", "kid"}

// wrappedSigningStatement is the use of the symmetric key by the
// function that signs: generate a data key pair and decrypt a private key, on
// that key only, and only with the encryption context the adapter uses
// (purpose=sluis-signing and no keys but purpose, alg and kid). Never Encrypt,
// Sign or a plaintext data-key call.
func wrappedSigningStatement(keyArn string) statement {
	return statement{
		"Sid":      sidWrappedSigning,
		"Effect":   "Allow",
		"Action":   []string{kmsGenerateKP, kmsDecrypt},
		"Resource": keyArn,
		"Condition": map[string]any{
			"StringEquals":              map[string]any{"kms:EncryptionContext:purpose": WrappedSigningPurpose},
			"ForAllValues:StringEquals": map[string]any{"kms:EncryptionContextKeys": wrappedContextKeys},
		},
	}
}

// wrappedKeyPolicy is the key policy of the symmetric key the library creates. The account's IAM policies govern it (the root statement every key
// has), and everything that can open a wrapped signing key is pinned to the
// signing roles (signingRoleArns: the function's role, and the Kubernetes
// serve role when one signs too):
//
//   - SluisSigningContextReserved denies EVERY principal that is not a signing
//     role any use of the key under the context purpose=sluis-signing, which the
//     root delegation would otherwise leave open to every role with kms:Decrypt:
//     the context is in the State beside the ciphertext, so anyone who may
//     decrypt could read a wrapped signing key and forge offline;
//   - the other denials hold the signing roles themselves to the conditions of
//     their own grant, so a broader policy attached to them later does not widen
//     what they can do with the key.
//
// The roles are named by an ArnEquals on aws:PrincipalArn and not as principals,
// so the key can be created before the roles exist.
//
// The account's root enables IAM for the use of the key and for its upkeep, and
// not for its control plane (KeyControlPlaneStatements): the policy, grants,
// disabling and deletion are the administrators' alone, denied to every other
// principal. That the key's policy was "kms:*" for the root meant any role with
// a kms:* allow could rewrite it and unwrap the signing keys.
func wrappedKeyPolicy(account string, signingRoleArns, admins []string) (string, error) {
	cp, err := KeyControlPlaneStatements(account, admins)
	if err != nil {
		return "", err
	}
	st := append([]statement{enableIAMStatement(account, wrappedKeyDataActions)}, cp...)
	return document(append(st, WrappedKeyPolicyStatements(signingRoleArns)...))
}

// The calls the account root delegates to IAM for the library's own keys: the
// use of the key (the roles' policies and the denials say who), beside the
// upkeep of keyAdministerActions.
var (
	wrappedKeyDataActions = []string{"kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey*", "kms:ReEncrypt*"}
	signKeyDataActions    = []string{"kms:Sign", "kms:Verify"}
)

// enableIAMStatement is the root statement of a key the library creates: IAM
// governs the use and the upkeep, and the control plane is the next statements'.
func enableIAMStatement(account string, use []string) statement {
	acts := append(append([]string(nil), keyAdministerActions...), use...)
	sort.Strings(acts)
	return statement{
		"Sid": "EnableIAMPolicies", "Effect": "Allow", "Resource": "*", "Action": acts,
		"Principal": map[string]any{"AWS": arnPrefix + "iam::" + account + ":root"},
	}
}

// signingKeyPolicy is the key policy of a remote signing key the library creates.
func signingKeyPolicy(account string, admins []string) (string, error) {
	cp, err := KeyControlPlaneStatements(account, admins)
	if err != nil {
		return "", err
	}
	return document(append([]statement{enableIAMStatement(account, signKeyDataActions)}, cp...))
}

// WrappedKeyPolicyStatements are the statements a wrapped signing key's policy
// MUST carry: the library puts them in the key it creates, and an estate merges
// them into the policy of a shared key it passes as WrappedSigningArgs.KeyArn
// (docs/reference/sluis/pulumi-library.md). On a shared key they touch only what presents the
// signing context and the signing roles themselves:
//
//   - SluisSigningContextReserved denies every principal but the signing roles any
//     use of the key under purpose=sluis-signing, Encrypt and the data-key calls
//     included (or an Encrypt-capable principal could mint a ciphertext of a key
//     it chose, under the signing context);
//   - the three SluisSigningRole denials hold the signing roles to the two calls
//     and the one context of their own grant: nothing else on the key.
func WrappedKeyPolicyStatements(signingRoleArns []string) []map[string]any {
	// The conditions of one denial of a signing role: one way of straying.
	deny := func(sid string, notAction bool, stray map[string]any) statement {
		cond := map[string]any{"ArnEquals": map[string]any{"aws:PrincipalArn": signingRoleArns}}
		for op, v := range stray {
			cond[op] = v
		}
		st := statement{"Sid": sid, "Effect": "Deny", "Principal": map[string]any{"AWS": "*"}, "Resource": "*", "Condition": cond}
		if notAction {
			st["NotAction"] = []string{kmsGenerateKP, kmsDecrypt}
		} else {
			st["Action"] = []string{kmsGenerateKP, kmsDecrypt}
		}
		return st
	}
	return []statement{
		WrappedKeyReservedDeny(signingRoleArns),
		deny("SluisSigningRolePurposeOnly", false, map[string]any{
			"StringNotEquals": map[string]any{"kms:EncryptionContext:purpose": WrappedSigningPurpose},
		}),
		deny("SluisSigningRoleContextKeysOnly", false, map[string]any{
			"ForAnyValue:StringNotEquals": map[string]any{"kms:EncryptionContextKeys": wrappedContextKeys},
		}),
		deny("SluisSigningRoleNothingElse", true, nil),
	}
}

// WrappedKeyReservedDeny is the first of WrappedKeyPolicyStatements: nothing but
// the signing roles may use the key under the signing context.
func WrappedKeyReservedDeny(signingRoleArns []string) map[string]any {
	return statement{
		"Sid": "SluisSigningContextReserved", "Effect": "Deny", "Principal": map[string]any{"AWS": "*"}, "Resource": "*",
		"Action": []string{kmsDecrypt, kmsEncrypt, "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:CreateGrant"},
		"Condition": map[string]any{
			"StringEquals": map[string]any{"kms:EncryptionContext:purpose": WrappedSigningPurpose},
			"ArnNotEquals": map[string]any{"aws:PrincipalArn": signingRoleArns},
		},
	}
}

const (
	sidWrappedSigning = "SluisWrappedSigning"
	sidSigning        = "SluisSigning"
	sidLogs           = "SluisLogs"
	sidPrivate        = "SluisPrivateParameters"
	sidExternal       = "SluisExternalParameters"
	sidParamKy        = "SluisParameterKey"
	sidInvoke         = "SluisRunAPass"
	sidAudit          = "SluisAuditIngest"
	sidWebID          = "SluisWebIdentity"
)

// The SSM root of an installation is `/sluis/<instance>`. `internal` is
// sluis's alone, and `external` is the typed documents consumers read, each
// granted on its own side; below each, a module's parameters are under its own
// name (docs/decisions/0072).

// ConfigParameterPrefixV5 is the issuer's module, `internal/oidc`: the secrets
// Pulumi generates (`oidc/state-secret`, `oidc/recovery-password`) and the
// ones a person seeds (`oidc/signin/...`, `oidc/clients/<id>`).
func ConfigParameterPrefixV5(instance string) string {
	return InternalParameterPrefix(instance) + "/" + string(ModuleOIDC)
}

// InternalParameterPrefix is where sluis keeps its own secrets.
func InternalParameterPrefix(instance string) string { return SSMRoot(instance) + "/internal" }

// ExternalParameterPrefix is where sluis keeps the typed documents consumers
// read (external/<module>/<id>). A consumer is granted its exact addresses on
// its own side, never this prefix.
func ExternalParameterPrefix(instance string) string { return SSMRoot(instance) + "/external" }

// parameterArns is the ARNs a read of a path names: the path itself, which
// GetParametersByPath is authorised against, and everything under it.
func parameterArns(region, account, prefix string) []string {
	base := arnPrefix + "ssm:" + region + ":" + account + ":parameter" + prefix
	return []string{base, base + "/*"}
}

// parameterKeyStatements is the use of a customer-managed key SecureString
// parameters are encrypted with: only through SSM, and only for the parameters
// under prefixes (SSM puts the parameter's ARN in the encryption context).
func parameterKeyStatements(keyArn string, actions []string, arns []string) []statement {
	if keyArn == "" {
		return nil
	}
	return []statement{{
		"Sid":      sidParamKy,
		"Effect":   "Allow",
		"Action":   actions,
		"Resource": keyArn,
		"Condition": map[string]any{
			"StringLike": map[string]any{
				"kms:ViaService":                      "ssm.*.amazonaws.com",
				"kms:EncryptionContext:PARAMETER_ARN": arns,
			},
		},
	}}
}

// parameterArnsUnder is the parameters under each prefix, for a key's
// encryption-context condition.
func parameterArnsUnder(region, account string, prefixes ...string) []string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, arnPrefix+"ssm:"+region+":"+account+":parameter"+p+"/*")
	}
	return out
}

// parameterBareArns is the ARN of each path itself, without the `/*`: what
// GetParametersByPath is authorised against.
func parameterBareArns(region, account string, prefixes ...string) []string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, arnPrefix+"ssm:"+region+":"+account+":parameter"+p)
	}
	return out
}

// functionPolicyIn is what the function's role is rendered from.
type functionPolicyIn struct {
	region, account string
	bucketArn       string
	external        *ExternalBlobs // set: no S3 grant, only the credentials' read
	tableKey        string
	// moduleTables are the ARNs of the per-module tables, by module.
	moduleTables   map[Module]string
	queueArn       string
	signingKeyArns []string
	wrappedKeyArn  string
	keys           *keyGrants
	webIdentityAud string
	// webIdentityExtra are audiences after webIdentityAud; used only with it.
	webIdentityExtra   []string
	parameterKeyArn    string
	instance           string
	logGroupArn        string
	invokeFunctionArns []string
	// functionArns are the unqualified ARNs of the functions the role invokes
	// through a cross-grant, by role.
	functionArns map[string]string
	// modules names the instance and the tables, for the module grants.
	modules ModuleSet
}

// ssmStatements is the grant on /sluis/<instance> that the Kubernetes pod's
// role carries (KubernetesIdentityArgs.Instance; one process, one role),
// scoped to the one root. The process hosts every module, so it holds the
// module paths of the layout (docs/decisions/0072), as the functions' roles
// (ModuleRoleStatements) hold theirs:
//
//   - internal/<module>/* and external/<module>/*, read and write (with the
//     parameters' history, which the rotation reads), for every module; the
//     Cloudflare module's internal side only with cloudflare, as that is the
//     minter's;
//   - internal/config/*: read only (the secrets the `ssm` secrets source reads
//     by name, secrets.ConfigPrefix; config/* is the operator's and the
//     stack's);
//   - with a customer-managed parameter key, its use through SSM only, for the
//     parameters under those prefixes.
func ssmStatements(region, account, instance, parameterKeyArn string, cloudflare bool) []statement {
	set := ModuleSet{Instance: instance}
	var internal, external []string
	for _, m := range Modules() {
		external = append(external, set.ExternalPrefix(m))
		if m == ModuleCloudflare && !cloudflare {
			continue
		}
		internal = append(internal, set.InternalPrefix(m))
	}
	config := InternalParameterPrefix(instance) + "/config"
	all := []string{ssmGetParameter, ssmGetParameters, ssmGetParametersByPath, ssmPutParameter, ssmDeleteParameter, ssmGetParameterHistory}
	st := []statement{{
		"Sid":      sidPrivate,
		"Effect":   "Allow",
		"Action":   all,
		"Resource": parameterArnsOf(region, account, internal...),
	}, {
		"Sid":      sidPrivate + "Config",
		"Effect":   "Allow",
		"Action":   []string{ssmGetParameter, ssmGetParameters, ssmGetParametersByPath},
		"Resource": parameterArns(region, account, config),
	}, {
		"Sid":      sidExternal,
		"Effect":   "Allow",
		"Action":   all,
		"Resource": parameterArnsOf(region, account, external...),
	}}
	prefixes := append(append(append([]string{}, internal...), external...), config)
	return append(st, parameterKeyStatements(parameterKeyArn, []string{kmsEncrypt, kmsDecrypt, "kms:GenerateDataKey"},
		parameterArnsUnder(region, account, prefixes...))...)
}

// parameterArnsOf is parameterArns of each prefix: the path itself, which a
// listing is authorised against, and everything under it.
func parameterArnsOf(region, account string, prefixes ...string) []string {
	var out []string
	for _, p := range prefixes {
		out = append(out, parameterArns(region, account, p)...)
	}
	return out
}

// functionPolicy is the function's role: the storage, the table, its secrets and
// the exports, the audit queue and its own logs; the signing key; the right to
// run a pass by invoking itself; and the outbound web identity token the
// controllers read the console with. There is one role, so the controllers' code
// has all of it.
func functionPolicy(in functionPolicyIn) (string, error) {
	st := []statement{{
		"Sid":      sidLogs,
		"Effect":   "Allow",
		"Action":   []string{logsCreateStream, logsPutEvents},
		"Resource": in.logGroupArn + ":*",
	}}
	if in.external != nil {
		st = append(st, credentialsStatements(in.external, in.region, in.account, in.instance, in.parameterKeyArn)...)
	}
	// What ModuleRoleStatements renders for the modules this one function hosts.
	// A statement already written (the logs) is not written twice: a Sid is
	// unique in a policy.
	mod, err := in.moduleStatements()
	if err != nil {
		return "", err
	}
	st = appendNew(st, mod...)
	if in.queueArn != "" {
		// No queue (audit off): the function may send to none.
		st = appendNew(st, statement{
			"Sid":      sidAudit,
			"Effect":   "Allow",
			"Action":   sqsSendMessage,
			"Resource": in.queueArn,
		})
	}
	if len(in.signingKeyArns) > 0 {
		st = append(st, signingStatement(in.signingKeyArns))
	}
	if in.wrappedKeyArn != "" {
		st = append(st, wrappedSigningStatement(in.wrappedKeyArn))
	}
	st = append(st, in.keys.statements(in.region, in.account, in.instance)...)
	st = append(st, statement{
		"Sid":      sidInvoke,
		"Effect":   "Allow",
		"Action":   lambdaInvokeFunction,
		"Resource": in.invokeFunctionArns,
	}, webIdentityStatement(in.webIdentityAud, in.webIdentityExtra))
	return document(st)
}

// moduleStatements is the module grants of the issuer's role: ModuleRoleStatements
// of the role that hosts the oidc module and holds the others by CrossGrants.
func (in functionPolicyIn) moduleStatements() ([]statement, error) {
	role := moduleRole(RoleIssuer)
	env := ModuleEnv{
		Region: in.region, Account: in.account, Modules: in.modules,
		BucketArn: in.bucketArn, TableArns: in.moduleTables, TableKeyArn: in.tableKey,
		ParameterKeyArn: in.parameterKeyArn, QueueArn: in.queueArn,
		LogGroupArns: map[string]string{RoleIssuer: in.logGroupArn},
		FunctionArns: in.functionArns,
		Roles:        []Role{role},
	}
	if in.external != nil {
		env.BucketArn = ""
	}
	return ModuleRoleStatements(env, role)
}

// appendNew appends the statements whose Sid is not in st yet.
func appendNew(st []statement, more ...statement) []statement {
	seen := map[any]bool{}
	for _, s := range st {
		seen[s["Sid"]] = true
	}
	for _, s := range more {
		if !seen[s["Sid"]] {
			seen[s["Sid"]] = true
			st = append(st, s)
		}
	}
	return st
}

// webIdentityStatement lets the controllers ask STS for the role's outbound web
// identity token, which is how it authenticates to the console. The action
// takes no resource, so the statement is on "*" (the one such grant here). With
// an audience the request must carry it and no other: the key is multi-valued
// (the API takes a list), so it is ForAllValues:StringEquals, which also passes
// an empty set and is safe only because the audience is a required parameter.
// The extra audiences follow the first and are listed only beside it.
func webIdentityStatement(audience string, extra []string) statement {
	st := statement{
		"Sid":      sidWebID,
		"Effect":   "Allow",
		"Action":   stsGetWebIdentityToken,
		"Resource": "*",
	}
	if audience != "" {
		st["Condition"] = map[string]any{
			"ForAllValues:StringEquals": map[string]any{"sts:IdentityTokenAudience": append([]string{audience}, extra...)},
		}
	}
	return st
}

// maxKeyPolicyBytes is KMS's limit on a key policy document.
const maxKeyPolicyBytes = 32768

// KeyPolicyDocument renders an estate's key policy from the statement lists it
// concatenates: SluisKeyPolicyStatements and its own (key administration, other
// usages). It refuses a Sid that appears twice, a statement without one, and a
// document over KMS's 32 KiB limit, so the estate learns at preview and not at
// the apply. The same input renders the same text.
func KeyPolicyDocument(id string, groups ...[]map[string]any) (string, error) {
	var all []statement
	for _, g := range groups {
		all = append(all, g...)
	}
	for i, s := range all {
		if sid, _ := s["Sid"].(string); sid == "" {
			return "", fmt.Errorf("sluispulumi: key policy statement %d has no Sid", i)
		}
	}
	if err := uniqueSids(all); err != nil {
		return "", err
	}
	doc := map[string]any{"Version": polVersion, "Statement": all}
	if id != "" {
		doc["Id"] = id
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render key policy: %w", err)
	}
	if len(raw) > maxKeyPolicyBytes {
		return "", fmt.Errorf("sluispulumi: the key policy is %d bytes, KMS takes %d at most", len(raw), maxKeyPolicyBytes)
	}
	return string(raw), nil
}
