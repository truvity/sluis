// Package edgecloudflare is the sluis installation's front door when
// Cloudflare is in front of it: an API Gateway custom domain with mutual TLS
// that accepts only Cloudflare's client certificate (Authenticated Origin
// Pulls), the truststore that says so, and the ACM certificate for the domain.
//
// It is a module of its own, github.com/truvity/sluis/deploy/pulumi/edge/cloudflare,
// so that the core library (github.com/truvity/sluis/deploy/pulumi) never
// depends on an edge: the core builds the function, the storage, the IAM, the
// schedules and the HTTP API, and exposes them as Lambda.FrontDoor(); this
// takes that and puts a domain in front. It needs no Cloudflare credential and
// imports no Cloudflare provider: DNS, and the zone's Authenticated Origin
// Pulls setting, are the estate's (see NewEdge).
package edgecloudflare

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/acm"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/apigatewayv2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluispulumi "github.com/truvity/sluis/deploy/pulumi"
)

// EdgeType is the Pulumi type token of the Edge component.
const EdgeType = "sluis:aws:CloudflareEdge"

// TruststorePrefix is where the truststore lives in its bucket, and
// TruststoreKey the object: ONE object, versioned, whose version the domain
// pins.
const (
	TruststorePrefix = "truststore/"
	TruststoreKey    = TruststorePrefix + "client-ca.pem"
)

// ValidationRecord is the DNS record ACM asks for to prove the domain.
type ValidationRecord struct {
	Name, Type, Value pulumi.StringOutput
}

// CertificateArgs asks for the ACM certificate to be requested, with DNS
// validation.
type CertificateArgs struct {
	// CreateValidationRecord creates the record in the estate's DNS and returns
	// its fully qualified name, which the library waits for ACM to see. The
	// edge holds no DNS credential: this is where the caller's own provider
	// (Cloudflare, Route 53, ...) is used. Required.
	CreateValidationRecord func(ctx *pulumi.Context, record ValidationRecord) (pulumi.StringInput, error)
}

// TruststoreBucketArgs is a small S3 bucket of the edge's own for the
// truststore, for an installation whose blobs are not on S3 (an S3-compatible
// store such as R2): API Gateway reads a truststore from S3 only.
type TruststoreBucketArgs struct {
	// Name is the bucket's name. Global. Required.
	Name string
	// ApplyPrincipalArns are the identities that may write the truststore: the
	// role the stack is applied with (CD), the operators' admin role, a
	// break-glass role. Every other principal is denied writes under
	// TruststorePrefix by the bucket policy. Required.
	ApplyPrincipalArns []string
}

// Args is the edge.
type Args struct {
	// FrontDoor is the installation's HTTP API: Lambda.FrontDoor(). Required.
	FrontDoor *sluispulumi.FrontDoor

	// DomainName is the custom domain. Required.
	DomainName string

	// CertificateArn is an ACM certificate for DomainName, in the function's
	// region, which the caller supplies (an Origin CA certificate imported to
	// ACM, say). Exactly one of CertificateArn and Certificate.
	CertificateArn pulumi.StringInput
	// Certificate requests the certificate here instead, with DNS validation.
	Certificate *CertificateArgs

	// TruststorePEM is the PEM bundle of the CAs a client certificate must chain
	// to: for Authenticated Origin Pulls, Cloudflare's origin-pull CA. Required.
	TruststorePEM string

	// Storage is the installation's blob bucket on S3, where the truststore goes
	// (under TruststorePrefix). The bucket must be versioned and must guard
	// that prefix (StorageArgs.Versioning and StorageArgs.ProtectedPrefixes, with
	// the apply identities): this refuses a Storage that does not, since
	// otherwise the functions, which write the bucket, could replace the
	// truststore. Exactly one of Storage and TruststoreBucket.
	Storage *sluispulumi.Storage
	// TruststoreBucket is a small bucket of the edge's own instead, for blobs on
	// an S3-compatible store.
	TruststoreBucket *TruststoreBucketArgs

	// Tags are put on what the edge creates. Default none.
	Tags map[string]string
}

