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
	sidState = "SluisState"
	sidKey   = "SluisStateKey"
)

type statement = map[string]any

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

// stateStatements is what the DynamoDB adapter needs of its table: the item
// calls it makes (a Scan is `sluis migrate` and a listing by a prefix
// with no dot) and DescribeTable, which the start-up check and the readiness
// probe make. keyArn, when not empty, is the customer-managed key the table is
// encrypted with: the caller's principal needs it for the table's reads and
// writes, and only through DynamoDB.
func stateStatements(tableArn, keyArn string) []statement {
	out := []statement{{
		"Sid":      sidState,
		"Effect":   "Allow",
		"Action":   []string{ddbGetItem, ddbPutItem, ddbUpdateItem, ddbDeleteItem, ddbQuery, ddbScan, ddbDescribeTable},
		"Resource": tableArn,
	}}
	if keyArn != "" {
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
func wrappedKeyPolicy(account string, signingRoleArns []string) (string, error) {
	return document(append([]statement{{
		"Sid": "EnableIAMPolicies", "Effect": "Allow", "Resource": "*", "Action": "kms:*",
		"Principal": map[string]any{"AWS": arnPrefix + "iam::" + account + ":root"},
	}}, WrappedKeyPolicyStatements(signingRoleArns)...))
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
	sidCloudflare     = "SluisCloudflare"
	sidParamKy        = "SluisParameterKey"
	sidInvoke         = "SluisRunAPass"
	sidAudit          = "SluisAuditIngest"
	sidWebID          = "SluisWebIdentity"
)

// SSM layout v4 (docs/decisions/0041): one root per installation,
// `/sluis/<instance>`. `internal` is sluis's alone, and `external` is the typed
// documents consumers read, each granted on its own side.

// ConfigParameterPrefix is the operator's and the stack's: the secrets a person
// seeds (`config/providers/google/<id>/client-secret`,
// `config/clients/<id>/secret`) and the ones Pulumi generates
// (`config/issuer/state-secret`, `config/recovery/password`). sluis only reads
// them, by the names its http document gives; its own writes are under
// `credentials/` (docs/reference/sluis/storage-layout.md).
func ConfigParameterPrefix(instance string) string {
	return InternalParameterPrefix(instance) + "/config"
}

// InternalParameterPrefix is where sluis keeps its own secrets.
func InternalParameterPrefix(instance string) string { return SSMRoot(instance) + "/internal" }

// ExternalParameterPrefix is where sluis keeps the typed documents consumers
// read on layout v4 (external/<kind>/<id>). A consumer is granted its exact
// addresses on its own side, never this prefix.
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

// functionPolicyIn is what the function's role is rendered from.
type functionPolicyIn struct {
	region, account    string
	bucketArn          string
	external           *ExternalBlobs // set: no S3 grant, only the credentials' read
	tableArn, tableKey string
	queueArn           string
	signingKeyArns     []string
	wrappedKeyArn      string
	keys               *keyGrants
	webIdentityAud     string
	// webIdentityExtra are audiences after webIdentityAud; used only with it.
	webIdentityExtra   []string
	parameterKeyArn    string
	instance           string
	logGroupArn        string
	invokeFunctionArns []string
	// cloudflare adds the grants of the Cloudflare minter (see ssmStatements).
	cloudflare bool
}

// ssmStatements is the grant on /sluis/<instance> that both the Lambda role and
// the Kubernetes pod's role carry (one process, one role), scoped to the one
// root:
//
//   - internal/credentials/* and external/*, read and write (with the
//     parameters' history, which the rotation reads);
//   - internal/config/*: read only (the secrets its document names: the
//     recovery password, the state secret, the OAuth client, the declared
//     clients and workspaces); config/* is the operator's and the stack's;
//   - with a customer-managed parameter key, its use through SSM only, for the
//     parameters under those prefixes.
func ssmStatements(region, account, instance, parameterKeyArn string, cloudflare bool) []statement {
	all := []string{ssmGetParameter, ssmGetParameters, ssmGetParametersByPath, ssmPutParameter, ssmDeleteParameter}
	var st []statement
	withHistory := append(append([]string{}, all...), ssmGetParameterHistory)
	st = append(st, statement{
		"Sid":      sidPrivate + "V4",
		"Effect":   "Allow",
		"Action":   withHistory,
		"Resource": parameterArns(region, account, InternalParameterPrefix(instance)+"/credentials"),
	}, statement{
		"Sid":      sidPrivate + "V4Config",
		"Effect":   "Allow",
		"Action":   []string{ssmGetParameter, ssmGetParameters, ssmGetParametersByPath},
		"Resource": parameterArns(region, account, InternalParameterPrefix(instance)+"/config"),
	}, statement{
		"Sid":      sidExternal,
		"Effect":   "Allow",
		"Action":   withHistory,
		"Resource": parameterArns(region, account, ExternalParameterPrefix(instance)),
	})
	if cloudflare {
		// sluis as the STS for Cloudflare: the minter credential of each account is
		// read and never written (it is the operator's), the record of the ids it
		// minted is read and written, and the credentials it stores are written
		// where consumers read them. Exactly these paths; the KMS grant below
		// already covers every parameter under internal/ and external/.
		st = append(st, statement{
			"Sid":      sidCloudflare + "Minter",
			"Effect":   "Allow",
			"Action":   []string{ssmGetParameter, ssmGetParameters, ssmGetParametersByPath},
			"Resource": parameterArns(region, account, InternalParameterPrefix(instance)+"/cloudflare"),
		}, statement{
			"Sid":      sidCloudflare + "Minted",
			"Effect":   "Allow",
			"Action":   withHistory,
			"Resource": parameterArns(region, account, InternalParameterPrefix(instance)+"/cloudflare-minted"),
		}, statement{
			"Sid":      sidCloudflare + "Stored",
			"Effect":   "Allow",
			"Action":   withHistory,
			"Resource": parameterArns(region, account, ExternalParameterPrefix(instance)+"/cloudflare"),
		})
	}
	prefixes := []string{InternalParameterPrefix(instance), ExternalParameterPrefix(instance)}
	return append(st, parameterKeyStatements(parameterKeyArn, []string{kmsEncrypt, kmsDecrypt, "kms:GenerateDataKey"},
		parameterArnsUnder(region, account, prefixes...))...)
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
	if in.external == nil {
		st = append(st, storageStatements(in.bucketArn)...)
	} else {
		st = append(st, credentialsStatements(in.external, in.region, in.account, in.instance, in.parameterKeyArn)...)
	}
	st = append(st, stateStatements(in.tableArn, in.tableKey)...)
	st = append(st, ssmStatements(in.region, in.account, in.instance, in.parameterKeyArn, in.cloudflare)...)
	if in.queueArn != "" {
		// No queue (audit off): the function may send to none.
		st = append(st, statement{
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
