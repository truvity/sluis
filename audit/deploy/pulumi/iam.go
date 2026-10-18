package auditpulumi

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// A policy document is built from ARNs that exist only once the resources do, so
// each builder takes them as strings and returns the JSON; audit.go calls them
// inside an Apply. Keeping them pure is what lets the tests read the statements
// the roles get.

type statement map[string]any

func policyJSON(statements ...statement) string {
	b, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		panic(err) // maps of strings
	}
	return string(b)
}

func allow(actions []string, resources []string, condition map[string]any) statement {
	s := statement{"Effect": "Allow", "Action": actions, "Resource": resources}
	if condition != nil {
		s["Condition"] = condition
	}
	return s
}

// bucketGrant is one AWS preset's bucket as the policies grant it: the bucket,
// the prefix every key of the preset lives under, and whether the bucket is under
// Object Lock (which adds the retention permissions).
type bucketGrant struct {
	Arn    string
	Prefix string
	Locked bool
}

// under names the objects below the given prefixes of the preset's own prefix.
func (b bucketGrant) under(prefixes ...string) []string {
	out := make([]string, len(prefixes))
	for i, p := range prefixes {
		out[i] = b.Arn + "/" + b.Prefix + p + "*"
	}
	return out
}

// list is s3:ListBucket on the bucket, scoped to the preset's prefix when it has
// one (or to the given prefixes below it).
func (b bucketGrant) list(prefixes ...string) statement {
	var cond map[string]any
	switch {
	case len(prefixes) > 0:
		like := make([]string, len(prefixes))
		for i, p := range prefixes {
			like[i] = b.Prefix + p + "*"
		}
		cond = map[string]any{"StringLike": map[string]any{"s3:prefix": like}}
	case b.Prefix != "":
		cond = map[string]any{"StringLike": map[string]any{"s3:prefix": []string{b.Prefix + "*"}}}
	}
	return allow([]string{"s3:ListBucket"}, []string{b.Arn}, cond)
}

// The prefixes of the bucket contract (docs/reference/audit/bucket-contract.md) a part
// writes. The writer writes everything but seals and keys; the notary writes
// those two and nothing else.
const recordsPrefix = "records/"

// writeOnce is the condition of a put that may not replace an object: the
// request carries If-None-Match: * (the s3:if-none-match condition key), which S3
// answers with 412 for a key that exists.
var writeOnce = map[string]any{"StringEquals": map[string]any{"s3:if-none-match": "*"}}

var (
	writerPrefixes = []string{recordsPrefix, "catalogue/", "schema/", "identity/", "dlq/"}
	sealPrefixes   = []string{"seals/", "keys/"}
)

// assumeRoleJSON lets a service principal assume a role.
func assumeRoleJSON(service string) string {
	return policyJSON(statement{
		"Effect": "Allow", "Principal": map[string]any{"Service": service}, "Action": "sts:AssumeRole",
	})
}

// schedulerTrust lets EventBridge Scheduler assume a role for ONE schedule of the
// account: aws:SourceAccount holds it to the account's schedules, and aws:SourceArn
// to the schedule the role is for (the default group's `schedule/default/<name>`).
// With no condition at all any schedule of any account could ask the role to
// invoke its function.
func schedulerTrust(region, account, scheduleName string) string {
	return policyJSON(statement{
		"Effect": "Allow", "Principal": map[string]any{"Service": "scheduler.amazonaws.com"}, "Action": "sts:AssumeRole",
		"Condition": map[string]any{
			"StringEquals": map[string]any{"aws:SourceAccount": account},
			"ArnEquals": map[string]any{
				"aws:SourceArn": "arn:" + "aws:scheduler:" + region + ":" + account + ":schedule/default/" + scheduleName,
			},
		},
	})
}

// logsStatement is the function's own log group, and nothing else.
func logsStatement(logGroupArn string) statement {
	return allow([]string{"logs:CreateLogStream", "logs:PutLogEvents"}, []string{logGroupArn + ":*"}, nil)
}

