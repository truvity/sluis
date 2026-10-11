package server

import (
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/githubroster/status"
)

// Where the GitHub records are kept is the same interface for the console and
// for the GitHub module the console's operations moved into
// (docs/decisions/0071), declared beside the records.
type (
	// GitHubReports is where the console reads what the GitHub controller last
	// reported: the documents of the status contract, keyed as written.
	GitHubReports = status.ReportReader
	// GitHubCatalogueApps is where catalogue Apps are kept: every App's record
	// and key, by its catalogue id.
	GitHubCatalogueApps = catalogueapp.Store
	// GitHubRunnerApps is where runner Apps are kept.
	GitHubRunnerApps = runnerapp.Store
	// GitHubLinkApp is where the link App is kept.
	GitHubLinkApp = link.AppStore
	// GitHubLinks is where people's links are kept.
	GitHubLinks = link.Links
	// GitHubConfirmations is where operators' confirmations of removal sets are
	// kept.
	GitHubConfirmations = connection.ConfirmationStore
)

// GitHubConnections is where connected organisations are kept: a record the
// console shows, a credential only the controller acts with.
type GitHubConnections = connection.Store
