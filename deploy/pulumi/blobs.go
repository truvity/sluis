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
	// the secret that holds the access key pair: `internal/<module>/<name>`, for
	// example `internal/google/blobs-r2` (the module that owns the blob prefix). The function reads it at run time through its
	// secrets source; the library grants it read on that one parameter and
	// nothing else.
	//
	// It may also be `external/cloudflare/<preset>`: the `cloudflare/v1`
	// document the Cloudflare module rotates for an R2 preset of the
	// installation's presets with `store: external`. This is the form to use
	// when the presets are the Cloudflare module's own (the installation's
	// Cloudflare section is rendered into its function, and the other functions
	// reach it through `cloudflare.remote`): every function that has the blobs
	// reads that one parameter, and none holds the minter's credential. The
	// preset must be declared, have an `endpoint`, and keep `store: external`
	// (its `lifetime` longer than its `rotation`); the Cloudflare module's own
	// function mints for its blobs in its process, so it needs no stored
	// credential to start. Seed the secret out of band
	// (docs/how-to: seed a secret without printing it). Exactly one of
	// CredentialsRef and CredentialsPreset is required.
	CredentialsRef string
	// CredentialsPreset makes sluis mint its own R2 credentials for the store
	// from this key of the installation's `cloudflare.presets` (an R2 preset: it
	// has an `endpoint`), and renders `ports.blob.s3.credentials.preset`. The
	// library then declares no parameter and grants no read for blob
	// credentials: each function that has the blobs mints in process through the
	// Cloudflare minter. That works only where the function holds the minter. An
	// installation that declares Cloudflare presets keeps them in the Cloudflare
	// module's document, and the other functions have no minter and no preset to
	// mint from, so the library refuses CredentialsPreset there: name the stored
	// credential as CredentialsRef
	// (`external/cloudflare/<preset>`) instead. Exclusive with CredentialsRef.
	CredentialsPreset string
}

// credentialsRefPattern is an `internal/<kind>/<id>` address: two lower-case
// segments, no wildcard, no dot-dot.
// cloudflareNamePattern is a preset's name: a DNS label, as the service document
// spells it.
var cloudflareNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// storedPreset is the preset a CredentialsRef of the form
// `external/cloudflare/<preset>` names: the credential the Cloudflare module
// rotates for it. Empty for any other ref.
func (b *ExternalBlobs) storedPreset() string {
	if b == nil {
		return ""
	}
	p, _ := sluisconfig.CloudflareStoredRef(b.CredentialsRef)
	return p
}

// checkPreset holds the blobs' use of a Cloudflare preset to the installation's
// presets. A CredentialsPreset must be declared and be an R2 preset, and is
// refused when the presets are the Cloudflare module's (remote): the other
// functions hold no minter. A stored CredentialsRef
// (`external/cloudflare/<preset>`) must name a declared R2 preset that keeps
// `store: external`, whether the presets are local or remote: the rotation is
// what writes the parameter.
func (b *ExternalBlobs) checkPreset(presets map[string]sluisconfig.CloudflarePreset, remote bool) error {
	if b == nil {
		return nil
	}
	if b.CredentialsPreset != "" {
		p, ok := presets[b.CredentialsPreset]
		switch {
		case remote:
			return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsPreset %q cannot be used with module functions: the presets are the "+
				"Cloudflare module's (the other functions reach it with cloudflare.remote) and only that function holds the minter. "+
				"Keep the preset with store: external and set StorageArgs.Blobs.CredentialsRef to %q", b.CredentialsPreset,
				sluisconfig.CloudflareStoredRefPrefix+b.CredentialsPreset)
		case !ok:
			return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsPreset %q is not in Installation.Cloudflare.Presets", b.CredentialsPreset)
		case p.Endpoint == "":
			return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsPreset %q has no endpoint, so it is not an R2 preset", b.CredentialsPreset)
		}
		return nil
	}
	name := b.storedPreset()
	if name == "" {
		return nil
	}
	p, ok := presets[name]
	switch {
	case !ok:
		return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsRef %q names the preset %q, which is not in Installation.Cloudflare.Presets",
			b.CredentialsRef, name)
	case p.Endpoint == "":
		return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsRef %q: the preset %q has no endpoint, so it is not an R2 preset", b.CredentialsRef, name)
	case !p.Stored():
		return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsRef %q: the preset %q has store: none, so nothing is rotated to that address; "+
			"set store: external (with a rotation shorter than its lifetime)", b.CredentialsRef, name)
	case !sameEndpoint(p.Endpoint, b.Endpoint):
		return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsRef %q: the preset %q is for the endpoint %q and the blobs are at %q",
			b.CredentialsRef, name, p.Endpoint, b.Endpoint)
	}
	return nil
}

