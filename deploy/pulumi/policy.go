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

// keyringWriteDenial keeps a process that does not sign from writing the key
// ring in the State table: with wrapped signing the ring is what a signer
// trusts to learn which keys to publish, so a write there is a way to plant one.
// The partition key is the record kind (layout v2): `keyring`, `keyring-index`
// and `keyring-retired`. The key-generation lease (kind `lease`, id
// `signing-keygen/<alg>`) shares its partition with the controllers' own leases
// and cannot be denied this way: a limit, noted in docs/deployment/aws.md.
func keyringWriteDenial(tableArn string) statement {
	return statement{
		"Sid":      sidKeyringWrites,
		"Effect":   "Deny",
		"Action":   []string{ddbPutItem, ddbUpdateItem, ddbDeleteItem, "dynamodb:BatchWriteItem"},
		"Resource": tableArn,
		"Condition": map[string]any{
			"ForAnyValue:StringEquals": map[string]any{"dynamodb:LeadingKeys": []string{"keyring", "keyring-index", "keyring-retired"}},
		},
	}
}

// wrappedKeyPolicy is the key policy of the symmetric key the library creates. The account's IAM policies govern it (the root statement every key
// has), and everything that can open a wrapped signing key is pinned to the
// signing roles (signingRoleArns: the http function's role, and the Kubernetes
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
// (docs/deployment/aws.md). On a shared key they touch only what presents the
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
	sidKeyringWrites  = "SluisNoKeyringWrites"
	sidSigning        = "SluisSigning"
	sidLogs           = "SluisLogs"
	sidPrivate        = "SluisPrivateParameters"
	sidExport         = "SluisExportParameters"
	sidParamKy        = "SluisParameterKey"
	sidInvoke         = "SluisRunAPass"
	sidAudit          = "SluisAuditIngest"
	sidWebID          = "SluisWebIdentity"
)

// SSM layout (decision D1a): `private` is sluis's alone, `export` is what
// consumers read.
const (
	// PrivateParameterPrefix is where sluis keeps its own secrets.
	PrivateParameterPrefix = "/sluis/private"
	// ConfigParameterPrefix is the operator's and the stack's: the secrets a
	// person seeds (`config/oauth/client-id`, `config/clients/<id>`) and the one
	// Pulumi generates (`config/issuer/state-secret`). sluis only reads them; its
	// own writes are under `credentials/` (docs/reference/storage-layout.md).
	ConfigParameterPrefix = PrivateParameterPrefix + "/config"
	// CredentialsParameterPrefix is where sluis writes the credentials of its
	// records, by kind.
	CredentialsParameterPrefix = PrivateParameterPrefix + "/credentials"
	// ExportParameterPrefix is where sluis writes what consumers read.
	ExportParameterPrefix = "/sluis/export"
)

// parameterArns is the ARNs a read of a path names: the path itself, which
// GetParametersByPath is authorised against, and everything under it.
func parameterArns(region, account, prefix string) []string {
	base := arnPrefix + "ssm:" + region + ":" + account + ":parameter" + prefix
	return []string{base, base + "/*"}
}

// parameterKeyStatements is the use of a customer-managed key SecureString
// parameters are encrypted with, and only through SSM.
func parameterKeyStatements(keyArn string) []statement {
	if keyArn == "" {
		return nil
	}
	return []statement{{
		"Sid":      sidParamKy,
		"Effect":   "Allow",
		"Action":   []string{kmsEncrypt, kmsDecrypt, "kms:GenerateDataKey"},
		"Resource": keyArn,
		"Condition": map[string]any{
			"StringLike": map[string]any{"kms:ViaService": "ssm.*.amazonaws.com"},
		},
	}}
}

// functionPolicyIn is what one function's role is rendered from.
type functionPolicyIn struct {
	role               string
	region, account    string
	bucketArn          string
	tableArn, tableKey string
	queueArn           string
	signingKeyArns     []string
	wrappedKeyArn      string
	webIdentity        bool
	webIdentityAud     string
	parameterKeyArn    string
	logGroupArn        string
	invokeFunctionArns []string
}

