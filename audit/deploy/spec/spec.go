// Package spec holds the names the audit deploy library gives an
// installation's AWS resources and the checks of the values an estate hands
// it, without the Pulumi SDK.
//
// An estate's own tooling needs both before anything exists: a key policy
// names a component's role, a render writes the archive bucket's name, and a
// configuration file is refused before a preview. Code that must not link a
// cloud provider SDK imports this package; `deploy/pulumi` applies the same
// rules, and its tests hold the two equal.
package spec

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/truvity/sluis/audit/profile"
)

// The Lambda functions' build, which a telemetry layer has to match.
const (
	// LambdaArchitecture is the architecture of the library's functions.
	LambdaArchitecture = "arm64"
	// LambdaRuntime is their runtime.
	LambdaRuntime = "provided.al2023"
)

// Lock modes of the archive bucket: NONE, then GOVERNANCE as the trial, then
// COMPLIANCE, which nobody can shorten.
const (
	LockNone       = "NONE"
	LockGovernance = "GOVERNANCE"
	LockCompliance = "COMPLIANCE"
)

// The install presets, the keys of the library's Presets.
const (
	PresetOperational = string(profile.Operational)
	PresetStandard    = string(profile.Standard)
	PresetAttested    = string(profile.Attested)
)

var (
	// NamePattern is a component name: it is in every role, queue and
	// function name.
	NamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	// KeyComponentPattern is a profile name: the first component of every
	// object key.
	KeyComponentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// BucketPrefixPattern is the start of an archive bucket's name.
	BucketPrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,40}$`)
)

// Component is one workload of a chart installation that holds an AWS role of
// its own.
type Component struct {
	// Name is the chart component: writer, digest, verify or query.
	Name string
	// ServiceAccount is the chart's ServiceAccount for it.
	ServiceAccount string
}

// installationComponents are the components that hold a role, in a fixed
// order.
var installationComponents = []string{"writer", "digest", "verify", "query"}

// InstallationComponents are the AWS identities of an installation of the
// chart named release, one per component that holds a role, in a fixed order.
// The writer's ServiceAccount is the release's own; the others are suffixed
// with the component, as the chart names them.
func InstallationComponents(release string) []Component {
	out := make([]Component, len(installationComponents))

	for i, name := range installationComponents {
		sa := release
		if name != "writer" {
			sa = release + "-" + name
		}

		out[i] = Component{Name: name, ServiceAccount: sa}
	}

	return out
}

// ComponentRoleName is the IAM role a deployment gives a component of an
// installation on a cluster: `<prefix>-audit-<component>`.
func ComponentRoleName(prefix, component string) string {
	return prefix + "-audit-" + component
}

// ArchiveBucketName is the archive bucket's name for a prefix, the account and
// the region: a bucket name is global, so it carries both.
func ArchiveBucketName(prefix, accountID, region string) string {
	return fmt.Sprintf("%s-%s-%s", prefix, accountID, region)
}

// CheckName reports whether name is one the library accepts for a component.
func CheckName(name string) error {
	if !NamePattern.MatchString(name) {
		return fmt.Errorf("the component name %q must be 1-32 characters of a-z, 0-9 and -, "+
			"starting with a letter: it is in every role, queue and function name", name)
	}

	return nil
}

// CheckRolePath reports whether p is an IAM path the library accepts.
func CheckRolePath(p string) error {
	if !strings.HasPrefix(p, "/") || !strings.HasSuffix(p, "/") {
		return fmt.Errorf("RolePath %q must start and end with /", p)
	}

	return nil
}

// CheckLockMode reports whether mode is a lock mode of an archive bucket.
func CheckLockMode(mode string) error {
	switch mode {
	case LockNone, LockGovernance, LockCompliance:
		return nil
	}

	return fmt.Errorf("ObjectLockMode %q must be NONE, GOVERNANCE or COMPLIANCE", mode)
}

// CheckProfile reports whether name can be a profile.
func CheckProfile(name string) error {
	if !KeyComponentPattern.MatchString(name) {
		return fmt.Errorf("the profile name %q is not a key component (no /, no leading dot)", name)
	}

	return nil
}

// CheckBucketPrefix reports whether prefix makes a valid bucket name (3 to 63
// characters of a-z, 0-9 and -) with any 12-digit account and any region.
func CheckBucketPrefix(prefix string) error {
	if !BucketPrefixPattern.MatchString(prefix) {
		return fmt.Errorf("bucket prefix %q is not a bucket name prefix", prefix)
	}

	if n := len(ArchiveBucketName(prefix, strings.Repeat("0", 12), "eu-central-1")); n > 63 {
		return fmt.Errorf("bucket prefix %q makes a bucket name of %d characters (63 at most)", prefix, n)
	}

	return nil
}

// CheckTelemetryURLs reports whether the OIDC issuer and the OTLP endpoint are
// https URLs: a bearer token crosses them.
func CheckTelemetryURLs(issuerURL, otlpEndpoint string) error {
	var errs []error

	for _, field := range [][2]string{{"IssuerURL", issuerURL}, {"OTLPEndpoint", otlpEndpoint}} {
		if u, err := url.Parse(field[1]); err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("telemetry %s %q must be an https URL", field[0], field[1]))
		}
	}

	return errors.Join(errs...)
}

// CheckExtensionLayer reports whether an OTLP extension layer's architectures
// and runtimes fit the library's functions.
func CheckExtensionLayer(architectures, runtimes []string) error {
	var errs []error

	if !slices.Contains(architectures, LambdaArchitecture) {
		errs = append(errs, fmt.Errorf("the layer's architectures %v must include %s, the functions' architecture",
			architectures, LambdaArchitecture))
	}

	for _, a := range architectures {
		if a != "arm64" && a != "amd64" {
			errs = append(errs, fmt.Errorf("the layer's architecture %q is not arm64 or amd64", a))
		}
	}

	if !slices.Contains(runtimes, LambdaRuntime) {
		errs = append(errs, fmt.Errorf("the layer's compatible runtimes %v must include %s, the functions' runtime", runtimes, LambdaRuntime))
	}

	return errors.Join(errs...)
}
