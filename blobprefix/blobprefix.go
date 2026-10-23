// Package blobprefix names every key prefix that sluis reads or writes in the
// blob store on layout v5, and is the one place they are defined.
//
// A module owns the blobs under `<module>/`, and nothing else in the bucket:
// the function role of the module that hosts it is granted exactly that prefix
// (deploy/pulumi, BlobPrefix), so a prefix defined anywhere else would be one no
// role may touch. The code, the backup export and the IAM policy all take their
// prefixes from here, and a test fails when one of them is not under the
// module's own root.
package blobprefix

// The module roots.
const (
	GitHub = "github/"
	Slack  = "slack/"
	Google = "google/"
	// OIDC is the issuer's: it hosts the Outline controller until that has a
	// function of its own (docs/decisions/0077).
	OIDC = "oidc/"
)

// The prefixes below a module's root.
const (
	// GitHubReports holds what the GitHub controller last reported, one object
	// per organisation.
	GitHubReports = GitHub + "reports/"
	// SlackReports holds what the Slack controller last reported, one object per
	// workspace.
	SlackReports = Slack + "reports/"
	// OutlineReports holds what the Outline controller last reported, one
	// object per workspace.
	OutlineReports = OIDC + "reports/outline/"
	// GoogleCache holds the hub's directory snapshots, one object per workspace.
	GoogleCache = Google + "cache/"
)

// The prefixes of layout v4 and of releases before v1.75, where the reports sat
// at the root of the bucket and the cache under `snapshots/`. Nothing reads or
// writes them on layout v5 except `sluis migrate`, which copies the reports
// from them; they can be deleted from a bucket.
const (
	LegacyGitHubReports = "reports/github/"
	LegacySlackReports  = "reports/slack/"
	LegacyCache         = "snapshots/"
)

// Root is the module's prefix in the bucket, with the trailing slash.
func Root(module string) string { return module + "/" }

// Used is every prefix the code reads or writes, by the module that owns it.
// The backup export keeps the reports and not the cache.
func Used() map[string][]string {
	return map[string][]string{
		"github": {GitHubReports},
		"slack":  {SlackReports},
		"google": {GoogleCache},
		"oidc":   {OutlineReports},
	}
}

// Legacy is the layout v4 location of a prefix of [Used], or "" for one that
// has none worth copying.
func Legacy(prefix string) string {
	switch prefix {
	case GitHubReports:
		return LegacyGitHubReports
	case SlackReports:
		return LegacySlackReports
	}
	return ""
}
