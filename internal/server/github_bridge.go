package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
)

// GitHubConnections is where connected organisations are kept: a record the
// console shows, a credential only the controller acts with.
type GitHubConnections = connection.Store

// The paths GitHub sends the browser back to, and what a flow's state binds,
// are the module's and the console's together.
const (
	githubCallbackPath  = consolewire.CallbackPath
	githubSetupPath     = consolewire.SetupPath
	githubBind          = consolewire.BindOrg
	githubCatalogueBind = consolewire.BindCatalogue
	githubRunnerBind    = consolewire.BindRunner
)

// githubFlowWindow is how long an owner has for each of the two clicks.
const githubFlowWindow = 10 * time.Minute

// githubTimeout bounds one call to GitHub on a request path.
const githubTimeout = 15 * time.Second

func (c *Console) githubHTTP() *http.Client {
	if c.deps.GitHubHTTP != nil {
		return c.deps.GitHubHTTP
	}
	return &http.Client{Timeout: githubTimeout}
}

// githubRoot is the origin the callbacks are at.
func (c *Console) githubRoot() string {
	if c.deps.RootURL != "" {
		return strings.TrimSuffix(c.deps.RootURL, "/")
	}
	return strings.TrimSuffix(c.deps.PublicURL, "/")
}

func recordOf(records []connection.Record, org string) (connection.Record, bool) {
	for i := range records {
		if records[i].Org == org {
			return records[i], true
		}
	}
	return connection.Record{}, false
}