func (a *Args) validate() error {
	if a == nil {
		return errors.New("edgecloudflare: Args is nil")
	}
	var missing []string
	if a.FrontDoor == nil || a.FrontDoor.APIID == nil || a.FrontDoor.StageName == nil {
		missing = append(missing, "FrontDoor")
	}
	if a.DomainName == "" {
		missing = append(missing, "DomainName")
	}
	if strings.TrimSpace(a.TruststorePEM) == "" {
		missing = append(missing, "TruststorePEM")
	}
	if len(missing) > 0 {
		return fmt.Errorf("edgecloudflare: Args: required and empty: %v", missing)
	}
	if (a.CertificateArn == nil) == (a.Certificate == nil) {
		return errors.New("edgecloudflare: Args: set exactly one of CertificateArn and Certificate")
	}
	if a.Certificate != nil && a.Certificate.CreateValidationRecord == nil {
		return errors.New("edgecloudflare: Args.Certificate.CreateValidationRecord is required")
	}
	if (a.Storage == nil) == (a.TruststoreBucket == nil) {
		return errors.New("edgecloudflare: Args: set exactly one of Storage (the blob bucket, on S3) and TruststoreBucket " +
			"(a small S3 bucket, for blobs on an S3-compatible store)")
	}
	if b := a.TruststoreBucket; b != nil {
		if b.Name == "" {
			return errors.New("edgecloudflare: Args.TruststoreBucket.Name is required")
		}
		if err := a.guard(b.ApplyPrincipalArns).Validate(); err != nil {
			return err
		}
	}
	if s := a.Storage; s != nil {
		if !s.Versioned {
			return errors.New("edgecloudflare: Args.Storage is not versioned: the domain pins the truststore's version " +
				"(StorageArgs.Versioning)")
		}
		if _, ok := guardOf(s); !ok {
			return fmt.Errorf("edgecloudflare: Args.Storage does not guard %q: the functions write the blob bucket and could replace "+
				"the truststore (StorageArgs.ProtectedPrefixes, with the apply identities)", TruststorePrefix)
		}
	}
	return nil
}

func (a *Args) guard(principals []string) sluispulumi.ProtectedPrefix {
	return sluispulumi.ProtectedPrefix{Prefix: TruststorePrefix, AllowedPrincipalArns: principals}
}

// Guard is the prefix guard to put in StorageArgs.ProtectedPrefixes when the
// truststore goes in the blob bucket.
func Guard(applyPrincipalArns ...string) sluispulumi.ProtectedPrefix {
	return sluispulumi.ProtectedPrefix{Prefix: TruststorePrefix, AllowedPrincipalArns: applyPrincipalArns}
}

func guardOf(s *sluispulumi.Storage) (sluispulumi.ProtectedPrefix, bool) {
	for _, p := range s.ProtectedPrefixes {
		if p.Prefix == TruststorePrefix || strings.HasPrefix(TruststorePrefix, p.Prefix) {
			return p, true
		}
	}
	return sluispulumi.ProtectedPrefix{}, false
}

// Edge is the component. Its fields are the outputs.
type Edge struct {
	pulumi.ResourceState

	// DomainTarget and DomainHostedZoneID are what DNS for the custom domain
	// points at (a CNAME, or an alias record).
	DomainTarget       pulumi.StringOutput
	DomainHostedZoneID pulumi.StringOutput
	// CertificateArn is the certificate the domain uses.
	CertificateArn pulumi.StringOutput
	// TruststoreBucketName, TruststoreURI and TruststoreVersion are where the
	// client-CA bundle is, and the version the domain pins.
	TruststoreBucketName pulumi.StringOutput
	TruststoreURI        pulumi.StringOutput
	TruststoreVersion    pulumi.StringOutput
}

