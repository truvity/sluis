package sluispulumi

import (
	"errors"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// StorageType is the Pulumi type token of the Storage component.
const StorageType = "sluis:aws:Storage"

// StorageArgs is the blob bucket.
type StorageArgs struct {
	// BucketName is the bucket's name. Required: it is in the processes'
	// configuration (`ports.blob.s3.bucket`), so it is known before anything is
	// created, and a bucket name is global.
	BucketName string

	// Versioning turns on S3 versioning. Default off. The bucket holds the
	// controllers' last reports and the directory snapshots, which the next tick
	// regenerates and which are never a credential, and an ETag is the
	// compare-and-swap's version, so an unversioned bucket is the shape in use.
	Versioning bool

	// Tags are put on the bucket. Default none.
	Tags map[string]string
}

func (a *StorageArgs) validate() (StorageArgs, error) {
	if a == nil {
		return StorageArgs{}, errors.New("sluispulumi: StorageArgs is nil")
	}
	out := *a
	if out.BucketName == "" {
		return out, errors.New("sluispulumi: StorageArgs.BucketName is required")
	}
	return out, nil
}

// Storage is the component. Its fields are the outputs.
type Storage struct {
	pulumi.ResourceState

	// BucketName and BucketArn are the blob bucket.
	BucketName pulumi.StringOutput
	BucketArn  pulumi.StringOutput
}

// StorageGrant is what a policy needs to name the storage: the ARN of the bucket.
type StorageGrant struct {
	BucketArn pulumi.StringInput
}

// Grant is the storage as KubernetesIdentityArgs.Storage and LambdaArgs.Storage take it.
func (s *Storage) Grant() *StorageGrant {
	return &StorageGrant{BucketArn: s.BucketArn}
}

func tagMap(t map[string]string) pulumi.StringMapInput {
	if len(t) == 0 {
		return nil
	}
	return pulumi.ToStringMap(t)
}

// NewStorage creates the component and everything under it.
//
// The bucket is encrypted with S3-managed keys, closed to the public and to
// plain HTTP, and protected. There is no key: sealing is retired, and a key that
// an earlier release created is scheduled for deletion by the next apply.
//
// The AWS provider is the caller's to choose: pass pulumi.Providers(p) or
// pulumi.Provider(p) as an option.
func NewStorage(ctx *pulumi.Context, name string, args *StorageArgs, opts ...pulumi.ResourceOption) (*Storage, error) {
	a, err := args.validate()
	if err != nil {
		return nil, err
	}
	out := &Storage{}
	if err := ctx.RegisterComponentResource(StorageType, name, out, opts...); err != nil {
		return nil, err
	}
	child := pulumi.Parent(out)
	tags := tagMap(a.Tags)

	bucket, err := s3.NewBucket(ctx, name+"-bucket", &s3.BucketArgs{
		Bucket: pulumi.String(a.BucketName),
		Tags:   tags,
	}, child, pulumi.Protect(true))
	if err != nil {
		return nil, fmt.Errorf("sluis bucket: %w", err)
	}

	if _, err := s3.NewBucketServerSideEncryptionConfigurationV2(ctx, name+"-bucket-encryption",
		&s3.BucketServerSideEncryptionConfigurationV2Args{
			Bucket: bucket.ID(),
			Rules: s3.BucketServerSideEncryptionConfigurationV2RuleArray{
				&s3.BucketServerSideEncryptionConfigurationV2RuleArgs{
					ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationV2RuleApplyServerSideEncryptionByDefaultArgs{
						SseAlgorithm: pulumi.String("AES256"),
					},
				},
			},
		}, child); err != nil {
		return nil, fmt.Errorf("sluis bucket encryption: %w", err)
	}

	publicBlock, err := s3.NewBucketPublicAccessBlock(ctx, name+"-bucket-public-access", &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}, child)
	if err != nil {
		return nil, fmt.Errorf("sluis bucket public access block: %w", err)
	}

	// S3 takes one policy per bucket; this is it. After the public-access block
	// so the two never race on the bucket.
	policy := bucket.Arn.ApplyT(func(arn string) (string, error) {
		return document([]statement{{
			"Sid":       "DenyPlainHTTP",
			"Effect":    "Deny",
			"Principal": "*",
			"Action":    "s3:*",
			"Resource":  []string{arn, arn + "/*"},
			"Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "false"}},
		}})
	}).(pulumi.StringOutput)
	if _, err := s3.NewBucketPolicy(ctx, name+"-bucket-policy", &s3.BucketPolicyArgs{
		Bucket: bucket.ID(),
		Policy: policy,
	}, child, pulumi.DependsOn([]pulumi.Resource{publicBlock})); err != nil {
		return nil, fmt.Errorf("sluis bucket policy: %w", err)
	}

	if a.Versioning {
		if _, err := s3.NewBucketVersioningV2(ctx, name+"-bucket-versioning", &s3.BucketVersioningV2Args{
			Bucket: bucket.ID(),
			VersioningConfiguration: &s3.BucketVersioningV2VersioningConfigurationArgs{
				Status: pulumi.String("Enabled"),
			},
		}, child); err != nil {
			return nil, fmt.Errorf("sluis bucket versioning: %w", err)
		}
	}

	out.BucketName = bucket.Bucket
	out.BucketArn = bucket.Arn
	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{
		"bucketName": out.BucketName,
		"bucketArn":  out.BucketArn,
	}); err != nil {
		return nil, err
	}
	return out, nil
}
