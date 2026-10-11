package server

import (
	"strings"
	"time"

	"github.com/truvity/sluis/internal/githubroster/connection"
)

// githubFlowWindow is how long an owner has for each of the two clicks.
const githubFlowWindow = 10 * time.Minute

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
