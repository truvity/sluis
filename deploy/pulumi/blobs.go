package sluispulumi

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	sluisconfig "github.com/truvity/sluis/config"
)

// ExternalBlobs is a blob store on an S3-compatible endpoint that the
// installation's infrastructure does not own: Cloudflare R2, say. The library
// creates no bucket for it and grants no IAM on it; it tells the function where
// the store is (`ports.blob.s3` in the service document) and which secret holds
// the credentials. The credentials are an address the runtime reads, never a
// value: nothing secret is an input here, so none is in the Pulumi state.
type ExternalBlobs struct {
	// Bucket is the bucket's name at the endpoint. Required.
	Bucket string
	// Endpoint is the https URL of the S3-compatible API
	// (`https://<account>.r2.cloudflarestorage.com`). Required.
	Endpoint string
	// Region is the signing region the endpoint expects. R2 wants "auto".
	// Default "auto".
	Region string
	// PathStyle addresses the bucket in the path and not the host name.
	PathStyle bool
	// Prefix is a key prefix inside the bucket, for an installation that shares it.
	Prefix string
	// CredentialsRef is the address, below the installation's secrets root, of
	// the secret that holds the access key pair: `internal/<kind>/<id>`, for
	// example `internal/blobs/r2`. The function reads it at run time through its
	// secrets source; the library grants it read on that one parameter and
	// nothing else. Seed the secret out of band
	// (docs/how-to: seed a secret without printing it). Exactly one of
	// CredentialsRef and CredentialsPreset is required.
	CredentialsRef string
	// CredentialsPreset makes sluis mint its own R2 credentials for the store
	// from this key of the installation's `cloudflare.presets` (an R2 preset: it
	// has an `endpoint`), and renders `ports.blob.s3.credentials.preset`. The
	// library then declares no parameter and grants no read for blob
	// credentials: each function that has the blobs mints in process through the
	// Cloudflare minter, whose grants it already receives. The preset may have
	// `store` external or none: the blobs never read the stored credential, they
	// mint on demand, so `none` (nothing stored, no rotation) is the right
	// choice when nothing else reads it. Exclusive with CredentialsRef.
	CredentialsPreset string
}

// credentialsRefPattern is an `internal/<kind>/<id>` address: two lower-case
// segments, no wildcard, no dot-dot.
// cloudflareNamePattern is a preset's name: a DNS label, as the service document
// spells it.
var cloudflareNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// checkPreset holds CredentialsPreset to the installation's Cloudflare presets:
// it must be declared, and be an R2 preset.
func (b *ExternalBlobs) checkPreset(presets map[string]sluisconfig.CloudflarePreset) error {
	if b == nil || b.CredentialsPreset == "" {
		return nil
	}
	p, ok := presets[b.CredentialsPreset]
	switch {
	case !ok:
		return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsPreset %q is not in Installation.Cloudflare.Presets", b.CredentialsPreset)
	case p.Endpoint == "":
		return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsPreset %q has no endpoint, so it is not an R2 preset", b.CredentialsPreset)
	}
	return nil
}