// webIdentityStatement lets a function role ask STS for the identity token the
// OTLP extension exchanges. sts:IdentityTokenAudience is a multi-valued key (the
// API takes a list of audiences), so it needs ForAllValues:StringEquals: a plain
// StringEquals is an implicit deny when the request carries the audience as a
// list. ForAllValues also passes on an empty set, which is safe here only
// because Audience is a required parameter of GetWebIdentityToken. The signing
// algorithm and the lifetime are pinned to what the extension asks for.
func webIdentityStatement(audience string) statement {
	return allow([]string{"sts:GetWebIdentityToken"}, []string{"*"}, map[string]any{
		"ForAllValues:StringEquals": map[string]any{"sts:IdentityTokenAudience": []string{audience}},
		"StringEquals":              map[string]any{"sts:SigningAlgorithm": "ES384"},
		"NumericLessThanEquals":     map[string]any{"sts:DurationSeconds": "300"},
	})
}

// writerPolicy is what the writer function may do, and no more, on each AWS
// preset's bucket (scoped to its prefix).
//
// PutObjectRetention and PutObjectLegalHold are needed by PutObject itself: S3
// refuses a put that carries an Object Lock header unless the caller also holds
// the matching permission. The legal hold is placed by the put of a record whose
// tenant is under a hold (roll.go sets LegalHold from the holds the writer reads),
// so the writer needs it; the notary, the readers and the archive writer do not
// have it. The put into records/ must carry If-None-Match: * (writeOnce). The writer reads the catalogue it compares at
// start-up, the legal holds, and the archive an addendum scans; it lists the
// bucket for the last two. It has no delete, no write to seals/ or keys/ (it reads
// seals/ to see whether an hour is sealed, so that it never puts an object into
// one), and no KMS Sign: whoever can write the archive and can also sign for it can choose
// what to sign (ADR 0061). When its configuration names secrets it may read the
// SSM parameters under its root and decrypt them, and read nothing else of SSM.
// Only the attested preset's bucket is locked (bucketGrant.Locked); the writer
// sends no lock header to any other, so it is granted neither permission there.
// A preset at an endpoint has no S3 statement at all: the store is reached with
// the credentials in the state store, which extra grants the reading of.
// extra is the grants on the installation's keys and state store.
func writerPolicy(buckets []bucketGrant, archiveKeyArns []string, tableArn, queueArn, logGroupArn string, audience string,
	secrets *secretGrant, extra []statement) string {
	var st []statement
	for _, b := range buckets {
		put := []string{"s3:PutObject"}
		if b.Locked {
			put = append(put, "s3:PutObjectRetention", "s3:PutObjectLegalHold")
		}
		// A record is written once: the store sends If-None-Match: * on every put, and
		// the policy requires it for records/, so that a writer (or a role that stole its
		// credentials) can only add an object under records/, never replace one, even
		// in a bucket with no Object Lock (S3 then refuses the put with 412).
		var others []string
		for _, p := range writerPrefixes {
			if p != recordsPrefix {
				others = append(others, p)
			}
		}
		st = append(st,
			allow(put, b.under(recordsPrefix), writeOnce),
			allow(put, b.under(others...), nil),
			allow([]string{"s3:GetObject"}, b.under(append(append([]string{}, writerPrefixes...), "holds/", "seals/")...), nil),
			b.list(),
		)
	}
	st = append(st,
		allow([]string{"dynamodb:GetItem", "dynamodb:BatchGetItem", "dynamodb:PutItem"}, []string{tableArn}, nil),
		allow([]string{"sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes", "sqs:ChangeMessageVisibility"},
			[]string{queueArn}, nil),
		logsStatement(logGroupArn),
	)
	st = append(st, archiveKeyStatements(archiveKeyArns, "kms:GenerateDataKey", "kms:Decrypt")...)
	st = append(st, secrets.statements()...)
	st = append(st, extra...)
	if audience != "" {
		st = append(st, webIdentityStatement(audience))
	}
	return policyJSON(st...)
}

