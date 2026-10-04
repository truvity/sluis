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

const (
	sidSigning = "SluisSigning"
	sidLogs    = "SluisLogs"
	sidPrivate = "SluisPrivateParameters"
	sidExport  = "SluisExportParameters"
	sidParamKy = "SluisParameterKey"
	sidInvoke  = "SluisRunAPass"
	sidAudit   = "SluisAuditIngest"
	sidWebID   = "SluisWebIdentity"
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
	webIdentity        bool
	webIdentityAud     string
	parameterKeyArn    string
	logGroupArn        string
	invokeFunctionArns []string
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
	st = append(st,
		statement{
			"Sid":      sidPrivate,
			"Effect":   "Allow",
			"Action":   []string{ssmGetParameter, ssmGetParameters, ssmGetParametersByPath, ssmPutParameter, ssmDeleteParameter},
			"Resource": parameterArns(in.region, in.account, PrivateParameterPrefix),
		},
		statement{
			"Sid":      sidExport,
			"Effect":   "Allow",
			"Action":   []string{ssmPutParameter, ssmDeleteParameter},
			"Resource": parameterArns(in.region, in.account, ExportParameterPrefix),
		},
	)
	st = append(st, parameterKeyStatements(in.parameterKeyArn)...)
	st = append(st, statement{
		"Sid":      sidAudit,
		"Effect":   "Allow",
		"Action":   sqsSendMessage,
		"Resource": in.queueArn,
	})
	if in.role == RoleHTTP {
		st = append(st, signingStatement(in.signingKeyArns), statement{
			"Sid":      sidInvoke,
			"Effect":   "Allow",
			"Action":   lambdaInvokeFunction,
			"Resource": in.invokeFunctionArns,
		})
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
