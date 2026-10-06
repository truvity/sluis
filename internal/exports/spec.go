// Package exports copies secrets the console keeps OUT of the service, into
// the secret store a consumer reads (docs/decisions/0034).
//
// The service keeps a Slack App's bot token, a runner App's key, a connected
// workspace's credential in Secrets. A program that must act as the App
// when the service is not there to ask (Alertmanager posting as the Slack
// bot, a runner scale set registering with its App) reads a copy from OpenBao
// instead, and so do the disaster-recovery backups. This package keeps those
// copies current: it reads the stores, shapes the entries as the Kubernetes
// Secrets once did, and puts them through the [port.Export] port.
//
// An export is a copy, asynchronous and never a dependency. It runs after the
// State write that changed its source has committed, out of band, and a store
// that is down changes nothing live: sign-in, a tick and a console action
// neither wait for it nor learn of its failure; the copy is stale until the
// next attempt, which is retried with backoff and counted.
package exports

import "github.com/truvity/sluis/internal/exportspec"

// What an export is, and the rules one is held to, are internal/exportspec's:
// the policy document's loader checks a declared export by them before the
// service starts, and this package runs what they describe.

// The sources an export reads, as `source` spells them.
const (
	SourceSlackApp  = exportspec.SourceSlackApp
	SourceGitHubApp = exportspec.SourceGitHubApp
	SourceRunnerApp = exportspec.SourceRunnerApp
	SourceBundle    = exportspec.SourceBundle

	SourceOIDCClient = exportspec.SourceOIDCClient
)

// The bundles, as `bundle` spells them.
const (
	BundleWorkspaceCredentials = exportspec.BundleWorkspaceCredentials
	BundleGitHubApps           = exportspec.BundleGitHubApps
	BundleGitHubLinks          = exportspec.BundleGitHubLinks
	BundleGitHubRunnerApps     = exportspec.BundleGitHubRunnerApps
	BundleGitHubCatalogueApps  = exportspec.BundleGitHubCatalogueApps
	BundleSlackCredentials     = exportspec.BundleSlackCredentials
	BundleSlackRecords         = exportspec.BundleSlackRecords
)

// Bundles lists every bundle name.
var Bundles = exportspec.Bundles

// The properties a source offers.
const (
	PropBotToken       = exportspec.PropBotToken
	PropAppID          = exportspec.PropAppID
	PropInstallationID = exportspec.PropInstallationID
	PropPrivateKey     = exportspec.PropPrivateKey
	PropClientID       = exportspec.PropClientID
	PropClientSecret   = exportspec.PropClientSecret
)

// DefaultInterval and MinInterval are exportspec's.
const (
	DefaultInterval = exportspec.DefaultInterval
	MinInterval     = exportspec.MinInterval
)

type (
	// Spec is one validated export.
	Spec = exportspec.Spec
	// Declared is what the deployment declares, to hold an export's source to.
	Declared = exportspec.Declared
)

// FromConfig validates the `exports` list and returns it as specs.
func FromConfig(entries []exportspec.Entry, declared Declared) ([]Spec, error) {
	return exportspec.FromConfig(entries, declared)
}