// archiveKeyStatements is the grant on the archive keys, and nothing when there
// are none (Archive.Encryption "s3", the AWS-managed key, or every preset at an
// endpoint). The keys are the archive key and the keys behind the presets'
// aliases, each once. S3 binds its own encryption context (the object's bucket),
// not the storage port's, so the grant has no encryption-context condition.
func archiveKeyStatements(arns []string, actions ...string) []statement {
	arns = dedupe(arns)
	if len(arns) == 0 {
		return nil
	}
	return []statement{allow(actions, arns, nil)}
}

// notaryPolicy is what the notary function may do: read the records it seals and
// the seals it chains to, put seals and keys/roots.jwks, and sign with the seal
// key and with nothing else. It cannot put a record, which is what makes a
// compromised writer unable to seal what it wrote.
func notaryPolicy(buckets []bucketGrant, archiveKeyArns []string, sealKeyArn, logGroupArn string, audience string, extra []statement) string {
	var st []statement
	for _, b := range buckets {
		put := []string{"s3:PutObject"}
		if b.Locked {
			put = append(put, "s3:PutObjectRetention")
		}
		st = append(st,
			allow([]string{"s3:GetObject"}, b.under("records/", "seals/", "keys/"), nil),
			b.list(),
			allow(put, b.under(sealPrefixes...), nil),
		)
	}
	st = append(st,
		allow([]string{"kms:Sign", "kms:GetPublicKey", "kms:DescribeKey"}, []string{sealKeyArn}, nil),
		logsStatement(logGroupArn),
	)
	st = append(st, archiveKeyStatements(archiveKeyArns, "kms:GenerateDataKey", "kms:Decrypt")...)
	st = append(st, extra...)
	if audience != "" {
		st = append(st, webIdentityStatement(audience))
	}
	return policyJSON(st...)
}

// observeReaderPolicy is what audit-observe reads the archive with, across the
// accounts or from a cluster: list and get on records/, catalogue/, schema/,
// seals/ and keys/ of every AWS preset's bucket, and decrypt under the archive
// keys. It writes nothing, which is what ADR 0062 means by observe following the
// bucket.
func observeReaderPolicy(buckets []bucketGrant, archiveKeyArns []string) string {
	return policyJSON(readStatements(buckets, archiveKeyArns)...)
}

// queryPolicy is what audit-query may do: the read of observeReaderPolicy, and,
// when queueArn is not empty, sqs:SendMessage on it and nothing else of SQS (the
// service records every read of the trail through the ingest queue).
func queryPolicy(buckets []bucketGrant, archiveKeyArns []string, queueArn string) string {
	st := readStatements(buckets, archiveKeyArns)
	if queueArn != "" {
		st = append(st, allow([]string{"sqs:SendMessage"}, []string{queueArn}, nil))
	}
	return policyJSON(st...)
}

func readStatements(buckets []bucketGrant, archiveKeyArns []string) []statement {
	prefixes := []string{"records/", "catalogue/", "schema/", "seals/", "keys/"}
	var st []statement
	for _, b := range buckets {
		st = append(st,
			allow([]string{"s3:GetObject"}, b.under(prefixes...), nil),
			b.list(prefixes...),
		)
	}
	return append(st, archiveKeyStatements(archiveKeyArns, "kms:Decrypt")...)
}

