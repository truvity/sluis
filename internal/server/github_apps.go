// The GitHub Apps handler: every kind of App — the link App, an
// organisation's controller App, a tier's runner App and a catalogue App —
// is one App with a purpose, listed, read, checked, created, installed and
// disconnected by the calls below. The per-kind calls that came before
// (GetGitHubStatus's catalogue and runner sections, the catalogue and runner
// Begin, Disconnect and Check) are served from the same code, so a client
// written for them reads what it always read.

package server

import (
	"maps"
	"slices"

	"connectrpc.com/connect/v2"
	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
)

// githubGroupGrants is the reverse of an App's grants: which Apps each
// internal group may mint tokens of, from the catalogue alone.
//
// From the catalogue and nothing else, on purpose. A group's page is read
// far more often than the Apps list, and what it needs — the App, its
// repositories and the most a token may carry — is declared here; asking
// GitHub about every App to colour a chip would put a network call on
// every read of the policy.
func (c *Console) githubGroupGrants() map[string][]*directoryrosterv1.GitHubGroupGrant {
	out := map[string][]*directoryrosterv1.GitHubGroupGrant{}
	if c.deps.GitHubCatalogue == nil {
		return out
	}
	for i := range c.deps.GitHubCatalogue.Apps {
		app := &c.deps.GitHubCatalogue.Apps[i]
		for _, grant := range app.Grants {
			row := &directoryrosterv1.GitHubGroupGrant{
				AppId: app.ID, AppName: app.DisplayName(), Org: app.Org,
				Repositories: slices.Clone(grant.Repositories), Permissions: maps.Clone(grant.Permissions),
			}
			// When the grant was last used, where this service still
			// remembers. It comes from the ring beside it — no store is
			// read and no call is made to GitHub — so it stays as cheap as
			// the rest of a group's page, and it is absent rather than
			// "never" where nothing is remembered.
			if at, minted := c.deps.GitHubMints.LastMinted(app.ID, grant.Group); minted {
				row.LastMinted = timestampOf(at)
			}
			out[grant.Group] = append(out[grant.Group], row)
		}
	}
	return out
}

// githubBegin is what starting any connect returns: where the browser
// goes, the manifest it POSTs there when the App is still to be created,
// and the signed state both are pinned to.
type githubBegin struct{ url, manifest, state string }

// pinFlow pins a flow to this browser for as long as the two clicks have.
func (c *Console) pinFlow(header *connect.Header, state string) {
	header.Add("Set-Cookie", access.ConnectCookie(state, c.deps.SecureCookie, githubFlowWindow).String())
}