// sameEndpoint compares two https endpoints without case or a trailing slash.
func sameEndpoint(a, b string) bool {
	norm := func(s string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "/") }
	return norm(a) == norm(b)
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
		errs = append(errs, errors.New("CredentialsRef and CredentialsPreset are exclusive: name the secret that holds the "+
			"credentials, or the Cloudflare preset that mints them"))
	case b.CredentialsPreset != "":
		if !cloudflareNamePattern.MatchString(b.CredentialsPreset) {
			errs = append(errs, fmt.Errorf("the credentials preset %q is not a preset name (a lower-case DNS label)", b.CredentialsPreset))
		}
	case b.storedPreset() != "":
		if !cloudflareNamePattern.MatchString(b.storedPreset()) {
			errs = append(errs, fmt.Errorf("the credentials ref %q is not external/cloudflare/<preset> with a preset name (a lower-case DNS label)", b.CredentialsRef))
		}
	case !credentialsRefPattern.MatchString(b.CredentialsRef) || strings.Contains(b.CredentialsRef, ".."):
		errs = append(errs, fmt.Errorf("the credentials ref %q is not an address of the form internal/<kind>/<id> "+
			"(lower-case letters, digits, - . _; no wildcard) or external/cloudflare/<preset>; it names a secret and is never the secret", b.CredentialsRef))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("sluispulumi: StorageArgs.Blobs: %w", err)
	}
	return nil
}

// moduleCredentialsRef is the address a CredentialsRef that does not begin
// with a module has under one: refs are `internal/<module>/<name>`, and the
// credential of a blob store belongs to the module that owns the blob prefix,
// google, so `internal/blobs/r2` is `internal/google/blobs-r2`. A ref that
// already begins with a module is returned as it is.
func moduleCredentialsRef(ref string) string {
	rest, ok := strings.CutPrefix(ref, "internal/")
	first, second, _ := strings.Cut(rest, "/")
	if !ok || validModule(Module(first)) {
		return ref
	}
	return "internal/" + string(ModuleGoogle) + "/" + first + "-" + second
}

// checkModuleRef refuses a credentials ref that does not begin with a module:
// the function would read an address the secrets layout does not have.
func (b *ExternalBlobs) checkModuleRef() error {
	if b.CredentialsPreset != "" {
		return nil
	}
	if want := moduleCredentialsRef(b.CredentialsRef); want != b.CredentialsRef {
		return fmt.Errorf("sluispulumi: StorageArgs.Blobs.CredentialsRef %q does not begin with a module: a ref is "+
			"internal/<module>/<name>; set it to %q", b.CredentialsRef, want)
	}
	return nil
}

func (b *ExternalBlobs) region() string {
	if b.Region == "" {
		return "auto"
	}
	return b.Region
}

// portsBlob is `ports.blob` for these blobs, as the function that hosts the
// module reads it. The Cloudflare module of an installation whose blobs read a
// stored preset mints for its own blobs in its process (`credentials.preset`),
// because it is the function that writes the stored credential and could not
// start if it needed that credential first; every other function reads it.
func (b *ExternalBlobs) portsBlob(module string) map[string]any {
	s3 := map[string]any{
		"bucket": b.Bucket, "endpoint": b.Endpoint, "region": b.region(),
	}
	switch {
	case b.CredentialsPreset != "":
		s3["credentials"] = map[string]any{"preset": b.CredentialsPreset}
	case b.storedPreset() != "" && module == RoleCloudflare:
		s3["credentials"] = map[string]any{"preset": b.storedPreset()}
	default:
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
