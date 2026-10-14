// Package spec holds the names the deploy libraries give AWS resources and
// the checks of the values an installation hands them, without the Pulumi SDK.
//
// An installation's own tooling needs both before anything exists: a key
// policy names a role that another stack creates, a render writes a bucket
// name into a document, and a configuration file is refused before a preview
// rather than half way through an apply. This package is that knowledge in one
// place, importable by code that must not link a cloud provider SDK.
//
// The rules are the ones `deploy/pulumi` applies; the library's tests hold the
// two equal.
package spec

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	internalconfig "github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/issuer"
)

// The two signing algorithms deploy tooling names for a verify-only key; the
// issuer takes no other (CheckVerifyKey follows it): ES384 is P-384, RS256 an
// RSA key of 2048 bits or more.
const (
	AlgES384 = "ES384"
	AlgRS256 = "RS256"
)

// KubernetesRoleSuffix ends the role of the Kubernetes installation's pod:
// `<role name prefix>-sluis`.
const KubernetesRoleSuffix = "-sluis"

var (
	// InstancePattern is an instance name: lower-case letters, digits and
	// dashes, at most 32, starting and ending with a letter or digit.
	InstancePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	// FunctionNamePattern is a Lambda function's name, and so its role's.
	FunctionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// AliasPattern is a KMS alias.
	AliasPattern = regexp.MustCompile(`^alias/[A-Za-z0-9/_-]{1,250}$`)
	// BucketPrefixPattern is the start of a bucket name that BucketName
	// completes.
	BucketPrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,40}$`)
)

// bucketNameLimit is S3's length limit on a bucket name.
const bucketNameLimit = 63

// reservedInstances would nest an installation's SSM tree under another's.
var reservedInstances = []string{"private", "export"}

// BucketName is a bucket named for one account and region:
// `<prefix>-<account id>-<region>`, unique without coordination.
func BucketName(prefix, accountID, region string) string {
	return fmt.Sprintf("%s-%s-%s", prefix, accountID, region)
}

// RoleARN is an IAM role's ARN, known before the role exists. path is `/` or
// `/<seg>/.../`.
func RoleARN(accountID, path, name string) string {
	if path == "" {
		path = "/"
	}

	return fmt.Sprintf("arn:aws:iam::%s:role%s%s", accountID, path, name)
}

// FunctionRoleARN is a Lambda function's role: the library names the role
// after the function, at path `/`.
func FunctionRoleARN(accountID, function string) string {
	return RoleARN(accountID, "/", function)
}

// KubernetesRoleName is the Kubernetes installation's pod role, named from the
// installation's role name prefix.
func KubernetesRoleName(prefix string) string {
	return prefix + KubernetesRoleSuffix
}

// CheckInstance refuses an instance name the library refuses.
func CheckInstance(instance string) error {
	if !InstancePattern.MatchString(instance) {
		return fmt.Errorf("instance %q is not lower-case letters, digits and dashes, at most 32", instance)
	}

	for _, reserved := range reservedInstances {
		if instance == reserved {
			return fmt.Errorf("instance %q is reserved: it would nest the installation under another's tree", instance)
		}
	}

	return nil
}

// CheckFunctionName refuses a Lambda function name the library refuses.
func CheckFunctionName(name string) error {
	if !FunctionNamePattern.MatchString(name) {
		return fmt.Errorf("function name %q is not letters, digits, - and _, at most 64", name)
	}

	return nil
}

// CheckKeyAlias refuses anything but a customer KMS alias: a key is named by
// alias, never by ARN or key id, and never by an AWS managed alias.
func CheckKeyAlias(alias string) error {
	if strings.HasPrefix(alias, "arn:") || !AliasPattern.MatchString(alias) || strings.HasPrefix(alias, "alias/aws/") {
		return fmt.Errorf("%q is not a customer KMS alias (alias/<name>)", alias)
	}

	return nil
}

// CheckBucketPrefix refuses a prefix that is not the start of a bucket name,
// or that makes a name longer than S3 allows in this region with any
// 12-digit account.
func CheckBucketPrefix(prefix, region string) error {
	if !BucketPrefixPattern.MatchString(prefix) {
		return fmt.Errorf("bucket prefix %q is not a bucket name prefix", prefix)
	}

	if n := len(BucketName(prefix, strings.Repeat("0", 12), region)); n > bucketNameLimit {
		return fmt.Errorf("bucket prefix %q makes a bucket name of %d characters (%d at most)", prefix, n, bucketNameLimit)
	}

	return nil
}

// CheckVerifyKey holds one verify-only key, and the algorithm it is for, to
// what the running issuer accepts: it IS the issuer's own parser
// ([issuer.ParseVerifyOnlyKey]), so the deploy check and the issuer cannot
// disagree about a key. alg may be empty (the key's own algorithm), and the
// key may be a PEM or a JWK. A private key is refused whatever its encoding,
// and an error never carries key material.
func CheckVerifyKey(pemText, alg string) error {
	_, err := issuer.ParseVerifyOnlyKey([]byte(pemText), "", alg, time.Time{})

	return err
}

// CheckCloudflareRotation holds a Cloudflare preset's token lifetime and
// rotation to what the module accepts at load: both between one minute and 24
// hours, the rotation shorter, so a reader always has the difference to pick
// up the replacement.
func CheckCloudflareRotation(lifetime, rotation string) error {
	life, err1 := time.ParseDuration(lifetime)
	rot, err2 := time.ParseDuration(rotation)

	switch {
	case err1 != nil || err2 != nil:
		return fmt.Errorf("lifetime %q and rotation %q must be durations", lifetime, rotation)
	case life < internalconfig.CloudflareMinLifetime || life > internalconfig.CloudflareMaxLifetime ||
		rot < internalconfig.CloudflareMinLifetime || rot >= life:
		return fmt.Errorf("lifetime %s and rotation %s must lie between %s and %s with the rotation shorter",
			lifetime, rotation, internalconfig.CloudflareMinLifetime, internalconfig.CloudflareMaxLifetime)
	}

	return nil
}

// ChartSecret is one entry of the sluis chart's `secrets` value: the secret
// the service reads by Name, projected from one key of a Kubernetes Secret.
type ChartSecret struct {
	// Name is the secret's name as the service reads it (`clients/<id>/secret`).
	Name string `yaml:"name"`
	// SecretName and Key are the Kubernetes Secret and its key.
	SecretName string `yaml:"secretName"`
	Key        string `yaml:"key"`
}