// privateStatements is a function's grant on /sluis/private, narrowed to what
// the function does there.
//
//   - http reads all of it (the operator's config/*, and the credentials of its
//     records) and writes only credentials/*: it never writes config/*, which is
//     the operator's and the stack's.
//   - a controller reads and writes what it is told to by name (its own secret
//     files, its credentials/*), but config/* is the http function's: the recovery
//     password, the state secret, the OAuth client and the declared clients. An
//     explicit Deny, which wins over any Allow, keeps a leaked controller role
//     from reading, replacing or deleting them.
func privateStatements(in functionPolicyIn) []statement {
	all := []string{ssmGetParameter, ssmGetParameters, ssmGetParametersByPath, ssmPutParameter, ssmDeleteParameter}
	if in.role == RoleHTTP {
		return []statement{
			{
				"Sid":      sidPrivate,
				"Effect":   "Allow",
				"Action":   []string{ssmGetParameter, ssmGetParameters, ssmGetParametersByPath},
				"Resource": parameterArns(in.region, in.account, PrivateParameterPrefix),
			},
			{
				"Sid":      sidPrivate + "Write",
				"Effect":   "Allow",
				"Action":   []string{ssmPutParameter, ssmDeleteParameter},
				"Resource": parameterArns(in.region, in.account, CredentialsParameterPrefix),
			},
		}
	}
	return []statement{
		{
			"Sid":      sidPrivate,
			"Effect":   "Allow",
			"Action":   all,
			"Resource": parameterArns(in.region, in.account, PrivateParameterPrefix),
		},
		{
			"Sid":      sidPrivate + "NotConfig",
			"Effect":   "Deny",
			"Action":   all,
			"Resource": parameterArns(in.region, in.account, ConfigParameterPrefix),
		},
	}
}

// functionPolicy is the role of one function. All three get the storage, the
// table, their secrets and the exports, the audit queue and their own logs; the
// signing key and the right to run another function's pass are the http
// function's alone.
func functionPolicy(in functionPolicyIn) (string, error) {
	st := []statement{{
		"Sid":      sidLogs,
		"Effect":   "Allow",
		"Action":   []string{logsCreateStream, logsPutEvents},
		"Resource": in.logGroupArn + ":*",
	}}
	st = append(st, storageStatements(in.bucketArn)...)
	st = append(st, stateStatements(in.tableArn, in.tableKey)...)
	st = append(st, privateStatements(in)...)
	st = append(st, statement{
		"Sid":      sidExport,
		"Effect":   "Allow",
		"Action":   []string{ssmPutParameter, ssmDeleteParameter},
		"Resource": parameterArns(in.region, in.account, ExportParameterPrefix),
	})
	st = append(st, parameterKeyStatements(in.parameterKeyArn)...)
	st = append(st, statement{
		"Sid":      sidAudit,
		"Effect":   "Allow",
		"Action":   sqsSendMessage,
		"Resource": in.queueArn,
	})
	if in.role == RoleHTTP {
		if len(in.signingKeyArns) > 0 {
			st = append(st, signingStatement(in.signingKeyArns))
		}
		if in.wrappedKeyArn != "" {
			st = append(st, wrappedSigningStatement(in.wrappedKeyArn))
		}
		st = append(st, statement{
			"Sid":      sidInvoke,
			"Effect":   "Allow",
			"Action":   lambdaInvokeFunction,
			"Resource": in.invokeFunctionArns,
		})
	}
	if in.role != RoleHTTP {
		st = append(st, keyringWriteDenial(in.tableArn))
	}
	if in.webIdentity && in.role != RoleHTTP {
		st = append(st, webIdentityStatement(in.webIdentityAud))
	}
	return document(st)
}

// webIdentityStatement lets a controller ask STS for its role's outbound web
// identity token, which is how it authenticates to the console. The action
// takes no resource, so the statement is on "*" (the one such grant here). With
// an audience the request must carry it and no other: the key is multi-valued
// (the API takes a list), so it is ForAllValues:StringEquals, which also passes
// an empty set and is safe only because the audience is a required parameter.
func webIdentityStatement(audience string) statement {
	st := statement{
		"Sid":      sidWebID,
		"Effect":   "Allow",
		"Action":   stsGetWebIdentityToken,
		"Resource": "*",
	}
	if audience != "" {
		st["Condition"] = map[string]any{
			"ForAllValues:StringEquals": map[string]any{"sts:IdentityTokenAudience": []string{audience}},
		}
	}
	return st
}

// ExportReadPolicy is the IAM policy document a consumer's External Secrets
// Operator role attaches: read on /sluis/export/* and nothing else (and, with a
// customer-managed parameter key, its decryption through SSM only).
func ExportReadPolicy(region, account, parameterKeyArn string) (string, error) {
	st := []statement{{
		"Sid":      sidExport,
		"Effect":   "Allow",
		"Action":   []string{ssmGetParameter, ssmGetParameters, ssmGetParametersByPath},
		"Resource": parameterArns(region, account, ExportParameterPrefix),
	}}
	if parameterKeyArn != "" {
		st = append(st, statement{
			"Sid":      sidParamKy,
			"Effect":   "Allow",
			"Action":   []string{kmsDecrypt},
			"Resource": parameterKeyArn,
			"Condition": map[string]any{
				"StringLike": map[string]any{"kms:ViaService": "ssm.*.amazonaws.com"},
			},
		})
	}
	return document(st)
}