// archiveWriterPolicy is what a workload outside AWS that writes part of the
// archive may do: put under the given prefixes only (with the retention
// permission in a locked bucket), read what it must chain to or compare
// (records/ and the prefixes it writes), list, and use the archive keys. It has
// no delete, no legal hold, no seal key, and no queue or table.
func archiveWriterPolicy(buckets []bucketGrant, archiveKeyArns []string, prefixes []string) string {
	var st []statement
	for _, b := range buckets {
		put := []string{"s3:PutObject"}
		if b.Locked {
			put = append(put, "s3:PutObjectRetention")
		}
		reads := append([]string{"records/"}, prefixes...)
		st = append(st,
			allow(put, b.under(prefixes...), nil),
			allow([]string{"s3:GetObject"}, b.under(dedupe(reads)...), nil),
			b.list(),
		)
	}
	st = append(st, archiveKeyStatements(archiveKeyArns, "kms:GenerateDataKey", "kms:Decrypt")...)
	return policyJSON(st...)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// trustPolicy lets the given principal (when there is one) assume a role, with an
// external id when there is one, and the one ServiceAccount of irsa (when there
// is one) assume it by web identity.
func trustPolicy(principalArn, externalID string, irsa *IRSAArgs, providerArn string, extra ...statement) string {
	var st []statement
	if principalArn != "" {
		s := statement{"Effect": "Allow", "Principal": map[string]any{"AWS": principalArn}, "Action": "sts:AssumeRole"}
		if externalID != "" {
			s["Condition"] = map[string]any{"StringEquals": map[string]any{"sts:ExternalId": externalID}}
		}
		st = append(st, s)
	}
	if irsa != nil {
		st = append(st, irsaTrustStatement(*irsa, providerArn))
	}
	st = append(st, extra...)
	return policyJSON(st...)
}

// irsaTrustStatement is the web identity trust of one ServiceAccount. BOTH
// conditions matter: without the sub pin any ServiceAccount of the cluster could
// assume the role, and without the aud pin a token minted for another audience
// would be accepted.
func irsaTrustStatement(i IRSAArgs, providerArn string) statement {
	return statement{
		"Effect": "Allow", "Principal": map[string]any{"Federated": providerArn}, "Action": "sts:AssumeRoleWithWebIdentity",
		"Condition": map[string]any{"StringEquals": map[string]any{
			i.IssuerHost + ":aud": i.Audience,
			i.IssuerHost + ":sub": "system:serviceaccount:" + i.Namespace + ":" + i.ServiceAccount,
		}},
	}
}

var clusterArnRE = regexp.MustCompile(`^arn:[a-z-]+:eks:[a-z0-9-]+:([0-9]{12}):cluster/[A-Za-z0-9][A-Za-z0-9_-]*$`)

// podIdentityTrustStatement is the EKS Pod Identity trust of one ServiceAccount.
// EKS assumes the role as pods.eks.amazonaws.com and stamps the cluster's ARN, its
// account and the pod's namespace and ServiceAccount on the request: every one of
// them is pinned, so that neither another cluster nor another ServiceAccount of
// this one can assume the role. The account is read from the cluster's ARN.
func podIdentityTrustStatement(clusterArn, namespace, serviceAccount string) (statement, error) {
	m := clusterArnRE.FindStringSubmatch(clusterArn)
	if m == nil {
		return nil, fmt.Errorf("auditpulumi: PodIdentity.ClusterArn %q is not the ARN of an EKS cluster (arn:aws:eks:<region>:<account>:cluster/<name>)", clusterArn)
	}
	return statement{
		"Effect": "Allow", "Principal": map[string]any{"Service": "pods.eks.amazonaws.com"},
		"Action": []string{"sts:AssumeRole", "sts:TagSession"},
		"Condition": map[string]any{
			"StringEquals": map[string]any{
				"aws:SourceAccount":                         m[1],
				"aws:RequestTag/kubernetes-namespace":       namespace,
				"aws:RequestTag/kubernetes-service-account": serviceAccount,
			},
			"ArnEquals": map[string]any{"aws:SourceArn": clusterArn},
		},
	}, nil
}

// invokePolicy is what the scheduler's role may do: invoke the notary's live
// alias, and no other version.
func invokePolicy(functionArn string) string {
	return policyJSON(allow([]string{"lambda:InvokeFunction"}, []string{functionArn}, nil))
}

// SealKeyPolicy is the key policy the estate gives the seal key. The library
// creates no key; this is the policy it would have been created with, for the
// estate to put on its own. The default policy hands the key to IAM, so any
// principal in the account with a kms:Sign allow could sign seals; this one does
// not. Only the notary's role signs: the account's root is given no kms:Sign,
// and kms:Sign is denied to every other principal outright.
//
// The root's delegation is the key's upkeep (describe, tags, aliases, rotation)
// and not its control plane. The key policy, grants, disabling and deletion
// (KeyControlPlaneActions) are the administrators' alone: the root allows them
// to the named roles and a Deny takes them from everyone else, so a role that
// has a kms:* or kms:Put* allow in IAM (a power user, an automation or StackSet
// role) cannot rewrite the policy to give itself kms:Sign and forge a seal. An
// earlier version of this policy let the root create grants and put policies and
// its comment said the administrator cannot use the key; the administrator could,
// by changing the policy first.
//
// admins are role ARNs (a `*` only in the hash of an SSO role's name, see
// CheckKeyAdmin), at least one: the role that applies the stack and the
// break-glass role. They can still change the policy, and with it the notary: a
// change of who signs is theirs to make, and CloudTrail records it.
//
// The notary's role ARN is
// arn:<partition>:iam::<account>:role<RolePath><name>-notary, which the estate
// can write before the installation exists.
func SealKeyPolicy(accountRootArn, notaryRoleArn string, admins []string) (string, error) {
	return sealKeyPolicy(accountRootArn, notaryRoleArn, admins)
}

// sealKeyPolicy is SealKeyPolicy.
func sealKeyPolicy(accountRootArn, notaryRoleArn string, admins []string) (string, error) {
	exempt, err := keyAdminSet("SealKeyPolicy admins", admins)
	if err != nil {
		return "", err
	}
	st := []statement{
		{
			"Sid": "Administer", "Effect": "Allow", "Principal": map[string]any{"AWS": accountRootArn},
			"Action": append([]string(nil), keyAdministerActions...), "Resource": "*",
		},
		{
			"Sid": "SealWithTheNotaryOnly", "Effect": "Allow", "Principal": map[string]any{"AWS": notaryRoleArn},
			"Action": []string{"kms:Sign", "kms:GetPublicKey", "kms:DescribeKey"}, "Resource": "*",
		},
	}
	st = append(st, controlPlane(accountRootArn, exempt)...)
	st = append(st, statement{
		"Sid": "NoSealButTheNotary", "Effect": "Deny", "Principal": map[string]any{"AWS": "*"},
		"Action": "kms:Sign", "Resource": "*",
		"Condition": map[string]any{"ArnNotEquals": map[string]any{"aws:PrincipalArn": notaryRoleArn}},
	})
	return policyJSON(st...), nil
}

// ArchiveLockGuard is the statement of an archive bucket's policy that keeps the
// lock to the break-glass roles: a Deny of s3:BypassGovernanceRetention,
// s3:DeleteObjectVersion and s3:PutBucketObjectLockConfiguration on the bucket and
// its objects to every principal not matched by breakglass (role ARNs, validated
// by Args). The writer, the notary and the readers use none of these actions, and
// the lifecycle's expiry is S3's own.
func ArchiveLockGuard(bucketArn string, breakglass []string) map[string]any {
	return statement{
		"Sid": "LockBreakglassOnly", "Effect": "Deny", "Principal": "*",
		"Action":    []string{"s3:BypassGovernanceRetention", "s3:DeleteObjectVersion", "s3:PutBucketObjectLockConfiguration"},
		"Resource":  []string{bucketArn, bucketArn + "/*"},
		"Condition": map[string]any{"ArnNotLike": map[string]any{"aws:PrincipalArn": dedupe(sortedCopy(breakglass))}},
	}
}

// ArchiveKeyPolicyArgs are the principals and the bucket of the archive grants
// on a shared key's policy.
type ArchiveKeyPolicyArgs struct {
	// AccountID is the 12-digit account of the key; its root is the principal of
	// every statement, narrowed by aws:PrincipalArn.
	AccountID string
	// Region is the region of the archive bucket (kms:ViaService s3.<region>).
	Region string
	// BucketArn is the archive bucket's ARN, `arn:aws:s3:::<bucket>`. S3 puts it
	// (with a bucket key) or the object's ARN in the encryption context.
	BucketArn string
	// Roles are the roles that write: GenerateDataKey, Decrypt and DescribeKey
	// (the writer and the notary), as exact ARNs.
	Roles []string
	// Readers are the roles that read: Decrypt and DescribeKey (observe and
	// query), as exact ARNs.
	Readers []string
	// Operator is the operator's role as an ARN pattern (`*` allowed, as for an
	// SSO role with a hash suffix). It may do what Roles may, to rewrite an
	// object under the key.
	Operator string
}

// ArchiveKeyPolicyStatements are the archive purpose of a shared symmetric key,
// for the key's policy: the three statements `AuditReadersOpenTheArchiveThroughS3`,
// `AuditRolesUseTheArchiveThroughS3` and `OperatorRewritesTheArchiveThroughS3`,
// in that order. Each is an Allow for the account root narrowed by
// aws:PrincipalArn, through S3 only and for the archive bucket's context only,
// so the roles need not exist when the key is made. The roles' own IAM
// policies, which the library writes, are what grant the calls.
func ArchiveKeyPolicyStatements(a ArchiveKeyPolicyArgs) ([]map[string]any, error) {
	switch {
	case !accountIDRE.MatchString(a.AccountID):
		return nil, fmt.Errorf("auditpulumi: ArchiveKeyPolicyArgs.AccountID %q must be a 12-digit account id", a.AccountID)
	case a.Region == "":
		return nil, errors.New("auditpulumi: ArchiveKeyPolicyArgs.Region is required")
	case !strings.HasPrefix(a.BucketArn, "arn:aws:s3:::"):
		return nil, fmt.Errorf("auditpulumi: ArchiveKeyPolicyArgs.BucketArn %q must be arn:aws:s3:::<bucket>", a.BucketArn)
	case len(a.Roles) == 0 || len(a.Readers) == 0:
		return nil, errors.New("auditpulumi: ArchiveKeyPolicyArgs.Roles and Readers are required")
	case a.Operator == "":
		return nil, errors.New("auditpulumi: ArchiveKeyPolicyArgs.Operator is required")
	}
	root := map[string]any{"AWS": "arn:" + "aws:iam::" + a.AccountID + ":root"}
	viaS3 := map[string]any{"kms:ViaService": "s3." + a.Region + ".amazonaws.com"}
	context := map[string]any{"kms:EncryptionContext:aws:s3:arn": []string{a.BucketArn, a.BucketArn + "/*"}}
	actions := []string{"kms:GenerateDataKey", "kms:Decrypt", "kms:DescribeKey"}
	readActions := []string{"kms:Decrypt", "kms:DescribeKey"}
	return []map[string]any{
		{
			"Sid": "AuditReadersOpenTheArchiveThroughS3", "Effect": "Allow", "Principal": root,
			"Action": readActions, "Resource": "*",
			"Condition": map[string]any{
				"ArnEquals":    map[string]any{"aws:PrincipalArn": append([]string{}, a.Readers...)},
				"StringEquals": viaS3, "StringLike": context,
			},
		},
		{
			"Sid": "AuditRolesUseTheArchiveThroughS3", "Effect": "Allow", "Principal": root,
			"Action": actions, "Resource": "*",
			"Condition": map[string]any{
				"ArnEquals":    map[string]any{"aws:PrincipalArn": append([]string{}, a.Roles...)},
				"StringEquals": viaS3, "StringLike": context,
			},
		},
		{
			"Sid": "OperatorRewritesTheArchiveThroughS3", "Effect": "Allow", "Principal": root,
			"Action": actions, "Resource": "*",
			"Condition": map[string]any{
				"ArnLike":      map[string]any{"aws:PrincipalArn": a.Operator},
				"StringEquals": viaS3, "StringLike": context,
			},
		},
	}, nil
}
