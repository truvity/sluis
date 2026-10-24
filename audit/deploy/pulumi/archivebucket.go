package auditpulumi

import (
	"errors"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// ArchiveBucketArgs is an archive bucket the ESTATE declares itself, with the
// settings the library gives a bucket it creates (versioning, SSE-KMS under one
// key, no public access, owner-enforced, TLS only) and the archive's lifecycle
// without the `security` framework's expiry.
//
// New takes an existing bucket (a preset without Create) and creates nothing for
// it. NewArchiveBucket is for the estate that owns the bucket and wants those
// settings declared, under its own names: an installation older than the
// library's keys-by-purpose shape made them as children of its component, and
// the Aliases hook keeps every URN and physical name.
type ArchiveBucketArgs struct {
	// BucketName is the bucket's physical name. Required.
	BucketName string
	// KeyArn is the KMS key (key ARN, not an alias ARN) new objects are
	// encrypted with by default. Required.
	KeyArn string
	// Profiles are the profiles whose records are kept: one lifecycle rule per
	// profile, `records-<profile>`, on the prefix `records/<profile>/`, with
	// Glacier Instant Retrieval and Deep Archive transitions and no expiry.
	// Required.
	Profiles []string
	// GlacierIRDays, DeepArchiveDays and MultipartDays are the lifecycle's days.
	// Defaults 30, 365 and 7 (abort of incomplete multipart uploads).
	GlacierIRDays, DeepArchiveDays, MultipartDays int
	// Tags are put on the bucket.
	Tags map[string]string
	// BreakglassRoleArns are the roles that alone may bypass the bucket's lock:
	// set, the policy carries ArchiveLockGuard beside the TLS-only statement, so
	// that a role with a broad S3 allow cannot delete a version of the archive
	// or change its lock. Role ARNs (a `*` only in the hash of an SSO role's
	// name, see CheckKeyAdmin); include the role that applies the stack.
	// Optional: without it the policy is the TLS-only statement alone, as before
	// v1.78.2, and the program logs a warning naming the gap.
	BreakglassRoleArns []string
	// Aliases returns the [pulumi.Alias] values of a resource, by its AWS type
	// token (the Archive*Type constants), for resources whose URN is being moved
	// (a new parent, a rename). nil adds none.
	Aliases func(resourceType string) []pulumi.Alias
}

// ArchiveBucket is the bucket NewArchiveBucket declared.
type ArchiveBucket struct {
	Bucket *s3.Bucket
	// Policy is the bucket policy text.
	Policy string
}

// The AWS type tokens of the resources NewArchiveBucket declares, as the
// Aliases hook receives them.
const (
	ArchiveBucketType      = "aws:s3/bucket:Bucket"
	ArchiveVersioningType  = "aws:s3/bucketVersioning:BucketVersioning"
	ArchiveEncryptionType  = "aws:s3/bucketServerSideEncryptionConfiguration:BucketServerSideEncryptionConfiguration"
	ArchivePublicBlockType = "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock"
	ArchiveOwnershipType   = "aws:s3/bucketOwnershipControls:BucketOwnershipControls"
	ArchivePolicyType      = "aws:s3/bucketPolicy:BucketPolicy"
	ArchiveLifecycleType   = "aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration"
)

// NewArchiveBucket declares the bucket and its six settings, each named `name`
// (the type token tells them apart in the URN), under opts (the provider, the
// parent). The bucket is protected and never force-destroyed.
func NewArchiveBucket(ctx *pulumi.Context, name string, args *ArchiveBucketArgs, opts ...pulumi.ResourceOption) (*ArchiveBucket, error) {
	if args == nil {
		return nil, errors.New("auditpulumi: ArchiveBucketArgs is required")
	}
	if name == "" {
		return nil, errors.New("auditpulumi: ArchiveBucket name is required")
	}
	if args.BucketName == "" {
		return nil, errors.New("auditpulumi: ArchiveBucketArgs.BucketName is required")
	}
	if !keyArn.MatchString(args.KeyArn) {
		return nil, fmt.Errorf("auditpulumi: ArchiveBucketArgs.KeyArn %q is not a KMS key ARN (arn:<partition>:kms:<region>:<account>:key/<id>)", args.KeyArn)
	}
	if len(args.Profiles) == 0 {
		return nil, errors.New("auditpulumi: ArchiveBucketArgs.Profiles is required: a lifecycle rule is made per profile")
	}
	if err := checkKeyAdmins("ArchiveBucketArgs.BreakglassRoleArns", args.BreakglassRoleArns); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, p := range args.Profiles {
		if p == "" || seen[p] {
			return nil, fmt.Errorf("auditpulumi: ArchiveBucketArgs.Profiles has %q: names must be non-empty and unique", p)
		}
		seen[p] = true
	}
	glacier, deep, multipart := orInt(args.GlacierIRDays, 30), orInt(args.DeepArchiveDays, 365), orInt(args.MultipartDays, 7)
	if deep <= glacier {
		return nil, fmt.Errorf("auditpulumi: ArchiveBucketArgs.DeepArchiveDays (%d) must be after GlacierIRDays (%d)", deep, glacier)
	}

	with := func(typ string, extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
		o := append([]pulumi.ResourceOption{}, opts...)
		if args.Aliases != nil {
			if a := args.Aliases(typ); len(a) > 0 {
				o = append(o, pulumi.Aliases(a))
			}
		}
		return append(o, extra...)
	}
	fail := func(what string, err error) (*ArchiveBucket, error) {
		return nil, fmt.Errorf("auditpulumi: archive bucket %s: %w", what, err)
	}

	bucketArn := "arn:aws:s3:::" + args.BucketName
	bucket, err := s3.NewBucket(ctx, name, &s3.BucketArgs{
		Bucket:       pulumi.String(args.BucketName),
		ForceDestroy: pulumi.Bool(false),
		Tags:         pulumi.ToStringMap(args.Tags),
	}, with(ArchiveBucketType, pulumi.Protect(true))...)
	if err != nil {
		return fail("bucket", err)
	}

	versioning, err := s3.NewBucketVersioning(ctx, name, &s3.BucketVersioningArgs{
		Bucket:                  bucket.ID(),
		VersioningConfiguration: &s3.BucketVersioningVersioningConfigurationArgs{Status: pulumi.String("Enabled")},
	}, with(ArchiveVersioningType)...)
	if err != nil {
		return fail("versioning", err)
	}

	if _, err := s3.NewBucketServerSideEncryptionConfiguration(ctx, name, &s3.BucketServerSideEncryptionConfigurationArgs{
		Bucket: bucket.ID(),
		Rules: s3.BucketServerSideEncryptionConfigurationRuleArray{
			&s3.BucketServerSideEncryptionConfigurationRuleArgs{
				ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationRuleApplyServerSideEncryptionByDefaultArgs{
					SseAlgorithm:   pulumi.String("aws:kms"),
					KmsMasterKeyId: pulumi.String(args.KeyArn),
				},
				BucketKeyEnabled: pulumi.Bool(true),
				// What S3 sets on every bucket: no customer-provided keys.
				BlockedEncryptionTypes: pulumi.StringArray{pulumi.String("SSE-C")},
			},
		},
	}, with(ArchiveEncryptionType)...); err != nil {
		return fail("encryption", err)
	}

	if _, err := s3.NewBucketPublicAccessBlock(ctx, name, &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}, with(ArchivePublicBlockType)...); err != nil {
		return fail("public access block", err)
	}

	if _, err := s3.NewBucketOwnershipControls(ctx, name, &s3.BucketOwnershipControlsArgs{
		Bucket: bucket.ID(),
		Rule:   &s3.BucketOwnershipControlsRuleArgs{ObjectOwnership: pulumi.String("BucketOwnerEnforced")},
	}, with(ArchiveOwnershipType)...); err != nil {
		return fail("ownership controls", err)
	}

	policy := fmt.Sprintf(`{"Statement":[{"Action":"s3:*","Condition":{"Bool":{"aws:SecureTransport":"false"}},`+
		`"Effect":"Deny","Principal":"*","Resource":["%[1]s","%[1]s/*"],"Sid":"OnlyOverTLS"}],"Version":"2012-10-17"}`, bucketArn)
	if len(args.BreakglassRoleArns) > 0 {
		policy = policyJSON(statement{
			"Sid": "OnlyOverTLS", "Effect": "Deny", "Principal": "*", "Action": "s3:*",
			"Resource":  []string{bucketArn, bucketArn + "/*"},
			"Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "false"}},
		}, ArchiveLockGuard(bucketArn, args.BreakglassRoleArns))
	} else if err := ctx.Log.Warn("auditpulumi: the archive bucket "+args.BucketName+" gets no lock guard: ArchiveBucketArgs.BreakglassRoleArns "+
		"is empty, so any role with a broad S3 allow may delete a version of the archive or change its lock. Name the break-glass "+
		"roles and the role that applies the stack", nil); err != nil {
		return nil, err
	}
	if _, err := s3.NewBucketPolicy(ctx, name, &s3.BucketPolicyArgs{
		Bucket: bucket.ID(),
		Policy: pulumi.String(policy),
	}, with(ArchivePolicyType)...); err != nil {
		return fail("policy", err)
	}

	rules := s3.BucketLifecycleConfigurationRuleArray{}
	for _, p := range args.Profiles {
		rules = append(rules, &s3.BucketLifecycleConfigurationRuleArgs{
			Id:     pulumi.String("records-" + p),
			Status: pulumi.String("Enabled"),
			Filter: &s3.BucketLifecycleConfigurationRuleFilterArgs{Prefix: pulumi.String("records/" + p + "/")},
			Transitions: s3.BucketLifecycleConfigurationRuleTransitionArray{
				&s3.BucketLifecycleConfigurationRuleTransitionArgs{Days: pulumi.Int(glacier), StorageClass: pulumi.String("GLACIER_IR")},
				&s3.BucketLifecycleConfigurationRuleTransitionArgs{Days: pulumi.Int(deep), StorageClass: pulumi.String("DEEP_ARCHIVE")},
			},
		})
	}
	rules = append(rules, &s3.BucketLifecycleConfigurationRuleArgs{
		Id:     pulumi.String("abort-incomplete-multipart-uploads"),
		Status: pulumi.String("Enabled"),
		Filter: &s3.BucketLifecycleConfigurationRuleFilterArgs{Prefix: pulumi.String("")},
		AbortIncompleteMultipartUpload: &s3.BucketLifecycleConfigurationRuleAbortIncompleteMultipartUploadArgs{
			DaysAfterInitiation: pulumi.Int(multipart),
		},
	})
	if _, err := s3.NewBucketLifecycleConfiguration(ctx, name, &s3.BucketLifecycleConfigurationArgs{
		Bucket: bucket.ID(),
		Rules:  rules,
	}, with(ArchiveLifecycleType, pulumi.DependsOn([]pulumi.Resource{versioning}))...); err != nil {
		return fail("lifecycle", err)
	}

	return &ArchiveBucket{Bucket: bucket, Policy: policy}, nil
}

func orInt(v, d int) int {
	if v != 0 {
		return v
	}
	return d
}