// NewEdge creates the custom domain with mutual TLS, its mapping to the API,
// the truststore object and, as asked, the certificate and the truststore
// bucket.
//
// The domain is regional, TLS 1.2, and demands a client certificate that chains
// to the truststore. Keeping the default `execute-api` endpoint off (it is a
// way round the client certificate) is the core's: LambdaArgs.API.KeepDefaultEndpoint.
//
// Authenticated Origin Pulls is the ESTATE's zone setting, made where the zone
// is (a Cloudflare provider in the estate's own program, not here): the zone's
// `tls_client_auth` on, so that Cloudflare presents its client certificate to
// the origin, and TruststorePEM the CA that signs it. Without the setting
// Cloudflare presents none and the domain refuses every request.
//
// The AWS provider is the caller's to choose, as for the core library.
func NewEdge(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*Edge, error) {
	if err := args.validate(); err != nil {
		return nil, err
	}
	a := *args
	out := &Edge{}
	if err := ctx.RegisterComponentResource(EdgeType, name, out, opts...); err != nil {
		return nil, err
	}
	child := pulumi.Parent(out)
	var tags pulumi.StringMapInput
	if len(a.Tags) > 0 {
		tags = pulumi.ToStringMap(a.Tags)
	}

	// ---- the certificate
	certArn := a.CertificateArn
	if a.Certificate != nil {
		cert, err := acm.NewCertificate(ctx, name+"-certificate", &acm.CertificateArgs{
			DomainName: pulumi.String(a.DomainName), ValidationMethod: pulumi.String("DNS"), Tags: tags,
		}, child)
		if err != nil {
			return nil, fmt.Errorf("sluis edge certificate: %w", err)
		}
		rec := ValidationRecord{
			Name: validationField(cert, func(o acm.CertificateDomainValidationOption) *string { return o.ResourceRecordName }),
			Type: validationField(cert, func(o acm.CertificateDomainValidationOption) *string { return o.ResourceRecordType }),
			Value: validationField(cert, func(o acm.CertificateDomainValidationOption) *string {
				return o.ResourceRecordValue
			}),
		}
		fqdn, err := a.Certificate.CreateValidationRecord(ctx, rec)
		if err != nil {
			return nil, fmt.Errorf("sluis edge certificate validation record: %w", err)
		}
		valid, err := acm.NewCertificateValidation(ctx, name+"-certificate-validation", &acm.CertificateValidationArgs{
			CertificateArn:        cert.Arn,
			ValidationRecordFqdns: pulumi.StringArray{fqdn},
		}, child)
		if err != nil {
			return nil, fmt.Errorf("sluis edge certificate validation: %w", err)
		}
		certArn = valid.CertificateArn
	}

	// ---- the truststore: one object, in the blob bucket or in a bucket of its own
	var bucketID, bucketName pulumi.StringInput
	var after []pulumi.Resource
	if s := a.Storage; s != nil {
		bucketID, bucketName = s.BucketName, s.BucketName
	} else {
		b, err := newTruststoreBucket(ctx, name, &a, tags, child)
		if err != nil {
			return nil, err
		}
		bucketID, bucketName, after = b.bucket.ID(), b.bucket.Bucket, b.ready
	}
	obj, err := s3.NewBucketObjectv2(ctx, name+"-truststore-pem", &s3.BucketObjectv2Args{
		Bucket:      bucketID,
		Key:         pulumi.String(TruststoreKey),
		Content:     pulumi.String(a.TruststorePEM),
		ContentType: pulumi.String("application/x-pem-file"),
	}, child, pulumi.DependsOn(after))
	if err != nil {
		return nil, fmt.Errorf("sluis edge truststore object: %w", err)
	}

	// ---- the domain, which pins the version of the object it was given
	fd := a.FrontDoor
	aliases := func(suffix string) pulumi.ResourceOption {
		if fd.Name == "" || fd.Component == nil {
			return pulumi.Aliases(nil)
		}
		return pulumi.Aliases([]pulumi.Alias{{Name: pulumi.String(fd.Name + suffix), Parent: fd.Component}})
	}
	uri := pulumi.Sprintf("s3://%s/%s", bucketName, obj.Key)
	domain, err := apigatewayv2.NewDomainName(ctx, name+"-domain", &apigatewayv2.DomainNameArgs{
		DomainName: pulumi.String(a.DomainName),
		DomainNameConfiguration: &apigatewayv2.DomainNameDomainNameConfigurationArgs{
			CertificateArn: certArn,
			EndpointType:   pulumi.String("REGIONAL"),
			SecurityPolicy: pulumi.String("TLS_1_2"),
		},
		MutualTlsAuthentication: &apigatewayv2.DomainNameMutualTlsAuthenticationArgs{
			TruststoreUri:     uri,
			TruststoreVersion: obj.VersionId,
		},
		Tags: tags,
	}, child, aliases("-domain"))
	if err != nil {
		return nil, fmt.Errorf("sluis edge custom domain: %w", err)
	}
	if _, err := apigatewayv2.NewApiMapping(ctx, name+"-domain-mapping", &apigatewayv2.ApiMappingArgs{
		ApiId: fd.APIID, DomainName: domain.DomainName, Stage: fd.StageName,
	}, child, aliases("-domain-mapping")); err != nil {
		return nil, fmt.Errorf("sluis edge api mapping: %w", err)
	}

	out.DomainTarget = domain.DomainNameConfiguration.ApplyT(func(c apigatewayv2.DomainNameDomainNameConfiguration) string {
		if c.TargetDomainName == nil {
			return ""
		}
		return *c.TargetDomainName
	}).(pulumi.StringOutput)
	out.DomainHostedZoneID = domain.DomainNameConfiguration.ApplyT(func(c apigatewayv2.DomainNameDomainNameConfiguration) string {
		if c.HostedZoneId == nil {
			return ""
		}
		return *c.HostedZoneId
	}).(pulumi.StringOutput)
	out.CertificateArn = certArn.ToStringOutput()
	out.TruststoreBucketName = bucketName.ToStringOutput()
	out.TruststoreURI = uri
	out.TruststoreVersion = obj.VersionId
	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{
		"domainTarget": out.DomainTarget, "domainHostedZoneId": out.DomainHostedZoneID, "certificateArn": out.CertificateArn,
		"truststoreBucketName": out.TruststoreBucketName, "truststoreUri": out.TruststoreURI, "truststoreVersion": out.TruststoreVersion,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// validationField is one field of the certificate's (single) DNS validation record.
func validationField(c *acm.Certificate, f func(acm.CertificateDomainValidationOption) *string) pulumi.StringOutput {
	return c.DomainValidationOptions.ApplyT(func(o []acm.CertificateDomainValidationOption) string {
		if len(o) == 0 || f(o[0]) == nil {
			return ""
		}
		return *f(o[0])
	}).(pulumi.StringOutput)
}

type truststoreBucket struct {
	bucket *s3.Bucket
	// ready is what the object waits for: versioning on and the policy in place.
	ready []pulumi.Resource
}

// newTruststoreBucket is the edge's own small bucket: encrypted, versioned,
// closed to the public and to plain HTTP, protected, with writes under the
// truststore prefix denied to every principal but the apply identities.
func newTruststoreBucket(ctx *pulumi.Context, name string, a *Args, tags pulumi.StringMapInput, child pulumi.ResourceOption) (*truststoreBucket, error) {
	b := a.TruststoreBucket
	bucket, err := s3.NewBucket(ctx, name+"-truststore", &s3.BucketArgs{Bucket: pulumi.String(b.Name), Tags: tags},
		child, pulumi.Protect(true))
	if err != nil {
		return nil, fmt.Errorf("sluis edge truststore bucket: %w", err)
	}
	if _, err := s3.NewBucketServerSideEncryptionConfigurationV2(ctx, name+"-truststore-encryption",
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
		return nil, err
	}
	versioning, err := s3.NewBucketVersioningV2(ctx, name+"-truststore-versioning", &s3.BucketVersioningV2Args{
		Bucket:                  bucket.ID(),
		VersioningConfiguration: &s3.BucketVersioningV2VersioningConfigurationArgs{Status: pulumi.String("Enabled")},
	}, child)
	if err != nil {
		return nil, err
	}
	block, err := s3.NewBucketPublicAccessBlock(ctx, name+"-truststore-public-access", &s3.BucketPublicAccessBlockArgs{
		Bucket: bucket.ID(), BlockPublicAcls: pulumi.Bool(true), BlockPublicPolicy: pulumi.Bool(true),
		IgnorePublicAcls: pulumi.Bool(true), RestrictPublicBuckets: pulumi.Bool(true),
	}, child)
	if err != nil {
		return nil, err
	}
	guard := a.guard(b.ApplyPrincipalArns)
	policy, err := s3.NewBucketPolicy(ctx, name+"-truststore-policy", &s3.BucketPolicyArgs{
		Bucket: bucket.ID(),
		Policy: bucket.Arn.ApplyT(func(arn string) (string, error) {
			raw, err := jsonDocument([]map[string]any{{
				"Sid": "DenyPlainHTTP", "Effect": "Deny", "Principal": "*", "Action": "s3:*",
				"Resource":  []string{arn, arn + "/*"},
				"Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "false"}},
			}, guard.Statement(arn)})
			return raw, err
		}).(pulumi.StringOutput),
	}, child, pulumi.DependsOn([]pulumi.Resource{block}))
	if err != nil {
		return nil, err
	}
	return &truststoreBucket{bucket: bucket, ready: []pulumi.Resource{versioning, policy}}, nil
}
