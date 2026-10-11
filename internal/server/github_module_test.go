package server

import (
	"context"

	"github.com/truvity/sluis/internal/consolecall"
	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/modcall"
)

// localGitHub is the GitHub module, in this process, over whatever stores the
// console under test holds when a call is made: a test sets the console's
// dependencies after building it, and the module must see them.
type localGitHub struct{ c *Console }

func (l localGitHub) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	deps := ghconsole.Deps{
		Log:        l.c.deps.Log,
		Audit:      l.c.deps.Audit,
		Trigger:    l.c.deps.Trigger,
		PublicURL:  l.c.deps.PublicURL,
		RootURL:    l.c.deps.RootURL,
		Orgs:       l.c.deps.GitHubOrgs,
		OrgAppName: l.c.deps.GitHubOrgAppName,
		Catalogue:  l.c.deps.GitHubCatalogue,
		HTTP:       l.c.deps.GitHubHTTP,
	}
	if l.c.deps.Authorizer != nil {
		deps.Policy = l.c.deps.Authorizer.Policy
	}
	srv := modcall.NewServer(consolewire.Module)
	ghconsole.Register(srv, ghconsole.New(deps))
	return modcall.Local{consolewire.Module: srv}.As(consolecall.Class).Call(ctx, module, method, payload)
}

// withGitHubModule connects a console to the module in this process.
func withGitHubModule(c *Console) *Console {
	c.deps.Modules.GitHub = localGitHub{c}
	return c
}