var credentialsRefPattern = regexp.MustCompile(`^internal/[a-z0-9][a-z0-9-]{0,30}/[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidAddress is whether s is an `<area>/<kind>/<id>` secret address with the
// given area (`internal` or `external`).
func validAddress(area, s string) bool {
	return regexp.MustCompile(`^`+regexp.QuoteMeta(area)+`/[a-z0-9][a-z0-9-]{0,30}/[a-z0-9][a-z0-9._-]{0,62}$`).MatchString(s) &&
		!strings.Contains(s, "..")
}

func (b *ExternalBlobs) validate() error {
	if b == nil {
		return nil
	}
	var errs []error
	if b.Bucket == "" {
		errs = append(errs, errors.New("the bucket is required"))
	}
	u, err := url.Parse(b.Endpoint)
	switch {
	case b.Endpoint == "":
		errs = append(errs, errors.New("the endpoint is required"))
	case err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "":
		errs = append(errs, fmt.Errorf("the endpoint %q is not an https URL (host, no credentials, no query)", b.Endpoint))
	}
	switch {
	case b.CredentialsRef != "" && b.CredentialsPreset != "":
		errs = append(errs, errors.New("CredentialsRef and CredentialsPreset are exclusive: name the secret that holds the credentials, or the Cloudflare preset that mints them"))
	case b.CredentialsPreset != "":
		if !cloudflareNamePattern.MatchString(b.CredentialsPreset) {
			errs = append(errs, fmt.Errorf("the credentials preset %q is not a preset name (a lower-case DNS label)", b.CredentialsPreset))
		}
	case !credentialsRefPattern.MatchString(b.CredentialsRef) || strings.Contains(b.CredentialsRef, ".."):
		errs = append(errs, fmt.Errorf("the credentials ref %q is not an address of the form internal/<kind>/<id> "+
			"(lower-case letters, digits, - . _; no wildcard); it names a secret and is never the secret", b.CredentialsRef))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("sluispulumi: StorageArgs.Blobs: %w", err)
	}
	return nil
}

// V5CredentialsRef is the address a layout v4 CredentialsRef has on layout v5:
// refs there are `internal/<module>/<name>`, and the credential of a blob store
// belongs to the module that owns the blob prefix, google, so
// `internal/blobs/r2` is `internal/google/blobs-r2` (the address `sluis migrate
// v5` copies it to). A ref that already begins with a module is returned as it
// is.
func V5CredentialsRef(ref string) string {
	rest, ok := strings.CutPrefix(ref, "internal/")
	first, second, _ := strings.Cut(rest, "/")
	if !ok || validModule(Module(first)) {
		return ref
	}
	return "internal/" + string(ModuleGoogle) + "/" + first + "-" + second
}

// checkLayoutV5 refuses a layout v4 credentials ref where the layout is v5: the
// function would read an address the v5 secrets layout does not have.
func (b *ExternalBlobs) checkLayoutV5() error {
	if b.CredentialsPreset != "" {
		return nil
	}
	if want := V5CredentialsRef(b.CredentialsRef); want != b.CredentialsRef {
		return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsRef %q is a layout v4 address and Layout is v5, where it is "+
			"internal/<module>/<name>: set it to %q (`sluis migrate v5` copies the secret there)", b.CredentialsRef, want)
	}
	return nil
}

func (b *ExternalBlobs) region() string {
	if b.Region == "" {
		return "auto"
	}
	return b.Region
}

// portsBlob is `ports.blob` for these blobs.
func (b *ExternalBlobs) portsBlob() map[string]any {
	s3 := map[string]any{
		"bucket": b.Bucket, "endpoint": b.Endpoint, "region": b.region(),
	}
	if b.CredentialsPreset != "" {
		s3["credentials"] = map[string]any{"preset": b.CredentialsPreset}
	} else {
		s3["credentialsRef"] = b.CredentialsRef
	}
	if b.PathStyle {
		s3["pathStyle"] = true
	}
	if p := strings.Trim(b.Prefix, "/"); p != "" {
		s3["prefix"] = p
	}
	return map[string]any{"adapter": "s3", "s3": s3}
}

// secretParameterArn is the ARN of the SSM parameter holding the secret at an
// address below an installation's secrets root.
func secretParameterArn(region, account, instance, address string) string {
	return arnPrefix + "ssm:" + region + ":" + account + ":parameter" + SSMRoot(instance) + "/" + address
}

// credentialsStatements lets a role read the one parameter of the external
// blobs' credentials, and decrypt it with the parameter key when there is one.
func credentialsStatements(b *ExternalBlobs, region, account, instance, parameterKeyArn string) []statement {
	if b == nil || b.CredentialsPreset != "" {
		return nil
	}
	arn := secretParameterArn(region, account, instance, b.CredentialsRef)
	st := []statement{{
		"Sid": sidBlobCredentials, "Effect": "Allow", "Action": ssmGetParameter, "Resource": arn,
	}}
	// The key statement has a Sid of its own: a role's parameters have their
	// SluisParameterKey, and a Sid is unique in a policy, so a shared one would
	// drop one of the two (the credential's, or the role's own).
	for _, k := range parameterKeyStatements(parameterKeyArn, []string{kmsDecrypt}, []string{arn}) {
		k["Sid"] = sidBlobCredentials + "Key"
		st = append(st, k)
	}
	return st
}

const sidBlobCredentials = "SluisBlobCredentials"
